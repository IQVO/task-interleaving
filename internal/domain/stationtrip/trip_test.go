package stationtrip_test

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/task-interleaving/internal/domain/policy"
	"github.com/claudioed/task-interleaving/internal/domain/recommend"
	"github.com/claudioed/task-interleaving/internal/domain/stationtrip"
	"github.com/claudioed/task-interleaving/internal/domain/travel"
)

const (
	siteID    policy.SiteID         = "SITE-1"
	stationID stationtrip.StationID = "STN-07"
	hint1     stationtrip.HintID    = "hint-1"
	hint2     stationtrip.HintID    = "hint-2"
)

var t0 = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

func testPolicy(t *testing.T) policy.Policy {
	t.Helper()
	p, err := policy.New(siteID, policy.Settings{
		MaxDetourM:        25,
		MaxCandidates:     3,
		HintTTL:           90 * time.Second,
		EligibleTaskTypes: []policy.TaskType{policy.TaskPick, policy.TaskReplenish},
		VerticalWeight:    1.5,
	})
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	return p
}

func candidates() []recommend.Candidate {
	return []recommend.Candidate{
		{Rank: 1, TaskID: "task-a", TaskType: policy.TaskPick, Location: "LOC-A", DetourM: 4.5},
		{Rank: 2, TaskID: "task-b", TaskType: policy.TaskReplenish, Location: "LOC-B", DetourM: 12},
		{Rank: 3, TaskID: "task-c", TaskType: policy.TaskPick, Location: "LOC-C", DetourM: 20},
	}
}

func position() stationtrip.Position {
	return stationtrip.Position{
		Site:  siteID,
		Place: travel.Place{Location: "WH1-FWD-A01-01-01-A", Point: &travel.Point{X: 4.2, Y: 10.5, Z: 0.4}},
	}
}

func newTrip(t *testing.T) *stationtrip.StationTrip {
	t.Helper()
	s, err := stationtrip.New(stationID)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// completedTrip is a trip that recorded task-8a1f at t0 (version 1).
func completedTrip(t *testing.T) *stationtrip.StationTrip {
	t.Helper()
	s := newTrip(t)
	applied, err := s.RecordCompletion("task-8a1f", t0, position())
	if err != nil || !applied {
		t.Fatalf("RecordCompletion: %v %v", applied, err)
	}
	return s
}

// hintedTrip has an Active hint1 issued at t0+1s (version 2); events are cleared.
func hintedTrip(t *testing.T) *stationtrip.StationTrip {
	t.Helper()
	s := completedTrip(t)
	if err := s.IssueHint(hint1, candidates(), testPolicy(t), t0.Add(time.Second)); err != nil {
		t.Fatalf("IssueHint: %v", err)
	}
	s.ClearDomainEvents()
	return s
}

func issuedAt() time.Time { return t0.Add(time.Second) }

func expiresAt() time.Time { return issuedAt().Add(90 * time.Second) }

func TestNewStationID(t *testing.T) {
	cases := []struct {
		in string
		ok bool
	}{
		{"STN-07", true}, {strings.Repeat("s", 64), true}, {"", false},
		{strings.Repeat("s", 65), false}, {"STN 07", false}, {"STN/07", false}, {"STN\x0107", false},
	}
	for _, tc := range cases {
		got, err := stationtrip.NewStationID(tc.in)
		if tc.ok && (err != nil || string(got) != tc.in) {
			t.Errorf("%q: want ok, got %q %v", tc.in, got, err)
		}
		if !tc.ok && !errors.Is(err, stationtrip.ErrInvalidStationID) {
			t.Errorf("%q: want ErrInvalidStationID, got %v", tc.in, err)
		}
	}
}

func TestNewHintID(t *testing.T) {
	cases := []struct {
		in string
		ok bool
	}{
		{"hint-1", true}, {"hint-" + strings.Repeat("a", 59), true}, {"hint-" + strings.Repeat("a", 60), false},
		{"hint-", false}, {"", false}, {"hin-1", false}, {"Hint-1", false}, {"hint-a b", false}, {"hint-a/b", false},
	}
	for _, tc := range cases {
		got, err := stationtrip.NewHintID(tc.in)
		if tc.ok && (err != nil || string(got) != tc.in) {
			t.Errorf("%q: want ok, got %q %v", tc.in, got, err)
		}
		if !tc.ok && !errors.Is(err, stationtrip.ErrInvalidHintID) {
			t.Errorf("%q: want ErrInvalidHintID, got %v", tc.in, err)
		}
	}
}

func TestHintStates(t *testing.T) {
	active := []stationtrip.HintState{stationtrip.StateActive}
	terminal := []stationtrip.HintState{stationtrip.StateConsumed, stationtrip.StateExpired, stationtrip.StateSuperseded}
	for _, s := range active {
		if !s.Valid() || s.Terminal() {
			t.Errorf("%s: valid and not terminal expected", s)
		}
	}
	for _, s := range terminal {
		if !s.Valid() || !s.Terminal() {
			t.Errorf("%s: valid and terminal expected", s)
		}
	}
	for _, s := range []stationtrip.HintState{"", "active", "Cancelled"} {
		if s.Valid() || s.Terminal() {
			t.Errorf("%q: neither valid nor terminal expected", s)
		}
	}
}

func TestNewTripIsEmpty(t *testing.T) {
	s := newTrip(t)
	if s.ID() != stationID || s.Version() != 0 {
		t.Fatalf("id %q version %d", s.ID(), s.Version())
	}
	if _, ok := s.Position(); ok {
		t.Fatal("a new trip has no position")
	}
	if task, at := s.LastCompleted(); task != "" || !at.IsZero() {
		t.Fatalf("last completed %q %v", task, at)
	}
	if _, ok := s.Hint(); ok {
		t.Fatal("a new trip has no hint")
	}
	if _, ok := s.ActiveHint(); ok {
		t.Fatal("a new trip has no active hint")
	}
	if len(s.DomainEvents()) != 0 {
		t.Fatal("a new trip has raised nothing")
	}
}

func TestNewRejectsInvalidStation(t *testing.T) {
	if _, err := stationtrip.New("bad station"); !errors.Is(err, stationtrip.ErrInvalidStationID) {
		t.Fatalf("want ErrInvalidStationID, got %v", err)
	}
}

// --- invariant 1: the position only moves forward in time ---

func TestRecordCompletionStoresPositionTaskAndTime(t *testing.T) {
	s := completedTrip(t)
	pos, ok := s.Position()
	if !ok || pos.Site != siteID || pos.Place.Location != "WH1-FWD-A01-01-01-A" {
		t.Fatalf("position %+v %v", pos, ok)
	}
	if pos.Place.Point == nil || *pos.Place.Point != (travel.Point{X: 4.2, Y: 10.5, Z: 0.4}) {
		t.Fatalf("point %+v", pos.Place.Point)
	}
	if task, at := s.LastCompleted(); task != "task-8a1f" || !at.Equal(t0) {
		t.Fatalf("last completed %q %v", task, at)
	}
	if s.Version() != 1 {
		t.Fatalf("version %d", s.Version())
	}
	if len(s.DomainEvents()) != 0 {
		t.Fatal("a completion raises no domain event")
	}
}

func TestRecordCompletionKeepsAnUnknownPoint(t *testing.T) {
	s := newTrip(t)
	pos := stationtrip.Position{Site: siteID, Place: travel.Place{Location: "LOC-X"}}
	if applied, err := s.RecordCompletion("task-1", t0, pos); err != nil || !applied {
		t.Fatalf("%v %v", applied, err)
	}
	got, _ := s.Position()
	if got.Place.Point != nil || got.Place.Location != "LOC-X" {
		t.Fatalf("position %+v", got)
	}
}

func TestRecordCompletionNormalisesToUTC(t *testing.T) {
	s := newTrip(t)
	zone := time.FixedZone("BRT", -3*3600)
	at := time.Date(2026, 10, 17, 9, 0, 0, 0, zone)
	if _, err := s.RecordCompletion("task-1", at, position()); err != nil {
		t.Fatal(err)
	}
	_, got := s.LastCompleted()
	if !got.Equal(t0) || got.Location() != time.UTC {
		t.Fatalf("got %v in %v", got, got.Location())
	}
}

func TestRecordCompletionIgnoresAnOlderOrEqualCompletion(t *testing.T) {
	older := position()
	older.Place.Location = "OLDER"
	cases := []struct {
		name string
		at   time.Time
	}{
		{"older", t0.Add(-time.Second)},
		{"equal instant", t0},
		{"far older", t0.Add(-24 * time.Hour)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := completedTrip(t)
			applied, err := s.RecordCompletion("task-older", tc.at, older)
			if err != nil || applied {
				t.Fatalf("want ignored, got applied=%v err=%v", applied, err)
			}
			pos, _ := s.Position()
			task, at := s.LastCompleted()
			if pos.Place.Location == "OLDER" || task != "task-8a1f" || !at.Equal(t0) || s.Version() != 1 {
				t.Fatalf("an ignored completion changed state: %+v %q %v v%d", pos, task, at, s.Version())
			}
		})
	}
}

func TestRecordCompletionAcceptsAStrictlyNewerCompletion(t *testing.T) {
	s := completedTrip(t)
	next := position()
	next.Place.Location = "NEXT"
	applied, err := s.RecordCompletion("task-next", t0.Add(time.Nanosecond), next)
	if err != nil || !applied {
		t.Fatalf("want applied, got %v %v", applied, err)
	}
	pos, _ := s.Position()
	task, _ := s.LastCompleted()
	if pos.Place.Location != "NEXT" || task != "task-next" || s.Version() != 2 {
		t.Fatalf("%+v %q v%d", pos, task, s.Version())
	}
}

func TestRecordCompletionValidates(t *testing.T) {
	cases := []struct {
		name string
		task recommend.TaskID
		at   time.Time
		pos  stationtrip.Position
		want error
	}{
		{"blank task", "  ", t0, position(), stationtrip.ErrInvalidTask},
		{"empty task", "", t0, position(), stationtrip.ErrInvalidTask},
		{"zero time", "task-1", time.Time{}, position(), stationtrip.ErrInvalidTime},
		{"invalid site", "task-1", t0, stationtrip.Position{Site: "bad site", Place: travel.Place{Location: "L"}}, stationtrip.ErrInvalidPosition},
		{"empty site", "task-1", t0, stationtrip.Position{Place: travel.Place{Location: "L"}}, stationtrip.ErrInvalidPosition},
		{"blank location", "task-1", t0, stationtrip.Position{Site: siteID, Place: travel.Place{Location: "  "}}, stationtrip.ErrInvalidPosition},
		{"empty location", "task-1", t0, stationtrip.Position{Site: siteID}, stationtrip.ErrInvalidPosition},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTrip(t)
			applied, err := s.RecordCompletion(tc.task, tc.at, tc.pos)
			if applied || !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got applied=%v err=%v", tc.want, applied, err)
			}
			if s.Version() != 0 {
				t.Fatal("a refused completion must not change the version")
			}
		})
	}
}

func TestRecordCompletionValidatesBeforeIgnoring(t *testing.T) {
	s := completedTrip(t)
	if _, err := s.RecordCompletion("", t0.Add(-time.Hour), position()); !errors.Is(err, stationtrip.ErrInvalidTask) {
		t.Fatalf("an older but invalid completion is an error, got %v", err)
	}
}

func TestPositionDoesNotAliasTheCallersPoint(t *testing.T) {
	pos := position()
	s := newTrip(t)
	if _, err := s.RecordCompletion("task-1", t0, pos); err != nil {
		t.Fatal(err)
	}
	pos.Place.Point.X = 999
	got, _ := s.Position()
	got.Place.Point.Y = 888
	again, _ := s.Position()
	if again.Place.Point.X != 4.2 || again.Place.Point.Y != 10.5 {
		t.Fatalf("the stored point was mutated through an alias: %+v", again.Place.Point)
	}
}

// --- invariants 2 and 3: one Active hint; a valid hint ---

func TestIssueHint(t *testing.T) {
	s := completedTrip(t)
	if err := s.IssueHint(hint1, candidates(), testPolicy(t), issuedAt()); err != nil {
		t.Fatal(err)
	}
	h, ok := s.ActiveHint()
	if !ok || h.ID != hint1 || h.State != stationtrip.StateActive {
		t.Fatalf("hint %+v %v", h, ok)
	}
	if h.AfterTask != "task-8a1f" || h.Origin != "WH1-FWD-A01-01-01-A" || h.PolicyVersion != 1 {
		t.Fatalf("hint fields %+v", h)
	}
	if !h.IssuedAt.Equal(issuedAt()) || !h.ExpiresAt.Equal(expiresAt()) {
		t.Fatalf("issued %v expires %v", h.IssuedAt, h.ExpiresAt)
	}
	if !reflect.DeepEqual(h.Candidates, candidates()) {
		t.Fatalf("candidates %+v", h.Candidates)
	}
	if !h.ConsumedAt.IsZero() || h.ConsumedTask != "" {
		t.Fatalf("a fresh hint has no consumption: %+v", h)
	}
	if s.Version() != 2 {
		t.Fatalf("version %d", s.Version())
	}
}

func TestIssueHintRaisesHintIssued(t *testing.T) {
	s := completedTrip(t)
	if err := s.IssueHint(hint1, candidates(), testPolicy(t), issuedAt()); err != nil {
		t.Fatal(err)
	}
	evs := s.DomainEvents()
	if len(evs) != 1 {
		t.Fatalf("want 1 event, got %d", len(evs))
	}
	e, ok := evs[0].(stationtrip.HintIssued)
	if !ok {
		t.Fatalf("event %T", evs[0])
	}
	assertHeader(t, e, "HintIssued", 2, issuedAt())
	if e.HintID != hint1 || e.AfterTask != "task-8a1f" || e.Origin != "WH1-FWD-A01-01-01-A" ||
		!e.ExpiresAt.Equal(expiresAt()) || e.PolicyVersion != 1 || e.SupersededHintID != "" {
		t.Fatalf("event %+v", e)
	}
	if !reflect.DeepEqual(e.Candidates, candidates()) {
		t.Fatalf("candidates %+v", e.Candidates)
	}
}

// assertHeader checks the facts every event of the station carries.
func assertHeader(t *testing.T, e stationtrip.Event, name string, version int64, at time.Time) {
	t.Helper()
	if e.EventName() != name || e.StationID() != stationID || e.SiteID() != siteID ||
		e.StationVersion() != version || !e.OccurredAt().Equal(at) {
		t.Fatalf("header of %s: station %q site %q v%d at %v", e.EventName(), e.StationID(), e.SiteID(), e.StationVersion(), e.OccurredAt())
	}
}

func TestIssueHintSupersedesTheActiveHint(t *testing.T) {
	s := hintedTrip(t)
	later := issuedAt().Add(10 * time.Second)
	if err := s.IssueHint(hint2, candidates()[:1], testPolicy(t), later); err != nil {
		t.Fatal(err)
	}
	h, _ := s.Hint()
	if h.ID != hint2 || h.State != stationtrip.StateActive || len(h.Candidates) != 1 {
		t.Fatalf("hint %+v", h)
	}
	e := s.DomainEvents()[0].(stationtrip.HintIssued)
	if e.SupersededHintID != hint1 {
		t.Fatalf("superseded %q want %q", e.SupersededHintID, hint1)
	}
	if s.Version() != 3 {
		t.Fatalf("version %d", s.Version())
	}
	// The superseded hint can no longer be consumed through the new one's candidates.
	if s.RecordClaim("task-c", later.Add(time.Second)) {
		t.Fatal("task-c is not a candidate of the new hint")
	}
}

func TestAtMostOneActiveHintAcrossManyIssues(t *testing.T) {
	s := hintedTrip(t)
	for i := 0; i < 3; i++ {
		id := stationtrip.HintID("hint-n" + string(rune('a'+i)))
		if err := s.IssueHint(id, candidates()[:1], testPolicy(t), issuedAt().Add(time.Duration(i+1)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	active := 0
	for _, e := range s.DomainEvents() {
		if e.(stationtrip.HintIssued).SupersededHintID == "" {
			t.Fatal("each of these issues replaced an Active hint")
		}
		active++
	}
	if h, _ := s.ActiveHint(); h.ID != "hint-nc" || active != 3 {
		t.Fatalf("active hint %q after %d issues", h.ID, active)
	}
}

func TestIssueHintDoesNotSupersedeATerminalHint(t *testing.T) {
	cases := map[string]func(*stationtrip.StationTrip){
		"consumed": func(s *stationtrip.StationTrip) { s.RecordClaim("task-a", issuedAt().Add(time.Second)) },
		"expired":  func(s *stationtrip.StationTrip) { s.Expire(expiresAt()) },
	}
	for name, end := range cases {
		t.Run(name, func(t *testing.T) {
			s := hintedTrip(t)
			end(s)
			s.ClearDomainEvents()
			if err := s.IssueHint(hint2, candidates(), testPolicy(t), expiresAt().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			if e := s.DomainEvents()[0].(stationtrip.HintIssued); e.SupersededHintID != "" {
				t.Fatalf("nothing was Active, superseded=%q", e.SupersededHintID)
			}
		})
	}
}

func TestIssueHintPreconditions(t *testing.T) {
	t.Run("no position", func(t *testing.T) {
		s := newTrip(t)
		if err := s.IssueHint(hint1, candidates(), testPolicy(t), issuedAt()); !errors.Is(err, stationtrip.ErrNoPosition) {
			t.Fatalf("want ErrNoPosition, got %v", err)
		}
	})
	t.Run("bad hint id", func(t *testing.T) {
		s := completedTrip(t)
		if err := s.IssueHint("nope", candidates(), testPolicy(t), issuedAt()); !errors.Is(err, stationtrip.ErrInvalidHintID) {
			t.Fatalf("want ErrInvalidHintID, got %v", err)
		}
	})
	t.Run("zero now", func(t *testing.T) {
		s := completedTrip(t)
		if err := s.IssueHint(hint1, candidates(), testPolicy(t), time.Time{}); !errors.Is(err, stationtrip.ErrInvalidTime) {
			t.Fatalf("want ErrInvalidTime, got %v", err)
		}
	})
	t.Run("policy of another site", func(t *testing.T) {
		s := completedTrip(t)
		other, err := policy.New("SITE-2", testPolicy(t).Settings())
		if err != nil {
			t.Fatal(err)
		}
		if err := s.IssueHint(hint1, candidates(), other, issuedAt()); !errors.Is(err, stationtrip.ErrSiteMismatch) {
			t.Fatalf("want ErrSiteMismatch, got %v", err)
		}
	})
}

func TestIssueHintRefusesAnEmptyCandidateSet(t *testing.T) {
	for name, cs := range map[string][]recommend.Candidate{"nil": nil, "empty": {}} {
		t.Run(name, func(t *testing.T) {
			s := hintedTrip(t)
			if err := s.IssueHint(hint2, cs, testPolicy(t), issuedAt().Add(time.Second)); !errors.Is(err, stationtrip.ErrNoCandidates) {
				t.Fatalf("want ErrNoCandidates, got %v", err)
			}
			assertUntouched(t, s)
		})
	}
}

// assertUntouched checks a refused IssueHint left hintedTrip's state alone: the
// Active hint1, version 2 and no event.
func assertUntouched(t *testing.T, s *stationtrip.StationTrip) {
	t.Helper()
	h, ok := s.ActiveHint()
	if !ok || h.ID != hint1 || s.Version() != 2 || len(s.DomainEvents()) != 0 {
		t.Fatalf("a refused hint changed state: %+v active=%v v%d events=%d", h, ok, s.Version(), len(s.DomainEvents()))
	}
}

func candidate(rank int, id string, typ policy.TaskType, detour float64) recommend.Candidate {
	return recommend.Candidate{Rank: rank, TaskID: recommend.TaskID(id), TaskType: typ, Location: "L-" + id, DetourM: detour}
}

func TestIssueHintCandidateRules(t *testing.T) {
	pick, repl := policy.TaskPick, policy.TaskReplenish
	cases := []struct {
		name string
		cs   []recommend.Candidate
		want error
	}{
		{"too many", []recommend.Candidate{
			candidate(1, "a", pick, 1), candidate(2, "b", pick, 2), candidate(3, "c", pick, 3), candidate(4, "d", pick, 4),
		}, stationtrip.ErrTooManyCandidates},
		{"duplicate task", []recommend.Candidate{candidate(1, "a", pick, 1), candidate(2, "a", pick, 2)}, stationtrip.ErrDuplicateCandidate},
		{"ineligible type", []recommend.Candidate{candidate(1, "a", policy.TaskPack, 1)}, stationtrip.ErrCandidateNotEligible},
		{"detour over the limit", []recommend.Candidate{candidate(1, "a", pick, 25.1)}, stationtrip.ErrCandidateDetour},
		{"negative detour", []recommend.Candidate{candidate(1, "a", pick, -0.1)}, stationtrip.ErrCandidateDetour},
		{"NaN detour", []recommend.Candidate{candidate(1, "a", pick, math.NaN())}, stationtrip.ErrCandidateDetour},
		{"Inf detour", []recommend.Candidate{candidate(1, "a", pick, math.Inf(1))}, stationtrip.ErrCandidateDetour},
		{"blank task id", []recommend.Candidate{candidate(1, " ", pick, 1)}, stationtrip.ErrInvalidTask},
		{"rank starts at 0", []recommend.Candidate{candidate(0, "a", pick, 1)}, stationtrip.ErrCandidateRanking},
		{"rank starts at 2", []recommend.Candidate{candidate(2, "a", pick, 1)}, stationtrip.ErrCandidateRanking},
		{"rank skips", []recommend.Candidate{candidate(1, "a", pick, 1), candidate(3, "b", repl, 2)}, stationtrip.ErrCandidateRanking},
		{"rank repeats", []recommend.Candidate{candidate(1, "a", pick, 1), candidate(1, "b", repl, 2)}, stationtrip.ErrCandidateRanking},
		{"detour descending", []recommend.Candidate{candidate(1, "a", pick, 9), candidate(2, "b", repl, 3)}, stationtrip.ErrCandidateRanking},
		{"tie in descending id order", []recommend.Candidate{candidate(1, "b", pick, 5), candidate(2, "a", repl, 5)}, stationtrip.ErrCandidateRanking},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := hintedTrip(t)
			if err := s.IssueHint(hint2, tc.cs, testPolicy(t), issuedAt().Add(time.Second)); !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
			assertUntouched(t, s)
		})
	}
}

func TestIssueHintAcceptsTheBoundaries(t *testing.T) {
	pick, repl := policy.TaskPick, policy.TaskReplenish
	cases := []struct {
		name string
		cs   []recommend.Candidate
	}{
		{"detour exactly at the limit", []recommend.Candidate{candidate(1, "a", pick, 25)}},
		{"detour zero", []recommend.Candidate{candidate(1, "a", pick, 0)}},
		{"exactly max candidates", []recommend.Candidate{candidate(1, "a", pick, 1), candidate(2, "b", repl, 2), candidate(3, "c", pick, 3)}},
		{"equal detours, ascending ids", []recommend.Candidate{candidate(1, "a", pick, 5), candidate(2, "b", repl, 5)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := completedTrip(t)
			if err := s.IssueHint(hint1, tc.cs, testPolicy(t), issuedAt()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestIssueHintDoesNotAliasTheCallersCandidates(t *testing.T) {
	s := completedTrip(t)
	cs := candidates()
	if err := s.IssueHint(hint1, cs, testPolicy(t), issuedAt()); err != nil {
		t.Fatal(err)
	}
	cs[0].TaskID = "mutated"
	got, _ := s.Hint()
	got.Candidates[1].TaskID = "mutated-too"
	if !reflect.DeepEqual(mustHint(t, s).Candidates, candidates()) {
		t.Fatalf("candidates were mutated through an alias: %+v", mustHint(t, s).Candidates)
	}
	if !reflect.DeepEqual(s.DomainEvents()[0].(stationtrip.HintIssued).Candidates, candidates()) {
		t.Fatal("the raised event aliases the hint")
	}
}

func mustHint(t *testing.T, s *stationtrip.StationTrip) stationtrip.Hint {
	t.Helper()
	h, ok := s.Hint()
	if !ok {
		t.Fatal("no hint")
	}
	return h
}

// --- invariant 5: consumption ---

func TestRecordClaimConsumesTheHintForACandidate(t *testing.T) {
	s := hintedTrip(t)
	at := issuedAt().Add(20 * time.Second)
	if !s.RecordClaim("task-b", at) {
		t.Fatal("claiming a candidate must consume the hint")
	}
	h := mustHint(t, s)
	if h.State != stationtrip.StateConsumed || h.ConsumedTask != "task-b" || !h.ConsumedAt.Equal(at) {
		t.Fatalf("hint %+v", h)
	}
	if _, active := s.ActiveHint(); active {
		t.Fatal("a consumed hint is not Active")
	}
	if s.Version() != 3 {
		t.Fatalf("version %d", s.Version())
	}
	evs := s.DomainEvents()
	if len(evs) != 1 {
		t.Fatalf("events %d", len(evs))
	}
	e, ok := evs[0].(stationtrip.HintConsumed)
	if !ok {
		t.Fatalf("event %T", evs[0])
	}
	assertHeader(t, e, "HintConsumed", 3, at)
	if e.HintID != hint1 || e.TaskID != "task-b" || e.Rank != 2 || e.DetourM != 12 {
		t.Fatalf("event %+v", e)
	}
}

func TestRecordClaimOfANonCandidateLeavesTheHintActive(t *testing.T) {
	s := hintedTrip(t)
	if s.RecordClaim("task-z", issuedAt().Add(time.Second)) {
		t.Fatal("a non-candidate claim must not consume the hint")
	}
	assertUntouched(t, s)
}

func TestRecordClaimWithoutAnActiveHintChangesNothing(t *testing.T) {
	t.Run("no hint at all", func(t *testing.T) {
		s := completedTrip(t)
		if s.RecordClaim("task-a", issuedAt()) || s.Version() != 1 {
			t.Fatal("nothing to consume")
		}
	})
	terminal := map[string]func(*stationtrip.StationTrip){
		"consumed": func(s *stationtrip.StationTrip) { s.RecordClaim("task-a", issuedAt().Add(time.Second)) },
		"expired":  func(s *stationtrip.StationTrip) { s.Expire(expiresAt()) },
	}
	for name, end := range terminal {
		t.Run(name, func(t *testing.T) {
			s := hintedTrip(t)
			end(s)
			v := s.Version()
			if s.RecordClaim("task-c", issuedAt().Add(2*time.Second)) || s.Version() != v {
				t.Fatalf("a terminal hint cannot be consumed")
			}
		})
	}
}

func TestRecordClaimTimeWindow(t *testing.T) {
	cases := []struct {
		name string
		at   time.Time
		want bool
	}{
		{"at the issue instant", issuedAt(), true},
		{"one nanosecond before issue", issuedAt().Add(-time.Nanosecond), false},
		{"one nanosecond before expiry", expiresAt().Add(-time.Nanosecond), true},
		{"at expiry", expiresAt(), false},
		{"after expiry", expiresAt().Add(time.Second), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := hintedTrip(t)
			if got := s.RecordClaim("task-a", tc.at); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
			wantState := stationtrip.StateActive
			if tc.want {
				wantState = stationtrip.StateConsumed
			}
			if mustHint(t, s).State != wantState {
				t.Fatalf("state %s want %s", mustHint(t, s).State, wantState)
			}
		})
	}
}

func TestRecordClaimConsumesAtMostOnce(t *testing.T) {
	s := hintedTrip(t)
	if !s.RecordClaim("task-a", issuedAt().Add(time.Second)) {
		t.Fatal("first claim consumes")
	}
	if s.RecordClaim("task-b", issuedAt().Add(2*time.Second)) {
		t.Fatal("a Consumed hint is terminal")
	}
	if len(s.DomainEvents()) != 1 || s.Version() != 3 {
		t.Fatalf("events %d version %d", len(s.DomainEvents()), s.Version())
	}
}

// --- invariant 4: expiry ---

func TestExpireMarksTheHintExpiredAtOrAfterItsExpiry(t *testing.T) {
	cases := []struct {
		name string
		at   time.Time
	}{
		{"at expiry", expiresAt()},
		{"after expiry", expiresAt().Add(5 * time.Minute)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := hintedTrip(t)
			if !s.Expire(tc.at) {
				t.Fatal("must expire")
			}
			h := mustHint(t, s)
			if h.State != stationtrip.StateExpired || s.Version() != 3 {
				t.Fatalf("state %s version %d", h.State, s.Version())
			}
			e, ok := s.DomainEvents()[0].(stationtrip.HintExpired)
			if !ok {
				t.Fatalf("event %T", s.DomainEvents()[0])
			}
			assertHeader(t, e, "HintExpired", 3, tc.at)
			if e.HintID != hint1 || !e.ExpiresAt.Equal(expiresAt()) {
				t.Fatalf("event %+v", e)
			}
		})
	}
}

func TestExpireBeforeExpiryChangesNothing(t *testing.T) {
	s := hintedTrip(t)
	if s.Expire(expiresAt().Add(-time.Nanosecond)) {
		t.Fatal("must not expire one nanosecond early")
	}
	assertUntouched(t, s)
}

func TestExpireWithoutAnActiveHintChangesNothing(t *testing.T) {
	if s := completedTrip(t); s.Expire(expiresAt()) || s.Version() != 1 {
		t.Fatal("no hint to expire")
	}
	s := hintedTrip(t)
	s.RecordClaim("task-a", issuedAt().Add(time.Second))
	v := s.Version()
	if s.Expire(expiresAt().Add(time.Hour)) || s.Version() != v || mustHint(t, s).State != stationtrip.StateConsumed {
		t.Fatal("a Consumed hint never becomes Expired")
	}
}

func TestExpireTwiceExpiresOnce(t *testing.T) {
	s := hintedTrip(t)
	if !s.Expire(expiresAt()) || s.Expire(expiresAt().Add(time.Second)) {
		t.Fatal("the second Expire must be a no-op")
	}
	if len(s.DomainEvents()) != 1 {
		t.Fatalf("events %d", len(s.DomainEvents()))
	}
}

// --- events ---

func TestDomainEventsAreOrderedCopiesAndClearable(t *testing.T) {
	s := completedTrip(t)
	if err := s.IssueHint(hint1, candidates(), testPolicy(t), issuedAt()); err != nil {
		t.Fatal(err)
	}
	s.RecordClaim("task-a", issuedAt().Add(time.Second))
	evs := s.DomainEvents()
	if len(evs) != 2 || evs[0].EventName() != "HintIssued" || evs[1].EventName() != "HintConsumed" {
		t.Fatalf("events %v", evs)
	}
	if evs[0].StationVersion() != 2 || evs[1].StationVersion() != 3 {
		t.Fatal("each event carries the version after its own change")
	}
	evs[0] = nil
	if s.DomainEvents()[0] == nil {
		t.Fatal("DomainEvents must return a copy")
	}
	s.ClearDomainEvents()
	if len(s.DomainEvents()) != 0 {
		t.Fatal("ClearDomainEvents must forget the facts")
	}
	if s.Version() != 3 {
		t.Fatal("clearing events must not touch the version")
	}
}

func TestHintAccessorsReturnCopies(t *testing.T) {
	s := hintedTrip(t)
	h, _ := s.Hint()
	h.Candidates[0].TaskID = "x"
	h.State = stationtrip.StateExpired
	if got := mustHint(t, s); got.Candidates[0].TaskID != "task-a" || got.State != stationtrip.StateActive {
		t.Fatalf("the hint was mutated through an accessor: %+v", got)
	}
	a, _ := s.ActiveHint()
	a.Candidates[0].TaskID = "y"
	if mustHint(t, s).Candidates[0].TaskID != "task-a" {
		t.Fatal("ActiveHint must return a copy")
	}
}

func TestAFullTrip(t *testing.T) {
	s := completedTrip(t)
	p := testPolicy(t)
	if err := s.IssueHint(hint1, candidates(), p, issuedAt()); err != nil {
		t.Fatal(err)
	}
	if !s.RecordClaim("task-a", issuedAt().Add(5*time.Second)) {
		t.Fatal("claim consumes hint1")
	}
	next := position()
	next.Place.Location = "LOC-A"
	if applied, err := s.RecordCompletion("task-a", issuedAt().Add(30*time.Second), next); err != nil || !applied {
		t.Fatalf("%v %v", applied, err)
	}
	only := []recommend.Candidate{candidate(1, "task-b", policy.TaskReplenish, 12)}
	if err := s.IssueHint(hint2, only, p, issuedAt().Add(31*time.Second)); err != nil {
		t.Fatal(err)
	}
	if !s.Expire(issuedAt().Add(31*time.Second + 90*time.Second)) {
		t.Fatal("hint2 expires unclaimed")
	}
	names := []string{}
	for _, e := range s.DomainEvents() {
		names = append(names, e.EventName())
	}
	want := []string{"HintIssued", "HintConsumed", "HintIssued", "HintExpired"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("events %v want %v", names, want)
	}
	if s.Version() != 6 {
		t.Fatalf("version %d", s.Version())
	}
}
