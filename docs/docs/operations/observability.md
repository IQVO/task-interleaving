---
id: observability
title: Observability
sidebar_label: Observability
sidebar_position: 3
---

# Observability

Short version: **structured logs, five OpenTelemetry metrics on `GET /metrics`,
and no exported traces.** `cmd/api` installs the OTel Prometheus exporter and
mounts it on the same HTTP port as the API (there is no separate metrics port).
`internal/telemetry` declares the instruments; the A2A adapter creates spans, but
no tracer provider is installed, so none is recorded.

## Metrics

The instruments come from the global OTel `MeterProvider` under the scope
`github.com/claudioed/task-interleaving`. `cmd/api` sets the provider with
`telemetry.PrometheusMetrics()` and serves the result on `GET /metrics`
(`internal/adapters/inbound/http/server.go`); the route is not part of
`apis/openapi.yaml`. Only `cmd/api` installs the exporter: `cmd/mcp`, `cmd/a2a`,
`cmd/ti-projector` and `cmd/ti-reports` expose no `/metrics`.

| Instrument (OTel name) | Type | Unit | Attributes | Meaning |
| --- | --- | --- | --- | --- |
| `taskinterleaving.hints.issued` | counter (int64) | none (a count) | `site` | Hints issued to stations by the interleaving recommender. Counted when an `InterleavingHintIssued` fact is handed to the outbox event sink. |
| `taskinterleaving.hints.consumed` | counter (int64) | none (a count) | `site` | Hints whose candidate the hinted station claimed (a `HintConsumed` fact). |
| `taskinterleaving.hints.expired` | counter (int64) | none (a count) | `site` | Hints that reached their `expires_at` unconsumed (a `HintExpired` fact, raised by the expiry worker). |
| `taskinterleaving.recommender.duration` | histogram (float64) | `s` | none | Wall time of one recommender run, simulation or issuance alike. `TimedRanker` wraps every `Rank` call and records it whether the call succeeds or fails. |
| `taskinterleaving.kafka.consumer.lag` | histogram (float64) | `s` | `consumer` (`fulfillment consumer` or `layout consumer`) | How far processing trails the CloudEvent `time` of a handled message (`0` when the event carries no time). Recorded in `Consumer.HandleMessage` for messages whose `type` has a handler, just before the handler runs; an undecodable message or a type the consumer ignores records nothing. |

The three hint counters are incremented by `telemetry.CountingEventSink`, which
wraps the outbox event sink in `cmd/api/adapters.go` and counts each hint fact
before delegating. `telemetry.TimedRanker` wraps the recommender in the same file.
The instruments are created at package initialisation from the global meter, so
they cost one call each and record nothing until the exporter is installed.

Series names as the Prometheus exposition renders them (read from a `GET
/metrics` rendering of a recorded sample; counters gain `_total` and the
second-unit histograms gain `_seconds`, with `_bucket`, `_sum` and `_count`):

| Series | Labels beyond `otel_scope_*` |
| --- | --- |
| `taskinterleaving_hints_issued_total` | `site` |
| `taskinterleaving_hints_consumed_total` | `site` |
| `taskinterleaving_hints_expired_total` | `site` |
| `taskinterleaving_recommender_duration_seconds` | none |
| `taskinterleaving_kafka_consumer_lag_seconds` | `consumer` |

`GET /metrics` also serves the Prometheus client's default collectors (`go_*`
runtime series and `process_*` series) and a `target_info` series.

| Question | Answer | Evidence |
| --- | --- | --- |
| OTLP exporter or `OTEL_*` variables | none | `cmd/api` reads only the variables in [Configuration](/docs/operations/configuration); metrics leave through `/metrics` only |
| HTTP server metrics (otelchi, otelhttp) | none wired | `otelhttp` appears in `go.mod` as an indirect dependency only; no middleware uses it |
| Pod scrape annotations | chart value `metrics.prometheus.enabled`, default `false` | `charts/task-interleaving/values.yaml`; when on, the deployment renders `prometheus.io/scrape`, `prometheus.io/path` (`/metrics`) and `prometheus.io/port` (`8080`). `warehouse-infra`'s `helm-values/task-interleaving.yaml` does not set it |
| Grafana dashboard for this context | exists, no panel on these metrics. `warehouse-infra`'s `scripts/gen-context-dashboards.py` registers the context with `business_metrics: []` on the comment "0 OTel instruments"; that comment is out of date, the code registers the five instruments above (checked on `origin/develop`: `internal/telemetry/metrics.go`, installed by `cmd/api` only) | `warehouse-infra` `terraform/dashboards/contexts/task-interleaving.json` (uid `warehouse-task-interleaving`) has Kong request, error and latency rows, HTTP and Go-runtime rows and Loki log rows; none of its queries reads a `taskinterleaving_*` series |
| Alert rules | Kong 5xx ratio, pod restarts, API availability and latency burn | `warehouse-infra` `terraform/alerting/warehouse-alert-rules.json`, groups `warehouse-task-interleaving` and `warehouse-task-interleaving-slo`; none uses a `taskinterleaving_*` series |

## Traces

`internal/adapters/inbound/a2a/skills.go` starts one span per A2A skill call from
the global tracer (`a2a.skill <skill>`, attribute `a2a.skill`, status Error when
the skill answers a problem). No binary calls `SetTracerProvider`, so the global
tracer is a no-op and **no span is recorded or exported today**. The HTTP router,
the MCP server and the Kafka consumers create no span.

The only other OpenTelemetry code is in
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
| Boot | INFO | `postgres adapters configured`, `kafka topics (informational; the broker auto-creates them on first write, the service never creates them through an admin API)` | `brokers`, `consumes`, `produces` (the last two are the topic lists); topics play no part in `/readyz` |
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
| Consumer | ERROR | `kafka reader error; retrying` (kafka-go retrying a group join, a consumed topic that does not exist yet because its producer has not written, or an unreachable broker; without this logger it would be silent. Not a readiness failure: the consumer recovers by itself) | `consumer`, `topic`, `detail` |
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
answers and a Kafka broker answers a metadata request, never topics; 503 once
shutdown starts or Postgres or the Kafka broker is unreachable) are described in the
[Runbook](/docs/operations/runbook#probes). The cause of a failed dependency is
only in the WARN log line above; the response body names the dependency.

## Suggested checks and alerts

These are suggestions built from the data that exists, not alert rules that
ship with the repo. The first rows below are log searches and SQL probes against
the service database; the last rows use the metrics above.

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
| Hint adoption | `rate(taskinterleaving_hints_consumed_total[1h])` against `rate(taskinterleaving_hints_expired_total[1h])`, by `site` | suggestion: a site whose hints are almost all expired (its policy or the pool is stale) |
| Recommender latency | `histogram_quantile(0.95, sum by (le) (rate(taskinterleaving_recommender_duration_seconds_bucket[5m])))` | suggestion: a sustained rise |
| Consumer lag | `histogram_quantile(0.95, sum by (le, consumer) (rate(taskinterleaving_kafka_consumer_lag_seconds_bucket[5m])))` | suggestion: lag growing without bound on one consumer |
| Table growth | row counts of `outbox_events`, `idempotency_keys`, `processed_events` (nothing deletes them) | capacity planning, see the [Runbook](/docs/operations/runbook#scaling-and-data-growth) |

No instrument covers the outbox backlog and age, consumer outcomes or HTTP request
duration yet; use the SQL probes and log lines above for those.
