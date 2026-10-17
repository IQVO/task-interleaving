//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/domain/stationtrip"
	"github.com/claudioed/task-interleaving/internal/testing/pgtest"
	"github.com/claudioed/task-interleaving/internal/testing/repocontract"
)

const activeHintIndex = "uq_hints_one_active_per_station"

// insertHintRow writes one hint row directly, bypassing the repository, so the
// test exercises the database's own rule.
func insertHintRow(s stores, id, station, state string) error {
	return s.exec(`
		INSERT INTO hints (hint_id, station_id, site_id, after_task_id, origin_location,
		                   issued_at, expires_at, policy_version, state, version)
		VALUES ($1, $2, 'SITE-1', 'task-0', 'LOC-0', $3, $4, 1, $5, 1)
	`, id, station, repocontract.T0, repocontract.T0.Add(time.Minute), state)
}

func isActiveHintViolation(err error) bool {
	return pgtest.IsUniqueViolation(err, activeHintIndex)
}

// TestTheDatabaseAdmitsOneActiveHintPerStation proves the partial unique index,
// not just the aggregate: hint rows racing for one station leave exactly one
// Active row and every loser fails on the index.
func TestTheDatabaseAdmitsOneActiveHintPerStation(t *testing.T) {
	s := newStores(t)
	if err := s.trips.Save(context.Background(), repocontract.EmptyTrip(t, "STN-1"), 0); err != nil {
		t.Fatal(err)
	}
	const n = 8
	results := race(n, func(i int) error { return insertHintRow(s, fmt.Sprintf("hint-%d", i), "STN-1", "Active") })
	won := 0
	for _, err := range results {
		switch {
		case err == nil:
			won++
		case !isActiveHintViolation(err):
			t.Fatalf("a loser must fail on %s, got %v", activeHintIndex, err)
		}
	}
	if won != 1 {
		t.Fatalf("%d Active hints inserted, want exactly 1", won)
	}
	if active := s.count("hints WHERE state = 'Active'"); active != 1 {
		t.Fatalf("%d Active hints stored, want 1", active)
	}
}

func TestTheActiveHintIndexIsPartialAndPerStation(t *testing.T) {
	s := newStores(t)
	def := s.indexDef(activeHintIndex)
	for _, want := range []string{"UNIQUE", "(station_id)", "state = 'Active'"} {
		if !strings.Contains(def, want) {
			t.Fatalf("index definition %q lacks %q", def, want)
		}
	}
	// the history is not limited: any number of closed hints, and one Active
	// hint for each of two stations
	ctx := context.Background()
	for _, st := range []string{"STN-1", "STN-2"} {
		if err := s.trips.Save(ctx, repocontract.EmptyTrip(t, st), 0); err != nil {
			t.Fatal(err)
		}
		for i, state := range []string{"Expired", "Expired", "Superseded", "Superseded", "Active"} {
			if err := insertHintRow(s, fmt.Sprintf("hint-%s-%d", st, i), st, state); err != nil {
				t.Fatalf("%s hint %d (%s): %v", st, i, state, err)
			}
		}
	}
	if n := s.count("hints"); n != 10 {
		t.Fatalf("hints = %d, want 10", n)
	}
	if err := insertHintRow(s, "hint-extra", "STN-1", "Active"); !isActiveHintViolation(err) {
		t.Fatalf("a second Active hint for STN-1 must be refused by the index, got %v", err)
	}
}

// TestAHintCannotBeReopenedBesideAnActiveOne covers the UPDATE path of the
// index: moving a closed row back to Active is refused while another is Active.
func TestAHintCannotBeReopenedBesideAnActiveOne(t *testing.T) {
	s := newStores(t)
	if err := s.trips.Save(context.Background(), repocontract.EmptyTrip(t, "STN-1"), 0); err != nil {
		t.Fatal(err)
	}
	for id, state := range map[string]string{"hint-1": "Expired", "hint-2": "Active"} {
		if err := insertHintRow(s, id, "STN-1", state); err != nil {
			t.Fatal(err)
		}
	}
	err := s.exec(`UPDATE hints SET state = 'Active' WHERE hint_id = 'hint-1'`)
	if !isActiveHintViolation(err) {
		t.Fatalf("reopening beside an Active hint: %v", err)
	}
}

// TestRacingIssuersOfOneStationLetExactlyOneWin proves the version guard under
// real concurrency: writers that loaded the same trip and each issue a new hint
// leave exactly one Active hint and one winner.
func TestRacingIssuersOfOneStationLetExactlyOneWin(t *testing.T) {
	s := newStores(t)
	ctx := context.Background()
	if err := s.trips.Save(ctx, repocontract.Hinted(t, "STN-1", "hint-0", repocontract.T0), 0); err != nil {
		t.Fatal(err)
	}
	const n = 6
	trips := make([]*stationtrip.StationTrip, n)
	var loaded int64
	for i := range n {
		trip, err := s.trips.Get(ctx, "STN-1")
		if err != nil {
			t.Fatal(err)
		}
		loaded = trip.Version()
		repocontract.Complete(t, trip, fmt.Sprintf("task-%d", i), "LOC-1", repocontract.T0.Add(time.Minute))
		repocontract.Issue(t, trip, fmt.Sprintf("hint-new-%d", i), 1, repocontract.T0.Add(time.Minute+time.Second))
		trips[i] = trip
	}
	results := race(n, func(i int) error { return s.trips.Save(ctx, trips[i], loaded) })
	if won := wins(t, results, repository.ErrConcurrentModification); won != 1 {
		t.Fatalf("%d writers won, want exactly 1", won)
	}
	if active := s.count("hints WHERE state = 'Active'"); active != 1 {
		t.Fatalf("%d Active hints, want 1", active)
	}
	if all := s.count("hints"); all != 2 {
		t.Fatalf("%d hints stored, want the superseded one and the winner", all)
	}
	old, err := s.trips.GetHint(ctx, "hint-0")
	if err != nil || old.Hint.State != stationtrip.StateSuperseded {
		t.Fatalf("hint-0 = %+v %v", old, err)
	}
}

// TestRacingFirstCompletionsOfANewStationLetExactlyOneWin: the insert race ends
// on the primary key, and the losers leave no hint behind.
func TestRacingFirstCompletionsOfANewStationLetExactlyOneWin(t *testing.T) {
	s := newStores(t)
	ctx := context.Background()
	const n = 6
	trips := make([]*stationtrip.StationTrip, n)
	for i := range n {
		trips[i] = repocontract.Hinted(t, "STN-1", fmt.Sprintf("hint-%d", i), repocontract.T0)
	}
	results := race(n, func(i int) error { return s.trips.Save(ctx, trips[i], 0) })
	if won := wins(t, results, repository.ErrConcurrentModification); won != 1 {
		t.Fatalf("%d writers won, want exactly 1", won)
	}
	if s.count("station_trips") != 1 || s.count("hints") != 1 || s.count("candidates") != 2 {
		t.Fatalf("trips %d hints %d candidates %d, want 1 1 2", s.count("station_trips"), s.count("hints"), s.count("candidates"))
	}
}

func TestDifferentStationsNeverConflict(t *testing.T) {
	s := newStores(t)
	ctx := context.Background()
	const n = 6
	trips := make([]*stationtrip.StationTrip, n)
	for i := range n {
		trips[i] = repocontract.Hinted(t, fmt.Sprintf("STN-%d", i), fmt.Sprintf("hint-%d", i), repocontract.T0)
	}
	results := race(n, func(i int) error { return s.trips.Save(ctx, trips[i], 0) })
	if won := wins(t, results); won != n {
		t.Fatalf("%d stations saved, want %d", won, n)
	}
	if active := s.count("hints WHERE state = 'Active'"); active != n {
		t.Fatalf("%d Active hints, want one per station (%d)", active, n)
	}
}

// TestAFailedHintWriteRollsBackTheTripWrite: the trip row and its history are
// one atomic save. A hint id already taken by another station fails the hint
// insert, and nothing of the trip update (nor the supersede) survives.
func TestAFailedHintWriteRollsBackTheTripWrite(t *testing.T) {
	s := newStores(t)
	ctx := context.Background()
	for _, st := range []struct{ station, hint string }{{"STN-1", "hint-1"}, {"STN-2", "hint-2"}} {
		if err := s.trips.Save(ctx, repocontract.Hinted(t, st.station, st.hint, repocontract.T0), 0); err != nil {
			t.Fatal(err)
		}
	}
	trip, err := s.trips.Get(ctx, "STN-2")
	if err != nil {
		t.Fatal(err)
	}
	loaded := trip.Version()
	repocontract.Complete(t, trip, "task-1", "LOC-1", repocontract.T0.Add(time.Minute))
	repocontract.Issue(t, trip, "hint-1", 1, repocontract.T0.Add(time.Minute+time.Second)) // taken by STN-1
	if err := s.trips.Save(ctx, trip, loaded); !errors.Is(err, repository.ErrConcurrentModification) {
		t.Fatalf("Save = %v, want ErrConcurrentModification", err)
	}
	again, err := s.trips.Get(ctx, "STN-2")
	if err != nil || again.Version() != loaded {
		t.Fatalf("the trip write survived: %v %v", again, err)
	}
	if h, _ := again.Hint(); h.ID != "hint-2" || h.State != stationtrip.StateActive {
		t.Fatalf("the supersede survived: %+v", h)
	}
	if first, err := s.trips.GetHint(ctx, "hint-1"); err != nil || first.StationID != "STN-1" {
		t.Fatalf("hint-1 = %+v %v", first, err)
	}
}
