# Phase 2: core formats and declarative mappings

Status: implemented. The approved design below is retained as the implementation
record. See [the language reference](mapping-language.md) and
[runnable examples](../api/testdata/mapping/README.md) for current usage.

Verification: `go test ./...`, `go vet ./...`, and `go build ./cmd/dif` passed.
The parser passed a 20-second fuzz run (over 46,000 executions); compilation and
execution benchmarks passed. Race testing was unavailable in this environment
(`CGO_ENABLED=0`, no C compiler configured). No dependencies were added.

## Outcome

Define reusable mappings under `dil.core.maps.map`, give them named input/output
contracts under `dil.core.formats.format`, and invoke them from normal flow steps.
Keep the existing DIL envelope and phase 1 imports, namespaces, snapshots, and IDs.
Mapping definitions describe data transformation; flows decide when to invoke them.

Recommend a small, versioned DIF expression language, provisionally `difmap/1`.
It constructs the desired output using object/array expressions, selectors,
collection operations, conditions, and explicit conversions. It is inspired by
DataSonnet, DataWeave, and UTL-X, but does not claim their syntax or library compatibility.
Implementing a language is a maintenance commitment: keep its initial grammar and
built-ins explicit and small. Do not make a full clone part of phase 2.

The alternative is named jq mappings using the already-approved gojq dependency.
That reduces language implementation work, but gives jq syntax and stream semantics
rather than the proposed output-construction notation. Recommend `difmap/1`
for the requested authoring experience; retain existing jq support unchanged.
Embedding a third-party mapping runtime is a separate dependency/runtime decision.

## Existing implementation to build on

- `flows/impl/dil.go`, `document.go`, and `build.go` decode DIL, resolve scoped core
  references, and produce standalone flow plans.
- `internal/program` and `api/program.go` prepare immutable document groups and
  validate them before processor construction.
- `steps/impl/xslt_action.go` already compiles inline or resource-backed XSLT.
- `steps/impl/validate_action.go` has an existing JSON Schema subset; use that
  subset explicitly rather than advertise full JSON Schema or XSD support.
- `steps/impl/docconverter_action.go` has JSON/XML/CSV/YAML readers and writers,
  but includes legacy shape and coercion rules. Reuse compatible primitives,
  not its assumptions as the new language's universal data model.
- `steps/impl/simple_jq.go` implements jq within Simple. Do not route the new
  language through string interpolation or change Simple's result semantics.

## Proposed DIL model

The following is a core fragment; collection entries support an object or an array,
as in existing DIL. Map expressions remain JSON strings in the DIL document.

```json
{
  "dil": {
    "core": {
      "formats": {
        "format": [
          {"name": "OrderIn", "mediaType": "application/json"},
          {"name": "OrderOut", "mediaType": "application/json", "schemaRef": "OrderOutSchema"}
        ]
      },
      "resources": {
        "resource": {
          "name": "OrderOutSchema",
          "content": "{\"type\":\"object\",\"required\":[\"id\",\"customer\",\"lines\"]}"
        }
      },
      "maps": {
        "map": {
          "name": "NormalizeOrder",
          "input": "OrderIn",
          "output": "OrderOut",
          "language": "difmap/1",
          "expression": "{ id: string(payload.orderId), customer: { name: trim(payload.customer.name), country: payload.customer.country default \"NL\" }, lines: (payload.lines default []) map (line) -> { sku: line.sku, quantity: number(line.qty) } }"
        }
      }
    }
  }
}
```

A flow invokes the map with a normal action; existing graph links are unchanged:

```json
{
  "id": "normalize-order",
  "type": "action",
  "uri": "mapper:NormalizeOrder",
  "options": {},
  "links": {
    "link": [
      {"id": "received", "bound": "in"},
      {"id": "normalized", "bound": "out"}
    ]
  }
}
```

For an imported core: `mapper:common:NormalizeOrder`. The map name is the URI
remainder, preserving names containing colons. There is no inline expression on
the flow action and no dynamic map-name expression in phase 2.

### Format contract

| Field | Meaning |
|---|---|
| `name` | Required document-scoped declaration name |
| `mediaType` | Required supported reader/writer format |
| `schemaRef` | Optional named core resource holding a supported JSON Schema |
| `reader` | Optional strict, format-specific reader settings |
| `writer` | Optional strict, format-specific writer settings |

Formats combine representation settings with optional validation. They are not
service contracts, processor definitions, endpoints, or a new static type system.
The map's declared input/output contracts are authoritative; missing Content-Type
is allowed, while an explicitly incompatible Content-Type fails. Normalize MIME
type case and parameters, and initially support UTF-8 only. Do not guess a format
from the payload. Input must match the declared representation.

JSON contracts accept JSON text/bytes and compatible already-decoded JSON values.
Disambiguation is explicit: a Go string body is serialized JSON, including quoted
JSON strings; other supported native values are already decoded. Reject unsupported
Go values rather than stringify them. All output is serialized UTF-8 text with the
declared Content-Type, matching DIF's converter convention.

Validate input before mapping and output before publishing the changed body.
Initially `schemaRef` is supported for JSON contracts only, using the existing
validator's documented subset. Unsupported schema keywords and schemas on other
formats fail validation rather than being silently ignored. XSD is deferred.

### Map contract

Required: `name`, `input`, `output`, `language`. Exactly one of `expression` or
`sourceRef` supplies mapping source. `sourceRef` references an existing core resource
whose `content` contains the source; this keeps large mappings reusable and within
the phase 1 snapshot mechanism. Do not add a second file-import mechanism yet.

Initial languages:

- `difmap/1`: the proposed declarative expression language.
- `identity`: no source; decode, validate, and encode through the format contracts.
- `xslt`: `sourceRef` required, XML input/output only in this phase, using the
  existing XSLT implementation. Require XML serialization; reject conflicting
  stylesheet output settings. Existing direct XSLT steps keep all their current behavior.

For XSLT adapters, retain the existing scalar-header parameter convention. Do not
expose arbitrary processor selection or arbitrary step graphs inside maps; those
would overlap phase 3 reusable processors.

Names are unique within each new core kind. Existing declaration kinds and naming
rules remain intact. Imported map references resolve in the map's defining document:
its `input`, `output`, and `sourceRef` never resolve against the calling flow's core.
Likewise, a format's `schemaRef` resolves in the format's defining document.
Exact local names win, followed by direct namespace lookup, as in phase 1.
Imported maps may internally use their own imports without re-exporting them.

## Mapping authoring experience

This is the expression from the core definition, formatted for reading:

```text
{
  id: string(payload.orderId),
  customer: {
    name: trim(payload.customer.name),
    country: payload.customer.country default "NL"
  },
  lines: (payload.lines default []) map (line) -> {
    sku: line.sku,
    quantity: number(line.qty)
  }
}
```

Another expression illustrates filtering, a local binding, and calculation:

```text
let active = payload.lines filter (line) -> line.active == true;
{
  orderId: payload.orderId,
  lines: active map (line, index) -> {
    position: index + 1,
    sku: upper(line.sku),
    amount: round(number(line.qty) * number(line.price), 2)
  }
}
```

The DIL map supplies language version and formats. Do not duplicate them in a
DataWeave-style script header. Objects are output constructors, not JSON strings
with `${...}` substitutions; escaping belongs to the selected writer.

### Initial language boundary

- Values: null, boolean, string, number, ordered object, array; XML node values
  enter through the XML adapter described below.
- Read-only roots: `payload` and `headers`. Headers exclude body and `metadata.*`;
  reject unsupported accessed header values with a type diagnostic.
- Object/array literals; quoted keys; field selection, bracket selection, and
  zero-based array indexing. Duplicate output keys are errors.
- Lexically scoped immutable `let` bindings, declared before use; no reassignment.
- `if (condition) expression else expression`; lazy `default`; short-circuit
  boolean operations; comparisons; numeric arithmetic; string concatenation `++`.
- Array `map` and `filter` with explicit element/index lambda parameters. Lambdas
  are confined to these operators; they are not general reusable function definitions.
- Small built-in set: `string`, `number`, `boolean`, `trim`, `upper`, `lower`,
  `size`, `sum`, `round`, `exists`, and `isNull`, plus the XML adapter helpers.
  Specify argument types/arity and Unicode behavior before implementing each.
- Comments: `//` and `/* ... */`. Publish precedence/associativity and source
  location rules with the grammar; use parentheses around ambiguous collections.

Missing and null are distinct. Missing object fields/out-of-range array items
produce an internal Missing value; traversing Missing continues to produce Missing.
Selecting a field from another non-object type is an error. `default` replaces
Missing or null, but preserves false, zero, and empty strings. Missing output fields
or array elements are errors unless handled explicitly. Explicit null is serialized.
`exists` distinguishes Missing from an explicit null.

No implicit scalar coercions: `number("12")` works; `"12" + 1` fails. Booleans are
not truthy/falsy numbers or strings. Specify decimal arithmetic using Go's standard
library: preserve parsed decimal values exactly, bound precision/digits, and require
explicit rounding for results that cannot be emitted as finite decimals. Division
by zero and non-finite numbers fail. Do not silently round large identifiers through
float64; document that an already-decoded float64 input has already lost any such precision.
`round(value, scale)` uses a documented rounding rule (proposed: half away from zero).

Deferred: user-defined/named functions, typed callable parameters, recursion,
map-to-map calls, reducers, joins, grouping, dynamic evaluation, date/time libraries,
and custom module imports. Phase 3 will build on the lexical scopes and immutable AST.

## Format delivery and XML semantics

Implement the same expression language across format adapters, but do not promise
that structurally different XML and JSON documents have identical selectors.

| Adapter | Phase 2 behavior |
|---|---|
| JSON | Typed values; preserve numbers; reject duplicate input keys; deterministic serialization |
| CSV | Array of row objects; required unique header names on read; explicit ordered `writer.columns`; configurable delimiter/header/line ending; cells remain text unless mapped |
| XML | Namespace-aware ordered element model with attributes, text, and children; UTF-8; no DTD/external entity loading |
| YAML | Deferred new-map adapter; existing YAML/docconverter steps continue to work |

CSV missing output columns become empty cells; null becomes an empty cell; nested
objects/arrays are rejected. Explicit `writer.columns` prevents output order from
depending on object implementation details. Bound record/field sizes.

XML must not depend on legacy singleton-versus-array guessing. Use a node model:
`child(node, name)` requires exactly one matching child, `children(node, name)`
always returns an array, `attr(node, name)` returns text or Missing, and `text(node)`
reads a text-only element (rejecting nested/mixed content for that helper).
Names use expanded `{namespaceURI}local` spelling, or unqualified local names.
`element(name, attributes, children)` and `textNode(value)` construct output nodes;
the writer requires one document root and assigns deterministic namespace prefixes.
The node model retains mixed content order; it never flattens it implicitly.
Unsupported comments/processing instructions fail explicitly in the initial adapter.

Example XML-to-JSON expression (an Order root with Item children):

```text
{
  id: attr(payload, "id"),
  lines: children(payload, "Item") map (item) -> {
    sku: attr(item, "sku"),
    quantity: number(attr(item, "quantity"))
  }
}
```

The XML vocabulary is deliberately explicit; a later ergonomic selector syntax
can be added without changing cardinality or namespace behavior.

## Execution and architecture

```text
core definitions + lexical scopes
  -> resolve map/format/resource references
  -> validate contracts and compile immutable mapping plans
  -> normal mapper action processor
  -> decode -> input validation -> evaluate -> output validation -> encode
  -> replace body and Content-Type on success
```

Keep the engine DIL-independent. Add `internal/mapping` for language syntax, values,
codecs, contracts, and immutable compiled plans. It must not import `api`, `engine`,
or `flows/impl`. The DIL resolver translates declarations to its neutral definitions;
the mapper processor consumes a compiled plan.

Do not flatten new imported maps into strings and then resolve their dependencies
in the caller. Extend phase 1 plans to retain declaration origin and resolved
dependencies. Keep compiled bindings out of user-controlled step options; inject
them using the existing trusted runtime-binding pattern. No global mutable map registry.

Pure compilation/validation needs to be callable without constructing processors.
Extract the existing schema compiler and required XSLT compilation boundary into
shared helpers as needed, preserving legacy wrappers and error behavior. Validation
must not open ports, read arbitrary files, decrypt options, or execute a mapping.
Compile named definitions, including unused maps, during group preparation; reject
unknown languages/fields, malformed scripts, unknown names, and unsupported schemas.
Dynamic field types and contract satisfaction remain runtime checks. The new JSON
schema path must compare the mapping model's exact numbers without converting them
to float64; preserve the legacy validator's behavior through its existing adapter.

Each prepared group owns immutable compiled plans reused across its independent
loads and processors. Each invocation has its own environment and output value.
Input messages/headers are read-only during mapping; publish body and Content-Type
only after the complete operation succeeds. Preserve other headers and identity.
Errors enter DIF's existing error route/redelivery handling with no partial body change.

Bound source size, AST depth, evaluation operations, decoded nodes, output bytes,
and numeric precision; propagate cancellation through codecs and evaluation.
Proposed defaults: 256 KiB source/map, AST depth 128, 1 million evaluation operations,
100,000 decoded/output nodes, 16 MiB input/output, and 128 decimal digits. Put limits
in host configuration/API options, not arbitrary expressions; run boundary tests
before fixing final defaults. `difmap/1` expressions cannot access filesystem,
network, environment, clock, random generators, or mutable tenant state. The XSLT
adapter retains its existing processor's supported language semantics and external
resource restrictions; it is not governed by the DIF expression evaluator's operation
budget. Bound its input/output and propagate context cancellation, and document any
remaining library-level cancellation limits.

Diagnostics identify document, map, format, invoking step, and source line/column
where available. Include structural data paths, but omit payloads, header values,
and script excerpts. `describe` lists map references, language, contracts, and origins;
it does not print script or schema contents.

## Implementation sequence

1. **Contracts and scoped resolution.** Extend DIL decoding/core indexes, namespaced
   lookup, new-kind duplicate checks, neutral mapping definitions, metadata, and
   single-flow compatibility adapters. Resolve map-owned dependencies by origin.
2. **Named transformation path.** Add `mapper` action/schema/binding, JSON/XML format
   contracts, identity and existing-XSLT adapters, pure validation, and an end-to-end
   imported-map test. This proves reuse of transformations before adding a language.
3. **Declarative JSON mapping.** Implement the specified lexer/parser, immutable AST,
   type/value rules, decimal operations, collection operators, and built-ins.
   Compile at preparation time and execute with per-message budgets.
4. **XML and CSV adapters.** Implement the explicit XML vocabulary and CSV rows;
   demonstrate XML-to-JSON and JSON-to-CSV through the same mapping core. Add input
   and output schema checks for JSON contracts.
5. **Integration and verification.** CLI validate/describe, examples, reference docs,
   cancellation/concurrency/error-route tests, fuzzing, and compatibility regression.

Complete phase 2 includes all five increments. The first useful checkpoint is
named contracts plus declarative JSON mapping; XML/CSV are subsequent increments
within this plan, not an unannounced reduction of the final scope.

Affected areas: `flows/impl/{dil,document,build}.go`, `internal/program`,
`api/{program,inspect}.go`, new `internal/mapping/`, new
`steps/impl/mapper_action.go` and `schemas/mapper-action.json`, built-in registration,
selected pure codec/schema/XSLT helpers, CLI descriptions, examples, and README.
No new dependencies are proposed. Existing approved dependencies may be reused.

## Compatibility risks and acceptance

- New format/number rules must not change legacy converters, Simple/jq, XSLT,
  JSON validators, message defaults, or existing flow IDs. Run their existing tests.
- Test local/imported maps, local names containing colons, private transitive map
  dependencies, caller shadowing, missing/wrong-kind refs, and duplicate new names.
- Preserve local-only single-flow Load/Inspect support for new core declarations;
  imports still require group APIs. Old documents require no migration.
- Prove snapshot and compiled-plan reuse after files change, with no processors
  constructed during validation. Concurrent invocations must not share message state.
- Golden tests: nested object construction, arrays, filtering, defaults, null vs
  Missing, explicit casts, Unicode, large integers, decimal arithmetic, failures.
- Format tests: CSV quoting/columns/empty records; XML namespaces, repeated and
  singleton children, ordered mixed content, escaping, unsupported constructs;
  input/output schema failure must preserve the original message body and headers.
- Security/resource tests: cancellation, deep input, oversized output, numeric
  bounds, malformed source, no external reads, and redacted diagnostics.
- Run focused tests, `go test ./...`, `go vet ./...`, `go build ./cmd/dif`, and race
  tests when cgo/compiler support is available. Add parser fuzz seeds and benchmarks
  for compilation versus repeated execution; no unsupported performance promises.

## Design references

- [DataSonnet documentation](https://datasonnet.github.io/datasonnet-mapper/datasonnet/latest/index.html):
  Jsonnet-based data transformation; useful inspiration for output construction.
- [DataWeave scripts](https://docs.mulesoft.com/dataweave/latest/dataweave-language-introduction):
  expressions generate output structures; format directives are separate from the body.
- [UTL-X repository](https://github.com/grauwen/utl-x): functional mapping across
  formats, with explicit collection transformations. This is the UTL-X project
  assumed by this proposal.

These are design references, not dependencies or compatibility commitments.
