# Foreground service deployment

Build from the repository root with `docker build -t dif:local .`. The image
contains a static binary, CA roots and embedded timezone data, and runs as UID
65532 without a shell. `GO_VERSION` selects the build toolchain. Pin base and
deployment images by digest in your release pipeline.

```sh
docker run --rm --name dif \
  --read-only --cap-drop=ALL --security-opt=no-new-privileges \
  --mount type=bind,src="$PWD/testdata",dst=/etc/dif/flows,readonly \
  -p 127.0.0.1:9090:9090 \
  dif:local run --file=/etc/dif/flows/timer.json \
  --monitor-address=0.0.0.0:9090 --shutdown-timeout=25s

docker stop --time=40 dif
```

Use `--file` for the example above: `testdata/` contains unrelated test flows,
not a deployable flow group. Production directories should contain only the
flows belonging to that service. The example timer explicitly logs its body;
service result logging itself never includes payloads.

`service.json` is an alternative to command flags; mount it read-only and run
`dif run --config=/etc/dif/service.json`. Flow input paths, directory paths,
keystores and all other relative paths are resolved against the process working
directory (`/data` in the container), not the configuration file or URL.
Use absolute mount paths in production. Remote definitions do not download
their referenced resources.

## Kubernetes

Adapt the image in `kubernetes.yaml`, then apply it to your target namespace.
The manifest provides a singleton timer worker, immutable flow ConfigMap,
probes, restricted security context, resource budgets, and private monitoring
Service. It grants no Kubernetes API credentials. The NetworkPolicy requires
a CNI implementation that enforces policies; adapt its monitoring pod selector.
Business HTTP ports need their own Service and ingress policy when applicable.

Create a new versioned ConfigMap and update the Deployment reference when
changing flows. `--dir` follows Kubernetes' visible projected file symlinks,
skips hidden entries and subdirectories, and takes one snapshot at startup.
It never watches or reloads files. Restart/roll out the service to load changes.

The timer uses `Recreate` to avoid normal rolling-update overlap. A timer, cron
or shared-directory poller is not automatically safe with multiple replicas;
strict singleton ownership needs external coordination even with one replica.
HTTP workers may use rolling updates and multiple replicas when their side
effects and state support them. DIF defaults to process-local memory channels.
Configure `channels` in the service JSON to opt queues and dead letters into
durable storage; topics and flowlinks remain volatile. A storage directory has
one owning process and is not a shared broker. Mount a writable persistent volume
at `channels.directory`, owned by UID 65532, even when the root filesystem is
read-only. Use one replica per storage directory. See the runnable
[reliable-channel example](../testdata/reliable/README.md).

In-memory producer targets need consumers in the service group. Durable queues
can retain messages without a live consumer. Drain is bounded; cycles or blocked
processing can exhaust the budget. Durable unacknowledged deliveries recover on
restart; memory-only work can be lost. Channel depths, blocked producers,
redeliveries and storage failures appear in `/status` and `/metrics`.

The current file source moves/deletes files on message admission, not confirmed
processing success. Use an appropriate durable ingestion mechanism if retries
across crashes are required.

## Secrets and operational endpoints

Keep credentials out of ConfigMaps and images. Mount keystore files from a
Secret and supply passwords with Secret `secretKeyRef` entries using
`DIF_SERVER_IDENTITY_PASSWORD`, `DIF_TRUSTSTORE_PASSWORD`, `DIF_SMTP_PASSWORD`, and `DIF_ENCRYPTION_PASSWORD` (the password of the `ENC(...)` values in flows).
Alternatively set their `_FILE` companions to mounted password-file paths.
Precedence is explicit step option, direct environment variable, then mounted
file (`DIF_ENCRYPTION_PASSWORD` has no step option). One trailing LF/CRLF is removed; files are limited to 64 KiB. An explicitly
empty environment value overrides its file companion. SMTP retains its existing
behavior of falling back to the environment when its password option is empty.
Do not put credentials in command arguments or remote URLs. HTTPS URL query
strings are excluded from loader diagnostics; URLs with userinfo are rejected.

The monitoring port is opt-in and read-only, without authentication. Never
publish it through a public Ingress. `/livez` checks supervisor availability;
it does not probe external dependencies or prove the absence of every deadlock.
`/readyz` checks required flow/source state; it is false during startup and
draining. `/startupz` records successful initial startup. `/status` reports
sanitized lifecycle state and counts. `/metrics` exposes Prometheus text
counters and gauges; flow IDs are the only per-flow labels, never message IDs.

Use a termination grace period longer than `--shutdown-timeout` (the example
uses 40s versus 25s). SIGTERM stops intake and drains work automatically; no
shell-based preStop hook is required. Probes do not stop cron/polling sources;
DIF does that during shutdown. Container termination bounds processors that
ignore cancellation. Exit codes: 0 orderly termination, 1 startup/runtime/drain
failure, 2 invalid arguments/configuration. Individual message failures do not
make an otherwise orderly service shutdown fail.

## Verification and custom processors

Run `go test ./...`, `go vet ./...`, and `go test -race ./...` on a Linux host
with Go 1.24+ and a C compiler. The Linux CLI test starts a real child process
without stdin and verifies clean SIGTERM termination. Build the Docker image,
probe `/readyz`, and use `docker stop --time=40` to verify the actual runtime
image before publishing it. Building a Linux binary on Windows alone does not
verify Linux signal handling or the container runtime.

Custom sources should implement `ReadySourceProcessor` and report readiness
before their first emit. Sources without that optional contract are assumed
initialized when their goroutine starts. A background listener can call
`ReportSourceFailure` before slow cleanup so supervision detects failure
immediately. A source must release resources before returning.

Internal consumers implement `InternalSourceProcessor`; producers identify
their destinations with `LocalProducer`. Asynchronous buffers use
`TrackWork(ctx, true)` when admitting work and release that token only after
the runner accepts it (or it is discarded). The runner tracks execution
separately, so there is no unaccounted interval during a hand-off. Context
cancellation must be honored by custom sources and processors. Construction
and forced shutdown waits are bounded, but Go cannot kill an uncooperative
goroutine; the executable's process exit is the final enforcement boundary.
