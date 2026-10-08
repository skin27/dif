# Tests

`go test ./...` runs everything; `go vet ./...` checks the code.

| Where | What it holds |
|---|---|
| `*_test.go` beside the code | Unit tests. They are compiled with their package and use what it does not export. Most tests are here. |
| `test/<package>/` | Tests of a package that use only its exported API (package `<name>_test`). |
| `test/dil/` | Builds every DIL flow in `testdata/`, and checks that the fixtures hold no credentials. |
| `testdata/` | The fixtures, see below. |
| `internal/sanitize/`, `cmd/sanitize/` | The tool that replaces credentials in the fixtures with dummies. |

A test moves to `test/<package>/` only if it, and the helpers it shares with other
tests, touch nothing unexported; otherwise it stays beside the code, since Go
cannot compile it from another directory.

## Fixtures

| Path | Holds |
|---|---|
| `testdata/hello.json`, `timer.json`, `test88.json`, `test89.json` | DIF's own small flows |
| `testdata/examples/` | Example flows (`channels/`, `experimental/` and `request-reply/` are groups of them) |
| `testdata/regression/` | Flows of real use cases, grouped as in the designer |
| `testdata/reliable/` | The reliable-channel example: two flows and the service configuration that runs them (`internal/service` tests it) |
| `testdata/parse/` | Flows that only the parser tests (`flows/impl`) read: they use steps that DIL exports as `unknown` |
| `keystore/testdata/` | Test keystores |

Every flow in `testdata/` must build, except `reliable/` and `parse/`. `go test ./test/dil`
builds them as `dif run` does before it starts one, with a throwaway server identity
(`internal/sanitize/dummy-identity.p12`, password `dummy-password`) in the working directory
and in `SSL_CERT_FILE`, so that flows that call each other over HTTPS trust it. Systems such
as SFTP, SMB, SQL, mail and SOAP are not contacted: building a flow does not connect.

## Credentials

The fixtures must not hold real credentials. `go run ./cmd/sanitize` replaces them with
dummies: passwords, tokens and API keys by option, header and variable name; `ENC(...)`
values; Basic and Bearer credentials; passwords in URLs and query strings; private keys and
embedded keystores. The same original becomes the same dummy, so values that belong
together still match. `-check` lists what would change and `-report` every finding, never
the value. Run it before you commit a new flow, and review the diff. It is a safety net: it
finds credentials by name and shape.

The `ENC(...)` values are one dummy, encrypted as the Java `EncryptionUtil` does (see
"Encrypted values" in the main README) under the test password `dif-regression-test-key`.
`test/dil` sets it as `DIF_ENCRYPTION_PASSWORD`, and `TestEncryptedFixturesDecrypt` checks
that every value in the fixtures decrypts. Flows with an encrypted value fail to build
without it, as they should (`TestEncryptedFlowsNeedThePassword`).

Credentials that were committed before are in the git history: rotate them.
