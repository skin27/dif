# DIF — Data Integration Framework

A minimal, dependency-free Go prototype of an integration framework built on
Flow-Based Programming, Enterprise Integration Patterns and DIL
(Data Integration Language). Background: [Integration Language Design](https://raymondmeester.medium.com/integration-language-design-da4cf51a05c0).

This MVP proves one architecture:

```text
DIL JSON  →  flow model  →  engine  →  steps  →  result Message
```

## Run

A flow is a long-running task: once started, it runs until you stop it (or
`dif` exits). Messages are sent to a running flow separately. `dif start`
opens the CLI: you type commands after the `> ` prompt, and the program's
answers are indented below them.

| Command        | Effect                                                                  |
|----------------|-------------------------------------------------------------------------|
| `send`         | Send the flow's configured message (`dil.core.messages`) into the flow  |
| `send <body>`  | Send the configured message with `<body>` as body                       |
| `start`        | Start the flow (again, after `stop`)                                    |
| `pause`        | Pause: the flow takes no new messages until it is resumed               |
| `resume`       | Resume a paused flow                                                    |
| `stop`         | Stop the flow; you stay in the CLI                                      |
| `status`       | Show the flow's state and message counts                                |
| `help`         | List all commands                                                       |
| `exit`         | Stop the flow and exit `dif` (Ctrl+C does the same)                     |

```text
$ go run ./cmd/dif start examples/hello.json
DIF CLI: flow examples/hello.json is started.
Type a command at the "> " prompt; "help" lists all commands.
> send
  message 1: {"traceid":"…","timestamp":"…","headers":{"greeting":"hello"},"body":"HELLO WORLD"}
    trail: source:hello-source -> action:hello-action -> sink:hello-sink (0 ms)
> stop
  flow stopped
> status
  flow is stopped: 1 messages processed, 0 failed
> start
  flow started
> exit
  exit: 1 messages processed, 0 failed
```

Output from the flow can arrive while you type, for example from a timer.
It is printed above a fresh prompt. When stdin ends (for example
`dif start flow.json < /dev/null`), `dif` keeps running until Ctrl+C.

`examples/timer.json` has a timer source that produces a message every second
by itself; `pause` holds it and `resume` continues it.

The exit code is 0 when every message succeeded, 1 when a message or the flow
failed, and 2 for bad usage.

```bash
go vet ./...
go test ./...
```

## Lifecycle

```text
Stopped --Start--> Started --Pause--> Paused --Resume--> Started
Started | Paused --Stop--> Stopped
```

- `Start` runs the flow in the background. It processes messages one at a time,
  from its source or from `Send`, until `Stop` is called. A source that runs
  out of messages does not stop the flow.
- `Pause` stops the flow from taking new messages: `Send` is refused and the
  source waits. A message already in a step completes.
- `Resume` continues a paused flow; `Stop` ends it and waits until it has finished.
- A failing message is reported and the flow continues with the next one.
- A stopped flow can be started again.

```go
f, err := api.Load("examples/hello.json", func(res *api.Result, err error) { /* per message */ })
f.Start()
f.Send(f.NewMessage())
f.Pause()
f.Resume()
f.Stop()
```

## Sources

The source node's URI selects what produces messages inside the flow:

| URI                    | Behavior                                                                       |
|------------------------|--------------------------------------------------------------------------------|
| `timer` / `timer:name` | Emits the counter (1, 2, 3…) as the body every `period` ms (default 1000), `numbers` times (default unlimited) |
| anything else          | Produces nothing by itself (a stand-in until real sources exist); messages arrive through `Send` |

## Packages

| Package            | Role                                                                     |
|--------------------|--------------------------------------------------------------------------|
| `message`          | `Message`: traceid, timestamp, key/value headers, arbitrary body          |
| `steps/definition` | `Step` (`Execute(*Message) (*Message, error)`) and `Source` contracts     |
| `steps/impl`       | Concrete steps (`Passthrough`) and sources (`Timer`)                     |
| `flows/definition` | Internal flow model (`Flow`, `Node`), independent of any DSL             |
| `flows/impl`       | Parses DIL JSON, validates links, builds the flow model                  |
| `engine`           | `Run` takes one message through a flow; `Runner` holds the lifecycle     |
| `api`              | Public entry point: `api.Load(path, onResult)` returns a `Flow` with lifecycle methods and `Send` |
| `cli`, `cmd/dif`   | `dif start <flow.json>` with `send` and lifecycle commands on stdin      |

The engine depends only on the flow model and the `Step` and `Source`
interfaces. New steps and sources plug in through `steps/impl` without touching
the engine.

## How DIL maps to the model

- Steps are connected by link ids: a step's `bound: "out"` link id matches the
  `bound: "in"` link id of the next step.
- Because DIL JSON is converted from XML, a single child (`flow`, `link`, …)
  may be an object instead of an array; both are accepted.
- `error` steps are skipped; `router` steps are rejected.

## Future work

- Real sources (https, file, queue, …) and cron/quartz scheduling
- Real EIP steps (setbody, log, …) selected by step URI
- Routers, splitters, aggregators, multiple flows per file
- Error channels (`error` steps and the flows hanging off them)
- Managing several flows in one long-running process
- State: `Message` is JSON-serializable, so it can be persisted later; nothing is persisted now
