# Logical channels

Run `go run ./cmd/dif` from the repository root. Load and start these flows
inside the same CLI process. Output is written to each flow's log in `logs`.

## Competing queue consumers

```text
load examples/channels/queue-worker-a.json examples/channels/queue-worker-b.json examples/channels/queue-producer.json
start queue-worker-a
start queue-worker-b
start queue-producer
request queue-producer
request queue-producer
```

Each request is enqueued on `orders.received` and goes to one worker. There
is no round-robin or fairness guarantee. Stop both workers and send again:
the request still completes, and a worker receives it when restarted.

## Topic fan-out

```text
load examples/channels/topic-audit.json examples/channels/topic-notifications.json examples/channels/topic-producer.json
start topic-audit
start topic-notifications
start topic-producer
request topic-producer
```

Both subscribers receive a copy. Stop one and publish again: only the running
subscriber receives it. Restarting the other does not replay old messages.
Pausing a subscriber keeps its subscription and buffers messages until resume.
The queue and topic named `orders.received` are independent channels.

## Queued wire tap

```text
load examples/channels/tap-consumer.json examples/channels/tap-producer.json
start tap-consumer
start tap-producer
request tap-producer
```

The main path replies immediately after enqueueing. The audit consumer delays
each message for a second. Stopping that consumer retains queued audit messages
until it restarts. If the queue fills, the detached tap logs the enqueue failure
and the main path continues; that audit copy is lost.

All buffers are in memory and disappear when DIF exits. Enqueue success does
not acknowledge consumer processing. Each buffer holds up to 10,000 waiting
messages, in addition to messages already handed off to its source or flow.
