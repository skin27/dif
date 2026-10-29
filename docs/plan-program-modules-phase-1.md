# Phase 1: DIL program and module foundation

Status: approved and implemented. The acceptance tests cover the document model,
scopes, immutable snapshots, validation, registration, and group startup.

## Outcome and scope

Load, validate, describe, and run all integrations and flows in a DIL document,
optionally composed with other local DIL documents. Keep the existing representation:
`dil.integrations.integration`, `flows.flow`, and `dil.core`.
A program is the resolved collection of DIL documents; a module is an imported DIL
document. Neither requires a new top-level construct or file format.

Existing single-flow documents, API calls, and valid commands retain their behavior.
The engine executes the existing flow model. No new dependencies are needed.

Phase 1 adds multiple integrations/flows, local imports, scoped shared declarations,
and diagnostics. Format/Mapper, functions, reusable processors, State/Endpoint,
service contracts, tasks, remote imports, package downloads, and hot reload remain
outside this phase.

## Document model

The current decoder already accepts one or many integrations and flows. The builder
restricts the result to one flow. Extend group loading rather than replace DIL.

This structural sketch omits flow contents; empty flow arrays are not runnable:

```json
{
  "dil": {
    "imports": {
      "import": [
        {"path": "./common.json", "namespace": "common"},
        {"path": "./flows/orders.json", "namespace": "orders"}
      ]
    },
    "integrations": {
      "integration": [
        {
          "id": "65367864455356",
          "name": "Base64Integration",
          "type": "esb",
          "version": "1",
          "flows": {"flow": []}
        },
        {
          "id": "orders-integration",
          "name": "OrdersIntegration",
          "type": "esb",
          "version": "1",
          "flows": {"flow": []}
        }
      ]
    },
    "core": {
      "messages": {
        "message": [{"name": "sample", "body": "hello"}]
      },
      "resources": {
        "resource": [{"name": "orderSchema", "content": "{\"type\":\"object\"}"}]
      },
      "connections": {"connection": []}
    }
  }
}
```

- `dil.imports.import` is the extension. Each entry has a local `path` and
  a required `namespace` for references to the imported core. No separate `files`
  list, `program` wrapper, mandatory document version, or flow-ID prefixes.
- All flows in every integration of a document share that document's `dil.core`.
  Core definitions stay at document level, not inside integrations or flows.
- A reusable file may contain only `dil.core`, only integrations, or both, and may
  import other DIL files. Importing includes its flows once and exposes its core
  under the importing namespace. Resolution never starts flows.
- Preserve object-or-array support for integrations, flows, and core collections;
  imports use the same `imports.import` convention.
- Preserve integration `id`, `name`, `type`, and `version` as descriptive metadata.
  They do not introduce a lifecycle. Missing metadata in old documents remains
  valid. Repeated non-empty integration IDs within one document fail group
  validation; across documents, integration identity includes the source file.
- Preserve tolerance for unrelated existing DIL export fields. Strictly validate
  the new import entries, including unknown fields and invalid paths/aliases.

A reusable core-only file is still DIL, for example `common.json`:

```json
{
  "dil": {
    "core": {
      "resources": {
        "resource": {
          "name": "customerSchema",
          "content": "{\"type\":\"object\"}"
        }
      }
    }
  }
}
```

Future definitions belong alongside the existing core collections. Candidate
locations are `dil.core.formats.format`, `dil.core.maps.map`,
`dil.core.functions.function`, and `dil.core.processors.processor`.
Their schemas and execution semantics are deferred to the corresponding phases.

## Names, scope, and references

Namespaces qualify imported core definitions; all flow runtime IDs stay unchanged.

| Reference | Meaning |
|---|---|
| `jsonvalidator:ref:orderSchema` | Resource in the defining file's core |
| `jsonvalidator:ref:common:customerSchema` | Resource in the direct `common` import |
| `setheaders:message:common:defaults` | Message in the direct `common` import |
| SQL2 `dataSource: "common:mainDatabase"` | Existing connection ID in imported core |
| Flowlink `targetFlowId: "68b173acaaa5120007000017"` | Exact runtime flow ID, unchanged |

- Import namespaces are document-local and case-sensitive. Aliases start with a
  letter or underscore and contain letters, digits, underscores, or hyphens.
  Existing declaration names and flow IDs acquire no new character restrictions.
- Resolve an exact local name first, preserving names that already contain colons.
  Otherwise resolve `alias:name` through a direct import. If a local name matches
  an imported qualified spelling, it remains local; use a different import alias.
- Imported flows use their defining file's scope. Parent declarations never leak
  into an import. Transitive imports are not automatically re-exported.
- Connections remain ordinary DIL core definitions. Sharing them reuses existing
  SQL2 resolution; it does not add State semantics or shared connection pools.
- Flow IDs must be unique throughout the loaded group and existing engine. Import
  aliases cannot instantiate flows twice. Flow display names may repeat; step and
  graph-link IDs stay flow-local.
- Never rewrite queue/topic names, paths, expressions, custom step options, or
  external addresses. Namespaces do not provide tenant/channel isolation.
- Group validation rejects duplicate aliases and duplicate declarations of the
  same kind within one core. Different kinds/documents can reuse local names.
  Imports do not merge declarations implicitly and do not override local core.
- Keep historical duplicate-core handling in the old single-flow API adapter.
  Group commands intentionally report ambiguous duplicates instead of choosing
  the last declaration; document this narrow validation tightening.
- Missing/wrong-kind core references fail static validation. Validate literal
  flowlink targets and compatible sources against the complete applicable group,
  plus registered flows in the shell. No new symbolic flowlink syntax is needed.

## Configured input messages

Keep the current default: each flow uses the first message in its own document's
`dil.core.messages`. This also applies to multi-flow files. Imported messages never
change that default.

Add optional `flow.options.inputMessage` to select a local or imported message.
Reject missing targets. Use the current body/header representation, with no new
template execution. This controls `NewMessage`/CLI send defaults, not automatic
message emission when a flow starts.

## Import resolution

1. Resolve paths relative to the declaring DIL file. Paths inside processor
   options retain working-directory semantics.
2. Read immutable document snapshots once; validate and construct from the same
   bytes even if files change afterward.
3. Canonicalize file identity, including symlinks and Windows path behavior.
   Deduplicate diamond imports while preserving each importer's aliases. Reject
   cycles with the complete import chain. Different aliases do not create new
   instances of the same file's flows.
4. Preserve deterministic document/import/integration/flow ordering. No network
   imports, glob expansion, or downloads. Relative parent paths are allowed.
5. Propagate cancellation and bounded reads. Default limits: existing 4 MiB
   per-document default, 256 distinct files, depth 32, and 64 MiB total snapshots.
   Existing standalone input limits retain their behavior.
6. HTTPS roots must be self-contained in phase 1. Reject their local imports.
   Bytes APIs require an explicit origin/resolver for imports; do not guess a
   local base for remote or anonymous documents.
7. Duplicate explicit entrypoints fail. For `--dir`, resolve discovered files and
   dependencies in one graph and do not independently reload imported files. Detect cycles
   even when every discovered file is imported. Independent ID collisions fail.

## Architecture and public behavior

```text
DIL document(s)
    -> immutable import graph
    -> integrations and document-scoped core declarations
    -> resolve references and validate every flow
    -> construct processors
    -> existing Flow/Node models and Runners
```

Separate validation from construction: no ports, decryption, keystore access, or
client initialization during validation. Static diagnostics identify file/import chain,
integration, flow, step, reference kind, and JSON location where available, without
printing option/resource values. Keep origin metadata outside engine semantics.

- Keep `Load`, `LoadBytes`, `Inspect`, `InspectBytes`, and runtime counterparts as
  single-flow APIs; never silently choose the first flow. Preserve old behavior.
  Imports require group APIs; old APIs explicitly reject imports rather than ignore
  them. Share decoding and graph-building logic through compatibility adapters.
- Add `LoadProgram`, `InspectProgram`, `ValidateProgram`, and a runtime group loader.
  These operate on DIL; their names do not imply a separate document format.
  Return ordered flows and integration/origin metadata; callbacks identify flow.
- Core-only roots can be validated/inspected. Running a resolved group with zero
  flows fails clearly. Add a bytes/origin/resolver path for prepared service inputs.
- Foreground `run --file`, `validate`, `describe`, and interactive `load`/`run`
  accept DIL with multiple integrations/flows and imports. Preserve single-flow
  output; group descriptions list integrations, dependencies, and references without
  values. Existing flow commands continue using the exact original IDs.
- `load` registers the whole prepared group stopped; `run` starts it. Add a small,
  DIL-independent engine `AddAll` for atomic registration. Prepare logs first and
  close them if preparation/registration fails.
- Reuse consumer-first initialization and the activation barrier for group starts;
  factor shared lifecycle code out of service plumbing where needed. Startup failure
  stops sources from that attempt. Shell flows remain registered/stopped for
  diagnosis; unrelated running flows remain untouched.
- No partial group is registered after construction failure, but do not promise
  rollback of existing constructor side effects such as creating directories.
  Preserve channel configuration, consumer checks, and shutdown behavior.

## Implementation sequence and affected code

1. **DIL parsing:** refactor `flows/impl/dil.go` and `build.go` to expose all
   integrations/flows, preserve metadata, and separate core/graph construction.
   Retain the single-flow adapter.
2. **Imports and scopes:** implement import resolution and shared existing core
   kinds, plus `options.inputMessage`. Use a focused `internal/program` package
   for filesystem snapshots/import graphs; DIL decoding stays in `flows/impl`.
   Avoid package cycles between resolver, parser, and public API.
3. **API/inspection:** extend `api/api.go`, `api/runtime.go`, and `api/inspect.go`
   with group loading, safe metadata, and common structural validation.
4. **CLI/service:** update `internal/service/inputs.go`, `service.go`, CLI commands,
   help/completion, and the engine's group registration helper. Reuse snapshots.
5. **Examples/docs:** add multi-flow/multi-integration DIL examples and core-only
   imports with shared messages/resources/dummy connections. Update README,
   deployment documentation, and tests.

## Verification and acceptance

- Existing single-flow fixtures and valid command behavior remain compatible;
  cover the intentional duplicate-core validation tightening.
- Load multiple flows and integrations in object/array forms without wrappers,
  document-version migration, or ID changes. Preserve integration metadata.
- Test shared local core, imported messages/resources/connections, per-document
  defaults, explicit input selection, lexical scope, colon-containing names,
  duplicate aliases/declarations/IDs, and missing/wrong-kind references.
- Test diamond imports, cycles, path resolution, Windows/symlink identity where
  supported, cancellation, dependency limits, HTTPS restrictions, and directory roots.
- Prove static validation makes no constructor calls, descriptions expose no values,
  and construction uses prepared snapshots.
- Test cross-integration/file flowlinks using original IDs, atomic registration,
  startup failure, cleanup, and activation barriers without disturbing running flows.
- Place dedicated composition fixtures under package-local `testdata`, preserving
  the existing `test/dil` single-flow sweep. Build and execute a complete composition
  example in dedicated tests with local/fake sources.
- Run relevant package tests, then `go test ./...`, `go vet ./...`, and
  `go build ./cmd/dif`; race-test changed lifecycle/registration paths when cgo
  and a C compiler are available.

The supplied Base64 example demonstrates the intended DIL structure. A runnable
fixture derived from it needs the missing comma after integration `version` and
distinct HTTP source paths (both pasted sources currently bind the same path).
These fixture corrections are separate from the composition feature.

## Main compatibility risks

Core lookup/default changes, duplicate loading through imports/directories, stricter
validation of old exports, changes to option-path bases, and partial group startup.
Preserving document scopes, IDs, DIL structure, and existing option semantics with
explicit regression tests addresses those risks.

Later construct schemas and execution semantics will be designed in their respective
phases. Existing constructor errors and side effects retain their processor-specific
behavior; static group schema diagnostics omit supplied option values.
