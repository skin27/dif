# Events

`failing.json` emits a number every three seconds and fails the odd ones.
`alerts.json` has an `event:exchange.failed` source for that flow and logs each
event, with the `event.*` and `error.*` headers and the identity of the message
that failed (its `Correlation-Id`, and its `Message-Id` as `Causation-Id`).

```sh
go run ./cmd/dif run --dir testdata/examples/events
```

Other types: `flow.started`, `flow.paused`, `flow.resumed`, `flow.stopped`,
`source.failed`, `exchange.completed`, `exchange.handled`, or `*` for all. An
event never carries the payload of the message.
