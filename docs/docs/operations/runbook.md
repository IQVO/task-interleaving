---
id: runbook
title: Runbook
sidebar_label: Runbook
sidebar_position: 2
---

# Runbook

How to run, deploy and operate `task-interleaving` as the code on `develop`
defines it. Settings are in [Configuration](/docs/operations/configuration),
symptoms in [Troubleshooting](/docs/operations/troubleshooting), signals in
[Observability](/docs/operations/observability).

## Deployment

**This repository ships no deployment artefact.** `git ls-files` on
`develop` shows no `Dockerfile`, no `charts/` directory, no `docker-compose.yml`
and no Kubernetes or Terraform manifest. Consequently:

- There is no Helm chart name, no Deployment, no Service, no HPA and no
  probe configuration in this repo to describe. The probe endpoints below are
  what such a manifest would point at.
- `.github/workflows/ci.yml` already refers to the missing files:
  `docker-publish` builds with `context: .` and pushes
  `ghcr.io/iqvo/task-interleaving`, `release` packages `charts/task-interleaving`,
  and `chart-tests` runs `charts/task-interleaving/tests/*`. The first two run
  only on a push to `main` and would fail without a `Dockerfile` and a chart;
  `chart-tests` finds no test and exits 0 with a warning.
- `warehouse-infra` (`origin/develop`, 2026-10-17) has no reference to
  `task-interleaving` in its Terraform, so no Kong route, ArgoCD application or
  dashboard exists for it, and no values file sets the variables of
  [Configuration](/docs/operations/configuration). The OpenAPI server list still
  names `http://localhost:8080` and Kong's `/api/task-interleaving` as the
  intended edge.

What you can run today is the binary: `go build -o task-interleaving ./cmd/api`
(`make build` compiles it with the rest of the module). It listens on
`HTTP_ADDR` (default `:8080`) and logs JSON to stdout.

### Binaries and ports

| Binary | Role | Port | Probes |
| --- | --- | --- | --- |
| `cmd/api` | REST API, hint-expiry worker, optional Kafka consumers, optional outbox relay (all in one process) | `HTTP_ADDR`, default `:8080` | `GET /healthz`, `GET /readyz` |

### Probes

| Endpoint | Answer | Meaning |
| --- | --- | --- |
| `GET /healthz` | `200 {"status":"ok"}` always | Liveness. It checks nothing and is never flipped by shutdown. |
| `GET /readyz` | `200 {"status":"ready"}`, then `503 {"status":"not_ready"}` once shutdown starts | Readiness. It **does not check Postgres or Kafka**. |

Both are plain `application/json` and are described in `apis/openapi.yaml`
(`health` tag). What readiness waits for is the **boot order**, because the
listener is opened last:

1. Configuration errors: `HINT_EXPIRY_INTERVAL`, then the consumer modes,
   groups and `KAFKA_BROKERS`. A bad value exits with status 1 before any
   database is dialled.
2. With `DATABASE_URL`: the embedded migrations run (retried), then the pool
   opens and is pinged (retried). Five attempts with 1 s, 2 s, 4 s and 8 s of
   backoff; on exhaustion the last error is returned and the process exits 1.
3. The workers start: consumers (if any), the expiry worker, the outbox relay
   (if any). Kafka connections are lazy, so an unreachable broker never delays
   boot.
4. The HTTP server starts listening. From here `/readyz` answers 200.

So a pod answering `/readyz` has finished its migrations and reached Postgres
once, and it keeps saying ready if Postgres or Kafka later fail. Watch the logs
and the SQL probes in [Observability](/docs/operations/observability#suggested-checks-and-alerts)
instead.

### Shutdown order

On SIGINT or SIGTERM (`cmd/api/main.go`, `serveUntilSignal`):

1. `/readyz` flips to 503.
2. The process waits `SHUTDOWN_DRAIN_DELAY` (default 5 s, log line
   `shutdown: readiness flipped to not-ready; waiting for traffic to drain`).
3. The HTTP server drains for at most 10 s.
4. Workers stop in this order, each awaited up to 10 s: **consumers first**
   (they enqueue outbox rows while draining), then the **expiry worker** (it
   enqueues rows too), then the **outbox relay** last, so nothing committed
   before it is stranded.
5. The Postgres pool closes last.

A worker that does not stop in time logs `worker did not stop before the
shutdown drain deadline`. A message whose handling was interrupted by shutdown
keeps its offset uncommitted and is redelivered on restart; it is never
dead-lettered for that reason.

## Modes

The same binary runs in two storage modes (details and the full matrix are in
[Configuration](/docs/operations/configuration)):

| Mode | Trigger | Behaviour |
| --- | --- | --- |
| In-memory | `DATABASE_URL` unset | All state in process memory, lost on exit. `Idempotency-Key` is **not** enforced on `POST /recommendations`. No outbox, no relay. Consumers may still run (with a WARN). |
| Postgres | `DATABASE_URL` set | Migrations at boot, `Idempotency-Key` required on `POST /recommendations`, transactional outbox, relay when `KAFKA_BROKERS` is also set. |

## Migrations

- **What**: one migration pair, `migrations/0001_interleaving_schema.up.sql` and
  `.down.sql`, embedded in the binary (`migrations/migrations.go`) and applied
  with golang-migrate. It creates `interleaving_policies`, `station_trips`,
  `hints`, `candidates`, `task_pool`, `zones`, `locations`, `processed_events`,
  `outbox_events` and `idempotency_keys` (golang-migrate adds
  `schema_migrations`). A run against an empty database recorded
  `schema_migrations` version 1, `dirty = f`.
- **When**: at **every boot** in Postgres mode, before the pool opens
  (`postgres.RunMigrations`, `m.Up()`; "no change" is success). There is no
  separate migration job and the binary never runs the `.down.sql` file.
- **Against which URL**: `MIGRATIONS_DATABASE_URL` if set, otherwise
  `DATABASE_URL`. Point it at a direct (session) connection when the service
  URL goes through PgBouncer in transaction mode.
- **Failure**: the migration is retried 5 times (about 15 s of backoff) and the
  process then exits 1 with `run migrations (after 5 attempts): ...`. A migration that
  fails halfway can leave `schema_migrations.dirty = true`, which golang-migrate
  refuses to run over until an operator fixes the schema and forces the version.
- **Concurrency**: two replicas booting together serialise on golang-migrate's
  advisory lock (the reason the direct URL matters).

## Kafka

One Kafka broker serves the whole fleet. Locally it is reachable at
`localhost:9092` through the kind cluster's external access; the docker-compose
Kafka is retired. Every message is a CloudEvents 1.0 structured-mode envelope
(`internal/adapters/kafka/cloudevents`).

### Consumed

| Topic | Consumer (log name) | Group id | Types handled | Dead-letter topic |
| --- | --- | --- | --- | --- |
| `warehouse.fulfillment.events` | `fulfillment consumer`, started by `FULFILLMENT_MODE=kafka` | value of `FULFILLMENT_CONSUMER_GROUP` | `TaskAvailable`, `TaskClaimed`, `TaskCompleted` of fulfillment-execution | `warehouse.fulfillment.events.dlq` |
| `warehouse.facility.events` | `layout consumer`, started by `LAYOUT_MODE=kafka` | value of `LAYOUT_CONSUMER_GROUP` | `ZoneRegistered`, `LocationSlotRegistered`, `LocationSlotDecommissioned`, `LocationGeometryUpdated` of facility-layout | `warehouse.facility.events.dlq` |

The repository fixes **no group id value**: the deployment must pick a stable
one per consumer. Every other CloudEvents type on those topics is ignored and
its offset committed.

Per message (`internal/adapters/inbound/kafka`):

1. Decode as CloudEvents 1.0; a message that is not one is logged at WARN and
   committed past.
2. Dispatch on the full `type`. The claim of the CloudEvents `id` in
   `processed_events` and the effect commit in one database transaction; a
   duplicate id is a no-op with outcome `duplicate`.
3. Commit the Kafka offset **after** the transaction (at least once).
4. A transient failure (database error, version conflict) retries the same
   message with backoff 200 ms, 400 ms, ... up to 5 s, at most 5 attempts,
   then publishes it to `<topic>.dlq` and moves on.
5. A payload of a handled type that can never be applied (malformed `data`, a
   missing required field, a non-finite coordinate) goes to the dead-letter
   topic at once, without retry.

The dead-letter message keeps the original key, value and headers and adds
`x-dlq-source-topic`, `x-dlq-source-partition`, `x-dlq-source-offset`,
`x-dlq-error` and `x-dlq-failed-at` (RFC 3339 UTC). The DLQ writer allows topic
auto-creation; publishing to it is retried until it succeeds, so a message is
never dropped silently.

### Produced

| Topic | Types | Key | Consumers |
| --- | --- | --- | --- |
| `warehouse.task-interleaving.events` | `InterleavingHintIssued` | station id | none built (see [Integration](/docs/ecosystem/integration)) |
| `warehouse.task-interleaving.analytics` | `InterleavingHintIssued`, `InterleavingHintConsumed`, `InterleavingHintExpired`, `InterleavingPolicyDeclared` | station id (hint events), site id (policy event) | none built |

The full type is `com.warehouse.wes.task-interleaving.<entity>.<EventName>`
with entity `stationtrip` (hint events) or `policy`. `InterleavingHintIssued`
is written to both topics with the **same CloudEvents id**; the other three go
to the analytics topic only. The relay's writer
(`internal/adapters/outbound/kafka/writer.go`) does not set
`AllowAutoTopicCreation`, and kafka-go only asks the broker to create a topic
when that flag is set, so create both topics before enabling the relay (or
rely on broker-side auto-creation, which is not configured in this repo).

### Outbox relay

Enabled when `DATABASE_URL` and `KAFKA_BROKERS` are set. Events are written to
`outbox_events` in the same transaction as the aggregate; the relay
(`internal/adapters/outbound/outbox`) drains them:

- Each pass claims up to 100 unpublished rows in `id` order with
  `FOR UPDATE SKIP LOCKED`, sends them **one at a time** and marks each
  `published_at`. A full batch is followed at once by another pass, otherwise
  it sleeps `OUTBOX_RELAY_INTERVAL` (1 s).
- The first send failure ends the pass: the error is stored in `last_error`,
  `attempts` is incremented and the rows already sent are committed. Order per
  key is preserved while the failing row is retried.
- With `OUTBOX_MAX_ATTEMPTS` > 0 (default 20) a row whose `attempts` reached
  the cap is no longer claimed; later rows overtake it. The failure that
  reaches the cap is logged once at ERROR: `outbox relay: row reached the
  attempt cap and is excluded from future claims; re-arm it with attempts = 0
  after fixing the cause`, with `outbox_id`, `topic`, `event_type`, `key`,
  `attempts`, `max_attempts`, `error`.
- Delivery is at least once. The CloudEvents `id` is minted when the event is
  enqueued and stored in `outbox_events.event_id`, so a retry republishes the
  same id. Rows have a unique `(event_id, topic)`.
- Without `KAFKA_BROKERS` the relay is off and rows accumulate (nothing is
  lost); restart with brokers to drain them.

### Hint-expiry worker

Runs in both storage modes. It sweeps at once on start and then every
`HINT_EXPIRY_INTERVAL` (5 s): up to 200 stations whose Active hint is at or past
`expires_at` are expired, each in its own transaction, raising
`InterleavingHintExpired` (analytics topic). A station whose write loses a race
is skipped until the next sweep; other failures are joined and logged as
`hint expiry sweep failed`. Hints that expire only become visible as `Expired`
after the next sweep, so the observable lag is at most the interval plus the
sweep time.

## Scaling and data growth

- **Replicas**: the code is written to be safe with several replicas.
  Aggregate writes are guarded by a version check (a lost race is `409
  concurrent-modification` over REST and a retry in a consumer); the relay's
  `SKIP LOCKED` claim prevents two relays sending the same row; consumers in
  one group split the partitions; the idempotency store keeps the first writer
  of a key; every replica runs the expiry worker and the version guard makes the
  duplicate sweeps harmless. No test in the repo runs two replicas against each
  other, so treat this as design intent backed by the unit and integration
  suites, not as a load-tested claim.
- **Connections**: each process opens at most 10 Postgres connections with a 5 s
  statement timeout. Size `max_connections` (or the pooler) as 10 per replica.
- **No autoscaling configuration exists** (no chart, no HPA). The hot path is
  `POST /recommendations`, a read-only computation over the site's open tasks.
- **Nothing deletes rows.** No code issues a `DELETE`: `outbox_events` keeps
  its published rows, `idempotency_keys` and `processed_events` grow with
  traffic, and `hints` is the permanent hint history. `idempotency_keys` has an
  index on `created_at` for a future cleanup job, but there is none. Plan table
  maintenance outside the service.

## Routine procedures

**Inspect the outbox.**

```sql
SELECT id, topic, event_type, attempts, last_error, created_at
FROM outbox_events WHERE published_at IS NULL ORDER BY id;
```

**Re-arm a row that hit the attempt cap** (after fixing the cause, e.g. a missing
topic). The ERROR message above names this exact statement:

```sql
UPDATE outbox_events SET attempts = 0 WHERE id = <outbox_id>;
```

**Re-publish an event.** To send an already published row again, clear its
publication mark (`UPDATE outbox_events SET published_at = NULL, attempts = 0
WHERE id = <id>`). The relay re-sends it with the same CloudEvents id, so a
downstream consumer that dedupes on `id` treats it as a duplicate by design;
use this only to recover a loss on the Kafka side. Do not insert rows by hand:
`value` is the pre-encoded CloudEvents JSON.

**Rebuild the local copies** (task pool, locations, zones). A consumer group
with no committed offset starts at the first offset, so pointing
`*_CONSUMER_GROUP` at a new group id replays the topic. The replay is
re-applied only if the events are not already claimed: `processed_events` holds
`(consumer, event_id)` pairs for `task-interleaving-fulfillment` and
`task-interleaving-layout`, and a claimed id is skipped as `duplicate`. To force
a re-application delete that consumer's rows first. This sequence follows from
the code and is not exercised by a test; do it on a copy first.

**Replay a dead-lettered message.** There is no replay tool. Read the message
from `<topic>.dlq`, fix the cause (usually a producer contract problem; see
`x-dlq-error`) and re-publish the original value to the source topic. The claim
of the id was rolled back with the failed transaction, so the message is applied
when it is delivered again.

**Rotate or change a consumer group.** Change `*_CONSUMER_GROUP` and restart;
the new group starts at the first offset (see above), the old group's offsets
are left in Kafka.

**Change a policy.** `PUT /sites/{siteId}/policy` with `If-Match: "<version>"`
(from the `ETag` of the last read). The first declaration sends no `If-Match`.
The declaration enqueues `InterleavingPolicyDeclared` (analytics). A stale
`If-Match` is `412 version-mismatch`.

There is no `*-reports` binary and no `/reports/...` route in this service, so
no analytics endpoint needs documenting beyond `apis/openapi.yaml`.
