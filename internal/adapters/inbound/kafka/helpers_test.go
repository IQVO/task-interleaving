package kafka_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	kafkaconsumer "github.com/claudioed/task-interleaving/internal/adapters/inbound/kafka"
	"github.com/claudioed/task-interleaving/internal/adapters/outbound/memory"
	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/application/usecases"
	"github.com/claudioed/task-interleaving/internal/domain/policy"
)

var (
	bg      = context.Background()
	t0      = time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	errBoom = errors.New("injected transient database failure")
	// fastRetry keeps Run-loop tests quick while still exercising backoff.
	fastRetry = kafkaconsumer.RetryPolicy{Initial: time.Millisecond, Max: 4 * time.Millisecond}
)

// fixedClock stands at t0, the time of every fixture CloudEvent.
type fixedClock struct{}

func (fixedClock) Now() time.Time { return t0 }

// seqIDs mints the hint ids hint-1, hint-2...
type seqIDs struct{ n int }

func (s *seqIDs) NewHintID() string {
	s.n++
	return fmt.Sprintf("hint-%d", s.n)
}

// logBuffer is a goroutine-safe slog sink the tests read back.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// flakyPool wraps the in-memory task pool: Save fails the first `fail` calls
// (before changing anything) and runs `onSave` on every call.
type flakyPool struct {
	*memory.TaskPool
	mu     sync.Mutex
	fail   int
	calls  int
	onSave func()
}

func (f *flakyPool) Save(ctx context.Context, t repository.Task) error {
	f.mu.Lock()
	f.calls++
	failing := f.fail > 0
	if failing {
		f.fail--
	}
	hook := f.onSave
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	if failing {
		return errBoom
	}
	return f.TaskPool.Save(ctx, t)
}

func (f *flakyPool) saveCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// countingUoW counts how often a unit of work was opened, proving a
// deterministic bad message never even touches the database.
type countingUoW struct {
	inner *memory.UnitOfWork
	mu    sync.Mutex
	calls int
}

func (u *countingUoW) Do(ctx context.Context, fn func(context.Context) error) error {
	u.mu.Lock()
	u.calls++
	u.mu.Unlock()
	return u.inner.Do(ctx, fn)
}

func (u *countingUoW) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.calls
}

// world is the whole read side and write side the two consumers act on, over
// the in-memory adapters.
type world struct {
	policies  *memory.PolicyRepo
	trips     *memory.StationTripRepo
	pool      *flakyPool
	book      *memory.LocationBook
	log       *memory.EventLog
	processed *memory.ProcessedEventRepo
	uow       *countingUoW
	logs      *logBuffer
}

func newWorld() *world {
	policies, trips := memory.NewPolicyRepo(), memory.NewStationTripRepo()
	pool, book := memory.NewTaskPool(), memory.NewLocationBook()
	log, processed := memory.NewEventLog(), memory.NewProcessedEventRepo()
	return &world{
		policies: policies, trips: trips, pool: &flakyPool{TaskPool: pool}, book: book, log: log, processed: processed,
		uow:  &countingUoW{inner: memory.NewUnitOfWork(policies, trips, pool, book, log, processed)},
		logs: &logBuffer{},
	}
}

func (w *world) logger() *slog.Logger { return slog.New(slog.NewTextHandler(w.logs, nil)) }

func (w *world) intake() usecases.Intake {
	return usecases.Intake{UoW: w.uow, Processed: w.processed}
}

func (w *world) fulfillmentUseCases() kafkaconsumer.FulfillmentUseCases {
	writer := usecases.TripWriter{Trips: w.trips, Events: w.log}
	return kafkaconsumer.FulfillmentUseCases{
		Available: &usecases.ApplyTaskAvailable{Intake: w.intake(), Pool: w.pool},
		Claimed:   &usecases.ApplyTaskClaimed{Intake: w.intake(), TripWriter: writer, Pool: w.pool, Clock: fixedClock{}},
		Completed: &usecases.ApplyTaskCompleted{
			Intake: w.intake(), TripWriter: writer, Policies: w.policies, Pool: w.pool,
			Ranker: usecases.Ranker{Pool: w.pool, Book: w.book}, Clock: fixedClock{}, IDs: &seqIDs{},
		},
	}
}

func (w *world) layoutUseCases() kafkaconsumer.LayoutUseCases {
	return kafkaconsumer.LayoutUseCases{
		Zone:           &usecases.ApplyZone{Intake: w.intake(), Book: w.book},
		Registered:     &usecases.ApplyLocationRegistered{Intake: w.intake(), Book: w.book},
		Decommissioned: &usecases.ApplyLocationDecommissioned{Intake: w.intake(), Book: w.book},
		Geometry:       &usecases.ApplyGeometryUpdated{Intake: w.intake(), Book: w.book},
	}
}

func (w *world) fulfillmentConsumer() *kafkaconsumer.Consumer {
	return &kafkaconsumer.Consumer{
		Name: "fulfillment", Topic: kafkaconsumer.FulfillmentTopic,
		Handlers: kafkaconsumer.FulfillmentHandlers(w.fulfillmentUseCases(), w.logger()), Logger: w.logger(), Retry: fastRetry,
	}
}

func (w *world) layoutConsumer() *kafkaconsumer.Consumer {
	return &kafkaconsumer.Consumer{
		Name: "layout", Topic: kafkaconsumer.LayoutTopic,
		Handlers: kafkaconsumer.LayoutHandlers(w.layoutUseCases(), w.logger()), Logger: w.logger(), Retry: fastRetry,
	}
}

// seedLayout gives site S1 the locations L0 (0,0,0), A (3,0,0) and B (3,4,0).
func (w *world) seedLayout(t *testing.T) {
	t.Helper()
	steps := []error{w.book.SaveZone(bg, repository.Zone{ID: "z1", SiteCode: "S1", ZoneCode: "FWD"})}
	for code, x := range map[string]float64{"L0": 0, "A": 3, "B": 3} {
		y := 0.0
		if code == "B" {
			y = 4
		}
		steps = append(steps, w.book.SaveLocation(bg, code, "z1"), w.book.SaveGeometry(bg, code, x, y, 0))
	}
	for _, err := range steps {
		if err != nil {
			t.Fatal(err)
		}
	}
}

// seedPolicy declares the interleaving policy of S1 (v1): 25 m detour, three
// candidates, 90 s hints, PICK tasks.
func (w *world) seedPolicy(t *testing.T) {
	t.Helper()
	p, err := policy.New("S1", policy.Settings{
		MaxDetourM: 25, MaxCandidates: 3, HintTTL: 90 * time.Second,
		EligibleTaskTypes: []policy.TaskType{policy.TaskPick}, VerticalWeight: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.policies.Save(bg, repository.PolicyRecord{Policy: p, DeclaredAt: t0}, 0); err != nil {
		t.Fatal(err)
	}
}

// assertConstructed checks what the New*Consumer constructors promise without
// dialling a broker: the topic, one handler per consumed type, a dead-letter
// writer, DeadLetterInvalid, and a Close that works on a never-started reader.
func assertConstructed(t *testing.T, c *kafkaconsumer.Consumer, topic string, types ...string) {
	t.Helper()
	if c.Topic != topic || c.Reader == nil || c.DLQ == nil || !c.DeadLetterInvalid {
		t.Fatalf("consumer = %+v, want topic %s, a reader, a DLQ writer and DeadLetterInvalid", c, topic)
	}
	if len(c.Handlers) != len(types) {
		t.Fatalf("%d handlers, want %d", len(c.Handlers), len(types))
	}
	for _, typ := range types {
		if c.Handlers[typ] == nil {
			t.Errorf("no handler for %s", typ)
		}
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close = %v", err)
	}
}

// fakeReader is a scripted kafkaconsumer.Reader. It records every
// FetchMessage/CommitMessages in order so a test can assert that the offset was
// committed only after the handler succeeded, and exactly once. After the
// scripted messages it blocks until ctx is cancelled, like a real reader on an
// idle topic.
type fakeReader struct {
	mu     sync.Mutex
	msgs   []kafkago.Message
	next   int
	events []string
}

func newFakeReader(values ...[]byte) *fakeReader {
	r := &fakeReader{}
	for i, v := range values {
		r.msgs = append(r.msgs, kafkago.Message{Partition: 0, Offset: int64(i), Value: v, Topic: "t", Key: []byte("k")})
	}
	return r
}

func (r *fakeReader) FetchMessage(ctx context.Context) (kafkago.Message, error) {
	r.mu.Lock()
	if r.next < len(r.msgs) {
		m := r.msgs[r.next]
		r.next++
		r.events = append(r.events, fmt.Sprintf("fetch:%d", m.Offset))
		r.mu.Unlock()
		return m, nil
	}
	r.mu.Unlock()
	<-ctx.Done()
	return kafkago.Message{}, ctx.Err()
}

func (r *fakeReader) CommitMessages(_ context.Context, msgs ...kafkago.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, m := range msgs {
		r.events = append(r.events, fmt.Sprintf("commit:%d", m.Offset))
	}
	return nil
}

func (r *fakeReader) Close() error { return nil }

func (r *fakeReader) log() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

func (r *fakeReader) commits() int {
	n := 0
	for _, e := range r.log() {
		if strings.HasPrefix(e, "commit:") {
			n++
		}
	}
	return n
}

// fakeDLQWriter records every message WriteMessages publishes.
type fakeDLQWriter struct {
	mu   sync.Mutex
	msgs []kafkago.Message
}

func (w *fakeDLQWriter) WriteMessages(_ context.Context, msgs ...kafkago.Message) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.msgs = append(w.msgs, msgs...)
	return nil
}

func (w *fakeDLQWriter) Close() error { return nil }

func (w *fakeDLQWriter) published() []kafkago.Message {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]kafkago.Message(nil), w.msgs...)
}

// runUntilCommits runs c.Run over reader until `want` commits happened (or
// fails after 10s), then cancels and returns Run's error.
func runUntilCommits(t *testing.T, c *kafkaconsumer.Consumer, reader *fakeReader, want int) error {
	t.Helper()
	c.Reader = reader
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	deadline := time.After(10 * time.Second)
	for reader.commits() < want {
		select {
		case err := <-done:
			return err
		case <-deadline:
			t.Fatalf("timed out waiting for %d commits; events=%v", want, reader.log())
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
		return nil
	}
}

// handle delivers one raw message to c, failing the test on a returned error.
func handle(t *testing.T, c *kafkaconsumer.Consumer, value []byte) {
	t.Helper()
	if err := c.HandleMessage(bg, value); err != nil {
		t.Fatalf("HandleMessage = %v, want nil", err)
	}
}
