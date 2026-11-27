# Hermetic Flow Testing and Tracing Example

This directory demonstrates declarative flow testing (`dif test`) and step-diff
tracing (`dif trace`) without network sockets, keystores, or credentials.

- `orders.dil` defines:
  - `orders`: an HTTPS-triggered flow that extracts `$.id` and calls an outbound
    HTTPS payment step (`chargePayment`), with an `on error` fallback route.
  - `normalize`: an in-process text normalization flow suitable for `dif trace`.
- `orders_test.dil` imports `./orders.dil` and tests both the happy path and the
  error-handling path of `orders` by mocking `chargePayment`.

## Run the tests

```sh
go run ./cmd/dif test api/testdata/testing/orders_test.dil
go run ./cmd/dif test api/testdata/testing --output json
```

## Trace a flow

```sh
go run ./cmd/dif trace api/testdata/testing/orders.dil --flow normalize --body "hello dif" --header tenant=acme
```
