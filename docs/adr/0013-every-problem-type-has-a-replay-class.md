# ADR 0013: Every problem type has a replay class; a retryable refusal is not stored

## Status

Accepted (2026-11-20). Adopts the fleet rule decided by the owner on 2026-11-18 (standard:
warehouse-harness-template `docs/idempotency-replay-classes.md`, rule
`.claude/rules/fleet/idempotency-and-outbox.md`). Supersedes one sentence of
[ADR 0010](0010-adopt-the-fleet-rest-api-guideline.md) item 2 ("a stored 2xx or 4xx") and the "2xx and 4xx only" of
the `idempotency_keys` row of [ADR 0005](0005-persistence-and-evaluation-semantics.md); the rest of both stands.
Amended 2026-11-21 (fleet audit 2026-11-21, defect 1): an in-flight duplicate re-reads the key before an unstored
answer, and a stored answer replays for 24 h only (see "Amendment 1" below).

## Context

`Idempotency.Middleware` (`internal/adapters/inbound/http/idempotency.go`) stored the first answer below 500 of
every key, refusals included, and replayed it. A refusal that a retry could pass was therefore pinned: a
`422 policy-not-declared` replayed to a caller whose site declared its policy a minute later, a
`422 station-position-unknown` replayed after the station completed its first task.

**The store (read before this change, same lines before and after).** The key row lives in the business
transaction. `serveFresh` opens `UoW.Do` (`idempotency.go`), runs the handler inside it and, for a 2xx, saves the
answer in that same unit of work, so the effects, their outbox rows and the key row commit or roll back together.
A refusal returned `errRollback` from the unit of work: the **whole unit of work is rolled back** (a rejected request
leaves no effect, and a Postgres transaction that hit a constraint is aborted anyway), and `finishRejected` then
stored the answer by a separate write (`Store.Save`, `ON CONFLICT (key) DO NOTHING`, first writer wins). A 5xx is
rolled back and never stored. There is no in-flight lock: two requests with one new key both run, and the second
`Save` blocks on the key's unique index until the first transaction ends.

**Consequences for the classes.**

- **OUTCOME cannot exist in this service today.** The only keyed route is `POST /recommendations`; its use case
  (`usecases/recommend.go` `Handle`) only reads (`Trips.Get`, `Policies.Get`, `Ranker.Origin|Rank`) and writes
  nothing; and a refusal rolls the whole unit of work back, so even a refusal after a write would discard the write.
  No problem type is an OUTCOME and a test pins that (`TestNoProblemTypeIsAnOutcome`). The middleware still honours
  the class (below) so the first OUTCOME problem type of a future command works; a synthetic one proves it.
- **RETRYABLE is skipping the separate write.** The refusal's unit of work is already rolled back, so no key row
  exists; releasing the key is not calling `Save`.

## Decision

Every problem type declares exactly one replay class on its catalogue entry (the `class` field of `problem`,
`internal/adapters/inbound/http/errors.go`, type in `replay_class.go`). A problem cannot be built without one:
`newProblem(status, slug, title, detail, class)` is the only constructor (an AST test forbids `problem{...}`
literals elsewhere) and registers it; a slug has one status and one class (a second registration panics at start).
The catalogue keeps its shape (a slice of `{err, problem}`); only the literals became `newProblem` calls, so the wire
slugs, titles, statuses and details are unchanged.

| Class | Meaning | Middleware |
| --- | --- | --- |
| `FINAL` | a function of the request alone | stored and replayed (unit of work rolled back, answer saved on its own) |
| `OUTCOME` | the command committed state, then answered an error that must be remembered | stored and replayed; the writes commit in the same unit of work as the key row, as a 2xx does |
| `RETRYABLE` | nothing committed; the answer depends on state that can change | not stored, the key is released, a retry executes |

`writeProblem` (every problem, `writeError` included) marks the middleware's capture with the class. The middleware
stores a 2xx, or a refusal marked FINAL or OUTCOME, and nothing else. A 5xx and a panic are never stored; a refusal
that reaches it without a class (a handler that wrote a 4xx by hand) is released like RETRYABLE. The middleware holds
no slug list and no status range beyond 2xx and 5xx. 2xx, 5xx and FINAL answers are byte-identical to before.
Releasing a RETRYABLE key means a different body under the same key afterwards is a new request, not
`idempotency-key-reused`. When unsure between FINAL and RETRYABLE the class is RETRYABLE.

**A concurrent duplicate when the first ends RETRYABLE.** There is no in-flight lock, so a duplicate that is already
running executes itself and is answered with its own result (its own refusal, or a 2xx that it stores and that the
first request's retry then replays). Nothing is stored for the refusals; a later retry runs again.
(`TestConcurrentCopiesOfARetryableRefusalAreAllAnsweredAndNothingIsStored`.) A duplicate whose original has stored
an answer by the time it answers replays that answer instead (Amendment 1).

### Amendment 1 (2026-11-21): in-flight duplicates replay; the 24 h window

**An unstored answer re-reads the key first.** Before the middleware answers an UNSTORED refusal (RETRYABLE,
unclassified, or any 5xx) it reads the key again (`finishUnstored`, `idempotency.go`). Found: the request answers
the stored answer with `Idempotency-Replayed: true` (another body: `409 idempotency-key-reused`); not found: its own
refusal goes out and the key stays released. A failed re-read is logged at WARN and the request's own refusal goes
out. N copies of one keyed request are one run and N-1 replays. This is the harness rule ("In-flight duplicates",
`docs/idempotency-replay-classes.md`) in the shape of asrs-control #16 (material-flow-control `cab4359` +
`f4b80e1`): the key Lookup runs before the unit of work and nothing holds the key while a request runs (the key row
is written only after the handler), so a re-read at the start of the unit would run before the original commits and
find nothing under READ COMMITTED. Re-reading before the unstored answer is the same as re-reading under a lock,
because the refusal already rolled back every write.

**Reachability today.** The audit row (a copy blocks on a hint's unique index, then answers `409
concurrent-modification`) does NOT reproduce: `Recommend` writes nothing and takes no row lock (`usecases/recommend.go`;
`hint_history.go` `insertHint` is reached only through `StationTripRepo.Save` from the consumers' `TripWriter`), so
the only lock a copy can wait on is the idempotency key itself, after which `saveAndReread` already replays the
original. `cmd/api` `TestPostgresAnInFlightDuplicateReplaysTheOriginalsAnswer` pins that over the real router and
Postgres. The re-read makes the middleware correct for any keyed command that locks or writes inside its use case,
and the harness template's `TestConcurrentDuplicateReplaysTheOriginalsStoredAnswer` (red without it) holds it.

**The replay window.** A stored answer replays while it is at most 24 h old (`IdempotencyWindow`): `Lookup` ignores
an older row and `Save` replaces it (`ON CONFLICT (key) DO UPDATE ... WHERE created_at < now - 24 h`), so past the
window the same key is a new request, and another body under it is not `idempotency-key-reused`. The re-read honours
the same window. Purging expired rows is still a follow-up (nothing deletes them).

### The classes (held equal to the catalogue by `TestTheADRDeclaresTheClassOfEveryProblemType`)

Only `POST /recommendations` is keyed; the problem types of the other routes, of the readiness probe and of the
separate reports binary are classified too (a problem type without a class fails a test) and marked "not keyed".

| Problem type | Class | Status | Reason (code) |
| --- | --- | --- | --- |
| `malformed-request` | FINAL | 400 | the request alone: `decodeJSON` (`response.go`), the body bound (`idempotency.go` `bufferBody`), a `fromLocation` outside the schema's bounds (`errInvalidLocation`, `recommend_handler.go`). Shared by two errors with one class |
| `idempotency-key-required` | FINAL | 400 | the header alone; answered before the store, never stored |
| `idempotency-key-invalid` | FINAL | 400 | the header alone; answered before the store, never stored |
| `idempotency-key-reused` | FINAL | 409 | the request and the stored fingerprint; it is the answer of a hit, never stored |
| `invalid-station-id` | FINAL | 400 | the body's `stationId` (`stationtrip.NewStationID`, blank check in `recommendInput`); the request alone |
| `invalid-site-id` | FINAL | 400 | the path value alone (not keyed: `PUT`/`GET` policy) |
| `invalid-hint-id` | FINAL | 400 | the path value alone (not keyed) |
| `invalid-policy` | FINAL | 400 | the policy settings of the body alone (not keyed: `PUT` policy) |
| `invalid-task-type` | FINAL | 400 | the body alone (not keyed) |
| `unknown-query-parameter` | FINAL | 400 | the query alone (not keyed) |
| `invalid-cursor` | FINAL | 400 | the query alone (not keyed) |
| `invalid-limit` | FINAL | 400 | the query alone (not keyed) |
| `invalid-state` | FINAL | 400 | the query alone (not keyed) |
| `policy-not-found` | RETRYABLE | 404 | the site declares its policy later by another request (`GetPolicy`); not keyed. J1 |
| `hint-not-found` | RETRYABLE | 404 | hints are issued by the event consumers, not by the caller (`ApplyTaskCompleted`); an unknown id can exist after the next event; not keyed. J1 |
| `station-not-found` | RETRYABLE | 404 | a station exists once a completion event is consumed (`ApplyTaskCompleted`); not keyed. J1 |
| `concurrent-modification` | RETRYABLE | 409 | the version guard lost a race (`repository.ErrConcurrentModification`, `DeclarePolicy`, the trip saves); nothing committed. Not keyed (see "What the proofs cover") |
| `version-mismatch` | RETRYABLE | 412 | MIXED: a stale `If-Match` on an existing policy never passes, but an `If-Match` on a site with no policy yet (`declare_policy.go` 77-79 against 87-89) passes once the first declaration lands; not keyed. J2 |
| `policy-not-declared` | RETRYABLE | 422 | the site has no policy yet (`recommend.go` 60-62): the site declares it later. Keyed. J3 |
| `origin-unknown` | RETRYABLE | 422 | the location book (`LocationBook`, fed by facility-layout events) has no geometry or no site for the origin (`recommend.go` 109-116, `Ranker.Origin`); the layout event may arrive later. Keyed. J3 |
| `station-position-unknown` | RETRYABLE | 422 | no `fromLocation` and no recorded position (`recommend.go` 100-104); the position is recorded by the station's first completion (`ApplyTaskCompleted`). Keyed. J3 |
| `internal-error` | RETRYABLE | 500 | a 5xx is never stored |
| `dependency-unavailable` | RETRYABLE | 503 | the `/readyz` answer, not keyed; a 5xx is never stored |
| `invalid-report-range` | FINAL | 400 | the query alone; separate reports binary, no keyed route |
| `invalid-report-granularity` | FINAL | 400 | the query alone; separate reports binary, no keyed route |
| `report-store-error` | RETRYABLE | 500 | a 5xx is never stored; separate reports binary |

No problem type is an OUTCOME. The MCP server and the A2A agent are read-only (ADR 0007, ADR 0009, ADR 0011): no
other surface stores answers. No writer bypasses the shared error writer: every `problem+json` and `WriteHeader`
of the handlers goes through `writeProblem`.

### Judgement calls (most consequential first)

- **J3 `policy-not-declared`, `origin-unknown`, `station-position-unknown` RETRYABLE.** They are the three refusals a
  keyed request can actually get, and each depends on a fact another request or an event delivers (the policy
  `PUT`, a facility-layout event, a fulfillment-execution completion). Before this change a replay pinned the first
  refusal for as long as the key lived. `origin-unknown` also fires when the origin names a location nobody will
  ever register: that costs one more execution per retry. The cause is not distinguishable from the request, so the
  safe class stands.
- **J2 `version-mismatch` RETRYABLE.** The phase-0 scan read it as FINAL ("versions only move forward"). The code
  shows two causes under one slug: a stale `If-Match` (final) and an `If-Match` sent before the first declaration
  (passes later). A mixed slug is RETRYABLE; splitting it is a recorded follow-up. Not on a keyed route today.
- **J1 the three 404s RETRYABLE.** `hint-not-found` and `station-not-found` name facts created by event consumers,
  not ids this service mints for the caller, and `policy-not-found` is created by another request. Not keyed today.
- **J4 `concurrent-modification` is RETRYABLE but unreachable on a keyed route.** `Recommend` writes nothing, so the
  integration proof uses real keyed RETRYABLEs (J3) and the class itself is covered by the template over both the
  in-memory and the Postgres store.
- **J5 the `idempotency-key-*` types and the reports types are classified although the middleware answers the
  former itself and never stores them:** the class records that they are a function of the request.

### What the proofs cover

- `idempotency_replay_classes_test.go` is `templates/idempotency/replay_classes_test.go.tmpl` rendered verbatim
  (`{{PKG}}`=`http`, route `/recommendations`, `idempotency-key-reused`) over the real middleware, with the wiring
  of two stores: in memory (unit job) and a real Postgres (`..._wiring_integration_test.go`, integration job). Its
  catalogue is the production one plus a synthetic OUTCOME (`export_test.go`), the only way to exercise the OUTCOME
  mechanism while no real problem type is one: the write commits with the key row, three replays record nothing
  new.
- `cmd/api/idempotency_replay_classes_integration_test.go` drives the real router over a real Postgres: the
  RETRYABLE `policy-not-declared` leaves no row, its key retries after the declaration and the success is then
  stored and replayed; the RETRYABLE `station-position-unknown` is released until the station completes a task and a
  different body under the released key is a new request; the FINAL `invalid-station-id` is stored and replayed.
  `concurrent-modification` cannot be provoked end to end on the keyed route (no version-guarded write).
- Amendment 1: `idempotency_inflight_test.go` (a blind first Lookup stands for the copy that passed it while the
  original was in flight: a RETRYABLE or 5xx re-run replays the stored 200, another body is
  `idempotency-key-reused`; the re-read honours the window at exactly 24 h and not 1 µs later; a failed re-read is
  logged at WARN); the template's `TestConcurrentDuplicateReplaysTheOriginalsStoredAnswer` over both wirings;
  `idempotency_window_test.go` and `cmd/api` `TestPostgresAStoredAnswerReplaysOnlyWithinThe24HourWindow` (the window
  over the memory and the Postgres store; the repository contract pins both stores); `cmd/api`
  `TestPostgresAnInFlightDuplicateReplaysTheOriginalsAnswer` (the real route, above).

## Consequences

- A refusal marked RETRYABLE is no longer replayed: a retry under the same key executes again. `apis/openapi.yaml`
  says which refusals are replayed and which release the key.
- ADR 0010's "stored 2xx or 4xx" and ADR 0005's "2xx and 4xx only" are superseded by the table above (dated pointers
  added there; their Decision text is untouched).
- **Callers.** This service has no outbound caller that derives a key from an id, and no MCP or A2A write tool with
  its own store. A released refusal means the same key may be tried again by whoever sent it, nothing more. No
  consumer-side or client-side retry is added here; a person-driven caller surfaces the refusal.
- Follow-ups, not done here: split `version-mismatch` into a request-only and a state-dependent problem type;
  nothing purges `idempotency_keys` (runbook, "Nothing deletes rows"; the age check on replay is Amendment 1); the
  `idempotency_keys` migration comment still reads "Only 2xx and 4xx answers are stored" (a migration is not edited).
