//go:build integration

package kafka_test

import (
	"context"
	"testing"
	"time"

	kafkaconsumer "github.com/claudioed/task-interleaving/internal/adapters/inbound/kafka"
	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/domain/policy"
	"github.com/claudioed/task-interleaving/internal/domain/stationtrip"
	"github.com/claudioed/task-interleaving/internal/testing/kafkatest"
)

// PICK-batching wave 2c: PICK is a first-class interleaving candidate (ADR
// 0012). fulfillment-execution does not announce PICK tasks yet (that lands
// with its pick-placement change); these tests feed synthetic TaskAvailable
// fixtures of the already-pinned contract through the REAL Kafka consumer and
// REAL Postgres, so the pool-to-hint path for a PICK announcement is proven
// end to end and cannot drift from the RELOCATE/REPLENISH path while the
// producer change is in flight.

// typedAvailableMsg is a TaskAvailable of site S1 for a task of any floor
// type, going from `from` and ending at `to` — the producer's contract as raw
// JSON (fixtures_test.go's availableMsg hardcodes PICK and one location).
func typedAvailableMsg(id, task, taskType, from, to string) []byte {
	data := `{"task_id":"` + task + `","task_type":"` + taskType + `","site_id":"S1","from_location":"` + from + `","to_location":"` + to + `",` +
		`"required_capabilities":[],"priority_band":"P1","available_at":"2026-10-17T08:59:00Z"}`
	return fulfillmentMsg(id, typeAvailable, task, data)
}

// seedPolicyFor declares S1's policy (v1) with the given eligible types and
// candidate bound.
func seedPolicyFor(t *testing.T, w *pgWorld, maxCandidates int, types ...policy.TaskType) {
	t.Helper()
	p, err := policy.New("S1", policy.Settings{
		MaxDetourM: 25, MaxCandidates: maxCandidates, HintTTL: 90 * time.Second,
		EligibleTaskTypes: types, VerticalWeight: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.policies.Save(context.Background(), repository.PolicyRecord{Policy: p, DeclaredAt: time.Now().UTC()}, 0); err != nil {
		t.Fatal(err)
	}
}

// seedGeometry registers one location of zone z1 with its point.
func seedGeometry(t *testing.T, w *pgWorld, code string, x, y float64) {
	t.Helper()
	ctx := context.Background()
	if err := w.book.SaveLocation(ctx, code, "z1"); err != nil {
		t.Fatal(err)
	}
	if err := w.book.SaveGeometry(ctx, code, x, y, 0); err != nil {
		t.Fatal(err)
	}
}

// activeHintOf reads STN-1's Active hint from Postgres.
func activeHintOf(t *testing.T, w *pgWorld) stationtrip.Hint {
	t.Helper()
	trip, err := w.trips.Get(context.Background(), "STN-1")
	if err != nil {
		t.Fatalf("load STN-1's trip: %v", err)
	}
	h, ok := trip.ActiveHint()
	if !ok {
		t.Fatal("STN-1 has no Active hint")
	}
	return h
}

// runPickScript drives the real fulfillment consumer over real Kafka and
// Postgres through the messages and returns once STN-1's hint is Active and
// every offset was committed. The hint is awaited first because it is in
// Postgres: by the time it exists the consumer group has joined and committed,
// so the offset assertion cannot race a group that is not registered yet.
func runPickScript(t *testing.T, w *pgWorld, messages []kafkatest.Message) {
	t.Helper()
	topic, _ := newTopic(t, kafkaconsumer.FulfillmentTopic)
	kafkatest.Produce(t, topic, messages...)
	group := groupFor("fulfillment", topic)
	runConsumer(t, kafkaconsumer.NewFulfillmentConsumerForTopic(kafkatest.Brokers(t), topic, group, w.fulfillmentUseCases(), itLogger(t)))
	eventually(t, "STN-1's hint to be issued", func() bool {
		trip, err := w.trips.Get(context.Background(), "STN-1")
		if err != nil {
			return false
		}
		_, ok := trip.ActiveHint()
		return ok
	})
	assertCommittedPast(t, group, topic, int64(len(messages)))
}

// TestFulfillmentConsumer_PickTaskAvailableRanksWithTheOtherFloorTypes: a
// mixed pool — one PICK, one RELOCATE and one REPLENISH task, all eligible —
// plus a completed PICK task issues a hint whose candidate list interleaves
// the three types by detour: a PICK TaskAvailable is ranked exactly like the
// announced types, with no type ordering of its own.
func TestFulfillmentConsumer_PickTaskAvailableRanksWithTheOtherFloorTypes(t *testing.T) {
	w := newPGWorld(t)
	w.seedLayout(t)
	seedGeometry(t, w, "C", 6, 8) // 14 m from L0 (0,0,0), rectilinear
	seedPolicyFor(t, w, 5, policy.TaskPick, policy.TaskRelocate, policy.TaskReplenish)

	runPickScript(t, w, keyed("k",
		typedAvailableMsg("p-a0", "T0", "PICK", "L0", "L0"),
		typedAvailableMsg("p-a1", "T-pick", "PICK", "A", "A"),
		typedAvailableMsg("p-a2", "T-relocate", "RELOCATE", "B", "B"),
		typedAvailableMsg("p-a3", "T-replenish", "REPLENISH", "C", "C"),
		claimedMsg("p-c0", "T0", "STN-1", "2026-10-17T08:59:30Z"),
		completedMsg("p-done", "T0", "STN-1"),
	))

	h := activeHintOf(t, w)
	want := []struct {
		id  string
		typ policy.TaskType
		d   float64
	}{
		{"T-pick", policy.TaskPick, 3},
		{"T-relocate", policy.TaskRelocate, 7},
		{"T-replenish", policy.TaskReplenish, 14},
	}
	if len(h.Candidates) != len(want) {
		t.Fatalf("candidates = %+v, want the three eligible tasks", h.Candidates)
	}
	for i, w := range want {
		c := h.Candidates[i]
		if c.Rank != i+1 || string(c.TaskID) != w.id || c.TaskType != w.typ || c.DetourM != w.d {
			t.Fatalf("candidate %d = %+v, want rank %d %s %s %.1f", i, c, i+1, w.id, w.typ, w.d)
		}
	}
	if h.Origin != "L0" {
		t.Fatalf("hint origin = %q, want L0 (the completed PICK task's to_location)", h.Origin)
	}
}

// TestFulfillmentConsumer_NoCandidateIsDroppedBelowTheMaxCandidatesBound: a
// site declaring the candidate bound's ceiling (5, which the pick-batching
// default batch maximum of 4 fits under) gets all five qualifying candidates
// in the issued hint — the SQL pool read, the geometry batch, the
// recommender's cut and the hint's own validation keep every candidate below
// the bound; a sixth, farther task is the only one cut.
func TestFulfillmentConsumer_NoCandidateIsDroppedBelowTheMaxCandidatesBound(t *testing.T) {
	w := newPGWorld(t)
	w.seedLayout(t)
	for i := 1; i <= 6; i++ {
		seedGeometry(t, w, string(rune('0'+i)), float64(i), 0) // i metres from L0
	}
	seedPolicyFor(t, w, 5, policy.TaskPick)

	script := keyed("k", typedAvailableMsg("b-a0", "T0", "PICK", "L0", "L0"))
	for i := 1; i <= 6; i++ {
		task, at := "T-"+string(rune('0'+i)), string(rune('0'+i))
		script = append(script, kafkatest.Message{Key: "k", Value: typedAvailableMsg("b-a"+at, task, "PICK", at, at)})
	}
	script = append(script,
		kafkatest.Message{Key: "k", Value: claimedMsg("b-c0", "T0", "STN-1", "2026-10-17T08:59:30Z")},
		kafkatest.Message{Key: "k", Value: completedMsg("b-done", "T0", "STN-1")},
	)

	runPickScript(t, w, script)

	h := activeHintOf(t, w)
	if len(h.Candidates) != 5 {
		t.Fatalf("hint holds %d candidates, want the full bound of 5: %+v", len(h.Candidates), h.Candidates)
	}
	for i, c := range h.Candidates {
		want := "T-" + string(rune('1'+i)) // the five nearest, 1..5 m
		if c.Rank != i+1 || string(c.TaskID) != want || c.DetourM != float64(i+1) {
			t.Fatalf("candidate %d = %+v, want rank %d %s at %d m", i, c, i+1, want, i+1)
		}
	}
}
