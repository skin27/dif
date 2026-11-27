# Durable orders with backpressure

From the repository root:

```text
go run ./cmd/dif validate --config testdata/reliable/service.json
go run ./cmd/dif run --config testdata/reliable/service.json
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

## Served channels

`stomp-service.json` serves the durable queue `incoming`, its dead-letter
queue and the topic `audit` to STOMP 1.2 clients on `127.0.0.1:61613`. The
flow `stomp-relay.json` takes each message from `incoming`, adds the header
`relayedBy: dif` and publishes it on `audit`. The passcode of the login
`orders-app` comes from the environment; choose your own:

```text
export DIF_STOMP_PASSWORD=choose-a-passcode
go run ./cmd/dif validate --config testdata/reliable/stomp-service.json
go run ./cmd/dif run --config testdata/reliable/stomp-service.json
```

Any STOMP 1.2 client over TCP works. Without one, frames can be typed with a
raw TCP tool such as `ncat`; every frame ends with a NUL byte (`\0` below).
Subscribe to the topic in one terminal:

```sh
(printf 'CONNECT\naccept-version:1.2\nhost:dif\nlogin:orders-app\npasscode:%s\n\n\0' "$DIF_STOMP_PASSWORD"
 printf 'SUBSCRIBE\nid:audit\ndestination:/topic/audit\n\n\0'; sleep 60) | ncat 127.0.0.1 61613
```

and send to the queue in another:

```sh
(printf 'CONNECT\naccept-version:1.2\nhost:dif\nlogin:orders-app\npasscode:%s\n\n\0' "$DIF_STOMP_PASSWORD"
 printf 'SEND\ndestination:/queue/incoming\ncontent-type:text/plain\norderId:42\nreceipt:sent\n\nhello\0'
 printf 'DISCONNECT\nreceipt:bye\n\n\0'; sleep 1) | ncat 127.0.0.1 61613
```

The sender gets a `RECEIPT` once the message is journaled; the subscriber gets
a `MESSAGE` with the body `hello` and the headers `orderId:42` and
`relayedBy:dif`. Subscribing to `/queue/incoming.DLQ` with `ack:client` shows
messages the flow failed three times. The listener only accepts loopback
connections here; to listen on other addresses add
`"tls": {"identity": "security/server-identity.p12"}` to the `stomp` block and
set `DIF_SERVER_IDENTITY_PASSWORD`.
