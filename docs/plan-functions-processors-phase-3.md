# Phase 3: typed functions and reusable processors

Status: implemented. The approved proposal is retained below as the design record.
Proposed: 2026-10-10. Implementation completed: 2026-10-12.
Implementation reference: [Functions and reusable actions](functions-language.md).
Runnable examples: [Functions examples](../api/testdata/functions/README.md).

Validation passed: `go test ./...`, `go vet ./...`, `go build ./cmd/dif`,
example program validation, named-function parser and processor-binding fuzz runs,
and function benchmarks. Targeted Simple tests also passed after the final depth
bounds fix. Race testing was unavailable because CGO is disabled in this environment.
No dependencies were added; implementation changes remain uncommitted.

This completes the three-part DIF language plan: (1) DIL programs/modules,
(2) formats and mappings, (3) functions and reusable processors. It is unrelated
to the historical expression-compatibility phase in `handover-phase-3.md`.
State, Endpoint, Service contracts, and task orchestration remain deferred.

## Decision

Add named, typed, pure functions under `dil.core.functions.function`, callable
from both Simple expressions and `difmap/1` mappings. Use `difmap/1` for their
bodies, extending the existing evaluator rather than introducing another parser.
Add the highest-value missing mapping helpers and parameterized single-action
definitions under `dil.core.processors.processor`.

Functions compute values. Processor definitions configure message operations.
Keep DIL's envelope, imports, names, and flow IDs. Keep the engine independent of
DIL and expression syntax. Add no dependencies.

## Existing support and actual gaps

Inspection of `steps/impl/simple_funcs.go`, `simple_call.go`,
`simple_functions.go`, and `simple_state.go` shows substantial existing Simple
support: body/header/variable access, conversions, string normalization and
substrings, split/join, collection operations, arithmetic aggregates, conditions,
JSONPath, XPath, jq, hashes, UUIDs, dates, and random values. Preserve these.

`internal/mapping/eval.go` already supplies strict conversions, trim/upper/lower,
size/sum/round, exists/isNull, and XML construction/selection. Its parser validates
against a fixed builtin table; it currently has no named function declarations.
The value model supports Missing, ordered objects, XML nodes, and exact numbers.

The main missing capability is naming shared business logic and using it across
flows and mappings. Reimplementing the whole Camel catalogue would add little
value. Camel is the reference for familiar use cases and Simple entry syntax;
new DIF function contracts are explicit and do not promise full Camel parity.

## 1. Named function declarations

Proposed DIL syntax; these fields are not implemented yet:

```json
{
  "dil": {
    "core": {
      "functions": {
        "function": [
          {
            "name": "normalizeCustomerId",
            "parameters": [{"name": "value", "type": "string"}],
            "returns": "string",
            "language": "difmap/1",
            "expression": "upper(trim(value))"
          },
          {
            "name": "lineTotal",
            "parameters": [
              {"name": "quantity", "type": "number"},
              {"name": "price", "type": "number"},
              {"name": "scale", "type": "number", "default": 2}
            ],
            "returns": "number",
            "language": "difmap/1",
            "expression": "round(quantity * price, scale)"
          }
        ]
      }
    }
  }
}
```

The collection accepts one object or an array, following existing core sections.
Require name, parameters (possibly empty), returns, language, and exactly one
of expression/sourceRef. Resolve sourceRef to an existing core text resource in
the defining document. Include source in immutable program snapshots.

Contract rules:

- Types: `string`, `number`, `boolean`, `array`, `object`, `xml`, and `any`.
  A `?` suffix permits null for a concrete type. `any` admits null and all supported
  value kinds, but never Missing or arbitrary Go objects. Collections are shallow
  type contracts; element schemas/generics are deferred.
- No implicit coercion. Use string/number/boolean explicitly at call sites.
  Check known literal types at preparation and every argument/result at evaluation.
  This is runtime contract checking, not a claim of complete static inference.
- Defaults are immutable JSON literals matching the parameter type, used only
  for omitted trailing arguments. Explicit null never selects a default.
  Required parameters precede defaulted parameters. No variadics or overloading.
- Evaluate provided arguments once, left to right. Function-local bindings cannot
  mutate arguments, the message, or another invocation's data.
- Function bodies see only parameters, lexical locals, pure builtins, and statically
  bound function calls. They cannot implicitly access payload, headers, tenant
  variables, clock, randomness, environment, files, network, or processors.
  Pass the required message values explicitly. XML inputs retain phase 2 semantics.
- Reject duplicate parameters, reserved parameter names (`payload`, `headers`,
  grammar keywords), invalid declarations, and unknown fields during preparation.
  Allow existing core-style names, including colons, since calls use string names.

Do not support Simple as a function-body language initially. Its current access
to message state and its conversion rules make it a poor shared pure-function
contract. Simple remains a caller; its existing standalone behavior stays intact.

## 2. Calls from mappings and Simple

In mappings and function bodies, add a compiler-recognized call form:

```text
{
  id: function("common:normalizeCustomerId", payload.customerId),
  total: function("common:lineTotal", number(payload.quantity), number(payload.price))
}
```

The first argument must be a literal function name. This avoids new qualified-name
grammar and builtin collisions, and makes call graphs statically discoverable.

In Simple, add:

```text
${function('common:normalizeCustomerId', ${header.customerId})}
${function('common:lineTotal', ${header.quantity}, ${header.price})}
```

Callers must supply typed numeric values for number parameters. String headers
should be converted explicitly; wrapping a conversion in a named string-accepting
function is useful for exact decimal input. Do not route decimal amounts through
Simple's integer conversion.

The name is literal and all arguments are explicit; no implicit-body overload.
Nested standalone expressions preserve typed values; mixed text arguments become
strings according to existing Simple rules. A standalone result stays typed until
the consuming processor applies its existing rendering behavior. An embedded call
renders into surrounding text. This is a DIF extension inspired by Camel's function
entry point, not an implementation of every Camel overload.

Define and test the bridge explicitly: native integers, finite floats (already
rounded by their producer), json.Number, strings, booleans, null, arrays, and
string-keyed objects convert to bounded mapping values. Reject cyclic/unsupported
native values. Return exact numeric values through an adapter supporting rendering
and Simple numeric comparisons without a float64 round-trip. Arrays/objects from
new calls render as JSON, without changing legacy jlist/jmap formatting. XML values
remain callable within mappings/functions; reject them at the Simple bridge until
an explicit XML conversion contract is added. Missing cannot cross call boundaries.

Make scoped calls available in configured Simple body/header/variable expressions,
predicates, and templates, including router links. Inventory every compiler entry
point. Runtime-generated expressions such as simplereplace retain their existing
late compilation: resolve any literal function names against the already prepared
scope at that point, and apply the same bounds. Unresolved names there fail at
evaluation; static configured expressions fail before flow registration.

## 3. Most important library additions

Keep all phase 2 builtins and add the following to the pure `difmap/1` library.
Existing Simple counterparts are retained with their current syntax and behavior;
they are not replaced by these stricter contracts. All new helpers are available
inside named functions, which makes them reachable from Simple too.

| Priority | Functions | Exact initial contract and purpose |
|---|---|---|
| P0 | `isEmpty(v)` | True for Missing, null, empty string/array/object; false for zero, false, and whitespace text. Other supported values return false. Useful for required-field checks. |
| P0 | `coalesce(a, b, ...)` | At least two operands; lazy first non-Missing/non-null value, otherwise null. Preserve empty string, false, and zero. Complements binary `default`. |
| P0 | `replace(text, old, new)` | Literal replacement of every occurrence; reject empty old text. Common identifier cleanup. |
| P0 | `substring(text, start, end)` | Zero-based Unicode rune indexes, end exclusive; require integers and `0 <= start <= end <= size(text)`, otherwise error. |
| P0 | `contains(text, part)`, `startsWith(text, prefix)`, `endsWith(text, suffix)` | Case-sensitive literal string predicates; empty search string succeeds. Useful for routing and validation. |
| P0 | `split(text, separator)`, `join(strings, separator)` | Literal separator; split rejects empty separator and preserves empty fields. join accepts only string elements; empty array yields empty string. |
| P0 | `min(numbers)`, `max(numbers)`, `abs(number)` | Exact numeric operations, no coercion. min/max reject empty arrays. Needed for bounds and business calculations. |
| P1 | `distinct(array)` | Stable first-occurrence order using existing deep equality and exact numeric equality; bounded comparison work. |
| P1 | `keys(object)`, `values(object)` | Return arrays in the object's existing member order; same positions correspond. |
| P1 | `formatDate(text, inputLayout, outputLayout, zone)` | Parse/format with explicit Go time layouts. Require an offset-bearing input layout; reject offset-free input and invalid dates. Zone is UTC or an IANA zone; no machine-local default. |

P0 and P1 are both in phase 3; the priority sets implementation order. These are
16 additions. `formatDate` is a deliberately small date conversion contract,
separate from legacy Simple's Java-pattern `date:now` behavior. Package timezone
data using the standard library so IANA zones behave consistently across hosts.
Reject ambiguous timezone-abbreviation-only input; unsupported layout/zone and
parse failures are errors, never fallback to the current time.

All strict string/collection/numeric helpers reject null, Missing, and wrong types
except where explicitly stated. Prefer `value default ...` before a strict helper.
Do not add another concat (use `++`), rounding implementation, selector language,
or conversion vocabulary. Coalesce is a lazy AST form despite function-call syntax.

Deferred: regex replacement, arbitrary date arithmetic, cryptographic APIs,
locale-sensitive formatting, custom Go/Java plugins, dynamic function names,
recursion, user lambdas beyond existing map/filter, and a full Camel catalogue.
Existing Simple hash/UUID/random/date/JSONPath/XPath/jq remain available at their
current call sites; they are not imported wholesale into pure function bodies.

## 4. Scope, resolution, and cycle detection

Use the phase 1 core lookup rules: exact local name first, then imported names
qualified with a namespace. Function and processor names occupy separate core
categories. Bind references to declaration identity (document plus kind plus
name), not a process-global name string.

The caller's scope resolves the selected declaration. That declaration's own
scope resolves its source and all nested calls. An imported function must retain
its private dependencies; a caller cannot replace them with a same-named local
function. Maps use the scope in which the map was defined, including when invoked
through an imported processor definition.

Prepare all declarations, even unused ones. Reject missing names, wrong arity,
invalid defaults, and direct/indirect cycles with the full declaration path.
Build a graph from parsed AST calls; never discover references with regex or text
replacement. Reject recursion even if a branch appears unreachable. Compile an
acyclic function graph in dependency order, then maps and configured call sites.
Apply bounded import and call graph traversal before recursive compilation.

## 5. Reusable processor definitions

Keep the initial processor feature small: one named configuration of one existing
action. Parameters bind during preparation; they are configuration values, not
per-message function arguments. Functions may appear in the action's ordinary
runtime expressions. Proposed example:

```json
{
  "processors": {
    "processor": {
      "name": "setNormalizedCustomerId",
      "parameters": [],
      "action": {
        "uri": "setbody",
        "options": {
          "language": "simple",
          "expression": "${function('normalizeCustomerId', ${header.customerId})}"
        }
      }
    }
  }
}
```

A parameterized setbody variant has
parameter `text` of type string and sets its constant-language `expression` to
`{"$param":"text"}`. This binds a complete option value, without interpolating
arbitrary JSON or altering Simple `${...}` syntax.

Invoke an action configuration with `processor:<name>` or
`processor:<namespace>:<name>` and `options.arguments` containing a named argument
object. Reject missing required/unknown arguments and arbitrary option overrides.
Parameter references are exact single-member `{"$param":"name"}` objects in
action options only; use `{"$literal": value}` to escape a literal marker-shaped
object. Recursively bind option values, preserve their types, then validate the
fully resolved options with the existing action schema. URI stays a literal.

Do not allow another `processor:` URI as the wrapped action in this version.
Sources, sinks, routers, links, multi-step composites, and runtime configuration
rebinding are deferred. Reject these during preparation. Function cycles are
checked as above; processor nesting is rejected rather than silently expanded.

Each use builds its own underlying processor through the existing registry.
Retain the invoking step ID and error route, and existing construction/cleanup
ownership. Never cache processor instances in a declaration. Resolve resource/map
references and function calls inside a template in its defining document; normal
flow metadata describes the invoking flow. Keep declaration-origin and call-site
information for errors and describe output.

## 6. Implementation architecture

| Area | Proposed change |
|---|---|
| `flows/impl/dil.go`, `document.go`; new `functions.go`, `processors.go` | Decode/validate declarations; resolve lexical scopes, sources, and call graphs; bind action templates. |
| `internal/mapping/parser.go`, `eval.go`, `value.go` | Add scoped compilation with parameter environments and literal named calls, contract checking, pure helpers, and callable plans using the existing exact value model. Keep evaluator code in this package initially to avoid a broad extraction. |
| `internal/mapping/plan.go`, `flows/impl/mapping.go` | Supply immutable function bindings to maps while retaining existing Compile callers through an empty-scope wrapper or additive API. |
| `internal/program/resolve.go`, `api/program.go` | Prepare functions before maps/flows; snapshot sources; include metadata and configurable limits in prepare/describe APIs. |
| `steps/definition`, `flows/definition`, `flows/impl/build.go` | Carry immutable trusted callable bindings and declaration scope to processor construction; never serialize closures into DIL. Reuse the existing RuntimeBindings mechanism. |
| `steps/impl/simple_compile.go`, `simple_call.go`, `simple_flow.go`, `predicate.go`, expression consumers | Add the function entry point and typed bridge; thread scope/context through every relevant compile/evaluation path without using global mutable registries. |
| `steps/impl/builtin.go`, registry validation | Advertise trusted bindings only where needed. Resolve reusable processors before constructing their underlying action; no new engine execution type. |
| README, `docs/mapping-language.md`, new function reference, `api/testdata/functions/` | Document contracts, differences from Simple, imports, failure examples, and runnable shared-function examples. |

Do not merge Simple's permissive values with mapping's strict values. Share only
primitives with identical semantics; for example legacy Simple trim and difmap
Unicode trim must keep their current behavior. Keep old compile wrappers working
with an empty function scope for callers that do not load DIL declarations.

## 7. Limits and error behavior

Every call shares the invoking evaluation's cancellation and cumulative budget;
nested calls must not reset it. Start with phase 2 source/depth/operations/nodes/
bytes limits for function evaluation, plus a maximum call depth of 64 and 1,000
function declarations per program (host-configurable). Bound the total resolved
source size at 16 MiB as well as per-source size. Preparation validates limits.
Keep existing mapper limits authoritative when functions run inside mappings;
Simple calls receive a bounded evaluation context for each top-level expression.

Account for string scans, equality comparisons, collection growth, native-value
conversion, and function frames, not merely AST visits. Static graph depth checks
and runtime call-depth checks are both required. Test cancellation inside long
collection operations and concurrent use of the same compiled plan.

Errors identify declaration, origin, call site, parameter/return contract, and
source line/column where available. Do not include message data, source excerpts,
or parameter values. Function failure leaves the message unchanged until its
consumer succeeds. Existing processor side effects retain existing behavior;
reusable action configuration adds no transaction or retry semantics.

## 8. Delivery order and acceptance criteria

1. **Declarations and resolution.** Parse core functions, parameters/defaults,
   sourceRef, imports, graph validation, snapshots, and describe metadata.
2. **Typed evaluation and mappings.** Add callable plans, shared budgets, named
   calls from maps/functions, and P0 helpers. Demonstrate a shared normalizer and
   exact invoice calculation across two imported maps.
3. **Simple integration.** Carry scope into expression consumers and predicates,
   implement the typed bridge, and add the P1 helpers. Demonstrate the same
   normalizer in a header/body assignment and routing predicate.
4. **Reusable actions.** Bind typed configuration parameters, validate underlying
   action schemas, and preserve per-step instances, scope, and cleanup behavior.
5. **Documentation and regression verification.** Publish runnable examples and
   an explicit supported-function table. Complete this milestone without adding
   State, Endpoint, Service, or composite execution features.

Required behavior tests:

- Local/qualified lookup, names containing colons, local precedence, imported
  private dependencies, duplicate/missing declarations, and unused-invalid errors.
- Direct and mutual cycles, argument order, omitted defaults versus explicit null,
  wrong types/arity/results, parameter isolation, and prohibited implicit roots.
- Numbers beyond 2^53, exact decimal round trips through Simple, false/zero/empty
  preservation, Missing/null behavior, Unicode indexes, and lazy error branches.
- All 16 helper contracts including empty collections, invalid dates/layouts/zones,
  stable distinct order, and bounded expensive comparisons.
- Same function through mapping, Simple predicates, and assignments; legacy
  rendering/conversions unchanged; standalone versus embedded call results.
- Runtime-generated Simple expressions use the correct prepared scope; unknown
  runtime names fail safely and never create or load declarations dynamically.
- Typed processor option binding, marker escaping, unknown arguments, action-kind
  rejection, defining-scope resources, independent processor state, and cleanup
  on failed group construction.
- Cancellation, depth/source/output limits, cumulative budgets across calls, no
  cross-message data leakage, and no message mutation on function failure.

Run targeted evaluator/resolver/Simple tests, all existing DIL fixture tests,
`go test ./...`, `go vet ./...`, and `go build ./cmd/dif`. Run `go test -race ./...`
where cgo and a C compiler are available; report unavailability explicitly. Fuzz
the new call parsing and parameter binding boundaries, and benchmark repeated
calls/collection helpers. No performance claim is required before measurement.

## References

- Local phase 1 plan: `docs/plan-program-modules-phase-1.md`.
- Local phase 2 implementation record: `docs/plan-formats-mapping-phase-2.md`.
- Current mapping contract: `docs/mapping-language.md`.
- [Camel Simple overview](https://camel.apache.org/components/next/languages/simple-language.html).
- [Camel built-in functions](https://camel.apache.org/components/next/languages/simple-functions.html).

The Camel links describe the moving Next documentation. They inform selection and
familiar syntax; this proposal's versioned DIF contracts govern the implementation.

