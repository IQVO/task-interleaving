package repocontract

import (
	"testing"
	"time"

	"github.com/claudioed/task-interleaving/internal/application/ports"
	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/domain/recommend"
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

func completeAndIssue(t *testing.T, trip *stationtrip.StationTrip, task, hint string, at time.Time) {
	t.Helper()
	trip.ClearDomainEvents()
	Complete(t, trip, task, "LOC-"+task, at)
	Issue(t, trip, hint, 1, at.Add(time.Second))
}

func hintSupersedes(t *testing.T, r ports.StationTripRepo) {
	trip := Hinted(t, "STN-1", "hint-1", T0)
	saved(t, r, trip, 0)
	v := trip.Version()
	completeAndIssue(t, trip, "task-1", "hint-2", T0.Add(time.Minute))
	saved(t, r, trip, v)
	first, err := r.GetHint(bg, "hint-1")
	must(t, err)
	if first.Hint.State != stationtrip.StateSuperseded || first.Version != 2 || !first.SupersededAt.Equal(T0.Add(time.Minute+time.Second)) {
		t.Fatalf("hint-1 = %+v", first)
	}
	second, err := r.GetHint(bg, "hint-2")
	must(t, err)
	if second.Hint.State != stationtrip.StateActive || second.Version != 1 || !second.SupersededAt.IsZero() {
		t.Fatalf("hint-2 = %+v", second)
	}
	active, err := r.ListHints(bg, repository.HintFilter{StationID: "STN-1", State: stationtrip.StateActive}, "", 10)
	must(t, err)
	if len(active) != 1 || active[0].Hint.ID != "hint-2" {
		t.Fatalf("active hints of the station = %+v", active)
	}
}

func hintConsumes(t *testing.T, r ports.StationTripRepo) {
	trip := Hinted(t, "STN-1", "hint-1", T0)
	saved(t, r, trip, 0)
	v := trip.Version()
	if !trip.RecordClaim("task-a", T0.Add(5*time.Second)) {
		t.Fatal("the claim should consume the hint")
	}
	saved(t, r, trip, v)
	saved(t, r, trip, trip.Version()) // saving the same state again changes nothing
	rec, err := r.GetHint(bg, "hint-1")
	must(t, err)
	if rec.Hint.State != stationtrip.StateConsumed || rec.Version != 2 || rec.Hint.ConsumedTask != "task-a" ||
		!rec.Hint.ConsumedAt.Equal(T0.Add(5*time.Second)) || !rec.SupersededAt.IsZero() {
		t.Fatalf("hint-1 = %+v", rec)
	}
}

func hintExpires(t *testing.T, r ports.StationTripRepo) {
	trip := Hinted(t, "STN-1", "hint-1", T0)
	saved(t, r, trip, 0)
	v := trip.Version()
	if !trip.Expire(T0.Add(2 * time.Minute)) {
		t.Fatal("the hint should expire")
	}
	saved(t, r, trip, v)
	rec, err := r.GetHint(bg, "hint-1")
	must(t, err)
	if rec.Hint.State != stationtrip.StateExpired || rec.Version != 2 || rec.Hint.ConsumedTask != "" || !rec.Hint.ConsumedAt.IsZero() {
		t.Fatalf("hint-1 = %+v", rec)
	}
	ids, err := r.ExpiredActive(bg, T0.Add(time.Hour), 10)
	must(t, err)
	if len(ids) != 0 {
		t.Fatalf("an expired hint is not Active any more: %v", ids)
	}
}

func hintTerminalStays(t *testing.T, r ports.StationTripRepo) {
	trip := Hinted(t, "STN-1", "hint-1", T0)
	saved(t, r, trip, 0)
	v := trip.Version()
	trip.RecordClaim("task-a", T0.Add(2*time.Second))
	saved(t, r, trip, v)
	v = trip.Version()
	completeAndIssue(t, trip, "task-1", "hint-2", T0.Add(time.Minute))
	saved(t, r, trip, v)
	first, err := r.GetHint(bg, "hint-1")
	must(t, err)
	if first.Hint.State != stationtrip.StateConsumed || !first.SupersededAt.IsZero() || first.Version != 2 {
		t.Fatalf("a Consumed hint must stay Consumed: %+v", first)
	}
}

func hintUnchangedSave(t *testing.T, r ports.StationTripRepo) {
	trip := Hinted(t, "STN-1", "hint-1", T0)
	saved(t, r, trip, 0)
	v := trip.Version()
	Complete(t, trip, "task-1", "LOC-1", T0.Add(time.Minute)) // a completion that found no candidates
	saved(t, r, trip, v)
	rec, err := r.GetHint(bg, "hint-1")
	must(t, err)
	if rec.Hint.State != stationtrip.StateActive || rec.Version != 1 {
		t.Fatalf("hint-1 = %+v", rec)
	}
}

func hintExpiredActive(t *testing.T, r ports.StationTripRepo) {
	saved(t, r, Hinted(t, "STN-B", "hint-b", T0), 0)
	saved(t, r, Hinted(t, "STN-A", "hint-a", T0), 0)
	saved(t, r, Hinted(t, "STN-C", "hint-c", T0.Add(time.Hour)), 0)
	saved(t, r, EmptyTrip(t, "STN-D"), 0)
	due := T0.Add(time.Second + 90*time.Second)
	ids, err := r.ExpiredActive(bg, due.Add(-time.Nanosecond), 10)
	must(t, err)
	if len(ids) != 0 {
		t.Fatalf("before expiry: %v", ids)
	}
	ids, err = r.ExpiredActive(bg, due, 10)
	must(t, err)
	if want := []stationtrip.StationID{"STN-A", "STN-B"}; !equalSlice(ids, want) {
		t.Fatalf("at expiry: %v, want %v", ids, want)
	}
	ids, err = r.ExpiredActive(bg, due, 1)
	must(t, err)
	if want := []stationtrip.StationID{"STN-A"}; !equalSlice(ids, want) {
		t.Fatalf("limit: %v, want %v", ids, want)
	}
}

func hintUnknown(t *testing.T, r ports.StationTripRepo) {
	_, err := r.GetHint(bg, "hint-x")
	wantErr(t, err, repository.ErrHintNotFound)
}

func hintRecord(t *testing.T, r ports.StationTripRepo) {
	trip := Hinted(t, "STN-1", "hint-1", T0)
	saved(t, r, trip, 0)
	rec, err := r.GetHint(bg, "hint-1")
	must(t, err)
	want, _ := trip.Hint()
	if rec.StationID != "STN-1" || rec.SiteID != Site || rec.Version != 1 || !sameHint(&rec.Hint, &want) {
		t.Fatalf("record = %+v, want hint %+v", rec, want)
	}
	if len(rec.Hint.Candidates) != 2 || rec.Hint.Candidates[0].Rank != 1 || rec.Hint.Candidates[1].TaskID != recommend.TaskID("task-b") {
		t.Fatalf("candidates = %+v", rec.Hint.Candidates)
	}
}

func hintIDs(recs []repository.HintRecord) []string {
	ids := make([]string, 0, len(recs))
	for _, rec := range recs {
		ids = append(ids, string(rec.Hint.ID))
	}
	return ids
}

func seedHints(t *testing.T, r ports.StationTripRepo) {
	saved(t, r, Hinted(t, "STN-1", "hint-1", T0), 0)
	saved(t, r, Hinted(t, "STN-2", "hint-2", T0.Add(time.Minute)), 0)
	saved(t, r, Hinted(t, "STN-3", "hint-3", T0.Add(time.Minute)), 0)
}

func hintListPaging(t *testing.T, r ports.StationTripRepo) {
	seedHints(t, r)
	all, err := r.ListHints(bg, repository.HintFilter{}, "", 10)
	must(t, err)
	if want := []string{"hint-3", "hint-2", "hint-1"}; !equalSlice(hintIDs(all), want) {
		t.Fatalf("order = %v, want %v (issued-at then id, newest first)", hintIDs(all), want)
	}
	page, err := r.ListHints(bg, repository.HintFilter{}, "hint-3", 1)
	must(t, err)
	if want := []string{"hint-2"}; !equalSlice(hintIDs(page), want) {
		t.Fatalf("page = %v, want %v", hintIDs(page), want)
	}
	last, err := r.ListHints(bg, repository.HintFilter{}, "hint-2", 5)
	must(t, err)
	if want := []string{"hint-1"}; !equalSlice(hintIDs(last), want) {
		t.Fatalf("last page = %v, want %v", hintIDs(last), want)
	}
	if past, err := r.ListHints(bg, repository.HintFilter{}, "hint-1", 5); err != nil || len(past) != 0 {
		t.Fatalf("past the end = %v %v", hintIDs(past), err)
	}
	if none, err := r.ListHints(bg, repository.HintFilter{}, "hint-zz", 5); err != nil || len(none) != 0 {
		t.Fatalf("unknown cursor = %v %v", hintIDs(none), err)
	}
	if got, _ := r.ListHints(bg, repository.HintFilter{}, "", 2); len(got) != 2 {
		t.Fatalf("limit 2 returned %d", len(got))
	}
	if len(all[0].Hint.Candidates) != 2 {
		t.Fatalf("listed hints must carry their candidates: %+v", all[0].Hint)
	}
}

func hintListFilters(t *testing.T, r ports.StationTripRepo) {
	seedHints(t, r)
	count := func(f repository.HintFilter) int {
		got, err := r.ListHints(bg, f, "", 10)
		must(t, err)
		return len(got)
	}
	if n := count(repository.HintFilter{StationID: "STN-1"}); n != 1 {
		t.Fatalf("by station = %d", n)
	}
	if n := count(repository.HintFilter{SiteID: "SITE-9"}); n != 0 {
		t.Fatalf("by an unknown site = %d", n)
	}
	if n := count(repository.HintFilter{SiteID: Site, State: stationtrip.StateActive}); n != 3 {
		t.Fatalf("active at the site = %d", n)
	}
	if n := count(repository.HintFilter{State: stationtrip.StateExpired}); n != 0 {
		t.Fatalf("expired = %d", n)
	}
	if n := count(repository.HintFilter{StationID: "STN-2", SiteID: Site, State: stationtrip.StateActive}); n != 1 {
		t.Fatalf("all three filters = %d", n)
	}
}

func hintHistoryDepth(t *testing.T, r ports.StationTripRepo) {
	trip := Hinted(t, "STN-1", "hint-0", T0)
	saved(t, r, trip, 0)
	ids := []string{"hint-1", "hint-2", "hint-3", "hint-4"}
	for i, id := range ids {
		v := trip.Version()
		completeAndIssue(t, trip, "task-"+id, id, T0.Add(time.Duration(i+1)*time.Minute))
		saved(t, r, trip, v)
	}
	all, err := r.ListHints(bg, repository.HintFilter{StationID: "STN-1"}, "", 10)
	must(t, err)
	if want := []string{"hint-4", "hint-3", "hint-2", "hint-1", "hint-0"}; !equalSlice(hintIDs(all), want) {
		t.Fatalf("history = %v, want %v", hintIDs(all), want)
	}
	superseded, err := r.ListHints(bg, repository.HintFilter{StationID: "STN-1", State: stationtrip.StateSuperseded}, "", 10)
	must(t, err)
	if len(superseded) != 4 {
		t.Fatalf("superseded = %d, want 4", len(superseded))
	}
	got, err := r.Get(bg, "STN-1")
	must(t, err)
	if h, _ := got.Hint(); h.ID != "hint-4" || h.State != stationtrip.StateActive {
		t.Fatalf("current hint = %+v", h)
	}
}
