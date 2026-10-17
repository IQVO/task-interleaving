package events_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/claudioed/task-interleaving/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/task-interleaving/internal/adapters/outbound/events"
	"github.com/claudioed/task-interleaving/internal/application/outbox"
	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/domain/recommend"
	"github.com/claudioed/task-interleaving/internal/domain/stationtrip"
)

const (
	hintID  = "hint-3b0e8d2a-7c41-4f6a-9d15-2a8b6c4e1f07"
	station = "STN-07"
	typePfx = "com.warehouse.wes.task-interleaving."
)

// capture is a fake outbox: it records every Insert call.
type capture struct {
	calls [][]outbox.Message
	err   error
}

func (c *capture) Insert(_ context.Context, msgs ...outbox.Message) error {
	if c.err != nil {
		return c.err
	}
	c.calls = append(c.calls, msgs)
	return nil
}

func (c *capture) rows() []outbox.Message {
	var all []outbox.Message
	for _, call := range c.calls {
		all = append(all, call...)
	}
	return all
}

func sinkWithID(out events.Inserter, ids ...string) *events.Sink {
	next := 0
	s := events.NewSink(out)
	s.NewID = func() string {
		id := ids[next%len(ids)]
		next++
		return id
	}
	return s
}

func at(h, m, s int) time.Time { return time.Date(2026, 10, 17, h, m, s, 0, time.UTC) }

func header(version int64, when time.Time) stationtrip.Header {
	return stationtrip.Header{Station: station, Site: "SITE-1", Version: version, At: when}
}

func issued() stationtrip.HintIssued {
	return stationtrip.HintIssued{
		Header: header(1, at(12, 0, 0)), HintID: hintID, AfterTask: "task-8a1f", Origin: "WH1-FWD-A01-01-01-A",
		ExpiresAt: at(12, 1, 30), PolicyVersion: 2,
		Candidates: []recommend.Candidate{
			{Rank: 1, TaskID: "task-9c2e", TaskType: "PICK", Location: "WH1-FWD-A01-02-01-A", DetourM: 4.5},
			{Rank: 2, TaskID: "task-9d10", TaskType: "REPLENISH", Location: "WH1-FWD-A02-01-01-A", DetourM: 12},
		},
	}
}

func consumed() stationtrip.HintConsumed {
	return stationtrip.HintConsumed{Header: header(2, at(12, 0, 20)), HintID: hintID, TaskID: "task-9c2e", Rank: 1, DetourM: 4.5}
}

func expired() stationtrip.HintExpired {
	return stationtrip.HintExpired{Header: header(2, at(12, 1, 31)), HintID: hintID, ExpiresAt: at(12, 1, 30)}
}

func declaration() repository.PolicyDeclaration {
	return repository.PolicyDeclaration{
		SiteID: "SITE-1", MaxDetourM: 25, MaxCandidates: 3, HintTTLSeconds: 90,
		EligibleTaskTypes: []string{"PICK", "REPLENISH"}, VerticalWeight: 1.5, Version: 2, DeclaredAt: at(11, 0, 0),
	}
}

// goldenRow is what one outbox row must be, byte for byte on the JSON side.
type goldenRow struct {
	topic, schema, golden string
}

type goldenCase struct {
	name      string
	id        string
	eventType string
	subject   string
	publish   func(*events.Sink) error
	rows      []goldenRow
}

func tripCase(name, id, entity string, ev stationtrip.Event, rows ...goldenRow) goldenCase {
	return goldenCase{
		name: name, id: id, eventType: typePfx + entity + "." + name, subject: station, rows: rows,
		publish: func(s *events.Sink) error { return s.PublishTripEvents(context.Background(), ev) },
	}
}

func schema(stream, name string) string {
	return "urn:warehouse:task-interleaving:" + stream + ":" + name + ":v1"
}

func goldenCases() []goldenCase {
	const issuedName, consumedName, expiredName, policyName = "InterleavingHintIssued", "InterleavingHintConsumed", "InterleavingHintExpired", "InterleavingPolicyDeclared"
	return []goldenCase{
		tripCase(issuedName, "6f1d2c3b-4a5e-4b7c-8d9e-0a1b2c3d4e5f", "stationtrip", issued(),
			goldenRow{events.TopicEvents, schema("events", issuedName), "issued.events.golden.json"},
			goldenRow{events.TopicAnalytics, schema("analytics", issuedName), "issued.analytics.golden.json"}),
		tripCase(consumedName, "0b9a8c7d-6e5f-4a3b-9c2d-1e0f9a8b7c6d", "stationtrip", consumed(),
			goldenRow{events.TopicAnalytics, schema("analytics", consumedName), "consumed.analytics.golden.json"}),
		tripCase(expiredName, "4d3c2b1a-0f9e-4d8c-8b7a-6f5e4d3c2b1a", "stationtrip", expired(),
			goldenRow{events.TopicAnalytics, schema("analytics", expiredName), "expired.analytics.golden.json"}),
		{
			name: policyName, id: "7e6d5c4b-3a29-4180-9f7e-6d5c4b3a2918", eventType: typePfx + "policy." + policyName, subject: "SITE-1",
			publish: func(s *events.Sink) error { return s.PublishPolicyDeclared(context.Background(), declaration()) },
			rows:    []goldenRow{{events.TopicAnalytics, schema("analytics", policyName), "policy_declared.analytics.golden.json"}},
		},
	}
}

func readGolden(t *testing.T, name string) any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read golden %s: %v", name, err)
	}
	return decode(t, name, raw)
}

func decode(t *testing.T, what string, raw []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("%s is not JSON: %v\n%s", what, err, raw)
	}
	return v
}

// assertRow checks one outbox row against its golden file: every CloudEvents
// attribute and the whole data object (exact JSON), plus the row columns the
// relay publishes with (topic, key, type, subject, dataschema, headers).
func assertRow(t *testing.T, c goldenCase, got outbox.Message, want goldenRow) {
	t.Helper()
	if got.Topic != want.topic {
		t.Errorf("topic = %q, want %q", got.Topic, want.topic)
	}
	if got.EventID != c.id || got.EventType != c.eventType || got.Subject != c.subject || got.DataSchema != want.schema {
		t.Errorf("row = id %q type %q subject %q schema %q", got.EventID, got.EventType, got.Subject, got.DataSchema)
	}
	if string(got.Key) != c.subject {
		t.Errorf("key = %q, want %q", got.Key, c.subject)
	}
	wantHeaders := []outbox.Header{{Key: "content-type", Value: "application/cloudevents+json; charset=UTF-8"}}
	if !reflect.DeepEqual(got.Headers, wantHeaders) {
		t.Errorf("headers = %+v, want %+v", got.Headers, wantHeaders)
	}
	if gotJSON, wantJSON := decode(t, "value", got.Value), readGolden(t, want.golden); !reflect.DeepEqual(gotJSON, wantJSON) {
		t.Errorf("%s: value differs from the golden\n got: %s\nwant: %v", want.golden, got.Value, wantJSON)
	}
}

func TestSink_GoldenJSONPerTypeAndTopic(t *testing.T) {
	for _, c := range goldenCases() {
		t.Run(c.name, func(t *testing.T) {
			out := &capture{}
			if err := c.publish(sinkWithID(out, c.id)); err != nil {
				t.Fatalf("publish: %v", err)
			}
			if len(out.calls) != 1 {
				t.Fatalf("Insert called %d times, want once (one atomic write)", len(out.calls))
			}
			rows := out.rows()
			if len(rows) != len(c.rows) {
				t.Fatalf("%d rows, want %d", len(rows), len(c.rows))
			}
			for i, want := range c.rows {
				assertRow(t, c, rows[i], want)
			}
		})
	}
}

// Every written value is a valid CloudEvents 1.0 event the repo's own decoder
// accepts, with the service source and the application/json data type.
func TestSink_RowsDecodeAsCloudEvents(t *testing.T) {
	for _, c := range goldenCases() {
		out := &capture{}
		if err := c.publish(sinkWithID(out, c.id)); err != nil {
			t.Fatalf("%s: publish: %v", c.name, err)
		}
		for _, row := range out.rows() {
			e, err := cloudevents.Decode(row.Value)
			if err != nil {
				t.Fatalf("%s: decode: %v", c.name, err)
			}
			if e.Source() != "/warehouse/task-interleaving" || e.DataContentType() != "application/json" || e.ID() != c.id {
				t.Errorf("%s: source %q datacontenttype %q id %q", c.name, e.Source(), e.DataContentType(), e.ID())
			}
		}
	}
}

// The integration row and its analytics copy of one occurrence share the
// CloudEvents id; two occurrences never do.
func TestSink_OneIDPerOccurrenceSharedByBothTopics(t *testing.T) {
	out := &capture{}
	if err := sinkWithID(out, "id-1", "id-2").PublishTripEvents(context.Background(), issued(), issued()); err != nil {
		t.Fatal(err)
	}
	rows := out.rows()
	if len(rows) != 4 {
		t.Fatalf("%d rows, want 4 (two events x two topics)", len(rows))
	}
	for i, want := range []struct{ id, topic string }{
		{"id-1", events.TopicEvents}, {"id-1", events.TopicAnalytics},
		{"id-2", events.TopicEvents}, {"id-2", events.TopicAnalytics},
	} {
		if rows[i].EventID != want.id || rows[i].Topic != want.topic {
			t.Errorf("row %d = id %q topic %q, want id %q topic %q", i, rows[i].EventID, rows[i].Topic, want.id, want.topic)
		}
	}
	if len(out.calls) != 1 {
		t.Errorf("Insert called %d times, want once", len(out.calls))
	}
}

// Only InterleavingHintIssued reaches the integration topic: Consumed, Expired
// and the policy declaration are analytics-only, one row on the analytics topic.
func TestSink_OnlyHintIssuedReachesTheIntegrationTopic(t *testing.T) {
	out := &capture{}
	s := sinkWithID(out, "x")
	if err := s.PublishTripEvents(context.Background(), consumed(), expired()); err != nil {
		t.Fatal(err)
	}
	if err := s.PublishPolicyDeclared(context.Background(), declaration()); err != nil {
		t.Fatal(err)
	}
	rows := out.rows()
	if len(rows) != 3 {
		t.Fatalf("%d rows, want 3 (one per analytics-only fact)", len(rows))
	}
	for _, r := range rows {
		if r.Topic != events.TopicAnalytics {
			t.Errorf("%s was written to %s", r.EventType, r.Topic)
		}
	}
}

// The Kafka key is the station id for the hint events and the site id for the
// policy declaration.
func TestSink_KeysAreStationAndSite(t *testing.T) {
	out := &capture{}
	s := sinkWithID(out, "x")
	d := declaration()
	d.SiteID = "SITE-9"
	if err := s.PublishTripEvents(context.Background(), issued(), consumed(), expired()); err != nil {
		t.Fatal(err)
	}
	if err := s.PublishPolicyDeclared(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	for _, r := range out.rows() {
		want := station
		if r.EventType == typePfx+"policy.InterleavingPolicyDeclared" {
			want = "SITE-9"
		}
		if string(r.Key) != want || r.Subject != want {
			t.Errorf("%s: key %q subject %q, want %q", r.EventType, r.Key, r.Subject, want)
		}
	}
}

func TestSink_TopicOverridesAndDefaults(t *testing.T) {
	out := &capture{}
	s := events.NewSink(out)
	s.EventsTopic, s.AnalyticsTopic = "t.events", "t.analytics"
	if err := s.PublishTripEvents(context.Background(), issued()); err != nil {
		t.Fatal(err)
	}
	rows := out.rows()
	if len(rows) != 2 || rows[0].Topic != "t.events" || rows[1].Topic != "t.analytics" {
		t.Fatalf("rows = %+v", rows)
	}
	if events.TopicEvents != "warehouse.task-interleaving.events" || events.TopicAnalytics != "warehouse.task-interleaving.analytics" {
		t.Fatalf("topics = %q, %q", events.TopicEvents, events.TopicAnalytics)
	}
}

// A zero-NewID sink and NewSink both mint random UUIDs, a fresh one per call.
func TestSink_MintsUUIDsWhenNewIDIsNil(t *testing.T) {
	out := &capture{}
	s := &events.Sink{Outbox: out}
	for range 2 {
		if err := s.PublishTripEvents(context.Background(), expired()); err != nil {
			t.Fatal(err)
		}
	}
	rows := out.rows()
	if len(rows) != 2 || rows[0].EventID == "" || rows[0].EventID == rows[1].EventID {
		t.Fatalf("ids = %+v", rows)
	}
	if len(rows[0].EventID) != 36 {
		t.Errorf("id %q is not a UUID", rows[0].EventID)
	}
}

type bogusEvent struct{ stationtrip.Header }

func (bogusEvent) EventName() string { return "Bogus" }

// An event the service does not publish is a programming error: the call
// fails and NOTHING (not even the events before it) is written.
func TestSink_UnknownEventFailsWholeCall(t *testing.T) {
	out := &capture{}
	err := sinkWithID(out, "x").PublishTripEvents(context.Background(), issued(), bogusEvent{})
	if err == nil {
		t.Fatal("want an error for an unpublished event type")
	}
	if len(out.calls) != 0 {
		t.Fatalf("Insert was called despite the error: %+v", out.calls)
	}
}

// The envelope refuses an empty subject: a hint event without a station cannot
// be encoded, and the policy declaration needs a site.
func TestSink_EncodeErrorsAreReturnedBeforeAnyWrite(t *testing.T) {
	out := &capture{}
	s := sinkWithID(out, "x")
	ev := issued()
	ev.Station = ""
	if err := s.PublishTripEvents(context.Background(), ev); err == nil {
		t.Fatal("want an error for an empty station id")
	}
	d := declaration()
	d.SiteID = ""
	if err := s.PublishPolicyDeclared(context.Background(), d); err == nil {
		t.Fatal("want an error for an empty site id")
	}
	if len(out.calls) != 0 {
		t.Fatalf("Insert was called despite the encode error: %+v", out.calls)
	}
}

func TestSink_OutboxErrorsAreWrapped(t *testing.T) {
	boom := errors.New("boom")
	out := &capture{err: boom}
	s := sinkWithID(out, "x")
	if err := s.PublishTripEvents(context.Background(), issued()); !errors.Is(err, boom) {
		t.Errorf("PublishTripEvents err = %v, want it to wrap boom", err)
	}
	if err := s.PublishPolicyDeclared(context.Background(), declaration()); !errors.Is(err, boom) {
		t.Errorf("PublishPolicyDeclared err = %v, want it to wrap boom", err)
	}
}

// An event list may be empty: nothing is encoded and the outbox sees no rows.
func TestSink_EmptyEventListWritesNothing(t *testing.T) {
	out := &capture{}
	if err := sinkWithID(out, "x").PublishTripEvents(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(out.rows()) != 0 {
		t.Fatalf("rows = %+v", out.rows())
	}
}

// A hint that superseded another raises the same single InterleavingHintIssued:
// the superseded hint has no event of its own (ADR 0004).
func TestSink_SupersedingHintIsAnOrdinaryHintIssued(t *testing.T) {
	out := &capture{}
	ev := issued()
	ev.SupersededHintID = "hint-previous"
	if err := sinkWithID(out, "x").PublishTripEvents(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if len(out.rows()) != 2 {
		t.Fatalf("rows = %d, want the integration and the analytics row of one hint", len(out.rows()))
	}
}

// The arrays of the payload are never null, even for empty inputs.
func TestSink_EmptyListsEncodeAsArrays(t *testing.T) {
	out := &capture{}
	s := sinkWithID(out, "x")
	d := declaration()
	d.EligibleTaskTypes = nil
	if err := s.PublishPolicyDeclared(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	ev := issued()
	ev.Candidates = nil
	if err := s.PublishTripEvents(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	for _, r := range out.rows() {
		var e struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(r.Value, &e); err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"eligible_task_types", "candidates"} {
			if v, ok := e.Data[field]; ok {
				if _, isList := v.([]any); !isList {
					t.Errorf("%s = %v, want an array", field, v)
				}
			}
		}
	}
}
