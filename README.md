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
- every message: its content and trail, or why it failed
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
> load examples/log.json
  error: flow 6970d9a1c9b9a4000d000053: step 492db687-…: no processor for "https" (source)
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
- A failing message is reported and the flow continues with the next one.
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
    ├── RouterProcessor  Route(ctx, m) routes  picks next links / message paths (defined; not run yet)
    └── SinkProcessor    Consume(ctx, m)       consumes a message, normally ending its path
```

- A source is not executed per message: it runs for the whole run of the flow
  and emits a message per tick, file, …
- Processors return errors to the engine and never retry; error handling is the
  engine's job. They honour `ctx`. A message the flow has taken completes even
  when the flow is stopped meanwhile, unless the stop is forced. They log with
  `stepdef.Logger(ctx)`, the flow's logger.
- Processor instances are shared by all messages of a flow and are safe for
  concurrent use (today a flow still processes one message at a time).
- An action position may use a sink processor (the message passes on unchanged
  after it is consumed, like Camel's `file-action`); a sink position may use an
  action processor.

## Steps

The scheme of a step's `uri` selects its processor in the registry
(`steps/registry`): `file:/data/in` is the step `file` with `path` `/data/in`.
Each step has a JSON Schema for its `options` (`steps/impl/schemas/<name>-<kind>.json`,
modelled on the Kamelet properties). When a flow is loaded, every step is
checked:

- a step without a registered processor rejects the flow:
  `no processor for "https" (source)`
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

Language `constant` is the literal text; `simple` replaces `${body}`,
`${header.<name>}` and `${headers.<name>}` (other `${…}` expressions are
rejected when the flow is loaded).

New steps plug in without touching the engine:

```go
api.RegisterStep(api.StepDefinition{
	Name:   "upper",
	Kind:   "action",
	Schema: []byte(`{"type": "object", "additionalProperties": false}`),
	New:    func(stepID string, p stepdef.Params) (stepdef.Processor, error) { return upper{}, nil },
})
```

Of the examples, `hello.json`, `timer.json` and `fileInbound.json` load; the
others use steps that have no processor yet (https, setheaders, …) and are
rejected.

## Packages

| Package            | Role                                                                     |
|--------------------|--------------------------------------------------------------------------|
| `message`          | `Message`: one map with the body, headers and `metadata.*` headers        |
| `steps/definition` | Processor contracts (`SourceProcessor`, `ActionProcessor`, `RouterProcessor`, `SinkProcessor`) and `Definition` |
| `steps/registry`   | Processor registry by URI scheme and kind; JSON Schema validation of step options |
| `steps/impl`       | Built-in steps (timer, file, log, setbody, setheader, passthrough, message) and their schemas |
| `flows/definition` | Internal flow model (`Flow`, `Node`), independent of any DSL             |
| `flows/impl`       | Parses DIL JSON, validates links, builds the flow model                  |
| `engine`           | `Run` takes one message through a flow; `Runner` holds the lifecycle; `Engine` is the registry of flows by id |
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
- `error` steps are skipped; `router` steps are rejected.

## Future work

- More sources and steps (https, queue, quartz, setheaders, …) and routers,
  splitters, aggregators; multiple flows per file
- More expression languages and simple-language functions
- Error channels (`error` steps and the flows hanging off them), retry policies in the engine
- Concurrent message execution within a flow (processors are already safe for it)
- A separate engine process with a network API for clients
- State: `Message` is JSON-serializable, so it can be persisted later; nothing is persisted now
