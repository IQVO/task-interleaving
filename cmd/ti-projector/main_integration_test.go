//go:build integration

package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"

	inboundkafka "github.com/claudioed/task-interleaving/internal/adapters/inbound/kafka"
	"github.com/claudioed/task-interleaving/internal/adapters/outbound/analyticsstore"
	"github.com/claudioed/task-interleaving/internal/adapters/outbound/events"
	"github.com/claudioed/task-interleaving/internal/application/outbox"
	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/domain/policy"
	"github.com/claudioed/task-interleaving/internal/domain/recommend"
	"github.com/claudioed/task-interleaving/internal/domain/stationtrip"
	"github.com/claudioed/task-interleaving/internal/testing/kafkatest"
	"github.com/claudioed/task-interleaving/internal/testing/pgtest"
)

func TestMain(m *testing.M) {
	code := m.Run()
	kafkatest.Shutdown()
	pgtest.Shutdown()
	os.Exit(code)
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().String()
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// recordingOutbox captures what the repo's own event encoder hands to the
// outbox, so the test can produce exactly those bytes to Kafka.
type recordingOutbox struct{ msgs []outbox.Message }

func (r *recordingOutbox) Insert(_ context.Context, msgs ...outbox.Message) error {
	r.msgs = append(r.msgs, msgs...)
	return nil
}

// stopWithSIGTERM stops a signal-driven run(): SIGTERM to self, retried until
// run returns. The test's own handler is registered before the first signal
// and NEVER unregistered — unregistering while a retried signal is still in
// flight would restore the default disposition and kill the test binary.
func stopWithSIGTERM(t *testing.T, done <-chan error) {
	t.Helper()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM)
	deadline := time.Now().Add(30 * time.Second)
	for {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("a SIGTERM must drain the projector cleanly, got %v", err)
			}
			return
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the projector did not stop after SIGTERM")
		}
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		time.Sleep(100 * time.Millisecond)
	}
}

// The real entry point (ADR 0008): env -> the embedded analytical migrations
// on an EMPTY database -> /healthz and /readyz -> the consumer projects the
// events the repo's own encoder wrote to the analytics topic (one fact row per
// hint, the milestone columns and the dedupe set exactly as documented) -> a
// SIGTERM drains cleanly.
func TestProjectorBootsMigratesProjectsAndStopsCleanly(t *testing.T) {
	url := pgtest.EmptyURL(t)
	brokers := kafkatest.Brokers(t)
	kafkatest.CreateTopic(t, inboundkafka.AnalyticsTopic)
	// The projector's dead-letter target must exist (the broker has
	// auto-creation off) so a poison message could never wedge the partition.
	kafkatest.CreateTopic(t, inboundkafka.AnalyticsTopic+inboundkafka.DLQSuffix)

	// Two hints through the encoder the OLTP side really uses: hint-1 issued
	// then consumed, hint-2 issued then expired, plus a policy declaration.
	base := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	station, site := stationtrip.StationID("STN-P1"), policy.SiteID("SITE-P1")
	header := func(at time.Time) stationtrip.Header {
		return stationtrip.Header{Station: station, Site: site, Version: 1, At: at}
	}
	candidate := recommend.Candidate{Rank: 1, TaskID: "task-1", TaskType: policy.TaskType("PICK"), Location: "B-01", DetourM: 4.5}
	captured := &recordingOutbox{}
	sink := events.NewSink(captured)
	ctx := context.Background()
	if err := sink.PublishTripEvents(ctx,
		stationtrip.HintIssued{Header: header(base), HintID: "hint-p1", AfterTask: "task-0", Origin: "A-01",
			ExpiresAt: base.Add(90 * time.Second), PolicyVersion: 1, Candidates: []recommend.Candidate{candidate}},
		stationtrip.HintConsumed{Header: header(base.Add(20 * time.Second)), HintID: "hint-p1", TaskID: "task-1", Rank: 1, DetourM: 4.5},
		stationtrip.HintIssued{Header: header(base.Add(time.Minute)), HintID: "hint-p2", AfterTask: "task-0", Origin: "A-01",
			ExpiresAt: base.Add(150 * time.Second), PolicyVersion: 1, Candidates: []recommend.Candidate{candidate}},
		stationtrip.HintExpired{Header: header(base.Add(3 * time.Minute)), HintID: "hint-p2", ExpiresAt: base.Add(150 * time.Second)},
	); err != nil {
		t.Fatal(err)
	}
	if err := sink.PublishPolicyDeclared(ctx, repository.PolicyDeclaration{
		SiteID: "SITE-P1", MaxDetourM: 25, MaxCandidates: 3, HintTTLSeconds: 90,
		EligibleTaskTypes: []string{"PICK"}, VerticalWeight: 1, Version: 1, DeclaredAt: base.Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	analytics := 0
	for _, m := range captured.msgs {
		if m.Topic != inboundkafka.AnalyticsTopic {
			continue
		}
		analytics++
		headers := map[string]string{}
		for _, h := range m.Headers {
			headers[h.Key] = h.Value
		}
		kafkatest.Produce(t, inboundkafka.AnalyticsTopic, kafkatest.Message{Key: string(m.Key), Value: m.Value, Headers: headers})
	}
	// Two issued events also built their integration-topic copies; the five
	// analytics messages are everything the projector consumes.
	if analytics != 5 {
		t.Fatalf("analytics messages encoded = %d, want 5", analytics)
	}

	group := fmt.Sprintf("ti-projector-itest-%d", time.Now().UnixNano())
	addr := freeAddr(t)
	t.Setenv("ANALYTICS_DATABASE_URL", url)
	t.Setenv("KAFKA_BROKERS", brokers[0])
	t.Setenv("ANALYTICS_CONSUMER_GROUP", group)
	t.Setenv("ADMIN_ADDR", addr)
	t.Setenv("LOG_LEVEL", "error")
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- run() }()

	// The documented admin probe: /healthz (liveness) and /readyz.
	waitUntil(t, "/healthz 200", func() bool {
		resp, err := http.Get("http://" + addr + "/healthz")
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})
	waitUntil(t, "/readyz 200", func() bool {
		resp, err := http.Get("http://" + addr + "/readyz")
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})

	pool, err := analyticsstore.NewPool(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	// ADR 0008 §2: one hint_facts row per hint, each milestone the CloudEvents
	// time of the event that marks it, the attributes first-writer-wins.
	waitUntil(t, "the two hints folded into hint_facts", func() bool {
		var total, hint1, hint2 int
		err := pool.QueryRow(ctx, `SELECT count(*),
			count(*) FILTER (WHERE hint_id = 'hint-p1' AND station_id = 'STN-P1' AND site_id = 'SITE-P1'
				AND issued_at = $1 AND expires_at = $2 AND candidate_count = 1
				AND consumed_at = $3 AND consumed_rank = 1 AND consumed_detour_m = 4.5 AND expired_at IS NULL),
			count(*) FILTER (WHERE hint_id = 'hint-p2' AND expired_at = $4 AND consumed_at IS NULL)
			FROM hint_facts`, base, base.Add(90*time.Second), base.Add(20*time.Second), base.Add(3*time.Minute)).Scan(&total, &hint1, &hint2)
		return err == nil && total == 2 && hint1 == 1 && hint2 == 1
	})
	// ADR 0008 §2: the dedupe set records every applied id exactly once, the
	// policy declaration only as an applied id; max(occurred_at) is the
	// freshness source.
	var processed time.Time
	var ids int
	waitUntil(t, "the five events recorded in analytics_processed_events", func() bool {
		return pool.QueryRow(ctx, `SELECT count(*), max(occurred_at) FROM analytics_processed_events`).Scan(&ids, &processed) == nil && ids == 5
	})
	if !processed.Equal(base.Add(3 * time.Minute)) {
		t.Fatalf("freshness source max(occurred_at) = %v, want %v", processed, base.Add(3*time.Minute))
	}
	// ADR 0008 §3: offsets are committed only after success — a fresh group
	// that handled the five messages sits at offset 5.
	waitUntil(t, "the consumer group to commit offset 5", func() bool {
		return kafkatest.CommittedOffset(t, group, inboundkafka.AnalyticsTopic) == 5
	})

	stopWithSIGTERM(t, done)
}
