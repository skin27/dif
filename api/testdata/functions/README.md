# Shared function examples

From the repository root:

```powershell
go run ./cmd/dif validate api/testdata/functions/orders.json
go run ./cmd/dif start api/testdata/functions/orders.json
```

In the shell, request each flow:

```text
request function-simple
request function-invoice
request function-summary
request function-text
```

Expected bodies are `ACME`, `{"customer":"ACME","total":0.3}`,
`{"customer":"ACME","labels":["new","paid"]}`, and
`Configured once, instantiated per step`.

`library.json` is a core-only module containing shared functions, two mappings,
and reusable actions. `orders.json` imports it as `common`. The invoice calculation
uses exact decimal arithmetic. Each processor use builds its own action instance.
