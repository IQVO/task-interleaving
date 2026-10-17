package http_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	rec := httptest.NewRecorder()
	inboundhttp.NewRouter(&inboundhttp.Server{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", http.NoBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("readyz without a gate = %d", rec.Code)
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
