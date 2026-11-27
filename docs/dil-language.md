# DIL text: the `.dil` language

DIL JSON is a serialization: it is converted from XML, and connects steps by
link ids that are matched by hand (`testdata/hello.json` takes 80 lines for a
source, an action and a sink). DIL text is a language with a grammar of its own
for the same model. A `.dil` file is parsed to an AST, and the AST is *lowered*
to DIL JSON; from there DIF validates, describes, builds and runs it exactly as
it does DIL JSON. The engine and the flow model know nothing of the text.

```text
# hello.json as DIL text
message hello {
  body: "HELLO WORLD"
  header greeting: "hello"
}

flow hello(name: "hello") {
  message:hello as hello-source
    -> passthrough as hello-action
    -> passthrough as hello-sink

  on error as hello-error
}
```

`dif convert` turns one representation into the other (see [Tools](#tools)).
The examples are [`testdata/hello.dil`](../testdata/hello.dil) and the program
in [`api/testdata/dil/`](../api/testdata/dil/README.md), the service contracts
example written as text.

## Files and loading

- A file whose name ends in `.dil` is DIL text, wherever DIF reads a document:
  the shell (`load`, `run`, `validate`, `describe`), `dif run` and
  `dif validate` (`--file`, `--dir`, positional files), the Go API (`api.Load`,
  `api.Inspect`, `Runtime.Load`, `api.LoadProgram`, `api.InspectProgram`,
  `api.PrepareProgram`) and imports.
- Any other document, such as bytes that come without a file name
  (`api.LoadBytes`, `api.InspectBytes`, `--url`, a `ProgramSource` without a
  `Path`), is recognised: it is DIL text when it is not JSON and starts with `#`
  or a letter. A DIL JSON document starts with `{`; DIL text never does. Every
  JSON document is read as DIL JSON, exactly as before.
- A file name without an extension still means `.json` (`run hello` runs
  `hello.json`); write `.dil` for text. `--dir` takes the visible `*.json` and
  `*.dil` files of a directory, so keep a flow in one of the two forms there.
- Imports may name `.dil` or `.json` files; one program can mix them.
- The limits of a program (4 MiB a document, 64 MiB in all by default) apply to
  the text as read.

## Lexical structure

| Element | Form |
|---|---|
| Comment | `#` to the end of the line, anywhere but in a string |
| Word | Letters, digits and `_ - . : / @ + * ? = & % ~ ! $`. A word never ends with `:` (in `name: value` the colon separates, so `name:value` is a single word) and stops before `->`. URIs such as `service:orders:PlaceOrder`, `timer:tick` and `https://host:9002/path?a=1` are words. |
| String | `"..."` or `'...'`, on one line, with the escapes of JSON (`\" \\ \/ \b \f \n \r \t \uXXXX`) and `\'`. `${...}` is plain text. |
| Raw string | `` `...` `` spans lines and has no escapes; carriage returns are dropped, so CRLF files read the same. It cannot contain a backquote: use a quoted string. |
| Number | A word with the syntax of a JSON number, in a value position: `5`, `-1.5e3` |
| Punctuation | `->` `(` `)` `{` `}` `[` `]` `,` `:` |

Whitespace and line breaks only separate tokens. Commas between options, fields
and list elements are optional.

Keywords are words with a meaning in one position only: `import`, `as`,
`integration`, `flow`, `on error`, the declaration kinds, `header` (in a
message), `source`/`action`/`router`/`sink` (before a step) and `_` (a branch
without a rule). Anywhere else they are ordinary words. A name that is not a
word, or that would be taken for a keyword, is written as a string:
`"my flow"`, `"https://host/path#part"`, `-> "sink"`.

## Grammar

In EBNF; `word` and `string` are the tokens above (a raw string is a string).

```ebnf
File         = { Import | Declaration | Integration | Flow } .
Import       = "import" Name "as" Name .
Declaration  = Kind [ Name ] "{" { ( Field | Header ) [ "," ] } "}" .
Kind         = "function" | "processor" | "interceptor" | "format" | "map"
             | "service" | "message" | "resource" | "connection" | "store" .
Header       = "header" Name ":" [ word ] string .        (* in a message only *)
Integration  = "integration" [ Name ] [ Params ] "{" { Flow } "}" .
Flow         = "flow" [ Name ] [ Params ] "{" { Chain | Handler } "}" .
                                                          (* at most one Chain *)
Handler      = "on" "error" [ Params ] [ "as" Name ] [ "{" Chain "}" ] .
Chain        = Step { "->" Step } .            (* a Step with a Block ends it *)
Step         = [ StepType ] Name [ Params ] [ "as" Name ] [ Block ] .
StepType     = "source" | "action" | "router" | "sink" .
                                   (* only when a word or string follows *)
Block        = "{" { Branch } "}" .
Branch       = ( "_" | Name ) [ [ word ] string ] "->" Chain .
Params       = "(" { Field [ "," ] } ")" .
Field        = Name ":" Value .
Value        = string | number | "true" | "false" | "null" | List | Object .
List         = "[" { Value [ "," ] } "]" .
Object       = "{" { Field [ "," ] } "}" .
Name         = word | string .
```

Values are those of JSON; text is always quoted (`method: "post"`, not
`method: post`), so `1`, `"1"`, `true` and `"true"` stay apart.

## Lowering: what each construct becomes

The lowering writes `dil.imports`, `dil.core` and `dil.integrations`, every
collection as an array.

| Text | DIL JSON |
|---|---|
| `import "x.dil" as ns` | `dil.imports.import[]`: `{"path": "x.dil", "namespace": "ns"}` |
| `<kind> N { f: v ... }` | an object `{"name": "N", "f": v, ...}` in `dil.core.<kind>s.<kind>[]`, such as `dil.core.maps.map[]`; for a `connection`, `N` is its `"id"`. Without `N`, the fields alone (a `name` or `id` field then names it). |
| `header Name: "v"` in a message | `{"name": "Name", "value": "v"}` in the message's `headers.header[]`; `header Name: simple "${...}"` adds `"language": "simple"` |
| `integration I(f: v ...) { ... }` | `{"id": "I", "f": v, ..., "flows": {"flow": [...]}}` in `dil.integrations.integration[]` |
| `flow F(...)` outside an integration | a flow of one integration without fields, placed where the first such flow is written |
| `flow F(name: n, o: v ...)` | `{"id": "F", "name": n, "options": {"o": v, ...}, "steps": {"step": [...]}}`: `name` (and `id`) are the flow's own fields, every other parameter one of its `options` (`inputMessage`, `service`, `tenant`, `environment`, `version`) |
| `uri(o: v ...) as S` | a step `{"id": "S", "type": ..., "uri": "uri", "options": {"o": v, ...}}` |
| `A -> B` | a link from `A` to `B`: an outbound link on `A` and an inbound link with the same generated id on `B` (`l1`, `l2`, ... per flow) |
| `R { rule lang "expr" -> C ... }` | `R` gets one outbound link per branch, in the order written, with `rule`, `language` and `expression` when not empty; each leads to the first step of its chain `C` |
| `on error(o: v ...) as E { C }` | a step `{"id": "E", "type": "error", "uri": "failedexchange", "options": {...}}` whose outbound link leads to the first step of the error route `C` |

Steps are written in the order of the text: the chain from the source, each
step before the routes that follow it, then each error handler before its
route. The order of error handlers is kept (the first that takes an error is
used).

### Step types

A step's type is inferred from where it stands:

1. the first step of a flow's chain is its **source**;
2. a step with a block of branches is a **router**;
3. the last step of a chain is a **sink**;
4. any other step is an **action**.

The first step of an error route is not a source: it is an action, router or
sink by rules 2 to 4. Write the type before the URI where the inference would
give another one, as the README's [Processors](../README.md#processors) allows:
a router processor in an action position (`-> action split as s { split xpath
"//item" -> ... }`), or a sink in the middle of a chain (`-> sink file(...) ->
log`). The printer writes the type whenever the inference would not give the
type of the JSON.

### Step ids

`as <id>` names a step. Without it, the lowering generates an id from the scheme
of the URI and a counter per flow: `setbody-1`, `setbody-2`, `service-1`,
`error-1` for an error handler, skipping ids written in the flow. Generated ids
are deterministic, but they change when steps are added before them: give an
id with `as` to every step that something refers to (error handlers with
`errorStep`, interceptors with `pointcut.steps`, flowlinks, logs you search).
`dif convert` always writes the ids.

### Routers and branches

```text
rest(method: "post", path: "/orders") as in
  -> content as by-type {
       when simple "${header.type} == 'rush'" -> queue:rush as rush
       otherwise -> queue:standard as standard
     }
```

A branch is the outbound link of a router and the chain it leads to. Its head
is the link's `rule`, then optionally its condition: a `language` word and an
`expression` string. `_` stands for no rule; it is how the continuation of a
`wiretap`, `enrich` or `split` and each route of a `recipient` list are
written:

| Branch head | rule | language | expression |
|---|---|---|---|
| `when simple "${x} > 1"` | `when` | `simple` | `${x} > 1` |
| `otherwise` | `otherwise` | | |
| `split "//item"` | `split` | | `//item` |
| `split xpath ""` | `split` | `xpath` | |
| `_ jsonpath "$.a"` | | `jsonpath` | `$.a` |
| `_` | | | |

The routes run in the order they are written, as in JSON. A router's block
ends its chain: what follows it is one of its branches, never `-> step` after
the closing brace. Flows are trees (every step has one inbound link and
branches never join), so nesting expresses every flow DIF builds.

### Error handlers

```text
on error(errorClass: "*FaultError") as catch-fault {
  setbody(expression: "${header.error.body}", language: "simple")
    -> passthrough
}
on error(maximumRedeliveries: 3, redeliveryDelay: 500) as retry
```

A flow may have several; see the README's [Error handlers by
error](../README.md#error-handlers-by-error).

### Declarations

Every `dil.core` kind has one generic form, `kind name { field: value ... }`,
which is lowered field by field: whatever a kind accepts in JSON can be written,
and DIF checks the fields as it checks JSON. Raw strings hold the multi-line
texts: JSON Schemas and templates in resources, `difmap/1` expressions in maps
([mapping language](mapping-language.md)) and function bodies ([functions
language](functions-language.md)). Embedded languages (simple, difmap/1,
xpath, jsonpath) are strings to the text syntax.

```text
resource OrderInSchema {
  content: `{
  "type": "object",
  "required": ["sku", "quantity"]
}`
}

format OrderIn { mediaType: "application/json", schemaRef: "OrderInSchema" }

map ConfirmOrder {
  input: "OrderIn"
  output: "OrderOut"
  language: "difmap/1"
  expression: `{
  orderId: "ORD-" ++ payload.sku,
  sku: payload.sku
}`
}

connection partnerSftp {
  type: "sftp"
  host: "sftp.partner.example"
  authentication: {username: "dif", password: "ENC(...)"}
}

store rates { durable: true, ttl: 3600000 }

processor text {
  parameters: [{name: "value", type: "string", default: "Hello"}]
  action: {uri: "setbody", options: {language: "constant", expression: {$param: "value"}}}
}

message ValidOrder {
  body: '{"sku":"BOOK-1","quantity":2}'
  header Idempotency-Key: "idem-100"
  header Sent: simple "${date:now:yyyy-MM-dd}"
}
```

A `store` is lowered to `dil.core.stores.store[]`: a named key-value store with
`durable` (default `false`), `ttl` (milliseconds an entry lives after it was
last put; `0`, the default, keeps it) and `maxKeys` (`0`: 100,000). Steps use it
as `store:rates` and expressions as `${store:rates:<key>}`; see the README's
[Stores](../README.md#stores).

The only sugar is the `header` line of a message, for `headers.header[]`; a
message may instead have a `headers` field, not both.

## Errors

A syntax error names the file, line and column, and what was expected:

```text
orders.dil:12:5: expected '->' before the next step, 'on error' or '}' to end the flow, found a word
```

Errors never show the text of a string or a word, which may be a credential.
Errors found later, by DIF's validation of the lowered JSON, name the steps by
their ids, as for JSON; they do not point into the text.

## Tools

`dif convert <file> [--to dil|json] [--output path]` (also in the shell):

- converts a `.json` file to DIL text and a `.dil` file to DIL JSON; `--to`
  sets the direction, and a document is not converted to its own form;
- writes to the screen, or to a new file with `--output`: it never overwrites a
  file.

Converting JSON to text writes what DIF reads and leaves out what it ignores:
the fields DIL exports but DIF does not read (`transport` and `pattern` of a
link, `type` of a flow, extra fields of a header) and the link ids. Comments in
text are not kept in JSON. `init <name>.dil` creates a starter flow as text.

From Go, `internal/dilsyntax` has `Compile` (text to JSON), `Print` (JSON to
text), `Parse` and `Lower`.

## Design decisions

- **One model, two representations.** The text is lowered to DIL JSON bytes;
  nothing after that knows the text. Converting a JSON document to text and
  back gives a document that DIF reads the same (every fixture of the
  repository is checked: `go test ./internal/dilsyntax`).
- **Values are JSON.** Option values keep their JSON types, so text is quoted.
- **Inference with an override.** The common case needs no step types; the
  rare case is still expressible, so every JSON flow can be written.
- **`#` comments only**, since `//` would be ambiguous in URIs.
- **No formatter.** A formatter through the JSON model would drop comments;
  `convert` refuses to convert text to text.
