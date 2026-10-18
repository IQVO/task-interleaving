# ADR 0008: Analytics read side: a projector and a hint-adoption report over a separate analytical database

## Status

Accepted (2026-10-18). Fills the "a later phase" of
[ADR 0004](0004-cloudevents-envelope-and-type-catalogue.md) for the analytics
topic. Additive: the OLTP API, the integration topic
`warehouse.task-interleaving.events`, every payload and the outbox are
unchanged (the sink's golden tests pass unchanged). No OLTP migration. Adds a
second, separate database.

## Context

Every OLTP context of the fleet ships an analytics read side: a separate
analytical Postgres fed by a **projector** that consumes the context's own
analytics topic, and a read-only **reports** binary over it
(slotting-optimization ADR 0006 is the reference this ADR follows, and records
below where this service differs). task-interleaving already writes the topic:
since ADR 0004 every `InterleavingHintIssued`, plus the analytics-only
`InterleavingHintConsumed`, `InterleavingHintExpired` and
`InterleavingPolicyDeclared`, is inserted into the outbox in the SAME unit of
work as the aggregate, as one row per topic under ONE CloudEvents id, with
`dataschema=urn:warehouse:task-interleaving:analytics:<EventName>:v1` (the
events sink; `TestSink_*` goldens). Nothing read it.

The question the data product answers is **hint adoption**: of the advice the
service issues, how much do operators act on, how fast, and how good was the
candidate they took. That is what tells an operator whether a site's policy
(`maxDetourM`, `maxCandidates`, TTL) is set right.

## Decision

### 1. What is projected

All four analytics types. The three hint events fold into one fact row per
hint; `InterleavingPolicyDeclared` is only recorded as an applied id (it feeds
the freshness and nothing else: no policy-history report is asked for).

### 2. The analytical model (`analytics/migrations/0001_hint_adoption`)

Facts, never aggregates, so the day buckets can be recomputed for any range:

- `analytics_processed_events`: the dedupe set and the freshness source
  (`max(occurred_at)`).
- `hint_facts`, one row per hint seen: `station_id`, `site_id`, `issued_at`,
  `expires_at`, `candidate_count`, `consumed_at`, `consumed_rank`,
  `consumed_detour_m`, `expired_at`.

Each milestone column is the CloudEvents `time` of the earliest such event
seen (`LEAST`), so arrival order never matters; the attributes of an event
(expiry and candidate count, rank and detour) are first-writer-wins. A hint
**superseded** by a newer one raises no event (ADR 0005): its row simply never
gets a `consumed_at` or `expired_at`.

### 3. The projector (`cmd/ti-projector`)

- Consumes `warehouse.task-interleaving.analytics` under a fixed consumer
  group read from `ANALYTICS_CONSUMER_GROUP` (default
  `task-interleaving-analytics`, set by the chart); at-least-once, offsets
  committed only after success, a new group replays from the earliest offset.
- **Dedupe and effect in one transaction**: the CloudEvents `id` is claimed in
  `analytics_processed_events` (`ON CONFLICT DO NOTHING`) and, only if it was
  new, folded into `hint_facts` in the same analytical transaction.
- **Policy**: not a CloudEvents 1.0 message: skipped and committed past (a
  rate-limited WARN); a valid CloudEvent of another type: ignored; a known type
  whose payload contradicts its envelope (`station_id` not the subject,
  `site_id` not the subject of a policy event) or fails validation, or one the
  store deterministically rejects (SQLSTATE class 22 or 23): dead-lettered at
  once, byte for byte, to `warehouse.task-interleaving.analytics.dlq`; a
  transient failure retries the same message with capped backoff and is never
  dead-lettered.
- **It stops when its consumer stops**: `awaitStop` turns a consumer exit, with
  or without an error, into a non-zero process exit (INCIDENTS 2026-10-12, the
  silent projector; `TestAwaitStop_ConsumerLoopExitIsAnError`).
- Writes only the analytical database (`ANALYTICS_DATABASE_URL`, a direct DSN);
  never opens the OLTP database and calls no sibling context. `/healthz` and
  `/readyz` on `:8091`.

### 4. The report (`cmd/ti-reports`, `:8092`)

Read-only HTTP over the analytical database through a read-only pool
(`default_transaction_read_only=on`); RFC 7807 errors; no auth (fleet rule);
additive paths in `apis/openapi.yaml` (tag `Reports`, server
`http://localhost:8092`); bodies are snake_case like every other context's
reports and the fleet freshness charter, whatever the OLTP API's casing.

| endpoint | question | shape |
| --- | --- | --- |
| `GET /reports/hint-adoption` | how much advice do operators take, per day | `from`, `to`, `site_id?`, `granularity` (`day`), `days[]`: `day`, `hints_issued`, `hints_consumed`, `hints_expired`, `adoption_rate`, `consumed_first_rank`, `mean_detour_m`, `time_to_consume{count,p50_seconds,p95_seconds}` |
| `GET /reports/freshness` | how far the projection is behind | `as_of` (newest applied CloudEvents time), `lag_seconds` (never negative); both `null` until the first event |

Rules (the range, granularity and day-window rules are slotting's): `from` /
`to` are optional RFC 3339 instants, both omitted = the 30 days ending now, at
most 366 days, `from` inclusive, `to` exclusive, an empty or inverted range is
a 400; `siteId` narrows every counter to one site; `granularity=day` is the
only bucket. `days[]` is dense (zeros included). A hint counts in the day its
own milestone falls in (issues by `issued_at`, consumptions and
`time_to_consume` by `consumed_at`, expiries by `expired_at`).

- `adoption_rate` is consumed / (consumed + expired) of the hints that ENDED in
  the window, `null` when none did. A superseded hint emits neither ending event
  and is in no denominator: the rate answers "when a hint ran its course, did
  the operator act on it", not "what share of all hints".
- `time_to_consume` is the issued-to-consumed time of the hints consumed in
  the window whose issue the projection also saw; p50/p95 are
  linear-interpolated (`percentile_cont`), `null` when `count` is 0.
- `consumed_first_rank` and `mean_detour_m` say how good the taken candidate
  was (rank 1 is the nearest).
- SQL only counts and takes percentiles; the range rules, day windows, rate and
  freshness lag live in `internal/analytics/report` (pure, imports nothing
  internal). The in-memory store and the Postgres store run the same contract
  test.

### 5. Not reported

- **Policy history** (`InterleavingPolicyDeclared` per version): projected as an
  id only; a report can add a fact table from the same stream later.
- **Per-candidate analytics** (which task types win) and **supersession
  counts**: no event carries a supersession (ADR 0005), and the candidate list
  of the issued event is reduced to its length.
- **Task execution** (fulfillment-execution's claims beyond `Consumed`): another
  context's topic, not pulled into this projection.

### 6. Why a separate database

The projection is a derived copy that can be dropped and rebuilt from the
topic; keeping it out of the OLTP database means a heavy report cannot take
OLTP connections, the schemas evolve independently, the reports role can be
read-only on a database that holds nothing transactional, and the projector has
no credentials on OLTP data. The cost is a second role and database to
provision in `warehouse-infra`.

### 7. Packaging

The one image builds every `cmd/*` (the Dockerfile's loop), so
`/app/ti-projector` and `/app/ti-reports` ship in it; the analytical migrations
are embedded. The chart gains the components `analytics-projector` and
`analytics-reports` (Deployment, and Service `<release>-reports`) plus a Secret
with `ANALYTICS_DATABASE_URL`, behind `analytics.enabled` (default `false`: the
default render is unchanged). External routing to the reports Service is
warehouse-infra's job.

## Consequences

- The analytics stream stays an own-service topic, not an integration contract:
  only `ti-projector` consumes it.
- Hints issued before the projector existed are partly visible (a consumption
  without a seen issue has no `time_to_consume`); replaying from the earliest
  retained offset is the backfill.
- Three isolation rules join the architecture test: the `internal/analytics`
  region depends on nothing internal but itself, and the OLTP domain and
  application never import it or the analytics store.

## Alternatives considered and rejected

- **Project from the integration topic**: it carries only `InterleavingHintIssued`;
  adoption needs the analytics-only events.
- **Pre-aggregated daily counters**: cannot be re-bucketed for a partial first
  or last day, and a late or replayed event would need decrement logic.
- **Count the adoption rate over issued hints**: a hint issued today is still
  open today, and superseded hints never end; the ended-hints denominator is
  stable.
- **Compute the report from the OLTP tables**: the hint table has the outcome
  but not the milestone history, and the analytical queries would run on the
  OLTP database.
