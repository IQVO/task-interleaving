// Package telemetry declares this context's OTel metric instruments: the hint
// lifecycle counters (issued, consumed, expired), the recommender latency
// histogram and the Kafka consumer-lag histogram.
//
// The instruments come from the GLOBAL MeterProvider (otel.Meter), exactly
// like the a2a adapter's tracer: until a composition root installs a provider
// they are no-ops that cost one call each, and once cmd/api installs the
// Prometheus exporter (PrometheusMetrics) every record turns into a series on
// GET /metrics. The package depends on no other package of this repo, so
// application use cases and inbound adapters may call it freely.
package telemetry

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
)

// meterName namespaces every instrument this package declares; the Prometheus
// exporter renders it as the taskinterleaving_* series.
const meterName = "github.com/claudioed/task-interleaving"

// noopMeter builds the fallback instruments for a name error, which cannot
// happen for these fixed names but keeps the constructors total.
var noopMeter = noop.NewMeterProvider().Meter(meterName)

// instruments holds the package's instruments, built once from the global
// meter (var initialization, not init()). The global meter forwards to
// whatever MeterProvider the composition root sets, whenever it sets it.
type instruments struct {
	hintsIssued     metric.Int64Counter
	hintsConsumed   metric.Int64Counter
	hintsExpired    metric.Int64Counter
	recommenderTime metric.Float64Histogram
	consumerLag     metric.Float64Histogram
}

var inst = newInstruments()

func newInstruments() instruments {
	m := otel.Meter(meterName)
	return instruments{
		hintsIssued:   counter(m, "taskinterleaving.hints.issued", "Hints issued to stations by the interleaving recommender."),
		hintsConsumed: counter(m, "taskinterleaving.hints.consumed", "Hints whose candidate the hinted station claimed."),
		hintsExpired:  counter(m, "taskinterleaving.hints.expired", "Hints that reached their expires_at unconsumed."),
		recommenderTime: histogram(m, "taskinterleaving.recommender.duration",
			"Wall time of one recommender run, simulation or issuance alike."),
		consumerLag: histogram(m, "taskinterleaving.kafka.consumer.lag",
			"How far processing trails the CloudEvent time of a handled message."),
	}
}

// counter builds an Int64Counter, falling back to the no-op meter on error.
func counter(m metric.Meter, name, doc string) metric.Int64Counter {
	c, err := m.Int64Counter(name, metric.WithDescription(doc))
	if err == nil {
		return c
	}
	c, _ = noopMeter.Int64Counter(name)
	return c
}

// histogram builds a seconds Float64Histogram, falling back on error.
func histogram(m metric.Meter, name, doc string) metric.Float64Histogram {
	h, err := m.Float64Histogram(name, metric.WithDescription(doc), metric.WithUnit("s"))
	if err == nil {
		return h
	}
	h, _ = noopMeter.Float64Histogram(name)
	return h
}

// HintIssued counts one hint issued for site.
func HintIssued(ctx context.Context, site string) {
	inst.hintsIssued.Add(ctx, 1, metric.WithAttributes(attribute.String("site", site)))
}

// HintConsumed counts one hint whose candidate was claimed, of site.
func HintConsumed(ctx context.Context, site string) {
	inst.hintsConsumed.Add(ctx, 1, metric.WithAttributes(attribute.String("site", site)))
}

// HintExpired counts one hint that expired unconsumed, of site.
func HintExpired(ctx context.Context, site string) {
	inst.hintsExpired.Add(ctx, 1, metric.WithAttributes(attribute.String("site", site)))
}

// ObserveRecommender records one recommender run's wall time.
func ObserveRecommender(d time.Duration) {
	inst.recommenderTime.Record(context.Background(), d.Seconds())
}

// ObserveConsumerLag records how far processing by consumer trails a
// message's CloudEvent time.
func ObserveConsumerLag(ctx context.Context, consumer string, d time.Duration) {
	inst.consumerLag.Record(ctx, d.Seconds(), metric.WithAttributes(attribute.String("consumer", consumer)))
}
