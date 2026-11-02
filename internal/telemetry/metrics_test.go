package telemetry

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// TestInstrumentsReachPrometheus wires the exporter exactly the way cmd/api
// does and checks that every instrument of the package -- built at var-init
// from the global meter, BEFORE the provider is set -- still shows up in the
// exposition format once each recording function has run.
func TestInstrumentsReachPrometheus(t *testing.T) {
	handler, err := PrometheusMetrics()
	if err != nil {
		t.Fatalf("build the exporter: %v", err)
	}
	t.Cleanup(func() { otel.SetMeterProvider(sdkmetric.NewMeterProvider()) })

	HintIssued(context.Background(), "SITE-1")
	HintConsumed(context.Background(), "SITE-1")
	HintExpired(context.Background(), "SITE-2")
	ObserveRecommender(2 * time.Millisecond)
	ObserveConsumerLag(context.Background(), "task-interleaving-fulfillment", time.Second)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()
	for _, want := range []string{
		"taskinterleaving_hints_issued",
		"taskinterleaving_hints_consumed",
		"taskinterleaving_hints_expired",
		"taskinterleaving_recommender_duration",
		"taskinterleaving_kafka_consumer_lag",
		`site="SITE-1"`,
		`consumer="task-interleaving-fulfillment"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the exposition format lacks %q", want)
		}
	}
}
