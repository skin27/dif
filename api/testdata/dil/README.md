# DIL text example

The program of [`../services/`](../services/README.md), written as DIL text
(`.dil` files) instead of DIL JSON. It is the same program: DIF compiles each
file to DIL JSON when it reads it, and runs the same flows.

From the repository root:

```text
go run ./cmd/dif validate api/testdata/dil/orders.dil
go run ./cmd/dif describe api/testdata/dil/orders.dil
go run ./cmd/dif
> run api/testdata/dil/orders.dil
> request submit-order
> request submit-out-of-stock
> exit
```

`contracts.dil` declares the schemas, formats and services; `provider.dil`
implements the services; `orders.dil` calls them. The files use what JSON cannot:
comments, raw strings for the JSON Schemas and the mapping, and chains of steps
connected by `->` instead of hand-matched link ids.

`dif convert api/testdata/services/orders.json` prints the JSON file as DIL text,
and `dif convert api/testdata/dil/orders.dil` prints the text as DIL JSON. See
[docs/dil-language.md](../../../docs/dil-language.md).
