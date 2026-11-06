//go:build load

package recommend_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/claudioed/task-interleaving/internal/domain/policy"
	"github.com/claudioed/task-interleaving/internal/domain/recommend"
	"github.com/claudioed/task-interleaving/internal/domain/travel"
)

// The recommender at PICK volume (ADR 0012). PICK dominates task count once
// fulfillment-execution announces it, so a site's open-task pool grows from
// the handful of RELOCATE/REPLENISH tasks of today's fixtures to the release
// policy's WIP ceiling (1 000 open units) with headroom beyond it. This is a
// tagged performance suite (`make load`), deliberately outside the default
// suite and CI: it needs no infrastructure — the recommender is pure — but a
// 10 000-task pool does not belong in every `make check`. The bounds are
// generous walls against complexity blow-ups, not benchmarks; the measured
// numbers are logged for the ADR's findings.

// loadPool builds n open tasks of site SITE-1 on a line: task t-%06d stands at
// (i, 0, 0), so its rectilinear detour from the origin (0, 0, 0) is i metres
// and the expected ranking is the id order. Every task is PICK except a
// sprinkling of RELOCATE and REPLENISH, as a real mixed pool will be.
func loadPool(n int) []recommend.OpenTask {
	pool := make([]recommend.OpenTask, 0, n)
	for i := 1; i <= n; i++ {
		typ := policy.TaskPick
		switch {
		case i%89 == 0:
			typ = policy.TaskReplenish
		case i%97 == 0:
			typ = policy.TaskRelocate
		}
		pool = append(pool, recommend.OpenTask{
			ID: recommend.TaskID(fmt.Sprintf("t-%06d", i)), Type: typ, Site: site,
			FromLocation: fmt.Sprintf("LOC-%06d", i), FromPoint: pt(float64(i), 0, 0),
			ToLocation: fmt.Sprintf("END-%06d", i),
		})
	}
	return pool
}

func loadPolicy(t *testing.T, maxCandidates int) policy.Policy {
	t.Helper()
	p, err := policy.New(site, policy.Settings{
		MaxDetourM: 20000, MaxCandidates: maxCandidates, HintTTL: 90 * time.Second,
		EligibleTaskTypes: []policy.TaskType{policy.TaskPick, policy.TaskRelocate, policy.TaskReplenish},
		VerticalWeight:    1,
	})
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	return p
}

// assertTopFive checks the ranked head of the pool: the five nearest tasks in
// id order, none dropped, none reordered — correctness does not degrade with
// pool size.
func assertTopFive(t *testing.T, got []recommend.Candidate) {
	t.Helper()
	if len(got) != 5 {
		t.Fatalf("%d candidates, want the policy bound of 5: %+v", len(got), got)
	}
	for i, c := range got {
		want := fmt.Sprintf("t-%06d", i+1)
		if c.Rank != i+1 || string(c.TaskID) != want || c.DetourM != float64(i+1) {
			t.Fatalf("candidate %d = %+v, want rank %d %s at %d m", i, c, i+1, want, i+1)
		}
	}
}

func TestLoadRecommendPickVolumePool(t *testing.T) {
	for _, n := range []int{1000, 10000} {
		t.Run(fmt.Sprintf("pool_of_%d_open_tasks", n), func(t *testing.T) {
			pool := loadPool(n)
			p := loadPolicy(t, 5)
			in := recommend.Input{
				Site: site, Origin: travel.Place{Location: "ORIGIN", Point: pt(0, 0, 0)},
				Pool: pool, Policy: p, Completed: "t-none",
				Model: recommend.RectilinearFor(p),
			}

			start := time.Now()
			got, err := recommend.Recommend(in)
			if err != nil {
				t.Fatalf("Recommend: %v", err)
			}
			first := time.Since(start)
			assertTopFive(t, got)

			const iterations = 50
			start = time.Now()
			for i := 0; i < iterations; i++ {
				if _, err := recommend.Recommend(in); err != nil {
					t.Fatalf("Recommend: %v", err)
				}
			}
			perCall := time.Since(start) / iterations
			t.Logf("Recommend over %d open tasks (mostly PICK): first call %v, %v per call over %d calls", n, first, perCall, iterations)

			wall := map[int]time.Duration{1000: 50 * time.Millisecond, 10000: 500 * time.Millisecond}[n]
			if perCall > wall {
				t.Fatalf("Recommend over %d tasks takes %v per call, beyond the %v wall", n, perCall, wall)
			}
		})
	}
}

// TestLoadRecommendCutIsStableAtVolume pins the cut itself at volume: with the
// pick-batching default maximum of 4 companions, a 10 000-task pool still
// yields exactly four, the four nearest — the cut prunes by rank, never by an
// internal limit below the policy bound.
func TestLoadRecommendCutIsStableAtVolume(t *testing.T) {
	pool := loadPool(10000)
	p := loadPolicy(t, 4)
	got := run(t, recommend.Input{
		Site: site, Origin: travel.Place{Location: "ORIGIN", Point: pt(0, 0, 0)},
		Pool: pool, Policy: p, Completed: "t-none", Model: recommend.RectilinearFor(p),
	})
	if len(got) != 4 {
		t.Fatalf("%d candidates, want exactly the batching default of 4: %+v", len(got), got)
	}
	for i, c := range got {
		if string(c.TaskID) != fmt.Sprintf("t-%06d", i+1) {
			t.Fatalf("candidate %d = %+v, want %s", i, c, fmt.Sprintf("t-%06d", i+1))
		}
	}
}
