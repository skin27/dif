# First-class service contracts

A service contract declares a named message-exchange capability once in
`dil.core.services.service`: its exchange pattern (`InOut` or `InOnly`), its input,
output, and structured fault body formats, and its typed input and output headers.
Flows can implement and invoke internal capabilities with `service:<name>`, or bind a
contract to an external transport flow (such as `https`, `rest`, `queue`, or `message`)
with `flow.options.service`.

See the [runnable example](../api/testdata/services/README.md).

## Declarations

```json
{
  "core": {
    "resources": {
      "resource": [
        {"name": "OrderInSchema", "content": "{\"type\":\"object\",\"required\":[\"sku\",\"quantity\"],\"additionalProperties\":false,\"properties\":{\"sku\":{\"type\":\"string\"},\"quantity\":{\"type\":\"integer\",\"minimum\":1}}}"},
        {"name": "OrderOutSchema", "content": "{\"type\":\"object\",\"required\":[\"orderId\",\"sku\",\"quantity\"],\"additionalProperties\":false,\"properties\":{\"orderId\":{\"type\":\"string\"},\"sku\":{\"type\":\"string\"},\"quantity\":{\"type\":\"integer\"}}}"}
      ]
    },
    "formats": {
      "format": [
        {"name": "OrderIn", "mediaType": "application/json", "schemaRef": "OrderInSchema"},
        {"name": "OrderOut", "mediaType": "application/json", "schemaRef": "OrderOutSchema"},
        {"name": "OrderProblem", "mediaType": "application/json"},
        {"name": "AuditNote", "mediaType": "text/plain"}
      ]
    },
    "services": {
      "service": [
        {
          "name": "PlaceOrder",
          "exchangePattern": "InOut",
          "input": "OrderIn",
          "output": "OrderOut",
          "error": "OrderProblem",
          "inputHeaders": [
            {"name": "Customer-Id", "type": "string"},
            {"name": "priority", "type": "number", "default": 1}
          ],
          "outputHeaders": [
            {"name": "Order-Status", "type": "string"}
          ]
        },
        {
          "name": "RecordAudit",
          "exchangePattern": "InOnly",
          "input": "AuditNote"
        }
      ]
    }
  }
}
```

Place `services` under `dil.core`; `service`, `inputHeaders`, and `outputHeaders` each
accept a single object or an array (or `{"header": ...}` on header collections).
Unknown fields, duplicate service names within a document, and duplicate header names
within a header list fail preparation, even when no flow references the service.

| Field | Default | Meaning |
|---|---|---|
| `name` | required | Service name in its document; imported as `<namespace>:<name>` |
| `exchangePattern` | `"InOut"` | `"InOut"` or `"InOnly"`, case-insensitive |
| `input` | bodyless | Named `dil.core.formats` contract for the request/event body |
| `output` | bodyless | Named `dil.core.formats` contract for the normal `InOut` reply body |
| `error` | unstructured | Named `dil.core.formats` contract for explicit `return(status=error)` fault payloads |
| `inputHeaders` | none | Typed header contract enforced on entry |
| `outputHeaders` | none | Typed header contract enforced on normal `InOut` replies |

`InOnly` services reject `output`, `error`, and `outputHeaders` at preparation time.
Format references (`input`, `output`, `error`) resolve in the **service's defining
document**, including that document's direct imports, just like `dil.core.maps`.

## Body formats and `Content-Type`

Service contracts reuse `dil.core.formats.format`:

- Supported media types: `application/json`, `application/xml`, `text/xml`, `text/csv`,
  and `text/plain` (UTF-8 text or bytes).
- `schemaRef` remains JSON-only; CSV options (`delimiter`, `hasHeader`, `columns`)
  remain CSV-only; `xslt` mappings remain XML-only.
- **Bodyless** (`input` omitted, or `output` omitted on `InOut`): the body must be
  `nil`, `""`, or `[]byte{}`. Any non-empty body fails contract validation.
- **Formatted** (`input`, `output`, or `error` declared):
  - An empty or missing body fails contract validation.
  - When `Content-Type` is unset or blank, validation stamps the format's canonical
    `mediaType` after validating the payload.
  - When `Content-Type` is present, its base media type (ignoring parameters such as
    `; charset=utf-8`, with `application/xml` and `text/xml` compatible) must match the
    format's `mediaType`; conflicts fail contract validation without coercive conversion.

Validation is atomic: when any body or header check fails, the message retains its
pre-validation body and headers unchanged.

## Header contracts

Each header entry declares:

| Field | Default | Meaning |
|---|---|---|
| `name` | required | Exact case-sensitive header key (`"body"` and `"metadata.*"` are rejected) |
| `type` | required | `string`, `number`, `boolean`, `array`, `object`, or `any`, optionally suffixed with `?` for nullable (`string?`); `xml` is rejected |
| `required` | `true` without `default`, `false` with `default` | Whether callers/providers must supply the header; `required: true` with `default` is rejected |
| `default` | none | JSON literal applied (as a fresh deep copy) when an optional header is absent; must match `type` |

Messages remain **open envelopes**: undeclared user headers and DIF identity headers
(`Message-Id`, `Correlation-Id`, `Causation-Id`, `metadata.*`) pass through untouched.
Internally (`service:<name>`, `flowlink`, `Flow.Send`, `Flow.Request`), header types are
checked strictly against native message values (`number` accepts `json.Number`, signed/unsigned
integers, and finite floats).

On external `https` and `rest` sources, incoming HTTP headers and query parameters carry
wire-header metadata (`metadata.wireheaders`):

- If a declared header's exact case-sensitive name is absent on the message, a matching
  Go-canonicalized HTTP header key (`textproto.CanonicalMIMEHeaderKey(name)`) is promoted
  to the declared name (for example `X-Customer-Id` satisfies declared `x-customer-id`, and
  `Priority` satisfies declared `priority`).
- Wire string values on declared `number` and `boolean` headers are parsed into canonical
  `json.Number` or `bool` values (`"true"` / `"false"` only). Invalid scalar syntax fails
  input contract validation (`400 Bad Request`).

## Internal capabilities (`service:<name>`) and transport bindings

### Implementing and invoking capabilities

- **Provider (`source`):** `"uri": "service:PlaceOrder"` or `"uri": "service:orders:PlaceOrder"`
  implements the capability. Across a program or runtime, **at most one** `service:` source
  may implement a canonical service `(definingDocument, serviceName)`, even when different
  modules import the contract under different namespace aliases.
- **Caller (`action` or `sink`):** `"uri": "service:orders:PlaceOrder"` invokes the capability
  over an isolated internal channel (`queue:service:<canonical-id>`) that ordinary `queue:`
  steps cannot collide with. Reusable `dil.core.processors` actions may also invoke `service:<name>`.
  - `InOut` services require `action` and reject `sink` and `delivery: "enqueue"`. Options:
    `requestTimeout` ms (default `20000`), `overflow` (`fail`\|`block`), `enqueueTimeout` ms.
  - `InOnly` services work as `action` or `sink` with `delivery: "processed"` (default, waits
    for provider completion and propagates unhandled failures) or `delivery: "enqueue"`
    (buffers after caller-side input validation).
- **Contract-only modules:** Validating or describing a shared contract module with no provider
  flows succeeds. Any program or shell load that **calls** `service:<name>` must have a matching
  `service:` provider in the group or already registered in the runtime (`FlowBindings`).

### Binding external transport flows

Any flow whose source is not `service:` can bind a contract with `flow.options.service`:

```json
{
  "id": "orders-rest",
  "options": {"service": "contracts:PlaceOrder"},
  "steps": { ... }
}
```

Multiple transport flows may bind the same contract (for example REST and messaging adapters).
Preparation verifies that the source's exchange pattern matches the contract:

- `InOut` contracts require a request-reply source (`https`/`rest` with `InOut`, `flowlink`,
  `queue`, or `message`). One-way sources (`timer`, `repeater`, `counter`, `quartz`, `file`,
  `ftp`/`ftps`/`sftp`, `smb`, `imaps`, `topic`, `event`, `reply`, or `https`/`rest` with
  `exchangePattern: "InOnly"`) are rejected for `InOut` contracts.
- `https` and `rest` sources with `exchangePattern: "InOut"` are rejected for `InOnly` contracts;
  set `exchangePattern: "InOnly"` on the HTTP source to bind an `InOnly` contract.

## Runtime gates and structured faults

Contract enforcement runs at four points:

1. **Caller pre-check (`service:<name>` action/sink):** Validates outbound request/event input
   before channel admission, failing fast at the caller step even with `delivery: "enqueue"`.
2. **Flow entry gate:** Validates and normalizes `input` + `inputHeaders` at the source node
   before steps or entry interceptors run. Violations raise `*flowdef.ContractError`
   (`Kind: "input"`, matching `errorClass: "*ContractError"` on `failedexchange`).
3. **Normal `InOut` reply gate:** Validates `output` + `outputHeaders` whenever a normal reply
   is delivered — terminal completion, early `setoneway`, `return(status=success)`, or a
   `handled: true` error route that finishes without `return(status=error)`. Violations raise
   `*flowdef.ContractError` (`Kind: "output"`), failing the exchange rather than returning a
   non-compliant reply.
4. **Structured fault gate (`return` with `status: "error"`):**
   - When the contract declares `error`, the fault reply message is validated against that
     `Format` (stamping `Content-Type` if unset). A non-compliant fault payload raises
     `*flowdef.ContractError` (`Kind: "error"`).
   - Validated faults travel as `*flowdef.FaultError` (matching `errorClass: "*FaultError"`).
     - On `https` and `rest` sources: unhandled input `ContractError` answers **`400 Bad Request`**;
       `FaultError` answers **`500 Internal Server Error`** with the structured fault body,
       `Content-Type`, and user response headers; unhandled runtime errors or output/error
       `ContractError` breaches answer `500` plain text.
     - On `service:<name>` `InOut` callers: a provider `FaultError` fails the caller step and,
       when routed to the caller's error handler, populates `error.body` and `error.contentType`
       alongside `error.message`, `error.step`, `error.class` (`*flowdef.FaultError`), and
       `error.stacktrace`.

`Inspect`, `InspectProgram`, and `dif describe` expose declared services (name, origin,
exchange pattern, format names, header names/types/required/hasDefault) and flow/step service
references without exposing header default literals, schemas, or message payloads.
