package telemetry

import (
	"fmt"
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// PrometheusMetrics installs the OTel Prometheus exporter as the global
// MeterProvider (so this package's instruments forward to it) and returns the
// handler that serves the exposition format on the default registry. The
// composition root mounts it on GET /metrics; there is no separate metrics
// port.
func PrometheusMetrics() (http.Handler, error) {
	exporter, err := prometheus.New()
	if err != nil {
		return nil, fmt.Errorf("build the prometheus exporter: %w", err)
	}
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter)))
	return promhttp.Handler(), nil
}
