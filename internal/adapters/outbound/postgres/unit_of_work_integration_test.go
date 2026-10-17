//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/claudioed/task-interleaving/internal/adapters/outbound/postgres/pgtx"
	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/testing/repocontract"
)

// every table a unit of work can touch.
var allTables = []string{
	"interleaving_policies", "station_trips", "hints", "candidates", "task_pool",
	"zones", "locations", "processed_events", "outbox_events",
}

// enqueue writes one outbox row on the transaction the unit of work carries:
// the write side of the transactional outbox, as the events adapter will do it.
func enqueue(ctx context.Context, eventID string) error {
	tx, ok := pgtx.From(ctx)
	if !ok {
		return errors.New("enqueue outside a unit of work")
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO outbox_events (event_id, topic, event_type, subject, key, dataschema, value)
		VALUES ($1, 'topic', 'type', 'STN-1', $2, 'urn:test', '{}')
	`, eventID, []byte("STN-1"))
	return err
}

// writeEverything writes one row to every table a unit of work can touch.
func writeEverything(ctx context.Context, t *testing.T, s stores) error {
	t.Helper()
	pol := repository.PolicyRecord{Policy: repocontract.Policy(t, repocontract.Site), DeclaredAt: repocontract.T0}
	trip := repocontract.Hinted(t, "STN-1", "hint-1", repocontract.T0)
	steps := []func() error{
		func() error { return s.policies.Save(ctx, pol, 0) },
		func() error { return s.trips.Save(ctx, trip, 0) },
		func() error {
			return s.tasks.Save(ctx, repository.Task{ID: "task-1", SiteID: "SITE-1", State: repository.TaskAvailable})
		},
		func() error {
			return s.book.SaveZone(ctx, repository.Zone{ID: "z1", SiteCode: "SITE-1", ZoneCode: "FWD"})
		},
		func() error { return s.book.SaveLocation(ctx, "LOC-1", "z1") },
		func() error { _, err := s.processed.Claim(ctx, "c", "e1"); return err },
		func() error { return enqueue(ctx, "e1") },
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

func wantRows(t *testing.T, s stores, n int) {
	t.Helper()
	for _, table := range allTables {
		if got := s.count(table); got != n {
			t.Fatalf("%s has %d rows, want %d", table, got, n)
		}
	}
}

func TestUnitOfWorkRollsEveryTableBackTogether(t *testing.T) {
	s := newStores(t)
	ctx := context.Background()
	err := s.uow.Do(ctx, func(ctx context.Context) error {
		if err := writeEverything(ctx, t, s); err != nil {
			return err
		}
		return errBoom
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("Do = %v, want the error fn returned", err)
	}
	wantRows(t, s, 0)
	if _, err := s.trips.Get(ctx, "STN-1"); !errors.Is(err, repository.ErrStationNotFound) {
		t.Fatalf("the trip survived the rollback: %v", err)
	}
}

func TestUnitOfWorkCommitsEveryTableTogether(t *testing.T) {
	s := newStores(t)
	if err := s.uow.Do(context.Background(), func(ctx context.Context) error { return writeEverything(ctx, t, s) }); err != nil {
		t.Fatal(err)
	}
	// hints and candidates hold the trip's hint with its two candidates
	for table, want := range map[string]int{"hints": 1, "candidates": 2, "station_trips": 1, "outbox_events": 1} {
		if got := s.count(table); got != want {
			t.Fatalf("%s has %d rows, want %d", table, got, want)
		}
	}
}

func TestUnitOfWorkRollsBackOnPanicAndStaysUsable(t *testing.T) {
	s := newStores(t)
	func() {
		defer func() { _ = recover() }()
		_ = s.uow.Do(context.Background(), func(ctx context.Context) error {
			if err := writeEverything(ctx, t, s); err != nil {
				return err
			}
			panic("fn blew up")
		})
	}()
	wantRows(t, s, 0)
	if err := s.uow.Do(context.Background(), func(ctx context.Context) error { return writeEverything(ctx, t, s) }); err != nil {
		t.Fatalf("the pool must be usable after a panic: %v", err)
	}
	if got := s.count("station_trips"); got != 1 {
		t.Fatalf("station_trips has %d rows, want 1", got)
	}
}

func TestNestedUnitOfWorkJoinsTheOuterTransaction(t *testing.T) {
	s := newStores(t)
	ctx := context.Background()
	err := s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.uow.Do(ctx, func(ctx context.Context) error { return writeEverything(ctx, t, s) }); err != nil {
			return err
		}
		return errBoom // the inner Do committed nothing of its own
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("Do = %v", err)
	}
	wantRows(t, s, 0)
}

func TestARolledBackEffectUnclaimsTheEventID(t *testing.T) {
	s := newStores(t)
	ctx := context.Background()
	_ = s.uow.Do(ctx, func(ctx context.Context) error {
		if claimed, err := s.processed.Claim(ctx, "fulfillment", "evt-1"); err != nil || !claimed {
			t.Errorf("claim = %v %v", claimed, err)
		}
		return errBoom
	})
	claimed, err := s.processed.Claim(ctx, "fulfillment", "evt-1")
	if err != nil || !claimed {
		t.Fatalf("the redelivery must be processed, not skipped: %v %v", claimed, err)
	}
}

func TestConcurrentClaimsOfOneEventLetExactlyOneWin(t *testing.T) {
	s := newStores(t)
	const n = 8
	claims := make([]bool, n)
	results := race(n, func(i int) error {
		return s.uow.Do(context.Background(), func(ctx context.Context) error {
			claimed, err := s.processed.Claim(ctx, "fulfillment", "evt-1")
			claims[i] = claimed
			return err
		})
	})
	if won := wins(t, results); won != n {
		t.Fatalf("%d units of work finished, want all %d", won, n)
	}
	claimedBy := 0
	for _, c := range claims {
		if c {
			claimedBy++
		}
	}
	if claimedBy != 1 {
		t.Fatalf("%d claims won, want exactly 1", claimedBy)
	}
}
