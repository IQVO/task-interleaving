//go:build integration

package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	inboundhttp "github.com/claudioed/task-interleaving/internal/adapters/inbound/http"
	"github.com/claudioed/task-interleaving/internal/adapters/outbound/postgres"
	"github.com/claudioed/task-interleaving/internal/application/ports"
	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/testing/pgtest"
)

// inflightTimeout bounds every wait of the in-flight probe, so a wedged
// request fails the test instead of hanging the integration job.
const inflightTimeout = 15 * time.Second

// parkingStore parks the FIRST Save of a key after it ran: the original's
// key row is inserted but its transaction is still open, so the unique key
// is held exactly as it is between the INSERT and the COMMIT of the request.
type parkingStore struct {
	ports.IdempotencyStore
	armed   atomic.Bool
	parked  chan struct{}
	release chan struct{}
}

func newParkingStore(next ports.IdempotencyStore) *parkingStore {
	s := &parkingStore{IdempotencyStore: next, parked: make(chan struct{}), release: make(chan struct{})}
	s.armed.Store(true)
	return s
}

func (s *parkingStore) Save(ctx context.Context, rec repository.IdempotencyRecord) error {
	if err := s.IdempotencyStore.Save(ctx, rec); err != nil {
		return err
	}
	if s.armed.CompareAndSwap(true, false) {
		close(s.parked)
		select {
		case <-s.release:
		case <-time.After(inflightTimeout):
		}
	}
	return nil
}

// answer is one HTTP answer collected from a goroutine (no t.Fatal there).
type answer struct {
	status int
	body   string
	hdr    http.Header
	err    error
}

func (a answer) String() string {
	return fmt.Sprintf("%d replayed=%q %s (err=%v)", a.status, a.hdr.Get("Idempotency-Replayed"), a.body, a.err)
}

func simulateAsync(url, body, key string) <-chan answer {
	out := make(chan answer, 1)
	go func() {
		req, err := http.NewRequest(http.MethodPost, url+"/recommendations", strings.NewReader(body))
		if err != nil {
			out <- answer{err: err}
			return
		}
		req.Header.Set("Idempotency-Key", key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			out <- answer{err: err}
			return
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		out <- answer{status: resp.StatusCode, body: string(raw), hdr: resp.Header, err: err}
	}()
	return out
}

func awaitAnswer(t *testing.T, what string, c <-chan answer) answer {
	t.Helper()
	select {
	case a := <-c:
		if a.err != nil {
			t.Fatalf("%s: %v", what, a.err)
		}
		return a
	case <-time.After(inflightTimeout):
		t.Fatalf("%s: no answer within %s", what, inflightTimeout)
		return answer{}
	}
}

// Fleet audit 2026-11-21 (defect 1, task-interleaving row): an in-flight
// duplicate of the keyed POST /recommendations must replay the original's
// stored answer, never answer a RETRYABLE refusal of its own re-run.
//
// The original is parked right after its key row is inserted, inside its open
// transaction. Two copies are then sent with the same key: one with the same
// body, one with another body. Both pass the first Lookup (nothing is
// committed yet), run the read-only simulation and block on the key's primary
// key in their own Save; the test waits until Postgres reports two Lock
// waiters, then releases the original. Expected: the original is a fresh 200,
// the same-body copy replays it byte for byte with Idempotency-Replayed: true,
// the other body is 409 idempotency-key-reused, and the key has one row.
//
// It does NOT reproduce the audit's claim (the probe is green on develop
// 3ea175d): the simulation writes nothing (usecases/recommend.go 33-35,
// 47-80), so it inserts no hint and takes no row lock; hint_history.go
// insertHint is reached only through StationTripRepo.Save from the event
// consumers' TripWriter (usecases/ranker.go 131). The only lock a copy can wait
// on is the idempotency key itself (postgres/processed_and_idempotency.go
// Save), and after that wait saveAndReread finds the original's committed row
// and replays it (inbound/http/idempotency.go serveFresh, errLostRace).
func TestPostgresAnInFlightDuplicateReplaysTheOriginalsAnswer(t *testing.T) {
	var parking *parkingStore
	svc := bootPostgresOver(t, func(ad *adapters, url string) {
		parking = newParkingStore(postgres.NewIdempotencyStore(pgtest.Open(t, url)))
		ad.idempotency = inboundhttp.NewIdempotency(parking, ad.uow, ad.clock).Middleware
	})
	svc.declare(t)

	const key = "k-inflight"
	original := simulateAsync(svc.srv.URL, simulation, key)
	select {
	case <-parking.parked:
	case <-time.After(inflightTimeout):
		t.Fatalf("the original never reached its key Save")
	}
	same := simulateAsync(svc.srv.URL, simulation, key)
	other := simulateAsync(svc.srv.URL, `{"stationId":"STN-08","fromLocation":"A-01"}`, key)
	svc.awaitLockWaiters(t, 2)
	close(parking.release)

	first := awaitAnswer(t, "original", original)
	if first.status != http.StatusOK || first.hdr.Get("Idempotency-Replayed") != "" || !strings.Contains(first.body, `"taskId":"task-9c2e"`) {
		t.Fatalf("original = %s, want a fresh 200 with the recommendation", first)
	}
	if dup := awaitAnswer(t, "same-body copy", same); dup.status != first.status || dup.body != first.body || dup.hdr.Get("Idempotency-Replayed") != "true" {
		t.Errorf("in-flight copy = %s, want the original's 200 replayed byte for byte", dup)
	}
	if o := awaitAnswer(t, "other-body copy", other); o.status != http.StatusConflict || !strings.Contains(o.body, "/idempotency-key-reused") {
		t.Errorf("in-flight copy with another body = %s, want 409 idempotency-key-reused", o)
	}
	if n := svc.count("idempotency_keys"); n != 1 {
		t.Errorf("idempotency rows = %d, want 1: the original's answer stored once", n)
	}
	if n := svc.count("hints") + svc.count("station_trips") + svc.count("outbox_events WHERE event_type LIKE '%Hint%'"); n != 0 {
		t.Errorf("the simulation wrote %d hint/station/hint-event rows, want none", n)
	}
}
