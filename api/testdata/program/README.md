# DIL program example

From the repository root:

```text
go run ./cmd/dif validate api/testdata/program/orders.json
go run ./cmd/dif describe api/testdata/program/orders.json
go run ./cmd/dif
> run api/testdata/program/orders.json
> request program-orders
> request program-ping
> exit
```

The first request validates the configured customer JSON against the imported
schema, calls the imported worker, and receives the same body with the `origin`
header set to `shared-core`. The ping returns `pong`.

`orders.json` has two integrations and three flows. `worker.json` adds one flow;
both import `common.json`, which is resolved once. Its connection is a dummy
SQL2 configuration illustrating reusable connections; these flows do not open it.
Import aliases affect core references only. All flow IDs remain literal.

The equivalent foreground service command is:

```text
go run ./cmd/dif run --file api/testdata/program/orders.json
```

Message sources wait for input; starting this example does not emit a message.
