// Package events is the outbound event publisher: it maps the facts the
// application raises (the StationTrip hint events and the analytics-only policy
// declaration) to CloudEvents 1.0 outbox messages and hands them to the
// transactional outbox (ADR 0004). Envelopes are built ONLY through
// internal/adapters/kafka/cloudevents; payloads are exactly the ones pinned in
// apis/asyncapi.yaml.
package events

import (
	"fmt"
	"time"

	"github.com/claudioed/task-interleaving/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/task-interleaving/internal/application/outbox"
	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/domain/stationtrip"
)

// The two topics this service publishes to. The outbox relay sets
// Message.Topic per row; the Kafka writer carries no Topic.
const (
	// TopicEvents is the integration topic: only InterleavingHintIssued.
	TopicEvents = "warehouse.task-interleaving.events"
	// TopicAnalytics is the analytics topic: the hint-issued event again, the
	// two other hint events and the analytics-only policy declaration.
	TopicAnalytics = "warehouse.task-interleaving.analytics"
)

// The `<entity>` segment of every published type:
// com.warehouse.wes.task-interleaving.<entity>.<EventName>.
const (
	// EntityStationTrip is the segment of the hint events.
	EntityStationTrip = "stationtrip"
	// EntityPolicy is the segment of the policy event.
	EntityPolicy = "policy"
)

// The CloudEvents event names. The domain names its facts HintIssued,
// HintConsumed and HintExpired; the published contract names carry the
// Interleaving prefix.
const (
	nameHintIssued     = "InterleavingHintIssued"
	nameHintConsumed   = "InterleavingHintConsumed"
	nameHintExpired    = "InterleavingHintExpired"
	namePolicyDeclared = "InterleavingPolicyDeclared"
)

// schemaVersion is the dataschema version of every payload below
// (urn:warehouse:task-interleaving:<events|analytics>:<EventName>:v1).
const schemaVersion = 1

// Wire payloads (CloudEvents `data`, snake_case JSON). Timestamps are RFC 3339
// UTC.

type candidateData struct {
	Rank     int     `json:"rank"`
	TaskID   string  `json:"task_id"`
	TaskType string  `json:"task_type"`
	Location string  `json:"location"`
	DetourM  float64 `json:"detour_m"`
}

type issuedData struct {
	HintID         string          `json:"hint_id"`
	StationID      string          `json:"station_id"`
	SiteID         string          `json:"site_id"`
	AfterTaskID    string          `json:"after_task_id"`
	OriginLocation string          `json:"origin_location"`
	ExpiresAt      string          `json:"expires_at"`
	PolicyVersion  int64           `json:"policy_version"`
	Candidates     []candidateData `json:"candidates"`
}

type consumedData struct {
	HintID     string  `json:"hint_id"`
	StationID  string  `json:"station_id"`
	SiteID     string  `json:"site_id"`
	TaskID     string  `json:"task_id"`
	Rank       int     `json:"rank"`
	DetourM    float64 `json:"detour_m"`
	ConsumedAt string  `json:"consumed_at"`
}

type expiredData struct {
	HintID    string `json:"hint_id"`
	StationID string `json:"station_id"`
	SiteID    string `json:"site_id"`
	ExpiresAt string `json:"expires_at"`
}

type policyDeclaredData struct {
	SiteID            string   `json:"site_id"`
	MaxDetourM        float64  `json:"max_detour_m"`
	MaxCandidates     int      `json:"max_candidates"`
	HintTTLSeconds    int64    `json:"hint_ttl_seconds"`
	EligibleTaskTypes []string `json:"eligible_task_types"`
	VerticalWeight    float64  `json:"vertical_weight"`
	Version           int64    `json:"version"`
	DeclaredAt        string   `json:"declared_at"`
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// fact is one thing to publish: everything the envelope needs, independent of
// the domain type it came from.
type fact struct {
	entity  string
	name    string
	subject string
	at      time.Time
	data    any
	// analyticsOnly facts are written to the analytics topic only.
	analyticsOnly bool
}

// tripFact maps a StationTrip event to its fact. An event type the service
// does not publish is a programming error.
func tripFact(ev stationtrip.Event) (fact, error) {
	if e, ok := ev.(stationtrip.HintIssued); ok {
		return issuedFact(e), nil
	}
	if e, ok := ev.(stationtrip.HintConsumed); ok {
		return consumedFact(e), nil
	}
	if e, ok := ev.(stationtrip.HintExpired); ok {
		return expiredFact(e), nil
	}
	return fact{}, fmt.Errorf("events: %s is not a published event", ev.EventName())
}

func issuedFact(e stationtrip.HintIssued) fact {
	candidates := make([]candidateData, 0, len(e.Candidates))
	for _, c := range e.Candidates {
		candidates = append(candidates, candidateData{
			Rank: c.Rank, TaskID: string(c.TaskID), TaskType: string(c.TaskType), Location: c.Location, DetourM: c.DetourM,
		})
	}
	return fact{
		entity: EntityStationTrip, name: nameHintIssued, subject: string(e.Station), at: e.At,
		data: issuedData{
			HintID: string(e.HintID), StationID: string(e.Station), SiteID: string(e.Site),
			AfterTaskID: string(e.AfterTask), OriginLocation: e.Origin, ExpiresAt: ts(e.ExpiresAt),
			PolicyVersion: e.PolicyVersion, Candidates: candidates,
		},
	}
}

func consumedFact(e stationtrip.HintConsumed) fact {
	return fact{
		entity: EntityStationTrip, name: nameHintConsumed, subject: string(e.Station), at: e.At, analyticsOnly: true,
		data: consumedData{
			HintID: string(e.HintID), StationID: string(e.Station), SiteID: string(e.Site),
			TaskID: string(e.TaskID), Rank: e.Rank, DetourM: e.DetourM, ConsumedAt: ts(e.At),
		},
	}
}

func expiredFact(e stationtrip.HintExpired) fact {
	return fact{
		entity: EntityStationTrip, name: nameHintExpired, subject: string(e.Station), at: e.At, analyticsOnly: true,
		data: expiredData{
			HintID: string(e.HintID), StationID: string(e.Station), SiteID: string(e.Site), ExpiresAt: ts(e.ExpiresAt),
		},
	}
}

// policyFact maps the policy declaration to its analytics-only fact. Subject
// and key are the site id.
func policyFact(d repository.PolicyDeclaration) fact {
	types := d.EligibleTaskTypes
	if types == nil {
		types = []string{}
	}
	return fact{
		entity: EntityPolicy, name: namePolicyDeclared, subject: d.SiteID, at: d.DeclaredAt, analyticsOnly: true,
		data: policyDeclaredData{
			SiteID: d.SiteID, MaxDetourM: d.MaxDetourM, MaxCandidates: d.MaxCandidates, HintTTLSeconds: d.HintTTLSeconds,
			EligibleTaskTypes: types, VerticalWeight: d.VerticalWeight, Version: d.Version, DeclaredAt: ts(d.DeclaredAt),
		},
	}
}

// build encodes f as one outbox message per destination topic. The CloudEvents
// `id` is minted ONCE per occurrence and shared by both messages, so the
// analytics copy has the same id as the integration one and a relay retry
// republishes the same bytes.
func (s *Sink) build(f fact, id string) ([]outbox.Message, error) {
	streams := []stream{{cloudevents.StreamEvents, s.eventsTopic()}, {cloudevents.StreamAnalytics, s.analyticsTopic()}}
	if f.analyticsOnly {
		streams = streams[1:]
	}
	out := make([]outbox.Message, 0, len(streams))
	for _, st := range streams {
		msg, err := message(f, id, st)
		if err != nil {
			return nil, err
		}
		out = append(out, msg)
	}
	return out, nil
}

// stream pairs a CloudEvents stream (dataschema segment) with its topic.
type stream struct {
	name  string
	topic string
}

func message(f fact, id string, st stream) (outbox.Message, error) {
	value, err := cloudevents.New(cloudevents.Spec{
		ID:        id,
		Entity:    f.entity,
		EventName: f.name,
		Subject:   f.subject,
		Time:      f.at,
		Stream:    st.name,
		Version:   schemaVersion,
		Data:      f.data,
	})
	if err != nil {
		return outbox.Message{}, fmt.Errorf("encode %s: %w", f.name, err)
	}
	ct := cloudevents.ContentTypeHeader()
	return outbox.Message{
		EventID:    id,
		Topic:      st.topic,
		EventType:  cloudevents.Type(f.entity, f.name),
		Subject:    f.subject,
		Key:        []byte(f.subject),
		DataSchema: cloudevents.DataSchema(st.name, f.name, schemaVersion),
		Value:      value,
		Headers:    []outbox.Header{{Key: ct.Key, Value: string(ct.Value)}},
	}, nil
}
