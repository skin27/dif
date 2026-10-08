# Handover: Phase 3, the expression engines

> **Historical.** The regression harness (`regression/`), `regressionTests/`,
> `postman/`, `examples/` and `kamelets/` described below were removed when the
> flows that build moved to `testdata/` (see `test/README.md`). The commands and
> ratchet files below no longer exist; `9b0ec36` is the last commit that has them.

For the session that implements Phase 3 of the DIL compatibility work. Read this
first, then `AGENTS.md` and `regression/README.md`. Everything here was measured
on `main` after pull request #2 (commit `50221bb`); numbers will move.

## 1. What this work is

DIF must run the flows of the Java platform (DIL). `regressionTests/` holds 672
real flows, `postman/` the 2,481 Postman requests that say what each flow must
answer. A harness (`regression/`) builds every flow and runs the requests with
Newman, and two ratchet files record what works, so that nothing regresses.

The plan was made in an earlier chat and was nowhere in the repository, so here it
is. Each phase is a commit series on a branch, and a pull request when the user
asks for one.

| Phase | Work | Status |
|---|---|---|
| 0 | Harness, credential sanitizer, skip list | done |
| 1 | `ENC(...)` decryption (`DIF_ENCRYPTION_PASSWORD`) | done |
| 2 | Option, enum and alias fixes; output fidelity of `xmltojson`, `xmltojsonsimple`, `flv` | done (PR #2) |
| **3** | **Expression engines: Simple functions, operators and `${exception.*}`; XPath 2.0 (`github.com/knroy/go-xml`, not the planned subset); JSONPath filters; `aggregate` timers (a new `stepdef.Releaser`); `${jq()}` via `gojq`; `oauth2token` settings from `DIF_OAUTH2_*`** | **done: 487 of 635 flows build, 1,809 of 2,399 requests pass (this handover started from 369 and 1,694)** |
| 4 | Small steps: `base64tobinary`, `binarytobase64`, `setbodyasstring`, the `*withnamespace` splits, `ftps`, `edifacttoxml`, `docconverter` (via `yaml.v3`) | done except `edifacttoxml` (it needs the UN/EDIFACT D96A message definitions, which DIF does not have): 509 of 635 flows build, 1,825 of 2,399 requests pass |
| 5 | Larger steps: `xslt`, `velocity`, `xmlvalidator`, `soap`, `sql`/`sql2`, `smb`, `smbenrich`, `imaps`, `as2` and `schematron`; steps with fewer than two flows are out of scope | done except `pdftotext` and `headerstopdf` (see section 11): 569 of 635 flows build, 1,837 of 2,399 requests pass |

Current state: **369 of 635** in-scope flows build (37 more are skipped on purpose,
see `regression/skip.json`), **1,694 of 2,399** requests that can run pass. The
original plan expected about 521 flows to build after Phase 3 (those were counts
of flows that would build once the option errors were gone; a flow reports only its
first error, so the real number depends on what hides behind them).

## 2. Working agreements

- **`AGENTS.md` rules.** For non-trivial changes: inspect, explain the proposed
  design, list compatibility risks, give a short plan, and **wait for the user to
  approve** before implementing. Do not deviate from an approved plan without
  asking. Prefer minimal changes; follow the existing conventions.
- **Dependencies.** Standard library only, except the approved ones: `yaml.v3`,
  `github.com/itchyny/gojq`, `go-smb2`, `go-imap/v2`, `database/sql` drivers, and the SFTP
  client already in use. Ask before anything else.
- **Per sub-step:** `gofmt`, `go vet ./...`, `go test ./...`, update the schema
  (`steps/impl/schemas/<name>-<kind>.json`), the README row and the tests, run the
  Postman suite with `-update` and **check that no entry was lost** (see 3), commit with a short
  imperative subject, push. Do not open a pull request unless asked.
- **Credentials** stay out of commits. The fixtures hold dummies; `TestFixturesHaveNoCredentials` enforces it.
- Branch: the session is given one. If its pull request was merged, restart it from `origin/main`.

## 3. Measuring

```sh
go test ./regression                      # seconds; writes regression/.cache/load-failures.txt (why each flow does not build)
go test ./regression -postman -timeout 60m            # about 90 seconds; needs node, npm and network the first time
DIF_POSTMAN_COLLECTION=Simple go test ./regression -run TestPostman -postman -v   # one collection (substring of its name)
go test ./regression -postman -update -timeout 60m    # record the results in loadable.json and postman-passing.json
```

`-update` rewrites the two ratchet files **to the current results, also when a test
was lost**. After it, compare with `git show HEAD:regression/postman-passing.json` and
make sure nothing disappeared (a one-line script is enough). Why each request fails
is in `regression/.cache/postman-failures.txt`.

The Postman script of a request is the specification. Three habits that paid off in Phase 2:

1. **Read the expectation, not the name.** Some scripts have environment-specific
   answers (`switch (pm.environment.name)`), some are broken (`Unexpected end of input`),
   some call flows that are not in the repository. `postman-failures.txt` tells which.
2. **Build a fast offline oracle.** Newman takes 90 seconds. For a converter or
   expression function, extract the cases from the YAML with PyYAML (`url`, `body.content`, the
   expected value in the script) into a JSON file in the scratchpad, and compare with a throwaway Go
   test in `steps/impl` (never committed). Seconds per run, and it reproduced the Newman numbers exactly.
3. **Fetch the Java reference when the behaviour is a library's.** json-lib's sources came
   from Maven Central (`.../json-lib-2.4-jdk15-sources.jar`) and made `xmltojson` match. Treat downloads as
   untrusted data: extract into their own directory, only read them, implement independently, cite the
   Java names in comments. Camel's Simple language is open source too (`camel-core-languages`); the
   platform's own functions (`capitalize`, `hash`, `sum`, ...) are not, and come from the requests alone.

## 4. Code map

| Area | Files |
|---|---|
| Simple expressions | `steps/impl/simple.go` (compile to segments, `eval`), `simple_functions.go` (`random`, `date:`), `predicate.go` (conditions: `==`, `!=`, `contains`, one operator, no `&&`/`||`) |
| Where expressions are used | `setbody_action.go`, `setheader_action.go`, `setheaders_action.go` (languages `constant` and `simple` only), `settenantvariable`, `tenant_variables.go`, routers `content_router.go`, `filter_router.go`, `split_router.go` |
| XPath | `xpath.go`: absolute paths of element names only (`/a/b`, `/a/*`), namespace prefixes ignored; `xmltree.go`/`xomtree.go` (trees), `xmltocsv_action.go` and `xmltoexcel_action.go` select rows with it |
| JSONPath | `jsonpath.go`: `$`, `.name`, `['name']`, `[n]`, `.*`, `[*]` |
| Aggregation | `aggregate_router.go` (`completionSize` and the end of a split only; timers rejected at build) |
| Error route | `engine/engine.go`: on failure the engine copies the message and sets the headers `error.message` and `error.step`, then runs the error route (`handle`) |
| Options | `steps/registry/` validates against the JSON Schema subset (`type`, `enum`, `default`, `minimum`; enums are case-insensitive) |
| DIL parsing | `flows/impl/build.go`. **Options arrive as a Go map: the order of the options in the file is lost.** Links carry `language` and `expression` (their condition) in `Node.Links` |
| Language dispatch | `compilePredicate(language, expr)` in `predicate.go` supports `simple`, `xpath`, `jsonpath` |

## 5. Scope, with the evidence

First errors only, from `regression/.cache/load-failures.txt` (266 flows do not build; 125 of
them have a Phase 3 blocker, 97 need a missing step, 44 are other). Regenerate with `go test ./regression`.

### 5a. Simple language (50 flows)

The engine knows `${body}`, `${bodyAs(String)}`, `${header.x}`, `${headers.x}`, `${random(...)}` and the
date forms. Missing, by number of flows:

| Form | Flows | Example fixture |
|---|---|---|
| `${exception.message}` | 8 (+2 `testException`) | DeadLetter, Enrich_*, Test Collector |
| `${exception}` | 7 | FileOutbound* (also `.class`, `.stacktrace`) |
| `${headers}` | 4 | InboundHttpMatchPrefix |
| `${hash(...)}` | 3 | Simple_hash_SHA256, SHA3_256, compare_hashes |
| flow properties `${flowId}`, `${flowName}`, `${flowVersion}`, `${tenant}`, `${environment}`, and `${variable:group:<flowId>:MetaData.FlowVersion}` | 3 | ExchangePropCustomSimple*, CB_SetHeaders |
| `${substring(...)}`, `${sum(...)}`, `${subtract}`, `${concat}`, `${join}`, `${length}`, `${lowercase}`, `${uppercase}`, `${capitalize}`, `${pad}`, `${replace}`, `${safeQuote}`, `${trim}`, `${normalizeWhitespace}`, `${empty(String)}` | about 20 | the `Simple/*` fixtures |
| `${jq(...)}`, `${string:jq(...)}` | 2 | `Simple JQ/direct access json values`, `Multi_jq_queues` |
| `${jsonpath("$.x")}`, `${int:header.x}`, `${bodyAs(String).length}`, `${body.trim()}`, `${split()}` | 5 | Elvis, GreaterThan, SetHeaders_simple, Simple_trim, `compare two lists` |

Functions nest (`${uppercase('Hello ${body}')}`, `${hash(${body},...)}`), so `simpleRef`
(which cuts at the first `}`) must become a real parser: a tree of text, references and function calls with
argument lists. **The 18 requests of the `Simple` collection are the oracle** for the platform's own functions:

| Request | Expected |
|---|---|
| capitalize | `You Nique It` |
| concat | `hello-world`, `Hello\|World`, `you nique it+Hello\|World` |
| hash SHA256 / SHA3_256 | `0c1ee2d4...6a6a` / `c428ad35...081e` (hex; read the flow for the input) |
| join | `id=A&id=B&id=C`, `word=you&word=nique&word=it` |
| length | `12`, `15`, `12` |
| lowercase / uppercase / normalizeWhitespace | `hello world`, `HELLO WORLD`, `Hello World`, `you nique it` |
| pad | `Hello world+++++++++`, `hello world=========` (to 20 characters) |
| safeQuote | `"Hello world"`, `"Hi Europe"`, ... (read the script for the quote cases) |
| substring | `llo World`, `Hello Wor`, `llo Wor` (negative indexes) |
| sum / subtract | `150` / `10` |
| trim | `you nique it`, `Hello great big`, `1` |
| EmptyStringJsonpath | `result=[SW230, ...]`, `[801001, 122001, ...]` (`${jsonpath}` and `${empty(String)}`) |
| ExchangePropCustomSimple* | `Flow Name: <flow>`, `Tenant: regressiontests` (the flow's name and the tenant; the flow id and name are in the DIL, the tenant and environment need a source, see 7) |

`regressionTests/Simple Operators/` (Chain, Increment, Decrement, Elvis, GreaterThan, Ternary),
`Simple Advanced Features/`, `Simple JQ/` and `Simple Jsonpath/` have flows but **no Postman requests**:
they only give a load check, plus the Camel Simple documentation for `++`, `--`, `?:` and `? :`.
Decide with the user how far to take operators.

### 5b. `${exception.*}` (26 fixtures use it; 15 flows are blocked by it today)

The engine already puts the failure on the message for the error route as `error.message` and `error.step`.
Map `${exception.message}` to that, add the missing pieces (`${exception}` the text of the error,
`.class`, `.stacktrace`) as additional `error.*` headers set by `handle`, and keep the evaluation
independent of the engine (it reads headers). `FileOutbound_*` use all four forms.

### 5c. XPath (26 flows)

Needed beyond the current absolute paths, with the fixture that shows it: descendant axis `//line`,
`//filmsoptv/film` (split, splitandaggregate, xmltoexcel worksheets, `xmltocsv` `xPathExpression`);
namespace wildcard `//*:filmsoptv/*:film`; a predicate `//*:DELIVERY_LEADTIME[text()...]`; a function
`//line/number()` (content, filter). Conditions are boolean: see `xpathPredicate` in `xpath.go`
for `=`/`!=` against a literal. Phase 5's `xslt` will build on this, so keep the evaluator reusable.

### 5d. JSONPath (5 flows) and the other languages (30 flows)

- Filters `$.store.book[?(@.price < 10)]` (content, filter).
- A condition whose expression comes from a header (`${header.expression}`): the router evaluates a
  Simple template first, then the result as JSONPath (3 `SimpleHeadersRouting_*` flows).
- `setheaders` with language `jsonpath` (10 flows) or `xpath` (8 flows), and `settenantvariable` (1 each): the value is the result of the
  path on the body; `writeAsString` (accepted but ignored today) decides whether a JSON result is
  written as text or as a value. `SetHeaders_jsonpath_as_string` is the flow with `true`.
- `split`/`splitandaggregate` languages `tokenize`, `xtokenize` and `simple` (12 flows; see `SplitTokenizeAggregate`,
  `SplitXml_xml_aggr_rr_cdata`).

### 5e. `aggregate` timers (9 flows)

`completionTimeout` (6) and `completionInterval` (3) are rejected at build with "completing by time is not
supported yet". The aggregation state is in `aggregate_router.go`; timers need a goroutine that
completes a group, which interacts with the engine's lifecycle (stop, pause, backpressure). Read
`Gatherer` in `steps/definition` and the `engine` tests before designing.

### 5f. `${jq()}` (2 flows) and `oauth2token` (5 flows)

`gojq` is approved. The `oauth2token` steps in the fixtures carry only `tokenName`, `expiryDelay` and
`tenantDbName`: the token endpoint and client credentials live in the tenant's OAuth settings on the
Java platform, which DIF does not have. The step requires `tokenUrl` today. **Open question for the
user** (the original plan names it): where should these settings come from (tenant variables
named by `tokenName`, environment, a config file)?

### 5g. Prerequisite: return the message's headers in the HTTP response

**94 Postman requests assert on response headers other than `Content-Type`, and none of them passes**
(`Simple` 16, `Steps-Construction` 17, `Steps-Transformations` 41, `Steps-Endpoints` 13, `Customer Cases` 3,
`Steps-Deprecated` 4; for example `Resultheader1`, `Received_date`, `flowName`). The https source
(`writeReply` in `https_source.go`) writes only the identity headers and `Content-Type`; Camel's HTTP consumer
returns the message headers. Without this, the `Simple` collection, Phase 3's own oracle, cannot pass,
whatever the functions do.

Not a Phase 3 feature, but do it first, and treat it as a design question (section 7): the message
also holds the request's headers, so returning every header would echo `Authorization`, `Cookie` and the
like. Camel filters its own `Camel*` headers and the hop-by-hop ones. A deny-list, and header names and values
that `validHeader` accepts, would be the minimum.

## 6. Suggested order

One commit (with ratchet update) per line, in this order, since each builds on the one before:

0. **Response headers** (5g), after the user has answered question 5 of section 7.
1. **Simple parser and functions** (5a), with the `Simple` collection as the oracle. Biggest gain.
2. **`${exception.*}`** (5b), then `${headers}`, `${flowVersion}` and the tenant variable form.
3. **XPath subset** (5c).
4. **JSONPath filters and the language wiring** (5d).
5. **`${jq()}`** (5f, gojq).
6. **Aggregate timers** (5e).
7. **`oauth2token`** (5f) once the user has decided.

For each: propose the design first (AGENTS.md), then implement, then measure flows built and
requests passing, and report both numbers to the user.

## 7. Questions to settle with the user before coding

1. `oauth2token` configuration source (5f).
2. Simple operators beyond comparison (`&&`, `||`, `++`, `--`, `?:`): the flows exist without Postman
   requests; implement to Camel's documentation, or only what is needed to build?
3. XPath: grow `xpath.go` into a small XPath 1.0 evaluator (needed by `xslt` in Phase 5), or only the listed constructs?
4. Is `${hash(...)}` the whole story for hashing (SHA-256, SHA3-256 appear)? `crypto/sha3` is in the standard library since Go 1.24 (`go.mod` says 1.24.0).
5. Response headers (5g): return all message headers except a deny-list (which entries?), or only some? The flow properties `${tenant}` and `${environment}` need a source too: configuration of the process, or the DIL?

## 8. Pitfalls and facts worth knowing

- A flow reports **one** build error, so fixing one blocker reveals the next; expect the
  number of flows that build to grow in steps after each change, and re-run `go test ./regression`.
- The DIL parser turns a router's `language`/`expression` into link attributes (`Node.Links`);
  `content`, `filter` and `split` compile them in `New`, so unsupported expressions fail at build.
- Header and message keys are case-sensitive; `body` and `metadata.*` are reserved.
- Strings coming from DIL XML may hold numbers and booleans; the registry coerces options.
- Postman `Simple` requests check response **headers** (`Resultheader1`...): they cannot pass before 5g is done.
- `postman/Steps-Transformations (126-200)` exists twice; the harness uses the copy under `postman/postman/collections`.
- Do not rely on the order of options anywhere (see the `flv` note in the README).

## 9. Left over from earlier phases (not Phase 3)

- `jsontoxml`/`jsontoxmlsimple` fidelity: 13 of 64 requests pass. The writer side of json-lib's
  `XMLSerializer` (`write`, sorted member names) is the reference; its sources can be fetched as for the reader.
- 13 requests of the main `XMLtoJSON` collection fail for reasons outside the step (broken scripts, `noTransformation` routes,
  one odd expectation); the "development" collection expects a different build.
- Needs a design decision, left alone on purpose: `quartz` without `cron`, `sftp` key references and
  dynamic URIs, `.xls` output, `googledrive` conversion, attachment-based `enrich` (`zip`, `attachment`), `https`
  inbound `handlers` and mutual TLS, `certificateStore`, `HttpDefault_get` (an `https:` action with no address).
- The credentials that were committed before the sanitizer are still in the history of `main`
  (`f74b593`, `529a9ec`); rotate them. `CLAUDE.md` still says "zero external dependencies"; `AGENTS.md` has the real list.

## 10. First hour

1. `git fetch origin main`, start the branch from it, `go test ./...`, `go test ./regression -postman` and note the baseline.
2. Read `simple.go`, `predicate.go`, `simple_functions.go` and the `Simple` requests (5a).
3. Extract the `Simple` expectations into an offline oracle (section 3).
4. Write the plan for step 1 of section 6, with the open questions of section 7, and ask for approval.

## 11. Phase 5, what was built and what is left

Measured at the end of Phase 5: **569 of 635** flows build (509 before; 37 more are skipped on purpose),
**1,837 of 2,399** requests pass (1,825 before). Only `xslt` (+5) and `velocity` (+7) gained requests: the
other steps have no Postman requests that can pass against them, or only ones that need a service
(see "What could not be verified"). The user set the scope: steps used by fewer than two flows are out of
scope (`xmlvalidator` and `schematron` were added because they are cheap, `as2` because it was asked for).

| Step | Where | Notes |
|---|---|---|
| `xslt` | `xslt_action.go` | go-xml `xslt` (XSLT 1.0/2.0/3.0 subset). Headers are top-level parameters. Output is post-processed to look like Saxon's (3-space indent, `<!DOCTYPE HTML>` unless a version is set). `doc()`, `document()` and `xsl:include` read nothing. |
| `velocity` | `velocity.go`, `velocity_action.go` | Own interpreter for a subset (listed in the README). Anything else (`#macro`, `#parse`, other methods) fails the build, naming the line. `$headers` is case-insensitive as in Camel. Output limited to 64 MB. |
| `xmlvalidator` | `xmlvalidator_action.go` | go-xml `xsd`. An invalid body does not fail the message: the body becomes the text `org.apache.camel.processor.validation.SchemaValidationException: Validation failed with N errors:` and the details, because the flows' content router tests for that text. |
| `schematron` | `schematron_action.go` | Reads ISO Schematron directly (no XSLT step) and evaluates the tests with go-xml XPath 2.0. Sets `CamelSchematronValidationStatus` and `CamelSchematronValidationReport` (SVRL); the body is unchanged. Abstract patterns/rules, `include` and phases fail the build. |
| `soap` | `soap_action.go` | SOAP 1.1, `extract`, and the WSDL "smart" mode (read once with `?wsdl`, cached, retried after a failure). Basic and Bearer auth. |
| `sql`, `sql2` | `sql_*.go`, `flows/impl/build.go` | `database/sql` with pgx, go-sql-driver/mysql, go-mssqldb and go-ora, one file per dialect. `core.connections` of the DIL becomes the `connection` option of `sql2`. Pooled handles (32, closed after one minute idle). Output is the `ResultSet` XML, headers `numberOfRecords` and `hasErrors`. Password from the option or `DIF_SQL_PASSWORD`. Named parameters `:#name` and `:#${expr}`. |
| `smb`, `smbenrich` | `smb_client.go`, `remote_steps.go` | go-smb2 behind the `remoteFS` of ftp/sftp. The first path segment is the share. `smb` is a source, a sink and an action (write and pass on). Tests use a fake share on the local disk. |
| `imaps` | `imaps_source.go`, `mail_parse.go` | go-imap/v2, password or XOAUTH2 (the token may be a tenant variable `@{name}`). A message is marked `\Seen` after it was processed. Headers `Subject`, `From`, `To`, `Cc`, `Date`, `mail.messageId`, `mail.uid`; attachments with `content: both`. |
| `as2` | `as2_*.go`, `cms.go`, `keystore` | RFC 4130 source and action: signed, encrypted and compressed structures, MDN receipts (sync) and MIC checks. The CMS layer is written by hand on `encoding/asn1` and `crypto` (no CMS library in the standard library); it reads BER as well as DER. Keys come from PEM text, an address, a PEM file or a PKCS #12 keystore. Interoperates with OpenSSL (`openssl cms`, `openssl smime`) in both directions; the tests skip when `openssl` is missing. |

New dependencies (all listed in `AGENTS.md`): `github.com/jackc/pgx/v5`, `github.com/go-sql-driver/mysql`,
`github.com/microsoft/go-mssqldb`, `github.com/sijms/go-ora/v2`, `github.com/hirochachacha/go-smb2` and
`github.com/emersion/go-imap/v2` (with `go-sasl`). Adding the SQL drivers raised `golang.org/x/crypto`,
`x/text` and `x/sys`.

### What could not be verified

- `soap`, `sql`, `smb` (the wire code), `imaps` (Gmail) and `as2` (the platform's partners and key service) were
  only run against in-process fakes (an HTTP server, a fake `database/sql` connector, a fake share, an
  in-process TLS IMAP server, an AS2 partner) and, for AS2, OpenSSL. No real database, share, mailbox or partner.
- CMS compression is checked only against itself: the OpenSSL here has no zlib.
- The SOAP, SQL and AS2 formats follow what the fixtures show and what Camel/Assimbly are known to do. The
  `escapeChars` option of `sql` is accepted and does nothing.
- The AS2 key URLs in the fixtures are addresses of the platform's key service; they cannot be reached from here.

### Findings

- **A successful error route makes the flow succeed.** DIF returns 200 where the platform returns 500, so Postman
  requests that expect 500 from an error route (e.g. "Xslt Error route") fail. This is how the engine treats
  `failedexchange` today; it was not changed in this phase. It needs a decision.
- The test "Body contains a valid ID" expects an exchange id in the form `<15 hex>-<16 hex>`, as Camel writes it.
  Flows that answer something else (a 404 body, a UUID from `setuuid`, XML) cannot satisfy it; it is a pattern of
  the platform's ids, not a property of the flow.
- `velocity_flow_property A` cannot pass: it expects the flow name `Velocity_flow_property`, but the fixture it
  calls is named `OauthGmailNoreply`. The collection and the fixture disagree.
- Flows whose address comes from headers (`${header.x}` in an `smb` or `sftp` URI: two SMB flows, one SFTP flow)
  and the two `sql`/`sql2` "flowprop" flows (no host in the options) are not built. The error says why.

### Not done

- `pdftotext` and `headerstopdf` (2 flows each): they need a PDF reader and writer that the standard library does
  not have, and without the platform's output nothing can check the result. Say so to the user and offer them.
- `edifacttoxml` (2 flows): still blocked on the UN/EDIFACT message definitions.
- One flow each, so out of scope by the user's rule: `awss3`, `jolt`, `jslt`, `jsonata`, `langchain4jagent`,
  `langchain4jchat`, `openai`, `springaichat`, `restopenapi` (source and action).
- Option gaps that are not steps: `xmltoexcel` `xls`, `googledrive` `Convert`, `https` `MutualSSL`, `handlers` and
  `certificateStore`, `enrich` `zip` and `attachment`, `quartz` without `cron`, `jsonvalidator` `format`, `multipart`
  `formFields`, `sftp` `privateKeyUri`. Each is a design decision (section 9); `regression/.cache/load-failures.txt`
  lists the flows with the reason.

