---
paths:
  - "internal/adapters/**/kafka/**"
  - "internal/adapters/outbound/events/**"
  - "apis/asyncapi*"
---

# Cross-service integration events (Kafka)

task-interleaving PUBLISHES and CONSUMES. It publishes to
`warehouse.task-interleaving.events` (integration; only `InterleavingHintIssued`,
read by fulfillment-execution) and `warehouse.task-interleaving.analytics` (all
four types, read only by this service's own analytics side), both through the
transactional outbox. It consumes `warehouse.fulfillment.events` and
`warehouse.facility.events` into durable local copies. Contracts:
`apis/asyncapi.yaml`, `docs/adr/0003-local-copies-and-consumed-contracts.md` and
`docs/adr/0004-cloudevents-envelope-and-type-catalogue.md`. Fleet naming:
`warehouse.<context>.events` (integration) and `warehouse.<context>.analytics`.

## Events: CloudEvents 1.0 is MANDATORY

Every Kafka message this service produces or consumes (integration
`warehouse.<ctx>.events` AND analytics `warehouse.<ctx>.analytics`) is a
CloudEvents 1.0 event in structured content mode. This is a hard fleet rule,
not a preference — there is nothing to "choose" here:

- No flat envelope (`event_id`/`event_type`/`occurred_at`), no dual-write,
  no dual-read, no envelope toggle env var (`EVENT_ENVELOPE_MODE` is gone
  fleet-wide). `internal/architecture/fitness_test.go`'s
  `TestNoEventEnvelopeToggleOrFlatEnvelope` fails CI on any of them.
- Build/validate/(un)marshal with `github.com/cloudevents/sdk-go/v2/event`
  (latest v2) via ONE helper package,
  `internal/adapters/kafka/cloudevents/` (`cloudevents.go` + its test; only the
  per-repo constants differ from the fleet's other repos). Transport stays
  `segmentio/kafka-go` (no sdk-go
  protocol/client packages, no hand-rolled CloudEvent structs).
- Every produced message carries the Kafka header
  `content-type: application/cloudevents+json; charset=UTF-8`
  (`cloudevents.ContentTypeHeader()`), next to the W3C trace headers
  (`traceparent`/`tracestate` stay in headers, never duplicated into
  extension attributes). Message key = aggregate id, `kafkago.Hash{}`
  balancer.
- Required attributes: `specversion=1.0`; `id` (UUID v4 minted ONCE per
  domain event and persisted with the outbox row, so redelivery carries
  the same id); `source=/warehouse/<repo>`; `type`; `subject` (aggregate
  instance id, never empty); `time` (domain occurred-at, UTC);
  `datacontenttype=application/json`;
  `dataschema=urn:warehouse:<repo>:<events|analytics>:<EventName>:v<N>`.
  No custom extension attributes without an ADR.
- `type` = `com.warehouse.<subdomain>.<bounded-context>.<entity>.<EventName>`
  (subdomain `wms` or `wes`; this repo's exact prefix is
  `com.warehouse.wes.task-interleaving`, entity `stationtrip` or `policy`). The SAME `type` names the
  occurrence on both the integration and the analytics topic; `dataschema`
  names the payload shape. Breaking payload change => new `.v2` type + new
  dataschema version, never mutate an existing one.
- Consumers decode with `cloudevents.Decode` (validates), dispatch on the
  FULL `type` string (never a short name or suffix match), ignore unknown
  types, read `time`/`subject` from attributes and the payload via
  `DataAs`, dedupe on `id`, and DLQ/skip — WARN log + commit past, never
  crash, never block the partition, never fall back to parsing a legacy
  shape — anything that fails CloudEvents validation.
- Tests: a golden exact-JSON test per published `type` (all attributes +
  the `content-type` header); a legacy-flat-message-rejected test per
  consumer; Kafka integration tests via testcontainers only.

The full standard, subdomain table and the fleet's cross-service type
catalogue live in the warehouse-docs Event Standard page (CloudEvents). This
repo records its envelope and type catalogue in
`docs/adr/0004-cloudevents-envelope-and-type-catalogue.md`;
`TestEventCatalogueMatchesContract` keeps that ADR and `apis/asyncapi.yaml` in
step.

### Published types

All four are on the analytics topic `warehouse.task-interleaving.analytics`
(`dataschema` stream `analytics`); only the first is also on the integration
topic `warehouse.task-interleaving.events` (stream `events`). Kafka key =
`subject`. Both rows of an occurrence are written by the outbox in the
aggregate's transaction with the same `id`.

| `type` | topic(s) | `subject` | `dataschema` |
| --- | --- | --- | --- |
| `com.warehouse.wes.task-interleaving.stationtrip.InterleavingHintIssued` | `warehouse.task-interleaving.events` + `warehouse.task-interleaving.analytics` | station id | `urn:warehouse:task-interleaving:<events\|analytics>:InterleavingHintIssued:v1` |
| `com.warehouse.wes.task-interleaving.stationtrip.InterleavingHintConsumed` | `warehouse.task-interleaving.analytics` | station id | `urn:warehouse:task-interleaving:analytics:InterleavingHintConsumed:v1` |
| `com.warehouse.wes.task-interleaving.stationtrip.InterleavingHintExpired` | `warehouse.task-interleaving.analytics` | station id | `urn:warehouse:task-interleaving:analytics:InterleavingHintExpired:v1` |
| `com.warehouse.wes.task-interleaving.policy.InterleavingPolicyDeclared` | `warehouse.task-interleaving.analytics` | site id | `urn:warehouse:task-interleaving:analytics:InterleavingPolicyDeclared:v1` |

`data` is snake_case with optional fields omitted; payload shapes are in the ADR
0004 table and `apis/asyncapi.yaml`. A superseded hint raises no event: the
station's next `InterleavingHintIssued` replaces it. Only
`InterleavingHintIssued` may be relied on by sibling contexts.

### Consumed types

Exact strings from the producers' catalogues (ADR 0003). Every other type on
these topics is ignored; a message that is not a valid CloudEvent is logged at
WARN and committed past.

| `type` | topic | producer |
| --- | --- | --- |
| `com.warehouse.wes.fulfillment-execution.task.TaskAvailable` | `warehouse.fulfillment.events` | fulfillment-execution (pending producer change P2) |
| `com.warehouse.wes.fulfillment-execution.task.TaskClaimed` | `warehouse.fulfillment.events` | fulfillment-execution (pending producer change P2) |
| `com.warehouse.wes.fulfillment-execution.task.TaskCompleted` | `warehouse.fulfillment.events` | fulfillment-execution (verified) |
| `com.warehouse.wms.facility-layout.zone.ZoneRegistered` | `warehouse.facility.events` | facility-layout (verified) |
| `com.warehouse.wms.facility-layout.locationslot.LocationSlotRegistered` | `warehouse.facility.events` | facility-layout (verified) |
| `com.warehouse.wms.facility-layout.locationslot.LocationSlotDecommissioned` | `warehouse.facility.events` | facility-layout (verified) |
| `com.warehouse.wms.facility-layout.locationslot.LocationGeometryUpdated` | `warehouse.facility.events` | facility-layout (verified) |

Effects: the fulfillment events feed the open-task pool (`TaskAvailable` upserts,
`TaskClaimed` removes it from the open pool and may mark the station's hint
Consumed, `TaskCompleted` closes the task and records the station position); the
facility events feed location geometry and site mapping. Each effect and the
CloudEvents `id` claim commit in ONE database transaction, then the offset is
committed. A claim or completion for a task the pool never saw is counted and
skipped, never dead-lettered.

## Consumer group id

The group ids come from the environment, never a string literal --
`internal/architecture/fitness_test.go`'s
TestKafkaConsumerGroupNeverHardcodedInline enforces this (a real incident:
wes-work-planning's hardcoded group id let a locally-run e2e-tests process
silently collide with the live in-cluster Deployment's consumer group on
the shared fleet Kafka broker).

| Concern | Mode env | Consumer group env |
| --- | --- | --- |
| Fulfillment (pool, claims, completions) | `FULFILLMENT_MODE` | `FULFILLMENT_CONSUMER_GROUP` |
| Layout (zones, slots, geometry) | `LAYOUT_MODE` | `LAYOUT_CONSUMER_GROUP` |

A mode is `kafka` or `permissive`; the default is `permissive` (no consumer
starts, the local copy is whatever is in the database). Mode `kafka` with its
group variable unset is a boot error. Both consumers are stable
process-and-commit consumers (no full replay), so a stable group id is correct;
if a consumer ever replays from FirstOffset on every start to build an in-memory
read model, its group id must additionally be UNIQUE PER PROCESS INSTANCE
(hostname+PID+timestamp). Outbox attempts: `OUTBOX_MAX_ATTEMPTS`.
