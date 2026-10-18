---
id: troubleshooting
title: Troubleshooting
sidebar_label: Troubleshooting
sidebar_position: 4
---

# Troubleshooting

Symptom, cause, check, fix, built from the failure modes visible in the code.
Quoted log lines and REST bodies come from real runs of `cmd/api`. SQL probes
assume Postgres mode; the table names are in [Runbook](/docs/operations/runbook#migrations).
There is no outbound HTTP or MCP client in this service, so there is **no circuit
breaker** to trip; the dependencies that can fail are Postgres and Kafka.

## Boot and runtime

| Symptom | Likely cause | Check | Fix |
| --- | --- | --- | --- |
| Process exits 1 at start: `HINT_EXPIRY_INTERVAL "abc" is not a valid duration` or `... must be greater than zero` | The expiry interval is not a positive Go duration | The ERROR line `service exited with error` (`cmd/api/config.go`) | Use `5s`, `500ms`, ...; unset it for the default 5 s |
| Exits 1 before anything else happens: `invalid configuration: DATABASE_URL is required: this context runs on real Postgres` (the same line for `KAFKA_BROKERS`, `FULFILLMENT_CONSUMER_GROUP` and `LAYOUT_CONSUMER_GROUP`, each `... is required: this context runs on real Kafka`; every missing variable is listed in one error) | A mandatory variable is unset. There is no in-memory or consumer-less mode to fall back to ([ADR 0006](/docs/adr/0006-postgres-and-kafka-are-the-only-runtime)) | The `error` field of `service exited with error`; env of the pod | Set the variables ([Configuration](/docs/operations/configuration)); the group ids are stable names you choose |
| Exits 1: `FULFILLMENT_MODE=permissive is not supported: this context runs on real Kafka` (same for `LAYOUT_MODE`) | A deployment still sets the retired `permissive` mode | env of the pod | Remove the variable or set it to `kafka` |
| Exits 1: `unknown FULFILLMENT_MODE "x" (want kafka)` (same for `LAYOUT_MODE`) | Mode typo | env of the pod | `kafka`, or leave it unset |
| Exits 1 after roughly 15 s: `run migrations (after 5 attempts): ...` or `ping postgres (after 5 attempts): ...` | Postgres unreachable, wrong DSN, or the migration itself fails | The WARN lines `retrying` with `op` and `err`; connect with `psql` from the same network | Fix the DSN or network. The retry exists because the first outbound dial of an Istio-injected pod can be reset; it is not a license to boot without a database |
| Migration fails and every later boot fails the same way | A partial migration left `schema_migrations.dirty = true`; golang-migrate refuses to continue | `SELECT version, dirty FROM schema_migrations` | Repair the schema by hand, then force the version with the golang-migrate tooling. Take a backup first |
| Migrations hang or fail through PgBouncer | `DATABASE_URL` goes through a transaction pooler, which breaks golang-migrate's advisory lock | Is the DSN a pooler address? | Set `MIGRATIONS_DATABASE_URL` to a direct connection |
| `/readyz` is 503 with `{"status":"not_ready"}` | The process is shutting down (the drain gate is only flipped on SIGINT/SIGTERM) | Is the pod terminating? | Nothing to fix; a new pod takes over |
| `/readyz` is 503 `dependency-unavailable`, detail `not ready: kafka` (or `postgres`, or both) | The Kafka broker is unreachable (no broker answered a metadata request), or Postgres does not answer a ping. A missing topic never causes it: topics are not checked, the broker auto-creates them on first write and the service never creates them through an admin API | WARN `readiness: dependency is not ready` has the cause in `error` | Restore the broker or database. The process stays up and recovers by itself: the verdict is re-checked every 3 s |
| `/readyz` is 200 but requests fail with 500 | A single query failing for another reason (statement timeout, constraint, bug); readiness only proves the database answers a ping | `request failed` lines (the real error is only logged) and `psql` from the pod | Fix the cause shown in the log |
| Statements fail with `canceling statement due to statement timeout` | Pool statement timeout is 5 s | Logs of the failing request; slow queries on `hints` listing or the open-task read | Investigate the query; the timeout is compiled in |

## Hints

| Symptom | Likely cause | Check | Fix |
| --- | --- | --- | --- |
| No hint is ever issued | Any of: the site has no policy (inert); the pool copy is empty (the consumers have not caught up, the topic is empty, or fulfillment-execution is not announcing the task); the task that was completed was never announced (no `to_location`, so no position); no open task has geometry; all candidates exceed `maxDetourM`; the task types are not in `eligibleTaskTypes` | `GET /sites/{siteId}/policy` (404 `policy-not-found` = inert); `SELECT count(*), state FROM task_pool GROUP BY state`; `SELECT count(*) FROM locations WHERE x_m IS NOT NULL`; `POST /recommendations` with `fromLocation` shows what the ranker would return, and the 422 slug says which precondition fails | Declare a policy; check the consumers (`consumer running` / `kafka reader error; retrying`); wait for geometry. Note fulfillment-execution announces `TaskAvailable` today only for RELOCATE and REPLENISH tasks (see [Integration](/docs/ecosystem/integration)), so a pool fed by the live producer contains those two types only |
| A completion is applied but the station has no position | The completed task is not in the pool, or has no `to_location` or `site_id`: outcome `ignored` in the `event processed` log line | `SELECT * FROM task_pool WHERE task_id = '<id>'` | Nothing to fix in this service; the announcement is missing upstream |
| A hint stays `Active` past its TTL | The expiry worker is not running or failing | `hint expiry worker running` at boot; `hint expiry sweep failed`; `SELECT count(*) FROM hints WHERE state = 'Active' AND expires_at < now()` | Restart; check the sweep error. A claim after the TTL does **not** consume the hint (the domain refuses it), only the sweep expires it |
| A claim did not consume the hint | The claimed task is not one of the hint's candidates, the claimer is another station, or the claim time is before the hint was issued or at or after `expires_at` | Outcome `applied` but the hint unchanged in `GET /hints/{hintId}` | Expected behaviour: the hint is advisory |

## Kafka and the outbox

| Symptom | Likely cause | Check | Fix |
| --- | --- | --- | --- |
| Events never reach Kafka; `outbox_events.published_at` stays NULL | The broker is unreachable (the relay is always on; its writer sets `AllowAutoTopicCreation`, so a missing output topic is created by the broker on the first write) | Relay ERROR `outbox relay pass failed` with the cause; `last_error` and `attempts` on the head row; `/readyz` | Restore the broker. Rows are drained in order |
| Relay logs `outbox relay pass failed` over and over | Broker unreachable, or the broker has topic auto-creation disabled and the target topic does not exist (the fleet cluster auto-creates; a local broker may not) | `last_error` and `attempts` on the head row | Restore the broker, or create the topic where auto-creation is off. The pass retries every `OUTBOX_RELAY_INTERVAL` |
| ERROR `outbox relay: row reached the attempt cap and is excluded from future claims` | A poison row, or an outage longer than `OUTBOX_MAX_ATTEMPTS` passes (default 20 at 1 s = about 20 s of broker outage; observed) | `SELECT id, topic, event_type, attempts, last_error FROM outbox_events WHERE published_at IS NULL AND attempts >= 20` | Fix the cause, then `UPDATE outbox_events SET attempts = 0 WHERE id = <id>` |
| Fulfillment or layout consumer is not consuming; ERROR `kafka reader error; retrying` repeats with `consumer`, `topic` and a `detail` | The consumed topic does not exist yet (its producer has not written, and the service never creates a consumed topic), the broker or its group coordinator is unreachable, or the group join fails. The consumer always runs and keeps retrying; `/readyz` is not affected by a missing topic and is 503 `not ready: kafka` only while the broker is unreachable | The `detail` field; `consumer running` lines list `topic` and `group_id`; `/readyz` | Fix the broker, or wait for the producer to publish (the broker creates the topic on its first write); no restart is needed |
| Consumer lag grows on `warehouse.fulfillment.events` | One message is failing transiently and blocks the partition until its 5 attempts end (backoff up to 5 s) | Repeated `handling failed; retrying the same message` with the same `partition`/`offset`; database health | Fix the dependency (usually Postgres). After 5 attempts the message goes to the DLQ and the consumer moves on |
| Messages appear on `<topic>.dlq` | A transient failure outlasted 5 attempts, or a payload that can never be applied (malformed `data`, `task_id` missing, `site_id` or `from_location` missing on `TaskAvailable`, `xM`/`yM`/`zM` missing, non-finite coordinate) | Read the `x-dlq-error` header; the log lines `dead-lettering` | Fix the producer or the cause, then re-publish the value to the source topic (see [Runbook](/docs/operations/runbook#routine-procedures)). A message that is not a CloudEvent, or has a type this service does not handle, is **not** dead-lettered: it is skipped with a WARN |
| Consumed events seem ignored | Outcome `duplicate` (the id was already claimed in `processed_events`), `stale` (older than the copy), or `ignored` | The `outcome` field of `event processed` | Expected idempotency. To force a re-application, see the rebuild procedure in the Runbook |
| ERROR `consumer stopped; restarting` | The Kafka reader failed (not a cancellation), typically an unreachable broker; the consumer is run again after 1 s | The `error` field | None needed; the offset is only committed after a successful handling, so nothing is lost. Persistent restarts mean the broker is down |
| Nothing consumes `warehouse.task-interleaving.events` | No consumer exists yet: fulfillment-execution does not read `InterleavingHintIssued` | [Integration](/docs/ecosystem/integration) | Not a fault of this service |

## REST problem types

Every error is `application/problem+json`,
`type = https://errors.task-interleaving.warehouse-systems.dev/<slug>`, with
`title`, `status`, `detail` and `instance` (the request path). The slugs below
are the catalogue in `internal/adapters/inbound/http/errors.go`.

| Status | Slug | When | What to do |
| --- | --- | --- | --- |
| 400 | `malformed-request` | Body is not valid JSON for the operation: unknown field, wrong type, trailing data, empty body, body over 64 KiB, or `fromLocation` blank or longer than 128 characters | Fix the body against `apis/openapi.yaml` |
| 400 | `invalid-site-id`, `invalid-station-id`, `invalid-hint-id` | Id is blank, over 64 characters, contains whitespace, a control character or `/` (hint ids must start with `hint-`) | Fix the path or filter |
| 400 | `invalid-policy` | `maxDetourM` not a finite number above 0; `maxCandidates` outside 1 to 5; `hintTtlSeconds` not above 0 or above 2147483647; `verticalWeight` negative or non-finite; a required field missing | Use the `detail` text |
| 400 | `invalid-task-type` | `eligibleTaskTypes` empty, missing or contains something outside `PICK, PACK, REBIN, SLAM, RELOCATE, REPLENISH` | |
| 400 | `invalid-cursor`, `invalid-limit`, `invalid-state` | Cursor not handed out by this API; `limit` outside 1 to 500; state not `Active`, `Consumed`, `Expired` or `Superseded` | |
| 400 | `idempotency-key-required` | `POST /recommendations` without an `Idempotency-Key`, or with one longer than 128 characters | Send a unique key per logical request |
| 404 | `policy-not-found`, `hint-not-found`, `station-not-found` | Unknown resource. `policy-not-found` means the site is **inert**; `station-not-found` means no completion was ever recorded for it | Normal for a new site or station |
| 409 | `concurrent-modification` | Two writers raced on one aggregate; the loser's version guard failed | Re-read and retry |
| 412 | `version-mismatch` | `PUT /sites/{siteId}/policy` with a stale or missing `If-Match` on an existing policy, or with an `If-Match` on a site that has none; an `If-Match` that is not a version (`*`, `W/"1"`) also counts as stale | `GET` the policy and send its `ETag` as `If-Match` |
| 422 | `policy-not-declared` | `POST /recommendations` for a site without a policy | Declare one |
| 422 | `origin-unknown` | The origin location has no known geometry in the layout copy (`detail` names it) | Wait for or feed `LocationGeometryUpdated`; try another `fromLocation` |
| 422 | `station-position-unknown` | No `fromLocation` given and the station has no recorded position | Send `fromLocation` |
| 422 | `idempotency-key-reused` | Same `Idempotency-Key` with a different method, path or body | Use a new key, or resend the original request to get the stored answer |
| 500 | `internal-error` | Unmapped error or handler panic; `detail` is always `an unexpected error occurred` | Read the `request failed` / `handler panicked` log line |
| 503 | `dependency-unavailable` | `GET /readyz` only: Postgres or the Kafka broker (no broker answers a metadata request; topics are never checked) is not usable; `detail` is `not ready: ` plus the names | See the `/readyz` row under Boot and runtime |

### Idempotency in practice

- Only `POST /recommendations` is wrapped; `PUT` and `GET` never need a key.
- A key with a stored answer replays that answer byte for byte, whatever its
  status (2xx and 4xx are stored). A 5xx is never stored, so the same key can
  retry real work.
- The middleware runs the handler inside the unit of work and rolls it back on
  a 4xx or 5xx; the 4xx answer is then stored on its own. Two concurrent
  requests with one new key both run, and the first to store wins; the loser
  rolls back and replays the winner's answer.
- Nothing purges `idempotency_keys`, so a key is remembered indefinitely.
