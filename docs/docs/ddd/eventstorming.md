---
id: eventstorming
title: EventStorming
sidebar_label: EventStorming
sidebar_position: 16
---

# EventStorming

Design-level EventStorming of the hint flow, using the
[ddd-crew glossary](https://github.com/ddd-crew/eventstorming-glossary-cheat-sheet)
colours: orange domain events, blue commands, yellow aggregates, lilac policies,
pink external systems, green read models, red hotspots. The model was derived from
the code and ADRs, not from a workshop.

```mermaid
flowchart LR
    classDef event fill:#ffa94d,stroke:#d9480f,color:#000
    classDef command fill:#74c0fc,stroke:#1971c2,color:#000
    classDef agg fill:#ffe066,stroke:#e67700,color:#000
    classDef policy fill:#d0bfff,stroke:#6741d9,color:#000
    classDef ext fill:#faa2c1,stroke:#c2255c,color:#000
    classDef read fill:#8ce99a,stroke:#2b8a3e,color:#000
    classDef hot fill:#ff8787,stroke:#c92a2a,color:#000

    opr["Operator"]:::ext
    fe["fulfillment-execution"]:::ext
    fl["facility-layout"]:::ext

    c1["Declare interleaving policy"]:::command
    a1["InterleavingPolicy"]:::agg
    e1(["PolicyDeclared (wire: InterleavingPolicyDeclared)"]):::event

    e2(["TaskAvailable (RELOCATE, REPLENISH only today)"]):::event
    e3(["TaskCompleted"]):::event
    e4(["TaskClaimed"]):::event
    e5(["LocationGeometryUpdated"]):::event
    r1["Open-task pool and geometry copies"]:::read

    p1["Whenever a task is completed, record the station position"]:::policy
    c2["RecordCompletion"]:::command
    a2["StationTrip"]:::agg
    p2["Whenever a position is recorded and the site has a policy, compute a hint"]:::policy
    c3["Recommend (pure)"]:::command
    c4["IssueHint"]:::command
    e6(["HintIssued (wire: InterleavingHintIssued)"]):::event
    p3["Whenever a task is claimed, check the Active hint"]:::policy
    c5["RecordClaim"]:::command
    e7(["HintConsumed (wire: InterleavingHintConsumed)"]):::event
    c6["Expire"]:::command
    e8(["HintExpired (wire: InterleavingHintExpired)"]):::event
    h1{{"RESOLVED: the expiry worker calls Expire"}}:::hot
    h2{{"HOTSPOT: no candidate leaves an old hint Active until its TTL"}}:::hot
    h3{{"HOTSPOT: PICK, PACK, SLAM and REBIN tasks are not announced, and nobody consumes the hint"}}:::hot

    opr --> c1 --> a1 --> e1
    fl --> e5 --> r1
    fe --> e2 --> r1
    fe --> e3 --> p1 --> c2 --> a2
    a2 --> p2 --> c3 --> c4 --> a2
    a2 --> e6
    r1 -.-> c3
    fe --> e4 --> p3 --> c5 --> a2 --> e7
    c6 --> a2 --> e8
    h1 -.-> c6
    h2 -.-> p2
    h3 -.-> e6
```

Source: `internal/domain/stationtrip/{trip,events}.go`, `internal/domain/policy/policy.go`,
`internal/domain/recommend/recommend.go`, `internal/application/usecases/*.go`
(`apply_task_completed.go`, `apply_task_claimed.go`, `expire_hints.go`),
`internal/adapters/inbound/expiry/worker.go`, `docs/adr/0001` to `0005`,
`apis/asyncapi.yaml`, and fulfillment-execution's `apis/asyncapi.yaml` on its
`origin/develop` for the `TaskAvailable` scope.
Each domain event name is the Go type; the wire name is the CloudEvents event name in
ADR 0004.

## Reading it

1. **Declare.** An operator declares a site policy. Until then the site is inert.
2. **Feed.** fulfillment-execution and facility-layout events fill the local copies
   (open-task pool, claims, geometry) through the opt-in consumers.
   fulfillment-execution publishes `TaskAvailable` today only for RELOCATE and REPLENISH tasks.
3. **Complete.** A `TaskCompleted` records the station's new position, taken from the
   completed task's `to_location` in the pool copy.
4. **Hint.** The recommender ranks the pool; when it yields at least one candidate,
   `IssueHint` supersedes any Active hint and raises `HintIssued`.
5. **Outcome.** A claim of a candidate while unexpired raises `HintConsumed`; the
   hint-expiry worker raises `HintExpired`; a newer hint supersedes silently.

## Hotspots

| # | Hotspot | State |
| --- | --- | --- |
| 1 | **Who calls `Expire`?** The `ExpireHints` use case, driven by the `inbound/expiry` worker every `HINT_EXPIRY_INTERVAL` (default 5 s). The aggregate still takes the instant from the caller. | Resolved in code (ADR text still silent) |
| 2 | **A completion with no candidate leaves an older Active hint in place** until its TTL (bounded staleness; the hint is advisory and fail-open). Retracting it would need an event for the retraction: a later ADR if the floor needs it (ADR 0002). | Accepted by ADR 0002 |
| 3 | **Announcement scope and the hint consumer.** fulfillment-execution publishes `TaskAvailable` only for RELOCATE and REPLENISH tasks (its ADR 0049 and ADR 0050), so the pool, and every candidate list, holds those two types only; PICK, PACK, SLAM and REBIN have no location there. And nothing in fulfillment-execution consumes `InterleavingHintIssued` (out of scope in both ADRs), so hints are published and acted on by nobody (ADR 0001). | Open, other contexts |
| 4 | **Site and geometry code spaces.** v1 assumes the pool's `site_id` equals facility-layout's `siteCode` and one geometry frame per site (ADR 0003, live-data rule). | Open until checked on live data |
| 5 | **Wording drift in the REST spec.** `apis/openapi.yaml` says a hint is `Superseded` by "a newer completion at the same station" and mentions `supersededAt`; ADR 0002 and the code supersede a hint only when a newer hint is issued (a completion with no candidate supersedes nothing). The code does keep `supersededAt` (`hints.superseded_at`), so only the "newer completion" wording differs. | Reported as a discrepancy |
