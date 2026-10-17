//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"

	inboundkafka "github.com/claudioed/task-interleaving/internal/adapters/inbound/kafka"
	"github.com/claudioed/task-interleaving/internal/testing/kafkatest"
	"github.com/claudioed/task-interleaving/internal/testing/pgtest"
)

// The end-to-end proof that the shipped binary is really wired: the test boots
// the REAL entry point (run, in-process) against testcontainers Postgres and
// Kafka with nothing but the environment a deployment sets, and drives it only
// through Kafka (the consumed topics) and REST. It reads the effects from
// Postgres and from the produced topics. The broker has topic auto-creation
// OFF, like the infrastructure that owns the topics.

// Topic names of the world the service lives in (ADR 0003, ADR 0004).
const (
	topicFacility    = inboundkafka.LayoutTopic
	topicFulfillment = inboundkafka.FulfillmentTopic
	topicEvents      = "warehouse.task-interleaving.events"
	topicAnalytics   = "warehouse.task-interleaving.analytics"

	typeIssued   = "com.warehouse.wes.task-interleaving.stationtrip.InterleavingHintIssued"
	typeConsumed = "com.warehouse.wes.task-interleaving.stationtrip.InterleavingHintConsumed"
	typePolicy   = "com.warehouse.wes.task-interleaving.policy.InterleavingPolicyDeclared"

	site    = "SITE-1"
	station = "STN-07"
)

var allTopics = []string{topicFacility, topicFulfillment, topicEvents, topicAnalytics}

var seq atomic.Int64

// service is one running instance of the real entry point.
type service struct {
	t      *testing.T
	base   string
	cancel context.CancelFunc
	done   chan error
}

// env is the environment a deployment sets.
type env struct {
	dbURL   string
	brokers string
	addr    string
	tag     string
}

func newEnv(t *testing.T, brokers string) env {
	t.Helper()
	return env{dbURL: pgtest.EmptyURL(t), brokers: brokers, addr: freeAddr(t), tag: fmt.Sprintf("e2e%d", seq.Add(1))}
}

// apply sets the process environment of run (restored by the test cleanup).
func (e env) apply(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", e.dbURL)
	t.Setenv("MIGRATIONS_DATABASE_URL", "")
	t.Setenv("KAFKA_BROKERS", e.brokers)
	t.Setenv("HTTP_ADDR", e.addr)
	t.Setenv("SHUTDOWN_DRAIN_DELAY", "0")
	t.Setenv("OUTBOX_RELAY_INTERVAL", "100ms")
	t.Setenv("HINT_EXPIRY_INTERVAL", "200ms")
	t.Setenv("LOG_LEVEL", "warn")
	for _, name := range []string{"FULFILLMENT", "LAYOUT"} {
		t.Setenv(name+"_MODE", "")
		t.Setenv(name+"_CONSUMER_GROUP", name+"-"+e.tag)
	}
}

// start boots run in-process and waits until the listener answers /healthz.
func (e env) start(t *testing.T) *service {
	t.Helper()
	e.apply(t)
	ctx, cancel := context.WithCancel(context.Background())
	s := &service{t: t, base: "http://" + e.addr, cancel: cancel, done: make(chan error, 1)}
	go func() { s.done <- run(ctx) }()
	t.Cleanup(func() { s.stopQuietly() })
	waitFor(t, 60*time.Second, "the service to listen on "+e.addr, func() bool {
		select {
		case err := <-s.done:
			t.Fatalf("run returned while booting: %v", err)
		default:
		}
		status, _, _ := s.try(http.MethodGet, "/healthz", "")
		return status == http.StatusOK
	})
	return s
}

// stop cancels run and requires a clean shutdown within a bound.
func (s *service) stop() {
	s.t.Helper()
	s.cancel()
	select {
	case err := <-s.done:
		if err != nil {
			s.t.Fatalf("shutdown returned %v", err)
		}
	case <-time.After(60 * time.Second):
		s.t.Fatal("run did not return after the shutdown")
	}
	s.done <- nil // let the cleanup find it stopped
}

func (s *service) stopQuietly() {
	s.cancel()
	select {
	case <-s.done:
	case <-time.After(60 * time.Second):
	}
}

// try performs a request and returns -1 when the connection fails.
func (s *service) try(method, path, body string) (int, string, http.Header) {
	req, err := http.NewRequestWithContext(context.Background(), method, s.base+path, strings.NewReader(body))
	if err != nil {
		return -1, err.Error(), nil
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return -1, err.Error(), nil
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw), resp.Header
}

func (s *service) call(method, path, body string, want int) string {
	s.t.Helper()
	status, raw, _ := s.try(method, path, body)
	if status != want {
		s.t.Fatalf("%s %s = %d %s, want %d", method, path, status, raw, want)
	}
	return raw
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

// readyz returns the status and body of GET /readyz.
func (s *service) readyz() (int, string) {
	status, body, _ := s.try(http.MethodGet, "/readyz", "")
	return status, body
}

// cloudEvent builds one structured-mode CloudEvents 1.0 message of another
// bounded context, exactly as that producer publishes it.
func cloudEvent(t *testing.T, source, typ, subject string, data any) kafkatest.Message {
	t.Helper()
	e := ce.New()
	e.SetID(fmt.Sprintf("evt-%s-%d", subject, seq.Add(1)))
	e.SetSource(source)
	e.SetType(typ)
	e.SetSubject(subject)
	e.SetTime(time.Now().UTC())
	if err := e.SetData("application/json", data); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return kafkatest.Message{Key: subject, Value: raw, Headers: map[string]string{"content-type": "application/cloudevents+json; charset=UTF-8"}}
}

// world is the pre-built stream of consumed messages of the scenario. The
// layout: L0 (0,0,0) is where STN-07 ends its completed task, A (3,0,0) is 3 m
// from it, B (3,4,0) is 7 m from it. task-z-near sits at A and task-a-far at B,
// so the ranking by detour (z-near first) is the reverse of the task id order.
type world struct {
	layout []kafkatest.Message
	// tasks announces the two open tasks and the one that will be completed.
	tasks []kafkatest.Message
	// finish is the claim and the completion of the third task, in order.
	finish []kafkatest.Message
	// claim builds the claim of a task by the station, stamped now: the
	// claim of the top candidate must not be older than the hint it consumes,
	// so it is built after the hint exists.
	claim func(id string) kafkatest.Message
}

func buildWorld(t *testing.T) world {
	t.Helper()
	const facility, fulfillment = "/warehouse/facility-layout", "/warehouse/fulfillment-execution"
	zone := cloudEvent(t, facility, inboundkafka.TypeZoneRegistered, "z-fwd", map[string]any{"zoneId": "z-fwd", "siteCode": site, "zoneCode": "FWD"})
	layout := []kafkatest.Message{zone}
	for code, p := range map[string][3]float64{"L0": {0, 0, 0}, "A": {3, 0, 0}, "B": {3, 4, 0}} {
		layout = append(layout,
			cloudEvent(t, facility, inboundkafka.TypeLocationSlotRegistered, code, map[string]any{"locationCode": code, "zoneId": "z-fwd"}),
			cloudEvent(t, facility, inboundkafka.TypeLocationGeometryUpdated, code, map[string]any{"locationCode": code, "xM": p[0], "yM": p[1], "zM": p[2]}))
	}
	available := func(id, from, to string) kafkatest.Message {
		return cloudEvent(t, fulfillment, inboundkafka.TypeTaskAvailable, id, map[string]any{
			"task_id": id, "task_type": "PICK", "site_id": site, "from_location": from, "to_location": to,
			"required_capabilities": []string{}, "priority_band": "STANDARD"})
	}
	claim := func(id string) kafkatest.Message {
		return cloudEvent(t, fulfillment, inboundkafka.TypeTaskClaimed, id, map[string]any{
			"task_id": id, "task_type": "PICK", "station_id": station, "claimed_at": time.Now().UTC().Format(time.RFC3339Nano)})
	}
	return world{
		layout: layout,
		tasks: []kafkatest.Message{
			available("task-z-near", "A", "A"), available("task-a-far", "B", "B"), available("task-done", "L0", "L0"),
		},
		finish: []kafkatest.Message{
			claim("task-done"),
			cloudEvent(t, fulfillment, inboundkafka.TypeTaskCompleted, "task-done", map[string]any{"task_id": "task-done", "station_id": station}),
		},
		claim: claim,
	}
}

// The CloudEvents context attributes of a published message, plus its data.
type envelope struct {
	SpecVersion     string          `json:"specversion"`
	ID              string          `json:"id"`
	Source          string          `json:"source"`
	Type            string          `json:"type"`
	Subject         string          `json:"subject"`
	Time            string          `json:"time"`
	DataContentType string          `json:"datacontenttype"`
	DataSchema      string          `json:"dataschema"`
	Data            json.RawMessage `json:"data"`
}

func decodeEnvelope(t *testing.T, m kafkatest.Message) envelope {
	t.Helper()
	var e envelope
	if err := json.Unmarshal(m.Value, &e); err != nil {
		t.Fatalf("not a CloudEvent: %v: %s", err, m.Value)
	}
	return e
}

// assertEnvelope checks every context attribute of a message of this service
// and its Kafka key (the station id, or the site id for the policy).
func assertEnvelope(t *testing.T, m kafkatest.Message, typ, schema, subject string) envelope {
	t.Helper()
	e := decodeEnvelope(t, m)
	if e.SpecVersion != "1.0" || e.Type != typ || e.Source != "/warehouse/task-interleaving" ||
		e.DataSchema != schema || e.DataContentType != "application/json" || e.Subject != subject || e.ID == "" {
		t.Fatalf("envelope = %+v, want 1.0 %s from /warehouse/task-interleaving (%s) about %s", e, typ, schema, subject)
	}
	if _, err := time.Parse(time.RFC3339Nano, e.Time); err != nil {
		t.Fatalf("time %q is not RFC 3339: %v", e.Time, err)
	}
	if m.Key != subject {
		t.Fatalf("kafka key = %q, want %q", m.Key, subject)
	}
	if got := m.Headers["content-type"]; got != "application/cloudevents+json; charset=UTF-8" {
		t.Fatalf("content-type header = %q", got)
	}
	return e
}

// pgView reads the service's database through pgtest.Scan.
type pgView struct {
	t    *testing.T
	scan func(query string, args []any, dest ...any)
}

func newPGView(t *testing.T, url string) pgView {
	t.Helper()
	pool := pgtest.Open(t, url)
	return pgView{t: t, scan: func(query string, args []any, dest ...any) { pgtest.Scan(t, pool, query, args, dest...) }}
}

func (v pgView) count(query string, args ...any) int {
	v.t.Helper()
	var n int
	v.scan(query, args, &n)
	return n
}

func (v pgView) text(query string, args ...any) string {
	v.t.Helper()
	var s string
	v.scan(query, args, &s)
	return s
}

// outbox counts the rows of one event type bound to one topic.
func (v pgView) outbox(eventType, topic string) int {
	return v.count(`SELECT count(*) FROM outbox_events WHERE event_type = $1 AND topic = $2`, eventType, topic)
}

func createTopics(t *testing.T) {
	t.Helper()
	for _, topic := range allTopics {
		kafkatest.CreateTopic(t, topic)
	}
}

// hintView is the part of a hint that GET /hints returns and the test reads.
type hintView struct {
	HintID         string `json:"hintId"`
	StationID      string `json:"stationId"`
	State          string `json:"state"`
	AfterTaskID    string `json:"afterTaskId"`
	OriginLocation string `json:"originLocation"`
	ConsumedTaskID string `json:"consumedTaskId"`
	Candidates     []struct {
		Rank    int     `json:"rank"`
		TaskID  string  `json:"taskId"`
		DetourM float64 `json:"detourM"`
	} `json:"candidates"`
}

func (s *service) hints(state string) []hintView {
	s.t.Helper()
	var page struct {
		Items []hintView `json:"items"`
	}
	if err := json.Unmarshal([]byte(s.call(http.MethodGet, "/hints?state="+state, "", http.StatusOK)), &page); err != nil {
		s.t.Fatal(err)
	}
	return page.Items
}

// Phase 1: the infrastructure is not there yet. The service boots anyway, is
// alive, reports NOT ready (naming kafka, never the cause) and does not create
// a single topic; once infrastructure creates the topics it becomes ready.
func phaseTopicsMissing(t *testing.T, s *service) {
	t.Helper()
	status, body := s.readyz()
	if status != http.StatusServiceUnavailable || !strings.Contains(body, "/dependency-unavailable") || !strings.Contains(body, "not ready: kafka") ||
		strings.Contains(body, "postgres") {
		t.Fatalf("readyz without topics = %d %s, want 503 dependency-unavailable naming kafka only", status, body)
	}
	for _, topic := range allTopics {
		if kafkatest.TopicExists(t, topic) {
			t.Fatalf("topic %s exists: the service must never create topics", topic)
		}
	}
	createTopics(t)
	waitFor(t, 60*time.Second, "/readyz to turn 200 once the topics exist", func() bool {
		status, _ := s.readyz()
		return status == http.StatusOK
	})
}

const policyBody = `{"maxDetourM":25,"maxCandidates":3,"hintTtlSeconds":3600,"eligibleTaskTypes":["PICK"],"verticalWeight":1}`

// Phase 2: layout, policy and the open tasks, then the third task is claimed
// and completed at L0. Returns the hint the completion issued, as GET /hints
// shows it, and the CloudEvent that left on the integration topic.
func phaseIssue(t *testing.T, s *service, w world, db pgView) (hintView, kafkatest.Message) {
	t.Helper()
	kafkatest.Produce(t, topicFacility, w.layout...)
	s.call(http.MethodPut, "/sites/"+site+"/policy", policyBody, http.StatusOK)
	waitFor(t, 90*time.Second, "the layout copy to land in Postgres", func() bool {
		return db.count(`SELECT count(*) FROM zones`) == 1 && db.count(`SELECT count(*) FROM locations WHERE x_m IS NOT NULL`) == 3
	})
	kafkatest.Produce(t, topicFulfillment, w.tasks...)
	waitFor(t, 90*time.Second, "the open tasks to land in Postgres", func() bool {
		return db.count(`SELECT count(*) FROM task_pool WHERE state = 'AVAILABLE'`) == 3
	})
	kafkatest.Produce(t, topicFulfillment, w.finish...)

	waitFor(t, 90*time.Second, "InterleavingHintIssued on "+topicEvents, func() bool { return kafkatest.EndOffset(t, topicEvents) >= 1 })
	msg := kafkatest.ReadN(t, topicEvents, 1, 30*time.Second)[0]
	active := s.hints("Active")
	if len(active) != 1 {
		t.Fatalf("GET /hints?state=Active = %+v, want the one hint", active)
	}
	return active[0], msg
}

// assertIssued checks the integration event and the REST view against the
// scenario: right station key, candidates ranked by detour, not by task id.
func assertIssued(t *testing.T, msg kafkatest.Message, h hintView) {
	t.Helper()
	e := assertEnvelope(t, msg, typeIssued, "urn:warehouse:task-interleaving:events:InterleavingHintIssued:v1", station)
	var data struct {
		HintID         string `json:"hint_id"`
		StationID      string `json:"station_id"`
		SiteID         string `json:"site_id"`
		AfterTaskID    string `json:"after_task_id"`
		OriginLocation string `json:"origin_location"`
		ExpiresAt      string `json:"expires_at"`
		PolicyVersion  int64  `json:"policy_version"`
		Candidates     []struct {
			Rank     int     `json:"rank"`
			TaskID   string  `json:"task_id"`
			TaskType string  `json:"task_type"`
			Location string  `json:"location"`
			DetourM  float64 `json:"detour_m"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(e.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.HintID != h.HintID || data.StationID != station || data.SiteID != site || data.AfterTaskID != "task-done" ||
		data.OriginLocation != "L0" || data.PolicyVersion != 1 || len(data.Candidates) != 2 {
		t.Fatalf("issued data = %+v, REST hint = %+v", data, h)
	}
	if _, err := time.Parse(time.RFC3339Nano, data.ExpiresAt); err != nil {
		t.Fatalf("expires_at %q: %v", data.ExpiresAt, err)
	}
	near, far := data.Candidates[0], data.Candidates[1]
	if near.Rank != 1 || near.TaskID != "task-z-near" || near.Location != "A" || near.DetourM != 3 || near.TaskType != "PICK" ||
		far.Rank != 2 || far.TaskID != "task-a-far" || far.Location != "B" || far.DetourM != 7 {
		t.Fatalf("candidates = %+v, want task-z-near (3 m) then task-a-far (7 m): ranked by detour, not by id", data.Candidates)
	}
	if h.StationID != station || h.State != "Active" || h.AfterTaskID != "task-done" || h.OriginLocation != "L0" ||
		len(h.Candidates) != 2 || h.Candidates[0].TaskID != "task-z-near" || h.Candidates[1].TaskID != "task-a-far" {
		t.Fatalf("GET /hints?state=Active = %+v", h)
	}
}

// assertIssuedRows checks what the issuing left in Postgres.
func assertIssuedRows(t *testing.T, db pgView, h hintView) {
	t.Helper()
	if got := db.text(`SELECT state FROM hints WHERE hint_id = $1`, h.HintID); got != "Active" {
		t.Fatalf("hint state in Postgres = %s, want Active", got)
	}
	if n := db.count(`SELECT count(*) FROM candidates WHERE hint_id = $1`, h.HintID); n != 2 {
		t.Fatalf("candidate rows = %d, want 2", n)
	}
	if got := db.text(`SELECT location_code FROM station_trips WHERE station_id = $1`, station); got != "L0" {
		t.Fatalf("station position in Postgres = %q, want L0", got)
	}
	if got := db.text(`SELECT state FROM task_pool WHERE task_id = 'task-done'`); got != "COMPLETED" {
		t.Fatalf("task-done in Postgres = %s, want COMPLETED", got)
	}
}

// Phase 3: the hinted station claims the top candidate: the hint is Consumed
// in Postgres and over REST, and InterleavingHintConsumed leaves on the
// analytics topic (which holds the policy declaration, the issued event and
// the consumed event, in that order). Returns the claim message (the replay
// sends the same bytes again).
func phaseConsume(t *testing.T, s *service, w world, db pgView, h hintView) kafkatest.Message {
	t.Helper()
	claimTop := w.claim("task-z-near")
	kafkatest.Produce(t, topicFulfillment, claimTop)
	waitFor(t, 90*time.Second, "the hint to be Consumed in Postgres", func() bool {
		return db.text(`SELECT state FROM hints WHERE hint_id = $1`, h.HintID) == "Consumed"
	})
	if got := db.text(`SELECT consumed_task_id FROM hints WHERE hint_id = $1`, h.HintID); got != "task-z-near" {
		t.Fatalf("consumed task = %s, want task-z-near", got)
	}
	if got := db.text(`SELECT state FROM task_pool WHERE task_id = 'task-z-near'`); got != "CLAIMED" {
		t.Fatalf("task-z-near in Postgres = %s, want CLAIMED", got)
	}
	if n := len(s.hints("Active")); n != 0 {
		t.Fatalf("GET /hints?state=Active holds %d hints after the claim, want none", n)
	}
	consumed := s.hints("Consumed")
	if len(consumed) != 1 || consumed[0].HintID != h.HintID || consumed[0].ConsumedTaskID != "task-z-near" {
		t.Fatalf("GET /hints?state=Consumed = %+v", consumed)
	}
	waitFor(t, 90*time.Second, "three facts on "+topicAnalytics, func() bool { return kafkatest.EndOffset(t, topicAnalytics) >= 3 })
	msgs := kafkatest.ReadN(t, topicAnalytics, 3, 30*time.Second)
	assertEnvelope(t, msgs[0], typePolicy, "urn:warehouse:task-interleaving:analytics:InterleavingPolicyDeclared:v1", site)
	assertEnvelope(t, msgs[1], typeIssued, "urn:warehouse:task-interleaving:analytics:InterleavingHintIssued:v1", station)
	e := assertEnvelope(t, msgs[2], typeConsumed, "urn:warehouse:task-interleaving:analytics:InterleavingHintConsumed:v1", station)
	var data struct {
		HintID  string  `json:"hint_id"`
		TaskID  string  `json:"task_id"`
		Rank    int     `json:"rank"`
		DetourM float64 `json:"detour_m"`
	}
	if err := json.Unmarshal(e.Data, &data); err != nil || data.HintID != h.HintID || data.TaskID != "task-z-near" || data.Rank != 1 || data.DetourM != 3 {
		t.Fatalf("consumed data = %+v (%v)", data, err)
	}
	return claimTop
}

// groupsOf names the consumer group of each consumed topic.
func groupsOf(e env) map[string]string {
	return map[string]string{topicFacility: "LAYOUT-" + e.tag, topicFulfillment: "FULFILLMENT-" + e.tag}
}

// waitCommitted waits until every group committed exactly want[topic] offsets.
func waitCommitted(t *testing.T, e env, what string, want map[string]int64) {
	t.Helper()
	for topic, group := range groupsOf(e) {
		waitFor(t, 90*time.Second, fmt.Sprintf("%s to commit %d offsets of %s (%s)", group, want[topic], topic, what), func() bool {
			return kafkatest.CommittedOffset(t, group, topic) == want[topic]
		})
	}
}

// Phase 4: the same bytes again change nothing. The test waits until each
// consumer group committed past the replay before it looks.
func phaseReplay(t *testing.T, s *service, e env, w world, db pgView, h hintView, claimTop kafkatest.Message) {
	t.Helper()
	// First pass: 7 facility events; 3 announcements, claim + completion and
	// the claim of the top candidate on the fulfillment topic.
	waitCommitted(t, e, "first pass", map[string]int64{topicFacility: 7, topicFulfillment: 6})
	issued, analytics := kafkatest.EndOffset(t, topicEvents), kafkatest.EndOffset(t, topicAnalytics)
	processed := db.count(`SELECT count(*) FROM processed_events`)

	kafkatest.Produce(t, topicFacility, w.layout...)
	fulfillment := append(append(append([]kafkatest.Message{}, w.tasks...), w.finish...), claimTop)
	kafkatest.Produce(t, topicFulfillment, fulfillment...)
	waitCommitted(t, e, "replay", map[string]int64{topicFacility: 14, topicFulfillment: 12})

	if n := db.count(`SELECT count(*) FROM hints`); n != 1 {
		t.Fatalf("hints after the replay = %d, want still 1", n)
	}
	if n := db.outbox(typeIssued, topicEvents); n != 1 {
		t.Fatalf("InterleavingHintIssued outbox rows for the integration topic = %d, want 1", n)
	}
	if n := db.outbox(typeConsumed, topicAnalytics); n != 1 {
		t.Fatalf("InterleavingHintConsumed outbox rows = %d, want 1", n)
	}
	if got := db.count(`SELECT count(*) FROM processed_events`); got != processed {
		t.Fatalf("the replay was processed again: %d processed events, was %d", got, processed)
	}
	if got := kafkatest.EndOffset(t, topicEvents); got != issued {
		t.Fatalf("%s grew from %d to %d messages on a replay", topicEvents, issued, got)
	}
	if got := kafkatest.EndOffset(t, topicAnalytics); got != analytics {
		t.Fatalf("%s grew from %d to %d messages on a replay", topicAnalytics, analytics, got)
	}
	if got := db.text(`SELECT state FROM hints WHERE hint_id = $1`, h.HintID); got != "Consumed" {
		t.Fatalf("hint state after the replay = %s, want Consumed", got)
	}
	if got := db.text(`SELECT state FROM task_pool WHERE task_id = 'task-z-near'`); got != "CLAIMED" {
		t.Fatalf("a replayed TaskAvailable re-opened task-z-near: %s", got)
	}
	if consumed := s.hints("Consumed"); len(consumed) != 1 {
		t.Fatalf("GET /hints?state=Consumed after the replay = %+v", consumed)
	}
}

// TestRealEntryPointRunsOnRealPostgresAndKafkaEndToEnd boots run() against
// real Postgres and Kafka and proves the whole path: consumed events in, REST
// in, rows in Postgres, CloudEvents out through the outbox relay, with a
// restart of the process in the middle and a replay at the end.
func TestRealEntryPointRunsOnRealPostgresAndKafkaEndToEnd(t *testing.T) {
	e := newEnv(t, strings.Join(kafkatest.Brokers(t), ","))
	db := newPGView(t, e.dbURL)
	w := buildWorld(t)

	s := e.start(t)
	phaseTopicsMissing(t, s)
	hint, msg := phaseIssue(t, s, w, db)
	assertIssued(t, msg, hint)
	assertIssuedRows(t, db, hint)

	// The process restarts in the middle: nothing is lost, nothing duplicated.
	s.stop()
	s = e.start(t)
	waitFor(t, 60*time.Second, "/readyz after the restart", func() bool { status, _ := s.readyz(); return status == http.StatusOK })
	if n := db.count(`SELECT count(*) FROM hints`); n != 1 {
		t.Fatalf("hints after the restart = %d, want 1", n)
	}
	if active := s.hints("Active"); len(active) != 1 || active[0].HintID != hint.HintID {
		t.Fatalf("the Active hint did not survive the restart: %+v", active)
	}

	claimTop := phaseConsume(t, s, w, db, hint)
	phaseReplay(t, s, e, w, db, hint, claimTop)
	if n := kafkatest.EndOffset(t, topicEvents); n != 1 {
		t.Fatalf("%s holds %d messages, want exactly InterleavingHintIssued", topicEvents, n)
	}
	if n := kafkatest.EndOffset(t, topicAnalytics); n != 3 {
		t.Fatalf("%s holds %d messages, want policy declared, issued and consumed", topicAnalytics, n)
	}
	s.stop()
}

// A broker outage never crashes the service: it stays alive, reports not-ready
// naming kafka, and keeps its Postgres-backed API up.
func TestServiceStaysAliveButNotReadyWhileKafkaIsUnreachable(t *testing.T) {
	e := newEnv(t, "127.0.0.1:1")
	s := e.start(t)
	if status, _, _ := s.try(http.MethodGet, "/healthz", ""); status != http.StatusOK {
		t.Fatalf("healthz = %d: liveness must not depend on Kafka", status)
	}
	waitFor(t, 30*time.Second, "/readyz to report kafka down", func() bool {
		status, body := s.readyz()
		return status == http.StatusServiceUnavailable && strings.Contains(body, "not ready: kafka") && !strings.Contains(body, "postgres")
	})
	s.call(http.MethodPut, "/sites/"+site+"/policy", policyBody, http.StatusOK)
	s.stop()
}
