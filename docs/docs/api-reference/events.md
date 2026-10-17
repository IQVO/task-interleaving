---
id: events
title: Event catalogue
sidebar_label: Event catalogue
sidebar_position: 2
---

# Event catalogue

Every Kafka message this service produces or consumes is a CloudEvents 1.0
event in **structured mode**: the Kafka value is the whole JSON envelope and the
header `content-type` is `application/cloudevents+json; charset=UTF-8`. Envelopes
are built and decoded only in `internal/adapters/kafka/cloudevents`, which is on
`develop` ([ADR 0004](/docs/adr/0004-cloudevents-envelope-and-type-catalogue)).
The contract is `apis/asyncapi.yaml`; field-level detail is on
[Domain events](/docs/ddd/domain-events).

:::info Built, with caveats
The outbox publisher and relay (`internal/adapters/outbound/{events,outbox}`) and the
two consumers (`internal/adapters/inbound/kafka`) are on `develop`; the relay needs
`DATABASE_URL` and `KAFKA_BROKERS`, and each consumer is opt-in. The tables below
are the pinned contract, not observed traffic. Whether the other side of each edge
exists is on [Integration](/docs/ecosystem/integration).
:::

## Published

Source `/warehouse/task-interleaving`, `dataschema`
`urn:warehouse:task-interleaving:<events|analytics>:<EventName>:v1`. The Kafka
key and `subject` are the station id for hint events and the site id for the
policy event.

| Type | Topics | Raised when | `data` |
| --- | --- | --- | --- |
| `com.warehouse.wes.task-interleaving.stationtrip.InterleavingHintIssued` | `warehouse.task-interleaving.events` and `warehouse.task-interleaving.analytics` | a hint is issued for a station | `hint_id`, `station_id`, `site_id`, `after_task_id`, `origin_location`, `expires_at`, `policy_version`, `candidates[{rank, task_id, task_type, location, detour_m}]` |
| `com.warehouse.wes.task-interleaving.stationtrip.InterleavingHintConsumed` | analytics only | the hinted station claims one of the candidates | `hint_id`, `station_id`, `site_id`, `task_id`, `rank`, `detour_m`, `consumed_at` |
| `com.warehouse.wes.task-interleaving.stationtrip.InterleavingHintExpired` | analytics only | an Active hint reaches its `expires_at` | `hint_id`, `station_id`, `site_id`, `expires_at` |
| `com.warehouse.wes.task-interleaving.policy.InterleavingPolicyDeclared` | analytics only | a site's policy is declared or replaced | `site_id`, `max_detour_m`, `max_candidates`, `hint_ttl_seconds`, `eligible_task_types`, `vertical_weight`, `version`, `declared_at` |

A superseded hint raises no event of its own: the next `InterleavingHintIssued` of
the same station replaces it.

## Consumed

| Type | Topic | Status |
| --- | --- | --- |
| `com.warehouse.wes.fulfillment-execution.task.TaskAvailable` | `warehouse.fulfillment.events` | verified on the producer's contract; announced for RELOCATE tasks only |
| `com.warehouse.wes.fulfillment-execution.task.TaskClaimed` | `warehouse.fulfillment.events` | verified on the producer's contract |
| `com.warehouse.wes.fulfillment-execution.task.TaskCompleted` | `warehouse.fulfillment.events` | verified on the producer's contract |
| `com.warehouse.wms.facility-layout.zone.ZoneRegistered` | `warehouse.facility.events` | verified on the producer's contract |
| `com.warehouse.wms.facility-layout.locationslot.LocationSlotRegistered` | `warehouse.facility.events` | verified on the producer's contract |
| `com.warehouse.wms.facility-layout.locationslot.LocationSlotDecommissioned` | `warehouse.facility.events` | verified on the producer's contract |
| `com.warehouse.wms.facility-layout.locationslot.LocationGeometryUpdated` | `warehouse.facility.events` | verified on the producer's contract |

Every other type on those topics is ignored; a message that is not a valid
CloudEvent is logged and skipped. Consumer groups come from the environment
variables `FULFILLMENT_CONSUMER_GROUP` and `LAYOUT_CONSUMER_GROUP`
([ADR 0003](/docs/adr/0003-local-copies-and-consumed-contracts)); `cmd/api` reads
them only when the matching `*_MODE` is `kafka`. A message that fails is published
to `<topic>.dlq`; see the [Runbook](/docs/operations/runbook#consumed).
