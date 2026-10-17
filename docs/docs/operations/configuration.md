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
| `DATABASE_URL` | unset | no | Postgres DSN. **Unset = in-memory adapters** (development): no outbox, no relay, no `Idempotency-Key` enforcement, data lost on exit. Set = Postgres adapters, boot migrations, relay possible. | `cmd/api/main.go`, `cmd/api/adapters.go` (`buildAdapters`) |
| `MIGRATIONS_DATABASE_URL` | value of `DATABASE_URL` | no | Direct (non-pooled) DSN used only for the boot migrations, because golang-migrate's advisory lock does not survive PgBouncer transaction pooling. An empty value falls back to `DATABASE_URL`. | `cmd/api/adapters.go` (`migrationsDatabaseURL`), `internal/adapters/outbound/postgres/migrate.go` |
| `KAFKA_BROKERS` | unset | only for the relay and for any `*_MODE=kafka` | Comma-separated broker list (entries are trimmed, blanks dropped). The outbox relay runs only when this **and** `DATABASE_URL` are set. A consumer in `kafka` mode without it is a boot error. In the fleet's kind cluster the single broker is reachable from the host at `localhost:9092`. | `cmd/api/config.go` (`brokersFromEnv`), `cmd/api/relay.go` |
| `OUTBOX_RELAY_INTERVAL` | `1s` | no | Pause between relay passes that did not fill a batch. A Go duration (`500ms`, `2s`) or plain seconds (`2`, `0.5`). Unparsable or non-positive values log a WARN and use the default. | `cmd/api/config.go` (`parseRelayInterval`) |
| `OUTBOX_MAX_ATTEMPTS` | `20` | no | Failures after which the relay stops claiming an outbox row. `0` = unlimited. Negative or unparsable values log a WARN and use `20`, so a typo never removes the cap. | `cmd/api/config.go` (`outboxMaxAttempts`) |
| `FULFILLMENT_MODE` | `permissive` | no | `kafka` starts the fulfillment-execution consumer (topic `warehouse.fulfillment.events`); `permissive` (or empty) starts nothing. Any other value is a boot error. | `cmd/api/consumers.go` (`consumerSpecs`, `parseConsumerMode`) |
| `FULFILLMENT_CONSUMER_GROUP` | unset | **yes** when `FULFILLMENT_MODE=kafka` | Kafka consumer group id of that consumer. The code has no default and no literal; the deployment chooses it. Missing = boot error. | `cmd/api/consumers.go` (`planConsumers`) |
| `LAYOUT_MODE` | `permissive` | no | `kafka` starts the facility-layout consumer (topic `warehouse.facility.events`); otherwise as `FULFILLMENT_MODE`. | `cmd/api/consumers.go` |
| `LAYOUT_CONSUMER_GROUP` | unset | **yes** when `LAYOUT_MODE=kafka` | Consumer group id of the layout consumer. | `cmd/api/consumers.go` |
| `HINT_EXPIRY_INTERVAL` | `5s` | no | How often the hint-expiry worker sweeps Active hints past `expires_at`. Go duration. Unlike the relay knobs, an invalid or non-positive value is a **boot error** (`exit 1`) so a typo cannot silently slow expiry down. Blank = default. | `cmd/api/config.go` (`hintExpiryInterval`) |
| `SHUTDOWN_DRAIN_DELAY` | `5s` | no | After SIGINT/SIGTERM, `/readyz` flips to 503 and the process waits this long before closing the listener. `0` disables the wait. Negative or unparsable values log a WARN and use `5s`. Read when the signal arrives, not at boot. | `cmd/api/config.go` (`shutdownDrainDelay`), `cmd/api/main.go` (`serveUntilSignal`) |
| `LOG_LEVEL` | `info` | no | `debug`, `info`, `warn` (or `warning`), `error`, case-insensitive. Any other value silently falls back to `info`. Logs are JSON on stdout. | `cmd/api/config.go` (`newLogger`) |

The mode matrix that follows from these variables:

| `DATABASE_URL` | `KAFKA_BROKERS` | `*_MODE` | Result |
| --- | --- | --- | --- |
| unset | any | `permissive` | In-memory REST service plus the expiry worker. No relay, no consumers. |
| unset | set | `kafka` | In-memory consumers run, with a WARN: the local copies are lost on restart while committed offsets are kept. Nothing is published (no outbox). |
| set | unset | `permissive` | Postgres service. Events accumulate in `outbox_events` (WARN `outbox relay off: KAFKA_BROKERS is not set`). |
| set | set | `permissive` | Postgres service plus the outbox relay. The copies are whatever the database holds. |
| set | set | `kafka` | Full service: consumers feed the copies, the relay publishes. |
| any | unset | `kafka` | Boot error `a consumer in kafka mode requires KAFKA_BROKERS`. |

## Compiled-in settings (not configurable)

These are constants in the source; changing them needs a code change.

| Setting | Value | Source |
| --- | --- | --- |
| HTTP `ReadHeaderTimeout` | 5 s | `cmd/api/main.go` |
| HTTP graceful shutdown timeout / per-worker drain timeout | 10 s / 10 s | `cmd/api/main.go` (`shutdownTimeout`, `workerDrainTimeout`) |
| Postgres pool size and statement timeout | 10 connections, `statement_timeout = 5s` | `internal/adapters/outbound/postgres/pool.go` |
| Boot retry (migrations, first ping) | 5 attempts, backoff 1 s, 2 s, 4 s, 8 s | `internal/bootretry/bootretry.go` |
| Outbox relay batch size | 100 rows per pass | `internal/adapters/outbound/outbox/relay.go` (`DefaultBatchSize`) |
| Kafka producer | acks=all, hash balancer on the message key, 10 ms batch timeout, no fixed topic | `internal/adapters/outbound/kafka/writer.go` |
| Consumer retry backoff | 200 ms doubling to a 5 s cap, at most 5 handling attempts, then the dead-letter topic `<topic>.dlq` | `internal/adapters/inbound/kafka/kafka.go`, `deadletter.go` |
| Consumer start offset | none committed = first offset (a fresh group replays the whole topic) | `internal/adapters/inbound/kafka/kafka.go` (`readerConfig`) |
| Expiry sweep batch | 200 stations per sweep | `internal/application/usecases/expire_hints.go` |
| Request body limit | 64 KiB | `internal/adapters/inbound/http/response.go` |
| `Idempotency-Key` length | 1 to 128 characters | `internal/adapters/inbound/http/idempotency.go` |
| Topics produced | `warehouse.task-interleaving.events`, `warehouse.task-interleaving.analytics` | `internal/adapters/outbound/events/encoder.go` |
| Topics consumed | `warehouse.fulfillment.events`, `warehouse.facility.events` | `internal/adapters/inbound/kafka/fulfillment_consumer.go`, `layout_consumer.go` |
| Consumer names in `processed_events` | `task-interleaving-fulfillment`, `task-interleaving-layout` (not Kafka group ids) | `internal/application/usecases/intake.go` |

## What is deliberately absent

- **No OpenTelemetry or metrics variables.** The binary reads no `OTEL_*`
  variable and starts no exporter; see [Observability](/docs/operations/observability).
- **No auth variables.** REST is unauthenticated fleet-wide;
  `TestNoAuthMiddlewareReintroduced` fails the build if a bearer or JWT check
  appears under `internal/adapters/inbound`.
- **No MCP variables.** There is no `cmd/mcp`.
- **No CORS, rate-limit or feature-flag variables.**
- **No chart values.** The repository has no `charts/` directory and no
  `Dockerfile` (see [Runbook](/docs/operations/runbook#deployment)), so no
  deployment manifest maps these variables onto a pod.
