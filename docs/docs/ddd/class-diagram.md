---
id: class-diagram
title: Class diagrams
sidebar_label: Class diagrams
sidebar_position: 17
---

# Class diagrams

UML class diagrams of the domain as it is in `internal/domain`, and of the
hexagonal ports with the adapters that exist. Types are Go types; "interface" marks
a Go interface.

## Domain types

```mermaid
classDiagram
    class Policy {
        -SiteID site
        -Settings settings
        -int64 version
        +SiteID() SiteID
        +MaxDetourM() float64
        +MaxCandidates() int
        +HintTTL() Duration
        +VerticalWeight() float64
        +EligibleTaskTypes() TaskType[]
        +Eligible(TaskType) bool
        +Version() int64
        +Settings() Settings
        +WithChanges(Settings) Policy
    }
    class Settings {
        +float64 MaxDetourM
        +int MaxCandidates
        +Duration HintTTL
        +TaskType[] EligibleTaskTypes
        +float64 VerticalWeight
    }
    class StationTrip {
        -StationID id
        -Position position
        -TaskID lastTask
        -Time lastAt
        -Hint hint
        -int64 version
        -Event[] events
        +RecordCompletion(TaskID, Time, Position) bool
        +IssueHint(HintID, Candidate[], Policy, Time) error
        +RecordClaim(TaskID, Time) bool
        +Expire(Time) bool
        +Position() Position
        +LastCompleted() TaskID
        +Hint() Hint
        +ActiveHint() Hint
        +DomainEvents() Event[]
        +ClearDomainEvents()
    }
    class Hint {
        +HintID ID
        +TaskID AfterTask
        +string Origin
        +Time IssuedAt
        +Time ExpiresAt
        +int64 PolicyVersion
        +Candidate[] Candidates
        +HintState State
        +TaskID ConsumedTask
        +Time ConsumedAt
    }
    class Position {
        +SiteID Site
        +Place Place
    }
    class Candidate {
        +int Rank
        +TaskID TaskID
        +TaskType TaskType
        +string Location
        +float64 DetourM
    }
    class OpenTask {
        +TaskID ID
        +TaskType Type
        +SiteID Site
        +string FromLocation
        +Point FromPoint
        +string ToLocation
    }
    class Input {
        +SiteID Site
        +Place Origin
        +OpenTask[] Pool
        +Policy Policy
        +TaskID Completed
        +Model Model
    }
    class Model {
        <<interface>>
        +Name() string
        +Distance(Place, Place) float64, bool
    }
    class Rectilinear {
        -float64 verticalWeight
        +Name() string
        +Distance(Place, Place) float64, bool
    }
    class Place {
        +string Location
        +Point Point
        +Known() bool
    }
    class Point {
        +float64 X
        +float64 Y
        +float64 Z
    }
    class Event {
        <<interface>>
        +EventName() string
        +StationID() StationID
        +SiteID() SiteID
        +StationVersion() int64
        +OccurredAt() Time
    }
    class HintIssued
    class HintConsumed
    class HintExpired

    Policy *-- Settings
    StationTrip *-- Position
    StationTrip o-- Hint
    StationTrip ..> Event : raises
    Hint *-- Candidate
    Position *-- Place
    Place o-- Point
    Input o-- OpenTask
    Input o-- Place
    Input o-- Policy
    Input o-- Model
    Rectilinear ..|> Model
    HintIssued ..|> Event
    HintConsumed ..|> Event
    HintExpired ..|> Event
    StationTrip ..> Policy : IssueHint reads
    StationTrip ..> Candidate : IssueHint validates
```

Source: `internal/domain/policy/policy.go`, `internal/domain/travel/travel.go`,
`internal/domain/recommend/recommend.go`, `internal/domain/stationtrip/{trip,values,events}.go`.
Omits: the value types `SiteID`, `StationID`, `HintID`, `TaskID`, `TaskType`,
`HintState`, the `Header` embedded in every event, the error variables and the
functions `recommend.Recommend` and `recommend.RectilinearFor` (they have no
receiver). `Policy.Settings()` is both a field and an accessor in the code; the
diagram shows the accessor.

Hint states: `StateActive`, `StateConsumed`, `StateExpired`, `StateSuperseded`
(`HintState`, with `Valid()` and `Terminal()`).

## Ports and adapters

```mermaid
classDiagram
    class Model {
        <<interface>>
        +Name() string
        +Distance(Place, Place) float64, bool
    }
    class Rectilinear
    class PolicyRepo {
        <<port>>
        +Get(site) PolicyRecord
        +Save(rec, loadedVersion)
    }
    class StationTripRepo {
        <<port>>
        +Get(id) StationTrip
        +Save(trip, loadedVersion)
        +ExpiredActive(now, limit) StationID[]
        +GetHint(id) HintRecord
        +ListHints(filter, after, limit) HintRecord[]
    }
    class TaskPool {
        <<port>>
        +Get(id) Task
        +Save(task)
        +Available(siteID) Task[]
    }
    class LocationBook {
        <<port>>
        +Places(codes) Place map
        +SaveZone(zone)
        +SaveLocation(code, zoneID)
        +DecommissionLocation(code)
        +SaveGeometry(code, x, y, z)
    }
    class EventSink {
        <<port>>
        +PublishTripEvents(events)
        +PublishPolicyDeclared(declaration)
    }
    class ProcessedEvents {
        <<port>>
        +Claim(consumer, eventID) bool
    }
    class IdempotencyStore {
        <<port>>
        +Lookup(key) IdempotencyRecord
        +Save(record)
    }
    class UnitOfWork {
        <<port>>
        +Do(ctx, fn) error
    }
    class usecases {
        <<package application/usecases>>
        DeclarePolicy GetPolicy ListHints GetHint GetStation Recommend
        ApplyTaskAvailable ApplyTaskClaimed ApplyTaskCompleted
        ApplyZone ApplyLocationRegistered ApplyLocationDecommissioned ApplyGeometryUpdated
        ExpireHints
    }
    class httpAdapter {
        <<package inbound/http>>
        chi router, Idempotency middleware
    }
    class kafkaConsumers {
        <<package inbound/kafka>>
        fulfillment consumer, layout consumer
    }
    class expiryWorker {
        <<package inbound/expiry>>
    }
    class postgresAdapter {
        <<package outbound/postgres>>
    }
    class memoryAdapter {
        <<package outbound/memory>>
    }
    class eventsSink {
        <<package outbound/events>>
    }
    class outboxRelay {
        <<package outbound/outbox>>
    }
    class kafkaWriter {
        <<package outbound/kafka>>
        +NewWriter(brokers) Writer
        RelaySink
    }
    class cloudevents {
        <<package kafka/cloudevents>>
        +New(Spec) bytes
        +Decode(bytes) Event
        +ContentTypeHeader() Header
        +Type(entity, eventName) string
        +DataSchema(stream, eventName, version) string
    }
    Rectilinear ..|> Model
    httpAdapter ..> usecases : calls
    kafkaConsumers ..> usecases : calls
    expiryWorker ..> usecases : calls ExpireHints
    kafkaConsumers ..> cloudevents : decodes with
    usecases ..> PolicyRepo
    usecases ..> StationTripRepo
    usecases ..> TaskPool
    usecases ..> LocationBook
    usecases ..> EventSink
    usecases ..> ProcessedEvents
    usecases ..> UnitOfWork
    postgresAdapter ..|> PolicyRepo
    postgresAdapter ..|> StationTripRepo
    postgresAdapter ..|> TaskPool
    postgresAdapter ..|> LocationBook
    postgresAdapter ..|> ProcessedEvents
    postgresAdapter ..|> IdempotencyStore
    postgresAdapter ..|> UnitOfWork
    memoryAdapter ..|> PolicyRepo
    memoryAdapter ..|> StationTripRepo
    memoryAdapter ..|> TaskPool
    memoryAdapter ..|> LocationBook
    memoryAdapter ..|> ProcessedEvents
    memoryAdapter ..|> IdempotencyStore
    memoryAdapter ..|> UnitOfWork
    memoryAdapter ..|> EventSink
    eventsSink ..|> EventSink
    eventsSink ..> cloudevents : encodes with
    eventsSink ..> postgresAdapter : inserts outbox rows via cmd wiring
    outboxRelay ..> postgresAdapter : drains the outbox
    outboxRelay ..> kafkaWriter : sends with RelaySink
    httpAdapter ..> IdempotencyStore : middleware, wired by cmd
```

Source: `internal/application/ports/ports.go`, `clock.go`, `unit_of_work.go` (the
OUT ports), `internal/domain/travel/travel.go` (the port declared in the domain),
`internal/adapters/outbound/{postgres,memory}/ports_check.go` (compile-time
assertions that each adapter implements the ports), `cmd/api/adapters.go` and
`cmd/api/relay.go` (the wiring), `internal/adapters/kafka/cloudevents/cloudevents.go`,
`internal/adapters/outbound/kafka/{writer,relay_sink}.go`,
`internal/architecture/architecture_test.go` (the layer rules). `Clock` and
`IDGenerator` are the remaining ports; the in-memory adapter implements `Clock`
(`SystemClock`) and `IDGenerator` (`RandomIDs`) and is used in both storage modes.
The `eventsSink` writes through the `Inserter` interface that
`postgres.OutboxRepo` satisfies; adapters never import each other, `cmd/api` does
the wiring.
