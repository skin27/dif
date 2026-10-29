# Typed connections

A typed connection describes a system outside the flow once: where it is, how to log in, and
which certificates to trust. Steps refer to it by name instead of repeating the same address,
credential, trust-store and timeout options. A runnable example is in
[`api/testdata/connections`](../api/testdata/connections/README.md).

```json
"core": {
  "connections": {
    "connection": {
      "id": "partnerSftp",
      "name": "Partner SFTP server",
      "type": "sftp",
      "host": "sftp.partner.example",
      "port": 22,
      "authentication": {"username": "dif", "password": "ENC(...)"},
      "hostKeys": {"knownHostsFile": "security/known_hosts", "strict": true},
      "socketTimeout": 30000
    }
  }
}
```

```json
{"id": "upload", "type": "sink", "uri": "sftp:/outbox", "options": {"connection": "partnerSftp", "fileName": "order.json"}}
```

## Two forms

`dil.core.connections.connection` holds one object or an array. Each connection has an `id`,
which steps use, and an optional descriptive `name`.

- **Legacy form**: a connection with `keys`, as the Java platform exports it (`jdbc`, `imaps`,
  `activemq`, ...). It is read as before: only the `dataSource` option of `sql2` uses it, and its
  keys are not checked.
- **Typed form**: a connection without `keys`. Its `type` is one of `https`, `sftp`, `ftps`,
  `smb`, `smtp`, `imaps`, `sql` or `as2`, and its fields are checked when the document is loaded,
  even if no step uses it. An unknown field, a field of another type, or a value of the wrong JSON
  type fails with an error that names the connection and the field, never a value.

## Fields

All fields are optional. Times are in milliseconds. A number may also be written as text, as DIL
converted from XML has it.

| Field | Types | Meaning |
|---|---|---|
| `host`, `port` | all except https | The server; without a port the step's default applies |
| `url` | https | The base address, such as `https://api.partner.example/v1` |
| `share` | smb | The share, the first part of the path |
| `dialect`, `database` | sql | `postgres`, `mysql`, `sqlserver` or `oracle` (as the `sql` step takes them), and the database |
| `connectTimeout` | https, sql, as2 | Time to connect |
| `socketTimeout` | all | Time to wait for the server |
| `authentication.username`, `.password` | all except as2 | Login |
| `authentication.token` | https, smtp, imaps | An access token (XOAUTH2 for smtp and imaps) |
| `authentication.privateKey`, `.passphrase` | sftp | A private key file, and its passphrase |
| `authentication.domain` | smb | The Windows domain |
| `tls.trustStoreFile`, `.trustStorePassword` | https, ftps, smtp, imaps, sql, as2 | The PKCS #12 trust store that trusts the server |
| `tls.implicit` | ftps | TLS from the start (port 990) instead of `AUTH TLS` |
| `tls.enabled` | sql, as2 | Connect over TLS |
| `tls.version` | sql | The one TLS version to use, such as `TLSv1.3` |
| `hostKeys.knownHostsFile`, `.strict` | sftp | The known hosts, and whether an unknown host key is refused |

There are no client certificates (mutual TLS) yet.

## Steps that take a connection

A step refers to a connection with the option `connection`: the `id` of a connection in its
own document, or `<namespace>:<id>` for one in a direct import. A reusable processor's action
refers to connections of the document that declares it; `connection` cannot be one of its
parameters.

| Type | Steps | What the connection sets |
|---|---|---|
| sftp | `sftp` source and sink, `sftpenrich` | address, `userName`, `password`, `privateKey`, `privateKeyPassphrase`, `knownHostsFile`, `strictHostKeyChecking`, `socketTimeout` |
| ftps | `ftps` source and sink, `ftpsenrich` | address, `userName`, `password`, `implicit`, trust store, `socketTimeout` |
| smb | `smb` source, sink and action, `smbenrich` | address with share, `userName`, `password`, `domain`, `socketTimeout` |
| https | `https` action | address, `authUsername` and `authPassword` (with `authMethod` Basic), trust store, `connectTimeout`, `socketTimeout` |
| smtp | `smtp`, `smtps` | address, `username`, `password`, `accessToken`, trust store, `timeout` (from `socketTimeout`) |
| imaps | `imaps` source | address, `username`, `password`, `accessToken` (with `authenticationType` oauth), trust store, `socketTimeout` |
| as2 | `as2` action | `hostName`, `targetPortNumber`, `sslContext` (when it has `tls`), trust store, `connectTimeout`, `socketTimeout` |
| sql | `sql`, `sql2` | `connectionType`, `host`, `port`, `database`, `username`, `password`, `useSSL`, `tlsVersion`, trust store, `connectTimeout`, `socketTimeout` |

`sql2` also takes a typed `sql` connection through `dataSource`, as it takes a legacy one: the
loader gives it the address and login as its option `connection`, and the rest as above.

A connection of a type the step does not take, or with a field the step cannot use (such as
`authentication.token` on the `https` action, which has no bearer option), fails when the flow is
validated. Nothing a connection sets is silently ignored.

## Precedence and the address

- An option the step sets itself wins over the connection. A step can, for example, use a
  longer `socketTimeout` than its connection.
- When the connection has an address (`url`, or `host` with optional `port` and `share`), the
  step's `path` is relative to it: `sftp:/outbox` with the connection above is
  `sftp.partner.example:22/outbox`, and `https:/orders` with `url` `https://api.partner.example/v1`
  is `https://api.partner.example/v1/orders`. A step whose server is the whole address (`imaps`,
  `smtp`) leaves the path out: `imaps:`.
- Without a password in either the connection or the step, a step still reads its environment
  variable, such as `DIF_SFTP_PASSWORD`.
- A connection without `tls` leaves the step's own default trust store in place.

## Secrets

Values can be encrypted, `ENC(...)`, as in any option (see "Encrypted values" in the README).
They are decrypted when the step is built, never during validation or `describe`. `describe`
lists the connections of each document by `id` and type, and each step's reference, without values.
