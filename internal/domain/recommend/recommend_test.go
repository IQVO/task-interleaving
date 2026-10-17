package recommend_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/claudioed/task-interleaving/internal/domain/policy"
	"github.com/claudioed/task-interleaving/internal/domain/recommend"
	"github.com/claudioed/task-interleaving/internal/domain/travel"
)

const site policy.SiteID = "SITE-1"

func testPolicy(t *testing.T, maxDetour float64, maxCandidates int, weight float64) policy.Policy {
	t.Helper()
	p, err := policy.New(site, policy.Settings{
		MaxDetourM:        maxDetour,
		MaxCandidates:     maxCandidates,
		HintTTL:           time.Minute,
		EligibleTaskTypes: []policy.TaskType{policy.TaskPick, policy.TaskReplenish},
		VerticalWeight:    weight,
	})
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	return p
}

func pt(x, y, z float64) *travel.Point { return &travel.Point{X: x, Y: y, Z: z} }

func origin() travel.Place {
	return travel.Place{Location: "ORIGIN", Point: pt(10, 10, 1)}
}

func task(id string, typ policy.TaskType, x, y, z float64) recommend.OpenTask {
	return recommend.OpenTask{
		ID: recommend.TaskID(id), Type: typ, Site: site,
		FromLocation: "LOC-" + id, FromPoint: pt(x, y, z), ToLocation: "END-" + id,
	}
}

func input(t *testing.T, pool ...recommend.OpenTask) recommend.Input {
	t.Helper()
	p := testPolicy(t, 20, 3, 2)
	return recommend.Input{
		Site: site, Origin: origin(), Pool: pool, Policy: p,
		Completed: "done", Model: recommend.RectilinearFor(p),
	}
}

func ids(cs []recommend.Candidate) []recommend.TaskID {
	out := make([]recommend.TaskID, len(cs))
	for i, c := range cs {
		out[i] = c.TaskID
	}
	return out
}

func run(t *testing.T, in recommend.Input) []recommend.Candidate {
	t.Helper()
	got, err := recommend.Recommend(in)
	if err != nil {
		t.Fatalf("Recommend: %v", err)
	}
	return got
}

func TestRecommendReturnsTheFullCandidate(t *testing.T) {
	// dx 3, dy 4, dz 1 with weight 2 -> 3 + 4 + 2 = 9
	got := run(t, input(t, task("t1", policy.TaskReplenish, 13, 14, 2)))
	want := []recommend.Candidate{{Rank: 1, TaskID: "t1", TaskType: policy.TaskReplenish, Location: "LOC-t1", DetourM: 9}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestRecommendRanksByDetourThenTaskID(t *testing.T) {
	got := run(t, input(t,
		task("c", policy.TaskPick, 15, 10, 1), // 5
		task("b", policy.TaskPick, 10, 14, 1), // 4
		task("z", policy.TaskPick, 12, 10, 1), // 2
		task("a", policy.TaskPick, 10, 15, 1), // 5 (tie with c: a before c)
	))
	// MaxCandidates is 3: z(2), b(4), a(5); c(5) is cut by the tie-break.
	want := []recommend.TaskID{"z", "b", "a"}
	if !reflect.DeepEqual(ids(got), want) {
		t.Fatalf("got %v want %v", ids(got), want)
	}
	for i, c := range got {
		if c.Rank != i+1 {
			t.Fatalf("rank %d at index %d", c.Rank, i)
		}
	}
}

func TestRecommendTieBreakIsTheTaskIDNotThePoolOrder(t *testing.T) {
	a := task("a", policy.TaskPick, 15, 10, 1)
	b := task("b", policy.TaskPick, 10, 15, 1)
	for _, pool := range [][]recommend.OpenTask{{a, b}, {b, a}} {
		got := run(t, input(t, pool...))
		if !reflect.DeepEqual(ids(got), []recommend.TaskID{"a", "b"}) {
			t.Fatalf("got %v", ids(got))
		}
	}
}

func TestRecommendCutsToMaxCandidates(t *testing.T) {
	pool := []recommend.OpenTask{
		task("t1", policy.TaskPick, 11, 10, 1),
		task("t2", policy.TaskPick, 12, 10, 1),
		task("t3", policy.TaskPick, 13, 10, 1),
		task("t4", policy.TaskPick, 14, 10, 1),
	}
	in := input(t, pool...)
	if got := run(t, in); len(got) != 3 {
		t.Fatalf("max 3: got %d", len(got))
	}
	in.Policy = testPolicy(t, 20, 1, 2)
	got := run(t, in)
	if len(got) != 1 || got[0].TaskID != "t1" || got[0].Rank != 1 {
		t.Fatalf("max 1: got %+v", got)
	}
	in.Policy = testPolicy(t, 20, 4, 2)
	if got := run(t, in); len(got) != 4 {
		t.Fatalf("exactly max: got %d", len(got))
	}
}

func TestRecommendDetourBoundary(t *testing.T) {
	// Origin (10,10,1); max detour 20.
	atLimit := task("limit", policy.TaskPick, 30, 10, 1)     // 20.0
	justOver := task("over", policy.TaskPick, 30.1, 10, 1)   // 20.1
	justUnder := task("under", policy.TaskPick, 29.9, 10, 1) // 19.9
	got := run(t, input(t, atLimit, justOver, justUnder))
	if !reflect.DeepEqual(ids(got), []recommend.TaskID{"under", "limit"}) {
		t.Fatalf("got %v", ids(got))
	}
}

func TestRecommendRulesExcludeTasks(t *testing.T) {
	other := task("other-site", policy.TaskPick, 11, 10, 1)
	other.Site = "SITE-2"
	ineligible := task("pack", policy.TaskPack, 11, 10, 1)
	completed := task("done", policy.TaskPick, 11, 10, 1)
	unknownGeometry := task("nogeo", policy.TaskPick, 11, 10, 1)
	unknownGeometry.FromPoint = nil
	eligible := task("ok", policy.TaskPick, 12, 10, 1)
	got := run(t, input(t, other, ineligible, completed, unknownGeometry, eligible))
	if !reflect.DeepEqual(ids(got), []recommend.TaskID{"ok"}) {
		t.Fatalf("got %v", ids(got))
	}
}

func TestRecommendEachRuleOnItsOwn(t *testing.T) {
	cases := map[string]func(*recommend.OpenTask){
		"other site":      func(o *recommend.OpenTask) { o.Site = "SITE-2" },
		"ineligible type": func(o *recommend.OpenTask) { o.Type = policy.TaskSlam },
		"completed task":  func(o *recommend.OpenTask) { o.ID = "done" },
		"no geometry":     func(o *recommend.OpenTask) { o.FromPoint = nil },
		"too far":         func(o *recommend.OpenTask) { o.FromPoint = pt(100, 10, 1) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			bad := task("x", policy.TaskPick, 11, 10, 1)
			mutate(&bad)
			if got := run(t, input(t, bad)); len(got) != 0 {
				t.Fatalf("want no candidate, got %+v", got)
			}
		})
	}
}

func TestRecommendUnknownGeometryIsNotZeroDistance(t *testing.T) {
	// An unknown point must not be ranked first as if it were at distance 0.
	unknown := task("a-unknown", policy.TaskPick, 0, 0, 0)
	unknown.FromPoint = nil
	known := task("z-known", policy.TaskPick, 15, 10, 1)
	got := run(t, input(t, unknown, known))
	if !reflect.DeepEqual(ids(got), []recommend.TaskID{"z-known"}) {
		t.Fatalf("got %v", ids(got))
	}
}

func TestRecommendEmptyResultMeansNoHint(t *testing.T) {
	got, err := recommend.Recommend(input(t))
	if err != nil || len(got) != 0 {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestRecommendOriginMustBeKnown(t *testing.T) {
	in := input(t, task("t1", policy.TaskPick, 11, 10, 1))
	in.Origin = travel.Place{Location: "ORIGIN"}
	if _, err := recommend.Recommend(in); !errors.Is(err, recommend.ErrOriginUnknown) {
		t.Fatalf("want ErrOriginUnknown, got %v", err)
	}
}

func TestRecommendRequiresAModel(t *testing.T) {
	in := input(t, task("t1", policy.TaskPick, 11, 10, 1))
	in.Model = nil
	if _, err := recommend.Recommend(in); !errors.Is(err, recommend.ErrNoModel) {
		t.Fatalf("want ErrNoModel, got %v", err)
	}
}

func TestRecommendDeduplicatesTaskIDsKeepingTheSmallestDetour(t *testing.T) {
	far := task("dup", policy.TaskPick, 18, 10, 1)  // 8
	near := task("dup", policy.TaskPick, 12, 10, 1) // 2
	other := task("other", policy.TaskPick, 14, 10, 1)
	got := run(t, input(t, far, near, other))
	if len(got) != 2 || got[0].TaskID != "dup" || got[0].DetourM != 2 || got[1].TaskID != "other" {
		t.Fatalf("got %+v", got)
	}
}

func TestRecommendIsIndependentOfPoolOrder(t *testing.T) {
	pool := []recommend.OpenTask{
		task("t1", policy.TaskPick, 14, 10, 1),
		task("t2", policy.TaskReplenish, 10, 13, 1),
		task("t3", policy.TaskPick, 12, 12, 1),
		task("t4", policy.TaskPick, 16, 10, 1),
		task("t5", policy.TaskPick, 15, 11, 1),
	}
	want := run(t, input(t, pool...))
	if len(want) != 3 {
		t.Fatalf("fixture should give 3 candidates, got %d", len(want))
	}
	permutations(len(pool), func(idx []int) {
		shuffled := make([]recommend.OpenTask, len(pool))
		for i, j := range idx {
			shuffled[i] = pool[j]
		}
		if got := run(t, input(t, shuffled...)); !reflect.DeepEqual(got, want) {
			t.Fatalf("order %v changed the result: %+v vs %+v", idx, got, want)
		}
	})
}

// permutations calls f with every permutation of 0..n-1 (Heap's algorithm).
func permutations(n int, f func([]int)) {
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	var heap func(k int)
	heap = func(k int) {
		if k == 1 {
			f(idx)
			return
		}
		for i := 0; i < k; i++ {
			heap(k - 1)
			j := 0
			if k%2 == 0 {
				j = i
			}
			idx[j], idx[k-1] = idx[k-1], idx[j]
		}
	}
	heap(n)
}

func TestRecommendUsesThePolicyVerticalWeight(t *testing.T) {
	// Same x/y, only height differs: weight 0 makes both equal (tie -> id), weight 3 separates them.
	high := task("a-high", policy.TaskPick, 12, 10, 4) // dz 3
	low := task("b-low", policy.TaskPick, 12, 10, 1)   // dz 0
	in := input(t, high, low)
	in.Policy = testPolicy(t, 20, 3, 0)
	in.Model = recommend.RectilinearFor(in.Policy)
	got := run(t, in)
	if got[0].TaskID != "a-high" || got[0].DetourM != 2 || got[1].DetourM != 2 {
		t.Fatalf("weight 0: %+v", got)
	}
	in.Policy = testPolicy(t, 20, 3, 3)
	in.Model = recommend.RectilinearFor(in.Policy)
	got = run(t, in)
	if got[0].TaskID != "b-low" || got[1].TaskID != "a-high" || got[1].DetourM != 11 {
		t.Fatalf("weight 3: %+v", got)
	}
}

type constantModel struct{ d float64 }

func (constantModel) Name() string { return "constant" }
func (c constantModel) Distance(_, _ travel.Place) (float64, bool) {
	return c.d, true
}

func TestRecommendUsesTheModelItIsGiven(t *testing.T) {
	in := input(t, task("far", policy.TaskPick, 900, 900, 1))
	in.Model = constantModel{d: 7.5}
	got := run(t, in)
	if len(got) != 1 || got[0].DetourM != 7.5 {
		t.Fatalf("got %+v", got)
	}
}

func TestRectilinearForUsesThePolicyWeight(t *testing.T) {
	p := testPolicy(t, 20, 3, 4)
	d, ok := recommend.RectilinearFor(p).Distance(
		travel.Place{Location: "A", Point: pt(0, 0, 0)},
		travel.Place{Location: "B", Point: pt(0, 0, 2)},
	)
	if !ok || d != 8 {
		t.Fatalf("got %v %v want 8", d, ok)
	}
	if recommend.RectilinearFor(p).Name() != travel.RectilinearV1 {
		t.Fatal("v1 model expected")
	}
}

func TestRecommendDoesNotMutateItsInput(t *testing.T) {
	pool := []recommend.OpenTask{
		task("b", policy.TaskPick, 15, 10, 1),
		task("a", policy.TaskPick, 12, 10, 1),
	}
	snapshot := append([]recommend.OpenTask(nil), pool...)
	run(t, input(t, pool...))
	if !reflect.DeepEqual(pool, snapshot) {
		t.Fatal("the pool slice was reordered or modified")
	}
}
