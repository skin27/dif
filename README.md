# DIF — Data Integration Framework

A minimal, dependency-free Go prototype of an integration framework built on
Flow-Based Programming, Enterprise Integration Patterns and DIL
(Data Integration Language). Background: [Integration Language Design](https://raymondmeester.medium.com/integration-language-design-da4cf51a05c0).

This MVP proves one architecture:

```text
DIL JSON  →  flow model  →  engine  →  steps  →  result Message
```

## Run

```bash
go run ./cmd/dif run examples/hello.json
```

```text
trail: source:hello-source -> action:hello-action -> sink:hello-sink
message: {"traceid":"…","timestamp":"…","headers":{"greeting":"hello"},"body":"HELLO WORLD"}
flow has been executed in 0 milliseconds
```

```bash
go vet ./...
go test ./...
```

## Packages

| Package            | Role                                                                     |
|--------------------|--------------------------------------------------------------------------|
| `message`          | `Message`: traceid, timestamp, key/value headers, arbitrary body          |
| `steps/definition` | `Step` interface: `Execute(*Message) (*Message, error)`                  |
| `steps/impl`       | Concrete steps. For now every node gets a `Passthrough`                  |
| `flows/definition` | Internal flow model (`Flow`, `Node`), independent of any DSL             |
| `flows/impl`       | Parses DIL JSON, validates links, builds the flow model                  |
| `engine`           | Runs a flow from source to sink; stops on the first error                |
| `api`              | Public entry point: `api.Run(path)`                                      |
| `cli`, `cmd/dif`   | `dif run <flow.json>`                                                    |

The engine depends only on the flow model and the `Step` interface. New steps
plug in through `steps/impl.New` without touching the engine.

## How DIL maps to the model

- Steps are connected by link ids: a step's `bound: "out"` link id matches the
  `bound: "in"` link id of the next step.
- Because DIL JSON is converted from XML, a single child (`flow`, `link`, …)
  may be an object instead of an array; both are accepted.
- The first `dil.core.messages.message` is the initial message fed into the source.
- `error` steps are skipped; `router` steps are rejected.

## Not in the MVP (future work)

- `start` / `stop` / `pause` of long-running flows (needs real sources and async)
- Real EIP steps (setbody, log, …) selected by step URI
- Routers, splitters, aggregators, multiple flows per file
- Error channels (`error` steps and the flows hanging off them)
- State: `Message` is JSON-serializable, so it can be persisted later; nothing is persisted now
