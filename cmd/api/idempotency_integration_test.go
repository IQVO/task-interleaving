//go:build integration

package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/claudioed/task-interleaving/internal/application/ports"
	"github.com/claudioed/task-interleaving/internal/domain/stationtrip"
)

// tripsDown fails the station lookup of the simulation while armed: the 5xx
// the idempotency middleware must roll back and never store.
type tripsDown struct {
	ports.StationTripRepo
	armed atomic.Bool
}

func (s *tripsDown) Get(ctx context.Context, id stationtrip.StationID) (*stationtrip.StationTrip, error) {
	if s.armed.Load() {
		return nil, errors.New("trips down")
	}
	return s.StationTripRepo.Get(ctx, id)
}

// A 5xx is rolled back with the key: no stored answer, and the same key then
// runs the real work and is a first execution (no Idempotency-Replayed).
func TestPostgresA5xxIsNeverStored(t *testing.T) {
	down := &tripsDown{}
	svc := bootPostgresWith(t, func(ad *adapters) {
		down.StationTripRepo = ad.trips
		ad.trips = down
	})
	svc.declare(t)

	down.armed.Store(true)
	status, body, hdr := svc.simulate(t, simulation, "k-5xx")
	if status != http.StatusInternalServerError || !strings.Contains(body, "/internal-error") {
		t.Fatalf("simulate while the station lookup fails = %d %s, want 500 internal-error", status, body)
	}
	if _, replayed := hdr["Idempotency-Replayed"]; replayed {
		t.Fatalf("a first execution carries Idempotency-Replayed: %v", hdr)
	}
	if n := svc.count("idempotency_keys"); n != 0 {
		t.Fatalf("idempotency rows = %d after a 5xx, want none stored", n)
	}

	down.armed.Store(false)
	status, body, hdr = svc.simulate(t, simulation, "k-5xx")
	if status != http.StatusOK {
		t.Fatalf("retry with the same key = %d %s, want the work to run (200)", status, body)
	}
	if _, replayed := hdr["Idempotency-Replayed"]; replayed {
		t.Fatalf("the retry of an unstored 5xx is a first execution: %v", hdr)
	}
	if n := svc.count("idempotency_keys"); n != 1 {
		t.Fatalf("idempotency rows = %d after the retry, want 1", n)
	}
}

// A malformed key is refused before the use case runs and stores nothing.
func TestPostgresMalformedKeyIsRefused(t *testing.T) {
	svc := bootPostgres(t)
	svc.declare(t)
	for name, key := range map[string]string{"too long": strings.Repeat("k", 129), "non ASCII": "caf\u00e9"} {
		status, body, _ := svc.simulate(t, simulation, key)
		if status != http.StatusBadRequest || !strings.Contains(body, "/idempotency-key-invalid") {
			t.Fatalf("%s key = %d %s, want 400 idempotency-key-invalid", name, status, body)
		}
	}
	if n := svc.count("idempotency_keys"); n != 0 {
		t.Fatalf("idempotency rows = %d after refused keys, want none", n)
	}
}
