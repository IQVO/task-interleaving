# Domain model: ubiquitous language, aggregates, events, use cases

Source of truth: `docs/adr/0001-task-interleaving-bounded-context.md`,
`docs/adr/0002-stationtrip-and-policy-aggregates.md` and the packages under
`internal/domain/`. Task-interleaving is an **advisor**: it ranks open tasks near
where a station just finished and publishes a hint; it never assigns work.

## Ubiquitous Language (use these exact names)

- **InterleavingPolicy** — the per-site settings, versioned; one per site. A site
  with no policy is **inert**: no hint is issued for it.
- **StationTrip** — where one station stands and the hint issued there; one per
  station (dispatch in this fleet is per station).
- **Hint** — the short ranked list of candidate tasks issued to a station after a
  completion. Advisory only; states `Active`, `Consumed`, `Expired`, `Superseded`.
- **Candidate** — one ranked entry of a hint: `Rank` (from 1), `TaskID`,
  `TaskType`, `Location`, `DetourM`.
- **Detour** — distance in metres from the station's origin to the candidate's
  `FromLocation`, rounded to 0.1 m. `ToLocation` never enters the ranking.
- **Open-task pool** — the event-fed local copy of open tasks (`OpenTask`); an
  input of the recommender, not aggregate state.
- **Travel model** — the replaceable port that measures distance between two
  places; v1 is `rectilinear-v1`.
- **Inert / fail-open** — no policy means no hint; a missing or expired hint means
  fulfillment-execution just proceeds with `claimNext`.

## Aggregates

- **InterleavingPolicy** (`internal/domain/policy`): `Policy`, built by
  `policy.New` / `policy.Restore`, changed only through `Policy.WithChanges`
  (version +1). Value objects `SiteID` (token, 1..64 chars, no whitespace or `/`),
  `TaskType` (`PICK`, `PACK`, `REBIN`, `SLAM`, `RELOCATE`, `REPLENISH`) and
  `Settings`. Invariants: `MaxDetourM` finite and > 0; `MaxCandidates` 1..5;
  `HintTTL` > 0; `EligibleTaskTypes` non-empty and known (duplicates collapse);
  `VerticalWeight` finite and >= 0; `Version` >= 1. Errors: `ErrInvalidSiteID`,
  `ErrInvalidMaxDetour`, `ErrInvalidCandidates`, `ErrInvalidHintTTL`,
  `ErrInvalidTaskTypes`, `ErrInvalidWeight`, `ErrInvalidVersion`.
- **StationTrip** (`internal/domain/stationtrip`): `StationTrip`, id `StationID`,
  built by `stationtrip.New`. Commands `RecordCompletion`, `IssueHint`,
  `RecordClaim`, `Expire`. Invariants (ADR 0002):
  1. position only moves forward in time: an older or equal completion is ignored
     (no event, no version change); a zero instant, blank task id or invalid
     position is an error;
  2. at most one Active hint per station: `IssueHint` supersedes the previous one;
  3. a hint is valid: distinct candidates, the policy is the station's site's,
     each task type eligible and each detour <= `MaxDetourM`, `1 <= len <=
     MaxCandidates`, ranked 1..n by detour then task id; an empty set issues no
     hint (`ErrNoCandidates`); no recorded completion means `ErrNoPosition`;
  4. a hint expires at `expires_at = now + HintTTL`; `Expire(now)` changes nothing
     before it;
  5. `RecordClaim` consumes the Active hint only for one of its candidates and
     while unexpired; any other claim changes nothing.
  `Consumed`, `Expired` and `Superseded` (`HintState`) are terminal.
- **Recommender** (`internal/domain/recommend`): not an aggregate but pure domain
  logic. `Recommend(Input)` has no I/O, no clock, no randomness; a pool task is a
  candidate when it is of the station's site, its type is eligible, it is not the
  task just completed, its geometry is known (never treated as distance 0) and
  its detour is <= `MaxDetourM`. Ranking: `DetourM` then `TaskID`, cut to
  `MaxCandidates`. An origin without geometry is `ErrOriginUnknown`; a nil model
  is `ErrNoModel`.

## Policy ports

- `travel.Model` (`internal/domain/travel`): `Distance(from, to Place)` returns
  metres and whether the distance is known. `travel.Rectilinear` (`rectilinear-v1`,
  `d = |dx| + |dy| + VerticalWeight*|dz|`, `travel.Round`) is the only
  implementation; `recommend.RectilinearFor(policy)` builds it. A `Place` without
  geometry is unknown, never 0. A new model version needs its own ADR.

## Domain events

Raised by `StationTrip` (`StationTrip.DomainEvents()`, cleared with
`ClearDomainEvents()`); each embeds `Header` (station, site, version after the
change, occurred-at). They are facts, not wire messages.

- `HintIssued` — a hint became Active; names the `SupersededHintID` it replaced.
- `HintConsumed` — the hinted station claimed a candidate; carries its `Rank` and
  `DetourM`.
- `HintExpired` — an Active hint reached its `ExpiresAt`.

On the wire they are `InterleavingHintIssued`, `InterleavingHintConsumed` and
`InterleavingHintExpired` (see `.claude/rules/integration-events.md`).
`InterleavingPolicyDeclared` is also published, but package `policy` has no event
type yet: the application layer builds it when a policy is declared.

## Key use cases (the application layer)

The application layer exists in `internal/application/usecases`, one use case per
REST route and consumer effect (see `.claude/rules/rest-api.md`): declare / get the
interleaving policy, list / get hints, get a station, simulate a recommendation
(`Recommend`), the consumer effects `ApplyTaskAvailable`, `ApplyTaskClaimed`,
`ApplyTaskCompleted` and the layout appliers in `apply_layout.go`, and the expiry
sweeper `ExpireHints`. Each runs in one UnitOfWork with the outbox rows of the
events it raises.
