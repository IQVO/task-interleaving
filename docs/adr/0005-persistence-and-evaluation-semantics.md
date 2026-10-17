# ADR 0005: persistence and evaluation semantics

## Status

Accepted (2026-10-17).

## Context

ADRs 0002 to 0004 fix the aggregates, the local copies and the event contract.
Building the service on them left a set of decisions open that every later step
depends on: how the state is stored and which database rule backs each
invariant, how a version conflict is told apart from a stale precondition, and
what the evaluation does when a local copy is incomplete (a task never seen, a
location without geometry, a site without a policy). They are recorded together
because the database schema is where most of them become enforceable. Where a
decision below refines wording of ADR 0003, this ADR wins and ADR 0003 stays
unchanged (ADR bodies are immutable).

## Decision

### Persistence

PostgreSQL through `pgx/v5`, migrations by golang-migrate. The SQL files live in
the top-level `migrations/` directory, embedded in the binary by package
`migrations`, so the image needs no files on disk. They are additive: a change
is a new numbered pair, an existing migration is never edited, and each `down`
recreates the previous schema exactly (a test walks every version up and down
and compares the schema). The binary reads `MIGRATIONS_DATABASE_URL` and falls
back to `DATABASE_URL`, so a deployment behind PgBouncer can run DDL over a
direct connection.

| table | key | notes |
|---|---|---|
| `interleaving_policies` | `site_id` | the settings of one site; `version` guards updates; `hint_ttl_ns` keeps the `time.Duration` exactly; a site without a row is inert |
| `station_trips` | `station_id` | position (site, location, optional point), last completed task and instant, `current_hint_id`, `version` guards updates |
| `hints` | `hint_id` | the hint **history**: one row per hint ever issued, with its `state`, `consumed_*`, `superseded_at` and a `version` that grows by one per state change; indexed by state, station and site for `GET /hints` |
| `candidates` | `(hint_id, rank)` | the ranked tasks of a hint, immutable once written; a task appears once per hint |
| `task_pool` | `task_id` | the task copy: type, site, `from_location`, `to_location`, capabilities, `priority_band`, `state` (`AVAILABLE`, `CLAIMED`, `COMPLETED`), `claimed_by` |
| `zones`, `locations` | `zone_id`, `location_code` | the layout copy; the geometry point sits on the location row and may arrive before the registration; a location is joined to its zone's site at read time; `decommissioned` is irreversible and the row stays |
| `processed_events` | `(consumer, event_id)` | the consumer idempotency claim |
| `outbox_events` | `id` | the transactional outbox, same column set as the fleet's |
| `idempotency_keys` | `key` | answers of POSTs made with an `Idempotency-Key` (2xx and 4xx only) |

- **A trip is a row plus its current hint.** The `StationTrip` aggregate holds
  the most recent hint in any state; `Snapshot` and `Restore` carry it. The
  repository writes the `station_trips` row first, then reconciles the history:
  a hint seen for the first time supersedes the station's Active one
  (`superseded_at` = the new hint's issue instant) and is inserted with its
  candidates; a known hint whose state changed moves on (`Consumed` also stores
  the consumed task and instant); a known hint that did not change is left
  alone. `current_hint_id` is a deferred foreign key, so a trip and its new hint
  are written in one transaction in either order.
- **One unit of work, one transaction.** `UnitOfWork.Do` begins a pgx
  transaction and carries it in the context; every repository joins it, and a
  nested `Do` joins the outer one. Aggregate save, outbox rows and the
  `processed_events` claim commit or roll back together, which is what makes a
  consumer effect and its claim atomic: a rolled-back effect un-claims the id,
  so the redelivery is processed, not skipped.
- **Byte order.** Keys that Go orders or compares (site ids, station ids, hint
  ids, task ids, location codes) use the `"C"` collation, so `ORDER BY` and
  keyset pages follow the same order as the in-memory adapters. Timestamps are
  `timestamptz` (microsecond precision) read back in UTC.
- **The in-memory adapters are the behavioural spec.** The repository contract
  suite (`internal/testing/repocontract`) runs against the in-memory adapters on
  every `go test` and against Postgres in the integration tests, so
  `go run ./cmd/api` without `DATABASE_URL` behaves like production for
  everything but durability.

### At most one Active hint per station

Invariant 2 of ADR 0002 is enforced twice. The aggregate supersedes the
previous hint in the same step as it issues the next, and the repository closes
the old row (`Superseded`) before it inserts the new one. The database is the
final arbiter: the **partial unique index**

```sql
CREATE UNIQUE INDEX uq_hints_one_active_per_station ON hints (station_id) WHERE state = 'Active';
```

admits one Active row per station, so no interleaving of writers (nor a bug in
a later change) can leave two Active hints. A write that loses on that index is
reported as `ErrConcurrentModification`. Hint rows are never deleted, so the
history is queryable by state, station and site.

### Two different conflicts

- **A policy `If-Match` that is missing or stale is `ErrVersionMismatch`**
  (HTTP 412, a precondition the caller got wrong). It is decided in the use
  case, before any write, by comparing the header with the stored version.
- **A lost write race is `ErrConcurrentModification`** (HTTP 409). Every
  version-guarded `Save` that matches no row, an insert over an existing key, and
  a loss on the Active-hint index return it. Two stations never conflict; two
  writers of one station or one site do, and the loser's unit of work rolls back
  with its claim, so the redelivered event is processed again.

### Evaluation semantics

Decided with the service and recorded here so the HTTP layer, the consumers and
the tests agree.

- **No declared policy means inert.** With no row in `interleaving_policies` for
  the site, a completion still moves the station (the position is real) but no
  hint is ever issued. `POST /recommendations` on such a site is refused
  (`ErrPolicyNotDeclared`): there is nothing to simulate.
- **The completion position is the task's `to_location` from the local task
  copy**, and the site is the task's `site_id`. The station is the claimer the
  copy recorded from `TaskClaimed`; the completion event itself is only needed
  for the task id and its instant. The point is the location's current geometry
  from the layout copy when it has one.
- **A task never seen is recorded as completed and otherwise ignored.** A
  `TaskCompleted` for a task absent from the pool copy (a claim or completion
  before the first `TaskAvailable`, or a task released before this context
  existed) writes a minimal `COMPLETED` row and issues nothing: there is no
  position to record. This refines ADR 0003, which said such a message is
  "counted and skipped". Recording the task keeps a later `TaskAvailable` replay
  from re-opening a task that is already done.
- **A completion older than the recorded one marks the task completed but leaves
  the trip alone.** Station position moves forward in time only; a completion
  not strictly newer than the recorded instant is a no-op for the trip, with no
  hint and no event. The task row is still `COMPLETED`.
- **A newer completion with no candidates leaves the previous Active hint to
  expire.** Only a new hint supersedes. A completion that finds nothing nearby,
  or whose origin has no geometry, issues no hint and does not touch the
  previous one; it stays Active until it is consumed or reaches its expiry. It
  is advisory and bounded by the TTL, so a stale one costs a lost tie-break at
  worst.
- **Unknown geometry skips a candidate; an origin without geometry yields no
  hint.** The distance of a place with no known point is unknown, never zero
  (the `travel` package); the recommender skips the candidate and, for the
  origin, returns `ErrOriginUnknown`.
- **A task starting at a decommissioned location is skipped; a decommissioned
  origin still counts.** A task whose `from_location` was decommissioned cannot
  be walked to, so it is never a candidate. A station that completed a task at a
  location decommissioned afterwards did stand there, so its geometry still
  serves as the origin. This refines ADR 0003, which said a decommission "deletes
  the location and its point": the row and its point stay and carry the flag.
- **Candidates come from all priority bands.** The pinned `candidates[]` payload
  carries no band, so this context issues candidates from every band of the site.
  fulfillment-execution, which owns the band ordering, applies a hint only inside
  one band and ignores the candidates of the others (ADR 0001). The `priority_band`
  of a task is stored and never interpreted here.
- **Claims.** A claim marks the task `CLAIMED` by the station and takes it out of
  the candidates of every later evaluation. It consumes the station's hint only
  when the task is a candidate of its Active, unexpired hint; any other claim
  leaves the hint Active.

## Consequences

- Hint history is queryable by state without a second store; `GET /hints` and the
  expiry sweeper read the same table.
- The index is the contract, not the use case: a new writer that forgets to
  supersede fails loudly instead of leaving two Active hints.
- A hint that outlives a completion with no candidates can be a little stale. The
  TTL (`hint_ttl_seconds`) is the bound, and fulfillment-execution re-checks the
  claim, so the cost is a lost tie-break.
- Recording unseen completed tasks adds a small row per unseen task; the copy is
  rebuilt by replaying the topic, which converges.
- Replaying a topic is safe: the copies converge through last-writer-wins on the
  task state and the point, everything else through `processed_events`.
