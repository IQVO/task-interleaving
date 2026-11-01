package repocontract

import (
	"testing"
	"time"

	"github.com/claudioed/task-interleaving/internal/application/ports"
	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/domain/stationtrip"
	"github.com/claudioed/task-interleaving/internal/domain/travel"
)

// StationTripRepo runs the StationTripRepo contract: the trip itself, then the
// hint history it keeps. newRepo must return an EMPTY repository on every call.
func StationTripRepo(t *testing.T, newRepo func(t *testing.T) ports.StationTripRepo) {
	t.Helper()
	cases := map[string]func(t *testing.T, r ports.StationTripRepo){
		"an unknown station is not found":                    tripNotFound,
		"save then get round trips a hinted trip":            tripRoundTrip,
		"a trip that recorded nothing round trips":           tripEmptyRoundTrip,
		"a completion without geometry round trips":          tripNoPointRoundTrip,
		"a consumed hint round trips":                        tripConsumedRoundTrip,
		"inserting over an existing station is lost":         tripInsertTwice,
		"an update is guarded by the loaded version":         tripUpdateGuard,
		"the repository does not share the live aggregate":   tripNoSharing,
		"stations are independent":                           tripsIndependent,
		"a newer hint supersedes the Active one":             hintSupersedes,
		"a consumed hint moves on once":                      hintConsumes,
		"an expired hint moves on":                           hintExpires,
		"a terminal hint stays when the next one is issued":  hintTerminalStays,
		"an unchanged hint is not touched by a later save":   hintUnchangedSave,
		"expired-active lists the due stations in order":     hintExpiredActive,
		"an unknown hint is not found":                       hintUnknown,
		"a hint record carries station, site and candidates": hintRecord,
		"hints list newest first and page by cursor":         hintListPaging,
		"hints list narrows by station, site and state":      hintListFilters,
		"the history keeps every hint of a busy station":     hintHistoryDepth,
		"a hint id taken by another station is a conflict":   hintIDClash,
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) { fn(t, newRepo(t)) })
	}
}

func saved(t *testing.T, r ports.StationTripRepo, trip *stationtrip.StationTrip, loaded int64) {
	t.Helper()
	must(t, r.Save(bg, trip, loaded))
}

func tripNotFound(t *testing.T, r ports.StationTripRepo) {
	_, err := r.Get(bg, "STN-1")
	wantErr(t, err, repository.ErrStationNotFound)
}

func roundTrips(t *testing.T, r ports.StationTripRepo, trip *stationtrip.StationTrip) {
	t.Helper()
	saved(t, r, trip, 0)
	got, err := r.Get(bg, trip.ID())
	must(t, err)
	if !sameTrip(got, trip) {
		t.Fatalf("got %+v, want %+v", got.Snapshot(), trip.Snapshot())
	}
}

func tripRoundTrip(t *testing.T, r ports.StationTripRepo) {
	roundTrips(t, r, Hinted(t, "STN-1", "hint-1", T0))
}

func tripEmptyRoundTrip(t *testing.T, r ports.StationTripRepo) {
	roundTrips(t, r, EmptyTrip(t, "STN-1"))
}

func tripNoPointRoundTrip(t *testing.T, r ports.StationTripRepo) {
	trip := EmptyTrip(t, "STN-1")
	pos := stationtrip.Position{Site: Site, Place: travel.Place{Location: "LOC-X"}}
	changed, err := trip.RecordCompletion("task-0", T0, pos)
	if err != nil || !changed {
		t.Fatalf("RecordCompletion = %v %v", changed, err)
	}
	roundTrips(t, r, trip)
}

func tripConsumedRoundTrip(t *testing.T, r ports.StationTripRepo) {
	trip := Hinted(t, "STN-1", "hint-1", T0)
	if !trip.RecordClaim("task-b", T0.Add(10*time.Second)) {
		t.Fatal("the claim should consume the hint")
	}
	roundTrips(t, r, trip)
}

func tripInsertTwice(t *testing.T, r ports.StationTripRepo) {
	trip := Hinted(t, "STN-1", "hint-1", T0)
	saved(t, r, trip, 0)
	wantErr(t, r.Save(bg, trip, 0), repository.ErrConcurrentModification)
	wantErr(t, r.Save(bg, Hinted(t, "STN-1", "hint-2", T0), 0), repository.ErrConcurrentModification)
	if rec, err := r.GetHint(bg, "hint-1"); err != nil || rec.Hint.State != stationtrip.StateActive {
		t.Fatalf("the losing insert changed the history: %+v %v", rec, err)
	}
	if _, err := r.GetHint(bg, "hint-2"); err == nil {
		t.Fatal("the losing insert left its hint behind")
	}
}

func tripUpdateGuard(t *testing.T, r ports.StationTripRepo) {
	trip := Hinted(t, "STN-1", "hint-1", T0)
	saved(t, r, trip, 0)
	loaded := trip.Version()
	Complete(t, trip, "task-1", "LOC-1", T0.Add(time.Minute))
	wantErr(t, r.Save(bg, trip, loaded+5), repository.ErrConcurrentModification)
	wantErr(t, r.Save(bg, Hinted(t, "STN-9", "hint-9", T0), 1), repository.ErrConcurrentModification)
	saved(t, r, trip, loaded)
	got, err := r.Get(bg, "STN-1")
	must(t, err)
	if !sameTrip(got, trip) {
		t.Fatalf("after update: %+v, want %+v", got.Snapshot(), trip.Snapshot())
	}
	// the same loaded version is stale now
	wantErr(t, r.Save(bg, trip, loaded), repository.ErrConcurrentModification)
}

func tripNoSharing(t *testing.T, r ports.StationTripRepo) {
	trip := Hinted(t, "STN-1", "hint-1", T0)
	saved(t, r, trip, 0)
	trip.Expire(T0.Add(time.Hour))
	got, err := r.Get(bg, "STN-1")
	must(t, err)
	if _, active := got.ActiveHint(); !active {
		t.Fatal("a change to the live aggregate leaked into the store")
	}
	got.Expire(T0.Add(time.Hour))
	again, _ := r.Get(bg, "STN-1")
	if _, active := again.ActiveHint(); !active {
		t.Fatal("a change to a loaded aggregate leaked into the store")
	}
}

func tripsIndependent(t *testing.T, r ports.StationTripRepo) {
	saved(t, r, Hinted(t, "STN-1", "hint-1", T0), 0)
	saved(t, r, Hinted(t, "STN-2", "hint-2", T0), 0)
	a, err := r.Get(bg, "STN-1")
	must(t, err)
	b, err := r.Get(bg, "STN-2")
	must(t, err)
	ha, _ := a.Hint()
	hb, _ := b.Hint()
	if ha.ID != "hint-1" || hb.ID != "hint-2" {
		t.Fatalf("hints = %s %s", ha.ID, hb.ID)
	}
	// two Active hints, one per station
	active, err := r.ListHints(bg, repository.HintFilter{State: stationtrip.StateActive}, "", 10)
	must(t, err)
	if len(active) != 2 {
		t.Fatalf("active hints = %d, want one per station", len(active))
	}
}
