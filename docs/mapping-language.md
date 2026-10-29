# Core mappings and difmap/1

Mappings are declared in `dil.core.maps.map` and invoked by an action whose URI is
`mapper:<name>` or `mapper:<namespace>:<name>`. The action has no options. A map's
formats and source resolve in its defining document, including its own imports.
A format's schema likewise resolves in the format's defining document. Caller
declarations cannot replace these private dependencies. Exact local names take
precedence over qualified imported names, including names containing colons.

See the [runnable example](../api/testdata/mapping/README.md).

## Declarations

```json
{
  "formats": {
    "format": [
      {"name":"Input", "mediaType":"application/json"},
      {"name":"Output", "mediaType":"application/json", "schemaRef":"OutputSchema"}
    ]
  },
  "maps": {
    "map": {
      "name":"Normalize",
      "input":"Input",
      "output":"Output",
      "language":"difmap/1",
      "expression":"{id: string(payload.id), name: trim(payload.name)}"
    }
  }
}
```

Place these collections under `dil.core`; both accept an object or an array.
`schemaRef` identifies a core resource containing JSON Schema. Only the existing
JSON Schema subset is supported: type, properties, required, additionalProperties,
items, enum, const, minimum/maximum, exclusiveMinimum/exclusiveMaximum,
minLength/maxLength, pattern, and minItems/maxItems. Standard annotations such as
title and description are accepted. Other keywords fail preparation. Comparisons
in the mapper use exact decimal values. Schema validation is supported only on JSON.

For mapping source, specify exactly one of `expression` and `sourceRef`; the latter
identifies a core resource whose content contains the script. Exceptions:

- `identity` has neither source field. It reads, validates, and writes through the
  format contracts without changing the data model.
- `xslt` requires `sourceRef` and XML input/output. It uses the existing XSLT engine
  and scalar-header parameter convention. Use `xsl:output method="xml"`; conflicting
  output methods, non-UTF-8 encodings, and doctypes are rejected. The result must
  satisfy the XML adapter's constraints. Existing standalone XSLT steps are unchanged.

Unknown declaration fields, duplicate new declaration names, missing references,
unsupported languages, malformed scripts, and unsupported schemas fail preparation,
including in unused maps. Input field types and output contract satisfaction are
checked when processing a message. All mapping sources are part of DIL snapshots;
there is no script-level import, filesystem lookup, or hot reload.

## Expressions

```text
let active = payload.lines filter (line) -> line.active == true;
{
  id: string(payload.id),
  country: payload.country default "NL",
  lines: active map (line, index) -> {
    position: index + 1,
    sku: upper(line.sku),
    amount: round(number(line.quantity) * number(line.price), 2)
  }
}
```

Roots are read-only `payload` and `headers`. Header access excludes `body` and
`metadata.*`; unsupported native header values fail when accessed. Scripts return
one value, never a stream. Named typed functions are supported (see
[functions](functions-language.md)). There is no recursion, map invocation,
or access to clock, environment, network, files, or mutable tenant state.

The grammar consists of literals, object/array constructors, selectors, function
calls, unary/binary expressions, `if (condition) value else value`,
`let name = value; expression`, and array `map`/`filter` lambdas. Objects use
identifier or double-quoted keys; strings are double-quoted with Go-style escaped
characters. Use bracket selection for arbitrary property names. Comments use
`//` or `/* ... */`. Trailing commas are not supported.

Selectors: `payload.customer.name`, `payload["odd-key"]`, `payload.items[0]`.
Array indexes are zero-based integers. Missing fields or out-of-range indexes produce
Missing. Further selection on Missing stays Missing; selecting on null or another
incompatible type fails. Duplicate object keys fail compilation.

`let` names are immutable and visible only after their declaration in that
expression. Duplicate visible bindings are rejected. Lambda element/index names
are local to their body and can shadow ordinary outer names, but cannot replace
`payload` or `headers`. A lambda is accepted only by `map` or `filter`.

Precedence, from lowest to highest (binary operators associate left):

| Level | Operators |
|---|---|
| 1 | `map`, `filter` |
| 2 | `default` |
| 3 | `or` |
| 4 | `and` |
| 5 | `==`, `!=` |
| 6 | `<`, `<=`, `>`, `>=` |
| 7 | `++` (strings) |
| 8 | `+`, `-` |
| 9 | `*`, `/` |
| 10 | Unary `-`, `not`, `!` |
| 11 | Field/index selection |

Parentheses override precedence. Parenthesize a defaulted collection before `map`.
A lambda body stops before a following collection operator at the same level;
parenthesize a nested collection expression when needed. `if` and `let` bodies
extend through their full expression.

`default` is lazy and handles both Missing and null. False, zero, and empty strings
are preserved. `and`/`or` short-circuit and require booleans. `if` evaluates only
the selected branch. Missing values must be handled before inserting them into
output objects/arrays. Null is explicit data. Ordered objects retain construction
order; equality ignores object member order and compares numbers exactly.

Numbers are exact rationals internally, with bounded decimal source precision.
They serialize as decimal numbers. A non-terminating result such as `1 / 3` requires
explicit rounding. There is no implicit scalar coercion. Division by zero and
non-finite numbers fail. Native float inputs have already been rounded by their
caller; use JSON text, `json.Number`, or integer inputs for exact large values.

| Built-in | Contract |
|---|---|
| `string(value)` | String, boolean, or finitely representable number to text |
| `number(value)` | Number or JSON decimal string to exact number |
| `boolean(value)` | Boolean or exactly `"true"`/`"false"` |
| `trim(text)` | Remove leading/trailing Unicode whitespace |
| `upper(text)`, `lower(text)` | Locale-independent Go Unicode case mapping |
| `size(value)` | Unicode rune count, array length, or object member count |
| `sum(array)` | Exact numeric sum; empty array produces zero |
| `round(number, scale)` | Non-negative integer decimal scale; half away from zero |
| `exists(value)` | False only for Missing; true for explicit null |
| `isNull(value)` | True only for explicit null |

## Formats

Supported MIME types are `application/json`, `application/xml`, and `text/csv`.
MIME type case is normalized. An optional charset must be UTF-8; other MIME parameters
are rejected. Missing input Content-Type is permitted; a conflicting one fails.
Output is UTF-8 serialized text and receives the output contract's Content-Type.

JSON accepts text/bytes or native decoded JSON values. A Go string body means
serialized JSON, so a JSON string body must include its JSON quotes. Native arrays
and objects are copied and bounded; cyclic or unsupported native values fail.
Duplicate keys in serialized JSON are rejected.

CSV is an array of row objects. Cells remain strings until explicitly converted.
Reader settings: `delimiter` (one character, default comma), `header` (default true),
and `columns` (required when header is false; when supplied with a header, it must
match exactly). Header names must be unique and nonempty. Standard CSV quoting is
supported; blank physical lines are skipped by the standard library reader.

CSV writer settings: required ordered `columns`, `delimiter`, `header` (default
true), and `lineEnding` (`lf`, default, or `crlf`). Extra object fields are omitted;
missing/null columns become empty cells. Nested arrays/objects are errors. Reader
and writer settings are separate objects on the named format. JSON/XML accept no
reader/writer options in this version. YAML continues to work through legacy steps.

XML uses ordered nodes rather than heuristic singleton/array projections:

| Built-in | Contract |
|---|---|
| `child(node, name)` | Exactly one matching child element, otherwise error |
| `children(node, name)` | Matching child elements as an array, even for zero/one |
| `attr(node, name)` | Attribute string or Missing |
| `text(node)` | Text of an element with no child elements |
| `element(name, attributes, children)` | Element from string-valued attributes and an array of node values |
| `textNode(text)` | Explicit text node for ordered mixed content |

Names use `{namespaceURI}local` spelling, or an unqualified local name. Namespace
prefixes are not selectors. The writer assigns deterministic prefixes and preserves
child/text order. Namespace-qualified names stored inside *text or attribute values*
are opaque strings; mappings must explicitly manage them when changing prefixes.
XSLT mappings retain the stylesheet's serialization and namespace prefixes.
XML requires exactly one root. DTDs, comments, and processing instructions other
than the XML declaration are rejected. External entities are unavailable.

## Validation, runtime, and limits

Preparation resolves references and compiles immutable plans without constructing
processors. Each load/invocation has independent message state. Mapping runs as:
decode → validate input → evaluate → validate output → encode. Only a fully successful
result replaces body and Content-Type; other headers and message identity remain.
Errors use normal DIF error routes and redelivery. Diagnostics include map, origin,
format, step, and script line/column where available, without data or source excerpts.

The engine remains independent of DIL and mapping syntax. `describe` displays
mapping languages, contracts, origins, and references without script/schema contents.
Single-flow APIs support local maps; group APIs are required for imports.

Host limits are available as `api.ProgramLimits.Mapping` (`api.MappingLimits`) or
the service configuration's `mappingLimits` object. The shell uses default limits.
Zero fields select defaults; negative fields fail validation.

| Field | Default |
|---|---|
| `sourceBytes` | 262144 per map |
| `depth` | 128; maximum configurable value 512 |
| `operations` | 1000000 |
| `nodes` | 100000, cumulative during one invocation |
| `bytes` | 16777216 input/output bytes |
| `digits` | 128; maximum configurable value 4096 |

The evaluator and codecs check cancellation and budgets. Compiled syntax, node
lookups, and writers have explicit bounds; objects are indexed within an invocation.
XSLT uses the library's context cancellation instead of the DIF operation budget.
Its source/input/output are bounded, but its intermediate transformation allocations
and library compilation are not governed by DIF's evaluator operation/node limits.
The new language does not claim compatibility with DataWeave, DataSonnet, or UTL-X.

Named calls and 16 additional pure helpers are documented in [the function reference](functions-language.md).
