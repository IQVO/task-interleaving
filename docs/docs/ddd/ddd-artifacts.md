---
id: ddd-artifacts
title: DDD artifacts (ddd-crew)
sidebar_label: DDD artifacts index
sidebar_position: 10
---

# DDD artifacts (ddd-crew)

The strategic and tactical design of `task-interleaving`, drawn with the
[ddd-crew](https://github.com/ddd-crew) tools plus UML and ER diagrams. Every
diagram is Mermaid, derived from the code and ADRs on `develop`, and carries a
`Source:` line naming the files it came from.

| Artifact | ddd-crew tool / notation | What it shows |
| --- | --- | --- |
| [Core domain chart](/docs/ddd/core-domain-chart) | [Core Domain Charts](https://github.com/ddd-crew/core-domain-charts) | Where this context sits on business differentiation vs model complexity, and why it is Supporting. |
| [Bounded context canvas](/docs/ddd/bounded-context-canvas) | [Bounded Context Canvas v5](https://github.com/ddd-crew/bounded-context-canvas) | Purpose, classification, roles, every inbound and outbound message, business decisions. |
| [Context map](/docs/ddd/context-map) | [Context Mapping](https://github.com/ddd-crew/context-mapping) | This context's slice of the fleet map: patterns per edge, planned edges labelled. |
| [Aggregate design canvas](/docs/ddd/aggregate-design-canvas) | [Aggregate Design Canvas v1.1](https://github.com/ddd-crew/aggregate-design-canvas) | `StationTrip` and `InterleavingPolicy`: states, invariants, commands, events, concurrency. |
| [Domain message flow](/docs/ddd/domain-message-flow) | [Domain Message Flow Modelling](https://github.com/ddd-crew/domain-message-flow-modelling) | The key scenarios as numbered command / event / query flows across contexts. |
| [EventStorming](/docs/ddd/eventstorming) | [EventStorming glossary and cheat sheet](https://github.com/ddd-crew/eventstorming-glossary-cheat-sheet) | Design-level process flow with the sticky-note colours, plus hotspots. |
| [Ubiquitous language](/docs/ddd/ubiquitous-language) | Glossary | Every term with the code identifier that implements it. |
| [Class diagrams](/docs/ddd/class-diagram) | UML class diagram | Domain types, and the hexagonal ports with the adapters that exist. |
| [Entity-relationship diagram](/docs/ddd/entity-relationship) | ER diagram | The conceptual model; there is no database schema on `develop`. |
| [Sequence diagrams](/docs/ddd/sequence-diagrams) | UML sequence diagram | Declare a policy, complete-then-hint, claim, expiry and the simulation, with error branches. |
| [Domain events](/docs/ddd/domain-events) | Event catalogue | Every event published and consumed: full CloudEvents type, topic, key, payload, producer, consumers. |

Related pages outside the pack: [Use cases](/docs/ddd/use-cases),
[Subdomain classification](/docs/ddd/subdomain-classification),
[Integration](/docs/ecosystem/integration) and the
[event catalogue](/docs/api-reference/events).

:::note[Sources of truth]
The code wins. Domain rules come from `internal/domain/policy`, `travel`,
`recommend` and `stationtrip`, the envelope from
`internal/adapters/kafka/cloudevents`, the REST contract from `apis/openapi.yaml`,
the event contract from `apis/asyncapi.yaml`, and decisions from the ADRs in
`docs/adr/`. Where a page and the code disagree, the page is wrong.
:::

:::info Not in this pack
`develop` has no application layer, HTTP server, Kafka consumer, outbox relay,
database schema, MCP server or web remote for this context. There are no pages for
them, and every edge or flow that depends on them is labelled *designed* or
*pending* wherever it appears.
:::
