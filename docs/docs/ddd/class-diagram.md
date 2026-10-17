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
    class cloudevents {
        <<package>>
        +New(Spec) bytes
        +Decode(bytes) Event
        +ContentTypeHeader() Header
        +Type(entity, eventName) string
        +DataSchema(stream, eventName, version) string
    }
    class kafkaWriter {
        <<package outbound/kafka>>
        +NewWriter(brokers) Writer
    }
    class applicationPorts {
        <<pending: internal/application/ports>>
    }
    class httpAdapter {
        <<pending: inbound/http>>
    }
    class kafkaConsumers {
        <<pending: inbound/kafka>>
    }
    class postgresAdapter {
        <<pending: outbound/postgres>>
    }
    class outboxRelay {
        <<pending>>
    }
    Rectilinear ..|> Model
    outboxRelay ..> kafkaWriter : writes with
    outboxRelay ..> cloudevents : encodes with
    kafkaConsumers ..> cloudevents : decodes with
    httpAdapter ..> applicationPorts : calls use cases
    postgresAdapter ..|> applicationPorts : implements OUT ports
```

Source: `internal/domain/travel/travel.go` (the only port declared in the domain),
`internal/adapters/kafka/cloudevents/cloudevents.go`,
`internal/adapters/outbound/kafka/writer.go`, `internal/architecture/architecture_test.go`
(the layer rules). Boxes tagged *pending* have no code on `develop`; their
relationships are the layout fixed by ADR 0001 and ADR 0004, not observed imports.
