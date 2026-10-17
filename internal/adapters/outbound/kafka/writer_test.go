package kafka

import (
	"testing"

	kafkago "github.com/segmentio/kafka-go"
)

func TestNewWriterIsSynchronousAcksAllAndTopicFree(t *testing.T) {
	w := NewWriter([]string{"broker-a:9092", "broker-b:9092"})
	if w.RequiredAcks != kafkago.RequireAll {
		t.Fatalf("RequiredAcks = %v, want RequireAll", w.RequiredAcks)
	}
	if w.Async {
		t.Fatal("writer must be synchronous so a failed write reaches the relay")
	}
	if w.Topic != "" {
		t.Fatalf("writer Topic = %q, want empty (messages carry their own topic)", w.Topic)
	}
}
