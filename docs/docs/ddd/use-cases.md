---
id: use-cases
title: Use cases
sidebar_label: Use cases
sidebar_position: 1
---

# Use cases

:::caution Application layer: pending the service PR
`develop` has no `internal/application`. There are no use-case types, no
transactions and no outbox writes yet. This page therefore documents the **domain
operations** that exist as code in `internal/domain` and that every future use case
will call, plus the trigger each one is *designed* to have
([ADR 0001](/docs/adr/0001-task-interleaving-bounded-context),
[ADR 0003](/docs/adr/0003-local-copies-and-consumed-contracts),
`.claude/rules/rest-api.md`). The intended use-case names come from that rule file;
none is in code. When the service merges, this page grows one row per use case.
:::

Hints are issued by event consumers, never by a REST call, and nothing here assigns
work to a worker.

## Domain operations

| # | Domain operation (code) | Designed trigger | Inputs | Invariants checked | Events raised |
| --- | --- | --- | --- | --- | --- |
| 1 | `policy.New(site, Settings)` and `Policy.WithChanges(Settings)` (intended use case `DeclareInterleavingPolicy`) | REST `PUT /sites/{siteId}/policy` | `SiteID`; `Settings{MaxDetourM, MaxCandidates, HintTTL, EligibleTaskTypes, VerticalWeight}` | site id is a token of 1 to 64 runes without whitespace, control characters or `/`; `MaxDetourM` finite and greater than 0; `MaxCandidates` 1 to 5; `HintTTL` greater than 0; `EligibleTaskTypes` a non-empty set of `PICK`, `PACK`, `REBIN`, `SLAM`, `RELOCATE`, `REPLENISH` (sorted, duplicates collapse); `VerticalWeight` finite and at least 0. Version is 1 on `New`, +1 per `WithChanges`, which replaces all settings. | none from the package. On the wire `InterleavingPolicyDeclared` (analytics topic only) is built by the application layer when it exists. |
| 2 | `policy.Restore(site, Settings, version)` | loading a persisted row | the stored values and version | every invariant above, plus `version >= 1`, so a corrupt row can never become a live aggregate | none |
| 3 | `stationtrip.New(StationID)` | first sight of a station | station id | token rules (1 to 64 runes, no whitespace, control characters or `/`) | none |
| 4 | `StationTrip.RecordCompletion(task, at, Position)` | Kafka `TaskCompleted` from fulfillment-execution (position = the completed task's `to_location` and `site_id` from the pool copy) | task id, instant, `Position{Site, Place}` | task id not blank; instant not zero; site is a valid token; location not blank. A completion **not strictly newer** than the recorded one returns `false`, changes nothing and raises nothing (position only moves forward in time). | none; version +1 when accepted |
| 5 | `recommend.Recommend(Input)` (intended use cases `RecommendTasks` and the compute step of the consumer) | the consumer effect that follows a recorded completion, and REST `POST /recommendations` as a read-only simulation | station site, origin `Place`, open-task `Pool`, `Policy`, completed task id, `travel.Model` | the model is not nil (`ErrNoModel`); the origin has known geometry (`ErrOriginUnknown`). A pool task is a candidate only if it is of the station's site, its type is eligible, it is not the completed task, its geometry is known and its detour is at most `MaxDetourM` (a detour equal to the limit qualifies). Ranked by detour then task id, distinct by task id, cut to `MaxCandidates`, numbered from 1. An empty result means *no hint*. | none: pure, no I/O, no clock, no randomness |
| 6 | `StationTrip.IssueHint(id, candidates, Policy, now)` | the consumer effect, with the output of operation 5 | hint id (`hint-` plus a suffix), candidates, the site's policy, instant | the station has a recorded position (`ErrNoPosition`); hint id valid (`ErrInvalidHintID`); `now` not zero (`ErrInvalidTime`); policy belongs to the station's site (`ErrSiteMismatch`); 1 to `MaxCandidates` candidates (`ErrNoCandidates`, `ErrTooManyCandidates`); distinct task ids (`ErrDuplicateCandidate`); eligible types (`ErrCandidateNotEligible`); detour finite, not negative and at most the limit (`ErrCandidateDetour`); ranks 1..n in detour-then-id order (`ErrCandidateRanking`). Any Active hint becomes `Superseded`; the new hint is `Active` and expires at `now + HintTTL`. | `HintIssued` (names the superseded hint, if any); wire `InterleavingHintIssued` on the events and analytics topics |
| 7 | `StationTrip.RecordClaim(task, now)` | Kafka `TaskClaimed` (pending producer change P2) | task id, instant | consumes only if there is an Active hint, `now` is not before `IssuedAt`, `now` is strictly before `ExpiresAt`, and the task is one of its candidates; otherwise returns `false` and changes nothing | `HintConsumed` (rank and detour of the claimed candidate); wire `InterleavingHintConsumed`, analytics only |
| 8 | `StationTrip.Expire(now)` | not fixed by an ADR or by the contract; the OpenAPI text says "the clock marks it Expired" | instant | only an Active hint expires, and only when `now` is at or after `ExpiresAt`; `Consumed`, `Expired` and `Superseded` are terminal | `HintExpired`; wire `InterleavingHintExpired`, analytics only |
| 9 | `StationTrip.DomainEvents()` and `ClearDomainEvents()` | the use case, after saving | none | `DomainEvents` returns an ordered copy | none (hands the raised facts to the outbox writer) |
| 10 | `travel.Rectilinear.Distance(from, to)` (via `travel.Model`) | called by operation 5 | two `Place`s | unknown if either place has no point or a non-finite coordinate: never 0. Otherwise `\|dx\| + \|dy\| + verticalWeight * \|dz\|`, rounded to 0.1 m | none |

## Not domain operations (all pending the service PR)

These are in the contract and in `.claude/rules/rest-api.md` but have **no domain
code**; they are projections or plumbing the application layer will own:

- `GetInterleavingPolicy`, `ListHints`, `GetHint`, `GetStation`: read-only queries
  over stored aggregates, using only accessors such as `StationTrip.Hint`,
  `ActiveHint`, `Position`, `LastCompleted`, `Policy.Settings`.
- The consumer effects that maintain the open-task pool (`TaskAvailable` upsert,
  `TaskClaimed` removal), the claims and the location geometry copy
  (`ZoneRegistered`, `LocationSlotRegistered`, `LocationSlotDecommissioned`,
  `LocationGeometryUpdated`): event-fed read models, not aggregate state.
- The `Idempotency-Key` middleware and the outbox relay.

## Domain errors and the problem slugs they will map to

Domain errors carry the comment "The application layer maps them to problem slugs"
(`policy.go`, `values.go`). The mapping is not in code on `develop`. The right-hand
column is the closest slug in `apis/openapi.yaml` **by name**, an inference to be
confirmed by the real mapping.

| Domain error | Closest OpenAPI problem slug |
| --- | --- |
| `policy.ErrInvalidSiteID` | `invalid-site-id` (400) |
| `policy.ErrInvalidMaxDetour`, `ErrInvalidCandidates`, `ErrInvalidHintTTL`, `ErrInvalidWeight` | `invalid-policy` (400) |
| `policy.ErrInvalidTaskTypes` | `invalid-task-type` (400) |
| `policy.ErrInvalidVersion` | none: only reachable through `Restore` of a corrupt row |
| `stationtrip.ErrInvalidStationID` | `invalid-station-id` (400) |
| `stationtrip.ErrInvalidHintID` | `invalid-hint-id` (400) |
| `recommend.ErrOriginUnknown` | `origin-unknown` (422) |
| `stationtrip.ErrNoPosition` | `station-position-unknown` (422) when simulating without `fromLocation` |
| `recommend.ErrNoModel` | a programming error: `internal-error` (500) |
| `stationtrip.ErrNoCandidates`, `ErrTooManyCandidates`, `ErrDuplicateCandidate`, `ErrCandidateNotEligible`, `ErrCandidateDetour`, `ErrCandidateRanking`, `ErrSiteMismatch`, `ErrInvalidTask`, `ErrInvalidTime`, `ErrInvalidPosition` | no REST route issues a hint, so none surfaces over REST; they guard the consumer path |

The 404, 409 and 412 slugs (`policy-not-found`, `hint-not-found`,
`station-not-found`, `concurrent-modification`, `version-mismatch`) and the
idempotency slugs belong to storage and HTTP concerns, not to the domain.

## Where the code differs from the ADR text

`StationTrip.RecordClaim` also refuses a claim whose instant is *before* the
hint's `IssuedAt` (`trip.go`, `now.Before(s.hint.IssuedAt)`). ADR 0002 invariant 5
states only the expiry bound. The code is stricter; the test
`TestRecordClaimTimeWindow` pins it ("one nanosecond before issue").
