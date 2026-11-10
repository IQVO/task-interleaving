---
id: context-map
title: Context map
sidebar_label: Context map
sidebar_position: 13
---

# Context map

This context's slice of the fleet map, following
[Context Mapping](https://github.com/ddd-crew/context-mapping). Relationship
patterns come from [ADR 0001](/docs/adr/0001-task-interleaving-bounded-context);
the **Status** column says, per edge, which side has code on its `origin/develop`
(checked 2026-10-17; the hint consumer, infra and console edges re-read 2026-11-09). The fleet-wide map lives in warehouse-docs.

```mermaid
flowchart LR
    FL["facility-layout<br/>(Generic, wms)"]
    FE["fulfillment-execution<br/>(Core, wes)"]
    TI["task-interleaving<br/>(Supporting, wes)"]
    OA["warehouse-ops-agent"]
    CO["warehouse-console"]
    FL -- "OHS / Published Language<br/>zones, slots, geometry" --> TI
    FE -- "Published Language<br/>TaskCompleted, TaskClaimed,<br/>TaskAvailable (RELOCATE, REPLENISH, PICK)" --> TI
    TI -- "Published Language<br/>InterleavingHintIssued" --> FE
    TI -. "Open Host Service: MCP read tools<br/>client built, endpoint set in infra" .-> OA
    TI -. "Open Host Service: REST<br/>web remote and report" .-> CO
```

Source: `docs/adr/0001-task-interleaving-bounded-context.md` (context map and edge
table), `docs/adr/0003-local-copies-and-consumed-contracts.md`, `apis/asyncapi.yaml`, and
fulfillment-execution's `apis/asyncapi.yaml` and ADRs 0049 and 0050 on its
`origin/develop` for the `TaskAvailable` scope, and its ADR 0051 for the hint consumer.
Solid arrows have code on both sides (producer publishes, consumer built and
always on); dotted arrows have code on one side only. Omits the contexts this one deliberately has no edge to.

## Edges

| Edge | Relationship | Status |
| --- | --- | --- |
| facility-layout -> task-interleaving | Open Host Service / Published Language (`ZoneRegistered`, `LocationSlotRegistered`, `LocationSlotDecommissioned`, `LocationGeometryUpdated` on `warehouse.facility.events`); task-interleaving is a **Conformist** and keeps a local location copy | Producer contract present in `facility-layout`'s `apis/asyncapi.yaml` on `origin/develop`; consumer **built** (always on; group `LAYOUT_CONSUMER_GROUP`) |
| fulfillment-execution -> task-interleaving, `TaskCompleted` | Published Language (`warehouse.fulfillment.events`); **Conformist** | Producer contract present on `origin/develop`; consumer **built** (always on; group `FULFILLMENT_CONSUMER_GROUP`) |
| fulfillment-execution -> task-interleaving, `TaskAvailable` and `TaskClaimed` | Published Language; **Conformist** | Both published on `warehouse.fulfillment.events` by fulfillment-execution (its ADR 0049; ADR 0050 adds REPLENISH; ADR 0062 adds PICK); consumer **built**. `TaskAvailable` is announced for **RELOCATE, REPLENISH and PICK tasks** (the only types that carry a location in fulfillment-execution; a PICK only when its plan carries both the site and the pick face, and then the pick face is both `from_location` and `to_location`), `TaskClaimed` for every task type. PACK, SLAM and REBIN tasks are not announced. This context needed no change for PICK (ADR 0012): a site opts in by declaring a policy whose `eligibleTaskTypes` includes `PICK`. |
| task-interleaving -> fulfillment-execution | Published Language (`InterleavingHintIssued`); fulfillment-execution is a **Conformist** to the payload but owns the decision to use it (tie-break inside one priority band, fail-open) | Published by this context; consumer **built** in fulfillment-execution's `origin/develop` (its ADR 0051; `TASK_INTERLEAVING_MODE=kafka` and its consumer group are set in `warehouse-infra`) |
| task-interleaving -> warehouse-console | **Open Host Service** (REST) | REST API **built**; the console registers the `task_interleaving_mfe` remote and a Hint Adoption report; `warehouse-infra` publishes `/api/task-interleaving`, which reaches `cmd/api` only: no Kong route reaches `cmd/ti-reports`, so the report's `/reports/*` calls have no route today (verified, see [Integration](/docs/ecosystem/integration)) |
| task-interleaving -> warehouse-ops-agent | **Open Host Service** (read-only MCP) | MCP server **built** (`cmd/mcp`); warehouse-ops-agent has the client; `warehouse-infra` sets the endpoint and the `/api/mcp/task-interleaving` route |

## Deliberate non-edges

ADR 0001's *Exclusions*: no edge to `wes-work-planning`, `process-path-management`,
`order-management`, `inventory-storage` or `labor-performance` (priority bands and
CPT stay opaque; `priority_band` is carried and never interpreted); no edge to
`pick-face-replenishment` (`REPLENISH` tasks arrive as ordinary `TaskAvailable`
occurrences, announced since fulfillment-execution's ADR 0050); and no live cross-context lookup in either direction: no outbound
HTTP client, no MCP client, and no sibling calls this context on its hot path.

## Why a separate context at all

The ADR's own answer is the test a reviewer should apply: interleaving **could** be
a rule inside `claimNext`, and it stays a separate context only while the hint is
advisory, never issues an `assign` and owns no task state. If any of those three
properties is dropped, the logic belongs back in fulfillment-execution. See the
[Core domain chart](/docs/ddd/core-domain-chart).
