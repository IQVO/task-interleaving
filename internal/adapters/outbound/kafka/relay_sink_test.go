package kafka

import (
	"context"
	"errors"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/claudioed/task-interleaving/internal/application/outbox"
)

type fakeWriter struct {
	got []kafkago.Message
	err error
}

func (w *fakeWriter) WriteMessages(_ context.Context, msgs ...kafkago.Message) error {
	w.got = append(w.got, msgs...)
	return w.err
}

func TestRelaySink_WritesKeyValueHeadersAndTopic(t *testing.T) {
	w := &fakeWriter{}
	sink := NewRelaySinkWithWriter(w)
	err := sink.Send(context.Background(), outbox.Message{
		Topic: "t", Key: []byte("STN-1"), Value: []byte("v"), EventType: "x",
		Headers: []outbox.Header{{Key: "content-type", Value: "application/cloudevents+json; charset=UTF-8"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(w.got) != 1 {
		t.Fatalf("wrote %d", len(w.got))
	}
	m := w.got[0]
	if m.Topic != "t" || string(m.Key) != "STN-1" || string(m.Value) != "v" || len(m.Headers) != 1 ||
		m.Headers[0].Key != "content-type" || string(m.Headers[0].Value) != "application/cloudevents+json; charset=UTF-8" {
		t.Fatalf("message = %+v", m)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("close of a fake writer: %v", err)
	}
}

func TestRelaySink_RoutesEachMessageToItsOwnTopic(t *testing.T) {
	w := &fakeWriter{}
	err := NewRelaySinkWithWriter(w).Send(context.Background(),
		outbox.Message{Topic: "a.events"}, outbox.Message{Topic: "a.analytics"})
	if err != nil || len(w.got) != 2 || w.got[0].Topic != "a.events" || w.got[1].Topic != "a.analytics" {
		t.Fatalf("err = %v, wrote %+v", err, w.got)
	}
}

func TestRelaySink_Errors(t *testing.T) {
	w := &fakeWriter{}
	sink := NewRelaySinkWithWriter(w)
	if err := sink.Send(context.Background()); err != nil || len(w.got) != 0 {
		t.Fatalf("empty send: %v, wrote %d", err, len(w.got))
	}
	if err := sink.Send(context.Background(), outbox.Message{Topic: "t"}, outbox.Message{EventType: "x"}); err == nil || len(w.got) != 0 {
		t.Fatalf("a message without a topic must be rejected before any write, err = %v, wrote %d", err, len(w.got))
	}
	boom := errors.New("boom")
	w.err = boom
	if err := sink.Send(context.Background(), outbox.Message{Topic: "t"}); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestNewRelaySink_UsesTheScaffoldWriter(t *testing.T) {
	sink := NewRelaySink([]string{"127.0.0.1:1"})
	w, ok := sink.writer.(*kafkago.Writer)
	if !ok {
		t.Fatalf("writer = %T", sink.writer)
	}
	if w.Topic != "" || w.RequiredAcks != kafkago.RequireAll || w.BatchTimeout != 10*time.Millisecond {
		t.Fatalf("writer settings = %+v", w)
	}
	if _, ok := w.Balancer.(*kafkago.Hash); !ok {
		t.Fatalf("balancer = %T, want Hash (key = station id)", w.Balancer)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
}
