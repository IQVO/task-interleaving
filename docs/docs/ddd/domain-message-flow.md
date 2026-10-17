---
id: domain-message-flow
title: Domain message flow
sidebar_label: Domain message flow
sidebar_position: 15
---

# Domain message flow

[Domain Message Flow Modelling](https://github.com/ddd-crew/domain-message-flow-modelling)
of the key scenarios: commands, events and queries as numbered messages between
collaborators. The domain steps (`policy`, `recommend`, `stationtrip`) exist as
code; the collaborators that carry the messages (REST handlers, consumers, outbox
relay) are **designed, not built**, and fulfillment-execution's side of flows 2 to 4
is partly pending (producer change P2, consumer change P3).

## Flow 1 — declare a policy

```mermaid
flowchart LR
    op(["Operator"])
    api["REST adapter (designed)"]
    pol["policy.Policy"]
    ob[("outbox (designed)")]
    an["analytics topic"]
    op -->|"1. cmd: PUT /sites/{siteId}/policy + If-Match"| api
    api -->|"2. policy.New or WithChanges"| pol
    pol -->|"3. Policy version n"| api
    api -->|"4. evt row: InterleavingPolicyDeclared"| ob
    ob ==>|"5. evt: analytics only"| an
    api -->|"6. 200 + ETag version"| op
```

Source: `internal/domain/policy/policy.go`, `apis/openapi.yaml`
(`declareInterleavingPolicy`), `apis/asyncapi.yaml`. Failure branches: invalid
settings 400 `invalid-policy` or `invalid-task-type`; stale or missing `If-Match`
on an existing policy 412 `version-mismatch`; lost race 409 `concurrent-modification`.

## Flow 2 — a station completes a task, a hint is issued

```mermaid
flowchart LR
    fe["fulfillment-execution"]
    cons["fulfillment consumer (designed)"]
    pool[("pool + geometry copies (designed)")]
    rec["recommend.Recommend"]
    trip["stationtrip.StationTrip"]
    ob[("outbox (designed)")]
    fe ==>|"1. evt: TaskCompleted (task_id, station_id)"| cons
    cons -->|"2. qry: end location and site of the task"| pool
    cons -->|"3. cmd: RecordCompletion"| trip
    cons -->|"4. qry: open tasks, origin geometry, policy"| pool
    cons -->|"5. Recommend"| rec
    rec -->|"6. ranked candidates, possibly none"| cons
    cons -->|"7. cmd: IssueHint (only if at least one)"| trip
    trip -->|"8. evt: HintIssued"| cons
    cons ==>|"9. evt rows: InterleavingHintIssued x2"| ob
    ob ==>|"10. evt: events + analytics topics"| fe
```

Source: `internal/domain/stationtrip/trip.go`, `internal/domain/recommend/recommend.go`,
ADR 0002, ADR 0003, ADR 0004. Branches:

- No candidate: step 7 does not happen; an older Active hint is **not** retracted
  and expires at its TTL.
- Origin without geometry: `ErrOriginUnknown`, no hint.
- A completion for a task the pool never saw: counted and skipped, not
  dead-lettered.
- A site with no policy: inert, no hint.
- The hint is advisory: fulfillment-execution may use it as a tie-break inside one
  priority band (consumer change P3, pending).

## Flow 3 — the hinted station claims a candidate

```mermaid
flowchart LR
    fe["fulfillment-execution"]
    cons["fulfillment consumer (designed)"]
    trip["stationtrip.StationTrip"]
    ob[("outbox (designed)")]
    an["analytics topic"]
    fe ==>|"1. evt: TaskClaimed (task_id, station_id, claimed_at) - pending P2"| cons
    cons -->|"2. cmd: RecordClaim(task, claimed_at)"| trip
    trip -->|"3. evt: HintConsumed (only for a candidate, unexpired)"| cons
    cons ==>|"4. evt row: InterleavingHintConsumed"| ob
    ob ==>|"5. evt: analytics only"| an
```

Source: `StationTrip.RecordClaim`, ADR 0003. A claim of a non-candidate, a claim after
expiry or a claim with no Active hint changes nothing and publishes nothing.

## Flow 4 — a hint expires

```mermaid
flowchart LR
    clk["caller with a clock (undecided)"]
    trip["stationtrip.StationTrip"]
    ob[("outbox (designed)")]
    an["analytics topic"]
    clk -->|"1. cmd: Expire(now)"| trip
    trip -->|"2. evt: HintExpired (at or after expires_at)"| clk
    clk ==>|"3. evt row: InterleavingHintExpired"| ob
    ob ==>|"4. evt: analytics only"| an
```

Source: `StationTrip.Expire`, `apis/openapi.yaml` ("the clock marks it Expired").
No ADR or contract names the component that calls `Expire`.

## Flow 5 — simulate a recommendation (query)

```mermaid
flowchart LR
    op(["Operator or agent"])
    api["REST adapter (designed)"]
    st[("station, pool, geometry, policy (designed)")]
    rec["recommend.Recommend"]
    op -->|"1. qry: POST /recommendations {stationId, fromLocation?}"| api
    api -->|"2. read position, site, policy, pool"| st
    api -->|"3. Recommend"| rec
    rec -->|"4. ranked candidates, possibly empty"| api
    api -->|"5. 200 (nothing issued, nothing published)"| op
```

Source: `apis/openapi.yaml` (`recommendTasks`), `recommend.Recommend`. Branches:
`422 policy-not-declared`, `422 origin-unknown`, `422 station-position-unknown`
(no `fromLocation` and no recorded position), `400 idempotency-key-required` and
`422 idempotency-key-reused` where the Idempotency-Key is enforced.
