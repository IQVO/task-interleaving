---
id: bounded-context-canvas
title: Bounded context canvas
sidebar_label: Bounded context canvas
sidebar_position: 12
---

# Bounded context canvas

ddd-crew [Bounded Context Canvas v5](https://github.com/ddd-crew/bounded-context-canvas),
filled from [ADR 0001](/docs/adr/0001-task-interleaving-bounded-context) (which
carries its own canvas), the domain code and the contracts. Every message below is
**contract-level**: the HTTP, Kafka and outbox adapters that carry it are not on
`develop` (see [Integration](/docs/ecosystem/integration) for the status of each).

## Canvas at a glance

Arrows follow the message type: **solid blue** commands (`cmd`), **dotted green**
queries (`qry`), **thick orange** events (`evt`). Dashed boxes are designed edges
with no counterpart built yet.

### Inbound

```mermaid
%%{init: {"flowchart": {"nodeSpacing": 14, "rankSpacing": 40, "padding": 6}}}%%
flowchart LR
    classDef bc fill:#fff3bf,stroke:#e67700,stroke-width:1.5px,color:#000
    classDef card fill:#fff9db,stroke:#f08c00,color:#000,text-align:left
    classDef ctx fill:#e7f5ff,stroke:#1c7ed6,color:#000
    classDef actor fill:#fff4e6,stroke:#e8590c,color:#000
    subgraph IN["Inbound - who commands, queries or feeds it"]
        direction TB
        in0(["Operator<br/>(console, agent)"]):::actor
        in1["fulfillment-execution"]:::ctx
        in2["facility-layout"]:::ctx
    end
    subgraph BC["task-interleaving - Supporting subdomain - WES tier"]
        direction TB
        purpose["<b>Purpose</b><br/>After a station completes a task, recommend the few open<br/>tasks nearest to where the operator now stands, as an<br/>advisory, expiring hint."]:::card
        roles["<b>Domain roles</b><br/>- Advisor (computes hints)<br/>- Policy holder (per-site settings)<br/>- Simulation (read-only what-if)"]:::card
        rules["<b>Business decisions it enforces</b><br/>- Max detour, max candidates (1 to 5), hint TTL<br/>- Eligible task types, weight of vertical travel<br/>- Rank by detour then task id<br/>- At most one Active hint per station<br/>- A site without a policy is inert"]:::card
        purpose ~~~ roles ~~~ rules
    end
    in0 -->|"cmd - REST<br/>Declare policy"| BC
    in0 -.->|"qry - REST<br/>Get policy, hints, station,<br/>simulate recommendation"| BC
    in1 ==>|"evt - Kafka<br/>warehouse.fulfillment.events<br/>TaskCompleted, TaskAvailable*,<br/>TaskClaimed*"| BC
    in2 ==>|"evt - Kafka<br/>warehouse.facility.events<br/>ZoneRegistered, LocationSlotRegistered,<br/>LocationSlotDecommissioned,<br/>LocationGeometryUpdated"| BC
    linkStyle 0 stroke:#1971c2,stroke-width:1.5px
    linkStyle 1 stroke:#2f9e44,stroke-width:1.5px
    linkStyle 2 stroke:#e8590c,stroke-width:2.5px
    linkStyle 3 stroke:#e8590c,stroke-width:2.5px
    class BC bc
```

`*` pending producer change P2 in fulfillment-execution.

### Outbound

```mermaid
%%{init: {"flowchart": {"nodeSpacing": 14, "rankSpacing": 40, "padding": 6}}}%%
flowchart LR
    classDef bc fill:#fff3bf,stroke:#e67700,stroke-width:1.5px,color:#000
    classDef ctx fill:#e7f5ff,stroke:#1c7ed6,color:#000
    classDef self fill:#f1f3f5,stroke:#868e96,color:#000
    classDef none fill:#f8f9fa,stroke:#adb5bd,stroke-dasharray:5 5,color:#495057
    BC["<b>task-interleaving - Supporting subdomain - WES tier</b>"]:::bc
    subgraph OUT["Outbound - whom it notifies"]
        direction TB
        out0["fulfillment-execution<br/>(tie-break inside one priority band;<br/>consumer change P3 pending)"]:::none
        out1["own analytics side<br/>(later phase)"]:::self
    end
    BC ==>|"evt - Kafka<br/>warehouse.task-interleaving.events<br/>InterleavingHintIssued"| out0
    BC ==>|"evt - Kafka<br/>warehouse.task-interleaving.analytics<br/>all four types"| out1
    linkStyle 0 stroke:#e8590c,stroke-width:2.5px
    linkStyle 1 stroke:#e8590c,stroke-width:2.5px
    class BC bc
```

Source: the Inbound and Outbound Communication tables of this page, which follow
`docs/adr/0001-task-interleaving-bounded-context.md` and `apis/asyncapi.yaml`. No
`internal/adapters/inbound` or `internal/adapters/outbound` package carries these
messages yet.

## Name

**task-interleaving**

## Purpose

After a station completes a task, recommend the few open tasks nearest to where the
operator now stands, as an advisory, expiring hint that `fulfillment-execution`
may use as a tie-break inside one priority band.

## Strategic classification

| Dimension | Value |
| --- | --- |
| Domain | **Supporting** ([Subdomain classification](/docs/ddd/subdomain-classification)) |
| Tier | WES, CloudEvents subdomain `wes`, type prefix `com.warehouse.wes.task-interleaving.`, source `/warehouse/task-interleaving`, Kong path `/api/task-interleaving` |
| Business model | Cost reduction (less travel between tasks) |
| Evolution | Custom-built, genesis |

## Domain roles

- **Advisor**: computes hints (`recommend.Recommend`, `StationTrip.IssueHint`).
- **Policy holder**: per-site settings (`policy.Policy`).
- **Simulation**: a read-only what-if (`POST /recommendations`).

## Inbound communication

| Collaborator | Message | Kind | Channel | Status |
| --- | --- | --- | --- | --- |
| fulfillment-execution | `TaskCompleted` | event | `warehouse.fulfillment.events` | producer verified; consumer designed |
| fulfillment-execution | `TaskAvailable`, `TaskClaimed` | events | `warehouse.fulfillment.events` | pending producer change P2 |
| facility-layout | `ZoneRegistered`, `LocationSlotRegistered`, `LocationSlotDecommissioned`, `LocationGeometryUpdated` | events | `warehouse.facility.events` | producer verified; consumer designed |
| operator (console, agent) | Declare policy | command | `PUT /sites/{siteId}/policy` | contract only |
| operator (console, agent) | Get policy, list hints, get hint, get station, simulate | queries | `GET /sites/{siteId}/policy`, `GET /hints`, `GET /hints/{hintId}`, `GET /stations/{stationId}`, `POST /recommendations` | contract only |

## Outbound communication

| Message | Kind | Channel | Consumers |
| --- | --- | --- | --- |
| `InterleavingHintIssued` | event | `warehouse.task-interleaving.events` and `warehouse.task-interleaving.analytics` | fulfillment-execution (pending consumer change P3) |
| `InterleavingHintConsumed`, `InterleavingHintExpired`, `InterleavingPolicyDeclared` | events | `warehouse.task-interleaving.analytics` only | the service's own analytics side (later phase) |

Source: `apis/asyncapi.yaml`, ADR 0004.

## Ubiquitous language

StationTrip, position, hint (Active, Consumed, Expired, Superseded), candidate,
detour, policy, travel model, open-task pool, priority band, tie-break, inert,
fail-open. Full glossary: [Ubiquitous language](/docs/ddd/ubiquitous-language).

## Business decisions

| Decision | Rule in code |
| --- | --- |
| Is a site served | only if it declared a policy; otherwise inert |
| How far | `MaxDetourM`, finite and greater than 0; a detour equal to the limit qualifies |
| How many | `MaxCandidates`, 1 to 5 |
| For how long | `HintTTL`, greater than 0; `expires_at = issued_at + TTL` |
| Which tasks | `EligibleTaskTypes`, non-empty subset of the six task types |
| How vertical travel weighs | `VerticalWeight`, at least 0 |
| Who is a candidate | same site, eligible type, not the completed task, known geometry, within the limit |
| Order | detour ascending, then task id ascending; distinct by task id |
| Hint count | at most one Active hint per station; a new hint supersedes it |
| Outcome | Consumed on a claim of a candidate while unexpired; Expired at `expires_at` |

## Assumptions

From ADR 0001 and ADR 0003:

- A station's position is the end location of its last completed task.
- A location's site is its zone's site.
- Geometry from facility-layout is in metres in one frame per site.
- The pool's `site_id` values equal facility-layout's `siteCode` values (one code
  space); to be checked on live data before the first live hint.
- The pool copy is as fresh as the events.

## Verification metrics

- Deterministic recommender: same pool in any order, same output
  (`TestRecommendIsIndependentOfPoolOrder`).
- Mutation gate: 90 / 90 on the four domain packages, measured 100% / 100%.
- Coverage gate: 90% over `internal/domain` and `internal/application`, measured
  100% on the domain.
- `TestEventCatalogueMatchesContract` keeps ADR 0004 and `apis/asyncapi.yaml` in
  step. Golden CloudEvents tests per published type are a rule of the fleet and are
  not on `develop` yet (no publisher).

## Open questions

- When does producer change P2 (`TaskAvailable`, `TaskClaimed` with `claimed_at` on
  the integration topic) land in fulfillment-execution, and when does consumer change
  P3 follow? Until P2 the pool is empty and no hint is issued; until P3 hints are
  published and observable only (ADR 0001).
- Who triggers expiry? `StationTrip.Expire(now)` exists; no ADR or contract names
  the scheduler that calls it.
- Do the pool's `site_id` values equal the facility `siteCode` values, what share of
  pool locations has geometry, and is the frame the same per site (ADR 0003's
  live-data rule)?
