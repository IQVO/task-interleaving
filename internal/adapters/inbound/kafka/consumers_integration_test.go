//go:build integration

package kafka_test

import (
	"context"
	"slices"
	"testing"

	kafkaconsumer "github.com/claudioed/task-interleaving/internal/adapters/inbound/kafka"
	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/testing/kafkatest"
)

const (
	hintIssuedRows = "outbox_events WHERE event_type LIKE '%.InterleavingHintIssued'"
	fulfilledTask  = "T0"
	sentinelTask   = "T-9"
)

// fulfillmentScript is the whole fulfillment topic of the test, in order:
//
//  1. e-a0, e-a1, e-a2: T0 (at L0), T-1 (at A) and T-2 (at B) enter the pool
//  2. a legacy flat message: not a CloudEvent, skipped with a WARN
//  3. e-a1 again with ANOTHER payload (T-1 at B): deduped on the id
//  4. an unknown type of the topic: ignored
//  5. a TaskCompleted without task_id: poison, dead-lettered
//  6. e-c0: STN-1 claims T0
//  7. e-done: STN-1 completes T0 (the hint, when a policy is declared)
//  8. e-last: a sentinel task enters the pool
func fulfillmentScript() (messages []kafkatest.Message, poison []byte) {
	poison = fulfillmentMsg("e-poison", typeCompleted, "x", `{"station_id":"STN-1"}`)
	released := fulfillmentMsg("e-rel", "com.warehouse.wes.fulfillment-execution.task.TaskReleased", "T-1", `{"task_id":"T-1"}`)
	messages = keyed("k",
		availableMsg("e-a0", fulfilledTask, "L0"),
		availableMsg("e-a1", "T-1", "A"),
		availableMsg("e-a2", "T-2", "B"),
		[]byte(legacyFlat),
		availableMsg("e-a1", "T-1", "B"),
		released,
		poison,
		claimedMsg("e-c0", fulfilledTask, "STN-1", "2026-10-17T08:59:30Z"),
		completedMsg("e-done", fulfilledTask, "STN-1"),
		availableMsg("e-last", sentinelTask, "A"),
	)
	return messages, poison
}

// runFulfillmentScript feeds the script to a real consumer over real Kafka and
// Postgres and returns once the sentinel is in the pool and every offset was
// committed.
func runFulfillmentScript(t *testing.T, w *pgWorld) {
	t.Helper()
	topic, dlq := newTopic(t, kafkaconsumer.FulfillmentTopic)
	messages, poison := fulfillmentScript()
	kafkatest.Produce(t, topic, messages...)

	group := groupFor("fulfillment", topic)
	runConsumer(t, kafkaconsumer.NewFulfillmentConsumerForTopic(kafkatest.Brokers(t), topic, group, w.fulfillmentUseCases(), itLogger(t)))

	eventually(t, "the sentinel task in the pool", func() bool {
		_, found, err := w.pool.Get(context.Background(), sentinelTask)
		return err == nil && found
	})
	assertCommittedPast(t, group, topic, int64(len(messages)))
	assertOnlyDeadLettered(t, topic, dlq, poison)
}

func assertFulfillmentState(t *testing.T, w *pgWorld) {
	t.Helper()
	ctx := context.Background()
	t1, _, err := w.pool.Get(ctx, "T-1")
	if err != nil || t1.FromLocation != "A" || t1.State != repository.TaskAvailable {
		t.Fatalf("T-1 = %+v, %v: the replayed id must not have moved it to B", t1, err)
	}
	t0, _, err := w.pool.Get(ctx, fulfilledTask)
	if err != nil || t0.State != repository.TaskCompleted || t0.ClaimedBy != "STN-1" {
		t.Fatalf("T0 = %+v, %v, want COMPLETED, claimed by STN-1", t0, err)
	}
	if _, err := w.trips.Get(ctx, "STN-1"); err != nil {
		t.Fatalf("the completion must record STN-1's position: %v", err)
	}
	// e-a0, e-a1, e-a2, e-c0, e-done and e-last: the replay, the unknown type,
	// the legacy message and the poison one claim nothing.
	if n := processedRows(w); n != 6 {
		t.Fatalf("processed_events rows = %d, want 6", n)
	}
}

// TestFulfillmentConsumer_RealKafkaAndPostgres: tasks feed the pool; a replay
// of an id with another payload, an unknown type and a legacy flat message
// change nothing and are committed past; the completion of a task, with a
// policy declared and candidates in the pool, issues an InterleavingHintIssued
// (an integration and an analytics outbox row); the poison completion is the
// only message on the dead-letter topic.
func TestFulfillmentConsumer_RealKafkaAndPostgres(t *testing.T) {
	w := newPGWorld(t)
	w.seedLayout(t)
	w.seedPolicy(t)

	runFulfillmentScript(t, w)

	assertFulfillmentState(t, w)
	if n := w.count(hintIssuedRows); n != 2 {
		t.Fatalf("InterleavingHintIssued outbox rows = %d, want 2 (integration + analytics)", n)
	}
	if n := w.count("outbox_events"); n != 2 {
		t.Fatalf("outbox rows = %d, want only the hint's two: the replays enqueue nothing", n)
	}
}

// TestFulfillmentConsumer_NoPolicyIssuesNoHint: the same topic over a site that
// declared no policy is inert: the position is recorded and nothing is
// published.
func TestFulfillmentConsumer_NoPolicyIssuesNoHint(t *testing.T) {
	w := newPGWorld(t)
	w.seedLayout(t)

	runFulfillmentScript(t, w)

	assertFulfillmentState(t, w)
	if n := w.count("outbox_events"); n != 0 {
		t.Fatalf("outbox rows = %d, want none: a site without a policy is inert", n)
	}
}

// layoutScript is the whole facility topic of the test, in order.
func layoutScript() (messages []kafkatest.Message, poison []byte) {
	poison = facilityMsg("e-poison", typeLocation, "R-99", `{"locationCode":"R-99"}`)
	renamed := facilityMsg("e-ren", "com.warehouse.wms.facility-layout.zone.ZoneRenamed", "z1", `{"zoneId":"z1"}`)
	messages = keyed("k",
		zoneMsg("e-z1", "z1", "S1"),
		[]byte(`{"event_id":"e-z1","event_type":"ZoneRegistered","zoneId":"z1","siteCode":"S1"}`),
		zoneMsg("e-z1", "z1", "S2"),
		renamed,
		locationMsg("e-l1", "R-07", "z1"),
		poison,
		geometryMsg("e-g1", "R-07", 1, 2, 0),
		locationMsg("e-l2", "R-08", "z1"),
		decommissionedMsg("e-d1", "R-08"),
		geometryMsg("e-last", "R-09", 7, 8, 1),
	)
	return messages, poison
}

// TestLayoutConsumer_RealKafkaAndPostgres: zones, locations and geometry feed
// the layout copy; a replayed zone id (naming another site), an unknown type
// and a legacy flat message change nothing and are committed past; a
// decommission is final; the location without a zone is the only dead letter.
func TestLayoutConsumer_RealKafkaAndPostgres(t *testing.T) {
	ctx := context.Background()
	w := newPGWorld(t)

	topic, dlq := newTopic(t, kafkaconsumer.LayoutTopic)
	messages, poison := layoutScript()
	kafkatest.Produce(t, topic, messages...)

	group := groupFor("layout", topic)
	runConsumer(t, kafkaconsumer.NewLayoutConsumerForTopic(kafkatest.Brokers(t), topic, group, w.layoutUseCases(), itLogger(t)))

	eventually(t, "the sentinel geometry of R-09", func() bool {
		places, err := w.book.Places(ctx, []string{"R-09"})
		return err == nil && places["R-09"].Point != nil
	})
	assertCommittedPast(t, group, topic, int64(len(messages)))

	places, err := w.book.Places(ctx, []string{"R-07", "R-08", "R-09", "R-99"})
	if err != nil {
		t.Fatal(err)
	}
	r07 := places["R-07"]
	if r07.SiteCode != "S1" || r07.Decommissioned || r07.Point == nil || r07.Point.X != 1 || r07.Point.Y != 2 {
		t.Fatalf("R-07 = %+v, want an active location of S1 at (1, 2, 0): the replayed zone id must not move the zone", r07)
	}
	if !places["R-08"].Decommissioned {
		t.Fatalf("R-08 = %+v, want decommissioned", places["R-08"])
	}
	if _, known := places["R-99"]; known {
		t.Fatal("the poison location must not be stored")
	}
	if got := sortedKeys(places); !slices.Equal(got, []string{"R-07", "R-08", "R-09"}) {
		t.Fatalf("known locations = %v", got)
	}
	// e-z1, e-l1, e-g1, e-l2, e-d1 and e-last.
	if n := processedRows(w); n != 6 {
		t.Fatalf("processed_events rows = %d, want 6", n)
	}
	assertOnlyDeadLettered(t, topic, dlq, poison)
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
