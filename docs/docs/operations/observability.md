---
id: observability
title: Observability
sidebar_label: Observability
sidebar_position: 3
---

# Observability

Short version: **today the service emits structured logs and nothing else.**
There is no metrics endpoint, no OpenTelemetry instrument, no span and no
exporter in `cmd/api` or anywhere under `internal/`. This page says what exists,
proves the absence, and gives the signals an operator can build on.

## Metrics: none

| Question | Answer | Evidence |
| --- | --- | --- |
| OTel metric instruments registered | **0** | no `Meter`, `Int64Counter`, `Float64Histogram` or similar call exists in non-test Go code; `go.opentelemetry.io/otel/metric` is only an indirect dependency in `go.mod` |
| `/metrics` (Prometheus) endpoint | none | the router in `internal/adapters/inbound/http/server.go` registers exactly `/healthz`, `/readyz` and the six API routes |
| OTLP exporter or `OTEL_*` variables | none | `cmd/api` reads only the variables in [Configuration](/docs/operations/configuration) |
| Runtime or HTTP server metrics (otelchi, otelhttp, runtime) | none wired | `otelhttp` appears in `go.mod` as an indirect dependency only; no middleware uses it |
| Grafana dashboard for this context | none | `warehouse-infra` `origin/develop` has no reference to `task-interleaving` |

Nothing in the audit sense (instrument name, type, unit, attributes) can be
listed because there is no instrument. If metrics are added later, this table
is the place to list them.

## Traces: none

No tracer provider and no propagator are installed by the binary, and no span is
created. The only OpenTelemetry code is in
`internal/adapters/outbound/postgres/outbox_repository.go`: when an outbox row is
inserted it injects the W3C trace context of the request context into the row's
`headers` (`traceparent`, and `tracestate` / `baggage` when present) so a relay
copy carries it onto the Kafka message. With no provider installed the context
holds no span, so **no trace header is ever written today**; an integration test
installs a propagator itself to prove the mechanism
(`outbox_integration_test.go`). Kafka consumers do not extract a trace context.

## Logs

Logs are JSON (`log/slog`) on **stdout**, one object per line with `time`,
`level`, `msg` and the fields below. The level comes from `LOG_LEVEL`
(default `info`). There is no access log: the HTTP router has no request-logging
middleware, so a successful request leaves no line. There are no correlation or
trace-id fields.

| Where | Level | `msg` (abridged) | Fields |
| --- | --- | --- | --- |
| Boot | INFO | `postgres adapters configured`, `kafka topics (owned by infrastructure; the service never creates them)` | `brokers`, `consumes`, `produces` (the last two are the topic lists) |
| Boot | INFO | `http server listening` | `addr` |
| Boot | INFO | `consumer running` | `consumer` (`fulfillment consumer` or `layout consumer`), `topic`, `group_id`, `brokers` |
| Boot | INFO | `outbox relay running` | `interval`, `max_attempts`, `brokers` |
| Boot | INFO | `hint expiry worker running` | `interval` |
| Boot retry | WARN / INFO | `retrying` / `succeeded after retry` | `op` (`run migrations`, `ping postgres`), `attempt`, `in`, `err` |
| Boot | WARN | `invalid OUTBOX_RELAY_INTERVAL; using the default`, `invalid OUTBOX_MAX_ATTEMPTS; using the default` | `value`, `default` |
| Boot failure | ERROR | `service exited with error` (process exits 1) | `error` |
| Shutdown | INFO / WARN | `shutdown: readiness flipped to not-ready; waiting for traffic to drain` / `ignoring invalid SHUTDOWN_DRAIN_DELAY` / `worker did not stop before the shutdown drain deadline` | `drain_delay` / `value`, `default` / `worker` |
| Consumer | INFO | `event processed` | `type`, `event_id`, `subject`, `outcome` (`applied`, `duplicate`, `stale`, `ignored`) |
| Consumer | WARN | `skipping a message that is not a valid CloudEvent` | `consumer`, `topic`, `error` |
| Consumer | WARN | `dead-lettering an event that can never be applied` / `skipping an event that can never be applied` | `consumer`, `type`, `event_id`, `error` |
| Consumer | ERROR | `<consumer> handling failed; retrying the same message` | `error`, `partition`, `offset`, `attempt`, `max_attempts`, `retry_in` |
| Consumer | ERROR | `<consumer> handling failed after the maximum attempts; dead-lettering the message` and `<consumer> message can never be applied; dead-lettering it without retry` | `error`, `partition`, `offset` (and `attempts`) |
| Consumer | ERROR | `kafka reader error; retrying` (kafka-go retrying a group join, a missing topic or an unreachable broker; without this logger it would be silent) | `consumer`, `topic`, `detail` |
| Consumer | ERROR | `<consumer> offset commit failed; retrying the same message`, `<consumer> dead-letter publish failed; retrying the same message`, `consumer stopped; restarting` (the reader failed; the consumer runs again after 1 s), `consumer close failed` | `error`, `partition`, `offset`, `attempt`, `retry_in` / `consumer`, `restart_in` |
| Readiness | WARN | `readiness: dependency is not ready` (once per round of checks, at most every 3 s) | `dependency` (`postgres` or `kafka`), `error` (the cause) |
| Relay | ERROR | `outbox relay pass failed` | `error` (text starts `outbox row <id> (<type>):`) |
| Relay | ERROR | `outbox relay: row reached the attempt cap and is excluded from future claims; ...` | `outbox_id`, `topic`, `event_type`, `key`, `attempts`, `max_attempts`, `error` |
| Relay | ERROR | `outbox relay stopped`, `kafka relay sink close failed` | `error` |
| Expiry worker | INFO | `hints expired` | `expired` |
| Expiry worker | ERROR | `hint expiry sweep failed`, `hint expiry worker stopped` | `expired`, `error` |
| HTTP | ERROR | `request failed` (an unmapped error, answered as 500 `internal-error`), `handler panicked` | `error` or `panic`, `method`, `path` |
| HTTP | WARN | `idempotency: answer not stored` | `error` |

`<consumer>` is the consumer's name, `fulfillment consumer` or `layout consumer`
(`internal/adapters/inbound/kafka`).

REST errors are `application/problem+json` documents whose `type` is
`https://errors.task-interleaving.warehouse-systems.dev/<slug>`; the slug and
status table is in [Troubleshooting](/docs/operations/troubleshooting#rest-problem-types).
The detail of an unmapped error is the fixed text `an unexpected error occurred`;
the real error is only in the `request failed` log line.

## Probes

`GET /healthz` (liveness, always 200) and `GET /readyz` (200 only while Postgres
answers and every consumed and produced topic has a Kafka leader; 503 once
shutdown starts or a dependency is down) are described in the
[Runbook](/docs/operations/runbook#probes). The cause of a failed dependency is
only in the WARN log line above; the response body names the dependency.

## Suggested checks and alerts

These are suggestions built from the data that exists, not alert rules that
ship with the repo. Without metrics they are log searches and SQL probes against
the service database.

| Signal | How to read it | Suggested alert |
| --- | --- | --- |
| Outbox backlog | `SELECT count(*), min(created_at) FROM outbox_events WHERE published_at IS NULL` | oldest unpublished row older than a few minutes |
| Readiness | `GET /readyz` 503 with `dependency-unavailable`; WARN `readiness: dependency is not ready` | not ready for more than a minute |
| Outbox rows stuck at the cap | `SELECT id, event_type, last_error FROM outbox_events WHERE published_at IS NULL AND attempts >= 20` (use your `OUTBOX_MAX_ATTEMPTS`) | any row; also the ERROR line `row reached the attempt cap` |
| Relay failing | log `outbox relay pass failed` | repeated for more than a minute |
| Consumer dead letters | logs `dead-lettering` at ERROR, and the depth of `warehouse.fulfillment.events.dlq` / `warehouse.facility.events.dlq` | any message |
| Consumer stuck on one message | repeated `handling failed; retrying the same message` with the same `partition` and `offset` | more than the 5 attempts' worth, or consumer-group lag on the source topic |
| Stale hints | `SELECT count(*) FROM hints WHERE state = 'Active' AND expires_at < now() - interval '1 minute'` | non-zero for several sweeps means the expiry worker is not running or failing (`hint expiry sweep failed`) |
| Boot loop | `service exited with error` and restarts | any |
| 5xx | `request failed` / `handler panicked` | any |
| Table growth | row counts of `outbox_events`, `idempotency_keys`, `processed_events` (nothing deletes them) | capacity planning, see the [Runbook](/docs/operations/runbook#scaling-and-data-growth) |

Adding real instruments (outbox backlog and age, consumer outcomes, hint
issued/consumed/expired counts, request duration) is the obvious next step; none
exists on `develop`.
