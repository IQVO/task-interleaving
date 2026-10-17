# ADR 0003: event-fed local copies and the consumed contracts

## Status

Accepted (2026-10-17). The `permissive` mode and its default in "Switches and
consumer groups" below are superseded by
[ADR 0006](0006-postgres-and-kafka-are-the-only-runtime.md): every consumer is
always on and `permissive` is rejected at boot.

## Context

A hint needs three kinds of facts that other contexts own: which tasks are open
and where they start (fulfillment-execution), which station claimed or
completed what (fulfillment-execution), and where locations are (facility-layout
geometry and zones). ADR 0001 forbids live cross-context lookups, so
task-interleaving keeps local, event-fed copies and computes hints from them.
This ADR records exactly what is consumed, how the copies converge, and the
switches that control them. The fulfillment-execution `TaskCompleted` and every
facility-layout contract below were read from the producers' `apis/asyncapi.yaml`
on `origin/develop` on 2026-10-17. `TaskAvailable` and `TaskClaimed` do **not**
exist on fulfillment-execution's integration topic yet: they are pinned here and
in `apis/asyncapi.yaml` so this context ships first and the producer matches
(producer change P2, consumer-first).

## Decision

### Consumed contracts

| Topic | Full `type` | Kafka key | Fields used | Status |
|---|---|---|---|---|
| `warehouse.fulfillment.events` | `com.warehouse.wes.fulfillment-execution.task.TaskAvailable` | task id (= subject) | `task_id, task_type, site_id, from_location, to_location, required_capabilities[], cpt?, priority_band, available_at` | **pending producer change P2** |
| `warehouse.fulfillment.events` | `com.warehouse.wes.fulfillment-execution.task.TaskClaimed` | task id | `task_id, task_type, station_id, claimed_at` (today analytics-only without `claimed_at`) | **pending producer change P2** |
| `warehouse.fulfillment.events` | `com.warehouse.wes.fulfillment-execution.task.TaskCompleted` | task id | `task_id, station_id` (the producer requires `task_id, station_id, work_unit_id`; every other field is ignored) | verified |
| `warehouse.facility.events` | `com.warehouse.wms.facility-layout.zone.ZoneRegistered` | `zoneId` | `zoneId, siteCode` | verified |
| `warehouse.facility.events` | `com.warehouse.wms.facility-layout.locationslot.LocationSlotRegistered` | `locationCode` | `locationCode, zoneId` | verified |
| `warehouse.facility.events` | `com.warehouse.wms.facility-layout.locationslot.LocationSlotDecommissioned` | `locationCode` | `locationCode` | verified |
| `warehouse.facility.events` | `com.warehouse.wms.facility-layout.locationslot.LocationGeometryUpdated` | `locationCode` | `locationCode, xM, yM, zM` (footprint and `pickSequence` ignored) | verified |

**Contradiction with the plan, fixed here.** The plan's table listed
`TaskCompleted` as "only `task_id` required" and reading the end position from the
pool copy. The producer's contract also requires `station_id` and carries it on
every completion; without it a completion cannot be attributed to a station. This
context reads `station_id` from the event and the end position from the pool
copy.

Every other type on those topics is ignored. facility-layout `data` also carries
`eventName`, `eventType` and `occurredAt`; they are decoded and ignored. A
message that is not a valid CloudEvent is logged at WARN and committed past.
Dispatch is on the FULL `type`.

### How each copy converges

All copies are durable Postgres tables fed by a normal process-and-commit
consumer under a **stable** consumer group (no full replay). The CloudEvents
`id` claim and the effect commit in ONE database transaction; the Kafka offset is
committed only afterwards.

- **Open-task pool** (`FULFILLMENT_*`). Keyed by `task_id`. `TaskAvailable`
  upserts the row (a task that re-enters the pool, for example after a lease
  expiry, replaces it); `TaskClaimed` marks it claimed and removes it from the
  open pool (the row is kept, so the end location of a task that is completed
  later is still known); `TaskCompleted` closes it. One task's three messages
  share a key, hence a partition, hence an order. A message for a task the copy
  never saw (a claim or completion before the first `TaskAvailable`, or a
  completion of a task released before this context existed) is counted and
  skipped, never dead-lettered: with no row there is no position to record.
- **Claims.** `TaskClaimed` also drives the StationTrip: when the claimed task is
  a candidate of the Active hint of `station_id` and that hint is unexpired, the
  hint is marked Consumed. Any other claim leaves the hint Active.
- **Station position.** `TaskCompleted` records the completion at `station_id`:
  the position is the completed task's `to_location` read from the pool copy and
  the site is the task's `site_id`. A completion that is not strictly newer than
  the recorded one is ignored.
- **Location geometry** (`LAYOUT_*`). Points are keyed by `locationCode`,
  last writer wins (one location's events share a key and so an order). A
  location's site is found at read time through `LocationSlotRegistered.zoneId`
  and `ZoneRegistered.siteCode`, so a zone, a slot and a geometry may arrive in
  any order (convergent). `LocationSlotDecommissioned` deletes the location and
  its point (irreversible). A location with no geometry has an **unknown**
  distance: it is never a candidate and, as the origin, yields no hint.

The copies are independent: a hint reads whatever each has.

### Switches and consumer groups

| Concern | Mode env | Consumer group env |
|---|---|---|
| Fulfillment (pool, claims, completions) | `FULFILLMENT_MODE` | `FULFILLMENT_CONSUMER_GROUP` |
| Layout (zones, slots, geometry) | `LAYOUT_MODE` | `LAYOUT_CONSUMER_GROUP` |

- A mode is `kafka` or `permissive`; **default `permissive`** (no consumer starts,
  the copy is whatever is in the database, which keeps tests and local runs free
  of a broker). The cluster sets `kafka`.
- Consumer groups come from the environment, never a string literal at the call
  site (enforced by `TestKafkaConsumerGroupNeverHardcodedInline`). **Mode `kafka`
  with the group variable unset is a boot error**, not a silent no-op.

### Live-data finding rule

The assumptions above are about data this context has not seen. Before the first
live hint, in the wiring wave, someone runs read-only SQL on the service's own
database and answers from rows, not from this ADR: (1) do the pool's `site_id`
values equal the facility `siteCode` values (v1 assumes one code space)?
(2) what share of pool task locations has a geometry point? (3) is the geometry
frame the same for every location of a site? A hint over data that fails these
checks is not wrong; it is absent or short, and the recommender skips what it
cannot measure.

## Consequences

- No synchronous dependency on sibling contexts; hint computation is a local read.
- The copies need no schema beyond what each event carries and are rebuilt by
  replaying their topics.
- Hints are only as fresh as the pool. The hint TTL bounds the staleness a
  station can see; a hint pointing at a task claimed elsewhere simply loses the
  tie-break in fulfillment-execution, which re-checks the claim.
- Until producer change P2 merges, the pool is empty and no hint is issued:
  accepted, and the reason this ADR pins the contract now.
