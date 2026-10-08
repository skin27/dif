# Regression tests

The regression suite checks DIF against real use cases:

| Directory | What it holds |
|---|---|
| `regressionTests/` | 672 DIL flows, one per file, grouped as in the designer |
| `postman/` | The Postman requests (2,481) that call those flows, each with a test script that states what the flow must answer |
| `regression/` | The harness: Go tests, the skip list, the two ratchet files and the tools below |

The flows say what DIF must be able to do, the Postman requests say what it must
answer. A request calls a flow by the last segment of its URL
(`{{BASE}}/CB_SimpleReplace` calls the flow whose source is
`https://0.0.0.0:9001/regressiontests/CB_SimpleReplace`).

## Running

```sh
go test ./regression                                 # seconds; no network
go test ./regression -postman -timeout 60m           # also the Postman requests; about a minute
go test ./regression -postman -update -timeout 60m   # record the results, see Ratchet
DIF_POSTMAN_COLLECTION=Steps-Basic go test ./regression -run Postman -postman   # one collection
```

Without `-postman` the tests do the following.

- `TestFlowsLoad` builds every flow that is not skipped, as `dif run` does before
  it starts one, and compares the flows that build with `loadable.json`. Why each
  flow does not build is written to `regression/.cache/load-failures.txt`.
- `TestSkipManifest` checks `skip.json`.
- `TestFixturesHaveNoCredentials` fails if a fixture holds a credential.

With `-postman`, `TestPostman` does the following.

1. It reads the Postman YAML (`regression/postman`) and writes it as the JSON that
   Newman runs. The server address in the URLs becomes `{{BASE}}`.
2. It starts DIF in-process, as `dif run --dir` does, with the flows that are
   not skipped, build, and have a source that requests or other flows reach
   (`https`, `flowlink`, `queue`, `topic`, `reply`). Timers, mailboxes and file
   sources are built by `TestFlowsLoad` but not started. The addresses in the
   flows are pointed at one local port (see below).
3. It runs each collection with Newman, which runs the test scripts as Postman
   does. Newman 6.2.2 is installed under `regression/.cache` (git-ignored) with
   npm the first time, so this needs Node, npm and network access. A small
   reporter of our own (`newman-reporter-difcompact`) keeps only the outcome per
   request, as Newman's JSON reporter cannot hold the bodies of this many requests.
4. It compares the requests that pass with `postman-passing.json`. Why each
   failing request fails is written to `regression/.cache/postman-failures.txt`.

A request passes when it was answered and every test in it passed. A request
without tests passes when the answer is HTTP 2xx.

## What is not run

- **Skipped flows** (`skip.json`, with a reason for each): Groovy flows (Groovy
  runs on the JVM only), custom steps (out of scope) and broken fixtures (an
  empty flow, or a link without a target). Their Postman requests are skipped too.
- **Requests with a file body** (61): the files are not in the repository.
- **Requests that name no flow** in `regressionTests` (56) are run, and fail,
  unless the request expects the 404.

## Ratchet

`loadable.json` lists the flows that build and `postman-passing.json` the
requests that pass. A test in the file that fails now is a regression and fails
the run. A test that passes now and is not in the file also fails the run, with
the command to record it, so that the files stay exact and later regressions are
caught. After you have fixed or added a step, run the tests with `-update`, check
the diff, and commit it with the change.

## Addresses and external systems

The flows were written for the Dovetail test servers. For a local run:

- `https://0.0.0.0:9001/` becomes `https://127.0.0.1:<port>/`, and the addresses
  of the Dovetail servers (`https://next.dovetail.world/test/inbound_http/regressiontests/`)
  become the same local address, so that flows can call each other. This is done
  in memory (`overlay` in `dil_test.go`); the files are not changed.
- The server identity is a throwaway key, `sanitize/dummy-identity.p12`
  (password `dummy-password`). The test trusts it through `SSL_CERT_FILE`, and
  the working directory gets it as `security/outbound-truststore.p12` too, so
  that the `https` and `rest` actions, which look there for the certificates to
  trust, trust the flows they call.
- Other systems (SFTP, SMB, SQL, mail, SOAP, ...) are not reachable, and are
  replaced by mocks and fakes as the steps for them are added.

## Credentials

The fixtures must not hold real credentials. `go run ./regression/cmd/sanitize`
replaces them with dummies: passwords, tokens and API keys by option, header and
variable name; `ENC(...)` values; Basic and Bearer credentials; passwords in
URLs and query strings; private keys and embedded keystores. The same original
becomes the same dummy, so a flow and the request that calls it still match.
`-check` lists what would change and `-report` every finding, never the value.
Run it before you commit a new import, and review the diff. It is a safety net:
it finds credentials by name and shape.

The `ENC(...)` values are one dummy, encrypted as the Java `EncryptionUtil` does
(see "Encrypted values" in the main README) under the test password
`dif-regression-test-key`. The tests set it as `DIF_ENCRYPTION_PASSWORD`, and
`TestEncryptedFixturesDecrypt` checks that every value in the fixtures decrypts.
Flows with an encrypted value fail to build without it, as they should
(`TestEncryptedFlowsNeedThePassword`).

Credentials that were committed before are in the git history: rotate them.

## Plan

The work on DIL compatibility goes in phases (harness and sanitizer, `ENC(...)`,
options and output fidelity, expression engines, small steps, larger steps). The
phase list, the status and the handover for the next phase are in
[docs/handover-phase-3.md](../docs/handover-phase-3.md).
