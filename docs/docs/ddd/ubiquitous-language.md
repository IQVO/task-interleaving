---
id: ubiquitous-language
title: Ubiquitous language
sidebar_label: Ubiquitous language
sidebar_position: 20
---

# Ubiquitous language

Every term of the context with the code identifier that implements it. Use these
exact names in code, tests, ADRs and conversation. Terms marked *(designed)* have no
code on `develop` yet.

| Term | Meaning | Code or contract |
| --- | --- | --- |
| **Station** | The place an operator works at. Dispatch in this fleet is per station (the claim carries a `station_id`), so hints are per station and the associate is not an input. | `stationtrip.StationID` |
| **StationTrip** | The aggregate, one per station: where the operator stands and the hint issued there. | `stationtrip.StationTrip` |
| **Position** | The site plus the end location (with geometry when known) of the last completed task. | `stationtrip.Position`, `StationTrip.Position()` |
| **Completion** | A task finished at a station at an instant. Only a strictly newer one moves the position. | `StationTrip.RecordCompletion` |
| **Hint** | The short ranked list of candidate tasks issued to a station after a completion. Advisory, expiring, never an assignment. | `stationtrip.Hint`, `HintID` (`hint-` + suffix) |
| **Hint state** | `Active`, `Consumed`, `Expired`, `Superseded`. The last three are terminal. | `stationtrip.HintState`, `StateActive`, `StateConsumed`, `StateExpired`, `StateSuperseded` |
| **Candidate** | One ranked entry of a hint: `Rank` from 1, `TaskID`, `TaskType`, `Location`, `DetourM`. | `recommend.Candidate` |
| **Detour** | Distance in metres from the station's origin to the candidate's first location (`FromLocation`), rounded to 0.1 m. `ToLocation` never enters the ranking. | `Candidate.DetourM`, `travel.Round` |
| **Origin** | Where the station stands now; the point distances are measured from. Without known geometry no hint can be computed. | `recommend.Input.Origin`, `ErrOriginUnknown` |
| **Open-task pool** | The event-fed local copy of the tasks still open, with where each starts and ends. An input of the recommender, not aggregate state. | `[]recommend.OpenTask`; copy tables *(designed)* |
| **Priority band** | An opaque string equal across tasks that fulfillment-execution orders as peers. Carried, never interpreted here. | `TaskAvailable.priority_band` *(contract)* |
| **Tie-break** | How fulfillment-execution may use a hint: only inside one priority band, fail-open. | ADR 0001 *(consumer change P3 pending)* |
| **InterleavingPolicy** | The per-site settings, versioned, one per site. | `policy.Policy` |
| **Inert (site)** | A site that never declared a policy: no hint is issued for it. | ADR 0002; `404 policy-not-found` *(contract)* |
| **Settings** | The values an operator declares: max detour, max candidates, hint TTL, eligible task types, vertical weight. | `policy.Settings` |
| **Max detour** | Largest detour in metres a candidate may have; a detour equal to the limit qualifies. | `Settings.MaxDetourM` |
| **Max candidates** | How many candidates a hint lists at most, 1 to 5. | `Settings.MaxCandidates` |
| **Hint TTL** | How long a hint stays valid; `expires_at = issued_at + TTL`. | `Settings.HintTTL`, `hintTtlSeconds` on REST |
| **Eligible task types** | The non-empty set of task types a hint may point at. | `Settings.EligibleTaskTypes`, `Policy.Eligible` |
| **Task type** | `PICK`, `PACK`, `REBIN`, `SLAM`, `RELOCATE`, `REPLENISH`, as fulfillment-execution names them. | `policy.TaskType` |
| **Vertical weight** | The weight of height difference in the travel model; REST default 1, at least 0. | `Settings.VerticalWeight` |
| **Site** | The fleet's site code; a location's site is its zone's site. | `policy.SiteID` |
| **Travel model** | The replaceable port that measures distance between two places. | `travel.Model` |
| **rectilinear-v1** | The v1 model: `\|dx\| + \|dy\| + verticalWeight * \|dz\|` in metres, rounded to 0.1 m. | `travel.Rectilinear`, `travel.RectilinearV1` |
| **Place** | A location code with its geometry, when known. | `travel.Place` |
| **Point** | A position in the site's local frame, in metres (`X`, `Y`, `Z`). | `travel.Point` |
| **Unknown distance** | The answer for a place without usable geometry. Never 0, never a candidate. | `Place.Known()`, `Model.Distance` second result |
| **Recommender** | The pure function that ranks the pool: same input in any pool order, same output. | `recommend.Recommend` |
| **Consume (a hint)** | The hinted station claims one of its candidates while it is unexpired. | `StationTrip.RecordClaim`, event `HintConsumed` |
| **Expire (a hint)** | An Active hint reaches `expires_at`. | `StationTrip.Expire`, event `HintExpired` |
| **Supersede (a hint)** | A newer hint replaces the Active one. Raises no event of its own. | `StationTrip.IssueHint`, `HintIssued.SupersededHintID` |
| **Simulation** | A read-only run of the recommender, optionally from a what-if `fromLocation`; issues nothing. | `POST /recommendations` *(contract)* |
| **Fail-open** | An absent, expired or unknown hint means fulfillment-execution just proceeds with `claimNext`. | ADR 0001 |
| **Domain event** | A fact raised by the aggregate, not a wire message. | `stationtrip.Event`, `HintIssued`, `HintConsumed`, `HintExpired` |
| **Outbox** | The transactional table the events are written to with the aggregate; a relay drains it to Kafka. | ADR 0004 *(designed)* |
| **Local copy** | A durable table fed by a consumer (pool, claims, geometry), rebuilt by replaying its topic. | ADR 0003 *(designed)* |

## Words not to use

| Do not say | Say | Why |
| --- | --- | --- |
| assign, dispatch, command | hint, recommend | The context never assigns; `claimNext` is the only claim path. |
| worker, associate, picker (as an input) | station | Hints are per station. |
| distance 0 for missing geometry | unknown distance | Unknown is never treated as zero. |
| route, path (as the model) | rectilinear travel model | v1 has no aisle or cross-aisle routing. |
