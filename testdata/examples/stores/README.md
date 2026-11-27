# Stores

Run `go run ./cmd/dif` from the repository root and load these flows in the
same process. The stores are in memory: they live as long as the process.

```text
load testdata/examples/stores/rates.dil testdata/examples/stores/price.dil
start rates
start price
request price
```

`rates` puts the EUR rate in the store `rates`; `price` reads it with the
`store` step and with `${store:rates:${header.currency}}`. Request `price`
before `rates` has run and the rate is `unknown`.

```text
load testdata/examples/stores/order-lines.dil
start order-lines
request order-lines
request order-lines
```

The two lines of order `A-1` make one group; the second request completes it
and the aggregate `[{"sku":"BOOK-1","quantity":1},{"sku":"BOOK-1","quantity":1}]`
goes on to the log. A service's `/status` and `/metrics` count the keys of
each store.

For a durable store, which survives a restart, see
[testdata/reliable](../../reliable/README.md#durable-stores).
