---
id: domain-message-flow
title: Domain message flow
sidebar_label: Domain message flow
sidebar_position: 15
---

# Domain message flow

[Domain Message Flow Modelling](https://github.com/ddd-crew/domain-message-flow-modelling)
of the key scenarios: commands, events and queries as numbered messages between
collaborators. Every collaborator is code on `develop`: the domain steps
(`policy`, `recommend`, `stationtrip`), the use cases, the REST handlers, the
consumers, the expiry worker and the outbox relay. On the sibling side,
fulfillment-execution publishes the events of flows 2 and 3 (`TaskAvailable` only
for RELOCATE and REPLENISH tasks), and nothing in it consumes the hint published at the end of
flow 2.

## Flow 1 — declare a policy

```mermaid
flowchart LR
    op(["Operator"])
    api["inbound/http + DeclarePolicy"]
    pol["policy.Policy"]
    ob[("outbox_events")]
    an["analytics topic"]
    op -->|"1. cmd: PUT /sites/{siteId}/policy + If-Match"| api
    api -->|"2. policy.New or WithChanges"| pol
    pol -->|"3. Policy version n"| api
    api -->|"4. evt row: InterleavingPolicyDeclared"| ob
    ob ==>|"5. evt: analytics only"| an
    api -->|"6. 200 + ETag version"| op
```

Source: `internal/domain/policy/policy.go`,
`internal/application/usecases/declare_policy.go`, `apis/openapi.yaml`
(`declareInterleavingPolicy`), `apis/asyncapi.yaml`. The outbox row is written in
the same transaction as the policy. Failure branches: invalid
settings 400 `invalid-policy` or `invalid-task-type`; stale or missing `If-Match`
on an existing policy 412 `version-mismatch`; lost race 409 `concurrent-modification`.

## Flow 2 — a station completes a task, a hint is issued

```mermaid
flowchart LR
    fe["fulfillment-execution"]
    cons["fulfillment consumer + ApplyTask* use cases"]
    pool[("task_pool, zones, locations")]
    rec["recommend.Recommend"]
    trip["stationtrip.StationTrip"]
    ob[("outbox_events")]
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

Source: `internal/application/usecases/apply_task_completed.go`,
`internal/domain/stationtrip/trip.go`, `internal/domain/recommend/recommend.go`,
ADR 0002, ADR 0003, ADR 0004. Messages 2 to 9 run in one database transaction
together with the claim of the CloudEvents id. Branches:

- No candidate: step 7 does not happen; an older Active hint is **not** retracted
  and expires at its TTL.
- Origin without geometry: `ErrOriginUnknown`, no hint.
- A completion for a task the pool never saw, or without a `to_location` or site:
  recorded as completed, outcome `ignored`, no position, not dead-lettered.
- A duplicate CloudEvents id: outcome `duplicate`, nothing runs.
- A site with no policy: inert, no hint.
- The hint is advisory: fulfillment-execution may use it as a tie-break inside one
  priority band. It does not consume it yet (no code reads
  `warehouse.task-interleaving.events` there).

## Flow 3 — the hinted station claims a candidate

```mermaid
flowchart LR
    fe["fulfillment-execution"]
    cons["fulfillment consumer + ApplyTask* use cases"]
    trip["stationtrip.StationTrip"]
    ob[("outbox_events")]
    an["analytics topic"]
    fe ==>|"1. evt: TaskClaimed (task_id, station_id, claimed_at)"| cons
    cons -->|"2. cmd: RecordClaim(task, claimed_at)"| trip
    trip -->|"3. evt: HintConsumed (only for a candidate, unexpired)"| cons
    cons ==>|"4. evt row: InterleavingHintConsumed"| ob
    ob ==>|"5. evt: analytics only"| an
```

Source: `internal/application/usecases/apply_task_claimed.go`,
`StationTrip.RecordClaim`, ADR 0003. The task is also marked CLAIMED in the pool
copy, which removes it from later candidate lists. A claim of a non-candidate, a claim after
expiry or a claim with no Active hint changes nothing and publishes nothing.

## Flow 4 — a hint expires

```mermaid
flowchart LR
    clk["inbound/expiry worker + ExpireHints"]
    trip["stationtrip.StationTrip"]
    ob[("outbox_events")]
    an["analytics topic"]
    clk -->|"1. cmd: Expire(now)"| trip
    trip -->|"2. evt: HintExpired (at or after expires_at)"| clk
    clk ==>|"3. evt row: InterleavingHintExpired"| ob
    ob ==>|"4. evt: analytics only"| an
```

Source: `internal/adapters/inbound/expiry/worker.go`,
`internal/application/usecases/expire_hints.go`, `StationTrip.Expire`,
`apis/openapi.yaml` ("the clock marks it Expired"). The worker sweeps every
`HINT_EXPIRY_INTERVAL` (default 5 s); the ADRs do not name the caller.

## Flow 5 — simulate a recommendation (query)

```mermaid
flowchart LR
    op(["Operator or agent"])
    api["inbound/http + DeclarePolicy"]
    st[("station_trips, task_pool, locations, policies")]
    rec["recommend.Recommend"]
    op -->|"1. qry: POST /recommendations {stationId, fromLocation?}"| api
    api -->|"2. read position, site, policy, pool"| st
    api -->|"3. Recommend"| rec
    rec -->|"4. ranked candidates, possibly empty"| api
    api -->|"5. 200 (nothing issued, nothing published)"| op
```

Source: `internal/application/usecases/recommend.go`, `apis/openapi.yaml`
(`recommendTasks`), `recommend.Recommend`. Branches:
`422 policy-not-declared`, `422 origin-unknown`, `422 station-position-unknown`
(no `fromLocation` and no recorded position), `400 idempotency-key-required`,
`400 idempotency-key-invalid` and `409 idempotency-key-reused` in Postgres mode,
where the Idempotency-Key is enforced; a replay carries `Idempotency-Replayed: true`.
