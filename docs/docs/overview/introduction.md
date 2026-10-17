---
id: introduction
title: Introduction
sidebar_label: Introduction
sidebar_position: 1
---

# Task Interleaving

`task-interleaving` is a **WES-tier Supporting** bounded context of the
warehouse-systems fleet. When a station completes a task, it looks at the open
tasks of the site, ranks them by how far each one starts from where the operator
now stands, and issues an **interleaving hint**: a short, ranked, expiring list of
candidate tasks. Operators chain compatible work instead of walking empty.

The hint is **advisory**. `fulfillment-execution` stays pull-based
(`claimNext(stationId, capabilities)` remains its only claim path) and may use a
hint only as a tie-break inside one priority band, failing open when the hint is
absent, expired or unknown. This context never assigns work and never writes task
state ([ADR 0001](/docs/adr/0001-task-interleaving-bounded-context)).

:::caution What exists on develop today
The service is being built on a separate branch and is **not on `develop`**.
What `develop` carries, and what this documentation covers:

- the domain: `internal/domain/{policy,travel,recommend,stationtrip}`, 100%
  statement coverage and a 90/90 mutation gate;
- the contracts: `apis/openapi.yaml` and `apis/asyncapi.yaml`;
- the architecture fitness tests in `internal/architecture`;
- two small adapter building blocks: the CloudEvents helper
  (`internal/adapters/kafka/cloudevents`) and the acks=all Kafka writer
  constructor (`internal/adapters/outbound/kafka`);
- ADRs 0001 to 0004.

There is no `cmd/`, no migration, no HTTP server, no Kafka consumer and no outbox
relay on `develop`. The operations pages (configuration, runbook, observability,
troubleshooting) and an MCP page are deliberately not written yet: there is no
code to ground them. Where a page describes behaviour that depends on the
service, it says *designed* and not *implemented*.
:::

## What the context owns

| Concept | Where it lives | Rule in one line |
| --- | --- | --- |
| **InterleavingPolicy** (one per site) | `internal/domain/policy` | How far (`MaxDetourM`), how many (`MaxCandidates`, 1 to 5), how long (`HintTTL`), which task types, how much vertical travel weighs. A site with no policy is **inert**: no hint. |
| **StationTrip** (one per station) | `internal/domain/stationtrip` | Where the operator stands (the end of the last completed task) and the most recent hint: `Active`, `Consumed`, `Expired` or `Superseded`. At most one Active hint. |
| **Recommender** | `internal/domain/recommend` | Pure function: same pool in any order, same ranked candidates. Rank by detour, then task id. |
| **Travel model** | `internal/domain/travel` | The port that measures distance; v1 is `rectilinear-v1`: `\|dx\| + \|dy\| + verticalWeight * \|dz\|`, metres, rounded to 0.1 m. Unknown geometry is *unknown*, never 0. |

## Classification and tier

- **Supporting subdomain.** The detour rule is a replaceable policy object behind
  a port, and the output is advisory; the competitive core stays the WES conductor
  and the task lifecycle ([Subdomain classification](/docs/ddd/subdomain-classification)).
- **WES tier**, CloudEvents subdomain segment `wes`: types look like
  `com.warehouse.wes.task-interleaving.<entity>.<EventName>`.

## Main capabilities (designed)

1. Declare a per-site policy with optimistic concurrency (`PUT /sites/{siteId}/policy`).
2. After a `TaskCompleted`, record the station's position and issue a hint of up
   to five nearby open tasks.
3. Mark the hint `Consumed` when the hinted station claims one of its candidates,
   `Expired` at its TTL, `Superseded` when a newer hint is issued.
4. Simulate the recommender read-only (`POST /recommendations`).
5. Publish `InterleavingHintIssued` on `warehouse.task-interleaving.events` and
   hint outcomes on the analytics topic.

Items 1 to 5 are contract-level on `develop`: the domain rules exist, the
use cases, REST routes and Kafka adapters that drive them do not.

## Where to go next

| You want to | Read |
| --- | --- |
| See the layers, packages and the rules that police them | [Architecture](/docs/overview/architecture) |
| Run what actually runs today | [Quickstart](/docs/overview/quickstart) |
| Know what tests and CI gates exist | [Testing](/docs/development/testing) |
| See who talks to whom and what is built | [Integration](/docs/ecosystem/integration) |
| See the domain operations and their invariants | [Use cases](/docs/ddd/use-cases) |
| Read the design in ddd-crew artifacts | [DDD artifacts](/docs/ddd/ddd-artifacts), [Ubiquitous language](/docs/ddd/ubiquitous-language) |
| Read the contracts | [API overview](/docs/api-reference), [Event catalogue](/docs/api-reference/events) |
| Read the decisions | [ADR index](/docs/adr) |
