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

## Durable stores

`stores-service.json` runs `visits.dil`, which declares two durable stores:
`visits`, with a counter, and `tenant.default`, the store of the default
tenant's variables.

```text
go run ./cmd/dif validate --config testdata/reliable/stores-service.json
go run ./cmd/dif run --config testdata/reliable/stores-service.json
```

The timer runs once: it reads the counter (0 the first time), adds one, puts
it back, sets the tenant variable `lastVisit` and logs `visit 1`. Stop the
service with Ctrl+C and run it again: it logs `visit 2`. The entries are in the
journal in `data/channels/stores-example`, which is ignored by Git; delete that
directory to start from 0. With `--monitor-address 127.0.0.1:9090`, `/status`
lists both stores with their key counts and `/metrics` has
`dif_store_keys{store="visits"}`.

Tenant variables in a durable store are written to the journal as they are: an
access token that an `oauth2token` step keeps there is on disk in that
directory.

## Permissions

`permissions.json` runs the example flow `testdata/examples/permissions.json`
(a file source on `inbox`, an https call to `api.example.com` and a file sink on
`outbox`) under a policy that grants exactly what it can touch; see
[Capabilities and permissions](../../README.md#capabilities-and-permissions):

```text
go run ./cmd/dif describe testdata/examples/permissions.json
go run ./cmd/dif validate testdata/examples/permissions.json --sandbox
go run ./cmd/dif validate testdata/examples/permissions.json --allow-net api.example.com:443 --allow-fs-read inbox
go run ./cmd/dif validate --config testdata/reliable/permissions.json
```

The first validation reports four `not-permitted` errors, the second the two
writes it does not grant, the third none. `run` instead of `validate` refuses to
start with the same messages, or starts.
