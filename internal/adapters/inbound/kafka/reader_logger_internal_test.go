package kafka

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// kafka-go retries a missing topic or a failing group join silently unless it
// has an ErrorLogger: the consumers wire one that logs at ERROR.
func TestReaderErrorLoggerSurfacesKafkaGoRetriesAsErrorLogs(t *testing.T) {
	var out bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&out, nil))
	readerErrorLogger(logger, "fulfillment consumer", "warehouse.fulfillment.events")("failed to join group %s: %v", "g", "unknown topic")
	got := out.String()
	for _, want := range []string{"level=ERROR", "kafka reader error; retrying", `consumer="fulfillment consumer"`, "topic=warehouse.fulfillment.events", "failed to join group g: unknown topic"} {
		if !strings.Contains(got, want) {
			t.Errorf("log %q lacks %q", got, want)
		}
	}
}
