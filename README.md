# DIF — Data Integration Framework

A minimal Go prototype of an integration framework (the standard library only,
apart from the few libraries that AGENTS.md lists) built on
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

Start the shell by running `dif.exe` without arguments (`.\dif.exe` in
PowerShell). `dif help`, `dif --help` and `dif -h` show startup instructions.
Interactive lifecycle commands are entered at the `>` prompt. For containers,
use the foreground service command described below.

Flow filenames default to `.json` in `load`, `run`, `init`, `validate` and
`describe`: for example, `load test88` loads `test88.json`, and `init hello`
creates `hello.json`. Explicit extensions are preserved. This also works for
directory paths and multiple filenames; quote paths containing spaces.

```text
PS> .\dif.exe
> init hello.json
> validate hello.json
> describe hello.json
> run hello.json
> request hello
> exit
```

### Foreground service

```sh
dif run --file=/etc/dif/orders.json
dif run --dir=/etc/dif/flows --monitor-address=0.0.0.0:9090
dif run --url=https://gist.githubusercontent.com/USER/ID/raw/REVISION/flow.json
dif run --config=/etc/dif/service.json
dif validate --dir=/etc/dif/flows --output=json
dif describe /etc/dif/orders.json
dif version --output=json
```

`run` requires no stdin or TTY. It loads, validates and builds the complete
flow group, initializes internal consumers before producers, and activates
message processing only after all sources have started successfully. It runs
until SIGINT/SIGTERM (Ctrl+C on Windows) or fatal source/monitor failure.
Starting a flow activates its source; it does not inject a configured message.
A `message` source therefore remains idle, and a finite timer completing does
not terminate the service. Batch/job completion is not implemented.

`--file`, `--dir` and `--url` are repeatable and may be combined. Directories
load visible `*.json` files in sorted order, without recursion; projected
ConfigMap file symlinks are supported. Missing/empty inputs, duplicate flow IDs,
invalid definitions and missing local channel consumers fail startup. Local
producer targets include queues, flow links, topics and dead-letter queues.
An initialization failure cancels already-started sources before exit. Loading
is not a transaction over external resources: constructors may read keystores,
and a file source may create its configured directory.

URLs must identify raw JSON over HTTPS, without userinfo or fragments. Fetches
have a 10s default timeout, a 4 MiB document limit and at most five requests in
a redirect chain; redirects must stay on the same HTTPS host. Optional
`--sha256=<hex>` verifies a single remote input. Definitions are fetched once;
there is no automatic refresh. Prefer mounted, versioned files for production.
All relative paths retain their existing working-directory semantics.

Service configuration is strict JSON, separate from DIL; see
[deploy/service.json](deploy/service.json). Precedence is flags, environment,
configuration, defaults. `DIF_FILES`, `DIF_DIRS`, and `DIF_URLS` are JSON string
arrays. Repeated input flags replace the corresponding environment/config list
on their first occurrence. Scalar overrides are `DIF_MONITOR_ADDRESS`,
`DIF_STARTUP_TIMEOUT`, `DIF_SHUTDOWN_TIMEOUT`, `DIF_FETCH_TIMEOUT`,
`DIF_MAX_BYTES`, `DIF_LOG_FORMAT`, and `DIF_SHA256`; `DIF_CONFIG` selects a
configuration file. `dif run --help` lists defaults. Config files are limited
to 4 MiB; flow document limits can be configured up to 64 MiB.

Service logs go to stdout as JSON by default (`--log-format=text` is available).
No automatic result payload logging or local log files are enabled. Explicit
`log`/`logger` steps still log what their flow options request. Runtime failures
are logged per flow; individual message failures do not terminate the service.
Exit codes are 0 for orderly termination, 1 for startup/runtime/drain failure,
and 2 for invalid arguments or service configuration.

The optional monitoring listener provides `/livez`, `/readyz`, `/startupz`,
`/status` and `/metrics`. It is read-only and unauthenticated; restrict network
access. Readiness becomes false during shutdown, external producers stop, and
internal consumers stay running until accepted work drains. The default 25s
shutdown budget includes source cleanup and HTTP draining. At the deadline,
remaining work is cancelled and the executable exits nonzero. Queues are
in-memory and cannot promise delivery across crashes or forced termination.

`validate` performs static structure/schema validation with the same input
resolver, without constructing processors, reading keystores or binding ports.
`run` additionally checks processor semantics, local channel consumers and
source startup. Top-level `describe`, `catalog`, `init`, `version` and
`--version` are also available; existing shell commands retain their behavior.

See [container and Kubernetes deployment](deploy/README.md) for the image,
probes, secrets, termination budgets and replica-safety limitations.
Existing password environment variables also support `_FILE` companions for
mounted secrets; no credential values need to be placed in command arguments.

### Interactive commands

You type commands after the `> ` prompt; the answers follow below them, tables
between blank lines. `<flow>` is a flow id. `help` lists the commands by group
and `help <command>` explains one. Mistakes are reported as `Error: ...` with
a hint, such as the usage of the command or `Did you mean: timer?` for a
mistyped flow id. On a terminal that supports it, flow states are colored
(started green, paused yellow, stopped gray); set `NO_COLOR` to turn that off.

| Command                  | Effect                                                                  |
|--------------------------|-------------------------------------------------------------------------|
| `load <flow.json>...`    | Register the flows in the files; they stay stopped until started        |
| `run <flow.json>...`     | Load the flows and start them, in one go                                |
| `send <flow>`            | Send the flow's configured message (`dil.core.messages`) into the flow  |
| `send <flow> <body>`     | Send the configured message with `<body>` as body                       |
| `request <flow> [body]`  | Send as `send` does and show the reply at once: the message the flow ends with (or has at a `setoneway` step), as JSON; waits at most 30 s |
| `start <flow>`           | Start the flow (again, after `stop`), or continue it after `pause`      |
| `pause <flow>`           | Pause: the flow takes no new messages until it is started or resumed    |
| `resume <flow>`          | Resume a paused flow                                                    |
| `stop <flow>`            | Stop the flow once the message it is processing is done; you stay in the CLI |
| `stop <flow> --force`    | Stop the flow at once; the message it is processing may be lost         |
| `log <flow>`             | Follow the flow's log live, starting with its last 10 lines (like `tail -f`); press Enter to stop |
| `log <flow> --lines <n>` | Show the last `n` lines of the flow's log                               |
| `list [state]`           | Table of the flows: id, state, completed and failed messages, and uptime of the current run; filter on `started`, `paused` or `stopped` |
| `ps [state]`             | Same as `list`                                                          |
| `stats`                  | Completed, failed and total messages per flow, with a total row         |
| `stats <flow>`           | State, message counts, startup time and uptime of one flow              |
| `status`                 | Show the number of flows and the message counts on one line             |
| `catalog`                | List the steps flows can use: name, type (source, action, router, sink), the Enterprise Integration Pattern it implements and description |
| `catalog <step>`         | Describe a step, its pattern and its options (type, default, required), from its schema |
| `init <flow.json> [--template hello\|timer\|file\|http] [--id name]` | Create a starter flow; never overwrite an existing file |
| `validate <flow.json>... [--output text\|json]` | Check structure, graph, references and step option schemas; reject duplicate flow ids across input files |
| `describe <flow.json> [--output text\|json]` | Show steps, links, error handling and configuration requirements, with values omitted |
| `version [--output text\|json]` | Show DIF version, revision, Go version and platform |
| `completion [command line]` | Suggest commands, flags, template names, output formats, catalog steps or file paths; for example `completion val` or `completion init --template t` |
| `help [command]`         | List all commands, or explain one                                       |
| `exit`                   | Stop all flows and exit `dif` (Ctrl+C does the same)                    |

`init` defaults to the `hello` template and uses the filename without its
extension as the flow id unless `--id` is given. The `file` template watches
`inbox` and moves consumed files to `inbox/.done`. The `http` template serves
HTTPS at `https://127.0.0.1:9002/hello` and needs a server identity in
`security/server-identity.p12` and `DIF_SERVER_IDENTITY_PASSWORD` at runtime.

`validate`, `describe`, `catalog` and `version` accept `--output json` inside
the shell. Options may precede or follow filenames; quote paths containing
spaces. Validation and description do not construct processors, read keystores
or start sources. They check structure and option schemas; processor-specific
semantics, expression syntax, resource availability and external connectivity
are checked when loading or running the flow. Description omits option values,
URI paths and expressions. Its configuration list identifies required schema
options and documented environment fallbacks.

`completion` displays suggestions without executing a command. Use `help
completion` for examples. It does not install operating-system completion
scripts or change shell profiles.

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
DIF - Data Integration Framework
Version: 0.1.0

No flows loaded.
Use 'help' for available commands.

> run testdata/timer.json testdata/hello.json
flow timer started (loaded from testdata/timer.json)
flow hello started (loaded from testdata/hello.json)
> send hello
message sent to flow hello; its result is in the flow's log
> list

FLOWS

ID      STATUS      COMPLETED   FAILED   UPTIME
───────────────────────────────────────────────
hello   ● STARTED           1        0   10s
timer   ● STARTED           2        0   10s

2 flows

> stats

DIF MESSAGE STATISTICS

FLOW    COMPLETED   FAILED   TOTAL
──────────────────────────────────
hello           1        0       1
timer           2        0       2
──────────────────────────────────
TOTAL           3        0       3

> log timer
following logs/timer.log; press Enter to stop
2026/10/02 13:37:40.104371 flow timer loaded from testdata/timer.json
2026/10/02 13:37:40.105789 flow timer started (loaded from testdata/timer.json)
2026/10/02 13:37:45.106427 step timer-log: traceid=c98d… headers={metadata.step=timer-log, metadata.timestamp=…, metadata.trail=flow:timer source:timer-source action:timer-setbody action:timer-setheader sink:timer-log, source=timer} body=tick 1
2026/10/02 13:37:45.106427 message 1: {"body":"tick 1",…} trail: source:timer-source -> action:timer-setbody -> action:timer-setheader -> sink:timer-log (0 ms)
2026/10/02 13:37:50.107112 step timer-log: traceid=84cf… headers={metadata.step=timer-log, …, source=timer} body=tick 2
2026/10/02 13:37:50.109383 message 2: {"body":"tick 2",…} trail: … (1 ms)

stopped following logs/timer.log
> log hello --lines 1
2026/10/02 13:37:40.106536 message 1: {"body":"HELLO WORLD","greeting":"hello",…} trail: source:hello-source -> action:hello-action -> sink:hello-sink (0 ms)
> stop timer --force
flow timer stopped (forced)
> pause helo
Error: flow 'helo' not found

Did you mean: hello?
Use 'list' to see loaded flows.
> catalog timer

STEP: timer
TYPE: SOURCE

Produces a message on every tick. The body is the tick counter (1, 2, 3, ...).

Options:
  NAME          TYPE      DEFAULT   REQUIRED   DESCRIPTION
  ───────────────────────────────────────────────────────────────────────────────────────────────────────────────
  path          string    -         no         Name of the timer, from the URI (timer:<name>); informational only
  period        integer   1000      no         Milliseconds between two ticks
  repeatCount   integer   0         no         Number of messages to produce; 0 or less (as -1) means unlimited

> load testdata/examples/scheduler.json
Error: flow 68b70775aaa512000600033b: step 8943a4b2-…: no processor for "quartz" (source)
> exit
exit: 3 messages processed, 0 failed
```

`testdata/timer.json` runs timer → setbody → setheader → log: the timer
produces a message every 5 seconds by itself; `pause` holds it and `start` or
`resume` continues it.

A flow is only loaded when every step has a processor and valid options (see
[Steps](#steps)); otherwise `load` or `run` reports why and the
flow is not registered.

When stdin ends, `dif` keeps
running until Ctrl+C.

The exit code is 0 when every message succeeded, 1 when a message or a flow
failed during execution or shutdown,
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
- Every flow counts its completed and failed messages (a message the error
  route handled counts as completed); stopping and starting keeps the counts.
  `list`, `stats` and `status` show them.

```go
e := api.NewEngine()
f, err := api.Load("testdata/hello.json", func(res *api.Result, err error) { /* per message */ })
e.Add(f.Runner)                       // registered as "hello"
e.StartFlow("hello")
f.Send(f.NewMessage())                // one-way: the result goes to the callback
reply, err := f.Request(ctx, f.NewMessage()) // request-reply: call the flow like a function
e.PauseFlow("hello")
e.StartFlow("hello")                  // continues the paused flow
e.ListFlows(api.Started)              // [{hello started <startup time> 1 0}]: id, state, start, completed, failed
e.Shutdown()
```

## Message

A message is one mutable map (`message.Message`, a `map[string]any`) holding:

- the body under the fixed key `body`
- user headers under any other key; values are strings, booleans, ints,
  `[]byte`, decoded JSON (maps, slices) or XML (as a string). `Content-Type`
  holds the media type of the body: the converters, the file source, `zip`,
  `unzip` and https set it, https sends it, and `setheader` can change it
- metadata headers, prefixed `metadata.`. Metadata is internal and `setheader`
  cannot set it. HTTPS/REST explicitly maps the trace ID to `DIF-Trace-Id`;
  other metadata is not exported (the file sink writes the body only).

### Message identity

| Header | Meaning |
|--------|---------|
| `Message-Id` | Identity of the logical message; generated as 32 random hex characters |
| `Correlation-Id` | Conversation or business-process identity; defaults to the root message's ID |
| `Causation-Id` | Immediate parent message's ID; absent on a new root message |

These are ordinary, case-sensitive message headers, available through
`message.MessageID`, `message.CorrelationID`, and `message.CausationID`.
For example, `setheader` can set `Correlation-Id` to an order number.
The independent `metadata.traceid` connects execution for diagnostics.

`message.New` creates fresh message and trace IDs. Engine entry initializes
missing, empty or non-string message, correlation and trace IDs, preserving
existing non-empty strings. `Copy`, routing, retries, dead-letter queues and
flow links preserve identity. Transformations and enrichment also retain the
current identity. These IDs do not by themselves provide deduplication.

Split and split-and-aggregate create children with new message IDs and
timestamps, the parent's ID as causation, and inherited correlation and trace
IDs. Nested splits reference their immediate parent. Custom processors can
use `m.Child(body)`; like `Copy`, it shares nested values, which must not be
mutated in place. Split-and-aggregate resumes the original message after
gathering; standalone aggregation releases a child of the group's last message.

HTTPS/REST requests and successful replies carry the three identity headers
and `DIF-Trace-Id`. Sources preserve supplied IDs and initialize missing ones;
HTTP header names are canonicalized to the spellings above. The trace header
is imported into `metadata.traceid`, not kept as a separate ordinary header.
Outbound trace identity comes from that metadata, overriding a stale ordinary
`DIF-Trace-Id` header. This is a DIF mapping, not W3C `traceparent` support.
HTTP actions retain their current message identity when updating the body
from a response; response identity headers do not replace it. Invalid HTTP
header values are not exported. Generated HTTP error responses have no message
identity headers. Other transports keep their existing serialization behavior.

| Metadata header | Set by | Value |
|-----------------|--------|-------|
| `metadata.traceid` | a new message, engine entry or HTTP import | generated as 32 hex characters; supplied IDs are preserved; copies and children keep it |
| `metadata.timestamp` | a new message | when it was created (RFC 3339) |
| `metadata.trail` | the engine | the steps the message entered, as `kind:id` separated by spaces, across flows: entering a flow adds `flow:id` (e.g. `flow:orders source:in action:check error:h sink:dlq flow:retry source:q`) |
| `metadata.step` | the engine | the id of the step the message is in, or was last in |
| `metadata.exchangepattern` | `setoneway` (`InOnly`), `setrequestreply` (`InOut`) | the exchange pattern of the message in its current flow; see [Exchange patterns](#exchange-patterns). Reset when the message enters a flow |
| `metadata.originalbody` | the engine | the body as the message entered its current flow; `setbody` with simple `${header.metadata.originalbody}` restores it. The log step and the CLI leave it out |

So a message carries where it has been: one taken from a dead letter queue
still shows the flow and step it failed in. Its trail is its own path; the
trail of a run (below) lists the steps of all branches.

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
  that passes the message on or stops it, such as `filter`), preferring the
  router when a step has both (`wastebin`); a sink position may use an action
  processor.

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
- A **releaser** (`stepdef.Releaser`, such as `aggregate` with a timer) holds
  messages and passes them on by itself. The engine runs its `Release` in a
  goroutine of its own while the flow runs, as it runs a source, and gives it
  `send`: a message sent enters the flow after the releaser's step, as a message
  of its own that nobody waits for. It is processed like any other (one at a
  time, not while the flow is paused, with the flow's error route), and counts
  in the flow's status. `Release` returns when the flow stops; what the
  processor still holds then is lost.
- A **looper** (`stepdef.Looper`, such as `loop` and `dowhile`) runs its
  routes in rounds: it has `Round` instead of `Route`, which the engine calls
  with the message that entered the router and the one the round before
  produced, until `Round` says the round is the last. The outcome of that
  round is the router's outcome.

The trail of a run (in the CLI's log) lists the steps in the order they ran,
branch after branch: `source:a -> router:r -> sink:tap -> sink:main`. The
`metadata.trail` of each copy holds only its own branch.

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
   `error.message` (what went wrong), `error.step` (the step's id),
   `error.class` (the Go type of the error at the bottom of the chain) and
   `error.stacktrace` (the error with every step and cause it wraps). They
   are what `${exception.message}`, `${exception.class}`,
   `${exception.stacktrace}` and `${exception}` (class and message) read in a
   simple expression; on any other message there is no exception. The https
   source does not return them. The error is then handled: the message counts as processed, the error route's
   outcome is what an https source replies (200), and the trail shows
   `error:<id>` before the error route's steps. The flow log adds
   `(error route handled: <error>)`.
3. Without an error route, or when the error route fails too, the message
   fails (`step x: …; error route: step y: …`).

A failure on a branch of a router goes to the error route with that branch's
message; a detached route (wire tap) never does. A forced stop never takes the
error route.

An error route can end in a **dead letter queue**: the `deadletter` step puts
the message, with its `error.*` headers, on an in-memory queue, and another
flow can read it with the source `queue:<name>`. `testdata/examples/deadletter.json` fails
every message (`${bodyAs(BlaBla)}`), retries 3 times 10 seconds apart, then
sends it to the queue `DLQ:68c7aed81e33920007000002`; the caller gets 200 with
the message as it failed. A flow of your own with the source
`queue:DLQ:68c7aed81e33920007000002` and a `log` sink shows what arrives there.

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
- an option with a fixed set of values (`enum`) accepts them in any case, as the
  Java platform does: `original` is `ORIGINAL`. The step gets the spelling of
  the schema.

The schema validator supports a small JSON Schema subset (`type`, `properties`,
`required`, `additionalProperties`, `enum`, `default`, `minimum`); a schema
using anything else fails registration.

| Step | Kind | Options (default) | Behavior |
|---|---|---|---|
| `timer:<name>` | source | `period` ms (1000), `repeatCount` (0 or less = unlimited) | Emits the counter 1, 2, 3… as body every period |
| `file:<dir>` | source | `fileName` (all files), `charset` utf-8, `autoCreate` (true), `recursive` (false), `delete` (false), `initialDelay` ms (1000), `delay` ms (500) | Polls the directory; body is the file content, header `file.name` its path relative to the directory, `Content-Type` by its extension (`.json`, `.xml`, `.csv`, `.txt`, `.zip`). Consumed files are deleted or moved to `<dir>/.done`. Names starting with a dot are skipped |
| `file:<dir>` | sink | `fileName` (header `file.name`, else the trace id), `charset` utf-8, `autoCreate` (true), `fileExist` Override\|Append\|Fail\|Ignore (Override) | Writes the body to the file |
| `log` | action | `showHeaders` (false), `showBody` (false), `showException` (no effect yet) | Logs `step <id>: traceid=… headers={…} body=…` to the flow's log |
| `setbody` | action | `language` constant\|simple (constant), `expression` ("") | Sets the body |
| `setheader` | action | `name` (required), `language` constant\|simple (simple), `value` ("") | Sets one header; not `body` or `metadata.*` |
| `passthrough` | action | – | Passes the message on unchanged |
| `message:<name>` | source | – | Produces nothing; messages are sent to the flow (`send`) |
| `queue[:<name>]` | source | `transport` (activemq, no effect) | Emits the messages of the in-memory queue `<name>`, by default the one named after its flow id, as they arrive (headers and trace id kept); see [Queues](#queues) |
| `deadletter` | sink | `deadLetterQueue` (DLQ), `connectionFactory` (no effect) | Puts a copy of the message on the in-memory queue `deadLetterQueue`; for error routes |
| `flowlink` | source | `flowId` (the parser fills in the flow's id), `transport` (no effect) | Emits the messages other flows send to this flow (also called `flowlink-async`); see [Flow links](#flow-links) |
| `flowlink` | action | `targetFlowId` (required), `transport` sync\|direct\|vm\|async\|seda\|activemq (sync; the last two act as async), `exchangePattern` InOnly\|InOut (InOut), `requestTimeout` ms (20000; also written `requestTimout`) | Sends a copy of the message to the flow `targetFlowId`; the step is also called `flowlink-async`, see [Flow links](#flow-links) |
| `queue[:<name>]` | action | `targetQueueId` (alternative to URI name), `delivery` processed\|enqueue (processed), `exchangePattern` InOnly\|InOut (InOnly), `requestTimeout` ms (20000; the designer writes `requestTimout`, which wins), `transport` (activemq, no effect) | Sends a copy to a logical queue; processed waits for the consumer, enqueue returns after buffering; see [Queues](#queues) |
| `topic:<name>` | source | — | Creates an independent subscription while running; paused subscriptions buffer messages; see [Topics](#topics) |
| `topic:<name>` | action | — | Publishes a copy to every active subscription without waiting for processing; see [Topics](#topics) |
| `https://<host>:<port>/<path>` | source | `matchPrefix` or `matchOnUriPrefix` (false), `exchangePattern` InOut\|InOnly (InOut), `preserveHttpHeaders` (false), `authenticationPreemptive` (no effect), `serverIdentityFile` (`security/server-identity.p12`), `serverIdentityPassword` | Receives HTTPS requests and replies with the flow's outcome, see [HTTPS](#https) |
| `https://<host>[:<port>]/<path>` | action | `httpMethod` GET\|POST\|PUT\|PATCH\|DELETE\|HEAD\|OPTIONS\|TRACE (GET), `authMethod` None\|Basic (None) with `authUsername` and `authPassword`, `trustStoreFile` (`security/outbound-truststore.p12`), `trustStorePassword`, `connectTimeout` ms (30000), `socketTimeout` ms (30000), `retryRequests` (false) with `retryAttempts` (5) and `retryInterval` ms (30000), `excludeHeaders` (regular expression), `throwExceptionOnFailure` or `useErrorRoute` (false); no effect: `authenticationPreemptive`, `maxTotalConnections`, `connectionsPerRoute`, `useCustomDateHeader`, `sslContextParameters` | Calls the endpoint, written `https://host/path` or, as DIL has it, `https:https://host/path`; the address may hold `${…}` parts, which are evaluated for each message. The response becomes the message, see [HTTPS](#https) |
| `rest` | action | `method` (post), `host` (`https://localhost:9002`), `path` (required), `produces` ("": Content-Type of the request when the message sets none), `consumes` ("": its Accept header), `trustStoreFile`, `trustStorePassword`, `socketTimeout` ms (30000), `throwExceptionOnFailure` (true) | Calls `host`/`path` as the https action does |
| `graphql` | action | `url` (or `graphql:<url>`), `query` ("": the body), `variables` (a JSON object), `accessToken` (bearer), `trustStoreFile` ("": the system's roots), `socketTimeout` ms (30000) | Posts the query as JSON and replaces the body with the response; an error status fails the message |
| `smtp:<host>:<port>`, `smtps:<host>:<port>` | action | `to` (required; commas or semicolons), `from` (username), `replyTo`, `subject` (the header `subject` overrides it), `exchangeBodyAs` body or attachment (body), `emailBody`, `contentType`, `username`, `password` (else `DIF_SMTP_PASSWORD`), `accessToken`, `trustStoreFile` ("": the system's roots), `timeout` ms (30000) | Sends the message as an email and passes it on unchanged: the body is the text, or, with `emailBody` or `exchangeBodyAs` attachment, attached (named after `file.name`) to the text `emailBody`. smtp requires STARTTLS, smtps uses TLS from the start; it logs in with PLAIN (password) or XOAUTH2 (accessToken), else not at all |
| `setheaders:message:<name>` | action | `expression`, `writeAsString` (false) | Sets all headers of the core message `<name>` (`dil.core.messages`); each header's `language` is constant, simple (default), xpath or jsonpath. A jsonpath header is the value the path selects in the body (several values: a list `[a, b]`); with `writeAsString` it is written as JSON, so a text has its quotes |
| `base64totext` | action | – | Decodes a base64 body to text (whitespace ignored, padding optional) |
| `texttobase64`, `binarytobase64` | action | – | Encodes the body as base64, without line breaks |
| `base64tobinary` | action | – | Decodes a base64 body like `base64totext`, but the body becomes the bytes, not text, so a binary file stays exact for the steps after it |
| `setbodyasstring` | action | – | Makes the body a string: bytes become their text, a decoded JSON body its JSON |
| `repeater[:<name>]` | source | `period` ms (10000), `repeatCount` (0 or less = unlimited) | The timer source with Camel's repeater defaults |
| `quartz:<name>` | source | `cron` (required), `timeZone` (local) | Emits a message without a body, with header `quartz.firetime` (RFC 3339), at every time the Quartz cron expression matches: `seconds minutes hours day-of-month month day-of-week [year]`, e.g. `0 0 3 * * ?` (03:00 daily). Supports `*`, `?`, values, ranges, steps, lists and names (`JAN`, `MON`; day-of-week 1–7 is SUN–SAT); not `L`, `W`, `#` or a year other than `*`. Times missed while the flow is busy or paused are skipped. Daylight saving time: a time that does not exist that day is skipped (as in Quartz), and a fixed time fires once when the clocks go back |
| `rest` | source | `method` get, post, put, delete, patch, head, … (get), `path` (required), `produces` (""), `consumes` (no effect), `exchangePattern` InOut or InOnly (InOut), `address` (0.0.0.0:9002), `serverIdentityFile`, `serverIdentityPassword` | Receives HTTPS requests with one method on a path of the REST address and replies with the flow's outcome; `produces` is the reply's Content-Type when the message sets none. See [HTTPS](#https) |
| `counter[:<name>]` | source | `start` (1), `numbers` (1; 0 or less = unlimited), `period` ms (10000) | Emits `start`, `start`+1, … as body every period, with `Content-Type` text/plain |
| `setoneway`, `setfireandforget` | action | – | Makes the exchange one-way: a waiting sender gets its reply now; see [Exchange patterns](#exchange-patterns) |
| `setrequestreply`, `settwoways`, `setrequestandreply` | action | – | Keeps the exchange request-reply (the default) |
| `removeheaders` | action | `pattern` (required), `excludePattern` ("") | Removes the headers matching `pattern` but not `excludePattern`: an exact name, a prefix ending with `*` or a regular expression, case-insensitive. Never removes the body or `metadata.*` |
| `replace` | action | `regex` (required), `replaceWith` (""), `flags` (`i`, `m`, `s`, comma-separated), `group` (0) | Replaces every match in the body; `$1` in `replaceWith` inserts a group. With `group` > 0 only that group of each match is replaced |
| `simplereplace` | action | – | Evaluates the body as a simple expression: `${header.<name>}` in the body becomes the header's value |
| `zip` | action | – | Zips the body as one file named after `file.name` (else the trace id); sets `file.name` to `<name>.zip` and `Content-Type: application/zip` |
| `unzip` | action | – | Extracts the one file of a zip body; `file.name` becomes its name and `Content-Type` is set by its extension (as the file source does) or removed. An archive with several files fails the message (that needs a splitter) |
| `validate` | action | `schema` (inline JSON Schema) or `schemaFile` (path) | Validates a JSON body against the schema; an invalid message fails with every problem, e.g. `body is not valid: /id: want integer, got string; /: missing required property lines`. Supports `type`, `properties`, `required`, `additionalProperties`, `items`, `enum`, `const`, `minimum`, `maximum`, `exclusiveMinimum`, `exclusiveMaximum`, `minLength`, `maxLength`, `pattern` and `minItems`/`maxItems`; a schema with any other keyword (`$ref`, `oneOf`, `format`, …) is rejected when the flow is loaded |
| `throttle` | action | `maxRequests` (required), `timePeriod` ms (1000) | Lets at most `maxRequests` messages pass per `timePeriod` (sliding window); the others wait |
| `encoder` | action | `originCharset` (UTF-8), `targetCharset` (UTF-8) | Converts the body between UTF-8, ISO-8859-1 (also `ISO8859_1`), US-ASCII and windows-1252 (`CP1252`); names are not case sensitive. Characters the target cannot hold become `?`; the five bytes windows-1252 leaves undefined (0x81, 0x8D, 0x8F, 0x90, 0x9D) decode to U+FFFD |
| `setuuid` | action | `headerName` (UUID), `generator` (no effect) | Sets the header to a new random UUID (version 4) |
| `setbodybyheader` | action | `headerName` (required) | Replaces the body with the header's value, as it is (empty if not set) |
| `setheaderbybody` | action | `headerName` (required; not `body` or `metadata.*`) | Sets the header to the body, as it is |
| `delay` | action | `milliseconds` (5000) | Holds the message, then passes it on; a forced stop does not wait |
| `logger` | action | `loggingLevel` TRACE\|DEBUG\|INFO\|WARN\|ERROR\|OFF (INFO), `language` constant\|simple (simple), `expression` (`${body}`) | Writes `step <id>: <level> <text>` to the flow's log (nothing with OFF) and passes the message on |
| `simplevalidator` | action | `expression` (required: a simple condition) | Passes the message on when the condition holds, else fails it with `validation failed: <condition>` |
| `wastebin` | action or sink | – | Drops the message: the steps after it never get it |
| `jsonvalidator:ref:<resource>` | action | – (the schema is the DIL resource) | Validates a JSON body against the JSON Schema in `dil.core.resources`, as `validate` does |
| `xslt:ref:<resource>` | action | – (the stylesheet is the DIL resource), `path` (a stylesheet file instead, read when the flow is built) | Transforms an XML body with an XSLT 2.0/3.0 stylesheet (1.0 stylesheets run too, as a literal `html xsl:version="1.0"` stylesheet does), by `github.com/knroy/go-xml`. The headers of the message that are text, numbers or booleans are the values of the top-level `xsl:param` of the same name, as in Camel. `xsl:include`, `xsl:import`, `document()` and `unparsed-text()` are closed. The output follows Saxon where go-xml follows the specification: indented by three spaces, and `<!DOCTYPE HTML>` before an `html` result without `version` or `html-version`. A stylesheet that does not compile fails the build; a body that is no XML, or `xsl:message terminate="yes"`, fails the message; `xsl:message` goes to the flow's log |
| `velocity:ref:<resource>` | action | – (the template is the DIL resource), `path` (a template file instead, read when the flow is built) | Replaces the body with the result of a Velocity template, as Camel's velocity component does. DIF has its own interpreter for a subset of the language: `##`, `#* *#` and `#[[ ]]#`; the references `$x`, `${x}`, `$!x` with `.property`, `[index]` and the methods `length size isEmpty trim toUpperCase toLowerCase toString equals equalsIgnoreCase startsWith endsWith indexOf replace substring contains get containsKey keySet values`; `#set($x = expression)`; `#if`, `#elseif`, `#else`; `#foreach($x in list)` with `$foreach.index\|count\|hasNext\|first\|last` and `$velocityCount`; expressions with strings (`"..."` expand references, `'...'` do not), numbers, lists `[a, b]`, ranges `[1..3]`, `! * / % + - < <= > >= == != && \|\|`. `#macro`, `#parse`, `#include`, `#define`, `#evaluate`, `#break`, `#stop` and any other method fail the build, naming the line. Variables: `$body` (text), `$headers` (the headers; names are not told apart by case, as in Camel), `$in` and `$request` (both `body` and `headers`). A reference without a value is written as it stands, as in Velocity; a line with only a directive leaves no line behind; the result is limited to 64 MB |
| `schematron:ref:<resource>` | action | – (the schema is the DIL resource), `path` (a schema file instead, read when the flow is built) | Checks an XML body against ISO Schematron rules, as Camel's schematron component does. The body goes on unchanged; the header `CamelSchematronValidationStatus` is `SUCCESS`, or `FAILED` when an assert does not hold, and `CamelSchematronValidationReport` holds the report in SVRL (failed asserts and successful reports with their location and text). A body that is not XML fails the message. The tests and contexts are XPath 2.0 (`xs:` is predeclared, `ns` declares other prefixes). Supported: `ns`, `let` (schema, pattern and rule), `pattern`, `rule` (a node fires only the first matching rule of a pattern), `assert`, `report`, `value-of` and `name` in the text. A report that holds is no failure. Abstract patterns and rules (`is-a`, `extends`), `include` and phases fail the build |
| `xmlvalidator:ref:<resource>` | action | – (the schema is the DIL resource), `path` (a schema file instead, read when the flow is built) | Validates an XML body against an XML Schema (1.0 and 1.1, by `github.com/knroy/go-xml`). A valid message passes on unchanged. As on the platform an invalid one does not fail: its body becomes `org.apache.camel.processor.validation.SchemaValidationException: Validation failed with N errors:` and one line per problem (code, path, line and column), so that a router after the step can test the body for `SchemaValidationException`. A body that is no XML is invalid too. `xs:import` and `xs:include` are closed. A schema that does not load fails the build (a type written without a prefix that names nothing is only found when a message is validated) |
| `soap:http(s)://<host>[:<port>]/<path>` | action | `action` (the operation; default the first element of the body), `extract` (false), `smart` (true), `params` (`wsdl` in any case, the query that fetches the WSDL; else a query added to every call), `headers` (JSON array `[{name, value, attrs: [{name, value}]}]`: SOAP header elements; an attribute without a value is left out), `httpHeaders` (JSON array `[{name, value}]`), `auth` (base64 of `user:password`, sent as Basic), `token` (Bearer, unless `auth`), `trustStoreFile`, `trustStorePassword`, `socketTimeout`, `connectTimeout` | Calls a SOAP 1.1 service. The body is the payload of the operation, such as `<GetPhoneTypes/>`; it goes in an envelope (with the `headers`) in a POST with `Content-Type: text/xml`, a `SOAPAction` and the `httpHeaders`. A body that already is an Envelope is sent as it is. With `smart` the WSDL (the address with `?wsdl`, read once and kept; a failure fails the message and is tried again with the next) gives the namespace of the operation, which a payload without one gets as its default namespace, and the SOAPAction of the operation; without `smart` the `action` is the SOAPAction. The answer replaces the body as it came; with `extract` it is the first element of the SOAP body after an XML declaration, with the namespace declarations it uses. `http.status` and `Content-Type` are set. A status of 300 or more, and with `extract` a SOAP fault, fail the message, with the `faultstring`. The headers of the message are not sent. The platform's component is not open: this follows its flows and their expected answers, and no flow could be compared with the real services (they are on the internet) |
| `as2` | source | `serverPortNumber` (9002), `address` (all), `requestUriPattern` (`*`; a path, or a prefix that ends in `*`), `as2MessageStructure` (what a message must have: `SIGNED`, `ENCRYPTED`, … require a valid signature or encryption), `decryptingPrivateKey`, `signingPrivateKey`, `signingCertificateChain`, `validateSigningCertificateChain` (PEM text, an `http(s)://` address of PEM, or a PEM or PKCS#12 file), `password` (of a PKCS#12 file), `serverFqdn`, `serverIdentityFile` and `serverIdentityPassword` (TLS; plain HTTP without), `as2From`, `as2To`, `alias`, `keyAlias`, `flowNameAsEndpoint`, `messageStructure` (accepted, no effect), and the timeout and trust store options | Receives documents by AS2 (RFC 4130), as the platform's as2 source does with Camel's AS2 server: an HTTP server that decrypts, verifies the signature, decompresses and makes a message of each document (body: the document, text if it is UTF-8, else bytes; `Content-Type`, `file.name`, `AS2-From`, `AS2-To`, `AS2-Message-Id`, `Subject`, `AS2-Signed`, `AS2-Encrypted`, `AS2-Compressed`, `AS2-Signer`). The receipt (MDN) answers in the same exchange once the flow has processed the message: `processed`, or `processed/Error: …` (`decryption-failed`, `authentication-failed`, `insufficient-message-security`, `decompression-failed`, `unexpected-processing-error`); it is signed when the sender asks and `signingPrivateKey` is set. Without `validateSigningCertificateChain` any valid signature passes, which proves the message whole, not who sent it. Keys are read when the first message comes, so an address of the platform's key service is not needed to build a flow. Not supported: asynchronous receipts (the receipt is in the response whatever the sender asked), RSA-OAEP, ciphers other than AES-CBC and 3DES-CBC. No CMS library is used: DIF's own (`cms.go`, standard library) is checked against `openssl cms` and `openssl smime` in both directions, in DER and BER, in the tests |
| `as2` | action | `hostName` (required), `targetPortNumber` or `port`, `requestUri` or `uri`, `as2From` and `as2To` (required), `subject`, `ediMessageContentType` (else `messageContentType`, else `application/edifact`), `as2MessageStructure` (PLAIN; SIGNED, ENCRYPTED, SIGNED_ENCRYPTED, PLAIN_COMPRESSED, SIGNED_COMPRESSED, ENCRYPTED_COMPRESSED, ENCRYPTED_COMPRESSED_SIGNED), `signingAlgorithm` (SHA256WITHRSA; also SHA1, SHA384, SHA512), `encryptingAlgorithm` (AES128_CBC; also AES192_CBC, AES256_CBC, DES_EDE3_CBC), `certificateForSigning` (private key and certificate), `certificateForEncrypt` (the partner's certificate), `password`, `mdn` sync\|none (sync), `dispositionNotificationTo`, `sslContext`, `keyAlias` (accepted, no effect), and the timeout and trust store options | Sends the body to a partner by AS2: compressed, then signed, then encrypted as the structure says, in a POST (https for port 443 or 8443 and with `sslContext`, else http). The body goes on; the headers `AS2-Message-Id`, `AS2-MDN-Disposition`, `AS2-MDN-Signed` and `http.status` are set. The receipt is required, has to say `processed`, has to have the integrity check (MIC) of what was sent, and when `certificateForEncrypt` is set has to be signed by that certificate if it is signed. A `subject` header of the message overrides the option. The headers of the message are not sent |
| `imaps:<host>[:<port>]` | source | `username` (required), `password` (env `DIF_IMAPS_PASSWORD`), `authenticationType` basic\|oauth (basic), `accessToken` (with oauth; `@{name}` is the tenant variable `name`, looked up for every poll, as an oauth2token step keeps it), `tenantDbName` (default), `content` body\|both (body), `folderName` (INBOX), `unseen` (true), `fetchSize` (10; 0 or -1 for all), `searchSubject`, `searchBody`, `searchFrom`, `searchTo`, `delay` (60000), `initialDelay` (1000), `socketTimeout`, `trustStoreFile`, `trustStorePassword` | Polls a mailbox of an IMAP server over TLS (port 993), with `github.com/emersion/go-imap/v2`, and makes a message of every unseen email that matches the search options, the oldest first. The body is the text of the email (the first `text/plain` part, else `text/html`, in UTF-8); the headers are `Subject`, `From`, `To`, `Cc`, `Date` (decoded), `mail.messageId` and `mail.uid`; the email's own Message-ID and Reply-To are not headers, as they would overwrite DIF's identity. With `content` `both` the attachments (and attached emails) are headers too, `attachment.<file name>` as bytes, and `attachments`, the names. An email is marked as seen once the flow has processed it; one the flow failed on stays unseen and comes again. A login is `LOGIN`, or with `oauth` XOAUTH2, as Gmail wants it. Not done: the platform's `bridgeErrorHandler`, which sends a failure to connect to the flow's error route (here a failed poll is logged and tried again), and attachments as a Camel concept (DIF has none). Tested against an in-process IMAP server over TLS, not against Gmail |
| `sql` | action | `query` (required; a simple template, usually `${body}`), `connectionType` (required: `postgres`, `mysql8` (also `mysql`, `mysql5`, `mariadb`), `sql_server`, `oracle`), `host` (required), `port` (the default of the database), `database` (for oracle the service name), `username`, `password` (env `DIF_SQL_PASSWORD`), `useSSL` (false), `tlsVersion` (the one version to use, such as `TLSv1.2`; empty: 1.2 or later), `trustStoreFile`, `trustStorePassword`, `socketTimeout`, `connectTimeout`, `escapeChars` (no effect) | Runs an SQL statement. `query`, `host`, `port`, `database`, `username` and `password` are simple templates evaluated for each message, so a flow can take them from the headers (`${header.DB_Host}`); the handles are kept per connection (at most 32), and closed after a minute without use. A final `;` is left out. A statement that returns rows (it starts with `SELECT`, `WITH`, `SHOW`, `DESCRIBE`, `EXPLAIN`, `VALUES` or `TABLE`, or has `RETURNING`) makes the body `<ResultSet><ResultSize>n</ResultSize><Results><Result><column>value</column>…</Result>…</Results></ResultSet>` (`<Results/>` for none), as the platform's expected answers show: an element for each column, a NULL as an empty element, characters of a column name that an XML name cannot hold left out (`version()` is `version`), dates as `2023-03-21`, moments as `2023-03-21 10:30:05.12`, binary data that is no text in base64. Another statement leaves the body. Either way the headers `numberOfRecords` (rows, or rows changed) and `hasErrors` (`false`: a statement that fails fails the message) are set. Drivers: pgx, go-sql-driver/mysql, go-mssqldb, go-ora (pure Go), each in a file of its own (`steps/impl/sql_<database>.go`). Not verified against real servers; the tests use a fake `database/sql` driver. `escapeChars` is accepted but ignored: the platform escapes characters in a way the flows' answers do not explain. A flow whose connection comes from platform flow properties (no `host`) does not build |
| `sql2` | action | `query` (required), `dataSource` (the id of a connection in `dil.core.connections`), `connection` (the keys of that connection as JSON: `dbtype` `postgres`\|`mysql`\|`sqlserver`\|`oracle`, `dbname`, `host`, `port`, `username`, `password`; the DIL parser fills it from `dataSource`), `useSSL`, `tlsVersion`, `trustStoreFile`, `trustStorePassword`, `socketTimeout`, `connectTimeout` | Runs an SQL statement with parameters, as Camel's SQL component does: `:#name` is the header `name` (any case; a header that is missing fails the message) and `:#${expression}` a simple expression. They are sent as parameters, never written into the statement; text in quotes has none. The body and headers are as with `sql` |
| `fileenrich:<dir>` | action | `fileName`, `include`, `exclude` (regular expressions on the name), `recursive` (false), `binary` (false), `charset` UTF-8, ISO-8859-1 or US-ASCII (utf-8), `delete` (false) | Replaces the body with the content of the first file (by name) the options select, and sets `file.name` and Content-Type; without one the message passes on unchanged. The file stays unless `delete` |
| `ftp:<host>[:<port>]/<dir>` | source | `recursive`, `fileName`, `include`, `exclude` (regular expressions on the whole name), `binary`, `charset` (utf-8), `sortBy` (name; `file:name`, `reverse:file:name`, `file:modified`, `reverse:file:modified`), `delete`, `move` (.archive), `moveFailed` (.error), `readLock` none\|changed, `delay`, `initialDelay` (60000), `maxMessagesPerPoll` (1; 0 or -1 for all), `autoCreate` (true), `userName`, `password` (env `DIF_FTP_PASSWORD`/`DIF_SFTP_PASSWORD`), `disconnect` (true), `socketTimeout` (30000), `passiveMode` (true; false is rejected) | Polls an FTP directory and produces a message per file; see [FTP, FTPS and SFTP](#ftp-ftps-and-sftp) |
| `ftp:<host>[:<port>]/<dir>` | sink | `fileName`, `binary`, `charset`, `autoCreate` (true), `fileExist` Override\|Append\|Fail\|Ignore (Override), `implicit` (false; true is rejected, use `ftps`), `passiveMode`, `userName`, `password` (env `DIF_FTP_PASSWORD`/`DIF_SFTP_PASSWORD`), `disconnect` (true), `socketTimeout` (30000) | Writes the body to a file in the directory |
| `ftpenrich:<host>[:<port>]/<dir>` | action | `recursive`, `fileName`, `include`, `exclude` (regular expressions on the whole name), `binary`, `charset` (utf-8), `sortBy` (name; `file:name`, `reverse:file:name`, `file:modified`, `reverse:file:modified`), `delete`, `move` (.archive), `moveFailed` (.error), `readLock` none\|changed, `abortMode` (false), `autoCreate`, `maxMessagesPerPoll` (no effect), `passiveMode`, `userName`, `password` (env `DIF_FTP_PASSWORD`/`DIF_SFTP_PASSWORD`), `disconnect` (true), `socketTimeout` (30000) | Replaces the body with the content of the first file; moves or deletes it afterwards |
| `ftps:<host>[:<port>]/<dir>` | source | as the `ftp` source, and `implicit` (false), `trustStoreFile`, `trustStorePassword` | The `ftp` source over TLS; env `DIF_FTPS_PASSWORD` |
| `ftps:<host>[:<port>]/<dir>` | sink | as the `ftp` sink with `implicit`, and `trustStoreFile`, `trustStorePassword` | The `ftp` sink over TLS |
| `ftpsenrich:<host>[:<port>]/<dir>` | action | as `ftpenrich`, and `implicit`, `trustStoreFile`, `trustStorePassword` | `ftpenrich` over TLS |
| `smb://[<user>@]<host>[:<port>]/<share>[/<dir>]` | source | as the `sftp` source without the key and host key options (`passiveMode` too); and `domain`, `userName` (or `DOMAIN\user`, or the user of the URI), `password` (env `DIF_SMB_PASSWORD`) | The `sftp` source on a share of a Windows (SMB 2/3) file server, by `github.com/hirochachacha/go-smb2` (pure Go): the first part of the path is the share, a backslash is a slash. Login is NTLM. `readLock` `changed` only takes a file that has not changed since the last poll. An address with `${...}` in it, such as `smb://${header.User}@${header.Host}/…`, is not supported: the address is fixed when the flow is built |
| `smb://[<user>@]<host>[:<port>]/<share>[/<dir>]` | action or sink | as the `sftp` sink without the key and host key options; and `domain` | Writes the body to a file, as the `sftp` sink does; as an action (the platform's flows have it so) it passes the message on unchanged. The go-smb2 wire code is not tested against a server (none exists in Go); the steps' logic is tested through a fake share on the local disk, with the same tests as ftp and sftp |
| `smbenrich://[<user>@]<host>[:<port>]/<share>[/<dir>]` | action | as `sftpenrich` without the key and host key options; and `domain` | `sftpenrich` on an SMB share |
| `sftp:<host>[:<port>]/<dir>` | source | as `ftp`, and `privateKey` (a file), `privateKeyPassphrase` (env `DIF_SFTP_PRIVATE_KEY_PASSPHRASE`), `knownHostsFile`, `strictHostKeyChecking` (true); `passiveMode` has no effect | Polls an SFTP directory |
| `sftp:<host>[:<port>]/<dir>` | sink | as the `ftp` sink, with the `sftp` connection options | Writes the body to a file |
| `sftpenrich:<host>[:<port>]/<dir>` | action | as `ftpenrich`, with the `sftp` connection options | Replaces the body with the content of the first file |
| `settenantvariable:<name>` | action | `language` simple, constant, xpath or jsonpath (simple), `value`, `tenantDbName` (default); `encrypt`, `protectedValue`, `groupName`, `flowName` (no effect) | Sets the tenant variable to the value. DIL carries the value in base64 (`dGVzdA==` is `test`); the DIL parser decodes it, and takes a value that is no base64 text as it is. Tenant variables are shared by all flows of the process and kept in memory |
| `gettenantvariable:<name>` | action | `headerName` (required), `tenantDbName` (default) | Sets the header to the tenant variable ("" if not set) |
| `removetenantvariable:<name>` | action | `tenantDbName` (default) | Removes the tenant variable |
| `oauth2token:<id>` | sink | `tokenName` (required: tenant variables, separated by commas), `tenantDbName` (default), `expiryDelay` (60 seconds), `tokenUrl` and `clientId` (both needed to create the step), `grantType` client_credentials\|refresh_token (refresh_token if a refresh token is set, else client_credentials), `clientSecret`, `refreshToken`, `scope`, `clientAuthentication` basic\|post (basic), `trustStoreFile`, `trustStorePassword`, `socketTimeout` | Fetches an OAuth2 access token and sets it in each variable of `tokenName` and in the same name with the suffix `_Temp`. A message that reaches the sink renews the token only if it expires within `expiryDelay`, so a repeater in front of it is a token service. The Java platform keeps the endpoint and credentials in the tenant's OAuth configuration, and the flows name only the token. DIF takes `tokenUrl`, `clientId`, `clientSecret`, `refreshToken` and `scope` from the option, else from the environment variable `DIF_OAUTH2_<TOKEN>_<SETTING>`, where `<TOKEN>` is a name in `tokenName` in capitals (other characters as `_`; the names are tried in turn) and `<SETTING>` is `TOKEN_URL`, `CLIENT_ID`, `CLIENT_SECRET`, `REFRESH_TOKEN` or `SCOPE`, else from `DIF_OAUTH2_<SETTING>` for all tokens. Each variable has a `_FILE` companion for a mounted secret. For `OauthTokenGoogleDrive`: `DIF_OAUTH2_OAUTHTOKENGOOGLEDRIVE_TOKEN_URL`, `..._CLIENT_ID`, `..._CLIENT_SECRET` and `..._REFRESH_TOKEN`. A flow without them does not load, and the error names the variables |
| `googledrive:<folderId>` | source | `accessToken` (required; `@{name}` is replaced by the tenant variable `name` at each call), `filterFiles` (a file name), `moveTo` (.done), `gSuiteFiles` Ignore, `initialDelay` (1000), `delay` (5000), `tenant` (default), `flowId` (no effect), `trustStoreFile`, `trustStorePassword`, `socketTimeout` | Polls a Google Drive folder with the Drive v3 API and produces a message per file: the body is the content, the headers are `file.name` and `googledrive.id`. Subfolders and Google's own formats (Docs, Sheets, ...) are skipped. A consumed file is moved to the subfolder `moveTo`, which is created if needed. A failed poll, such as one before the token exists, is logged and tried again |
| `googledrive:<folderId>` | action | `accessToken` (required), `fileName`, `fileExist` Override\|Fail\|Ignore (Override), `tenant` (default), `flowId` (no effect), `trustStoreFile`, `trustStorePassword`, `socketTimeout` | Writes the body to a file in the folder and sets `googledrive.id`. The name is `fileName`, else the header `file.name`, else `CamelFileName`; the file's Content-Type is the message's. Override replaces the content of a file with that name |
| `setcookie` | action | `name`, `domain` (both required), `value`, `path` (/), `isSecure` (false) | Adds a cookie to the cookie store, which the https and rest actions send to its domain (and subdomains) and path |
| `removecookie` | action | `name` (required), `domain` | Removes the cookies with the name and domain from the cookie store |
| `multipart` | action | `fname` (required), `formFields` (a JSON object of text fields); `contentType` (no effect) | Makes the body a multipart/form-data body: a part `fname` with the body (a file named after `file.name`, with its Content-Type), then the form fields |
| `editoxml` | action | `segment` (LB: a line break), `field` (~), `component` (^), `subComponent` (!) | Converts delimited EDI into XML: `<edi-message>` with `<delimiters>` and an element per segment, named after its first field, holding `<field.N>`, `<component.N>` and `<sub-component.N>` |
| `xmltoedi` | action | – | Converts that XML back into EDI, with its `<delimiters>` |
| `wiretap` | router | – | Sends a copy to the link with rule `wiretap` (detached), then the message along the other link |
| `recipient` | router | – | Sends a copy to every link, in order; the outcome is the last one's |
| `content` | router | `expression`, `namespace` (no effect; the conditions are on the links) | Sends the message along the first link whose condition (`language`, `expression`) holds, else along the link without a condition; with none, the message stops |
| `filter` | action | `language` simple\|xpath\|jsonpath (simple), `expression` (required) | Passes the message on when the condition holds, else stops it |
| `split` | router or action | `language` xpath\|jsonpath\|tokenize\|xtokenize\|simple (xpath), `expression` (required); `streaming`, `parallelProcessing`, `exchangePattern` (no effect yet) | Sends each part of the body along the link with rule `split`, with headers `split.index`, `split.size` and `split.complete`; then the message itself along the other link, if any. The parts: for xpath the selected nodes (an element as XML), for jsonpath the selected values as JSON (strings as is; one selected array is split), for tokenize what lies between the occurrences of the text, trimmed and not empty, for xtokenize the elements of an XML path (`//product`, or just a name), for simple the elements of the list the expression gives (`${body.split(',')}`) or the parts of a text between commas. An xpath or jsonpath with `${...}`, such as `${header.expression}`, is evaluated for every message |
| `enrich` | router | `enrichType` override\|xml\|json (xml; the designer also writes it as `enrichMethod` or `enrichFileType`, which win), `useErrorRoute` (true), `attachmentName` (no effect) | Content enricher: sends a copy along the link with rule `enrich`, merges what comes out into the message and sends that along the other link. `override`: the enrichment (body and headers) replaces the message; `xml`: its root element is appended inside the body's root element; `json`: its members are set in the body's object (the message keeps its headers). When the enrichment fails, the message fails with that error (so the flow's error route can take it), or with `useErrorRoute` false continues without it and the error is logged |
| `aggregate` | action | `aggregateType` xml\|text/xml\|application/xml\|json\|application/json (xml), `completionSize` (0), `completionTimeout` (0), `completionInterval` (0), the last two in milliseconds | Collects messages. A group is complete with the last part of a split (`split.complete`), with `completionSize` messages, when `completionTimeout` has passed since its last message came, and every `completionInterval`. The complete group is released as a message of its own, made from the group's last message (a new message ID, caused by it, without the split headers), with the aggregate as body; it goes on along the link and what comes of it is ignored (a failure is logged, or, when a timer completed the group, handled by the flow's error route). The message that came in carries on as it came, so a request gets its reply at once. A body that is not XML or JSON fails when it comes in. One group at a time (the Kamelet correlates all messages); without a timer a new split (`split.index` 0) starts a new group. A group a timer has not completed when the flow stops is lost. Inside the `split` route of a `splitandaggregate`, the aggregate's type decides how that aggregates the parts |
| `splitandaggregate` | router | as `split` (`expression` may be on the split link instead), and `aggregateType` | Splits the body, sends each part along the link with rule `split`, aggregates what comes out (a gatherer) and sends the message with the aggregate along the other link; with nothing to split the message goes on as it is. A failed part fails the message |
| `splitwithnamespace` | router or action | as `split` with `language` xpath only, and `nsprefix` and `namespace` (given together) | `split` whose xpath may use the prefix `nsprefix`, which stands for `namespace` (`/root/h:table`); an element is written with the declarations its names need |
| `splitandaggregatewithnamespace` | router | as `splitandaggregate` with `language` xpath only, and `nsprefix` and `namespace` | `splitandaggregate` whose xpath may use the prefix `nsprefix` |
| `if` | router or action | – (the condition is on the link with rule `if`) | Sends the message along the `if` link when its condition holds, else along the link without a condition; as an action the message stops there |
| `loop` | router or action | `language` simple\|constant (simple), `expression` (1), `copy` (false); the link with rule `loop` may set both | Sends the message along the `loop` link the given number of times, each round with the message the round before produced (with `copy`: a copy of the message as it entered) and headers `loop.index` (from 0) and `loop.size`; then along the other link, if any. As an action the rest of the flow runs once per round |
| `dowhile` | router or action | `language` simple\|xpath\|jsonpath (simple), `expression`, `maxLoops` (1000), `copy` (no effect); the link with rule `dowhile` may set the condition | Sends the message along the `dowhile` link as long as the condition holds for it (checked before every round, at most `maxLoops` times), with header `loop.index`; then along the other link, if any |

Aggregates are, for XML, the parts' root elements in `<Aggregated>…</Aggregated>`
after an XML declaration and, for JSON, an array of the parts.

Camel keeps the round of a loop in the exchange property `CamelLoopIndex`, so
`${header.CamelLoopIndex}` in `testdata/examples/experimental/loop.json` is empty there
and in DIF alike; DIF has no exchange properties and sets the headers
`loop.index` and `loop.size` instead, as `split` sets `split.index`.

Language `constant` is the literal text. Language `simple` is the simple
language of Camel (camel-core-languages 4.x), built on the standard library: text
with `${...}` references in it, which may be nested, as in
`${uppercase('Hello ${body}')}`. The expression of a flow is trimmed first, as
Camel's DSL does; the body of a message that `simplereplace` evaluates is not.

| Reference | Value |
|---|---|
| `${body}`, `${bodyAs(String)}`, `${in.body}` | the body; `${bodyAs(<type>)}` with another type loads but fails the message when evaluated, as the conversion does in Camel (`deadletter.json` relies on it) |
| `${header.<name>}`, `${headers.<name>}`, `${header:<name>}`, `${header[<name>]}` | the header. Names are not told apart by case (the https source writes a request header `condition` as `Condition`). A name with dots, such as `file.name`, is a name as a whole |
| after a value: `.trim()`, `.length`, `.substring(2)`, `.replaceAll(re,repl)`, `.toUpperCase()`, `.split(',')[1]`, ... | the methods of Java's String, List and Map that flows use (OGNL); `?.` stops at nothing |
| `${random(<max>)}`, `${random(<min>,<max>)}` | an integer from min (default 0) up to max |
| `${date:now:<format>}`, `${date-with-timezone:now:<zone>:<format>}` | the current time; `<format>` is a Java date format such as `yyyy-MM-dd HH:mm:ss`, `<zone>` an IANA zone such as `Europe/Amsterdam` |
| `${capitalize(x)}`, `${uppercase(x)}`, `${lowercase(x)}`, `${trim(x)}`, `${normalizeWhitespace(x)}`, `${quote(x)}`, `${safeQuote(x)}`, `${unquote(x)}`, `${length(x)}`, `${size(x)}`, `${val(x)}` | text functions; without `x` they work on the body |
| `${concat(a,b,sep)}`, `${pad(x,width,sep)}`, `${replace(from,to,x)}`, `${substring(head,tail,x)}`, `${substringBefore(x,t)}`, `${substringAfter(x,t)}`, `${substringBetween(x,after,before)}`, `${contains(x,t)}` | text functions with arguments, in Camel's order |
| `${sum(...)}`, `${min(...)}`, `${max(...)}`, `${average(...)}`, `${abs(x)}`, `${ceil(x)}`, `${floor(x)}` | whole numbers; an argument may be a list or comma separated text |
| `${join(sep,prefix,x)}`, `${split(x,regex)}`, `${distinct(...)}`, `${reverse(...)}`, `${sort(x,reverse)}`, `${range(min,max)}` | lists; a list is written `[a, b]`, as Java does |
| `${hash(x,alg)}` | lower case hexadecimal digest; MD5, SHA-1, SHA-224/256/384/512 (default SHA-256), SHA3-224/256/384/512 |
| `${jsonpath(path)}`, `${jsonpath(path,Integer)}` | on the body: the value, or a list for a path with `*` |
| `${xpath(expression)}` | on the body: the text of the first item the XPath 2.0 expression selects |
| `${jq(program)}`, `${jq(program,Integer)}` | the [jq](https://jqlang.github.io/jq/) program ([gojq](https://github.com/itchyny/gojq)) on the body (JSON): nothing is empty, one result is that value (a text as it is, an object or array as JSON), several are a list. The program has `$headers`, `$body`, `header("name")` and `body`, and is stopped after 10 seconds. Write a `}` in it as `\}`, as in any block |
| `${file:name}`, `${file:name.ext}`, `${file:name.ext.single}`, `${file:name.noext}`, `${file:onlyname}`, `${file:parent}`, `${file:path}`, `${file:length}`, ... | the file headers `CamelFileName` (else `file.name`), `CamelFileNameOnly`, `CamelFileParent`, `CamelFilePath`, `CamelFileAbsolute`, `CamelFileAbsolutePath`, `CamelFileLength`, `CamelFileLastModified`; `.ext` is after the first dot of the name, `.single` after the last |
| `${flowId}`, `${flowName}`, `${flowVersion}`, `${tenant}`, `${environment}`, and `${variable:group:<id>:MetaData.FlowID}` (also `FlowName`, `FlowVersion`, `TenantName`, `EnvironmentName`) | the flow's own properties; a DIL flow has them in its `options` (`tenant`, `environment`, `version`) |
| `${exception}`, `${exception.message}`, `${exception.class}`, `${exception.stacktrace}` | the error on a message that goes along the error route |
| `${headers}`, `${variable.<name>}`, `${variables}` | all headers, written `{a=1, b=2}`; variables, which an `$init` block sets and which stay on the message |
| `${empty(String)}`, `${iif(cond,a,b)}`, `${not(cond)}`, `${isEmpty(x)}`, `${isNumeric(x)}`, `${uuid}`, `${null}` | |
| `${int:...}`, `${long:...}`, `${boolean:...}`, `${string:...}` | the value as that type |

Operators, as in Camel: between values `?:` (the right side when the left is
nothing, false, empty or 0) and `~>` and `?~>` (the left value is the body for the
function on the right); after a value `++` and `--`; in a function
`${header.n > 10 ? 'big' : 'small'}`. Conditions (`content`, `filter`, `iif`) use
`==`, `!=`, `=~`, `!=~`, `>`, `>=`, `<`, `<=`, `contains`, `!contains`, `~~`,
`!~~`, `regex`, `!regex`, `in`, `!in`, `is`, `!is`, `range`, `!range`,
`startsWith`, `endsWith`, `!startsWith`, `!endsWith`, joined by `&&` and `||`;
the operators have a space on both sides, and numbers are compared as numbers.
An expression may start with an init block that sets variables, as in Camel:
`$init{ $limit := 18; $who := ${uppercase(${body})}; }init$` and then
`$who is over $limit`. Not supported: the functions that need the Camel exchange (`${exchangeId}`, `${routeId}`,
`exchangeProperty`, ...).
Other `${...}` expressions are rejected when the flow is loaded.

### Exchange patterns

How a sender and a flow communicate:

| Pattern | In DIF |
|---|---|
| One-way (fire and forget) | `send`, `Flow.Send`, timer, file and quartz sources, the https source with `exchangePattern` InOnly (it replies at once with the request), `deadletter`, `flowlink` with `exchangePattern` InOnly |
| Request-reply | `request`, `Flow.Request`, the https source, `flowlink` and `queue` with InOut: the sender gets the message the flow ends with, or the error |
| Scatter-gather | `enrich`, `splitandaggregate` |

A flow can make its exchange one-way part way. When a message reaches
`setoneway` (or `setfireandforget`), a sender that waits for a reply gets the
message as it is then. The flow goes on without the sender. A step that fails
after that no longer reaches the sender; it goes to the error route and the
log. `setrequestreply` (or `settwoways`, `setrequestandreply`) keeps the
default, InOut. It cannot take back a reply that `setoneway` already sent. In
`testdata/examples/setOneWay.json` the https caller gets `1234`, the body at
`setoneway`; in `setRequestReply.json` it gets `last step`.

### Asynchronous request/reply

Use three steps when the submitter needs an acceptance now and a result later:

```text
submit:  message -> request:jobs (replyTo=results)
worker:  queue:jobs -> process -> reply
result:  reply:results -> handle response
```

`request:<queue>` creates a child message and registers its request before
enqueueing. It returns the original payload with a `Request-Id` receipt after
admission, without waiting for a worker. `replyTo` is a required local queue name;
it must differ from the request queue. `requestTimeout` defaults to 20000 ms and
sets an absolute deadline from submission, including any admission wait.
`overflow` and `enqueueTimeout` work as on queue actions. Failed admission removes
the pending registration. Cancelling the caller after admission does not cancel
the job. Existing synchronous queue modes and `Flow.Request` are unchanged.

The request carries `Reply-To`, `Request-Id`, and `Reply-Deadline`. `Request-Id`
equals the child request's `Message-Id`; `Correlation-Id` still identifies the
whole conversation, which can contain several requests. The `reply` sink creates
a new child response, retains the request ID, and clears reply-routing headers.
Its `status` option is `success` (default) or `error`; an error route can use
`reply` with `status=error` to return the existing `error.*` headers. Each logical
request accepts one response. A worker that never replies produces a timeout.

`reply:<queue>` matches responses by request ID and expected destination, emitting
messages with `Reply-Status` set to `success`, `error`, or `timeout`. Timeout
messages retain request/correlation/trace identity and have a nil body. A response
observed by the reply source at or after the deadline is late. Expiration does
not stop queued or running worker operations. Start the reply source before
submitting requests when timely response matching matters.

Only one reply source may run for a destination. Ordinary queue consumers cannot
share it. Reply intake continues while the result flow processes an earlier
outcome. Terminal completion, including a handled error route, acknowledges the
outcome; `setoneway` does not. Unhandled result-processing failures retry the same
outcome, with a 25 ms polling interval, until success or shutdown. Use an error
route for permanent failures and idempotent handling for external side effects.
Result delivery order is unspecified. Stopping and restarting the source in the
same runtime preserves undelivered outcomes.

Late, duplicate, malformed, unknown, and wrongly addressed responses go to
`unmatchedQueue` (default `<reply queue>.unmatched`) with `Reply-Reason` set to
`late`, `duplicate`, `malformed`, `unknown`, or `wrong-destination`. If that queue
is full, the source fails and returns the response to its input queue for a later
restart. Service deployments must include the unmatched queue's consumer.

Pending requests, undelivered outcomes, and retained terminal records share a
bounded capacity. Set `channels.requests` in a service configuration, or
`api.ChannelConfig.Requests` for embedded use:

```json
{"requests": {"capacity": 10000, "retention": 86400000}}
```

These are the defaults; retention is milliseconds after successful result-flow
completion. It retains enough information to distinguish duplicate and late
responses; later responses are classified as unknown. Capacity exhaustion rejects
new requests. Payload values follow the existing shallow-copy contract.

This first implementation is **process-local and in-memory**. Request and reply
queues must use memory storage; pending conversations do not survive process
restarts. Durable conversations require a separate atomic journal extension.

Run the four-flow demonstration with:

```sh
go run ./cmd/dif run --dir testdata/examples/request-reply
```

The timer submits a document every five seconds, the worker processes it after a
short delay, and the result flow logs the later response. The fourth flow logs
unmatched responses.

### Queues

Queues are named, in-memory FIFO queues shared by all flows and engines of one
`dif` process. A `queue:orders.received` action sends to the logical name; any
flow with a `queue:orders.received` source can consume it. Multiple consumers
compete: each message is handed to one flow, with no fairness guarantee.
Dequeue order is FIFO, but competing flows can finish in a different order.

The action's `delivery` option selects the handoff:

- `processed` (default) preserves existing behavior: wait for consumer
  processing, up to `requestTimeout` milliseconds. With `exchangePattern`
  `InOut`, the consumer's response replaces the message; with `InOnly`, the
  original continues. A consumer using `setoneway` can reply early. A timed-out
  message is discarded if it has not been taken yet, as with synchronous flow links.
- `enqueue` copies the message into the queue and immediately continues with
  the original. It works without a running consumer. Only `InOnly` is supported;
  success means buffered, not processed. `requestTimeout` has no effect in this mode.

The legacy `targetQueueId` option remains an alternative to the URI name;
specifying both with different names is rejected. A bare `queue` source still
defaults to its flow ID. In `testdata/examples/queueOutbound.json` the HTTPS caller gets
the reply of `queueInbound.json`. `transport` remains informational: it does
not connect to ActiveMQ or any other broker.

A buffer defaults to 10,000 waiting messages, in addition to messages already handed
off to a source or flow. Sending to a full buffer fails immediately by default. Stopping
consumers preserves queued messages; a source returns a message the flow refused
during shutdown. No channel redelivery happens after a flow takes a message:
use the flow's step retries and error route, optionally ending in `deadletter`.
Forced stops can lose in-flight messages. With the default memory storage, all
queued messages disappear on process exit. For persistent queues and channel
redelivery, see [Durable channels and backpressure](#durable-channels-and-backpressure).
These are not exactly-once delivery guarantees.

### Topics

A `topic:orders.received` action publishes to every active source subscribed to
that topic. Each source owns an independent FIFO buffer of 10,000 messages and
gets a separate message map. Subscription capacity can be configured per topic.
Queue and topic names occupy separate namespaces.
Message, correlation and trace IDs are preserved. Payload values follow DIF's
existing shallow-copy contract: processors must replace values rather than
mutating shared maps, slices or byte arrays.

Publication is enqueue-only and does not support request/reply. If any active
subscription buffer is full, the action fails without enqueueing to any
subscriber by default. A slow subscriber therefore does not make the publisher wait for
processing, but can cause subsequent publications to fail. Concurrent publications
have the same enqueue order at every subscriber. With no subscriptions, publication
succeeds with no deliveries and no retained history.

A subscription is registered before its flow's `Start()` returns. Loading a flow
does not subscribe. Pause retains the subscription and buffers new messages;
resume consumes the backlog. Stop unregisters the subscription and discards its
pending messages; an already accepted message completes on graceful stop. Restart
creates a fresh subscription without replay. Publication racing with stop may be
accepted before the subscription is removed and then discarded. Successful
publication guarantees buffer admission, not completion in every flow.

Consumer errors use each flow's existing retries and error routes. They do not
propagate back to the publisher. No acknowledgement, automatic channel redelivery,
persistence, or durable subscription is provided.

### Durable channels and backpressure

`api.NewRuntime(api.ChannelConfig{...})` creates an isolated channel runtime.
Load its flows with `runtime.Load` or `runtime.LoadBytes`, stop them before
`runtime.Close`, and monitor `runtime.Failed()` for storage failures. Existing
package-level `api.Load` calls continue using shared in-memory channels.
Services own a runtime and accept its configuration under `channels` in their
`--config` JSON. Channel declarations are centralized: steps refer to channel
names and cannot override storage or capacity. Undeclared channels use memory
and the existing capacity. No external broker or dependency is required.

```json
{
  "files": ["testdata/reliable/producer.json", "testdata/reliable/consumer.json"],
  "channels": {
    "directory": "data/channels",
    "maxDiskBytes": 268435456,
    "maxMessageBytes": 4194304,
    "queues": {
      "orders": {
        "durable": true,
        "capacity": 1000,
        "maxDeliveries": 5,
        "retryDelay": 1000,
        "deadLetter": "orders.DLQ"
      }
    },
    "topics": {"audit": {"capacity": 2000}},
    "idempotency": {
      "orders": {"durable": true, "retention": 86400000, "maxKeys": 100000}
    }
  }
}
```

Paths are relative to the working directory. Queue retry delay and idempotency
retention are milliseconds. Omitted/zero configuration values select defaults:
capacity 10,000, five delivery attempts, 1,000 ms retry delay, 24-hour retention,
100,000 keys, 4 MiB encoded messages and a 256 MiB disk budget.

Queue, flowlink, topic actions and dead-letter sinks accept `overflow: "fail"`
(default) or `overflow: "block"`. Blocking requires a positive `enqueueTimeout`
in milliseconds and ends on cancellation. Request/reply also retains its overall
`requestTimeout` deadline. Waiting never holds channel locks. Topic publications
wait until every active subscription has space, then enqueue to all atomically;
membership is checked again after each wakeup. There are no discard policies.
Use finite timeouts even when flows form cycles. Reserved deliveries are outside
the waiting limit; returning/retrying them can temporarily exceed that limit,
which prevents new admission until space becomes available.

Durable queues accept only `delivery: "enqueue"` with `exchangePattern: "InOnly"`.
Acceptance means the message is journaled and synced, not processed. Their sources
acknowledge only after processing and the error route finish; `setoneway` does
not acknowledge early. A successful error route is terminal success. Unhandled
errors redeliver the original stored message after the retry delay, preserving
identity. Each delivery also gets the flow's existing step retries. Exhausted
deliveries move atomically to the durable dead-letter queue, preserving the body
and adding `error.message` and `error.queue`. An unspecified dead-letter queue
defaults to `<queue>.DLQ` and is created automatically. An automatically created
DLQ parks messages if its own consumer exhausts five attempts, preventing endless
dead-letter chains. Parked records remain stored and visible in monitoring; this
release has no administrative replay command. A full DLQ or failed settlement
retains the original delivery and reports a source failure.

After a process restart, unacknowledged messages are available again. Queue
reservation is FIFO among currently available deliveries; retries can change
completion order. Durable queues without a consumer can retain work for a later
run and do not prevent graceful service shutdown. Active consumers register
recovered work before startup completes so draining includes their backlog.
Forced shutdown preserves unfinished disk deliveries. Topics and flowlinks remain
in memory; durable subscriptions and persistent request/reply are not supported.

Storage uses a versioned, checksummed journal with synced writes and compaction.
One process exclusively owns a storage directory. Use local durable storage,
not a shared broker filesystem; mount it on a persistent volume in containers.
The disk budget reserves room for compaction and settlement records, so admission
can stop below the total budget. Unsupported payload types are rejected before
admission: supported values are nil, strings, booleans, built-in integers and
finite floats, `json.Number`, byte slices, string slices, `[]any`, string-keyed
`map[string]any`, and `message.Message`, recursively up to 100 levels. Their types
survive recovery. A torn final record is truncated; checksum corruption fails
startup. Storage I/O failures stop the runtime and wake blocked operations.
Process-crash recovery depends on filesystem/device flush guarantees for power
failure durability. `/status` includes channel backlog, in-flight/parked records,
blocked producers, retries and storage failures; `/metrics` exposes these counts.

The `idempotent` router (also usable as an action) guards its single downstream
branch. Set `namespace` to a configured namespace and `key` to a Simple expression,
for example `${header.orderId}`. The default is `${header.Message-Id}`. Concurrent
duplicates wait up to `claimTimeout` (default 20,000 ms). Only branch success
records completion; failures and cancelled branches release the key. Completed
duplicates stop at the guard, without executing the branch or replaying a cached
response. Durable completion keys survive restarts; unfinished reservations do
not. Completed keys expire after retention, after which the operation may execute
again. Namespace capacity fails admission instead of evicting live keys.

These features provide **at-least-once delivery**, not exactly-once external
effects. A crash between an external side effect and local completion can repeat
the effect. Pass a stable business idempotency key to external systems that
support it, and use separate namespaces for separate business operations.
Runnable examples are in [testdata/reliable](testdata/reliable/README.md).

### Queued wire taps

For timing independence, end a wire tap's detached branch with
`queue:orders.audit` and `delivery: "enqueue"`, then perform the slow work in a
separate flow with a `queue:orders.audit` source. Only enqueueing precedes the
main path; the audit work runs independently. A topic action can similarly fan
out a tap to several active consumers.

Detached taps retain their error isolation: a full buffer logs an enqueue error
and the main path continues, losing that tap copy. Work elsewhere on the tap
branch still runs sequentially. See [channel examples](testdata/examples/channels/README.md)
for competing consumers, fan-out and a slow audit consumer.

### Flow links

A flow with a `flowlink` source can be called by other flows of the same `dif`
process: its endpoint is an in-memory queue named after its flow id, so a
message sent before it starts waits until it does. The `flowlink` step sends a
copy of the message to it:

| `transport` | `exchangePattern` | The sender |
|---|---|---|
| async, seda | InOnly | does not wait: the message goes on at once |
| sync, direct, vm | InOnly | waits until the target flow has processed the copy; the message goes on unchanged, or fails if the target failed |
| any | InOut | waits for the target flow's outcome, which replaces the message |

Waiting ends after `requestTimeout` (`flow x did not reply within 20s`); a copy
the target has not taken by then is dropped, so it is never processed late.
`flowLinkOutbound.json` and `flowLinkInbound.json` show it: run both, and a
request to the outbound flow is logged by the inbound one.

Conditions (`content`, `filter`) and split expressions are written in these languages; anything
else is rejected when the flow is loaded:

| Language | Supported | Condition holds when |
|---|---|---|
| `simple` | the conditions of Camel's simple language: `==`, `!=`, `>`, `contains`, `regex`, `in`, `range`, `startsWith`, ... joined by `&&` and `\|\|` (see above). Without an operator, the expression must be `true` | the condition holds |
| `xpath` | XPath 2.0 (also functions such as `count()`, `max()`, `distinct-values()`, `year-from-dateTime()`, `if … then … else`, `for … return`): `//person[@id = 1]/name/text()`, `//*:film`, `count(//a) > 2`. Names are namespace aware, as in XPath: `*:name` is a name in any namespace, and the content router binds the prefix `ns` to its option `namespace`. A plain path of element names (`/persons/person`) is found by scanning the document, without a tree | the expression selects a node, or its value is true, a number other than 0 or a text that is not empty |
| `jsonpath` | as Jayway (Camel): `$.store.book[0].author`, `$..author` (deep scan), `[*]`, unions `['a','b']` and `[0,1]`, slices `[:2]`, filters `[?(@.price < 10 && @.isbn)]` with `==` `!=` `<` `<=` `>` `>=` `=~` `in` `nin` `subsetof` `anyof` `noneof` `size` `empty` `contains` and `!`, and the functions `.length()` `.size()` `.min()` `.max()` `.avg()` `.sum()` `.first()` `.last()` `.keys()` `.index(n)`. `@` is the root, in a filter the element tested. Members are visited in key order, not in document order | the path selects a value other than `null` or `false` |

A body that is not XML or JSON matches no xpath or jsonpath condition; a split
of such a body fails the message. An xpath that is not valid, or calls a function that does not exist,
is rejected when the flow is loaded. The XPath 2.0 processor is
[github.com/knroy/go-xml](https://github.com/knroy/go-xml) (pure Go), which also
reads the document into a tree of about 35 times its size; a DOCTYPE in the body is refused.
The text of a selected element is its XML with the namespace declarations it uses;
of an attribute, a text node or a value, the value. The `xpath` language of
`setheaders` and `settenantvariable` sets the text of the first item the expression selects.
`${xpath(expression)}` in a simple expression does the same on the body.

### Converters

The converters turn the body from one format into another; the result is text,
and `Content-Type` is set to the new format (`application/json`,
`application/xml` or `text/csv`). A body that is not the input format fails
the message. They follow the
libraries the DIL components were built on:

| Step | Options (default) | Mapping |
|---|---|---|
| `xmltojson` | `forceTopLevelObject`, `skipWhitespace`, `trimSpaces`, `skipNamespaces`, `removeNamespacePrefixes`, `typeHints` (all false) | json-lib (Camel's xmljson, version 2.4): the root is left out unless `forceTopLevelObject`; attributes are `"@name"`, namespace declarations `"@xmlns:p"` (not with `skipNamespaces`), text beside attributes or children `"#text"`; repeated elements make an array, and so does an element that holds only elements with one name (and whitespace), as json-lib decides; an empty element is an empty array. Values stay strings. With `typeHints`, the attributes `type` (`number`, `integer`, `float`, `boolean`, `string`, `function`), `class` (`object`, `array`) and `null="true"` set the type and are left out; without it they are attributes like any other. Numbers are written as Java does (`1.0` is `1`); text that is no number, with a number `type`, is null. Text that looks like a JSON array or object is parsed as such, as json-lib does |
| `jsontoxml` | `rootName` (o), `arrayName` (a), `elementName` (e), `typeHints` (false), `namespaceLenient` (no effect) | The reverse: members as elements, `"@name"` as attributes, `"#text"` as text, array items as `elementName` elements; starts with an XML declaration. `typeHints` adds `class="object"`, `class="array"`, `type="string"`, `type="number"` or `type="boolean"` to every element (null is `class="object" null="true"`), so `xmltojson` with `typeHints` can restore the JSON |
| `xmltojsonsimple` | `keepStrings`, `removeNamespaces`, `removeRoot`, `hasTypes` (false), `typeValueMismatch` NULL\|ORIGINAL\|ERROR (ORIGINAL) | `{"root": …}` unless `removeRoot`; attributes as `"@name"`, children by name, text beside them as `"jsonContent"`, repeated elements as an array, trimmed text. Numbers, `true`, `false` and `null` become JSON values unless `keepStrings`. With `hasTypes` a `type` attribute (`string`, `number`, `integer`, `double`, `boolean`, `null`) sets the type; text that does not fit becomes `null`, stays a string or, with `ERROR`, fails the conversion |
| `jsontoxmlsimple` | `addRoot` (false), `rootTag` (root), `changeArrayElements` (false), `arrayElementName` (element), `checkJsonKeys` (false) | The reverse: members as elements, `"content"` as text, an array as one element per item named after its key (with `changeArrayElements`: one element holding `arrayElementName` items), `null` as the text `null`, no declaration. A key that is not an XML name fails the message with `checkJsonKeys`, else its invalid characters become `_` |
| `csvtoxml` | `delimiter` (,), `useHeader` or `useHeaders` (false), `encoding` (UTF-8) | `<rows><row><name>value</name>…</row>…</rows>`; with `useHeader` the first record names the fields (invalid characters become `_`), else `field1`, `field2`, … `encoding` only sets the XML declaration; the `encoder` step converts the bytes |
| `xmltocsv` | `includeHeader`, `includeIndexColumn` (false), `indexColumnName` (line), `delimiter` (,), `lineSeparator` linefeed\|carriage_return\|carriage_return_linefeed\|endofline (the system's), `orderHeaders` unordered\|ordered\|ascending\|descending, `quoteFields` all_fields\|non_empty_fields\|non_integer_fields\|no_fields (no_fields), `xPathExpression` | Every child of the root, or every element `xPathExpression` selects, is a record, every child of a record a field (trimmed text); a record without children is one field. Columns in order of appearance, or alphabetical (`ordered` or `ascending`; `descending` from Z). `non_integer_fields` quotes all but whole numbers. A field holding the delimiter, a quote or a line break is always quoted |
| `formtoxml` | – | `a=1&b=2` (form-urlencoded, percent-decoded) becomes `<form><a>1</a><b>2</b></form>`: an element per field in the order of the body, repeated fields repeated; characters a name cannot hold become `_` |
| `flv` | `rules` (the DIL list, passed as JSON text), or an option per rule | Fixed-length values to XML, in the shape of the Java platform. Every non-empty line is matched with the rules: the first whose `matchOn` the line starts with cuts it into its fields, `name[length]` after each other from the start of the line (so `matchOn` is part of the first field), in characters, trimmed; a line no rule matches is left out. `<flv-message>` holds a `<rule matchOn="HDR" fields="header[3]body[5]" />` for each rule, then a `<segment>` for each line with an element per field. A line of a group rule (`group` true) opens a `<group>`, which holds its segment and those of the following lines of other rules, up to the next line of a group rule. Instead of `rules`, every other option is a rule: its name is `matchOn`, or `_group_` and `matchOn` for a group rule, its value the fields, such as `header[3]body[5]`. The rules of the list are tried in its order, the options after them with the longest `matchOn` first, then in reverse alphabetical order, as a flow cannot tell the order of its options |
| `exceltoxml` | `rules` (required; as for `flv`) | xlsx (not xls) to XML: `<workbook>` holds an element per rule, named after its `name`, else its `worksheet`, with a `<row>` per row of the rule's cells and an element per cell (`field1`, `field2`, … or the header names). A rule has `worksheet` (the first if empty), `cellRange` (`A2:C4`; the whole used range if empty), `transpose`, `headerRow` (the first row names the fields) and `discardEmpty` (leave out empty cells and rows). Values only: strings and numbers; dates are Excel's serial numbers |
| `xmltoexcel` | `includeHeader`, `includeIndexColumn` (false), `indexColumnName` (line), `orderHeaders` unordered\|ordered\|ascending\|descending, `excelFormat` xlsx, `useCustomWorksheets` (false), `worksheets` | XML to xlsx with `xmltocsv`'s mapping: each child of the root is a row, each of its children a cell. Numbers are numeric cells, all else text. With `useCustomWorksheets`, `worksheets` (a JSON list of `{name, xPathExpression}`, also as `RAW(<base64>)`) makes a worksheet per entry whose rows are the elements the path selects (the root's children if it is empty) |
| `xmltoedifact` | `edifactType` (no effect) | The XML form of an EDIFACT interchange, as Smooks writes it (`env:UNB`, `iftmin:BGM`, composites such as `c:C002`), to EDIFACT with the default delimiters, one line without breaks. An element named by three upper-case characters is a segment, its children are its elements and a child with children a composite; the elements above (interchange, message, segment groups) are walked through. It is structural: DIF has no message definitions, so an element the XML omits is not restored as an empty position (`BGM+340+347605` where `BGM+340++347605` was meant). Keep a position by leaving the element in the XML, empty |
| `docconverter` | `convert` xmltojson\|csvtoxml\|… (any of csv, xml, json, yaml to another; xmltojson) | Converts the body between CSV, XML, JSON and YAML through one tree of ordered objects. CSV is the table the platform writes, `rows`, a `row` per record, an `item` per cell, all text (`{"rows":{"row":[{"item":["a","b"]}]}}`); to CSV only such a table converts (cells quoted where CSV needs it), anything else fails the message. XML is read as `xmltojsonsimple` reads it and written as `jsontoxmlsimple` writes it; CSV to XML starts with `<?xml version='1.0' encoding='UTF-8'?>`. JSON and YAML keep their types; XML and CSV have none, so they stay text in JSON and a number, `true`, `false` or `null` is that value in YAML. YAML is written as Jackson writes it: `---`, strings in double quotes; from XML its members come in the order of a Java HashMap, as the platform's output does. A YAML document is read with the first of several, aliases resolved |

The Kamelets only pass these options on to Assimbly's components, so where a
detail is not defined by json-lib or org.json (the CSV element names, the
`hasTypes` type names, `checkJsonKeys`), DIF's choice is the one above. That
goes for the XML shapes of `formtoxml`, `exceltoxml` and `xmltoedifact`
too: their Java code is not in this repository. The shapes of `flv`,
`xmltojson` and `xmltojsonsimple` follow the answers of the platform in the
regression tests.

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
becomes a message (body = request body; request headers = message headers; query
parameters = message headers too, so `?config=A` sets the header `config`, unless a
request header has that name, and `body` and `metadata.*` are never set from the
query; plus `http.method`, `http.path`, `http.query` and `http.uri` with
`preserveHttpHeaders`), and the caller gets the final message body back, with
its `Content-Type` header (default `text/plain; charset=utf-8`) and the other
headers of the message, as Camel's HTTP consumers return them. Not returned
are `body`, `metadata.*`, `http.*` and `error.*`, the headers of one HTTP hop
(`Connection`, `Content-Length`, `Host`, `Transfer-Encoding`, ...), the
credentials `Authorization`, `Proxy-Authorization` and `Cookie`, and `Date`;
neither are values that have no text form (maps, lists, bytes). Line breaks in a
value become spaces. A flow that wants a header out of the reply
sets it; one that wants to keep the request's headers private starts with
`removeheaders`.

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

The `https` action calls an endpoint with the message: the body (with POST,
PUT, PATCH and DELETE) and its string headers, except those that `excludeHeaders`
matches. It maps the trace ID to `DIF-Trace-Id` and never
sends other `metadata.*` or `http.*` headers. The response sets
the body, `http.status` and `Content-Type`. An error status fails the message
only with `throwExceptionOnFailure` (or `useErrorRoute`, the designer's name for
it). With `authMethod` Basic the credentials go with every request. With
`retryRequests` a call that cannot connect, or that the server answers with 503,
is tried again `retryAttempts` times, `retryInterval` apart. Mutual TLS
(`authMethod` MutualSSL, `mutualTls`) is not supported yet, and a flow that asks for it
does not load.
Cookies in the cookie store (see `setcookie`) for the host and path go along,
and cookies the server sets are kept, for all flows of the process. An empty
`trustStoreFile` trusts the system's root certificates instead of a trust store.

The `rest` source is the https source for one method on a path of the REST
address (`0.0.0.0:9002` by default, as Camel's rest configuration in the Java
platform); all rest sources share it, and other methods get 405. The `rest`
action calls `host` + `path` (by default this dif's REST address).

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
> run testdata/examples/httpsInbound.json
$ curl -k -d hello https://localhost:9001/_new2/httpsInbound
12345
```

### Encrypted values

Any option can hold a password, token or API key as an encrypted value,
`ENC(salt|iv|cipher)`, in the format of the Java `EncryptionUtil` of the
platform: each part in base64, AES-256-CBC with PKCS#5 padding, and a key
derived from the password and the salt with PBKDF2WithHmacSHA1 (10000
iterations). Flows made for the Java platform work as they are.

```json
"options": { "password": "ENC(MUIgE3IHqgPmUQ9qyyOdtw==|3v7+OIgbaGdiodkVvrY4XQ==|jjpsKDsxY7aaQYFZU5yz8A==)" }
```

(That value is `hunter22`, encrypted with the password `vector-password-1`; the
Java platform made it, and the tests decrypt it.)

The password is `DIF_ENCRYPTION_PASSWORD`, or the file that
`DIF_ENCRYPTION_PASSWORD_FILE` names (a mounted secret; one trailing line feed is
removed). Values are decrypted when the flow is loaded, in the options that a
step is built with; a value may also be part of a longer text, such as a URI
(`...?password=ENC(...)`). Validation and `describe` do not decrypt, need no
password, and show no option values. Text that only looks like `ENC(...)` is
left as it is. A flow with an encrypted value is rejected when there is no
password or when it is wrong (the error names the option and never shows the
value). The format has no integrity check: about one wrong password in 256 does
not fail but gives garbage, so a wrong password may only show when the remote
system refuses the login.

The value `ENC(...)` in `core.connections` is not read: DIF does not use connection
blocks.

### FTP, FTPS and SFTP

The `ftp`, `ftps` and `sftp` steps work on a directory of a remote server as the `file`
steps do on a local one, and are the same apart from the connection:

- The URI is `ftp:[//][user@]host[:port]/directory`. The directory is below the
  login directory; `ftp:host//a/b` is the absolute `/a/b`. Ports default to 21
  (`ftps`: 990 with `implicit`) and 22. `RAW(...)` around a value (as DIL writes passwords and folder names) is
  removed.
- The **source** polls every `delay` ms, reads up to `maxMessagesPerPoll` files
  and emits a message each: the body is the content, `file.name` the path below
  the directory. Once the flow has processed a file, it is deleted (`delete`) or
  moved to the folder `move` below the directory, keeping its place in it; if
  processing failed, it is moved to `moveFailed` (empty leaves it, so it is
  consumed again). Entries starting with a dot are skipped, and so are the move
  folders in a recursive search. A failed poll (a server that is down, a login
  that fails) is logged and tried again. A file that could not be moved is not
  consumed again until it changes, which avoids a loop of duplicates. With
  `readLock` changed, a file is taken only if it is unchanged since the last poll.
- The **sink** writes the body to the file named by `fileName`, else the header
  `file.name`, else `CamelFileName`, else the trace id. A name must stay below the
  directory (no `..`). `Append` adds to the file, `Fail` and `Ignore` check for it first.
- **`ftpenrich`** and **`sftpenrich`** replace the body with the content of the
  first file the options select, then delete it or move it to `move` (not if
  `move` is empty). Without a file the message passes on unchanged, or fails with
  `abortMode`.
- `disconnect` false keeps the connection open between uses, and closes it
  after 30 seconds idle; a failed use opens a new one.
- Not supported, and rejected: `implicit` on `ftp` (use `ftps`), active FTP
  (`passiveMode` false). Other options of the Java platform (`stopIfNoFileFound`,
  `maxMessagesPerPoll` of an enricher, `hostName` and `port`, which the URI
  gives) have no effect or are not offered.
- **`ftps`** (source, sink and `ftpsenrich`) is FTP over TLS (RFC 4217), as Camel's
  `ftps` component: explicitly, `AUTH TLS` before the login, or with `implicit`
  true TLS from the first byte; then `PBSZ 0` and `PROT P`, so the data
  connections are TLS too. They resume the TLS session of the control connection,
  which servers often require, and their handshake follows the transfer command.
  The server's certificate is checked against `trustStoreFile` (a PKCS#12 trust
  store, `security/outbound-truststore.p12` by default, as for the https steps;
  `trustStorePassword`, env `DIF_TRUSTSTORE_PASSWORD`), or the system's roots if
  it is empty. The password comes from `DIF_FTPS_PASSWORD` if the option is not
  given. A server that does not do TLS fails the connection.
- FTP is plain text: the password and the files cross the network unencrypted.
  It is written with the standard library: passive mode (EPSV, then PASV, always
  to the address it connected to), and MLSD or, if the server has no MLSD, `LIST`
  in the Unix or DOS format. Symbolic links are left out.
- SFTP logs in with the password and/or the private key. **The server's key is
  checked** against `knownHostsFile` (default `~/.ssh/known_hosts`), and a
  missing file or an unknown server fails the connection with a hint; the Java
  platform does not check. `strictHostKeyChecking` false turns the check off,
  which leaves the connection open to impersonation. SFTP uses
  `github.com/pkg/sftp` and `golang.org/x/crypto/ssh`.
  `socketTimeout` bounds connecting and logging in; an operation on an open SFTP
  connection ends when the flow stops.

### Example flows

`testdata/examples/` holds the example flows and `testdata/regression/` the flows
of real use cases, grouped as in the designer. Every flow in them builds, which
`go test ./test/dil` checks (given the keystores that test sets up); flows that
need a step DIF does not have yet (rabbitmq, groovy, ...) are not kept.
`setoauth2-CustomForBVG` and `setoauth2-GoogleDrive` need an endpoint and a
client, in the options or the environment (see `oauth2token`). The flows
`testdata/hello.json` and `testdata/timer.json` are DIF's own, used by the tests
and the examples in this README.

## Packages

| Package            | Role                                                                     |
|--------------------|--------------------------------------------------------------------------|
| `message`          | `Message`: one map with the body, headers and `metadata.*` headers        |
| `steps/definition` | Processor contracts (`SourceProcessor`, `ActionProcessor`, `RouterProcessor` with `Route` and `Link`, `Gatherer` with `Outcome`, `Releaser`, `Looper`, `SinkProcessor`) and `Definition` |
| `steps/registry`   | Processor registry by URI scheme and kind; JSON Schema validation of step options; gives routers their links |
| `steps/impl`       | Built-in steps (timer, repeater, counter, file, https, log, setbody, setheader, setheaders, removeheaders, replace, simplereplace, base64totext, base64tobinary, texttobase64, binarytobase64, setbodyasstring, zip, unzip, throttle, encoder, passthrough, message, queue, deadletter, flowlink, setuuid, setbodybyheader, setheaderbybody, delay, logger, simplevalidator, wastebin, rest, graphql, smtp, smtps, jsonvalidator, xmlvalidator, schematron, xslt, velocity, fileenrich, settenantvariable, gettenantvariable, removetenantvariable, oauth2token, googledrive, setcookie, removecookie, multipart, the converters xmltojson, jsontoxml, xmltojsonsimple, jsontoxmlsimple, csvtoxml, xmltocsv, docconverter, editoxml, xmltoedi, xmltoedifact, formtoxml, flv, exceltoxml, xmltoexcel, and the routers wiretap, recipient, content, if, loop, dowhile, filter, split, enrich, aggregate, splitandaggregate) and their schemas; the simple language, XPath 2.0 and the jsonpath subset |
| `keystore`         | Reads PKCS#12 keystores: server identity and trust store                 |
| `flows/definition` | Internal flow model (`Flow`, `Node`, `ErrorHandler`), independent of any DSL |
| `flows/impl`       | Parses DIL JSON, validates links, builds the flow model                  |
| `engine`           | `Run` takes one message through a flow, with redelivery and the error route; `Runner` holds the lifecycle; `Engine` is the registry of flows by id |
| `api`              | Public entry point: `api.Load(path, onResult)` returns a `Flow` with lifecycle methods and `Send`; `api.NewEngine()` manages several flows; `api.RegisterStep` adds steps |
| `cli`, `cmd/dif`   | `dif` opens the interactive shell with `load`, `run`, `send`, `log`, `list`/`ps`, `stats`, `catalog` and lifecycle commands on stdin; one log file per flow |

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
- A step URI `<step>:ref:<name>` (such as `jsonvalidator`) refers to the core
  resource `<name>` (`dil.core.resources`); the parser passes its content to
  the step as the option `resource`, and the URI becomes `<step>`.
- A `queue` source without a queue name gets its flow's id as `path`.
- A `router` step has one or more outbound links. The attributes of an
  outbound link, `rule` (its role, such as `wiretap` or `split`), `language`
  and `expression` (its condition), are kept in the flow model (`Node.Links`)
  and given to the router; `pattern` is ignored.
- The `error` step (`failedexchange`, at most one per flow) becomes the flow's
  error handler: its options `maximumRedeliveries` and `redeliveryDelay` (or
  `redeliveryAttempts` and `redeliveryInterval`, used when the first are
  absent) set the redelivery, and its outbound link, if any, starts the error
  route. It has no inbound link.
- DIL exports some steps with the URI `unknown`; the parser names them by an
  option only that step has: `deadLetterQueue` makes it `deadletter`,
  `targetFlowId` a `flowlink` step, and `transport` on a source a `flowlink`
  source, which also gets the option `flowId` (the flow's id). A `rules` list
  whose rules have `subcollection` makes the step `flv`, one whose rules have
  `worksheet` `exceltoxml`; both get the list as JSON text, since step options
  are scalar. An `unknown` action with no options at all is `formtoxml`. Other
  `unknown` steps stay unknown and are rejected.

## Tests

`go test ./...` runs everything. Unit tests sit beside the code they test
(`*_test.go`), since Go compiles a package's tests with the package and they can
reach what it does not export. Tests that use only the exported API of a package
are in `test/<package>`, and `test/dil` builds every DIL flow in `testdata/` and
checks that the fixtures hold no credentials. The fixtures are in `testdata/`.
See [test/README.md](test/README.md).

## Future work

- More steps (`pdftotext`, `headerstopdf`, `edifacttoxml`, the AI and `jolt`/`jslt`/`jsonata` family, `awss3`, `restopenapi`); multiple flows per file
- Message definitions for `xmltoedifact` (EDIFACT directories such as d96a),
  to restore the positions of omitted elements, and a matching EDIFACT to XML
- Persisted tenant variables (the seam is `tenantStore`) and cookies
- A CLI command to inspect/replay durable queues and parked dead letters
- Aggregation by correlation key (the aggregate has one group), and groups
  that survive a restart
- More expression languages and simple-language functions (`${exchangeId}`, …)
- More error handling: exponential backoff, retrying only some errors, keeping the original message
- Concurrent message execution within a flow (processors are already safe for it)
- A separate engine process with a network API for clients
- Persistence for other processor state beyond durable queues and idempotency keys
