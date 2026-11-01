package repocontract

import (
	"testing"
	"time"

	"github.com/claudioed/task-interleaving/internal/application/ports"
	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/domain/recommend"
	"github.com/claudioed/task-interleaving/internal/domain/stationtrip"
)

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

// hintIDClash: a hint id belongs to one station for good. A save that brings
// another station's id is a lost race (ids are unique per hint), and the save
// leaves nothing behind: not the trip update, not the supersede.
func hintIDClash(t *testing.T, r ports.StationTripRepo) {
	saved(t, r, Hinted(t, "STN-1", "hint-1", T0), 0)
	other := Hinted(t, "STN-2", "hint-2", T0)
	saved(t, r, other, 0)
	loaded := other.Version()
	completeAndIssue(t, other, "task-1", "hint-1", T0.Add(time.Minute))
	wantErr(t, r.Save(bg, other, loaded), repository.ErrConcurrentModification)
	got, err := r.Get(bg, "STN-2")
	must(t, err)
	if h, _ := got.Hint(); got.Version() != loaded || h.ID != "hint-2" || h.State != stationtrip.StateActive {
		t.Fatalf("the failed save left a trace: version %d, hint %+v", got.Version(), h)
	}
	rec, err := r.GetHint(bg, "hint-1")
	must(t, err)
	if rec.StationID != "STN-1" || rec.Hint.State != stationtrip.StateActive || rec.Version != 1 {
		t.Fatalf("hint-1 = %+v", rec)
	}
}
