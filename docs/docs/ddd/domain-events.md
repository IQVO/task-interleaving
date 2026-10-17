---
id: domain-events
title: Domain events
sidebar_label: Domain events
sidebar_position: 21
---

# Domain events

Every event the context raises, publishes or consumes: Go domain event, full
CloudEvents `type`, topic, key, payload, producer and consumers. Payloads are
`snake_case`, optional fields are omitted when unset, timestamps are RFC 3339 UTC.
Contract: `apis/asyncapi.yaml`; catalogue: [ADR 0004](/docs/adr/0004-cloudevents-envelope-and-type-catalogue).

:::caution Contract only
The domain events below are raised in code (`internal/domain/stationtrip/events.go`)
and the envelope helper exists (`internal/adapters/kafka/cloudevents`). Nothing
publishes or consumes them yet: the use cases, the outbox and the Kafka adapters are
not on `develop`.
:::

## Raised by the domain

| Go event | Raised by | Carries | Wire type |
| --- | --- | --- | --- |
| `stationtrip.HintIssued` | `StationTrip.IssueHint` | `Header{Station, Site, Version, At}`, `HintID`, `AfterTask`, `Origin`, `ExpiresAt`, `PolicyVersion`, `Candidates`, `SupersededHintID` | `InterleavingHintIssued` |
| `stationtrip.HintConsumed` | `StationTrip.RecordClaim` | `Header`, `HintID`, `TaskID`, `Rank`, `DetourM` | `InterleavingHintConsumed` |
| `stationtrip.HintExpired` | `StationTrip.Expire` | `Header`, `HintID`, `ExpiresAt` | `InterleavingHintExpired` |
| none (package `policy` raises no event) | the application layer when a policy is declared | | `InterleavingPolicyDeclared` |

`HintIssued` also names the hint it superseded, so persistence can close that row.
A superseded hint has no event of its own.

## Published

Common envelope: `specversion` `1.0`, `source` `/warehouse/task-interleaving`,
`datacontenttype` `application/json`, `id` a UUID persisted with the outbox row
(stable across relay retries), `time` the occurred-at instant in UTC, `subject` and
Kafka key as below, Kafka header `content-type: application/cloudevents+json; charset=UTF-8`.
`dataschema` is `urn:warehouse:task-interleaving:<events|analytics>:<EventName>:v1`.

| Full `type` | Topic(s) | `subject` and key | Consumers |
| --- | --- | --- | --- |
| `com.warehouse.wes.task-interleaving.stationtrip.InterleavingHintIssued` | `warehouse.task-interleaving.events`, `warehouse.task-interleaving.analytics` | station id | fulfillment-execution (consumer change P3, pending) |
| `com.warehouse.wes.task-interleaving.stationtrip.InterleavingHintConsumed` | `warehouse.task-interleaving.analytics` | station id | the service's own analytics side (later phase) |
| `com.warehouse.wes.task-interleaving.stationtrip.InterleavingHintExpired` | `warehouse.task-interleaving.analytics` | station id | same |
| `com.warehouse.wes.task-interleaving.policy.InterleavingPolicyDeclared` | `warehouse.task-interleaving.analytics` | site id | same |

### Payloads

`InterleavingHintIssued`

| Field | Type | Notes |
| --- | --- | --- |
| `hint_id` | string | `hint-` plus a unique suffix |
| `station_id` | string | |
| `site_id` | string | |
| `after_task_id` | string | the completed task this hint follows |
| `origin_location` | string | where the station stood |
| `expires_at` | date-time | `issued_at + hint_ttl_seconds`; `issued_at` is the CloudEvents `time` |
| `policy_version` | integer | at least 1 |
| `candidates` | array, at least 1, at most the policy's `max_candidates` | `{rank, task_id, task_type, location, detour_m}`; ranked from 1 by `detour_m` then `task_id`; `detour_m` rounded to 0.1 m; `location` is the task's `from_location` |

`InterleavingHintConsumed`: `hint_id`, `station_id`, `site_id`, `task_id`, `rank`,
`detour_m` (those of the claimed candidate), `consumed_at`.

`InterleavingHintExpired`: `hint_id`, `station_id`, `site_id`, `expires_at`. The
CloudEvents `time` is when the expiry was recorded, at or after `expires_at`.

`InterleavingPolicyDeclared`: `site_id`, `max_detour_m`, `max_candidates`,
`hint_ttl_seconds`, `eligible_task_types`, `vertical_weight`, `version`,
`declared_at`.

The integration and analytics rows of one occurrence share `type` and `id`; only
`dataschema` differs. Both rows are written in the aggregate's transaction.

## Consumed

| Full `type` | Topic | Key | Fields used | Producer status |
| --- | --- | --- | --- | --- |
| `com.warehouse.wes.fulfillment-execution.task.TaskAvailable` | `warehouse.fulfillment.events` | task id | `task_id, task_type, site_id, from_location, to_location, required_capabilities[], cpt?, priority_band, available_at` | pending producer change P2 |
| `com.warehouse.wes.fulfillment-execution.task.TaskClaimed` | `warehouse.fulfillment.events` | task id | `task_id, task_type, station_id, claimed_at` | pending producer change P2 (analytics-only today, without `claimed_at`) |
| `com.warehouse.wes.fulfillment-execution.task.TaskCompleted` | `warehouse.fulfillment.events` | task id | `task_id, station_id` (the producer requires `task_id, station_id, work_unit_id`) | verified |
| `com.warehouse.wms.facility-layout.zone.ZoneRegistered` | `warehouse.facility.events` | `zoneId` | `zoneId, siteCode` | verified |
| `com.warehouse.wms.facility-layout.locationslot.LocationSlotRegistered` | `warehouse.facility.events` | `locationCode` | `locationCode, zoneId` | verified |
| `com.warehouse.wms.facility-layout.locationslot.LocationSlotDecommissioned` | `warehouse.facility.events` | `locationCode` | `locationCode` | verified |
| `com.warehouse.wms.facility-layout.locationslot.LocationGeometryUpdated` | `warehouse.facility.events` | `locationCode` | `locationCode, xM, yM, zM` | verified |

A `TaskAvailable` with a `task_type` outside the six known types is stored but is
never a candidate. facility-layout `data` also carries `eventName`, `eventType` and
`occurredAt`, which are decoded and ignored. Every other type on these topics is
ignored; dispatch is on the full `type`, never a suffix.

## Envelope helper (on develop)

`internal/adapters/kafka/cloudevents` builds and decodes every envelope:

| Function | Behaviour |
| --- | --- |
| `Type(entity, eventName)` | `com.warehouse.wes.task-interleaving.<entity>.<EventName>` from the constants `Subdomain = "wes"` and `Context = "task-interleaving"` |
| `DataSchema(stream, eventName, version)` | `urn:warehouse:task-interleaving:<stream>:<EventName>:v<version>`, streams `events` and `analytics` |
| `New(Spec)` | Requires a non-empty subject, defaults `Version` below 1 to 1, sets `specversion`, `id`, `source`, `type`, `subject`, `time` (UTC), `dataschema`, `datacontenttype` and the data, validates, returns the structured-mode JSON |
| `ContentTypeHeader()` | the Kafka header `content-type: application/cloudevents+json; charset=UTF-8` |
| `Decode(value)` | Parses and validates; anything that is not a valid CloudEvents 1.0 event, including the retired flat envelope, returns an error wrapping `ErrNotCloudEvent` |

Source: `internal/adapters/kafka/cloudevents/cloudevents.go`, `cloudevents_test.go`
(`TestDecodeRejectsLegacyFlatEnvelope`).
