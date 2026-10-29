# Core mapping example

From the repository root:

```text
go run ./cmd/dif validate api/testdata/mapping/orders.json
go run ./cmd/dif describe api/testdata/mapping/orders.json
go run ./cmd/dif
> run api/testdata/mapping/orders.json
> request map-order
> request map-csv
> exit
```

`map-order` returns a JSON order with ID `9007199254740993`, trimmed customer name
`Ada`, default country `NL`, and one line with SKU `PART-A` and quantity `2`.
`map-csv` invokes the same normalization map and then writes:

```csv
sku,quantity
PART-A,2
```

The flow imports `maps.json`, which privately imports `formats.json`. Mapping
source lives in a core resource; named maps and formats live in their respective
core collections. The caller does not need to import the maps' private contracts.
`ReadXML` is a reusable XML-to-JSON map, also compiled by validation, that accepts:

```xml
<Order id="42"><Customer>Ada</Customer><Item sku="part-a" quantity="2"/></Order>
```

It can precede `NormalizeOrder` in a flow receiving XML. The example's message
sources wait for input; starting them does not generate messages automatically.
See the [language reference](../../../docs/mapping-language.md) for syntax,
format contracts, limits, and supported behavior.
