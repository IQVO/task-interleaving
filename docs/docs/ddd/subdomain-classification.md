---
id: subdomain-classification
title: Subdomain classification
sidebar_label: Subdomain classification
sidebar_position: 2
---

# Subdomain classification

`task-interleaving` is a **Supporting** subdomain in the **WES** tier (CloudEvents
subdomain segment `wes`). The verdict is the one in the fleet table of the
warehouse-docs aggregator (`docs/strategic-design/subdomain-classification.md` on
`main`, section *Scaffolded, not yet built*, 2026-10-17), and it matches the
strategic classification of
[ADR 0001](/docs/adr/0001-task-interleaving-bounded-context). Neighbour rows below
are copied from that table, not recalled.

## Why Supporting

- **Not Generic**: detour limits and eligible task types differ per site, so the
  policy cannot be bought off the shelf unchanged. The per-site
  [InterleavingPolicy](/docs/ddd/aggregate-design-canvas) holds exactly those
  differences.
- **Not Core**: the detour rule is a replaceable policy object. The travel model
  `rectilinear-v1` sits behind the `travel.Model` port and a later model (aisle
  routing) replaces it under its own ADR. The output is **advisory**:
  `fulfillment-execution` keeps pull-based `claimNext` and uses a hint only as a
  tie-break inside one priority band, fail-open. The competitive core stays the WES
  conductor (`wes-work-planning`) and the task lifecycle (`fulfillment-execution`).
- **Business model**: cost reduction (less travel between tasks). **Evolution**:
  custom-built, genesis.

The model complexity is low to moderate (two small aggregates, one pure ranking
function and one port), see the [Core domain chart](/docs/ddd/core-domain-chart).
What would move it: a routing model that measurably beats rectilinear distance and
that the WES relies on would push it towards Core; replacing the recommender with a
bought engine behind an anti-corruption layer would push it to Generic.

## Neighbours (from the fleet table)

| Bounded context | Classification | CloudEvents subdomain | Relation to this context |
| --- | --- | --- | --- |
| `fulfillment-execution` | Core | `wes` | upstream (task events) and downstream (reads the hint) |
| `facility-layout` | Generic | `wms` | upstream (zones, slots, geometry) |
| `wes-work-planning` | Core | `wes` | no edge |
| `process-path-management` | Generic | `wes` | no edge |
| `order-management` | Generic/Supporting | `wes` | no edge |
| `inventory-storage` | Core | `wms` | no edge |
| `labor-performance` | Supporting | `wes` | no edge |
| `warehouse-ops-agent` | Supporting (publishes no events) | `wes` | later: REST reader |
| `pick-face-replenishment` | Supporting | `wms` | no edge; its `REPLENISH` tasks arrive as ordinary tasks |
| `task-interleaving` | **Supporting** | `wes` | this context |

The full fleet table (fifteen built contexts plus the two scaffolded on 2026-10-17)
is in the aggregator; this page lists only the contexts this one touches or
explicitly excludes in ADR 0001.

## Tier is not classification

`wes` and `wms` group contexts by tier, not by Core, Supporting or Generic. The
fleet's own example: `facility-layout` is `wms` and Generic, `wes-work-planning` is
`wes` and Core. For this context the two axes read: tier WES, class Supporting.
The tier decides the type prefix `com.warehouse.wes.task-interleaving.`; the class
decides where investment goes.
