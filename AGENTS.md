# Repository Guidelines

## Project Structure & Module Organization

DIF is a Go integration framework that stays close to the standard library (see [Dependencies](#dependencies)). `cmd/dif/` contains the executable; `cli/` implements interactive commands; `api/` exposes the public API. `engine/` runs flows and manages lifecycle, while `message/` defines messages. `flows/definition/` holds the internal flow model and `flows/impl/` parses and builds DIL JSON flows. Keep the engine independent of DIL.

`steps/definition/` defines processor contracts, `steps/registry/` registers and validates steps, and `steps/impl/` contains built-in processors and embedded schemas. `keystore/` handles PKCS#12 files. Tests live beside source files as `*_test.go`; runnable fixtures live in `testdata/`, and broader flow examples in `examples/`. `regressionTests/` holds the DIL flows of real use cases, `postman/` the Postman requests that state what each flow must answer, and `regression/` the harness that runs them (see `regression/README.md`). `steps/` also contains legacy designer assets and documentation.

## Build, Test, and Development Commands

Use Go 1.25 or newer from the repository root:

- `go build ./cmd/dif` — build the CLI executable.
- `go run ./cmd/dif` — open the interactive CLI.
- `go run ./cmd/dif start testdata/hello.json` — load and start a sample flow; use `send hello` or `request hello` at the prompt.
- `go test ./...` — run all tests.
- `go vet ./...` — check for common Go mistakes.
- `go test -race ./...` — check concurrent behavior; requires cgo and a C compiler.

## Development Workflow

For non-trivial changes:

1. Inspect the existing implementation first.
2. Do not immediately modify code.
3. Identify affected modules, classes, and files.
4. Explain the proposed architecture.
5. Identify compatibility and regression risks.
6. Produce a short implementation plan.
7. Only implement after the user explicitly approves the plan.

During implementation:

- Prefer minimal changes.
- Follow existing project conventions.
- Do not introduce dependencies without approval.
- Run relevant tests.
- Do not silently change architecture from the approved plan. Obtain approval before deviating.

## Coding Style & Naming Conventions

Format changed Go files with `gofmt`; use its tab indentation and standard Go naming conventions. Keep package names lowercase, exported identifiers in PascalCase, and implementation files descriptive, such as `file_source.go`. Use the standard library; the only dependencies are those of the SFTP client, so adding another needs approval. Keep processors pluggable without engine changes.

For new built-in steps, add a schema at `steps/impl/schemas/<name>-<kind>.json` and register the processor in `steps/impl/builtin.go`. Keep option descriptions short and defaults consistent with implementation.

## Dependencies

Use the standard library first. These dependencies are approved; add one only when the step or tool that needs it is implemented, and ask before adding any other:

- `github.com/pkg/sftp` and `golang.org/x/crypto`: the SFTP client (in use).
- `gopkg.in/yaml.v3`: reading the Postman collections in `regression/postman` (in use), and the `docconverter` step.
- `github.com/knroy/go-xml`: the XPath 2.0 processor of the `xpath` language (in use), and the XSLT 2.0 processor of the `xslt` step. It is a young project (v1.6.0, October 2026) that needs Go 1.25; `steps/impl/xpath2.go` is the only file that imports it for XPath, so it can be replaced.
- `github.com/itchyny/gojq`: the `jq` function of the simple language (in use).
- `github.com/hirochachacha/go-smb2`: the `smb` and `smbenrich` steps.
- `github.com/emersion/go-imap/v2`: the `imaps` source (its tests use its in-memory server and `github.com/emersion/go-sasl`).
- `github.com/jackc/pgx/v5`, `github.com/go-sql-driver/mysql`, `github.com/microsoft/go-mssqldb` and `github.com/sijms/go-ora/v2`: the `database/sql` drivers of the `sql` and `sql2` steps (postgres, mysql, sql server, oracle), each in a file of its own, `steps/impl/sql_<database>.go`.

## Testing Guidelines

Use Go's `testing` package with `Test<Behavior>` functions. Cover successful processing, invalid options, and error paths; lifecycle changes should cover cancellation and concurrent operations. Prefer local fixtures and temporary files over external services; mock or fake the systems a step talks to (the repository has in-process FTP, SFTP and SMTP servers). `go test ./regression` builds every regression flow and checks the results against `regression/loadable.json`; `go test ./regression -postman` also runs the Postman requests. After you change a step, run them with `-update`, check that only tests that now pass were added, and commit the files with the change. No numeric coverage threshold is configured.

## Commit & Pull Request Guidelines

Recent commits use short imperative subjects, such as “Add the flowlink source and action.” Follow that style. In pull requests, explain the behavior change, list validation commands and results, and link relevant issues. Update schemas, examples, and README documentation when changing step options or CLI behavior.

## Security & Configuration

Keep local keystores and credentials out of commits. Before committing new regression fixtures, run `go run ./regression/cmd/sanitize`, which replaces credentials with dummies (`go test ./regression` fails while a fixture holds one). `security/`, runtime `logs/`, and `dif.exe` are ignored. Use the documented `DIF_SERVER_IDENTITY_PASSWORD`, `DIF_TRUSTSTORE_PASSWORD`, `DIF_SMTP_PASSWORD`, and `DIF_ENCRYPTION_PASSWORD` environment variables for local secrets.
