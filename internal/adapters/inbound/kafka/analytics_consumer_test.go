package kafka_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	kafkaconsumer "github.com/claudioed/task-interleaving/internal/adapters/inbound/kafka"
	"github.com/claudioed/task-interleaving/internal/analytics/report"
)

var errInjected = errors.New("injected transient projection failure")

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeProjection is a scripted report.Projection for the analytics consumer.
type fakeProjection struct {
	mu      sync.Mutex
	applied []report.Event
	fail    int // transient failures before success
	reject  bool
}

func (p *fakeProjection) Apply(_ context.Context, e report.Event) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.reject {
		return false, errors.Join(report.ErrRejected, errors.New("integrity violation"))
	}
	if p.fail > 0 {
		p.fail--
		return false, errInjected
	}
	p.applied = append(p.applied, e)
	return true, nil
}

func (p *fakeProjection) events() []report.Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]report.Event(nil), p.applied...)
}

func (p *fakeProjection) count() int { return len(p.events()) }

const analyticsSource = "/warehouse/task-interleaving"

func analyticsMsg(id, entity, name, subject, data string) []byte {
	return ce(id, analyticsSource, "com.warehouse.wes.task-interleaving."+entity+"."+name, subject,
		"urn:warehouse:task-interleaving:analytics:"+name+":v1", data)
}

const issuedData = `{"hint_id":"hint-1","station_id":"STN-07","site_id":"SITE-1","after_task_id":"task-8a1f","origin_location":"A-01",` +
	`"expires_at":"2026-10-17T09:01:30Z","policy_version":2,"candidates":[` +
	`{"rank":1,"task_id":"task-9c2e","task_type":"PICK","location":"B-01","detour_m":4.5},` +
	`{"rank":2,"task_id":"task-9d10","task_type":"REPLENISH","location":"B-02","detour_m":12}]}`

func issuedMsg(id string) []byte {
	return analyticsMsg(id, "stationtrip", "InterleavingHintIssued", "STN-07", issuedData)
}

func newAnalyticsConsumer(p *fakeProjection, dlq kafkaconsumer.DeadLetterWriter, reader *fakeReader) *kafkaconsumer.AnalyticsConsumer {
	return &kafkaconsumer.AnalyticsConsumer{
		Reader: reader, Projection: p, DLQ: dlq, DLQTopic: "warehouse.task-interleaving.analytics.dlq",
		Logger: quietLogger(), Retry: fastRetry,
	}
}

func TestAnalyticsConsumer_AppliesEveryKind(t *testing.T) {
	ctx := context.Background()
	p := &fakeProjection{}
	c := newAnalyticsConsumer(p, &fakeDLQWriter{}, newFakeReader())
	for _, v := range [][]byte{
		issuedMsg("e1"),
		analyticsMsg("e2", "stationtrip", "InterleavingHintConsumed", "STN-07",
			`{"hint_id":"hint-1","station_id":"STN-07","site_id":"SITE-1","task_id":"task-9c2e","rank":1,"detour_m":4.5,"consumed_at":"2026-10-17T09:00:20Z"}`),
		analyticsMsg("e3", "stationtrip", "InterleavingHintExpired", "STN-07",
			`{"hint_id":"hint-2","station_id":"STN-07","site_id":"SITE-1","expires_at":"2026-10-17T09:01:30Z"}`),
		analyticsMsg("e4", "policy", "InterleavingPolicyDeclared", "SITE-1",
			`{"site_id":"SITE-1","max_detour_m":25,"max_candidates":3,"hint_ttl_seconds":90,"eligible_task_types":["PICK"],"vertical_weight":1.5,"version":2,"declared_at":"2026-10-17T09:00:00Z"}`),
	} {
		if err := c.HandleMessage(ctx, kafkago.Message{Value: v}); err != nil {
			t.Fatalf("err = %v", err)
		}
	}
	got := p.events()
	if len(got) != 4 {
		t.Fatalf("applied = %+v, want 4", got)
	}
	wantExpiry := time.Date(2026, 10, 17, 9, 1, 30, 0, time.UTC)
	want := []report.Event{
		{Kind: report.KindHintIssued, EventID: "e1", HintID: "hint-1", Station: "STN-07", SiteID: "SITE-1", CandidateCount: 2, ExpiresAt: wantExpiry},
		{Kind: report.KindHintConsumed, EventID: "e2", HintID: "hint-1", Station: "STN-07", SiteID: "SITE-1", Rank: 1, DetourM: 4.5},
		{Kind: report.KindHintExpired, EventID: "e3", HintID: "hint-2", Station: "STN-07", SiteID: "SITE-1"},
		{Kind: report.KindPolicyDeclared, EventID: "e4", SiteID: "SITE-1"},
	}
	for i := range want {
		want[i].At = t0 // the CloudEvents time of every fixture message
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Errorf("event %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestAnalyticsConsumer_SkipsWhatItCannotRoute(t *testing.T) {
	ctx := context.Background()
	p := &fakeProjection{}
	dlq := &fakeDLQWriter{}
	c := newAnalyticsConsumer(p, dlq, newFakeReader())
	// A non-CloudEvents message (the retired flat envelope) is skipped.
	if err := c.HandleMessage(ctx, kafkago.Message{Value: []byte(legacyFlat)}); err != nil {
		t.Fatalf("err = %v, want nil (skipped)", err)
	}
	// A valid CloudEvent of another type is ignored.
	foreign := ce("x", "/warehouse/fulfillment-execution", typeAvailable, "task-1", "urn:warehouse:fulfillment-execution:events:TaskAvailable:v1", `{}`)
	if err := c.HandleMessage(ctx, kafkago.Message{Value: foreign}); err != nil {
		t.Fatalf("err = %v, want nil (ignored)", err)
	}
	if p.count() != 0 || len(dlq.published()) != 0 {
		t.Fatalf("applied = %d, dead-lettered = %d; want none", p.count(), len(dlq.published()))
	}
}

var poisonMessages = map[string][]byte{
	"station contradicting the subject": analyticsMsg("e1", "stationtrip", "InterleavingHintExpired", "STN-OTHER",
		`{"hint_id":"hint-1","station_id":"STN-07","site_id":"SITE-1","expires_at":"2026-10-17T09:01:30Z"}`),
	"site contradicting the policy subject": analyticsMsg("e2", "policy", "InterleavingPolicyDeclared", "SITE-9",
		`{"site_id":"SITE-1","version":1}`),
	"issued with a bad expires_at": analyticsMsg("e3", "stationtrip", "InterleavingHintIssued", "STN-07",
		`{"hint_id":"hint-1","station_id":"STN-07","site_id":"SITE-1","expires_at":"soon","candidates":[{"rank":1}]}`),
	"issued without candidates": analyticsMsg("e4", "stationtrip", "InterleavingHintIssued", "STN-07",
		`{"hint_id":"hint-1","station_id":"STN-07","site_id":"SITE-1","expires_at":"2026-10-17T09:01:30Z","candidates":[]}`),
	"consumed with a zero rank": analyticsMsg("e5", "stationtrip", "InterleavingHintConsumed", "STN-07",
		`{"hint_id":"hint-1","station_id":"STN-07","site_id":"SITE-1","rank":0,"detour_m":1}`),
	"expired without a site": analyticsMsg("e6", "stationtrip", "InterleavingHintExpired", "STN-07",
		`{"hint_id":"hint-1","station_id":"STN-07"}`),
	"data of the wrong shape": analyticsMsg("e7", "stationtrip", "InterleavingHintExpired", "STN-07", `[1,2]`),
}

func headerValue(m kafkago.Message, key string) string {
	for _, h := range m.Headers {
		if h.Key == key {
			return string(h.Value)
		}
	}
	return ""
}

func TestAnalyticsConsumer_DeadLettersDeterministicFailures(t *testing.T) {
	for name, value := range poisonMessages {
		t.Run(name, func(t *testing.T) {
			p := &fakeProjection{}
			dlq := &fakeDLQWriter{}
			c := newAnalyticsConsumer(p, dlq, newFakeReader())
			if err := c.HandleMessage(context.Background(), kafkago.Message{Partition: 3, Offset: 7, Value: value}); err != nil {
				t.Fatalf("err = %v, want nil (dead-lettered)", err)
			}
			published := dlq.published()
			if p.count() != 0 || len(published) != 1 || published[0].Topic != c.DLQTopic || string(published[0].Value) != string(value) {
				t.Fatalf("applied = %d, dead-lettered = %+v", p.count(), published)
			}
			if headerValue(published[0], "x-dlq-error") == "" || headerValue(published[0], "x-dlq-failed-at") == "" {
				t.Fatal("the dead-lettered message must carry x-dlq-error and x-dlq-failed-at")
			}
		})
	}
}

// A store rejection is dead-lettered at once.
func TestAnalyticsConsumer_DeadLettersStoreRejections(t *testing.T) {
	dlq := &fakeDLQWriter{}
	c := newAnalyticsConsumer(&fakeProjection{reject: true}, dlq, newFakeReader())
	if err := c.HandleMessage(context.Background(), kafkago.Message{Value: issuedMsg("e1")}); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(dlq.published()) != 1 {
		t.Fatalf("dead-lettered = %d, want 1", len(dlq.published()))
	}
}

func TestAnalyticsConsumer_RetriesTransientFailures(t *testing.T) {
	ctx := context.Background()
	p := &fakeProjection{fail: 1}
	dlq := &fakeDLQWriter{}
	c := newAnalyticsConsumer(p, dlq, newFakeReader())
	err := c.HandleMessage(ctx, kafkago.Message{Value: issuedMsg("e1")})
	if !errors.Is(err, errInjected) {
		t.Fatalf("err = %v, want the injected transient error", err)
	}
	if len(dlq.published()) != 0 {
		t.Fatalf("dead-lettered = %d, want 0", len(dlq.published()))
	}

	// A failed DLQ write is a transient error too (the poison is retried, never lost).
	c2 := newAnalyticsConsumer(&fakeProjection{reject: true}, failingDLQ{}, newFakeReader())
	err = c2.HandleMessage(ctx, kafkago.Message{Value: issuedMsg("e1")})
	if err == nil || errors.Is(err, report.ErrRejected) {
		t.Fatalf("err = %v, want the DLQ write failure", err)
	}
}

// failingDLQ always fails, like an unreachable DLQ topic.
type failingDLQ struct{}

func (failingDLQ) WriteMessages(context.Context, ...kafkago.Message) error {
	return errors.New("dlq unreachable")
}
func (failingDLQ) Close() error { return nil }

// Through the real run loop, a poison message is dead-lettered and its offset
// committed; a message whose projection fails transiently is retried until it
// succeeds and only then committed.
func TestAnalyticsConsumer_RunCommitsPastPoisonAndRetriesTransient(t *testing.T) {
	poison := analyticsMsg("e1", "stationtrip", "InterleavingHintExpired", "STN-OTHER",
		`{"hint_id":"hint-1","station_id":"STN-07","site_id":"SITE-1","expires_at":"2026-10-17T09:01:30Z"}`)
	reader := newFakeReader(poison, issuedMsg("e2"))
	// The first Apply of the SECOND message fails once; the poison never reaches
	// Apply (it fails validation).
	p := &fakeProjection{fail: 1}
	dlq := &fakeDLQWriter{}
	c := newAnalyticsConsumer(p, dlq, reader)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	deadline := time.After(10 * time.Second)
	for reader.commits() < 2 {
		select {
		case err := <-done:
			t.Fatalf("Run returned early: %v", err)
		case <-deadline:
			t.Fatalf("timed out; events = %v, applied = %d, dlq = %d", reader.log(), p.count(), len(dlq.published()))
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if p.count() != 1 {
		t.Fatalf("applied = %d, want only the healthy message", p.count())
	}
	if len(dlq.published()) != 1 || dlq.published()[0].Topic != c.DLQTopic {
		t.Fatalf("dead-lettered = %+v, want the poison message once", dlq.published())
	}
	if got := reader.log(); got[len(got)-1] != "commit:1" {
		t.Fatalf("events = %v; the last commit must be the retried message", got)
	}
}

func TestAnalyticsConsumer_SkipsNeverFail(t *testing.T) {
	c := newAnalyticsConsumer(&fakeProjection{}, &fakeDLQWriter{}, newFakeReader())
	c.Now = func() time.Time { return t0 }
	for range 3 {
		if err := c.HandleMessage(context.Background(), kafkago.Message{Value: []byte("nope")}); err != nil {
			t.Fatalf("a skipped message returned %v, want nil", err)
		}
	}
}

// A CR/LF smuggled in an id can never forge a log line.
func TestAnalyticsConsumer_LogsStripLineBreaks(t *testing.T) {
	var buf logBuffer
	c := newAnalyticsConsumer(&fakeProjection{fail: 1}, &fakeDLQWriter{}, newFakeReader())
	c.Logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	foreign := ce("bad\r\nid", "/x", "com.example.other.Type", "s", "urn:x", `{}`)
	_ = c.HandleMessage(context.Background(), kafkago.Message{Value: foreign})
	err := c.HandleMessage(context.Background(), kafkago.Message{Value: issuedMsgWithID("evil\r\nforged=1")})
	if err == nil || strings.ContainsAny(err.Error(), "\r\n") {
		t.Fatalf("err = %q, want a transient error without line breaks", err)
	}
	if strings.Contains(buf.String(), "\nid") {
		t.Fatalf("log line forged: %q", buf.String())
	}
}

func issuedMsgWithID(id string) []byte {
	return analyticsMsg(strings.NewReplacer("\r", `\r`, "\n", `\n`).Replace(id), "stationtrip", "InterleavingHintIssued", "STN-07", issuedData)
}
