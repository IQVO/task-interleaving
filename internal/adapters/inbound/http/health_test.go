package http_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	inboundhttp "github.com/claudioed/task-interleaving/internal/adapters/inbound/http"
)

func TestHealthAndReadiness(t *testing.T) {
	f := newFixture(t)
	health := f.mustCall(t, http.MethodGet, "/healthz", "", http.StatusOK)
	if health.body["status"] != "ok" || health.contentType() != "application/json" {
		t.Fatalf("healthz: %s", health.raw)
	}
	ready := f.mustCall(t, http.MethodGet, "/readyz", "", http.StatusOK)
	if ready.body["status"] != "ready" {
		t.Fatalf("readyz: %s", ready.raw)
	}
	f.readiness.SetNotReady()
	draining := f.mustCall(t, http.MethodGet, "/readyz", "", http.StatusServiceUnavailable)
	if draining.body["status"] != "not_ready" {
		t.Fatalf("readyz while draining: %s", draining.raw)
	}
	f.mustCall(t, http.MethodGet, "/healthz", "", http.StatusOK) // liveness never flips
}

func TestNilReadinessIsAlwaysReady(t *testing.T) {
	var gate *inboundhttp.Readiness
	gate.SetNotReady() // nil-safe
	if !gate.Ready() {
		t.Fatal("a nil gate must read as ready")
	}
	if failing := gate.Failing(context.Background()); failing != nil {
		t.Fatalf("a nil gate has no failing dependency, got %v", failing)
	}
	rec := httptest.NewRecorder()
	inboundhttp.NewRouter(&inboundhttp.Server{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", http.NoBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("readyz without a gate = %d", rec.Code)
	}
}

// depProbe is a dependency whose verdict the test flips and whose calls it counts.
type depProbe struct {
	calls atomic.Int32
	err   atomic.Pointer[error]
}

func (p *depProbe) fail(err error) { p.err.Store(&err) }
func (p *depProbe) heal()          { p.err.Store(nil) }

func (p *depProbe) dependency(name string) inboundhttp.Dependency {
	return inboundhttp.Dependency{Name: name, Check: func(context.Context) error {
		p.calls.Add(1)
		if e := p.err.Load(); e != nil {
			return *e
		}
		return nil
	}}
}

func serveReadyz(gate *inboundhttp.Readiness) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	inboundhttp.NewRouter(&inboundhttp.Server{Readiness: gate}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", http.NoBody))
	return rec
}

func TestReadyzIsNotReadyWhileADependencyIsDown(t *testing.T) {
	pg, kafka := &depProbe{}, &depProbe{}
	gate := inboundhttp.NewReadiness(time.Nanosecond, time.Second, pg.dependency("postgres"), kafka.dependency("kafka"))
	if rec := serveReadyz(gate); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ready"`) {
		t.Fatalf("all dependencies up = %d %s", rec.Code, rec.Body.String())
	}
	kafka.fail(errDB)
	time.Sleep(time.Millisecond) // the nanosecond cache has expired
	rec := serveReadyz(gate)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("kafka down = %d %s %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "/dependency-unavailable") || !strings.Contains(body, "not ready: kafka") || strings.Contains(body, "postgres") || strings.Contains(body, errDB.Error()) {
		t.Fatalf("problem body must name only the failing dependency and never the cause: %s", body)
	}
	pg.fail(errDB)
	time.Sleep(time.Millisecond)
	if rec := serveReadyz(gate); !strings.Contains(rec.Body.String(), "not ready: postgres, kafka") {
		t.Fatalf("both down = %d %s", rec.Code, rec.Body.String())
	}
	pg.heal()
	kafka.heal()
	time.Sleep(time.Millisecond)
	if rec := serveReadyz(gate); rec.Code != http.StatusOK {
		t.Fatalf("recovered = %d %s", rec.Code, rec.Body.String())
	}
}

// The verdict, pass or fail, is cached for the ttl: probes cannot hammer
// Postgres or Kafka.
func TestReadinessCachesTheVerdictForItsTTL(t *testing.T) {
	probe := &depProbe{}
	gate := inboundhttp.NewReadiness(time.Hour, time.Second, probe.dependency("postgres"))
	for range 5 {
		if failing := gate.Failing(context.Background()); failing != nil {
			t.Fatalf("failing = %v", failing)
		}
	}
	if n := probe.calls.Load(); n != 1 {
		t.Fatalf("dependency checked %d times within the ttl, want 1", n)
	}
	probe.fail(errDB)
	if failing := gate.Failing(context.Background()); failing != nil {
		t.Fatalf("a cached healthy verdict must be reused, got %v", failing)
	}
}

func TestReadinessDefaultsApplyToNonPositiveValues(t *testing.T) {
	probe := &depProbe{}
	gate := inboundhttp.NewReadiness(0, -1, probe.dependency("postgres"))
	gate.Failing(context.Background())
	gate.Failing(context.Background())
	if n := probe.calls.Load(); n != 1 {
		t.Fatalf("default ttl must cache: %d checks", n)
	}
}

func TestAPanickingHandlerIsA500Problem(t *testing.T) {
	rec := httptest.NewRecorder()
	// No use cases wired: the nil dereference stands in for any handler bug.
	inboundhttp.NewRouter(&inboundhttp.Server{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hints", http.NoBody))
	if rec.Code != http.StatusInternalServerError || rec.Header().Get("Content-Type") != "application/problem+json" ||
		!strings.Contains(rec.Body.String(), "/internal-error") {
		t.Fatalf("panic answer = %d %s %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
}

func TestUnknownRoutesAndMethodsAreNotServed(t *testing.T) {
	f := newFixture(t)
	if r := f.call(t, http.MethodGet, "/nope", ""); r.status != http.StatusNotFound {
		t.Fatalf("GET /nope = %d", r.status)
	}
	for method, path := range map[string]string{
		http.MethodDelete: "/sites/SITE-1/policy",
		http.MethodPost:   "/hints",
		http.MethodPut:    "/recommendations",
		http.MethodPatch:  "/stations/STN-07",
		http.MethodGet:    "/recommendations",
	} {
		if r := f.call(t, method, path, `{}`); r.status != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d, want 405", method, path, r.status)
		}
	}
}
