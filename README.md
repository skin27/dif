# DIF — Data Integration Framework

A minimal, dependency-free Go prototype of an integration framework built on
Flow-Based Programming, Enterprise Integration Patterns and DIL
(Data Integration Language). Background: [Integration Language Design](https://raymondmeester.medium.com/integration-language-design-da4cf51a05c0).

This MVP proves one architecture:

```text
DIL JSON  →  flow model  →  engine  →  steps  →  result Message
```

## Run

A flow is a long-running task: once started, it runs in the background until
you stop it (or `dif` exits). Flows run concurrently, each registered under its
DIL `flow.id`. Messages are sent to a running flow separately.

- `dif` opens the CLI without flows; add them with `run <flow.json>` (load and
  start), or `load <flow.json>` and later `start <flow>`.
- `dif start <flow.json>...` loads and starts the given flows, then opens the CLI.

You type commands after the `> ` prompt, and the program's answers are
indented below them. `<flow>` is a flow id.

| Command                  | Effect                                                                  |
|--------------------------|-------------------------------------------------------------------------|
| `load <flow.json>...`    | Register the flows in the files; they stay stopped until started        |
| `run <flow.json>...`     | Load the flows and start them, in one go                                |
| `send <flow>`            | Send the flow's configured message (`dil.core.messages`) into the flow  |
| `send <flow> <body>`     | Send the configured message with `<body>` as body                       |
| `start <flow>`           | Start the flow (again, after `stop`), or continue it after `pause`      |
| `pause <flow>`           | Pause: the flow takes no new messages until it is started or resumed    |
| `resume <flow>`          | Resume a paused flow                                                    |
| `stop <flow>`            | Stop the flow once the message it is processing is done; you stay in the CLI |
| `stop <flow> --force`    | Stop the flow at once; the message it is processing may be lost         |
| `log <flow>`             | Follow the flow's log live, starting with its last 10 lines (like `tail -f`); press Enter to stop |
| `log <flow> --lines <n>` | Show the last `n` lines of the flow's log                               |
| `list [state]`           | Table of the flows: id, state, startup time and uptime of the current run; filter on `started`, `paused` or `stopped` |
| `status`                 | Show the number of flows and the message counts                         |
| `help`                   | List all commands                                                       |
| `exit`                   | Stop all flows and exit `dif` (Ctrl+C does the same)                    |

### Flow logs

Flows work in the background and never write to the console. Each flow logs
to its own file, `logs/<flow id>.log` in the working directory (appended to
across runs):

- lifecycle events: loaded, started, paused, stopped (forced, or because `dif` exits)
- every message: its content and trail (and the error the error route handled, if any), or why it failed
- redeliveries of failing steps
- the lines of the flow's `log` steps

Read it with `log <flow>` or any other tool.

```text
$ go run ./cmd/dif
DIF CLI: no flows yet; add them with "load <flow.json>" or "run <flow.json>".
Type a command at the "> " prompt; "help" lists all commands.
> run examples/timer.json examples/hello.json
  flow timer started (loaded from examples/timer.json)
  flow hello started (loaded from examples/hello.json)
> send hello
  message sent to flow hello; its result is in the flow's log
> list
  ID      STATUS    STARTUP TIME          UPTIME
  hello   started   2026-10-02 13:37:40   10s
  timer   started   2026-10-02 13:37:40   10s
> log timer
  following logs/timer.log; press Enter to stop
  2026/10/02 13:37:40.104371 flow timer loaded from examples/timer.json
  2026/10/02 13:37:40.105789 flow timer started (loaded from examples/timer.json)
  2026/10/02 13:37:45.106427 step timer-log: traceid=c98d… headers={metadata.timestamp=…, source=timer} body=tick 1
  2026/10/02 13:37:45.106427 message 1: {"body":"tick 1",…} trail: source:timer-source -> action:timer-setbody -> action:timer-setheader -> sink:timer-log (0 ms)
  2026/10/02 13:37:50.107112 step timer-log: traceid=84cf… headers={metadata.timestamp=…, source=timer} body=tick 2
  2026/10/02 13:37:50.109383 message 2: {"body":"tick 2",…} trail: … (1 ms)

  stopped following logs/timer.log
> log hello --lines 1
  2026/10/02 13:37:40.106536 message 1: {"body":"HELLO WORLD","greeting":"hello",…} trail: source:hello-source -> action:hello-action -> sink:hello-sink (0 ms)
> stop timer --force
  flow timer stopped (forced)
> load examples/scheduler.json
  error: flow 68b70775aaa512000600033b: step 8943a4b2-…: no processor for "quartz" (source)
> exit
  exit: 3 messages processed, 0 failed
```

`examples/timer.json` runs timer → setbody → setheader → log: the timer
produces a message every 5 seconds by itself; `pause` holds it and `start` or
`resume` continues it.

A flow is only loaded when every step has a processor and valid options (see
[Steps](#steps)); otherwise `load`, `run` (or `dif start`) reports why and the
flow is not registered.

When stdin ends (for example `dif start flow.json < /dev/null`), `dif` keeps
running until Ctrl+C.

The exit code is 0 when every message succeeded, 1 when a message or a flow
failed (or a file given to `dif start` could not be loaded, or two have the same flow id),
and 2 for bad usage.

```bash
go vet ./...
go test ./...
go test -race ./...   # needs cgo and a C compiler
```

## Lifecycle

```text
Stopped --Start--> Started --Pause--> Paused --Start|Resume--> Started
Started | Paused --Stop|ForceStop--> Stopped
```

- `Start` runs the flow in the background. It processes messages one at a time,
  from its source or from `Send`, until `Stop` is called. A source that runs
  out of messages does not stop the flow.
- `Pause` stops the flow from taking new messages: `Send` is refused and the
  source waits. A message already in a step completes.
- `Start` or `Resume` continues a paused flow; `Stop` ends it and waits until it has finished.
  A message the flow has already taken completes; it is never stopped halfway.
- `ForceStop` (`stop <flow> --force`) does not wait for that message: the context
  it runs with is cancelled, so the engine abandons it before its next step
  (and processors that honour the context stop at once). The message is reported
  as failed (`aborted by forced stop`) and may be lost.
- Processors log to the flow's logger (`Runner.SetLogger`), which they get from
  their context; the CLI points it at the flow's log file.
- A failing message is reported and the flow continues with the next one; see
  [Error handling](#error-handling) for retries and error routes.
- A stopped flow can be started again. Starting a started flow, pausing a paused
  flow and so on are refused with an error such as `cannot start: flow is started`.

## Concurrency

The engine keeps a registry of flows by flow id (`engine.Engine`, a map guarded
by a `sync.RWMutex`). Every flow runs independently:

```text
Engine ── flow id → Runner
           ├── hello: 1 goroutine executing messages (one at a time)
           └── timer: 1 goroutine executing messages + 1 for its timer source
```

- A flow has at most one run: `Start` begins one only when the flow is
  stopped, so a flow never gets two execution goroutines.
- Each message is a standalone execution; it carries no lifecycle state.
- Lifecycle calls on one flow never block another: the registry lock is held
  only to find a flow, and each flow guards its own state.
- `Shutdown` stops every flow and waits until all have finished.
- The startup time is when the current run began: pausing and starting again
  keeps it, stopping clears it (`-` in `list`).

```go
e := api.NewEngine()
f, err := api.Load("examples/hello.json", func(res *api.Result, err error) { /* per message */ })
e.Add(f.Runner)                       // registered as "hello"
e.StartFlow("hello")
f.Send(f.NewMessage())
e.PauseFlow("hello")
e.StartFlow("hello")                  // continues the paused flow
e.ListFlows(api.Started)              // [{hello started <startup time>}]
e.Shutdown()
```

## Message

A message is one mutable map (`message.Message`, a `map[string]any`) holding:

- the body under the fixed key `body`
- user headers under any other key; values are strings, booleans, ints,
  `[]byte`, decoded JSON (maps, slices) or XML (as a string)
- metadata headers, prefixed `metadata.`: `metadata.traceid` and
  `metadata.timestamp` (RFC 3339). Metadata is internal: it is never sent
  outside a flow (the file sink writes the body only), and `setheader` cannot
  set it.

Keys are case-sensitive. A message is a plain map, so it is JSON-serializable
and can be persisted later; nothing is persisted now.

## Processors

Every step is executed by a processor (`steps/definition`). The DIL step type
decides which contract it needs:

```text
Processor
    ├── SourceProcessor  Run(ctx, emit)        produces messages and injects them into the flow
    ├── ActionProcessor  Process(ctx, m) (m)   modifies or inspects a message, passes it on
    ├── RouterProcessor  Route(ctx, m) routes  picks the outbound links that get the message (or copies)
    └── SinkProcessor    Consume(ctx, m)       consumes a message, normally ending its path
```

- A source is not executed per message: it runs for the whole run of the flow
  and emits a message per tick, file, request, … A request-reply source (https)
  passes a reply callback to `emit`; the flow calls it with the final message or
  the error once the message has been processed.
- Processors return errors to the engine and never retry; error handling
  (redelivery, error routes) is the engine's job. They honour `ctx`. A message the flow has taken completes even
  when the flow is stopped meanwhile, unless the stop is forced. They log with
  `stepdef.Logger(ctx)`, the flow's logger.
- Processor instances are shared by all messages of a flow and are safe for
  concurrent use (today a flow still processes one message at a time).
- An action position may use a sink processor (the message passes on unchanged
  after it is consumed, like Camel's `file-action`) or a router processor (one
  that passes the message on or stops it, such as `filter`); a sink position
  may use an action processor.

### Routing

A router returns routes: each sends a message to one of its outbound links. The
engine runs them in order, each path to its end, one after another (no
goroutines). Then:

- The message that comes out of the **last route** is the router's outcome, and
  so the flow's: what a request-reply source replies with.
- A **detached** route (the wire tap) never changes or fails the message; if it
  fails, the error goes to the flow's log.
- **No routes** ends the message at the router (a filter that does not pass);
  the outcome is the message as it entered the router.
- An error on any other route fails the message, and later routes do not run.
- A router that sends a message along several links sends copies
  (`Message.Copy`), so branches never see each other's changes.
- A router that is also a **gatherer** (`stepdef.Gatherer`, scatter-gather,
  such as `enrich`) gets the outcomes of its routes instead: the message or
  the error of each route that is not detached. It combines them and returns
  the routes to run next, which run as above. A failed route does not stop the
  message; the gatherer decides (returning the route's error keeps its step
  and message for the error route).

The trail lists the steps in the order they ran, branch after branch:
`source:a -> router:r -> sink:tap -> sink:main`.

### Error handling

A flow may have an error handler (in DIL, its `error` step, `failedexchange`).
It works like Camel's dead letter channel:

1. **Redelivery.** A step that fails is tried again, up to
   `maximumRedeliveries` times (default 0), `redeliveryDelay` ms apart
   (default 1000). Each try is logged:
   `step x: redelivery 1 of 2 in 200ms after: <error>`. Only the failing step
   runs again, with the message as it got it (and any change it made before
   failing). A forced stop ends the wait.
2. **Error route.** If the step keeps failing and the error step has an
   outbound link, the message goes along that route, with the headers
   `error.message` (what went wrong) and `error.step` (the step's id). The
   error is then handled: the message counts as processed, the error route's
   outcome is what an https source replies (200), and the trail shows
   `error:<id>` before the error route's steps. The flow log adds
   `(error route handled: <error>)`.
3. Without an error route, or when the error route fails too, the message
   fails (`step x: …; error route: step y: …`).

A failure on a branch of a router goes to the error route with that branch's
message; a detached route (wire tap) never does. A forced stop never takes the
error route.

## Steps

The scheme of a step's `uri` selects its processor in the registry
(`steps/registry`): `file:/data/in` is the step `file` with `path` `/data/in`.
Each step has a JSON Schema for its `options` (`steps/impl/schemas/<name>-<kind>.json`,
modelled on the Kamelet properties). When a flow is loaded, every step is
checked:

- a step without a registered processor rejects the flow:
  `no processor for "sftp" (source)`
- options are validated against the schema; defaults are applied and, because
  DIL converted from XML stores numbers and booleans as strings, `"5"` and
  `"true"` are accepted for integers and booleans. Unknown options are errors.
  All problems are reported at once: `step t1: timer: option period: want integer, got "x"; unknown option numbers`

The schema validator supports a small JSON Schema subset (`type`, `properties`,
`required`, `additionalProperties`, `enum`, `default`, `minimum`); a schema
using anything else fails registration.

| Step | Kind | Options (default) | Behavior |
|---|---|---|---|
| `timer:<name>` | source | `period` ms (1000), `repeatCount` (0 = unlimited) | Emits the counter 1, 2, 3… as body every period |
| `file:<dir>` | source | `fileName` (all files), `charset` utf-8, `autoCreate` (true), `recursive` (false), `delete` (false), `initialDelay` ms (1000), `delay` ms (500) | Polls the directory; body is the file content, header `file.name` its path relative to the directory. Consumed files are deleted or moved to `<dir>/.done`. Names starting with a dot are skipped |
| `file:<dir>` | sink | `fileName` (header `file.name`, else the trace id), `charset` utf-8, `autoCreate` (true), `fileExist` Override\|Append\|Fail\|Ignore (Override) | Writes the body to the file |
| `log` | action | `showHeaders` (false), `showBody` (false), `showException` (no effect yet) | Logs `step <id>: traceid=… headers={…} body=…` to the flow's log |
| `setbody` | action | `language` constant\|simple (constant), `expression` ("") | Sets the body |
| `setheader` | action | `name` (required), `language` constant\|simple (simple), `value` ("") | Sets one header; not `body` or `metadata.*` |
| `passthrough` | action | – | Passes the message on unchanged |
| `message:<name>` | source | – | Produces nothing; messages are sent to the flow (`send`) |
| `https://<host>:<port>/<path>` | source | `matchPrefix` (false), `preserveHttpHeaders` (false), `serverIdentityFile` (`security/server-identity.p12`), `serverIdentityPassword` | Receives HTTPS requests and replies with the flow's outcome, see [HTTPS](#https) |
| `https://<host>[:<port>]/<path>` | action | `httpMethod` GET\|POST\|PUT\|PATCH\|DELETE\|HEAD (GET), `trustStoreFile` (`security/outbound-truststore.p12`), `trustStorePassword`, `socketTimeout` ms (30000), `throwExceptionOnFailure` (false) | Calls the endpoint; the response becomes the message, see [HTTPS](#https) |
| `setheaders:message:<name>` | action | – | Sets all headers of the core message `<name>` (`dil.core.messages`); each header's `language` is constant or simple (default) |
| `base64totext` | action | – | Decodes a base64 body to text (whitespace ignored, padding optional) |
| `texttobase64` | action | – | Encodes the body as base64, without line breaks |
| `repeater[:<name>]` | source | `period` ms (10000), `repeatCount` (0 = unlimited) | The timer source with Camel's repeater defaults |
| `removeheaders` | action | `pattern` (required), `excludePattern` ("") | Removes the headers matching `pattern` but not `excludePattern`: an exact name, a prefix ending with `*` or a regular expression, case-insensitive. Never removes the body or `metadata.*` |
| `replace` | action | `regex` (required), `replaceWith` (""), `flags` (`i`, `m`, `s`, comma-separated), `group` (0) | Replaces every match in the body; `$1` in `replaceWith` inserts a group. With `group` > 0 only that group of each match is replaced |
| `simplereplace` | action | – | Evaluates the body as a simple expression: `${header.<name>}` in the body becomes the header's value |
| `zip` | action | – | Zips the body as one file named after `file.name` (else the trace id); sets `file.name` to `<name>.zip` and `Content-Type: application/zip` |
| `unzip` | action | – | Extracts the one file of a zip body; `file.name` becomes its name. An archive with several files fails the message (that needs a splitter) |
| `throttle` | action | `maxRequests` (required), `timePeriod` ms (1000) | Lets at most `maxRequests` messages pass per `timePeriod` (sliding window); the others wait |
| `encoder` | action | `originCharset` (UTF-8), `targetCharset` (UTF-8) | Converts the body between UTF-8, ISO-8859-1 and US-ASCII; characters the target cannot hold become `?` |
| `wiretap` | router | – | Sends a copy to the link with rule `wiretap` (detached), then the message along the other link |
| `recipient` | router | – | Sends a copy to every link, in order; the outcome is the last one's |
| `content` | router | – (conditions are on the links) | Sends the message along the first link whose condition (`language`, `expression`) holds, else along the link without a condition; with none, the message stops |
| `filter` | action | `language` simple\|xpath\|jsonpath (simple), `expression` (required) | Passes the message on when the condition holds, else stops it |
| `split` | router or action | `language` xpath\|jsonpath (xpath), `expression` (required); `streaming`, `parallelProcessing`, `exchangePattern` (no effect yet) | Sends each part of the body along the link with rule `split`, with headers `split.index`, `split.size` and `split.complete`; then the message itself along the other link, if any. XML parts are the elements as written; JSON parts are JSON (strings as is) |
| `enrich` | router | `enrichType` override\|xml\|json (xml), `useErrorRoute` (true), `attachmentName` (no effect) | Content enricher: sends a copy along the link with rule `enrich`, merges what comes out into the message and sends that along the other link. `override`: the enrichment (body and headers) replaces the message; `xml`: its root element is appended inside the body's root element; `json`: its members are set in the body's object (the message keeps its headers). When the enrichment fails, the message fails with that error (so the flow's error route can take it), or with `useErrorRoute` false continues without it and the error is logged |
| `aggregate` | action | `aggregateType` xml\|text/xml\|application/xml\|json\|application/json (xml), `completionSize` (0); `completionTimeout`, `completionInterval` (must be 0: not supported yet) | Collects messages and passes one on when the group is complete: the last part of a split (`split.complete`) or `completionSize` messages. That message goes on with the aggregate as body and without the split headers; the others stop here. One group at a time (the Kamelet correlates all messages); a new split (`split.index` 0) starts a new group |
| `splitandaggregate` | router | as `split` (`expression` may be on the split link instead), and `aggregateType` | Splits the body, sends each part along the link with rule `split`, aggregates what comes out (a gatherer) and sends the message with the aggregate along the other link. A failed part fails the message |

Aggregates are, for XML, the parts' root elements in `<Aggregated>…</Aggregated>`
and, for JSON, an array of the parts.

Language `constant` is the literal text; `simple` replaces `${body}` (also
written `${bodyAs(String)}`), `${header.<name>}` and `${headers.<name>}` (other
`${…}` expressions are rejected when the flow is loaded).

Conditions (`content`, `filter`) and split expressions use small subsets, built
on the standard library; anything else is rejected when the flow is loaded:

| Language | Supported | Condition holds when |
|---|---|---|
| `simple` | `<expr> == <value>`, `!=`, `contains`; a value is `'quoted'`, a number or an expression. Without an operator, the expression must be `true`. No `&&` / `\|\|` | the comparison holds |
| `xpath` | absolute paths of element names, `*` for any: `/persons/person`; namespace prefixes are ignored. As a condition also `<path> = 'literal'` and `!=` | the path selects an element (whose text equals the literal) |
| `jsonpath` | `$` with `.name`, `['name']`, `[n]` (negative from the end), `.*`, `[*]` | the path selects a value other than `null` or `false` |

A body that is not XML or JSON matches no xpath or jsonpath condition; a split
of such a body fails the message.

### Converters

The converters turn the body from one format into another; the result is text.
A body that is not the input format fails the message. They follow the
libraries the DIL components were built on:

| Step | Options (default) | Mapping |
|---|---|---|
| `xmltojson` | `forceTopLevelObject`, `skipWhitespace`, `trimSpaces`, `skipNamespaces`, `removeNamespacePrefixes`, `typeHints` (all false) | json-lib (Camel's xmljson): attributes as `"@name"`, text beside attributes or children as `"#text"`, repeated elements as an array, an element whose two or more children share one name as an array of their values, an empty element as `""`. The root is left out unless `forceTopLevelObject`. All values are strings; with `typeHints`, a `json_type` attribute (`number`, `boolean`, `string`, `null`, `array`, `object`) sets the type |
| `jsontoxml` | `rootName` (o), `arrayName` (a), `elementName` (e), `typeHints` (false), `namespaceLenient` (no effect) | The reverse: members as elements, `"@name"` as attributes, `"#text"` as text, array items as `elementName` elements; starts with an XML declaration. `typeHints` adds `json_type` to every element, so `xmltojson` can restore the JSON exactly |
| `xmltojsonsimple` | `keepStrings`, `removeNamespaces`, `removeRoot`, `hasTypes` (false), `typeValueMismatch` NULL\|ORIGINAL (ORIGINAL) | org.json: `{"root": …}` unless `removeRoot`, attributes and children by name, text beside them as `"content"`, repeated elements as an array, trimmed text. Numbers, `true`, `false` and `null` become JSON values unless `keepStrings`. With `hasTypes` a `type` attribute (`string`, `number`, `integer`, `double`, `boolean`, `null`) sets the type; text that does not fit becomes `null` or stays a string |
| `jsontoxmlsimple` | `addRoot` (false), `rootTag` (root), `changeArrayElements` (false), `arrayElementName` (element), `checkJsonKeys` (false) | The reverse: members as elements, `"content"` as text, an array as one element per item named after its key (with `changeArrayElements`: one element holding `arrayElementName` items), `null` as the text `null`, no declaration. A key that is not an XML name fails the message with `checkJsonKeys`, else its invalid characters become `_` |
| `csvtoxml` | `delimiter` (,), `useHeader` (false), `encoding` (UTF-8) | `<rows><row><name>value</name>…</row>…</rows>`; with `useHeader` the first record names the fields (invalid characters become `_`), else `field1`, `field2`, … `encoding` only sets the XML declaration; the `encoder` step converts the bytes |
| `xmltocsv` | `includeHeader`, `includeIndexColumn` (false), `indexColumnName` (line), `delimiter` (,), `lineSeparator` linefeed\|carriage_return\|carriage_return_linefeed, `orderHeaders` unordered\|ordered, `quoteFields` all_fields\|non_empty_fields\|no_fields (no_fields) | Every child of the root is a record, every child of a record a field (trimmed text); a record without children is one field. Columns in order of appearance or (`ordered`) alphabetical. A field holding the delimiter, a quote or a line break is always quoted |

The Kamelets only pass these options on to Assimbly's components, so where a
detail is not defined by json-lib or org.json (the CSV element names, the
`hasTypes` type names, `checkJsonKeys`), DIF's choice is the one above.

New steps plug in without touching the engine:

```go
api.RegisterStep(api.StepDefinition{
	Name:   "upper",
	Kind:   "action",
	Schema: []byte(`{"type": "object", "additionalProperties": false}`),
	New:    func(stepID string, p stepdef.Params) (stepdef.Processor, error) { return upper{}, nil },
})
```

### HTTPS

The `https` source makes a flow an HTTPS endpoint, request-reply: a request
becomes a message (body = request body; request headers = message headers, plus
`http.method`, `http.path`, `http.query` and `http.uri` with
`preserveHttpHeaders`), and the caller gets the final message body back, with
its `Content-Type` header (default `text/plain; charset=utf-8`).

| Outcome | Response |
|---|---|
| message processed | 200 with the final body |
| message failed, handled by the error route | 200 with the error route's final body |
| message failed | 500 with the error |
| flow stopping or stopped | 503 |
| no flow serves the path | 404 |

A paused flow holds requests until it is resumed. Flows on the same host:port
share one listener, each on its own path (`matchPrefix` also serves the paths
below it); a second flow on a path already served fails to start, and its log
says why (`source stopped: path … is already served by another flow`).

The `https` action calls an endpoint with the message: the body (not for GET and
HEAD) and its string headers, never `metadata.*` or `http.*`. The response sets
the body, `http.status` and `Content-Type`. An error status fails the message
only with `throwExceptionOnFailure`.

TLS uses PKCS#12 keystores (`.p12`), read by DIF's own reader in `keystore`
(no dependencies). It supports what Java keytool (JDK 18+) and OpenSSL 3 write
by default: PBES2 with PBKDF2 and AES; legacy 3DES/RC2 keystores are rejected.

| Keystore | Used by | Default file | Password |
|---|---|---|---|
| server identity: private key + certificate | https source | `security/server-identity.p12` | option `serverIdentityPassword`, else `DIF_SERVER_IDENTITY_PASSWORD` |
| trust store: certificates to trust | https action | `security/outbound-truststore.p12` | option `trustStorePassword`, else `DIF_TRUSTSTORE_PASSWORD` |

Paths are relative to the working directory. Keystores are read when the flow
is loaded, so a missing file or a wrong password rejects the flow. The action
trusts only the trust store's certificates.

```text
$ DIF_SERVER_IDENTITY_PASSWORD=… DIF_TRUSTSTORE_PASSWORD=… go run ./cmd/dif
> run examples/httpsInbound.json examples/httpsClient.json
$ curl -k -d hello https://localhost:9001/_new2/httpsinbound
12345
```

`httpsClient` posts to `httpsInbound` every 5 seconds and logs its reply.

### Examples that load

34 of the examples load (given the keystores): aggregate, base64ToText,
contentrouter, csvtoxml, encoder, enrich, errorHandler, fileInbound,
fileOutbound, filter, hello, httpsClient, httpsInbound, jsontoxml,
jsontoxmlsimple, log, queueAsynchronousOutbound, recipient, removeHeaders,
repeater, replace, setBody, simplereplace, split, splitAndAggregate, test,
textToBase64, timer, unzip, wiretap, xmltocsv, xmltojson, xmltojsonsimple and
zip. The others use steps without a processor yet (such as the dead letter
queue in deadletter), or expressions such as `groovy` and `${date:now:ss}`. Several https examples
listen on the same path (`/_new2/httpsinbound`), so only one of them can run
at a time.


## Packages

| Package            | Role                                                                     |
|--------------------|--------------------------------------------------------------------------|
| `message`          | `Message`: one map with the body, headers and `metadata.*` headers        |
| `steps/definition` | Processor contracts (`SourceProcessor`, `ActionProcessor`, `RouterProcessor` with `Route` and `Link`, `Gatherer` with `Outcome`, `SinkProcessor`) and `Definition` |
| `steps/registry`   | Processor registry by URI scheme and kind; JSON Schema validation of step options; gives routers their links |
| `steps/impl`       | Built-in steps (timer, repeater, file, https, log, setbody, setheader, setheaders, removeheaders, replace, simplereplace, base64totext, texttobase64, zip, unzip, throttle, encoder, passthrough, message, the converters xmltojson, jsontoxml, xmltojsonsimple, jsontoxmlsimple, csvtoxml, xmltocsv, and the routers wiretap, recipient, content, filter, split, enrich, aggregate, splitandaggregate) and their schemas; the simple, xpath and jsonpath subsets |
| `keystore`         | Reads PKCS#12 keystores: server identity and trust store                 |
| `flows/definition` | Internal flow model (`Flow`, `Node`, `ErrorHandler`), independent of any DSL |
| `flows/impl`       | Parses DIL JSON, validates links, builds the flow model                  |
| `engine`           | `Run` takes one message through a flow, with redelivery and the error route; `Runner` holds the lifecycle; `Engine` is the registry of flows by id |
| `api`              | Public entry point: `api.Load(path, onResult)` returns a `Flow` with lifecycle methods and `Send`; `api.NewEngine()` manages several flows; `api.RegisterStep` adds steps |
| `cli`, `cmd/dif`   | `dif` / `dif start <flow.json>...` with `load`, `run`, `send`, `log`, `list` and lifecycle commands on stdin; one log file per flow |

The engine depends only on the flow model and the processor interfaces. New
steps plug in through the registry without touching the engine.

## How DIL maps to the model

- Steps are connected by link ids: a step's `bound: "out"` link id matches the
  `bound: "in"` link id of the next step.
- Because DIL JSON is converted from XML, a single child (`flow`, `link`, …)
  may be an object instead of an array; both are accepted.
- A step's `uri` scheme selects its processor and its `options` are validated
  against the processor's schema.
- A step URI `<step>:message:<name>` (such as `setheaders`) refers to the core
  message `<name>`; the parser passes its headers to the step as the option `headers`.
- A `router` step has one or more outbound links. The attributes of an
  outbound link, `rule` (its role, such as `wiretap` or `split`), `language`
  and `expression` (its condition), are kept in the flow model (`Node.Links`)
  and given to the router; `pattern` is ignored.
- The `error` step (`failedexchange`, at most one per flow) becomes the flow's
  error handler: its options `maximumRedeliveries` and `redeliveryDelay` (or
  `redeliveryAttempts` and `redeliveryInterval`, used when the first are
  absent) set the redelivery, and its outbound link, if any, starts the error
  route. It has no inbound link.

## Future work

- More sources and steps (queue, quartz, sftp, xslt, EDI and Excel converters, …);
  multiple flows per file
- Aggregation by time (`completionTimeout`, `completionInterval`) and by
  correlation key; it needs a timer that emits into the flow
- More expression languages and simple-language functions (`${date:now:<format>}`, …)
- More error handling: exponential backoff, retrying only some errors, keeping the original message
- Concurrent message execution within a flow (processors are already safe for it)
- A separate engine process with a network API for clients
- State: `Message` is JSON-serializable, so it can be persisted later; nothing is persisted now
