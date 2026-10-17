//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/claudioed/task-interleaving/internal/adapters/outbound/postgres"
	"github.com/claudioed/task-interleaving/internal/application/outbox"
	"github.com/claudioed/task-interleaving/internal/testing/pgtest"
)

func message(id string) outbox.Message {
	return outbox.Message{
		EventID: id, Topic: "t", EventType: "type-" + id, Subject: "STN-1", Key: []byte("STN-1"), DataSchema: "urn:d", Value: []byte("{}"),
		Headers: []outbox.Header{{Key: "content-type", Value: "application/cloudevents+json; charset=UTF-8"}},
	}
}

func acceptAll(context.Context, outbox.Message) error { return nil }

func TestOutboxDrainSendsInOrderAndKeepsEveryField(t *testing.T) {
	ctx := context.Background()
	ob := postgres.NewOutboxRepo(pgtest.NewPool(t))
	if err := ob.Insert(ctx, message("a"), message("b"), message("c")); err != nil {
		t.Fatal(err)
	}
	var sent []string
	n, err := ob.Drain(ctx, 2, 0, func(_ context.Context, m outbox.Message) error {
		sent = append(sent, m.EventType)
		if len(m.Headers) != 1 || m.Headers[0].Key != "content-type" || string(m.Key) != "STN-1" || m.Subject != "STN-1" || m.DataSchema != "urn:d" {
			t.Errorf("message lost data: %+v", m)
		}
		return nil
	})
	if err != nil || n != 2 || len(sent) != 2 || sent[0] != "type-a" || sent[1] != "type-b" {
		t.Fatalf("drain: n=%d err=%v sent=%v", n, err, sent)
	}
	if n, err = ob.Drain(ctx, 10, 0, acceptAll); err != nil || n != 1 {
		t.Fatalf("the rest drains next: n=%d err=%v", n, err)
	}
}

func TestOutboxFailedRowIsRetriedAndIdentityIsUnique(t *testing.T) {
	ctx := context.Background()
	ob := postgres.NewOutboxRepo(pgtest.NewPool(t))
	if err := ob.Insert(ctx, message("a")); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("broker down")
	n, err := ob.Drain(ctx, 10, 0, func(context.Context, outbox.Message) error { return boom })
	var failure *outbox.SendFailure
	if !errors.Is(err, boom) || !errors.As(err, &failure) || failure.Attempts != 1 || n != 0 {
		t.Fatalf("failure: n=%d err=%v", n, err)
	}
	if n, err = ob.Drain(ctx, 10, 0, acceptAll); err != nil || n != 1 {
		t.Fatalf("the failed row is retried: n=%d err=%v", n, err)
	}
	if err := ob.Insert(ctx, message("a")); err == nil {
		t.Fatal("a second row with the same event id and topic must violate the unique constraint")
	}
}

func TestOutboxAStoppedPassKeepsWhatWasAlreadySent(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.NewPool(t)
	ob := postgres.NewOutboxRepo(pool)
	if err := ob.Insert(ctx, message("a"), message("b"), message("c")); err != nil {
		t.Fatal(err)
	}
	calls := 0
	n, err := ob.Drain(ctx, 10, 0, func(context.Context, outbox.Message) error {
		calls++
		if calls == 2 {
			return errors.New("second one fails")
		}
		return nil
	})
	if err == nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if published := pgtest.Count(t, pool, "outbox_events WHERE published_at IS NOT NULL"); published != 1 {
		t.Fatalf("%d rows published, want the first one only", published)
	}
	if calls != 2 {
		t.Fatalf("the pass must stop at the first failure, send was called %d times", calls)
	}
}

func TestOutboxAttemptCapExcludesTheRowButKeepsItForInspection(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.NewPool(t)
	ob := postgres.NewOutboxRepo(pool)
	if err := ob.Insert(ctx, message("poison"), message("fine")); err != nil {
		t.Fatal(err)
	}
	rejectPoison := func(_ context.Context, m outbox.Message) error {
		if m.EventID == "poison" {
			return errors.New("rejected")
		}
		return nil
	}
	const maxAttempts = 2
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		_, err := ob.Drain(ctx, 10, maxAttempts, rejectPoison)
		var failure *outbox.SendFailure
		if !errors.As(err, &failure) || failure.Attempts != attempt {
			t.Fatalf("attempt %d: err=%v", attempt, err)
		}
	}
	// The poison row reached the cap: it is no longer claimed, so the row behind it flows.
	n, err := ob.Drain(ctx, 10, maxAttempts, rejectPoison)
	if err != nil || n != 1 {
		t.Fatalf("after the cap: n=%d err=%v", n, err)
	}
	var attempts int
	var lastErr string
	err = pool.QueryRow(ctx, `SELECT attempts, coalesce(last_error, '') FROM outbox_events WHERE event_id = 'poison' AND published_at IS NULL`).Scan(&attempts, &lastErr)
	if err != nil || attempts != maxAttempts || lastErr != "rejected" {
		t.Fatalf("poison row: attempts=%d last_error=%q err=%v", attempts, lastErr, err)
	}
	// 0 means unlimited: the same row is claimed again.
	if _, err := ob.Drain(ctx, 10, 0, rejectPoison); err == nil {
		t.Fatal("with no cap the poison row is claimed again")
	}
}

func TestOutboxInsertCarriesTheTraceContext(t *testing.T) {
	prev := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTextMapPropagator(prev) })
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		SpanID:     trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8},
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)
	ob := postgres.NewOutboxRepo(pgtest.NewPool(t))
	if err := ob.Insert(ctx, message("a")); err != nil {
		t.Fatal(err)
	}
	var got outbox.Message
	if _, err := ob.Drain(context.Background(), 10, 0, func(_ context.Context, m outbox.Message) error { got = m; return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got.Headers) != 2 || got.Headers[0].Key != "content-type" || got.Headers[1].Key != "traceparent" ||
		!strings.HasPrefix(got.Headers[1].Value, "00-0102030405060708090a0b0c0d0e0f10-0102030405060708-01") {
		t.Fatalf("headers = %+v", got.Headers)
	}
}

func TestOutboxInsertOfNothingIsANoOp(t *testing.T) {
	pool := pgtest.NewPool(t)
	if err := postgres.NewOutboxRepo(pool).Insert(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := pgtest.Count(t, pool, "outbox_events"); n != 0 {
		t.Fatalf("rows = %d", n)
	}
}
