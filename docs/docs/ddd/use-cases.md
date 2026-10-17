---
id: use-cases
title: Use cases
sidebar_label: Use cases
sidebar_position: 1
---

# Use cases

The application layer is `internal/application/usecases`. Every write runs in
**one** `ports.UnitOfWork`: load, apply the aggregate command, save guarded by the
version that was loaded, and hand the raised facts to the outbox
(`ports.EventSink`), so the aggregate, its events and, for consumers, the claim of
the CloudEvents id commit or roll back together. Reads use the event-fed local
copies (`ports.TaskPool`, `ports.LocationBook`); nothing here calls a sibling
context.

Hints are issued by event consumers, never by a REST call, and nothing here assigns
work to a worker. There are **14 use cases**: 6 REST queries and commands, 7
Kafka-driven consumer effects and 1 scheduler.

## Use cases

Triggers: REST routes are those of `apis/openapi.yaml`; Kafka types are the full
CloudEvents `type` of the producer (`com.warehouse.wes.fulfillment-execution.task.*`
or `com.warehouse.wms.facility-layout.*`); the scheduler is the hint-expiry worker.

| # | Use case (file) | Trigger | Inputs | Invariants checked | Events raised |
| --- | --- | --- | --- | --- | --- |
| 1 | `DeclarePolicy` (`declare_policy.go`) | REST `PUT /sites/{siteId}/policy` | site id; `maxDetourM`, `maxCandidates`, `hintTtlSeconds`, `eligibleTaskTypes`, optional `verticalWeight` (default 1.0); optional `If-Match` version | site id token (1 to 64 runes, no whitespace, control characters or `/`); `maxDetourM` finite and above 0; `maxCandidates` 1 to 5; TTL above 0 (and at most 2147483647 s over REST); non-empty eligible set of `PICK, PACK, REBIN, SLAM, RELOCATE, REPLENISH`; weight finite and at least 0. First declaration: no `If-Match` (version 1). Replacement: `If-Match` must equal the current version (`ErrVersionMismatch`, 412); a lost write race is `ErrConcurrentModification` (409). | `InterleavingPolicyDeclared` (analytics topic only) |
| 2 | `GetPolicy` (`get_policy.go`) | REST `GET /sites/{siteId}/policy` | site id | valid site id; no row = `ErrPolicyNotFound` (404, the site is inert) | none |
| 3 | `ListHints` (`list_hints.go`) | REST `GET /hints` | optional `stationId`, `siteId`, `state`, `cursor`, `limit` | `limit` 1 to 500 (default 100); `state` one of `Active, Consumed, Expired, Superseded`; cursor must be one this API issued; newest first, keyset paged | none |
| 4 | `GetHint` (`get_hint.go`) | REST `GET /hints/{hintId}` | hint id | id starts with `hint-`, no whitespace, control characters or `/`; unknown id is `ErrHintNotFound` (404) | none |
| 5 | `GetStation` (`get_station.go`) | REST `GET /stations/{stationId}` | station id | valid station id; a station with no recorded completion is `ErrStationNotFound` (404) | none |
| 6 | `Recommend` (`recommend.go`) | REST `POST /recommendations` (needs `Idempotency-Key` in Postgres mode) | `stationId`; optional `fromLocation` | the origin is `fromLocation`, else the station's recorded position (`ErrStationPositionUnknown`, 422); the origin has known geometry and a site (`ErrOriginUnknown`, 422); the site has a policy (`ErrPolicyNotDeclared`, 422). Read-only: issues nothing, publishes nothing. | none |
| 7 | `ApplyTaskAvailable` (`apply_task_available.go`) | Kafka `...task.TaskAvailable` | `task_id`, `task_type`, `site_id`, `from_location`, `to_location`, `required_capabilities`, `priority_band` | `task_id`, `task_type`, `from_location` not blank; `site_id` a valid site id (else `ErrInvalidEvent`, dead-lettered at once). A task already COMPLETED never re-enters the pool (`stale`). Otherwise upserted as AVAILABLE; a re-entry forgets the old claimer. | none |
| 8 | `ApplyTaskClaimed` (`apply_task_claimed.go`) | Kafka `...task.TaskClaimed` | `task_id`, `task_type`, `station_id`, `claimed_at` (falls back to the CloudEvents `time`) | `task_id` not blank and a valid station id. COMPLETED tasks are `stale`. The task is marked CLAIMED by the station (removed from the candidates; a never-seen task is recorded minimally). If the station has a trip and `StationTrip.RecordClaim` accepts the claim (Active hint, inside its window, task is a candidate) the hint is consumed. | `InterleavingHintConsumed` (analytics) when a hint is consumed |
| 9 | `ApplyTaskCompleted` (`apply_task_completed.go`) | Kafka `...task.TaskCompleted` | `task_id`, `station_id` (else the claimer in the pool copy), CloudEvents `time` | `task_id` not blank. An already COMPLETED task is `stale`. The task is marked COMPLETED. If the completer, the task's `to_location` or `site_id` is unknown the outcome is `ignored`. Otherwise the station's position moves to the end of the task (`RecordCompletion`: older or equal instants are `stale`), and, if the site has a policy, the origin has geometry and at least one candidate qualifies, a hint is issued; any earlier Active hint is `Superseded`. | `InterleavingHintIssued` (events and analytics topics); no event when no hint is issued |
| 10 | `ApplyZone` (`apply_layout.go`) | Kafka `...zone.ZoneRegistered` | `zoneId`, `siteCode`, `zoneCode` | `zoneId` and `siteCode` not blank; upsert | none |
| 11 | `ApplyLocationRegistered` | Kafka `...locationslot.LocationSlotRegistered` | `locationCode`, `zoneId` | both not blank; upsert (a late registration cannot undo a decommission) | none |
| 12 | `ApplyLocationDecommissioned` | Kafka `...locationslot.LocationSlotDecommissioned` | `locationCode` | not blank; the location is retired for good, its row and geometry stay for origins | none |
| 13 | `ApplyGeometryUpdated` | Kafka `...locationslot.LocationGeometryUpdated` | `locationCode`, `xM`, `yM`, `zM` | code not blank; all three coordinates present (consumer) and finite (use case); stored whether or not the location was registered yet | none |
| 14 | `ExpireHints` (`expire_hints.go`) | scheduler: `inbound/expiry` worker every `HINT_EXPIRY_INTERVAL` (default 5 s; the first sweep runs at start-up) | the clock | up to 200 stations with an Active hint at or past `expires_at`, each in its own unit of work; only an Active hint expires; a lost race is skipped until the next sweep | `InterleavingHintExpired` (analytics) |

The consumer use cases (7 to 13) all go through `Intake`: the CloudEvents `id` is
claimed in `processed_events` first (consumer name `task-interleaving-fulfillment`
or `task-interleaving-layout`) and a repeat is the outcome `duplicate`. Outcomes:
`applied`, `duplicate`, `stale`, `ignored`. A blank `id` is `ErrInvalidEvent`.

Hint ids are minted by `ports.IDGenerator` (`memory.RandomIDs`), the clock by
`ports.Clock`; both are injected so the BDD suite can fake time.

## Domain operations behind them

The use cases call these operations; the invariants live here, not in the
adapters.

| Domain operation | Rule |
| --- | --- |
| `policy.New(site, Settings)` / `Policy.WithChanges(Settings)` | Settings validated as in use case 1; version 1 on `New`, +1 per `WithChanges`, which replaces all settings. |
| `policy.Restore(site, Settings, version)` | Loads a persisted row; every invariant plus `version >= 1`, so a corrupt row can never become a live aggregate. |
| `stationtrip.New(StationID)` | Token rules on the station id. |
| `StationTrip.RecordCompletion(task, at, Position)` | Task id not blank, instant not zero, site a valid token, location not blank. A completion **not strictly newer** than the recorded one returns `false`, changes nothing and raises nothing. |
| `recommend.Recommend(Input)` | Pure ranking: model not nil (`ErrNoModel`), origin geometry known (`ErrOriginUnknown`). A candidate is of the station's site, of an eligible type, not the completed task, with known geometry and a detour at most `MaxDetourM` (equal qualifies). Ranked by detour then task id, distinct, cut to `MaxCandidates`, numbered from 1. |
| `StationTrip.IssueHint(id, candidates, Policy, now)` | Position recorded (`ErrNoPosition`), valid hint id, `now` not zero, policy of the station's site (`ErrSiteMismatch`), 1 to `MaxCandidates` distinct eligible candidates within the detour limit ranked 1..n. Any Active hint becomes `Superseded`; the new hint is `Active` and expires at `now + HintTTL`. |
| `StationTrip.RecordClaim(task, now)` | Consumes only with an Active hint, `now` not before `IssuedAt`, `now` strictly before `ExpiresAt`, and the task one of its candidates; otherwise `false`, no change. |
| `StationTrip.Expire(now)` | Only an Active hint, only at or after `ExpiresAt`; `Consumed`, `Expired` and `Superseded` are terminal. |
| `travel.Rectilinear.Distance` | Unknown when either place has no point or a non-finite coordinate, never 0; else `\|dx\| + \|dy\| + verticalWeight * \|dz\|`, rounded to 0.1 m. |

## Errors and the problem types they become

The HTTP adapter maps each error in `internal/adapters/inbound/http/errors.go`;
the full table with remedies is in
[Troubleshooting](/docs/operations/troubleshooting#rest-problem-types).

| Error | Problem slug (status) |
| --- | --- |
| `policy.ErrInvalidSiteID` | `invalid-site-id` (400) |
| `ErrInvalidMaxDetour`, `ErrInvalidCandidates`, `ErrInvalidHintTTL`, `ErrInvalidWeight` | `invalid-policy` (400) |
| `policy.ErrInvalidTaskTypes` | `invalid-task-type` (400) |
| `stationtrip.ErrInvalidStationID` / `ErrInvalidHintID` | `invalid-station-id` / `invalid-hint-id` (400) |
| `usecases.ErrInvalidLimit` / `ErrInvalidCursor` / `ErrInvalidState` | `invalid-limit` / `invalid-cursor` / `invalid-state` (400) |
| `repository.ErrPolicyNotFound` / `ErrHintNotFound` / `ErrStationNotFound` | `policy-not-found` / `hint-not-found` / `station-not-found` (404) |
| `repository.ErrConcurrentModification` | `concurrent-modification` (409) |
| `usecases.ErrVersionMismatch` | `version-mismatch` (412) |
| `usecases.ErrPolicyNotDeclared`, `recommend.ErrOriginUnknown`, `usecases.ErrStationPositionUnknown` | `policy-not-declared`, `origin-unknown`, `station-position-unknown` (422) |
| anything else (including `recommend.ErrNoModel`) | `internal-error` (500), real error only in the log |

The stationtrip errors `ErrNoCandidates`, `ErrTooManyCandidates`,
`ErrDuplicateCandidate`, `ErrCandidateNotEligible`, `ErrCandidateDetour`,
`ErrCandidateRanking`, `ErrSiteMismatch`, `ErrInvalidTask`, `ErrInvalidTime` and
`ErrInvalidPosition` guard the consumer path; no REST route can raise them.
`ErrInvalidEvent` (consumer path) is the "can never be applied" signal that sends a
message to the dead-letter topic.

## Where the code differs from the ADR text

`StationTrip.RecordClaim` also refuses a claim whose instant is *before* the
hint's `IssuedAt` (`trip.go`, `now.Before(s.hint.IssuedAt)`). ADR 0002 invariant 5
states only the expiry bound. The code is stricter; the test
`TestRecordClaimTimeWindow` pins it ("one nanosecond before issue").
