---
id: configuration
title: Configuration
sidebar_label: Configuration
sidebar_position: 1
---

# Configuration

`task-interleaving` has **one binary**, `cmd/api`, and it is configured only
through environment variables. There is no config file, no flag and no
secret-manager integration in the repository. REST is unauthenticated
fleet-wide, so there is no token or key to configure.

The service runs on real Postgres and real Kafka only
([ADR 0006](/docs/adr/0006-postgres-and-kafka-are-the-only-runtime)): four
variables are mandatory and a missing one is a boot error that names it, raised
before any connection is opened and before the listener binds.

Every variable below is read in `cmd/api` (the composition root) or in the
`internal/adapters/outbound/postgres` migration helper; no other package reads
the environment. Test-only variables do not exist in this repo (the integration
tests start their own containers with testcontainers). Semantics were verified
by reading the code and by running the binary: the boot errors quoted in
[Troubleshooting](/docs/operations/troubleshooting) are real output.

## `cmd/api`

| Variable | Default | Required? | Meaning | Source |
| --- | --- | --- | --- | --- |
| `HTTP_ADDR` | `:8080` | no | Listen address of the REST server (also serves `/healthz` and `/readyz`). | `cmd/api/main.go` (`run`) |
| `DATABASE_URL` | none | **yes** | Postgres DSN. Missing = boot error `DATABASE_URL is required: this context runs on real Postgres`. The embedded migrations run at boot; the outbox, the `Idempotency-Key` store and every repository live here. | `cmd/api/config.go` (`loadBootConfig`), `cmd/api/adapters.go` (`buildAdapters`) |
| `MIGRATIONS_DATABASE_URL` | value of `DATABASE_URL` | no | Direct (non-pooled) DSN used only for the boot migrations, because golang-migrate's advisory lock does not survive PgBouncer transaction pooling. An empty value falls back to `DATABASE_URL`. | `cmd/api/adapters.go` (`migrationsDatabaseURL`), `internal/adapters/outbound/postgres/migrate.go` |
| `KAFKA_BROKERS` | none | **yes** | Comma-separated broker list (entries are trimmed, blanks dropped). Missing = boot error `KAFKA_BROKERS is required: this context runs on real Kafka`. Used by the consumers, the outbox relay and the `/readyz` metadata check. In the fleet's kind cluster the single broker is reachable from the host at `localhost:9092`. | `cmd/api/config.go` (`brokersFromEnv`, `loadBootConfig`), `cmd/api/relay.go` |
| `OUTBOX_RELAY_INTERVAL` | `1s` | no | Pause between relay passes that did not fill a batch. A Go duration (`500ms`, `2s`) or plain seconds (`2`, `0.5`). Unparsable or non-positive values log a WARN and use the default. | `cmd/api/config.go` (`parseRelayInterval`) |
| `OUTBOX_MAX_ATTEMPTS` | `20` | no | Failures after which the relay stops claiming an outbox row. `0` = unlimited. Negative or unparsable values log a WARN and use `20`, so a typo never removes the cap. | `cmd/api/config.go` (`outboxMaxAttempts`) |
| `FULFILLMENT_CONSUMER_GROUP` | none | **yes** | Kafka consumer group id of the fulfillment-execution consumer (topic `warehouse.fulfillment.events`). The code has no default and no literal; the deployment chooses it. Missing = boot error `FULFILLMENT_CONSUMER_GROUP is required: this context runs on real Kafka`. | `cmd/api/consumers.go` (`planConsumers`) |
| `FULFILLMENT_MODE` | `kafka` | no | The only supported value is `kafka` (also when empty): the consumer always runs. `permissive` is **rejected** at boot (`FULFILLMENT_MODE=permissive is not supported: this context runs on real Kafka`); any other value is a boot error. The variable stays because fleet documents and templates name it. | `cmd/api/consumers.go` (`parseConsumerMode`) |
| `LAYOUT_CONSUMER_GROUP` | none | **yes** | Consumer group id of the facility-layout consumer (topic `warehouse.facility.events`). | `cmd/api/consumers.go` |
| `LAYOUT_MODE` | `kafka` | no | As `FULFILLMENT_MODE`. | `cmd/api/consumers.go` |
| `HINT_EXPIRY_INTERVAL` | `5s` | no | How often the hint-expiry worker (always on) sweeps Active hints past `expires_at`. Go duration. Unlike the relay knobs, an invalid or non-positive value is a **boot error** (`exit 1`) so a typo cannot silently slow expiry down. Blank = default. | `cmd/api/config.go` (`hintExpiryInterval`) |
| `SHUTDOWN_DRAIN_DELAY` | `5s` | no | After SIGINT/SIGTERM, `/readyz` flips to 503 and the process waits this long before closing the listener. `0` disables the wait. Negative or unparsable values log a WARN and use `5s`. Read when the signal arrives, not at boot. | `cmd/api/config.go` (`shutdownDrainDelay`), `cmd/api/main.go` (`serveUntilSignal`) |
| `LOG_LEVEL` | `info` | no | `debug`, `info`, `warn` (or `warning`), `error`, case-insensitive. Any other value silently falls back to `info`. Logs are JSON on stdout. | `cmd/api/config.go` (`newLogger`) |

All problems are reported together, for example (real output of the binary run
with an empty environment):

```text
invalid configuration: DATABASE_URL is required: this context runs on real Postgres
KAFKA_BROKERS is required: this context runs on real Kafka
FULFILLMENT_CONSUMER_GROUP is required: this context runs on real Kafka
LAYOUT_CONSUMER_GROUP is required: this context runs on real Kafka
```

There is no mode matrix any more: with the four mandatory variables set the
consumers, the expiry worker and the outbox relay all start, and `/readyz` says
whether Postgres and Kafka are usable (next section).

## Readiness

`GET /readyz` is 200 only when the shutdown drain gate is open **and** Postgres
answers a ping **and** a Kafka broker answers a metadata request that names no
topic (reachability only). The verdict is cached for 3 s and a
round of checks is bounded by 2 s. A failed dependency is `503`
`application/problem+json` (`dependency-unavailable`, detail `not ready:
kafka` or `not ready: postgres`, never the cause). `/healthz` is liveness only.
Topics are **not** part of readiness: the service never creates topics through
an admin API, the cluster broker auto-creates them on first write (the relay's
writer sets `AllowAutoTopicCreation`; infrastructure declares none), and a
consumed topic that does not exist yet shows as consumer retry logs, never as
`503` ([ADR 0006 Amendment 1](/docs/adr/0006-postgres-and-kafka-are-the-only-runtime)).

## Compiled-in settings (not configurable)

These are constants in the source; changing them needs a code change.

| Setting | Value | Source |
| --- | --- | --- |
| HTTP `ReadHeaderTimeout` | 5 s | `cmd/api/main.go` |
| HTTP graceful shutdown timeout / per-worker drain timeout | 10 s / 10 s | `cmd/api/main.go` (`shutdownTimeout`, `workerDrainTimeout`) |
| Postgres pool size and statement timeout | 10 connections, `statement_timeout = 5s` | `internal/adapters/outbound/postgres/pool.go` |
| Boot retry (migrations, first ping) | 5 attempts, backoff 1 s, 2 s, 4 s, 8 s | `internal/bootretry/bootretry.go` |
| Outbox relay batch size | 100 rows per pass | `internal/adapters/outbound/outbox/relay.go` (`DefaultBatchSize`) |
| Kafka producer | acks=all, hash balancer on the message key, 10 ms batch timeout, no fixed topic, `AllowAutoTopicCreation` (the broker creates a topic on first write) | `internal/adapters/outbound/kafka/writer.go` |
| Consumer retry backoff | 200 ms doubling to a 5 s cap, at most 5 handling attempts, then the dead-letter topic `<topic>.dlq` | `internal/adapters/inbound/kafka/kafka.go`, `deadletter.go` |
| Consumer restart after a reader error | 1 s, until the service stops | `cmd/api/consumers.go` (`consumerRestartDelay`) |
| `/readyz` dependency cache and check timeout | 3 s verdict cache, 2 s per round of checks | `internal/adapters/inbound/http/readiness.go` |
| Consumer start offset | none committed = first offset (a fresh group replays the whole topic) | `internal/adapters/inbound/kafka/kafka.go` (`readerConfig`) |
| Expiry sweep batch | 200 stations per sweep | `internal/application/usecases/expire_hints.go` |
| Request body limit | 64 KiB | `internal/adapters/inbound/http/response.go` |
| `Idempotency-Key` length | 1 to 128 characters | `internal/adapters/inbound/http/idempotency.go` |
| Topics produced | `warehouse.task-interleaving.events`, `warehouse.task-interleaving.analytics` | `internal/adapters/outbound/events/encoder.go` |
| Topics consumed | `warehouse.fulfillment.events`, `warehouse.facility.events` | `internal/adapters/inbound/kafka/fulfillment_consumer.go`, `layout_consumer.go` |
| Consumer names in `processed_events` | `task-interleaving-fulfillment`, `task-interleaving-layout` (not Kafka group ids) | `internal/application/usecases/intake.go` |

## Other binaries

| Binary | Variables | Notes |
| --- | --- | --- |
| `cmd/mcp` | `MCP_ADDR` (default `:8090`), `LOG_LEVEL`, `DATABASE_URL` (required), `MIGRATIONS_DATABASE_URL` | Read-only MCP over the OLTP database; starts no relay, consumer or worker ([ADR 0007](/docs/adr/0007-mcp-server-adoption)). |
| `cmd/ti-projector` | `ANALYTICS_DATABASE_URL` and `KAFKA_BROKERS` (required), `ANALYTICS_CONSUMER_GROUP` (default `task-interleaving-analytics`), `ADMIN_ADDR` (default `:8091`), `LOG_LEVEL` | The only writer of the analytical database; never opens the OLTP one ([ADR 0008](/docs/adr/0008-analytics-read-side)). |
| `cmd/ti-reports` | `ANALYTICS_DATABASE_URL` (required), `HTTP_ADDR` (default `:8092`), `LOG_LEVEL` | Read-only; never dials Kafka. |
| `web/` | runtime `window.__WAREHOUSE_CONFIG__.apiOrigin`, set by the console shell | Calls only `<apiOrigin>/api/task-interleaving`. |

## What is deliberately absent

- **No OpenTelemetry or metrics variables.** The binary reads no `OTEL_*`
  variable and starts no exporter; see [Observability](/docs/operations/observability).
- **No auth variables.** REST is unauthenticated fleet-wide;
  `TestNoAuthMiddlewareReintroduced` fails the build if a bearer or JWT check
  appears under `internal/adapters/inbound`.
- **No MCP auth or write variables.** `cmd/mcp` reads only `MCP_ADDR`, `LOG_LEVEL`, `DATABASE_URL` and `MIGRATIONS_DATABASE_URL`; it has no Kafka variable.
- **No CORS, rate-limit or feature-flag variables.**
- **Chart values.** `charts/task-interleaving` maps these variables onto the pod
  (`database.*`, `kafka.brokers`, `config.*`; see
  [Runbook](/docs/operations/runbook#deployment)). `FULFILLMENT_MODE` and
  `LAYOUT_MODE` have no value: the chart never renders them and refuses them in
  `extraEnv`.
