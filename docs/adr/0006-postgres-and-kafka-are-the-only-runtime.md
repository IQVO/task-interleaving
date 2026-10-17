# ADR 0006: Postgres and Kafka are the only runtime

## Status

Accepted (2026-10-17). Supersedes the in-memory and `permissive` statements of
[ADR 0003](0003-local-copies-and-consumed-contracts.md) and
[ADR 0005](0005-persistence-and-evaluation-semantics.md). Amended the same day
by [Amendment 1](#amendment-1-the-fleet-topic-convention) (topics, readiness and
the relay writer): where the text below says infrastructure owns the topics or
that `/readyz` needs them, the amendment governs.

## Context

The service shipped with three ways to run without its infrastructure: without
`DATABASE_URL` `cmd/api` logged "using in-memory adapters" and served from
memory (no outbox, no relay, no `Idempotency-Key`); the two consumers were off
unless `*_MODE=kafka` (`permissive`, the default, started nothing); and the
outbox relay was off unless `KAFKA_BROKERS` was set. `/readyz` reported only
the shutdown drain gate. A deployment that missed one variable therefore came
up "healthy" and silently lost every hint it was meant to publish and every
fact it was meant to consume. The decision of the owner is final: the service
runs on a real database and real Kafka, wired for real.

## Decision

- **One runtime.** The shipped binary runs on Postgres and Kafka only. The
  in-memory adapters (`internal/adapters/outbound/memory`) stay as test doubles
  for unit and BDD tests. `TestRuntimeBinaryHasNoInMemoryAdapters` fails the
  build when a non-test file under `cmd/**` imports them, and a fixtures test
  proves the sensor bites. The production clock and hint-id generator moved to
  `internal/adapters/outbound/system` so `cmd/**` does not need the memory
  package for them.
- **Mandatory configuration, boot error otherwise.** `DATABASE_URL`,
  `KAFKA_BROKERS`, `FULFILLMENT_CONSUMER_GROUP` and `LAYOUT_CONSUMER_GROUP`
  have no default. A missing one is a boot error that names the variable
  (`X is required: this context runs on real Postgres|Kafka`), all problems
  reported together, raised before any connection is opened and before the
  listener binds. A bad `HINT_EXPIRY_INTERVAL` keeps being a boot error in the
  same pass. Group ids still come from the environment, never from a literal.
- **Consumers are always on.** The variables `FULFILLMENT_MODE` and
  `LAYOUT_MODE` stay (the fleet documents and the template rule files name
  them) but the default is `kafka`, and `permissive` is rejected at boot with
  `<NAME>=permissive is not supported: this context runs on real Kafka`. Any
  other value is a boot error. Dead-lettering, the shutdown guard and the offset
  semantics (`FetchMessage` then `CommitMessages`) are unchanged. A consumer is
  also supervised: when kafka-go reports a group-join failure that outlasted its
  attempts (an unreachable broker) `Run` returns, and the consumer is run again
  after one second (log `consumer stopped; restarting`) until the service
  stops, so a broker outage at any time never leaves a consumer dead while
  `/readyz` turns green again.
- **The expiry worker and the relay are always on.** `HINT_EXPIRY_INTERVAL`
  keeps its default of 5 s; `OUTBOX_MAX_ATTEMPTS` keeps its default of 20. A
  relay that cannot be built (no outbox store, no brokers) fails the boot. The
  shutdown order is unchanged: `/readyz` flips, HTTP drains, the consumers stop,
  the expiry worker stops, the relay stops, the pool closes.
- **`/readyz` is real.** *(Amended: the Kafka part is reachability only, see
  Amendment 1.)* It is ready only when the drain gate is open AND
  Postgres answers a ping AND a Kafka metadata request succeeds with a leader
  for every topic the service consumes or produces (`warehouse.fulfillment.events`,
  `warehouse.facility.events`, `warehouse.task-interleaving.events`,
  `warehouse.task-interleaving.analytics`). The verdict is cached for 3 seconds
  and one round of checks is bounded by 2 seconds, so probes cannot hammer
  either dependency. A failed dependency is `503` `application/problem+json`
  with the new slug `dependency-unavailable`, naming the dependency (`not ready:
  kafka`) and never its cause (the cause is logged). The drain answer is
  unchanged (`503 {"status":"not_ready"}`). `/healthz` stays liveness only and
  does not look at any dependency.
- **Migrations at boot stay**, with backoff (`internal/bootretry`) and the
  `MIGRATIONS_DATABASE_URL` fallback.
- **The service never creates topics.** *(Amended: the statement that
  infrastructure owns them and that a missing topic makes `/readyz` fail is
  superseded by Amendment 1.)* Infrastructure owns them. At boot the
  service logs the topics it consumes and produces. A topic that does not exist
  yet makes `/readyz` report `kafka` not ready and nothing else; the process
  keeps running.
- **`Idempotency-Key` is required** on `POST /recommendations` in every
  deployment, so the OpenAPI parameter is now `required: true` (it was optional
  only because of the in-memory mode).

## Missing topics: what kafka-go does (verified)

*(Superseded in part by Amendment 1: the `503` on missing topics and the
"infrastructure creates the topics" step below describe the first version. The
consumer and relay observations still hold.)*

The end-to-end test boots the real entry point against a broker with topic
auto-creation off, before any topic exists:

- `/readyz` is `503 dependency-unavailable` ("not ready: kafka") and no topic is
  created: the readiness check asks for metadata of the named topics without
  `AllowAutoTopicCreation`.
- The consumers do not crash: kafka-go keeps retrying the group join. It does
  that silently, so the consumers install an `ErrorLogger` and every retry is an
  ERROR log line (`kafka reader error; retrying`, with the consumer and the
  topic).
- Once infrastructure creates the topics the same process becomes ready and
  consumes from the first offset, with no restart.

The relay writes with `Message.Topic`; a write that cannot be delivered (a topic
that does not exist, or an unreachable broker) fails and the row's `attempts`
grows. Observed with the broker unreachable and the default 1 s interval: the
first row reached the cap of 20 after about 20 s, the relay logged `row reached
the attempt cap and is excluded from future claims` and left the row in
`outbox_events`. Rows at the cap (`OUTBOX_MAX_ATTEMPTS`) are kept and excluded
from claims; an operator re-arms them with `UPDATE outbox_events SET attempts =
0 WHERE id = ...` once the cause is fixed. An outage longer than the cap times
the relay interval is therefore an incident that needs that step; the cap is
unchanged by this ADR.

## Consequences

- A misconfigured deployment now fails to boot (or reports not ready) instead of
  running without a relay or consumers.
- Local runs need Postgres and Kafka: `make run-local` brings both up with
  `docker-compose.yml` (a job pre-creates the four topics locally) and runs the service on
  them.
- The unit and BDD suites keep using the in-memory doubles; no `cmd` test relies
  on an in-memory runtime of the binary. The command tests are pure
  configuration tests or run the real entry point on testcontainers.
- The `permissive` mode of ADR 0003 and the in-memory development mode of ADR
  0005 no longer exist. Their bodies stay as written, with a pointer here.

## Amendment 1: the fleet topic convention

Found before the first deployment. The fleet convention (warehouse-infra
`terraform/kafka.tf`) is that **the broker auto-creates every business topic on
first write** (`num.partitions=8`), infrastructure declares **no** topics, and
every service's Writer sets `AllowAutoTopicCreation: true`. The first version of
this ADR assumed the opposite (infrastructure owns the topics) and broke the
convention twice:

1. The relay's writer (`NewWriter`) did not set `AllowAutoTopicCreation`, so on
   the cluster the service's own topics (`warehouse.task-interleaving.events`
   and `warehouse.task-interleaving.analytics`) were never created: the relay
   failed with `UnknownTopicOrPartition` and rows sat at the attempt cap.
2. `/readyz` required every consumed AND produced topic to exist with a leader.
   On a fresh cluster the output topics do not exist until the service first
   publishes, and a not-ready pod receives no traffic, so it could never be
   driven to publish: a deadlock. The consumed topics
   (`warehouse.fulfillment.events`, `warehouse.facility.events`) only exist once
   their producers ran, an ordering coupling no other service has.

Decision (this replaces the topic statements above, the rest stands):

- **The relay writer sets `AllowAutoTopicCreation: true`** (`NewWriter`, next to
  the DLQ writers that already did), with a unit test
  (`TestNewWriterAllowsAutoTopicCreation`). The service still never calls a
  topic-admin API (`CreateTopics` and the like): the broker creates a topic when
  the first message is written to it.
- **`/readyz` = drain gate AND Postgres ping AND a Kafka broker answers a
  metadata request.** The request names no topics (an empty, non-nil list; a nil
  list would mean all topics), so it is a reachability check, cheap, and asking
  never creates a topic. A missing or leaderless topic, consumed or produced, is
  **not** a readiness failure. The 3 second cache, the 2 second bound, the `503`
  `application/problem+json` with the `dependency-unavailable` slug (`not ready:
  kafka`, never the cause) and the drain answer are unchanged.
- **A missing consumed topic shows as consumer retry logs**, not as unreadiness:
  kafka-go keeps retrying the group join and the `ErrorLogger` turns each retry
  into an ERROR line (`kafka reader error; retrying`, with the consumer and the
  topic). Once the producer has written, the topic exists and the same process
  consumes from the first offset with no restart. The boot line `kafka topics
  (...)` stays, informational only.
- **Local runs** (`docker-compose.yml`) still pre-create the four topics with a
  job; that is a local convenience, not the contract.
- **The end-to-end test** keeps auto-creation OFF in its testcontainers broker so
  it controls when a topic appears: `/readyz` is `200` while the topics do not
  exist and the broker is up, then the test creates the topics and the existing
  flow runs unchanged. `/readyz` still reports `503` while Kafka is unreachable
  (`TestServiceStaysAliveButNotReadyWhileKafkaIsUnreachable`). That
  `AllowAutoTopicCreation` takes effect against a broker with auto-creation
  enabled is covered at unit level only (the shared test broker is configured
  once per binary); the cluster is where it is proven.

Consequences: a fresh cluster deploys in any order and the pod becomes ready
without waiting for any producer; a topic missing for long is visible in the
consumer logs rather than by taking the pod out of rotation.
