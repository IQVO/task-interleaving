package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// countingDep is a dependency that counts its calls and fails on demand.
type countingDep struct {
	calls atomic.Int32
	fail  atomic.Bool
}

func (c *countingDep) dependency(name string) Dependency {
	return Dependency{Name: name, Check: func(context.Context) error {
		c.calls.Add(1)
		if c.fail.Load() {
			return errors.New("down")
		}
		return nil
	}}
}

func TestReadinessRechecksOnceTheTTLExpires(t *testing.T) {
	probe := &countingDep{}
	now := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	gate := NewReadiness(3*time.Second, time.Second, probe.dependency("postgres"))
	gate.now = func() time.Time { return now }
	gate.Failing(context.Background())
	now = now.Add(2 * time.Second)
	gate.Failing(context.Background())
	if n := probe.calls.Load(); n != 1 {
		t.Fatalf("checked %d times before the ttl, want 1", n)
	}
	probe.fail.Store(true)
	now = now.Add(time.Second) // exactly the ttl: the verdict is stale
	if failing := gate.Failing(context.Background()); len(failing) != 1 || failing[0] != "postgres" {
		t.Fatalf("after the ttl the check must run again, got %v", failing)
	}
	if n := probe.calls.Load(); n != 2 {
		t.Fatalf("checked %d times, want 2", n)
	}
}

// A dependency that hangs is cut off by the timeout and reported down.
func TestReadinessBoundsASlowDependency(t *testing.T) {
	slow := Dependency{Name: "kafka", Check: func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	gate := NewReadiness(time.Second, 20*time.Millisecond, slow)
	start := time.Now()
	if failing := gate.Failing(context.Background()); len(failing) != 1 {
		t.Fatalf("failing = %v", failing)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("the slow dependency was not cut off by the timeout")
	}
}

// A caller that hangs up must not poison the cached verdict.
func TestReadinessCheckIgnoresTheCallersCancellation(t *testing.T) {
	probe := &countingDep{}
	gate := NewReadiness(time.Hour, time.Second, Dependency{Name: "postgres", Check: func(ctx context.Context) error {
		probe.calls.Add(1)
		return ctx.Err()
	}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if failing := gate.Failing(ctx); failing != nil {
		t.Fatalf("a cancelled caller context must not fail the dependency, got %v", failing)
	}
}

// Draining wins over the dependency verdict and does not run the checks.
func TestDrainingDoesNotCheckDependencies(t *testing.T) {
	probe := &countingDep{}
	gate := NewReadiness(time.Second, time.Second, probe.dependency("postgres"))
	gate.SetNotReady()
	rec := httptest.NewRecorder()
	NewRouter(&Server{Readiness: gate}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", http.NoBody))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "not_ready") || probe.calls.Load() != 0 {
		t.Fatalf("draining = %d %s (checks=%d)", rec.Code, rec.Body.String(), probe.calls.Load())
	}
}
