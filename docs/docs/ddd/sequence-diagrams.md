---
id: sequence-diagrams
title: Sequence diagrams
sidebar_label: Sequence diagrams
sidebar_position: 19
---

# Sequence diagrams

UML sequence diagrams of the key scenarios, with error branches. Every participant
is code on `develop`; where a message names a Go function it is a function that
exists, and the order of steps follows the use cases in
`internal/application/usecases`.

## Complete a task, issue a hint

```mermaid
sequenceDiagram
    autonumber
    participant FE as fulfillment-execution
    participant K as inbound/kafka fulfillment consumer
    participant UC as ApplyTaskCompleted
    participant DB as Postgres + copies
    participant T as StationTrip
    participant R as recommend.Recommend
    participant OB as outbox_events

    FE->>K: TaskCompleted task_id, station_id
    K->>K: cloudevents.Decode, dispatch on full type
    alt not a valid CloudEvent
        K-->>K: WARN log, commit past
    end
    K->>UC: Handle event id, task id, station id, time
    UC->>DB: begin unit of work, claim event id in processed_events
    alt event id already claimed
        UC-->>K: outcome duplicate
    end
    UC->>DB: read task copy
    alt task already COMPLETED
        UC-->>K: outcome stale
    end
    UC->>DB: save task as COMPLETED
    alt no station, or no to_location or site_id in the copy
        UC-->>K: outcome ignored, no position
    end
    UC->>T: RecordCompletion task, at, Position
    alt not strictly newer than recorded
        T-->>UC: false, outcome stale
    else accepted
        T-->>UC: true, version + 1
    end
    UC->>DB: read the site policy
    alt site has no policy
        UC-->>K: inert, position recorded, no hint
    else policy declared
        UC->>R: Recommend site, origin, available pool, policy, completed, model
        alt origin without geometry
            R-->>UC: ErrOriginUnknown, no hint
        else candidates computed
            R-->>UC: ranked candidates, possibly none
        end
        alt at least one candidate
            UC->>T: IssueHint id, candidates, policy, now
            T-->>UC: HintIssued, previous Active becomes Superseded
        else no candidate
            UC-->>K: no hint, an older Active hint expires at its TTL
        end
    end
    UC->>DB: save trip guarded by the loaded version
    UC->>OB: InterleavingHintIssued for the events and analytics topics
    UC->>DB: commit, one transaction for claim, copy, trip and outbox rows
    K->>K: commit the Kafka offset
    OB-->>FE: relay publishes to warehouse.task-interleaving.events
```

Source: `internal/application/usecases/apply_task_completed.go`, `intake.go`,
`ranker.go`, `StationTrip.RecordCompletion`, `StationTrip.IssueHint`,
`recommend.Recommend`, `internal/adapters/inbound/kafka/consumer.go`,
`internal/adapters/outbound/events/sink.go`. A transient failure rolls the unit of
work back (which un-claims the event id), the consumer retries the same message
with backoff and, after 5 attempts, publishes it to `warehouse.fulfillment.events.dlq`.
A save that loses a version race is such a transient failure. The relay publishes
only when `DATABASE_URL` and `KAFKA_BROKERS` are set.

## The hinted station claims a candidate

```mermaid
sequenceDiagram
    autonumber
    participant FE as fulfillment-execution
    participant K as inbound/kafka fulfillment consumer
    participant UC as ApplyTaskClaimed
    participant DB as Postgres + copies
    participant T as StationTrip
    participant OB as outbox_events

    FE->>K: TaskClaimed task_id, task_type, station_id, claimed_at
    K->>UC: Handle event id, claim
    alt task_id blank or station id invalid
        UC-->>K: ErrInvalidEvent, dead-lettered at once
    end
    UC->>DB: claim event id, read task copy
    alt task already COMPLETED
        UC-->>K: outcome stale
    end
    UC->>DB: save task as CLAIMED by the station
    UC->>DB: load the station trip
    alt station has no trip
        UC-->>K: applied, nothing to consume
    end
    UC->>T: RecordClaim task, claimed_at
    alt no Active hint, claim before issue, at or after expiry, or not a candidate
        T-->>UC: false, nothing changes
    else candidate claimed in time
        T-->>UC: true, HintConsumed with rank and detour
        UC->>DB: save trip
        UC->>OB: InterleavingHintConsumed, analytics topic only
    end
```

Source: `internal/application/usecases/apply_task_claimed.go`,
`StationTrip.RecordClaim`, `apis/asyncapi.yaml` (`TaskClaimed`) and fulfillment-execution's
`apis/asyncapi.yaml` (the producer, with `claimed_at`).

## A hint expires

```mermaid
sequenceDiagram
    autonumber
    participant W as inbound/expiry worker
    participant UC as ExpireHints
    participant DB as Postgres
    participant T as StationTrip
    participant OB as outbox_events

    loop every HINT_EXPIRY_INTERVAL, first sweep at start-up
        W->>UC: Handle
        UC->>DB: ExpiredActive now, limit 200
        loop each station, own unit of work
            UC->>DB: load the trip
            UC->>T: Expire now
            alt no Active hint, or now before expires_at
                T-->>UC: false, nothing changes
            else Active and due
                T-->>UC: true, HintExpired
                UC->>DB: save trip guarded by the loaded version
                alt lost a version race
                    UC-->>UC: skipped until the next sweep
                else committed
                    UC->>OB: InterleavingHintExpired, analytics topic only
                end
            end
        end
        UC-->>W: expired count and joined errors
        W->>W: log hints expired or hint expiry sweep failed
    end
```

Source: `internal/adapters/inbound/expiry/worker.go`,
`internal/application/usecases/expire_hints.go`, `StationTrip.Expire`. The worker
runs in both storage modes.

## Declare a policy

```mermaid
sequenceDiagram
    autonumber
    participant O as Operator
    participant H as inbound/http
    participant UC as DeclarePolicy
    participant DB as Postgres
    participant P as policy.Policy
    participant OB as outbox_events

    O->>H: PUT /sites/siteId/policy with If-Match
    alt body malformed, unknown field, required field missing, ttl too large
        H-->>O: 400 malformed-request or invalid-policy
    end
    H->>UC: Handle site, settings, If-Match
    UC->>DB: begin unit of work, load current policy
    alt policy exists and If-Match missing or stale
        UC-->>O: 412 version-mismatch
    else no policy and an If-Match was sent
        UC-->>O: 412 version-mismatch
    end
    alt first declaration
        UC->>P: New site, settings
    else replacement
        UC->>P: WithChanges settings
    end
    alt invalid settings
        P-->>UC: policy error
        UC-->>O: 400 invalid-policy or invalid-task-type
    else valid
        P-->>UC: Policy at version n
        UC->>DB: save guarded by the loaded version
        alt lost a race at the database
            UC-->>O: 409 concurrent-modification
        else saved
            UC->>OB: InterleavingPolicyDeclared, analytics topic only
            UC-->>O: 200 with ETag version
        end
    end
```

Source: `internal/application/usecases/declare_policy.go`,
`internal/adapters/inbound/http/policy_handlers.go`, `errors.go`, `policy.New`,
`Policy.WithChanges`, `apis/openapi.yaml` (`declareInterleavingPolicy`). The
`PUT` is not wrapped by the `Idempotency-Key` middleware; the version check is its
concurrency control.

## Simulate a recommendation

```mermaid
sequenceDiagram
    autonumber
    participant O as Operator or agent
    participant H as inbound/http
    participant I as Idempotency middleware
    participant UC as Recommend
    participant R as recommend.Recommend

    O->>H: POST /recommendations stationId, fromLocation optional
    H->>I: Postgres mode only
    alt Idempotency-Key missing or over 128 characters
        I-->>O: 400 idempotency-key-required
    else key already stored
        I-->>O: stored answer replayed, or 422 idempotency-key-reused for another body
    end
    I->>UC: Handle station, fromLocation, inside a unit of work
    alt no fromLocation and no recorded position
        UC-->>O: 422 station-position-unknown
    else origin without geometry or site
        UC-->>O: 422 origin-unknown
    else site has no policy
        UC-->>O: 422 policy-not-declared
    else
        UC->>R: Recommend, read-only
        alt origin without geometry
            R-->>UC: ErrOriginUnknown
            UC-->>O: 422 origin-unknown
        else
            R-->>UC: ranked candidates, possibly none
            UC-->>I: 200, nothing issued, nothing published
            I-->>O: 200 and the answer is stored under the key
        end
    end
```

Source: `internal/application/usecases/recommend.go`,
`internal/adapters/inbound/http/recommend_handler.go`, `idempotency.go`,
`recommend.Recommend`, `apis/openapi.yaml` (`recommendTasks`). A 4xx answer is
rolled back and stored on its own so the same key replays the same rejection; a 5xx
is never stored. In-memory mode skips the middleware.
