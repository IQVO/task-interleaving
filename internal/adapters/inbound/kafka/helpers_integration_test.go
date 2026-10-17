//go:build integration

package kafka_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	tckafka "github.com/testcontainers/testcontainers-go/modules/kafka"

	kafkaconsumer "github.com/claudioed/task-interleaving/internal/adapters/inbound/kafka"
	"github.com/claudioed/task-interleaving/internal/adapters/outbound/events"
	"github.com/claudioed/task-interleaving/internal/adapters/outbound/memory"
	"github.com/claudioed/task-interleaving/internal/adapters/outbound/postgres"
	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/application/usecases"
	"github.com/claudioed/task-interleaving/internal/domain/policy"
	"github.com/claudioed/task-interleaving/internal/testing/kafkatest"
	"github.com/claudioed/task-interleaving/internal/testing/pgtest"
)

// The broker itself comes from internal/testing/kafkatest, which starts it
// through testcontainers-go/modules/kafka (referenced here so the module import
// stays reachable from the Kafka-touching test files).
var _ = tckafka.Run

// The infrastructure comes from testcontainers, never from the environment:
// internal/testing/pgtest boots ONE Postgres and internal/testing/kafkatest ONE
// Kafka per test binary (each lazily, on first use); TestMain only tears them
// down at the end.
func TestMain(m *testing.M) {
	code := m.Run()
	kafkatest.Shutdown()
	pgtest.Shutdown()
	os.Exit(code)
}

// pgWorld is the production Postgres adapters over a fresh, migrated database
// of the shared testcontainers Postgres.
type pgWorld struct {
	policies  *postgres.PolicyRepo
	trips     *postgres.StationTripRepo
	pool      *postgres.TaskPool
	book      *postgres.LocationBook
	events    *events.Sink
	uow       *postgres.UnitOfWork
	processed *postgres.ProcessedEventRepo
	// count returns the number of rows of a table of the test's database; the
	// argument may carry a WHERE clause.
	count func(table string) int
}

func newPGWorld(t *testing.T) *pgWorld {
	t.Helper()
	pool := pgtest.NewPool(t)
	return &pgWorld{
		policies: postgres.NewPolicyRepo(pool), trips: postgres.NewStationTripRepo(pool),
		pool: postgres.NewTaskPool(pool), book: postgres.NewLocationBook(pool),
		events: events.NewSink(postgres.NewOutboxRepo(pool)), uow: postgres.NewUnitOfWork(pool),
		processed: postgres.NewProcessedEventRepo(pool),
		count:     func(table string) int { return pgtest.Count(t, pool, table) },
	}
}

func (w *pgWorld) intake() usecases.Intake {
	return usecases.Intake{UoW: w.uow, Processed: w.processed}
}

func (w *pgWorld) fulfillmentUseCases() kafkaconsumer.FulfillmentUseCases {
	writer := usecases.TripWriter{Trips: w.trips, Events: w.events}
	return kafkaconsumer.FulfillmentUseCases{
		Available: &usecases.ApplyTaskAvailable{Intake: w.intake(), Pool: w.pool},
		Claimed:   &usecases.ApplyTaskClaimed{Intake: w.intake(), TripWriter: writer, Pool: w.pool, Clock: memory.SystemClock{}},
		Completed: &usecases.ApplyTaskCompleted{
			Intake: w.intake(), TripWriter: writer, Policies: w.policies, Pool: w.pool,
			Ranker: usecases.Ranker{Pool: w.pool, Book: w.book}, Clock: memory.SystemClock{}, IDs: memory.RandomIDs{},
		},
	}
}

func (w *pgWorld) layoutUseCases() kafkaconsumer.LayoutUseCases {
	return kafkaconsumer.LayoutUseCases{
		Zone:           &usecases.ApplyZone{Intake: w.intake(), Book: w.book},
		Registered:     &usecases.ApplyLocationRegistered{Intake: w.intake(), Book: w.book},
		Decommissioned: &usecases.ApplyLocationDecommissioned{Intake: w.intake(), Book: w.book},
		Geometry:       &usecases.ApplyGeometryUpdated{Intake: w.intake(), Book: w.book},
	}
}

// seedLayout stores site S1's layout copy: L0 (0,0,0), A (3,0,0) and B (3,4,0).
func (w *pgWorld) seedLayout(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	steps := []error{w.book.SaveZone(ctx, repository.Zone{ID: "z1", SiteCode: "S1", ZoneCode: "FWD"})}
	for code, p := range map[string][2]float64{"L0": {0, 0}, "A": {3, 0}, "B": {3, 4}} {
		steps = append(steps, w.book.SaveLocation(ctx, code, "z1"), w.book.SaveGeometry(ctx, code, p[0], p[1], 0))
	}
	for _, err := range steps {
		if err != nil {
			t.Fatal(err)
		}
	}
}

// seedPolicy declares S1's interleaving policy (v1): 25 m detour, three
// candidates, 90 s hints, PICK tasks.
func (w *pgWorld) seedPolicy(t *testing.T) {
	t.Helper()
	p, err := policy.New("S1", policy.Settings{
		MaxDetourM: 25, MaxCandidates: 3, HintTTL: 90 * time.Second,
		EligibleTaskTypes: []policy.TaskType{policy.TaskPick}, VerticalWeight: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.policies.Save(context.Background(), repository.PolicyRecord{Policy: p, DeclaredAt: time.Now().UTC()}, 0); err != nil {
		t.Fatal(err)
	}
}

func processedRows(w *pgWorld) int { return w.count("processed_events") }

// testWriter sends the consumer's log lines to the test output, so a failed
// run shows what the consumer did.
type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(string(p))
	return len(p), nil
}

func itLogger(t *testing.T) *slog.Logger { return slog.New(slog.NewTextHandler(testWriter{t}, nil)) }

// newTopic creates the consumed topic and its dead-letter topic explicitly and
// waits for both leaders.
func newTopic(t *testing.T, base string) (topic, dlq string) {
	t.Helper()
	topic = kafkatest.Topic(t, base)
	dlq = topic + kafkaconsumer.DLQSuffix
	kafkatest.CreateTopic(t, dlq)
	return topic, dlq
}

// groupFor is the consumer group id of an integration test: unique per topic,
// like the environment variable a deployment sets.
func groupFor(name, topic string) string { return "itest-" + name + "-" + topic }

// runConsumer starts c and stops it (and waits, bounded, for it) when the test
// ends.
func runConsumer(t *testing.T, c *kafkaconsumer.Consumer) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = c.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Error("the consumer did not stop within 30s of its context being cancelled")
		}
		_ = c.Close()
	})
}

// eventually polls cond for at most 90 seconds.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// assertCommittedPast waits until the group committed `want` offsets of topic:
// every message, the skipped ones (legacy flat, unknown type) and the
// dead-lettered one included, was committed past.
func assertCommittedPast(t *testing.T, group, topic string, want int64) {
	t.Helper()
	eventually(t, fmt.Sprintf("%d committed offsets of %s", want, topic), func() bool {
		return kafkatest.CommittedOffset(t, group, topic) == want
	})
}

// assertOnlyDeadLettered checks that the dead-letter topic holds exactly the
// given poison messages, untouched, with their source recorded, and nothing
// else. The read is bounded: it ends once nothing new arrives for 5 seconds.
func assertOnlyDeadLettered(t *testing.T, topic, dlq string, poison ...[]byte) {
	t.Helper()
	got := kafkatest.ReadAll(t, dlq, 5*time.Second)
	if len(got) != len(poison) {
		t.Fatalf("the dead-letter topic holds %d messages, want exactly the %d poison one(s)", len(got), len(poison))
	}
	for i, want := range poison {
		if string(got[i].Value) != string(want) || got[i].Headers["x-dlq-source-topic"] != topic {
			t.Fatalf("dead letter %d = %s %v, want the poison message %s from %s", i, got[i].Value, got[i].Headers, want, topic)
		}
	}
}

func keyed(key string, values ...[]byte) []kafkatest.Message {
	out := make([]kafkatest.Message, len(values))
	for i, v := range values {
		out[i] = kafkatest.Message{Key: key, Value: v}
	}
	return out
}
