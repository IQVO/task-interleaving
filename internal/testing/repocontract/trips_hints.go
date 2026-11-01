package repocontract

import (
	"testing"
	"time"

	"github.com/claudioed/task-interleaving/internal/application/ports"
	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/domain/stationtrip"
)

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
