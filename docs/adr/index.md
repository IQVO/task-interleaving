---
id: index
title: Architecture decision records
sidebar_label: ADR index
slug: /
---

# Architecture decision records

The decisions of `task-interleaving`, in the order they were taken. The ADR
bodies are immutable; a changed decision gets a new ADR that supersedes the old
one. The *Status* column is copied from each ADR's own `## Status` section.

| ADR | Title | Status |
| --- | --- | --- |
| [0001](/docs/adr/0001-task-interleaving-bounded-context) | task-interleaving as the advisor of next-task hints at a station | Accepted (2026-10-17) |
| [0002](/docs/adr/0002-stationtrip-and-policy-aggregates) | the StationTrip and InterleavingPolicy aggregates, the recommender and the policy ports | Accepted (2026-10-17) |
| [0003](/docs/adr/0003-local-copies-and-consumed-contracts) | event-fed local copies and the consumed contracts | Accepted (2026-10-17) |
| [0004](/docs/adr/0004-cloudevents-envelope-and-type-catalogue) | CloudEvents envelope and type catalogue | Accepted (2026-10-17) |
| [0005](/docs/adr/0005-persistence-and-evaluation-semantics) | persistence and evaluation semantics | Accepted (2026-10-17) |
