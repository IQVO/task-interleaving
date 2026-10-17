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

:::info What runs on develop
The service merged to `develop` in PR #7: one binary, `cmd/api`, with a chi REST
adapter (RFC 7807 problems, `Idempotency-Key` middleware), Postgres repositories
with embedded migrations, a transactional outbox with a Kafka relay, a hint-expiry
worker, and two Kafka consumers (fulfillment-execution and facility-layout)
with retry, a dead-letter topic and processed-event de-duplication. It runs on
real Postgres and real Kafka only: there is no in-memory mode, the consumers and
the relay are always on, and `DATABASE_URL`, `KAFKA_BROKERS` and both consumer
group ids are mandatory
([ADR 0006](/docs/adr/0006-postgres-and-kafka-are-the-only-runtime)).

Packaged: a `Dockerfile` and the Helm chart `charts/task-interleaving`. Not built:
an MCP server (`cmd/mcp`), any OpenTelemetry metric or trace, a `warehouse-infra`
entry and a consumer of the hint in fulfillment-execution.
The [Runbook](/docs/operations/runbook#deployment) and
[Integration](/docs/ecosystem/integration) pages say exactly where each edge stands.
:::

## What the context owns

| Concept | Where it lives | Rule in one line |
| --- | --- | --- |
| **InterleavingPolicy** (one per site) | `internal/domain/policy` | How far (`MaxDetourM`), how many (`MaxCandidates`, 1 to 5), how long (`HintTTL`), which task types, how much vertical travel weighs. A site with no policy is **inert**: no hint. |
| **StationTrip** (one per station) | `internal/domain/stationtrip` | Where the operator stands (the end of the last completed task) and the most recent hint: `Active`, `Consumed`, `Expired` or `Superseded`. At most one Active hint. |
| **Recommender** | `internal/domain/recommend` | Pure function: same pool in any order, same ranked candidates. Rank by detour, then task id. |
| **Travel model** | `internal/domain/travel` | The port that measures distance; v1 is `rectilinear-v1`: `\|dx\| + \|dy\| + verticalWeight * \|dz\|`, metres, rounded to 0.1 m. Unknown geometry is *unknown*, never 0. |
| **Local copies** | `task_pool`, `zones`, `locations` tables | Event-fed read models of the open-task pool (from fulfillment-execution) and of location geometry (from facility-layout); not aggregate state ([ADR 0003](/docs/adr/0003-local-copies-and-consumed-contracts)). |

## Classification and tier

- **Supporting subdomain.** The detour rule is a replaceable policy object behind
  a port, and the output is advisory; the competitive core stays the WES conductor
  and the task lifecycle ([Subdomain classification](/docs/ddd/subdomain-classification)).
- **WES tier**, CloudEvents subdomain segment `wes`: types look like
  `com.warehouse.wes.task-interleaving.<entity>.<EventName>`.

## Main capabilities

1. Declare a per-site policy with optimistic concurrency
   (`PUT /sites/{siteId}/policy`, `If-Match` / `ETag`).
2. Keep the open-task pool and the location geometry current from Kafka
   (`TaskAvailable`, `TaskClaimed`, `TaskCompleted`; zone, location and geometry
   events), always on.
3. After a `TaskCompleted`, record the station's position and issue a hint of up
   to five nearby open tasks.
4. Mark the hint `Consumed` when the hinted station claims one of its candidates,
   `Expired` at its TTL (swept every 5 s by default), `Superseded` when a newer
   hint is issued.
5. Read policies, hints and stations over REST, and simulate the recommender
   read-only (`POST /recommendations`).
6. Publish `InterleavingHintIssued` on `warehouse.task-interleaving.events` and
   the hint outcomes on `warehouse.task-interleaving.analytics`, through the
   transactional outbox.

## Where to go next

| You want to | Read |
| --- | --- |
| See the layers, packages, binary and the rules that police them | [Architecture](/docs/overview/architecture) |
| Run it and make the first calls | [Quickstart](/docs/overview/quickstart) |
| Know every setting | [Configuration](/docs/operations/configuration) |
| Deploy and operate it | [Runbook](/docs/operations/runbook), [Observability](/docs/operations/observability) |
| Fix something that broke | [Troubleshooting](/docs/operations/troubleshooting) |
| Know what tests and CI gates exist | [Testing](/docs/development/testing) |
| See who talks to whom and what is built | [Integration](/docs/ecosystem/integration) |
| See the application use cases and their invariants | [Use cases](/docs/ddd/use-cases) |
| Read the design in ddd-crew artifacts | [DDD artifacts](/docs/ddd/ddd-artifacts), [Ubiquitous language](/docs/ddd/ubiquitous-language) |
| Read the contracts | [API overview](/docs/api-reference), [Event catalogue](/docs/api-reference/events) |
| Read the decisions | [ADR index](/docs/adr) |

Published at [https://iqvo.github.io/task-interleaving/](https://iqvo.github.io/task-interleaving/).
