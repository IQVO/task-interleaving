package usecases_test

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/application/usecases"
	"github.com/claudioed/task-interleaving/internal/domain/policy"
	"github.com/claudioed/task-interleaving/internal/domain/recommend"
	"github.com/claudioed/task-interleaving/internal/domain/stationtrip"
)

// invalidCase is a consumed payload that must be refused as ErrInvalidEvent
// before anything is claimed.
type invalidCase struct {
	name string
	act  func(e *env) error
}

func TestInvalidPayloadsAreRefusedBeforeClaiming(t *testing.T) {
	cases := append(availabilityCases(), append(claimCases(), layoutCases()...)...)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv()
			err := c.act(e)
			if !errors.Is(err, usecases.ErrInvalidEvent) {
				t.Fatalf("err = %v, want ErrInvalidEvent", err)
			}
			claimed, _ := e.processed.Claim(bg, usecases.FulfillmentConsumer, "evt-1")
			layout, _ := e.processed.Claim(bg, usecases.LayoutConsumer, "evt-1")
			if !claimed || !layout {
				t.Fatal("an invalid payload must not claim its event id")
			}
		})
	}
}

func availability(mutate func(*usecases.TaskAvailability)) func(e *env) error {
	return func(e *env) error {
		c := usecases.TaskAvailability{TaskID: "T-9", TaskType: "PICK", SiteID: "S1", FromLocation: "A"}
		mutate(&c)
		_, err := e.available().Handle(bg, "evt-1", c)
		return err
	}
}

func availabilityCases() []invalidCase {
	return []invalidCase{
		{"available without task id", availability(func(c *usecases.TaskAvailability) { c.TaskID = " " })},
		{"available without type", availability(func(c *usecases.TaskAvailability) { c.TaskType = "" })},
		{"available without site", availability(func(c *usecases.TaskAvailability) { c.SiteID = "" })},
		{"available with a bad site", availability(func(c *usecases.TaskAvailability) { c.SiteID = "S 1" })},
		{"available without a start", availability(func(c *usecases.TaskAvailability) { c.FromLocation = "" })},
	}
}

func claim(mutate func(*usecases.TaskClaim)) func(e *env) error {
	return func(e *env) error {
		c := usecases.TaskClaim{TaskID: "T-9", TaskType: "PICK", StationID: "STN-1"}
		mutate(&c)
		_, err := e.claimed().Handle(bg, "evt-1", c)
		return err
	}
}

func claimCases() []invalidCase {
	return []invalidCase{
		{"claim without task id", claim(func(c *usecases.TaskClaim) { c.TaskID = "" })},
		{"claim without station", claim(func(c *usecases.TaskClaim) { c.StationID = "" })},
		{"claim with a bad station", claim(func(c *usecases.TaskClaim) { c.StationID = "STN/1" })},
		{"completion without task id", func(e *env) error {
			_, err := e.completed().Handle(bg, "evt-1", usecases.TaskCompletion{TaskID: " "})
			return err
		}},
	}
}

func layoutCases() []invalidCase {
	return []invalidCase{
		{"zone without id", func(e *env) error {
			_, err := (&usecases.ApplyZone{Intake: e.intake, Book: e.book}).Handle(bg, "evt-1", usecases.ZoneChange{SiteCode: "S1"})
			return err
		}},
		{"zone without site", func(e *env) error {
			_, err := (&usecases.ApplyZone{Intake: e.intake, Book: e.book}).Handle(bg, "evt-1", usecases.ZoneChange{ZoneID: "z"})
			return err
		}},
		{"location without code", func(e *env) error {
			_, err := (&usecases.ApplyLocationRegistered{Intake: e.intake, Book: e.book}).Handle(bg, "evt-1", usecases.LocationChange{ZoneID: "z"})
			return err
		}},
		{"location without zone", func(e *env) error {
			_, err := (&usecases.ApplyLocationRegistered{Intake: e.intake, Book: e.book}).Handle(bg, "evt-1", usecases.LocationChange{Code: "A"})
			return err
		}},
		{"decommission without code", func(e *env) error {
			_, err := (&usecases.ApplyLocationDecommissioned{Intake: e.intake, Book: e.book}).Handle(bg, "evt-1", " ")
			return err
		}},
		{"geometry without code", func(e *env) error {
			_, err := (&usecases.ApplyGeometryUpdated{Intake: e.intake, Book: e.book}).Handle(bg, "evt-1", usecases.GeometryChange{})
			return err
		}},
		{"geometry with NaN", geometry(math.NaN(), 0, 0)},
		{"geometry with +Inf", geometry(0, math.Inf(1), 0)},
		{"geometry with -Inf", geometry(0, 0, math.Inf(-1))},
	}
}

func geometry(x, y, z float64) func(e *env) error {
	return func(e *env) error {
		_, err := (&usecases.ApplyGeometryUpdated{Intake: e.intake, Book: e.book}).Handle(bg, "evt-1",
			usecases.GeometryChange{Code: "A", X: x, Y: y, Z: z})
		return err
	}
}

func TestAnEventWithoutAnIDIsInvalid(t *testing.T) {
	e := newEnv()
	_, err := e.available().Handle(bg, "", usecases.TaskAvailability{TaskID: "T-9", TaskType: "PICK", SiteID: "S1", FromLocation: "A"})
	if !errors.Is(err, usecases.ErrInvalidEvent) {
		t.Fatalf("err = %v", err)
	}
}

func TestClaimOfACompletedTaskIsStale(t *testing.T) {
	e := newEnv()
	e.seed(t)
	mustNil(t, e.pool.Save(bg, repository.Task{ID: "T-9", State: repository.TaskCompleted}))
	out, err := e.claimed().Handle(bg, "evt-1", usecases.TaskClaim{TaskID: "T-9", StationID: "STN-1"})
	if err != nil || out != usecases.OutcomeStale {
		t.Fatalf("Handle = %v, %v", out, err)
	}
	task, _, _ := e.pool.Get(bg, "T-9")
	if task.State != repository.TaskCompleted || task.ClaimedBy != "" {
		t.Fatalf("a stale claim changed the task: %+v", task)
	}
}

func TestClaimKeepsWhatTheTaskKnewAndUsesTheEventTime(t *testing.T) {
	e := newEnv()
	e.seedHinted(t)
	// the claim is dated before the hint was issued: it must not consume it
	out, err := e.claimed().Handle(bg, "evt-1", usecases.TaskClaim{TaskID: "T-1", StationID: "STN-1", At: t0.Add(-time.Second)})
	if err != nil || out != usecases.OutcomeApplied {
		t.Fatalf("Handle = %v, %v", out, err)
	}
	trip, _ := e.trips.Get(bg, "STN-1")
	if _, active := trip.ActiveHint(); !active {
		t.Fatal("a claim older than the hint consumed it")
	}
	task, _, _ := e.pool.Get(bg, "T-1")
	if task.Type != "PICK" || task.FromLocation != "A" || task.State != repository.TaskClaimed || task.ClaimedBy != "STN-1" {
		t.Fatalf("task = %+v", task)
	}
}

func TestACompletionWithAnUnusableTaskIsInvalid(t *testing.T) {
	e := newEnv()
	e.seed(t)
	mustNil(t, e.pool.Save(bg, repository.Task{
		ID: "T-bad", SiteID: "S 1", FromLocation: "L0", ToLocation: "L0",
		State: repository.TaskClaimed, ClaimedBy: "STN-1",
	}))
	_, err := e.completed().Handle(bg, "evt-1", usecases.TaskCompletion{TaskID: "T-bad"})
	if !errors.Is(err, usecases.ErrInvalidEvent) {
		t.Fatalf("err = %v, want ErrInvalidEvent", err)
	}
	if ok, _ := e.processed.Claim(bg, usecases.FulfillmentConsumer, "evt-1"); !ok {
		t.Fatal("the rolled back claim must be free again")
	}
}

func TestACompletionWithoutASiteIsIgnored(t *testing.T) {
	e := newEnv()
	e.seed(t)
	mustNil(t, e.pool.Save(bg, repository.Task{
		ID: "T-min", ToLocation: "L0", State: repository.TaskClaimed, ClaimedBy: "STN-1",
	}))
	out, err := e.completed().Handle(bg, "evt-1", usecases.TaskCompletion{TaskID: "T-min"})
	if err != nil || out != usecases.OutcomeIgnored {
		t.Fatalf("Handle = %v, %v", out, err)
	}
}

type badIDs struct{}

func (badIDs) NewHintID() string { return "not-a-hint-id" }

func TestABadHintIDFailsTheCompletion(t *testing.T) {
	e := newEnv()
	e.seed(t)
	uc := e.completed()
	uc.IDs = badIDs{}
	_, err := uc.Handle(bg, "evt-1", usecases.TaskCompletion{TaskID: "T0"})
	if !errors.Is(err, stationtrip.ErrInvalidHintID) {
		t.Fatalf("err = %v, want ErrInvalidHintID", err)
	}
	if _, err := e.trips.Get(bg, "STN-1"); !errors.Is(err, repository.ErrStationNotFound) {
		t.Fatalf("a failed completion must roll back the position: %v", err)
	}
}

func TestEveryWriteRollsBackWithItsClaim(t *testing.T) {
	e := newEnv()
	e.seed(t)
	e.fail("StationTripRepo.Save")
	if _, err := e.completed().Handle(bg, "evt-1", usecases.TaskCompletion{TaskID: "T0"}); !errors.Is(err, errBoom) {
		t.Fatalf("err = %v", err)
	}
	delete(e.faults.always, "StationTripRepo.Save")
	out, err := e.completed().Handle(bg, "evt-1", usecases.TaskCompletion{TaskID: "T0"})
	if err != nil || out != usecases.OutcomeApplied {
		t.Fatalf("redelivery = %v, %v: the failed attempt must not leave the id claimed or the task completed", out, err)
	}
}

func TestDeclarePolicyDefaultsAndValidation(t *testing.T) {
	e := newEnv()
	if _, err := e.declare().Handle(bg, declareIn("S 2", nil)); !errors.Is(err, policy.ErrInvalidSiteID) {
		t.Fatalf("bad site: %v", err)
	}
	if _, err := e.declare().Handle(bg, declareIn("S2", one())); !errors.Is(err, usecases.ErrVersionMismatch) {
		t.Fatalf("version for a missing policy: %v", err)
	}
	rec, err := e.declare().Handle(bg, declareIn("S2", nil))
	if err != nil || rec.Policy.VerticalWeight() != usecases.DefaultVerticalWeight || !rec.DeclaredAt.Equal(t0) {
		t.Fatalf("rec = %+v, %v", rec, err)
	}
	weight := 2.5
	in := declareIn("S2", one())
	in.VerticalWeight = &weight
	rec, err = e.declare().Handle(bg, in)
	if err != nil || rec.Policy.VerticalWeight() != 2.5 || rec.Policy.Version() != 2 {
		t.Fatalf("rec = %+v, %v", rec, err)
	}
	if got := e.log.Declarations(); len(got) != 2 || got[1].VerticalWeight != 2.5 || got[1].HintTTLSeconds != 60 {
		t.Fatalf("declarations = %+v", got)
	}
}

func TestReadsValidateTheirInput(t *testing.T) {
	e := newEnv()
	e.seedHinted(t)
	if _, err := (&usecases.GetPolicy{Policies: e.policies}).Handle(bg, "S 1"); !errors.Is(err, policy.ErrInvalidSiteID) {
		t.Fatalf("policy: %v", err)
	}
	if _, err := (&usecases.GetPolicy{Policies: e.policies}).Handle(bg, "S9"); !errors.Is(err, repository.ErrPolicyNotFound) {
		t.Fatalf("policy: %v", err)
	}
	if _, err := (&usecases.GetStation{Trips: e.trips}).Handle(bg, "STN 1"); !errors.Is(err, stationtrip.ErrInvalidStationID) {
		t.Fatalf("station: %v", err)
	}
	if _, err := (&usecases.GetStation{Trips: e.trips}).Handle(bg, "STN-9"); !errors.Is(err, repository.ErrStationNotFound) {
		t.Fatalf("station: %v", err)
	}
	if _, err := (&usecases.GetHint{Trips: e.trips}).Handle(bg, "nope"); !errors.Is(err, stationtrip.ErrInvalidHintID) {
		t.Fatalf("hint: %v", err)
	}
	if _, err := (&usecases.GetHint{Trips: e.trips}).Handle(bg, "hint-zz"); !errors.Is(err, repository.ErrHintNotFound) {
		t.Fatalf("hint: %v", err)
	}
	rec, err := (&usecases.GetHint{Trips: e.trips}).Handle(bg, "hint-1")
	if err != nil || rec.StationID != "STN-1" || rec.SiteID != "S1" {
		t.Fatalf("hint = %+v, %v", rec, err)
	}
	trip, err := (&usecases.GetStation{Trips: e.trips}).Handle(bg, "STN-1")
	if err != nil || trip.ID() != "STN-1" {
		t.Fatalf("station = %v, %v", trip, err)
	}
}

func TestRecommendValidatesTheStation(t *testing.T) {
	e := newEnv()
	e.seed(t)
	if _, err := e.recommend().Handle(bg, usecases.RecommendInput{StationID: " "}); !errors.Is(err, stationtrip.ErrInvalidStationID) {
		t.Fatalf("err = %v", err)
	}
}

func TestListHintsValidatesAndPages(t *testing.T) {
	e := newEnv()
	e.seedHinted(t)
	for name, q := range map[string]usecases.ListHintsQuery{
		"station": {StationID: "a b"}, "site": {SiteID: "a b"}, "state": {State: "Open"},
		"limit": {Limit: -1}, "big limit": {Limit: usecases.MaxListLimit + 1}, "cursor": {Cursor: "***"},
	} {
		if _, err := (&usecases.ListHints{Trips: e.trips}).Handle(bg, q); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	list := &usecases.ListHints{Trips: e.trips}
	page, err := list.Handle(bg, usecases.ListHintsQuery{StationID: "STN-1", SiteID: "S1", State: "Active"})
	if err != nil || len(page.Items) != 1 || page.NextCursor != "" {
		t.Fatalf("page = %+v, %v", page, err)
	}
}

func TestListHintsPagesNewestFirst(t *testing.T) {
	e := newEnv()
	e.seed(t)
	seedStations(t, e, 3)
	list := &usecases.ListHints{Trips: e.trips}
	first, err := list.Handle(bg, usecases.ListHintsQuery{Limit: 2})
	if err != nil || len(first.Items) != 2 || first.NextCursor == "" {
		t.Fatalf("first = %+v, %v", first, err)
	}
	second, err := list.Handle(bg, usecases.ListHintsQuery{Limit: 2, Cursor: first.NextCursor})
	if err != nil || len(second.Items) != 1 || second.NextCursor != "" {
		t.Fatalf("second = %+v, %v", second, err)
	}
	seen := map[stationtrip.HintID]bool{}
	for _, rec := range append(first.Items, second.Items...) {
		seen[rec.Hint.ID] = true
	}
	if len(seen) != 3 {
		t.Fatalf("pages overlap or miss hints: %v", seen)
	}
}

// seedStations completes one task for each of n stations, one second apart, so
// every station gets a hint.
func seedStations(t *testing.T, e *env, n int) {
	t.Helper()
	for i := range n {
		station := "STN-" + string(rune('A'+i))
		id := "T-s" + string(rune('A'+i))
		mustNil(t, e.pool.Save(bg, repositoryTask(id, station)))
		uc := e.completed()
		uc.Clock = stepClock{i}
		uc.IDs = &seqIDs{n: i}
		if _, err := uc.Handle(bg, "evt-"+id, usecases.TaskCompletion{TaskID: id}); err != nil {
			t.Fatal(err)
		}
	}
}

type stepClock struct{ i int }

func (c stepClock) Now() time.Time { return t0.Add(time.Duration(c.i) * time.Second) }

func TestCursorRoundTrip(t *testing.T) {
	c := usecases.EncodeCursor("hint-1")
	got, err := usecases.DecodeCursor(c)
	if err != nil || got != "hint-1" {
		t.Fatalf("got %q, %v", got, err)
	}
	if got, err := usecases.DecodeCursor(""); got != "" || err != nil {
		t.Fatalf("first page: %q, %v", got, err)
	}
	for _, bad := range []string{"***", "=", strings.Repeat("!", 3)} {
		if _, err := usecases.DecodeCursor(bad); !errors.Is(err, usecases.ErrInvalidCursor) {
			t.Fatalf("%q: %v", bad, err)
		}
	}
}

func TestOnlyEligibleAvailableTasksOfTheSiteAreRanked(t *testing.T) {
	e := newEnv()
	e.seed(t)
	mustNil(t, e.pool.Save(bg, repository.Task{ID: "T-x", Type: "PACK", SiteID: "S1", FromLocation: "A", State: repository.TaskAvailable}))
	trip := completedTrip(t)
	mustNil(t, e.trips.Save(bg, trip, 0))
	got, err := e.recommend().Handle(bg, usecases.RecommendInput{StationID: "STN-1"})
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]recommend.TaskID, 0, len(got.Candidates))
	for _, c := range got.Candidates {
		ids = append(ids, c.TaskID)
	}
	if len(ids) != 2 || ids[0] != "T-1" || ids[1] != "T-2" {
		t.Fatalf("candidates = %v", ids)
	}
}
