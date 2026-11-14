//go:build load

package usecases_test

import (
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/application/usecases"
	"github.com/claudioed/task-interleaving/internal/domain/policy"
)

// The full hint-issuing path at PICK volume (ADR 0012): ApplyTaskCompleted is
// what runs on EVERY task completion, and once fulfillment-execution announces
// PICK tasks the pool it ranks is the release policy's WIP ceiling (1 000 open
// units) instead of a handful of RELOCATE/REPLENISH tasks. Tagged performance
// suite (`make load`), outside the default suite and CI, over the in-memory
// test doubles: the Postgres read is a plain indexed scan (no LIMIT), so the
// in-memory scan bounds the use-case cost above. The bound is a generous wall
// against complexity blow-ups; the measured number is logged for the ADR.

func TestLoadCompletionRanksAPickSizedPool(t *testing.T) {
	const poolSize = 1000 // wes-work-planning's release WIP ceiling
	e := newEnv()

	// The layout: L0 is where STN-1 completes; every pool task stands on its
	// own bin of a 50-column face, as PICK tasks do.
	mustNil(t, e.book.SaveZone(bg, repository.Zone{ID: "z1", SiteCode: "S1", ZoneCode: "FWD"}))
	e.book.SaveLocation(bg, "L0", "z1")
	e.book.SaveGeometry(bg, "L0", 0, 0, 0)

	type placed struct {
		id   string
		x, y float64
	}
	face := make([]placed, 0, poolSize)
	for i := 1; i <= poolSize; i++ {
		loc := fmt.Sprintf("BIN-%06d", i)
		x, y := float64(i%50), float64(i/50)
		mustNil(t, e.book.SaveLocation(bg, loc, "z1"), e.book.SaveGeometry(bg, loc, x, y, 0))
		typ := "PICK"
		if i%89 == 0 {
			typ = "REPLENISH"
		} else if i%97 == 0 {
			typ = "RELOCATE"
		}
		id := fmt.Sprintf("t-%06d", i)
		mustNil(t, e.pool.Save(bg, repository.Task{
			ID: id, Type: typ, SiteID: "S1", FromLocation: loc, ToLocation: loc,
			State: repository.TaskAvailable,
		}))
		face = append(face, placed{id, x, y})
	}
	mustNil(t, e.pool.Save(bg, repository.Task{
		ID: "T0", Type: "PICK", SiteID: "S1", FromLocation: "L0", ToLocation: "L0",
		State: repository.TaskClaimed, ClaimedBy: "STN-1",
	}))
	p, err := policy.New("S1", policy.Settings{
		MaxDetourM: 200, MaxCandidates: 5, HintTTL: 90 * time.Second,
		EligibleTaskTypes: []policy.TaskType{policy.TaskPick, policy.TaskRelocate, policy.TaskReplenish},
		VerticalWeight:    1,
	})
	mustNil(t, err)
	mustNil(t, e.policies.Save(bg, repository.PolicyRecord{Policy: p, DeclaredAt: t0}, 0))

	// The five nearest bins by rectilinear distance, then id — the same
	// arithmetic the rectilinear-v1 model applies, restated for the check.
	expected := append([]placed(nil), face...)
	sort.Slice(expected, func(i, j int) bool {
		di, dj := expected[i].x+expected[i].y, expected[j].x+expected[j].y
		if di != dj {
			return di < dj
		}
		return expected[i].id < expected[j].id
	})
	wantTop := expected[:5]

	start := time.Now()
	out, err := e.completed().Handle(bg, "evt-load", usecases.TaskCompletion{TaskID: "T0"})
	if err != nil || out != usecases.OutcomeApplied {
		t.Fatalf("completion = %v %v, want applied", out, err)
	}
	elapsed := time.Since(start)
	t.Logf("ApplyTaskCompleted over a %d-task pool (mostly PICK): %v end to end", poolSize, elapsed)

	trip, err := e.trips.Get(bg, "STN-1")
	if err != nil {
		t.Fatalf("load STN-1's trip: %v", err)
	}
	h, ok := trip.ActiveHint()
	if !ok {
		t.Fatal("no Active hint was issued")
	}
	if len(h.Candidates) != len(wantTop) {
		t.Fatalf("hint holds %d candidates, want %d: %+v", len(h.Candidates), len(wantTop), h.Candidates)
	}
	for i, c := range h.Candidates {
		if c.Rank != i+1 || string(c.TaskID) != wantTop[i].id {
			t.Fatalf("candidate %d = %+v, want rank %d %s", i, c, i+1, wantTop[i].id)
		}
	}
	if elapsed > 5*time.Second {
		t.Fatalf("ApplyTaskCompleted over %d tasks took %v, beyond the 5s wall", poolSize, elapsed)
	}
}
