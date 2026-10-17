package kafka

import (
	"context"
	"errors"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"
)

// TestHandleWithDeadLetter_ExhaustsAttemptsThenDeadLetters: a transient
// failure is retried EXACTLY maxAttempts times (not forever), then
// dead-lettered once, and the loop moves on instead of blocking the partition.
func TestHandleWithDeadLetter_ExhaustsAttemptsThenDeadLetters(t *testing.T) {
	var handleCalls int
	var dlqCalls []error
	injected := errors.New("transient")

	l := &consumeLoop{
		logger: quietLogger(),
		name:   "test",
		handle: func(context.Context, kafkago.Message) error {
			handleCalls++
			return injected
		},
		sleep:       func(context.Context, time.Duration) error { return nil },
		maxAttempts: 3,
		deadLetter: func(_ context.Context, _ kafkago.Message, cause error) error {
			dlqCalls = append(dlqCalls, cause)
			return nil
		},
	}
	if err := l.handleWithDeadLetter(context.Background(), RetryPolicy{}.withDefaults(), l.sleep, kafkago.Message{Offset: 7}); err != nil {
		t.Fatalf("handleWithDeadLetter = %v, want nil (dead-lettered, loop moves on)", err)
	}
	if handleCalls != 3 {
		t.Fatalf("handle called %d times, want exactly maxAttempts=3", handleCalls)
	}
	if len(dlqCalls) != 1 || !errors.Is(dlqCalls[0], injected) {
		t.Fatalf("dead-letter calls = %v, want exactly one carrying the injected error", dlqCalls)
	}
}

// A poison message is dead-lettered at once, without retrying.
func TestHandleWithDeadLetter_PoisonIsDeadLetteredWithoutRetry(t *testing.T) {
	handleCalls, dlqCalls := 0, 0
	l := &consumeLoop{
		logger: quietLogger(),
		name:   "test",
		handle: func(context.Context, kafkago.Message) error {
			handleCalls++
			return errPoison
		},
		sleep:       func(context.Context, time.Duration) error { return nil },
		maxAttempts: 5,
		deadLetter:  func(context.Context, kafkago.Message, error) error { dlqCalls++; return nil },
	}
	if err := l.handleWithDeadLetter(context.Background(), RetryPolicy{}.withDefaults(), l.sleep, kafkago.Message{}); err != nil {
		t.Fatalf("handleWithDeadLetter = %v", err)
	}
	if handleCalls != 1 || dlqCalls != 1 {
		t.Fatalf("handle %d, dead-letter %d: want 1 and 1", handleCalls, dlqCalls)
	}
}

// A handler that succeeds before exhausting maxAttempts never reaches the
// dead-letter path.
func TestHandleWithDeadLetter_SucceedsBeforeExhaustion_NeverDeadLetters(t *testing.T) {
	attempt := 0
	dlqCalled := false
	l := &consumeLoop{
		logger: quietLogger(),
		name:   "test",
		handle: func(context.Context, kafkago.Message) error {
			attempt++
			if attempt < 3 {
				return errors.New("transient")
			}
			return nil
		},
		sleep:       func(context.Context, time.Duration) error { return nil },
		maxAttempts: 5,
		deadLetter:  func(context.Context, kafkago.Message, error) error { dlqCalled = true; return nil },
	}
	if err := l.handleWithDeadLetter(context.Background(), RetryPolicy{}.withDefaults(), l.sleep, kafkago.Message{}); err != nil {
		t.Fatalf("handleWithDeadLetter = %v, want nil", err)
	}
	if attempt != 3 {
		t.Fatalf("handle called %d times, want 3 (fail, fail, succeed)", attempt)
	}
	if dlqCalled {
		t.Fatal("dead-letter must not be called when handle eventually succeeds")
	}
}

// A dead-letter PUBLISH failure is itself retried (never silently dropping the
// message); once it succeeds, the loop returns nil.
func TestHandleWithDeadLetter_PublishFailureIsRetried(t *testing.T) {
	dlqAttempts := 0
	l := &consumeLoop{
		logger:      quietLogger(),
		name:        "test",
		handle:      func(context.Context, kafkago.Message) error { return errors.New("transient") },
		sleep:       func(context.Context, time.Duration) error { return nil },
		maxAttempts: 1,
		deadLetter: func(context.Context, kafkago.Message, error) error {
			dlqAttempts++
			if dlqAttempts < 3 {
				return errors.New("dlq broker blip")
			}
			return nil
		},
	}
	policy := RetryPolicy{Initial: time.Millisecond, Max: time.Millisecond}
	if err := l.handleWithDeadLetter(context.Background(), policy, l.sleep, kafkago.Message{}); err != nil {
		t.Fatalf("handleWithDeadLetter = %v, want nil once the DLQ publish finally succeeds", err)
	}
	if dlqAttempts != 3 {
		t.Fatalf("dead-letter publish attempted %d times, want 3 (fail, fail, succeed)", dlqAttempts)
	}
}

// Cancelling mid-retry (before maxAttempts is reached) must stop without
// dead-lettering: the offset stays uncommitted so the message is redelivered on
// restart.
func TestHandleWithDeadLetter_CancelledMidRetry_NeverDeadLetters(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	dlqCalled := false
	l := &consumeLoop{
		logger: quietLogger(),
		name:   "test",
		handle: func(context.Context, kafkago.Message) error {
			cancel()
			return errors.New("transient")
		},
		sleep:       func(context.Context, time.Duration) error { return nil },
		maxAttempts: 5,
		deadLetter:  func(context.Context, kafkago.Message, error) error { dlqCalled = true; return nil },
	}
	err := l.handleWithDeadLetter(ctx, RetryPolicy{}.withDefaults(), l.sleep, kafkago.Message{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("handleWithDeadLetter = %v, want context.Canceled", err)
	}
	if dlqCalled {
		t.Fatal("a cancelled shutdown must never dead-letter")
	}
}

// A poison failure that arrives together with a cancelled context is not
// dead-lettered either.
func TestHandleWithDeadLetter_PoisonDuringShutdown_NeverDeadLetters(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	dlqCalled := false
	l := &consumeLoop{
		logger: quietLogger(),
		name:   "test",
		handle: func(context.Context, kafkago.Message) error {
			cancel()
			return errPoison
		},
		sleep:       func(context.Context, time.Duration) error { return nil },
		maxAttempts: 5,
		deadLetter:  func(context.Context, kafkago.Message, error) error { dlqCalled = true; return nil },
	}
	if err := l.handleWithDeadLetter(ctx, RetryPolicy{}.withDefaults(), l.sleep, kafkago.Message{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("handleWithDeadLetter = %v, want context.Canceled", err)
	}
	if dlqCalled {
		t.Fatal("a cancelled shutdown must never dead-letter")
	}
}

// With no maxAttempts/deadLetter configured, handle is retried forever on a
// transient failure.
func TestHandleWithDeadLetter_ZeroValueRetriesUnbounded(t *testing.T) {
	calls := 0
	ctx, cancel := context.WithCancel(context.Background())
	l := &consumeLoop{
		logger: quietLogger(),
		name:   "test",
		handle: func(context.Context, kafkago.Message) error {
			calls++
			if calls == 10 {
				cancel()
			}
			return errors.New("transient")
		},
		sleep: func(context.Context, time.Duration) error { return nil },
	}
	err := l.handleWithDeadLetter(ctx, RetryPolicy{}.withDefaults(), l.sleep, kafkago.Message{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("handleWithDeadLetter = %v, want context.Canceled", err)
	}
	if calls != 10 {
		t.Fatalf("handle called %d times, want 10 (no bound without maxAttempts/deadLetter)", calls)
	}
}

// TestDLQWriter_TopicComesFromTheMessage pins the writer shape a real broker
// accepts: publishDeadLetter sets the message's Topic, so the writer must not
// set one too (kafka-go rejects both), and the write stays synchronous with
// RequireAll acks.
func TestDLQWriter_TopicComesFromTheMessage(t *testing.T) {
	w, ok := newDLQWriter([]string{"127.0.0.1:1"}).(*kafkago.Writer)
	if !ok {
		t.Fatal("the dead-letter writer must be a *kafkago.Writer")
	}
	defer func() { _ = w.Close() }()
	if w.Topic != "" {
		t.Fatalf("writer Topic = %q, want none: the message names the DLQ topic", w.Topic)
	}
	if w.RequiredAcks != kafkago.RequireAll || w.Async || !w.AllowAutoTopicCreation {
		t.Fatalf("writer = acks %v async %v autocreate %v", w.RequiredAcks, w.Async, w.AllowAutoTopicCreation)
	}
}

type fakeDLQ struct {
	failures int
	calls    []kafkago.Message
	err      error
}

func (f *fakeDLQ) WriteMessages(_ context.Context, msgs ...kafkago.Message) error {
	f.calls = append(f.calls, msgs...)
	if f.failures > 0 {
		f.failures--
		return f.err
	}
	return nil
}

func (f *fakeDLQ) Close() error { return nil }

// publishDeadLetter keeps the original key, value and headers and names the
// source and the error in x-dlq-* headers.
func TestPublishDeadLetter_AddsSourceAndErrorHeaders(t *testing.T) {
	w := &fakeDLQ{}
	msg := kafkago.Message{
		Topic: "warehouse.fulfillment.events", Partition: 2, Offset: 41, Key: []byte("k"), Value: []byte("v"),
		Headers: []kafkago.Header{{Key: "content-type", Value: []byte("x")}},
	}
	if err := publishDeadLetter(context.Background(), w, "warehouse.fulfillment.events.dlq", msg, errors.New("boom")); err != nil {
		t.Fatal(err)
	}
	if len(w.calls) != 1 {
		t.Fatalf("writes = %d, want 1", len(w.calls))
	}
	got := w.calls[0]
	if got.Topic != "warehouse.fulfillment.events.dlq" || string(got.Key) != "k" || string(got.Value) != "v" {
		t.Fatalf("message = %+v", got)
	}
	headers := map[string]string{}
	for _, h := range got.Headers {
		headers[h.Key] = string(h.Value)
	}
	for k, want := range map[string]string{
		"content-type": "x", "x-dlq-source-topic": "warehouse.fulfillment.events", "x-dlq-source-partition": "2",
		"x-dlq-source-offset": "41", "x-dlq-error": "boom",
	} {
		if headers[k] != want {
			t.Errorf("header %s = %q, want %q", k, headers[k], want)
		}
	}
	if headers["x-dlq-failed-at"] == "" {
		t.Error("x-dlq-failed-at is missing")
	}
}

// writeDLQ retries only while the auto-created topic is not ready.
func TestWriteDLQ_RetriesOnlyWhileTheTopicIsNotReady(t *testing.T) {
	notReady := &fakeDLQ{failures: 1, err: kafkago.LeaderNotAvailable}
	if err := writeDLQ(context.Background(), notReady, kafkago.Message{}); err != nil || len(notReady.calls) != 2 {
		t.Fatalf("not-ready: err=%v calls=%d, want nil and 2", err, len(notReady.calls))
	}
	hard := &fakeDLQ{failures: 1, err: errors.New("broker rejected")}
	if err := writeDLQ(context.Background(), hard, kafkago.Message{}); err == nil || len(hard.calls) != 1 {
		t.Fatalf("hard failure: err=%v calls=%d, want an error after 1 call", err, len(hard.calls))
	}
}

func TestWriteDLQ_StopsWhenTheContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := &fakeDLQ{failures: 100, err: kafkago.UnknownTopicOrPartition}
	if err := writeDLQ(ctx, w, kafkago.Message{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("writeDLQ = %v, want context.Canceled", err)
	}
}

func TestIsTopicNotReady(t *testing.T) {
	if !isTopicNotReady(kafkago.WriteErrors{kafkago.UnknownTopicOrPartition, kafkago.LeaderNotAvailable}) {
		t.Error("write errors that are all not-ready must count as not ready")
	}
	if isTopicNotReady(kafkago.WriteErrors{kafkago.UnknownTopicOrPartition, errors.New("other")}) {
		t.Error("a hard error among the write errors must not count as not ready")
	}
	if isTopicNotReady(kafkago.WriteErrors{nil, nil}) {
		t.Error("write errors without any error must not count as not ready")
	}
	if isTopicNotReady(errors.New("other")) {
		t.Error("an unrelated error is not not-ready")
	}
}
