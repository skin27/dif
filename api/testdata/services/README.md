# Service contracts example

From the repository root:

```text
go run ./cmd/dif validate api/testdata/services/contracts.json
go run ./cmd/dif validate api/testdata/services/orders.json
go run ./cmd/dif describe api/testdata/services/orders.json
go run ./cmd/dif
> run api/testdata/services/orders.json
> request submit-order
> request submit-out-of-stock
> exit
```

`contracts.json` is a standalone contract module with no flows. It declares JSON
Schema-backed formats (`OrderIn`, `OrderOut`, `OrderProblem`), a `text/plain`
format (`AuditNote`), the `InOut` capability `PlaceOrder` (with typed
`inputHeaders` and `outputHeaders`), and the `InOnly` capability `RecordAudit`.

`provider.json` imports `contracts.json` as `contracts` and implements both
capabilities with `service:contracts:PlaceOrder` and `service:contracts:RecordAudit`
sources. `orders.json` imports `contracts.json` as `orders` and invokes
`service:orders:PlaceOrder` — resolved by canonical declaration identity across
different import aliases:

- `submit-order` sends a valid order, receives `{"orderId":"ORD-BOOK-1","sku":"BOOK-1","quantity":2,"priority":1}`
  with header `Order-Status: ACCEPTED`, and triggers a `text/plain` audit note.
- `submit-out-of-stock` triggers a validated `OrderProblem` fault via `return`
  (`status: "error"`), matched by `errorClass: "*FaultError"` with structured
  `error.body` and `error.contentType` headers.

See [docs/service-contracts.md](../../../docs/service-contracts.md) for full syntax
and validation semantics.
