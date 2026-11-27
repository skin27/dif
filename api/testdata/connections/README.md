# Typed connection examples

From the repository root:

```text
go run ./cmd/dif validate api/testdata/connections/orders.json
go run ./cmd/dif describe api/testdata/connections/orders.json
```

`common.json` is a core-only module with three typed connections: an SFTP server, an
HTTPS API and a PostgreSQL database. `orders.json` imports it as `common`, and each of its
flows refers to one of them: `sftpenrich` and `https` with the option `connection`, `sql2`
with `dataSource`. The `https` step sets its own `socketTimeout`, which wins over the
connection. The addresses are not real, so the flows validate and load but cannot reach
their servers. The trust store is the test one in `keystore/testdata`; its password comes from
`DIF_TRUSTSTORE_PASSWORD`. See the [language reference](../../../docs/connections.md).
