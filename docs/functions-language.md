# Functions and reusable actions

`dil.core.functions.function` defines typed, pure value-producing functions.
`dil.core.processors.processor` defines reusable configurations of one existing
action. Collections accept one object or an array. See the
[runnable example](../api/testdata/functions/README.md).

## Define and call a function

Place this under `dil.core`:

```json
{
  "functions": {
    "function": {
      "name": "normalizeCustomerId",
      "parameters": [{"name": "value", "type": "string"}],
      "returns": "string",
      "language": "difmap/1",
      "expression": "upper(trim(value))"
    }
  }
}
```

In a mapping or another function:

```text
function("common:normalizeCustomerId", payload.customerId)
```

In a Simple expression:

```text
${function('common:normalizeCustomerId', ${header.customerId})}
```

The name must be a literal; arguments are explicit and evaluated once in order.
Function bodies use `difmap/1` and see parameters, local bindings, pure builtins,
and named calls. They cannot implicitly read payload, headers, time, environment,
or tenant state. Pass needed values as arguments. Functions cannot mutate messages,
perform I/O, invoke processors, or recurse.

Required fields are name, parameters (including `[]` for no arguments), returns,
language, and exactly one of expression/sourceRef. A sourceRef identifies a core
text resource in the defining document. Program preparation snapshots the source.

Types are string, number, boolean, array, object, xml, and any. A concrete type
with `?` permits null; any also permits null. Missing and arbitrary native objects
are never valid arguments or results. Array/object contracts are shallow. Defaults
are JSON literals; required parameters precede defaulted parameters. Defaults apply
only to omitted trailing arguments, never to explicit null. There is no coercion:
use string/number/boolean explicitly. Invalid types, calls, declarations, defaults,
and cycles fail preparation where statically known, and otherwise evaluation.

Functions and imported maps retain their defining document's scope. Exact local
names take precedence over names qualified by a direct import namespace, including
local names containing colons. Nested calls cannot be overridden by the caller.
All declarations are checked, including unused definitions.

## Simple results and precision

Existing Simple syntax and conversions remain unchanged. New named calls preserve
their value until the consuming processor applies its existing rendering rules.
For example, setbody and setheader write the rendered result. Embedded calls become
text. New arrays/objects render as JSON; existing Simple list/map rendering stays
unchanged. Numbers crossing the new bridge use json.Number, including comparisons
with Simple numeric literals; they do not pass through float64.

Native integers, json.Number, finite floats, booleans, strings, null, arrays, and
string-keyed objects are supported. Floats have already been rounded by their
producer. Use JSON decimal strings and explicit number conversion for monetary
input. Cyclic/unsupported native values and XML values at the Simple boundary fail.
XML nodes remain usable within functions and mappings.

Functions work in Simple expressions, routing predicates, and dynamic templates.
`simplereplace` resolves calls at evaluation against the prepared scope. Constant
expressions stay literal. Dynamic names and loading declarations from message
content are not supported.

## Additional pure helpers

These extend the existing mapping builtins (conversion, trim/upper/lower,
size/sum/round, exists/isNull, and XML helpers). Simple's existing similarly named
functions keep their own signatures and behavior.

| Function | Contract |
|---|---|
| `isEmpty(value)` | True for Missing, null, empty string/array/object; false for zero, false, whitespace text, and other supported values. |
| `coalesce(a, b, ...)` | At least two operands; lazily choose the first non-Missing/non-null value, otherwise null. |
| `replace(text, old, new)` | Replace all literal occurrences; old cannot be empty. |
| `substring(text, start, end)` | Unicode rune indexes, zero-based, end exclusive; invalid bounds or noninteger indexes fail. |
| `contains(text, part)` | Case-sensitive literal containment. |
| `startsWith(text, prefix)` | Case-sensitive literal prefix. |
| `endsWith(text, suffix)` | Case-sensitive literal suffix. |
| `split(text, separator)` | Nonempty literal separator; preserve empty fields. |
| `join(strings, separator)` | String elements only; empty array gives empty text. |
| `min(numbers)`, `max(numbers)` | Exact numeric extrema; reject empty arrays. |
| `abs(number)` | Exact absolute value. |
| `distinct(array)` | Stable first-occurrence order using mapping equality; comparison work is bounded. |
| `keys(object)`, `values(object)` | Arrays in existing object member order. |
| `formatDate(text, inputLayout, outputLayout, zone)` | Go time layout fields, offset-bearing input, explicit UTC or IANA output zone. |

Except for isEmpty/coalesce, null, Missing, and incorrect types fail. Empty strings
are valid search terms for contains/startsWith/endsWith. Date layouts support Go
numeric/textual fields and punctuation, with T/Z as literal letters; arbitrary
alphabetic literals and Java patterns are rejected. Offset-free inputs, timezone
abbreviation-only inputs, invalid dates, and Local/unknown output zones fail.
Timezone data is embedded from the standard library. Existing Simple date:now
continues to use its existing Java-style pattern contract.

## Reusable actions

Under `dil.core`:

```json
{
  "processors": {
    "processor": {
      "name": "text",
      "parameters": [{"name": "value", "type": "string", "default": "Hello"}],
      "action": {
        "uri": "setbody",
        "options": {"language": "constant", "expression": {"$param": "value"}}
      }
    }
  }
}
```

A normal action step invokes `processor:text` or `processor:common:text` with:

```json
{"arguments": {"value": "Hello from the caller"}}
```

Arguments bind during preparation. An exact single-member `{"$param":"name"}`
object substitutes an entire option value; nested option values are walked.
`{"$literal":value}` escapes a literal marker-shaped object. URI stays literal.
Unknown/missing arguments, extra invocation options, invalid option types, and
underlying schema violations fail. The wrapped processor must be a registered
action; sources, sinks, routers, nested processor declarations, and multi-step
compositions are not supported. Each use constructs an independent instance.

Resources, messages, connections, maps, and function references inside the action
resolve in its defining document. Flow metadata and error routes remain those of
the invoking flow. Inspection includes declaration name and origin without source
or argument values. Existing action side effects and cleanup rules still apply.

## Bounds and failures

`api.ProgramLimits.Mapping` and service `mappingLimits` configure the existing
mapping source/depth/operations/nodes/bytes/digits limits and these additions:

| Field | Default |
|---|---|
| `callDepth` | 64 (maximum configurable 512) |
| `functions` | 1000 across the program |
| `totalSourceBytes` | 16777216 across resolved function sources |

Calls inside mappings share their evaluation budget. Calls inside one top-level
Simple expression share a session, cumulative output limit, and cancellation context. String scanning,
collection comparisons, native input conversion, and function frames consume
budget. Compiled functions are immutable and safe for concurrent invocations.
Failures report declaration/call-site information without argument values. A
function does not modify the message; its consuming action applies the result only
after success. Reusable actions introduce no transaction or retry semantics.
