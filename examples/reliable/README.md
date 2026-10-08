# Durable orders with backpressure

From the repository root:

```text
go run ./cmd/dif validate --config examples/reliable/service.json
go run ./cmd/dif run --config examples/reliable/service.json
```

The timer sends five orders into a durable queue with space for two waiting
messages. The slower consumer forces the producer to wait for admission, up to
five seconds. Each order's processing is guarded by its `orderId`.

After the five messages finish, stop the service with Ctrl+C. Run it again:
the timer generates the same five business identifiers, and the persisted
idempotency keys suppress the logging branch for 24 hours. The service stays
running until stopped, even after its timer completes.

State lives in `data/channels/reliable-example`, which is ignored by Git. Only
one process can use that directory. Use a different `channels.directory` for a
fresh demonstration. Unacknowledged messages recover after a process crash;
external effects may repeat if the process fails before local completion.

`orders.DLQ` is created as a durable queue automatically. The example does not
consume dead letters; they remain stored. Add a queue-source flow for that name
when ready to process them. Topics remain in memory but can use the same
`overflow` and `enqueueTimeout` producer options and per-topic capacity.

To inspect channel counts, add `--monitor-address 127.0.0.1:9090` and read
`/status` or `/metrics`. Monitoring never includes message bodies or idempotency
keys. Use a private monitoring endpoint in deployments.
