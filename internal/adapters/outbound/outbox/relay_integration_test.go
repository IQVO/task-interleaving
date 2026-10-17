//go:build integration

package outbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"
	tckafka "github.com/testcontainers/testcontainers-go/modules/kafka"

	"github.com/claudioed/task-interleaving/internal/adapters/kafka/cloudevents"
	outboundkafka "github.com/claudioed/task-interleaving/internal/adapters/outbound/kafka"
	relay "github.com/claudioed/task-interleaving/internal/adapters/outbound/outbox"
	"github.com/claudioed/task-interleaving/internal/adapters/outbound/postgres"
	"github.com/claudioed/task-interleaving/internal/application/outbox"
	"github.com/claudioed/task-interleaving/internal/testing/kafkatest"
	"github.com/claudioed/task-interleaving/internal/testing/pgtest"
)

// The relay is proven end to end against real infrastructure, both started with
// testcontainers (never skip-gated, never a hardcoded broker address): the use
// cases write the aggregates AND their events to real Postgres inside one unit
// of work, the relay drains the outbox to a real broker on explicitly created
// topics, and the CloudEvents are read back and compared with the golden
// envelopes of the events adapter.

// The broker itself comes from internal/testing/kafkatest, which starts it
// through testcontainers-go/modules/kafka (referenced here so the module import
// stays reachable from this Kafka-touching file).
var _ = tckafka.Run

const readTimeout = 60 * time.Second

// idle is how long a topic must stay silent before ReadAll calls it complete.
const idle = 4 * time.Second

// golden loads a golden envelope of the events adapter: the same bytes that
// adapter's golden test pins, so the wire contract has ONE source of truth.
func golden(t *testing.T, name string) any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "events", "testdata", name))
	if err != nil {
		t.Fatalf("read golden %s: %v", name, err)
	}
	return decodeJSON(t, name, raw)
}

func decodeJSON(t *testing.T, what string, raw []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("%s is not JSON: %v\n%s", what, err, raw)
	}
	return v
}

// assertEnvelope checks one consumed message: the Kafka key, exactly the
// CloudEvents content-type header, a valid CloudEvents 1.0 value and the whole
// envelope against its golden file.
func assertEnvelope(t *testing.T, msg kafkatest.Message, wantKey, goldenName string) {
	t.Helper()
	if msg.Key != wantKey {
		t.Errorf("%s: key = %q, want %q", goldenName, msg.Key, wantKey)
	}
	if len(msg.Headers) != 1 || msg.Headers["content-type"] != cloudevents.MediaType {
		t.Errorf("%s: headers = %v, want exactly the content-type %q", goldenName, msg.Headers, cloudevents.MediaType)
	}
	if _, err := cloudevents.Decode(msg.Value); err != nil {
		t.Fatalf("%s: not a valid CloudEvent: %v", goldenName, err)
	}
	if got, want := decodeJSON(t, goldenName, msg.Value), golden(t, goldenName); !reflect.DeepEqual(got, want) {
		t.Errorf("%s: envelope differs from the golden\n got: %s\nwant: %v", goldenName, msg.Value, want)
	}
}

// assertPolicyV1 checks the first declaration (no golden: the golden is version
// 2): the analytics-only type, its id, the site key and version 1.
func assertPolicyV1(t *testing.T, msg kafkatest.Message) {
	t.Helper()
	e, err := cloudevents.Decode(msg.Value)
	if err != nil {
		t.Fatalf("policy v1: not a valid CloudEvent: %v", err)
	}
	var data struct {
		Version int64 `json:"version"`
	}
	if err := e.DataAs(&data); err != nil {
		t.Fatal(err)
	}
	if e.ID() != idPolicyV1 || e.Type() != cloudevents.Type("policy", "InterleavingPolicyDeclared") || msg.Key != siteID || data.Version != 1 {
		t.Errorf("policy v1 = id %q type %q key %q version %d", e.ID(), e.Type(), msg.Key, data.Version)
	}
}

func runRelay(t *testing.T, r *relay.Relay) {
	t.Helper()
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = r.Run(ctx) }()
	t.Cleanup(func() { stop(); <-done })
}

func kafkaSink(t *testing.T) relay.Sink {
	t.Helper()
	s := outboundkafka.NewRelaySink(kafkatest.Brokers(t))
	t.Cleanup(func() { _ = s.Close() })
	return patientSink{inner: s}
}

// patientSink retries the one error a just-created topic can still answer with
// for a moment after its leader is visible (UnknownTopicOrPartition), so the
// tests that drive RelayOnce by hand do not race the broker's metadata. Any
// other error is returned at once.
type patientSink struct{ inner relay.Sink }

func (p patientSink) Send(ctx context.Context, msgs ...outbox.Message) error {
	var err error
	for attempt := 0; attempt < 50; attempt++ {
		if err = p.inner.Send(ctx, msgs...); err == nil || !errors.Is(err, kafkago.UnknownTopicOrPartition) {
			return err
		}
		time.Sleep(200 * time.Millisecond)
	}
	return err
}

func productionRelay(w *world, sink relay.Sink) *relay.Relay {
	return relay.NewRelay(postgres.NewOutboxRepo(w.pool), sink, quiet(),
		relay.WithInterval(100*time.Millisecond), relay.WithMaxAttempts(relay.DefaultMaxAttempts))
}

// TestRelay_HintIssuedThenConsumed_PublishesTheExactEnvelopes: a policy declared
// twice, a TaskCompleted that issues a hint and the claim of its rank-1
// candidate go through the production use cases and EventSink; the production
// relay and RelaySink publish them. The integration topic carries ONLY the
// hint; the analytics topic carries the two declarations, the hint again (same
// id, analytics dataschema) and the consumption.
func TestRelay_HintIssuedThenConsumed_PublishesTheExactEnvelopes(t *testing.T) {
	w := newWorld(t, idPolicyV1, idPolicyV2, idIssued, idConsumed)
	w.seedLayout()
	w.declarePolicy()
	w.completeTask()
	w.claimCandidate()
	runRelay(t, productionRelay(w, kafkaSink(t)))

	got := kafkatest.ReadN(t, w.eventsTopic, 1, readTimeout)
	assertEnvelope(t, got[0], stationID, "issued.events.golden.json")

	an := kafkatest.ReadN(t, w.analyticsTopic, 4, readTimeout)
	assertPolicyV1(t, an[0])
	assertEnvelope(t, an[1], siteID, "policy_declared.analytics.golden.json")
	assertEnvelope(t, an[2], stationID, "issued.analytics.golden.json")
	assertEnvelope(t, an[3], stationID, "consumed.analytics.golden.json")
	pgtest.WaitAllPublished(t, w.pool)
	assertNoMore(t, w.eventsTopic, 1)
	assertNoMore(t, w.analyticsTopic, 4)
}

// TestRelay_HintIssuedThenExpired_PublishesTheExactEnvelopes: same flow with the
// hint left to expire; the sweeper's ExpireHints enqueues the analytics-only
// expiry and nothing else reaches the integration topic.
func TestRelay_HintIssuedThenExpired_PublishesTheExactEnvelopes(t *testing.T) {
	w := newWorld(t, idPolicyV1, idPolicyV2, idIssued, idExpired)
	w.seedLayout()
	w.declarePolicy()
	w.completeTask()
	w.expireHint()
	runRelay(t, productionRelay(w, kafkaSink(t)))

	got := kafkatest.ReadN(t, w.eventsTopic, 1, readTimeout)
	assertEnvelope(t, got[0], stationID, "issued.events.golden.json")

	an := kafkatest.ReadN(t, w.analyticsTopic, 4, readTimeout)
	assertPolicyV1(t, an[0])
	assertEnvelope(t, an[1], siteID, "policy_declared.analytics.golden.json")
	assertEnvelope(t, an[2], stationID, "issued.analytics.golden.json")
	assertEnvelope(t, an[3], stationID, "expired.analytics.golden.json")
	pgtest.WaitAllPublished(t, w.pool)
	assertNoMore(t, w.eventsTopic, 1)
	assertNoMore(t, w.analyticsTopic, 4)
}

// assertNoMore proves the topic holds exactly want messages: no duplicate.
func assertNoMore(t *testing.T, topic string, want int) {
	t.Helper()
	if all := kafkatest.ReadAll(t, topic, idle); len(all) != want {
		t.Fatalf("%s holds %d messages, want exactly %d (no duplicates)", topic, len(all), want)
	}
}

// crashAfter is a relay.Sink that delivers the first n messages to the real
// broker and then fails every send, like a process that died mid-pass.
type crashAfter struct {
	inner relay.Sink
	n     int
	sent  int
}

var errCrash = errors.New("simulated crash")

func (c *crashAfter) Send(ctx context.Context, msgs ...outbox.Message) error {
	if c.sent >= c.n {
		return errCrash
	}
	if err := c.inner.Send(ctx, msgs...); err != nil {
		return err
	}
	c.sent += len(msgs)
	return nil
}

// TestRelay_CrashAndRestartDoesNotDuplicate: a relay dies after publishing two of
// the five rows. The rows it committed as published are not sent again by the
// next relay, which publishes only the remaining three; every topic ends up with
// each message exactly once.
func TestRelay_CrashAndRestartDoesNotDuplicate(t *testing.T) {
	w := newWorld(t, idPolicyV1, idPolicyV2, idIssued, idConsumed)
	w.seedLayout()
	w.declarePolicy()
	w.completeTask()
	w.claimCandidate()
	broker := kafkaSink(t)

	first := productionRelay(w, &crashAfter{inner: broker, n: 2})
	published, err := first.RelayOnce(context.Background())
	var failure *outbox.SendFailure
	if !errors.Is(err, errCrash) || !errors.As(err, &failure) || published != 2 {
		t.Fatalf("crashing pass = %d, %v; want 2 published then the crash", published, err)
	}
	if n := pgtest.Count(t, w.pool, "outbox_events WHERE published_at IS NOT NULL"); n != 2 {
		t.Fatalf("%d rows committed as published, want 2", n)
	}

	restarted, err := productionRelay(w, broker).RelayOnce(context.Background())
	if err != nil || restarted != 3 {
		t.Fatalf("restarted pass = %d, %v; want only the 3 remaining rows", restarted, err)
	}
	pgtest.WaitAllPublished(t, w.pool)
	assertNoMore(t, w.eventsTopic, 1)
	assertNoMore(t, w.analyticsTopic, 4)
}

// cancelAfterSend delivers to the real broker and then cancels the relay's
// context, the way a crash between the broker's acknowledgement and the
// outbox's UPDATE happens: the write is out, the row is not marked.
type cancelAfterSend struct {
	inner  relay.Sink
	cancel context.CancelFunc
}

func (c *cancelAfterSend) Send(ctx context.Context, msgs ...outbox.Message) error {
	err := c.inner.Send(ctx, msgs...)
	c.cancel()
	return err
}

// TestRelay_CrashBetweenSendAndMarkRepublishesTheSameID states the at-least-once
// edge: when the relay dies between the broker's ack and the UPDATE, the row is
// sent once more on restart, byte for byte the same message with the same
// CloudEvents id, so a consumer's dedupe on id collapses it. The hint's
// analytics row, which the crashed pass never reached, is sent exactly once.
func TestRelay_CrashBetweenSendAndMarkRepublishesTheSameID(t *testing.T) {
	w := newWorld(t, idIssued)
	w.seedLayout()
	w.seedPolicy()
	w.completeTask()
	broker := kafkaSink(t)

	ctx, cancel := context.WithCancel(context.Background())
	if _, err := productionRelay(w, &cancelAfterSend{inner: broker, cancel: cancel}).RelayOnce(ctx); err == nil {
		t.Fatal("the pass that lost its context after the send must fail")
	}
	if n := pgtest.Count(t, w.pool, "outbox_events WHERE published_at IS NOT NULL"); n != 0 {
		t.Fatalf("%d rows marked published after the crash, want none", n)
	}
	if n, err := productionRelay(w, broker).RelayOnce(context.Background()); err != nil || n != 2 {
		t.Fatalf("restarted pass = %d, %v; want both rows of the hint", n, err)
	}

	twice := kafkatest.ReadAll(t, w.eventsTopic, idle)
	if len(twice) != 2 || string(twice[0].Value) != string(twice[1].Value) {
		t.Fatalf("integration topic = %d messages, want the same message twice", len(twice))
	}
	if e, err := cloudevents.Decode(twice[0].Value); err != nil || e.ID() != idIssued {
		t.Fatalf("redelivered id = %v (%v), want %s", e.ID(), err, idIssued)
	}
	assertNoMore(t, w.analyticsTopic, 1)
}
