---
id: entity-relationship
title: Entity-relationship diagram
sidebar_label: Entity-relationship
sidebar_position: 18
---

# Entity-relationship diagram

The Postgres schema of the service as created by the single embedded migration,
`migrations/0001_interleaving_schema.up.sql` ([ADR 0005](/docs/adr/0005-persistence-and-evaluation-semantics)).
Postgres is the only runtime ([ADR 0006](/docs/adr/0006-postgres-and-kafka-are-the-only-runtime)); the in-memory test doubles keep the same shapes in process memory.

```mermaid
erDiagram
    interleaving_policies {
        text site_id PK
        float max_detour_m
        int max_candidates
        bigint hint_ttl_ns
        text_array eligible_task_types
        float vertical_weight
        bigint version
        timestamptz declared_at
    }
    station_trips {
        text station_id PK
        text site_id
        text location_code
        float pos_x
        float pos_y
        float pos_z
        text last_task_id
        timestamptz last_completed_at
        text current_hint_id FK
        bigint version
        timestamptz updated_at
    }
    hints {
        text hint_id PK
        text station_id FK
        text site_id
        text after_task_id
        text origin_location
        timestamptz issued_at
        timestamptz expires_at
        bigint policy_version
        text state
        text consumed_task_id
        timestamptz consumed_at
        timestamptz superseded_at
        bigint version
        timestamptz updated_at
    }
    candidates {
        text hint_id PK
        int rank PK
        text task_id
        text task_type
        text location
        float detour_m
    }
    task_pool {
        text task_id PK
        text task_type
        text site_id
        text from_location
        text to_location
        text_array required_capabilities
        text priority_band
        text state
        text claimed_by
        timestamptz updated_at
    }
    zones {
        text zone_id PK
        text site_code
        text zone_code
        timestamptz updated_at
    }
    locations {
        text location_code PK
        text zone_id
        boolean decommissioned
        float x_m
        float y_m
        float z_m
        timestamptz updated_at
    }
    processed_events {
        text consumer PK
        text event_id PK
        timestamptz processed_at
    }
    outbox_events {
        bigserial id PK
        text event_id
        text topic
        text event_type
        text subject
        bytea key
        text dataschema
        bytea value
        jsonb headers
        timestamptz created_at
        timestamptz published_at
        int attempts
        text last_error
    }
    idempotency_keys {
        text key PK
        text fingerprint
        int status
        text content_type
        bytea body
        timestamptz created_at
    }
    station_trips ||--o{ hints : "issues"
    hints ||--o{ station_trips : "current_hint_id, deferred"
    hints ||--|{ candidates : "ranks 1..n"
    locations }o--o| zones : "zone_id, joined at read time"
```

Source: `migrations/0001_interleaving_schema.up.sql` (every column, key and index
below), `internal/adapters/outbound/postgres/*.go` for the access paths. Types are
shortened (`text COLLATE "C"` is shown as `text`).

## Analytical database

The hint-adoption read model ([ADR 0008](/docs/adr/0008-analytics-read-side))
is a separate database (`task_interleaving_analytics`), written only by
`cmd/ti-projector` from `warehouse.task-interleaving.analytics` and read by
`cmd/ti-reports`. Facts, not aggregates: it can be dropped and rebuilt by
replaying the topic under a new consumer group.

```mermaid
erDiagram
    analytics_processed_events {
        text event_id PK "every applied CloudEvents id, claimed with its effect"
        text event_type
        timestamptz occurred_at "indexed; max() is the projection's freshness"
        timestamptz applied_at
    }
    hint_facts {
        text hint_id PK
        text station_id
        text site_id
        timestamptz issued_at "nullable; earliest InterleavingHintIssued seen"
        timestamptz expires_at "nullable"
        int candidate_count "nullable"
        timestamptz consumed_at "nullable; earliest InterleavingHintConsumed"
        int consumed_rank "nullable"
        float consumed_detour_m "nullable"
        timestamptz expired_at "nullable"
    }
```

Each milestone column keeps the CloudEvents time of the earliest such event
(`LEAST` ignores NULLs), so arrival order never matters; a hint superseded by
a newer one has no event of its own and stays without a `consumed_at` or
`expired_at`. No foreign keys. Source: `analytics/migrations/0001_hint_adoption.up.sql`.

## Reading it

| Table | Kind | Notes |
| --- | --- | --- |
| `interleaving_policies` | aggregate root, one row per site | `version` starts at 1 and guards every update (`WHERE version = <loaded>`). `hint_ttl_ns` keeps the `time.Duration` exactly. A site without a row is **inert**. Checks: detour above 0, candidates 1 to 5, TTL above 0, at least one task type, weight at least 0. |
| `station_trips` | aggregate root, one row per station | Written only together with the first completion. The point `pos_x/y/z` is all-or-nothing (CHECK). `current_hint_id` points at the most recent hint in any state through a **deferrable** foreign key, so a trip and its new hint are written in one transaction in either order. |
| `hints` | the hint history | One row per hint ever issued; `state` is `Active`, `Consumed`, `Expired` or `Superseded` (CHECK). The unique partial index `uq_hints_one_active_per_station` allows at most one `Active` hint per station: the database's own guarantee behind the aggregate's invariant. `consumed_task_id` and `consumed_at` are set exactly when the state is `Consumed`. `expires_at > issued_at`. |
| `candidates` | value objects of a hint | Primary key `(hint_id, rank)`, unique `(hint_id, task_id)`, deleted with the hint (`ON DELETE CASCADE`), immutable once the hint exists. |
| `task_pool` | event-fed read model | Keyed by task id (ADR 0003). `state` is `AVAILABLE`, `CLAIMED` or `COMPLETED`. A task learned only from a claim or a completion is minimal (empty site and locations). `priority_band` is carried and never interpreted. Index `idx_task_pool_available (site_id, task_id) WHERE state = 'AVAILABLE'` serves the recommender. |
| `zones`, `locations` | event-fed read model | A location's site is found at read time through its zone, so zone, slot and geometry events may arrive in any order. `decommissioned` is irreversible and the row stays, so a late registration cannot bring the location back. The point `x_m/y_m/z_m` is all-or-nothing; a location with no point has an unknown distance. |
| `processed_events` | consumer idempotency | `(consumer, event_id)` is claimed with `INSERT ... ON CONFLICT DO NOTHING` in the **same** transaction as the effect, so a rollback un-claims it. |
| `outbox_events` | transactional outbox | One row per already-encoded Kafka message and destination topic; unique `(event_id, topic)`; partial index `idx_outbox_events_unpublished` keeps the relay's scan tiny. `attempts` and `last_error` carry the relay's failures. |
| `idempotency_keys` | stored answers | Only 2xx and 4xx answers are stored (CHECK `status` 200 to 499), first writer wins. |

The link from a policy to a trip is by site, not by a foreign key: `IssueHint`
refuses a policy whose site is not the station's site (`ErrSiteMismatch`), and the
use case supplies the policy. No code issues a `DELETE` on any table (the
`candidates` cascade only fires if an operator deletes a hint).
