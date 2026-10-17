# ADR 0004: CloudEvents envelope and type catalogue

## Status

Accepted (2026-10-17).

## Context

Every Kafka message in the fleet is a CloudEvents 1.0 event in structured mode
(fleet rule `.claude/rules/fleet/cloudevents.md`, warehouse-docs
`docs/strategic-design/event-standard-cloudevents.md`). This ADR is the catalogue
of every type task-interleaving publishes or consumes;
`TestEventCatalogueMatchesContract` checks it against `apis/asyncapi.yaml`.

## Decision

### Envelope

- `specversion` `1.0`; `id` a UUID persisted with the outbox row (stable across
  relay retries; consumers dedupe on it); `source`
  `/warehouse/task-interleaving`; `subject` the aggregate id: the station id for
  `stationtrip` events, the site id for `policy` events; `time` the occurred-at
  instant in UTC; `datacontenttype` `application/json`; `dataschema`
  `urn:warehouse:task-interleaving:<events|analytics>:<EventName>:v1`.
- Kafka value = the JSON event format; header
  `content-type: application/cloudevents+json; charset=UTF-8`; Kafka key = the
  subject (`kafkago.Hash{}` balancer), so all hint events of one station stay
  ordered on one partition.
- `data` is snake_case; optional fields are omitted when unset; timestamps are
  RFC 3339 UTC.
- Built and decoded only through `internal/adapters/kafka/cloudevents`.
- A breaking payload change is a new `.v2` type and dataschema, never a mutation
  of v1.

### Published

`<entity>` is the raising aggregate, lowercase, no separators: `stationtrip` or
`policy`. The same `type` and the same `id` name an occurrence on both topics;
`dataschema` carries the stream (`events` or `analytics`). Topics:
`warehouse.task-interleaving.events` (integration) and
`warehouse.task-interleaving.analytics`. Both rows of an occurrence are written
by the transactional outbox in the transaction of the aggregate.

| type | stream | raised when | `data` |
|---|---|---|---|
| `com.warehouse.wes.task-interleaving.stationtrip.InterleavingHintIssued` | integration + analytics | a hint is issued for a station | `{hint_id, station_id, site_id, after_task_id, origin_location, expires_at, policy_version, candidates:[{rank, task_id, task_type, location, detour_m}]}` |
| `com.warehouse.wes.task-interleaving.stationtrip.InterleavingHintConsumed` | analytics only | the hinted station claims one of the candidates | `{hint_id, station_id, site_id, task_id, rank, detour_m, consumed_at}` |
| `com.warehouse.wes.task-interleaving.stationtrip.InterleavingHintExpired` | analytics only | an Active hint reaches its `expires_at` | `{hint_id, station_id, site_id, expires_at}` |
| `com.warehouse.wes.task-interleaving.policy.InterleavingPolicyDeclared` | analytics only | a site's policy is declared or replaced | `{site_id, max_detour_m, max_candidates, hint_ttl_seconds, eligible_task_types, vertical_weight, version, declared_at}` |

Payload rules:

- `InterleavingHintIssued.candidates` is non-empty and ranked from 1 by
  `detour_m` ascending then `task_id`; at most the policy's `max_candidates`.
  `expires_at` is `issued_at + hint_ttl_seconds`, with `issued_at` the CloudEvents
  `time`.
- fulfillment-execution consumes only `InterleavingHintIssued` (consumer change
  P3). It may use a hint only as a tie-break inside one priority band and must
  ignore it once `expires_at` has passed.
- A superseded hint raises no event of its own: the next
  `InterleavingHintIssued` of the station replaces it.
- `InterleavingHintConsumed`'s `rank` and `detour_m` are those of the claimed
  candidate in the hint.
- `detour_m` is rounded to 0.1 m.

### Consumed (ADR 0003)

| type | topic | consumer group env | status |
|---|---|---|---|
| `com.warehouse.wes.fulfillment-execution.task.TaskAvailable` | `warehouse.fulfillment.events` | `FULFILLMENT_CONSUMER_GROUP` | pending producer change P2 |
| `com.warehouse.wes.fulfillment-execution.task.TaskClaimed` | `warehouse.fulfillment.events` | `FULFILLMENT_CONSUMER_GROUP` | pending producer change P2 |
| `com.warehouse.wes.fulfillment-execution.task.TaskCompleted` | `warehouse.fulfillment.events` | `FULFILLMENT_CONSUMER_GROUP` | verified |
| `com.warehouse.wms.facility-layout.zone.ZoneRegistered` | `warehouse.facility.events` | `LAYOUT_CONSUMER_GROUP` | verified |
| `com.warehouse.wms.facility-layout.locationslot.LocationSlotRegistered` | `warehouse.facility.events` | `LAYOUT_CONSUMER_GROUP` | verified |
| `com.warehouse.wms.facility-layout.locationslot.LocationSlotDecommissioned` | `warehouse.facility.events` | `LAYOUT_CONSUMER_GROUP` | verified |
| `com.warehouse.wms.facility-layout.locationslot.LocationGeometryUpdated` | `warehouse.facility.events` | `LAYOUT_CONSUMER_GROUP` | verified |

Every other type on those topics is ignored. A message that is not a valid
CloudEvent is logged and skipped, never retried forever.

### Analytics topic

`warehouse.task-interleaving.analytics` carries all four published types. Only
this service's own analytics read side is meant to consume it (a later phase);
other contexts consume the integration topic.

## Consequences

- Consumers need nothing but this catalogue and `apis/asyncapi.yaml`; there is no
  shared Go code.
- Keeping hint outcomes (`Consumed`, `Expired`) and the policy declaration off
  the integration topic keeps the integration contract to the single message a
  sibling acts on, while the analytics topic can measure hint acceptance.
- Two outbox rows per analytics-visible occurrence (one per topic) are written in
  one transaction, so the two topics cannot disagree about whether an occurrence
  happened.
