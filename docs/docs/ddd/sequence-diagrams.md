---
id: sequence-diagrams
title: Sequence diagrams
sidebar_label: Sequence diagrams
sidebar_position: 19
---

# Sequence diagrams

UML sequence diagrams of the key scenarios, with error branches. The domain calls
are real (`internal/domain`); the participants labelled *designed* (consumers, use
cases, repositories, outbox, REST adapter) are not on `develop`. Where a message
names a Go function it is a function that exists.

## Complete a task, issue a hint

```mermaid
sequenceDiagram
    autonumber
    participant FE as fulfillment-execution
    participant K as Kafka consumer (designed)
    participant DB as Postgres + copies (designed)
    participant UC as use case (designed)
    participant T as StationTrip
    participant R as recommend.Recommend
    participant OB as outbox (designed)

    FE->>K: TaskCompleted task_id, station_id
    K->>K: cloudevents.Decode, dispatch on full type
    alt not a valid CloudEvent
        K-->>K: WARN log, commit past
    end
    K->>DB: claim CloudEvents id, load task copy, policy, trip
    alt task never seen in the pool
        K-->>K: counted and skipped, no dead letter
    end
    K->>UC: record completion
    UC->>T: RecordCompletion task, at, Position
    alt not strictly newer than recorded
        T-->>UC: false, nothing changes
    else accepted
        T-->>UC: true, version + 1
    end
    alt site has no policy
        UC-->>K: inert, no hint
    else policy declared
        UC->>R: Recommend site, origin, pool, policy, completed, model
        alt origin without geometry
            R-->>UC: ErrOriginUnknown, no hint
        else candidates computed
            R-->>UC: ranked candidates, possibly none
        end
        alt at least one candidate
            UC->>T: IssueHint id, candidates, policy, now
            T-->>UC: HintIssued, previous Active becomes Superseded
            UC->>OB: InterleavingHintIssued for events and analytics topics
        else no candidate
            UC-->>K: no hint, an older Active hint expires at its TTL
        end
    end
    UC->>DB: save aggregate and outbox rows in ONE transaction
    K->>K: commit the Kafka offset
    OB-->>FE: relay publishes to warehouse.task-interleaving.events
```

Source: `StationTrip.RecordCompletion`, `StationTrip.IssueHint`, `recommend.Recommend`,
`cloudevents.Decode`, ADR 0002, ADR 0003, ADR 0004.

## The hinted station claims a candidate

```mermaid
sequenceDiagram
    autonumber
    participant FE as fulfillment-execution
    participant K as Kafka consumer (designed)
    participant UC as use case (designed)
    participant T as StationTrip
    participant OB as outbox (designed)

    FE->>K: TaskClaimed task_id, station_id, claimed_at (pending P2)
    K->>UC: record claim
    UC->>T: RecordClaim task, claimed_at
    alt no Active hint, claim before issue, at or after expiry, or not a candidate
        T-->>UC: false, nothing changes
    else candidate claimed in time
        T-->>UC: true, HintConsumed with rank and detour
        UC->>OB: InterleavingHintConsumed, analytics topic only
    end
```

Source: `StationTrip.RecordClaim`, `apis/asyncapi.yaml` (`TaskClaimed`).

## A hint expires

```mermaid
sequenceDiagram
    autonumber
    participant C as caller with a clock (undecided)
    participant T as StationTrip
    participant OB as outbox (designed)

    C->>T: Expire now
    alt no Active hint, or now before expires_at
        T-->>C: false, nothing changes
    else Active and due
        T-->>C: true, HintExpired
        C->>OB: InterleavingHintExpired, analytics topic only
    end
```

Source: `StationTrip.Expire`. The caller is not named by any ADR or contract.

## Declare a policy

```mermaid
sequenceDiagram
    autonumber
    participant O as Operator
    participant H as REST adapter (designed)
    participant UC as use case (designed)
    participant P as policy.Policy
    participant OB as outbox (designed)

    O->>H: PUT /sites/siteId/policy with If-Match
    H->>H: Idempotency check where enforced
    alt body malformed or unknown field
        H-->>O: 400 malformed-request
    end
    UC->>UC: load current policy
    alt policy exists and If-Match missing or stale
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
        UC->>OB: InterleavingPolicyDeclared, analytics topic only
        alt lost a race at the database
            UC-->>O: 409 concurrent-modification
        else committed
            UC-->>O: 200 with ETag version
        end
    end
```

Source: `policy.New`, `Policy.WithChanges`, `apis/openapi.yaml`
(`declareInterleavingPolicy`). Step order inside the designed use case is inferred
from the contract; only the `policy` calls exist.

## Simulate a recommendation

```mermaid
sequenceDiagram
    autonumber
    participant O as Operator or agent
    participant H as REST adapter (designed)
    participant UC as use case (designed)
    participant R as recommend.Recommend

    O->>H: POST /recommendations stationId, fromLocation optional
    alt Idempotency-Key missing where enforced
        H-->>O: 400 idempotency-key-required
    end
    H->>UC: simulate
    alt site has no policy
        UC-->>O: 422 policy-not-declared
    else no fromLocation and no recorded position
        UC-->>O: 422 station-position-unknown
    else
        UC->>R: Recommend, read-only
        alt origin without geometry
            R-->>UC: ErrOriginUnknown
            UC-->>O: 422 origin-unknown
        else
            R-->>UC: ranked candidates, possibly none
            UC-->>O: 200, nothing issued, nothing published
        end
    end
```

Source: `recommend.Recommend`, `apis/openapi.yaml` (`recommendTasks`).
