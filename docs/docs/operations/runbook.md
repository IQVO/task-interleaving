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

**This repository ships a `Dockerfile`, a `.dockerignore` and the Helm chart
`charts/task-interleaving`.** The chart deploys what
[ADR 0006](/docs/adr/0006-postgres-and-kafka-are-the-only-runtime) requires:
the real Postgres and Kafka runtime, with no in-memory mode.

- **Image.** `docker build -t task-interleaving:local .` runs a digest-pinned
  multi-stage build of every `cmd/*` binary (today `cmd/api` only) onto an
  `alpine` runtime that runs as uid 1000 and writes nothing to disk, so the root
  filesystem can be read-only. Run without configuration it exits 1 at boot with
  the error that names `DATABASE_URL`, `KAFKA_BROKERS` and both consumer groups.
- **Chart.** Required values (`helm template` fails with a message naming the
  value otherwise): `database.url` (the chart creates a Secret) or
  `database.existingSecret` (key `DATABASE_URL`, optional key
  `MIGRATIONS_DATABASE_URL` for a direct non-pooled migration connection),
  `kafka.brokers`, `config.fulfillmentConsumerGroup` and
  `config.layoutConsumerGroup` (stable ids, defaulted in `values.yaml`).
  Optional: `config.hintExpiryInterval`, `config.outboxMaxAttempts`,
  `config.outboxRelayInterval`, `config.shutdownDrainDelay`, `config.httpAddr`,
  `config.logLevel`. The `*_MODE` variables are never rendered and are refused
  through `extraEnv`. Probes: startup and liveness on `/healthz`, readiness on
  `/readyz` (5 s delay, 10 s period, 5 s timeout, 3 failures).
  `terminationGracePeriodSeconds` is 60 and the chart refuses less than 55: the
  binary stops its workers one after the other, each bounded by 10 s
  (`SHUTDOWN_DRAIN_DELAY` 5 s + HTTP 10 s + two consumers + the expiry worker + the
  relay).
- **Gateway.** The HPA, the `HTTPRoute` (prefix `/api/task-interleaving`,
  stripped before the service) and the Kong plugins (`cors` allowing
  `Idempotency-Key`, `prometheus`, `opentelemetry`; never an auth plugin) are
  off by default and switched on by values.
- **Tests.** `charts/task-interleaving/tests/render_test.sh` (renders with the
  mandatory values, asserts the `envFrom` mount, the probes, the hardening, no
  `*_MODE`, the refusals, and that every variable `cmd/api` reads is rendered),
  `test_service_selectors.py` and `test_kong_config.py` run in the `chart-tests`
  CI job.
- `.github/workflows/ci.yml`: `docker-publish` builds this `Dockerfile` and
  pushes `ghcr.io/iqvo/task-interleaving`, `release` packages
  `charts/task-interleaving`. Both run only on a push to `main`.
- `warehouse-infra` (`origin/develop`, 2026-10-17) has no reference to
  `task-interleaving` in its Terraform, so no Kong route, ArgoCD application or
  dashboard exists for it, and no values file of that repository sets the chart
  values yet. The `docker-compose.yml` at the root only serves `make run-local`
  (a throwaway Postgres, Kafka and a job that pre-creates the four topics locally; see the
  README note that it is not verified end to end).

What you can run today is the binary: `go build -o task-interleaving ./cmd/api`
(`make build` compiles it with the rest of the module). It listens on
`HTTP_ADDR` (default `:8080`) and logs JSON to stdout.

### Binaries and ports

| Binary | Role | Port | Probes |
| --- | --- | --- | --- |
| `cmd/api` | REST API, hint-expiry worker, the two Kafka consumers and the outbox relay (all in one process, all always on) | `HTTP_ADDR`, default `:8080` | `GET /healthz`, `GET /readyz` |

### Probes

| Endpoint | Answer | Meaning |
| --- | --- | --- |
| `GET /healthz` | `200 {"status":"ok"}` always | Liveness. It checks nothing and is never flipped by shutdown. |
| `GET /readyz` | `200 {"status":"ready"}` while the drain gate is open, Postgres answers a ping and a Kafka broker answers a metadata request that names no topic (topics are never checked); `503 {"status":"not_ready"}` once shutdown starts; `503` `application/problem+json` `dependency-unavailable` (`not ready: kafka`, `not ready: postgres`, or `postgres, kafka`) while a dependency is down | Readiness. The dependency verdict is cached for 3 s and bounded to 2 s, so probes cannot hammer Postgres or Kafka. The body names the dependency, never the cause (the cause is the WARN log `readiness: dependency is not ready`). |

Both are described in `apis/openapi.yaml` (`health` tag). The boot order is:

1. Configuration errors, all reported at once: `DATABASE_URL`, `KAFKA_BROKERS`,
   both `*_CONSUMER_GROUP` variables, the consumer modes and
   `HINT_EXPIRY_INTERVAL`. A bad or missing value exits with status 1 before
   any database is dialled and before the listener binds.
2. The embedded migrations run (retried), then the pool opens and is pinged
   (retried). Five attempts with 1 s, 2 s, 4 s and 8 s of backoff; on
   exhaustion the last error is returned and the process exits 1. The boot log
   then names the topics the service consumes and produces.
3. The workers start: the two consumers, the expiry worker and the outbox
   relay. Kafka connections are lazy, so an unreachable broker never delays or
   fails boot: the consumers log `kafka reader error; retrying` and are run
   again after a reader failure, the relay retries its rows, and `/readyz`
   reports `not ready: kafka` only while the broker is unreachable (a missing
   topic never makes it unready).
4. The HTTP server starts listening.

So a pod that answers `/readyz` 200 has Postgres and a Kafka broker that
answers a metadata request; nothing is said about topics. **Topics play no part
in readiness**: the broker auto-creates the service's own topics on first write
(the relay's writer sets `AllowAutoTopicCreation`; fleet convention, infrastructure
declares none; [ADR 0006 Amendment 1](/docs/adr/0006-postgres-and-kafka-are-the-only-runtime)),
so a pod that waited for them could never become ready and so never publish, and a
consumed topic exists only once its producer ran. A missing consumed topic shows
up as consumer retry logs (`kafka reader error; retrying`, with the consumer and
the topic) and the consumer starts reading by itself once the topic exists. The
service never creates topics through an admin API (the end-to-end test boots the
real `run()` on a broker with topic auto-creation off and asserts `/readyz` is
200 before any topic exists). Watch the logs and the SQL probes in
[Observability](/docs/operations/observability#suggested-checks-and-alerts) for
everything readiness cannot see.

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

## Runtime

There is one runtime: real Postgres and real Kafka
([ADR 0006](/docs/adr/0006-postgres-and-kafka-are-the-only-runtime)). There is
no in-memory mode, no consumer that is off by default and no optional relay.
`Idempotency-Key` is required on `POST /recommendations` in every deployment,
the outbox and the relay are always on and `DATABASE_URL`, `KAFKA_BROKERS` and
both consumer groups are mandatory (see
[Configuration](/docs/operations/configuration)). The in-memory adapters exist
only as test doubles.

## Migrations

- **What**: one migration pair, `migrations/0001_interleaving_schema.up.sql` and
  `.down.sql`, embedded in the binary (`migrations/migrations.go`) and applied
  with golang-migrate. It creates `interleaving_policies`, `station_trips`,
  `hints`, `candidates`, `task_pool`, `zones`, `locations`, `processed_events`,
  `outbox_events` and `idempotency_keys` (golang-migrate adds
  `schema_migrations`). A run against an empty database recorded
  `schema_migrations` version 1, `dirty = f`.
- **When**: at **every boot**, before the pool opens
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
`localhost:9092` through the kind cluster's external access; the fleet's
docker-compose Kafka is retired (this repo's `docker-compose.yml` is only for
`make run-local`). The cluster broker auto-creates every business topic on first
write and infrastructure declares none; the service never creates a topic through
an admin API, and a missing topic never affects `/readyz` (a consumed topic that
does not exist yet shows as consumer retry logs). Every message is a CloudEvents
1.0 structured-mode envelope (`internal/adapters/kafka/cloudevents`).

### Consumed

| Topic | Consumer (log name) | Group id | Types handled | Dead-letter topic |
| --- | --- | --- | --- | --- |
| `warehouse.fulfillment.events` | `fulfillment consumer`, always on (`FULFILLMENT_MODE` defaults to `kafka`) | value of `FULFILLMENT_CONSUMER_GROUP` | `TaskAvailable`, `TaskClaimed`, `TaskCompleted` of fulfillment-execution | `warehouse.fulfillment.events.dlq` |
| `warehouse.facility.events` | `layout consumer`, always on (`LAYOUT_MODE` defaults to `kafka`) | value of `LAYOUT_CONSUMER_GROUP` | `ZoneRegistered`, `LocationSlotRegistered`, `LocationSlotDecommissioned`, `LocationGeometryUpdated` of facility-layout | `warehouse.facility.events.dlq` |

The repository fixes **no group id value**: the deployment must pick a stable
one per consumer (both are mandatory). Every other CloudEvents type on those
topics is ignored and its offset committed. A consumer whose reader fails (an
unreachable broker) is run again after 1 s, so it never stays dead while the
service is up.

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
| `warehouse.task-interleaving.analytics` | `InterleavingHintIssued`, `InterleavingHintConsumed`, `InterleavingHintExpired`, `InterleavingPolicyDeclared` | station id (hint events), site id (policy event) | `ti-projector` only (ADR 0008) |

The full type is `com.warehouse.wes.task-interleaving.<entity>.<EventName>`
with entity `stationtrip` (hint events) or `policy`. `InterleavingHintIssued`
is written to both topics with the **same CloudEvents id**; the other three go
to the analytics topic only. The relay's writer
(`internal/adapters/outbound/kafka/writer.go`) sets `AllowAutoTopicCreation`
(`TestNewWriterAllowsAutoTopicCreation`), which is how the fleet creates topics:
the cluster broker creates each business topic on the first write
(`num.partitions=8`) and infrastructure declares none. Both topics therefore need
not exist before the service starts and `/readyz` does not wait for them. The
auto-creation takes effect on a real cluster only: the test broker has it off and
the unit test covers the flag (ADR 0006 Amendment 1).

### Outbox relay

Always on. Events are written to
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
- A broker outage longer than `OUTBOX_MAX_ATTEMPTS` passes (default 20 at 1 s,
  about 20 s; observed) parks the head row at the cap: re-arm it as described
  under Routine procedures once Kafka is back. The service refuses to boot
  without `KAFKA_BROKERS`; it does not run without a relay.

### Hint-expiry worker

Always on. It sweeps at once on start and then every
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

**Analytics and reports.** `cmd/ti-projector` consumes
`warehouse.task-interleaving.analytics` into the analytical database
(`ANALYTICS_DATABASE_URL`, separate from `DATABASE_URL`) and `cmd/ti-reports`
serves `GET /reports/hint-adoption` and `GET /reports/freshness` from it
(in-cluster, port 8092). Enable both with `analytics.enabled=true` plus
`analytics.url` or `analytics.existingSecret`. The projector reuses
`kafka.brokers`, uses the stable group `task-interleaving-analytics` and exits
non-zero if its consumer stops, so a stuck projector restarts instead of
answering `/readyz` 200 ([ADR 0008](/docs/adr/0008-analytics-read-side)).

**MCP.** `mcp.enabled=true` renders the read-only `cmd/mcp` Deployment and
Service (port 8090); `mcp.route.enabled=true` publishes it through Kong at
`/api/mcp/task-interleaving`.

**Web remote.** `frontend.enabled=true` renders the nginx Deployment and a
ClusterIP Service for `task_interleaving_mfe`; the Nginx web gateway in
`warehouse-infra` owns `/mfes/task-interleaving/`. Kong's CORS plugin allows
`If-Match`, which the policy edit sends.
