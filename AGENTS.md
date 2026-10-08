# Repository Guidelines

## Project Structure & Module Organization

DIF is a Go integration framework with no dependencies beyond the SFTP client (`github.com/pkg/sftp` and `golang.org/x/crypto`). `cmd/dif/` contains the executable; `cli/` implements interactive commands; `api/` exposes the public API. `engine/` runs flows and manages lifecycle, while `message/` defines messages. `flows/definition/` holds the internal flow model and `flows/impl/` parses and builds DIL JSON flows. Keep the engine independent of DIL.

`steps/definition/` defines processor contracts, `steps/registry/` registers and validates steps, and `steps/impl/` contains built-in processors and embedded schemas. `keystore/` handles PKCS#12 files. Tests live beside source files as `*_test.go`; runnable fixtures live in `testdata/`, and broader flow examples in `examples/`. `steps/` also contains legacy designer assets and documentation.

## Build, Test, and Development Commands

Use Go 1.24 or newer from the repository root:

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

## Testing Guidelines

Use Go's `testing` package with `Test<Behavior>` functions. Cover successful processing, invalid options, and error paths; lifecycle changes should cover cancellation and concurrent operations. Prefer local fixtures and temporary files over external services. No numeric coverage threshold is configured.

## Commit & Pull Request Guidelines

Recent commits use short imperative subjects, such as “Add the flowlink source and action.” Follow that style. In pull requests, explain the behavior change, list validation commands and results, and link relevant issues. Update schemas, examples, and README documentation when changing step options or CLI behavior.

## Security & Configuration

Keep local keystores and credentials out of commits. `security/`, runtime `logs/`, and `dif.exe` are ignored. Use the documented `DIF_SERVER_IDENTITY_PASSWORD`, `DIF_TRUSTSTORE_PASSWORD`, and `DIF_SMTP_PASSWORD` environment variables for local secrets.
