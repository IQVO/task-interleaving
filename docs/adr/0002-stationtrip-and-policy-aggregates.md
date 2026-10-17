# ADR 0002: the StationTrip and InterleavingPolicy aggregates, the recommender and the policy ports

## Status

Accepted (2026-10-17).

## Context

ADR 0001 makes task-interleaving an advisor of next-task hints. This ADR fixes
the aggregates that hold the state, the rules that make a hint valid, the pure
recommender that computes one, and the ports behind which the replaceable parts
(the travel model) sit. The open-task pool, the claims and the location
geometry are event-fed read models (ADR 0003), not aggregate state.

## Decision

### Two aggregates

**InterleavingPolicy** (package `policy`, id = site id, one per site): the
settings of one site, versioned.

| Field | Rule |
|---|---|
| `SiteID` | a token: 1 to 64 characters, no whitespace, control characters or `/` |
| `MaxDetourM` | finite and greater than 0, metres |
| `MaxCandidates` | 1 to 5 |
| `HintTTL` | greater than 0 (REST carries seconds) |
| `EligibleTaskTypes` | non-empty set of `PICK`, `PACK`, `REBIN`, `SLAM`, `RELOCATE`, `REPLENISH`; a duplicate collapses, an unknown value is refused |
| `VerticalWeight` | finite and at least 0 (REST default 1 when omitted) |
| `Version` | 1 at declaration, +1 per `WithChanges` |

A site that never declared a policy is **inert**: no hint is issued. The
defaults are deliberately "off": no input is invented for a site nobody
configured.

**StationTrip** (package `stationtrip`, id = station id, one per station): where
the operator stands and the hint issued there.

- `position`: the site, the location code and, when geometry is known, the
  point of the end of the last completed task.
- `lastCompleted`: task id and instant.
- `hint`: the most recent hint (Active or terminal): id, `after_task_id`, origin
  location, `issued_at`, `expires_at`, policy version, the ranked candidates and
  its state.
- `version`: 0 for a new trip, +1 per accepted transition.

Why a station and not an associate: dispatch in this fleet is per station
(fulfillment-execution ADR 0002), the claim carries a `station_id`, and the hint
is consumed by that same key.

### Hint states

```
Active --RecordClaim of a candidate--> Consumed
   |
   +----Expire at/after expires_at---> Expired
   |
   +----IssueHint (newer hint)-------> Superseded
```

`Consumed`, `Expired` and `Superseded` are terminal: no transition leaves them.

### Invariants of StationTrip

1. **Position only moves forward in time.** `RecordCompletion` ignores a
   completion whose instant is not strictly newer than the recorded one (no
   event, no version change). A zero instant, a blank task id or an invalid
   position is an error.
2. **At most one Active hint per station.** `IssueHint` marks any Active hint
   `Superseded` and the new one `Active`. A completion that yields no candidate
   issues no hint and leaves an older Active hint to expire at its TTL
   (bounded staleness; the hint is advisory and fail-open).
3. **A hint is valid.** Candidates are distinct by task id; the policy belongs to
   the station's site; each candidate's task type is eligible and its detour is
   at most `MaxDetourM`; `1 <= len <= MaxCandidates`; ranks are 1..n in the
   order of `detour_m` ascending then `task_id` ascending. An empty candidate
   set is refused: it issues **no hint**. A station with no recorded completion
   cannot be hinted.
4. **A hint expires.** `expires_at = now + HintTTL` at issue. `Expire(now)` marks
   an Active hint `Expired` at or after `expires_at`; before it, nothing changes.
5. **Consumption.** `RecordClaim(taskID, now)` marks the Active hint `Consumed`
   only when the task is one of its candidates and the hint is unexpired
   (`now` strictly before `expires_at`); any other claim changes nothing and
   leaves the hint Active until it expires or is superseded.

"Currently in the open-task pool" (part of the plan's invariant 3) is a property
of the recommender's input, not of the aggregate: the recommender only ever
ranks tasks of the pool it is given, and the aggregate re-checks the shape and
bounds of what it is handed.

The aggregate raises facts, not wire messages: `HintIssued`, `HintConsumed`,
`HintExpired`, available from `DomainEvents()`, carrying the station, site,
version after the change and the occurrence time. The domain imports nothing
from CloudEvents or Kafka; `HintIssued` also names the hint it superseded, if
any, so persistence can close that row. The use case writes the events to the
outbox in the same transaction as the aggregate (ADR 0004).

### The pure recommender (package `recommend`)

`Recommend(Input) ([]Candidate, error)`, no I/O, no clock, no randomness.

**Input.** The station's `Site`, the `Origin` (location and optional point), the
open-task `Pool` (`ID`, `Type`, `Site`, `FromLocation`, optional `FromPoint`,
`ToLocation`), the `Policy` and the id of the task just completed.

**Rules (FINAL for v1).** A pool task is a candidate when ALL hold:

1. it is of the same site as the station;
2. its type is in the policy's `EligibleTaskTypes`;
3. it is not the task just completed;
4. its geometry is known (a task with no `FromPoint` is skipped, never treated
   as distance 0);
5. its detour is at most `MaxDetourM` (a detour equal to the limit qualifies).

Candidates are ranked by `DetourM` ascending, then `TaskID` ascending, distinct
by task id, cut to `MaxCandidates` and numbered from 1. An empty result means NO
hint. The result is independent of the order of the pool. An origin without
known geometry is an error (`ErrOriginUnknown`): there is nothing to measure
from.

The detour is the distance from the origin to the task's `FromLocation` (where
the operator must go first); `ToLocation` is carried for the pool copy (it is
where a completed task leaves the operator, which becomes the next origin) and
does not enter the ranking.

### Policy ports

`travel.Model` is the port behind which the travel model sits:
`Distance(from, to Place) (metres, known)`. The v1 implementation is
`rectilinear-v1`:

```
d = |x1-x2| + |y1-y2| + VerticalWeight * |z1-z2|     (metres, rounded to 0.1 m)
```

A place without known geometry (no point, or a non-finite coordinate) yields
"unknown", never 0. Aisle geometry, cross-aisle routes and fixed structures
(facility-layout ADR 0017) are PLANNED refinements: a new model version behind
the same port with its own ADR, selected per policy. The recommender builds the
model from the policy's `VerticalWeight` unless the caller passes one.

### Human decision and read-only simulation

A policy change is an explicit operator command with optimistic concurrency
(`If-Match`). `POST /recommendations` runs the same recommender without issuing
anything: no state change, no event.

## Consequences

- Every rule above lives in `internal/domain/policy`, `travel`, `recommend` and
  `stationtrip`; all four packages sit under the mutation gate.
- The recommender is cheap to test exhaustively (permutations of the pool, ties,
  boundaries) because it is pure.
- A completion that leaves no candidate does not retract an older hint: the hint
  expires on its TTL. Retracting it would need an event for the retraction; that
  is a later ADR if the floor needs it.
- Rank order is stable across ties because `task_id` is the tie-break: a hint is
  reproducible from the same pool and position.
