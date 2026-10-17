---
name: how-to-add-an-integration-event
description: Publish or consume a cross-service Kafka event: CloudEvents 1.0 type naming, AsyncAPI, transactional outbox, consumer-group rules. Use when touching internal/adapters kafka or outbox code, a publisher/consumer, or apis/asyncapi*.yaml.
---

# How to add an integration event (publish and consume)

Use when asked to publish a new cross-context integration event, or
consume one from a sibling bounded context. This fleet's Kafka is ONE
broker platform-wide — every design decision below exists because that
shared-broker reality has already caused a real incident once.

**State of this repo.** The contract (`apis/asyncapi.yaml`), the type catalogue
(`docs/adr/0004-cloudevents-envelope-and-type-catalogue.md`), the consumed
contracts (`docs/adr/0003-local-copies-and-consumed-contracts.md`), the
CloudEvents helper (`internal/adapters/kafka/cloudevents/cloudevents.go`) and
the Kafka writer (`internal/adapters/outbound/kafka/writer.go`) exist. The
outbox, the relay, the publisher and the consumers are built in wave 2, so those
steps say where code goes, in words.

## Publishing a new integration event

### 1. Is it actually cross-service?

Not every domain event this service raises belongs on the integration topic.
Today only `InterleavingHintIssued` is on `warehouse.task-interleaving.events`
(fulfillment-execution reads it); `InterleavingHintConsumed`,
`InterleavingHintExpired` and `InterleavingPolicyDeclared` are analytics-only,
on `warehouse.task-interleaving.analytics`. Before putting a new event on the
integration topic, confirm a sibling context genuinely needs to react to it —
check the context map in `docs/adr/0001-task-interleaving-bounded-context.md`
for who is downstream. A new integration-topic type is a new cross-repo
contract and needs an ADR (see `.claude/skills/how-to-write-an-adr/SKILL.md`).

### 2. Envelope: CloudEvents 1.0, structured mode — MANDATORY

Every message is a CloudEvents 1.0 JSON document in structured content mode,
with the Kafka header `content-type: application/cloudevents+json;
charset=UTF-8`. There is no other envelope in this fleet — no flat
`event_id`/`event_type`/`occurred_at` shape, no dual-write, no
`EVENT_ENVELOPE_MODE` toggle (see `.claude/rules/integration-events.md`;
`TestNoEventEnvelopeToggleOrFlatEnvelope` enforces it).

```json
{
  "specversion": "1.0",
  "id": "<uuid v4, minted once, persisted with the outbox row>",
  "source": "/warehouse/task-interleaving",
  "type": "com.warehouse.wes.task-interleaving.stationtrip.InterleavingHintIssued",
  "subject": "<station id>",
  "time": "<domain occurred-at, RFC3339 UTC>",
  "datacontenttype": "application/json",
  "dataschema": "urn:warehouse:task-interleaving:events:InterleavingHintIssued:v1",
  "data": { "hint_id": "...", "station_id": "...", "candidates": [] }
}
```

`type` is `com.warehouse.wes.task-interleaving.<entity>.<EventName>` where
`<entity>` is the raising aggregate, lowercase, no separators: `stationtrip` or
`policy`. The same `type` and `id` name the occurrence on both topics; only
`dataschema` changes (`…:analytics:<EventName>:v1`). `data` is snake_case with
optional fields omitted.

### 3. Implementation

The event must already exist as a domain event the aggregate raises (the
`stationtrip` package raises `HintIssued`, `HintConsumed` and `HintExpired`):
publishing maps an EXISTING domain event onto the wire, it does not invent a
payload at the adapter layer. The use case (application layer) maps the domain
event to a CloudEvent and writes the encoded rows to the transactional outbox
in the SAME transaction as the aggregate. The outbox relay (wave 2) drains the
rows through the existing writer. Rules:

- Encode ONLY through `internal/adapters/kafka/cloudevents`:
  ```go
  value, err := cloudevents.New(cloudevents.Spec{
      ID:        eventID,                       // minted once; the outbox row stores it
      Entity:    "stationtrip",
      EventName: "InterleavingHintIssued",
      Subject:   string(evt.StationID()),
      Time:      evt.OccurredAt(),
      Stream:    cloudevents.StreamEvents,      // StreamAnalytics for the analytics row
      Version:   1,
      Data:      payload,                       // snake_case wire struct, adapter-owned
  })
  msg := kafkago.Message{
      Topic:   "warehouse.task-interleaving.events",
      Key:     []byte(evt.StationID()),
      Value:   value,
      Headers: append(traceHeaders, cloudevents.ContentTypeHeader()),
  }
  ```
- `outbound/kafka.NewWriter` returns an acks=all writer with NO `Topic`: set
  `Message.Topic` per message and never add a `Topic` to the Writer
  (`TestKafkaWriterTopicNotSetTwice`, `TestKafkaWritersSetRequiredAcks`).
- The message key is the subject (the station id, or the site id for policy
  events) with the `kafkago.Hash{}` balancer, so one station's hint events stay
  ordered.
- The relay passes the attempt cap, env `OUTBOX_MAX_ATTEMPTS`
  (`TestOutboxRelaysPassAttemptCap`). Events leave ONLY via the outbox.

### 4. Contract + catalogue

- Add the message to `apis/asyncapi.yaml` with its exact `type` const and
  `dataschema`, the shared CloudEvents envelope schema with every attribute
  required, and `defaultContentType: application/cloudevents+json`.
- Add its line to the type catalogue in
  `docs/adr/0004-cloudevents-envelope-and-type-catalogue.md`:
  `TestEventCatalogueMatchesContract` fails when the ADR and the contract
  disagree.
- Lint the contract with `.spectral.asyncapi.yaml`.
- Update the published table in `.claude/rules/integration-events.md`.

### 5. Test

Add a golden exact-JSON unit test for the new `type` asserting every
CloudEvents attribute, the `type` string and the `content-type` header, against
a fake writer (see `internal/adapters/kafka/cloudevents/cloudevents_test.go` for
the shape) — never a real broker in a unit test. A test asserting real delivery
must be an integration test using testcontainers (`TestKafkaIntegrationTestsUseTestcontainers`;
a skip-gated `KAFKA_BROKERS` test or a hardcoded `localhost:9092` fails CI).

## Consuming an integration event from a sibling context

### 1. Never import the sibling's Go packages

This service knows a sibling's topic name, its exact CloudEvents `type`
strings and payload shape ONLY — never its Go types. Hand-mirror the payload
struct locally and add the consumed type to the table in
`docs/adr/0003-local-copies-and-consumed-contracts.md` and to
`apis/asyncapi.yaml`, after reading the producer's own `apis/asyncapi.yaml` on
its `origin/develop` (`git show origin/develop:apis/asyncapi.yaml` in the
sibling checkout). ADR 0003 lists what is consumed today:
fulfillment-execution's `TaskAvailable`, `TaskClaimed` and `TaskCompleted`
(`TaskAvailable` for RELOCATE tasks only), and facility-layout's `ZoneRegistered`,
`LocationSlotRegistered`, `LocationSlotDecommissioned`,
`LocationGeometryUpdated`.

### 2. Decode CloudEvents only, dispatch on the full `type`

```go
evt, err := cloudevents.Decode(msg.Value)
if err != nil { // errors.Is(err, cloudevents.ErrNotCloudEvent): deterministic poison
    logger.Warn("skipping non-CloudEvents message", "topic", msg.Topic,
        "partition", msg.Partition, "offset", msg.Offset, "err", err)
    return commit(msg) // never crash, never block the partition
}
if evt.Type() != "com.warehouse.wes.fulfillment-execution.task.TaskCompleted" {
    return commit(msg) // unknown types are ignored, not errors; exact type, byte-identical to the producer
}
var p taskCompletedData // local mirror of the payload
if err := evt.DataAs(&p); err != nil { /* poison: skip as above */ }
// claim evt.ID() and apply the effect in ONE DB transaction, THEN commit the offset
```

Fetch with `FetchMessage` and commit with `CommitMessages` only after the
effect succeeds. Never parse a legacy flat shape as a fallback and never
dispatch on a short name or suffix match. Add a test that a legacy flat-envelope
message is rejected. Never dead-letter on a cancelled context (a DLQ call is
`//dlq:deterministic` only for a poison payload; `TestDeadLetterCallsGuardCancellation`).

### 3. The consumer-group pattern and the mode switch

Both consumers here (fulfillment, layout) are **stable process-and-commit
consumers** over durable Postgres copies — Pattern A below. The group id comes
from the environment, never a literal: `FULFILLMENT_CONSUMER_GROUP` and
`LAYOUT_CONSUMER_GROUP`, next to the modes `FULFILLMENT_MODE` and `LAYOUT_MODE`
(`kafka` or `permissive`, default `permissive`; `kafka` with its group
variable unset is a boot error). See ADR 0003.

**Pattern A — long-lived, single-instance consumer group.** One instance at a
time; the group id is stable across restarts, which is correct because
committed-offset resume is exactly what you want.

**Pattern B — per-process-unique consumer group.** Use only when a consumer
rebuilds a complete read model from a topic's FULL history on every start (an
in-memory cache, not a durable table). The group id MUST be unique per process
instance (hostname+PID+timestamp), NEVER a fixed shared string: a new process
joining a group an earlier instance already consumed resumes from that
instance's committed offset, comes up "ready" with an empty cache, and replays
nothing — a silent correctness bug (`TestReplayConsumersSetCommitInterval`
covers replay consumers).

**Never do this** (the real incident, wes-work-planning#67): a fixed shared
group id on a consumer meant to run as one instance per environment. A local
process joining the SAME broker's SAME group as the live in-cluster Deployment
makes Kafka hand the partition to only ONE of them and the other silently
starves. `TestKafkaConsumerGroupNeverHardcodedInline` fails CI on an inline
`GroupID: "literal"`.

### 4. Readiness gate, only if the consumer backs an in-memory cache

Not needed for the durable copies here. If a future consumer replays a topic
to build an in-memory cache, expose a `Ready()` gate the health check consults
and block readiness (not process startup) until the initial replay finishes;
prefer the per-process-unique group, which sidesteps the deadlock where a
shared, caught-up group never receives a message to trigger readiness.

### 5. Test the consumer

Integration tests use testcontainers only: create the topic explicitly and wait
for the partition leader before the first read/write, use a unique topic per
test (`TestKafkaIntegrationTestsUseTestcontainers`,
`TestPostgresIntegrationTestsUseTestcontainers`).

## Verify before opening the PR

```bash
make check-all    # includes arch-test
make integration  # when Docker is available
```
