// Package repocontract holds the behavioural contract of the outbound
// repositories. The in-memory adapters and the Postgres adapters run the SAME
// cases, so what BDD and `go run ./cmd/api` without DATABASE_URL show is what
// production does (ADR 0005).
package repocontract

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/claudioed/task-interleaving/internal/domain/policy"
	"github.com/claudioed/task-interleaving/internal/domain/recommend"
	"github.com/claudioed/task-interleaving/internal/domain/stationtrip"
	"github.com/claudioed/task-interleaving/internal/domain/travel"
)

// T0 is the fixed instant the cases build on. It has no sub-microsecond part,
// so a Postgres round trip returns it unchanged.
var T0 = time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)

// Site is the site of every trip and policy the builders make.
const Site policy.SiteID = "SITE-1"

var bg = context.Background()

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func wantErr(t *testing.T, got, want error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Fatalf("error = %v, want %v", got, want)
	}
}

func equalSlice[T comparable](a, b []T) bool { return slices.Equal(a, b) }

// Policy builds the policy of Site: 25 m detour, 3 candidates, 90 s TTL, PICK
// tasks only, vertical weight 1.5.
func Policy(t *testing.T, site policy.SiteID) policy.Policy {
	t.Helper()
	p, err := policy.New(site, policy.Settings{
		MaxDetourM: 25, MaxCandidates: 3, HintTTL: 90 * time.Second,
		EligibleTaskTypes: []policy.TaskType{policy.TaskPick, policy.TaskRelocate}, VerticalWeight: 1.5,
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// EmptyTrip builds the trip of a station that recorded nothing yet.
func EmptyTrip(t *testing.T, station string) *stationtrip.StationTrip {
	t.Helper()
	trip, err := stationtrip.New(stationtrip.StationID(station))
	if err != nil {
		t.Fatal(err)
	}
	return trip
}

// Completed builds a trip of station that completed task at the given instant
// at LOC-0, a place with geometry.
func Completed(t *testing.T, station, task string, at time.Time) *stationtrip.StationTrip {
	t.Helper()
	trip := EmptyTrip(t, station)
	Complete(t, trip, task, "LOC-0", at)
	return trip
}

// Complete records that trip's station completed task at location (with a
// point) at the given instant.
func Complete(t *testing.T, trip *stationtrip.StationTrip, task, location string, at time.Time) {
	t.Helper()
	pos := stationtrip.Position{Site: Site, Place: travel.Place{Location: location, Point: &travel.Point{X: 1, Y: 2, Z: 3}}}
	changed, err := trip.RecordCompletion(recommend.TaskID(task), at, pos)
	if err != nil || !changed {
		t.Fatalf("RecordCompletion = %v %v", changed, err)
	}
}

// Candidates builds n ranked candidates, task-a, task-b, ...
func Candidates(n int) []recommend.Candidate {
	out := make([]recommend.Candidate, 0, n)
	for i := range n {
		out = append(out, recommend.Candidate{
			Rank: i + 1, TaskID: recommend.TaskID("task-" + string(rune('a'+i))), TaskType: policy.TaskPick,
			Location: "LOC-" + string(rune('A'+i)), DetourM: float64(i+1) * 1.5,
		})
	}
	return out
}

// Issue issues hint id with n candidates to trip at the given instant.
func Issue(t *testing.T, trip *stationtrip.StationTrip, id string, n int, at time.Time) {
	t.Helper()
	must(t, trip.IssueHint(stationtrip.HintID(id), Candidates(n), Policy(t, Site), at))
}

// Hinted builds a trip of station that completed a task at `at` and was given
// hint id one second later (Active until at+91s).
func Hinted(t *testing.T, station, id string, at time.Time) *stationtrip.StationTrip {
	t.Helper()
	trip := Completed(t, station, "task-0", at)
	Issue(t, trip, id, 2, at.Add(time.Second))
	return trip
}

// sameTrip reports whether two trips hold the same state, instants by Equal
// (not by the struct's location pointer).
func sameTrip(a, b *stationtrip.StationTrip) bool {
	x, y := a.Snapshot(), b.Snapshot()
	if x.ID != y.ID || x.Version != y.Version || x.LastTask != y.LastTask || !x.LastAt.Equal(y.LastAt) {
		return false
	}
	if x.Position.Site != y.Position.Site || !samePlace(x.Position.Place, y.Position.Place) {
		return false
	}
	return sameHint(x.Hint, y.Hint)
}

func samePlace(a, b travel.Place) bool {
	if a.Location != b.Location || (a.Point == nil) != (b.Point == nil) {
		return false
	}
	return a.Point == nil || *a.Point == *b.Point
}

func sameHint(a, b *stationtrip.Hint) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.ID != b.ID || a.AfterTask != b.AfterTask || a.Origin != b.Origin || a.PolicyVersion != b.PolicyVersion {
		return false
	}
	if a.State != b.State || a.ConsumedTask != b.ConsumedTask {
		return false
	}
	if !a.IssuedAt.Equal(b.IssuedAt) || !a.ExpiresAt.Equal(b.ExpiresAt) || !a.ConsumedAt.Equal(b.ConsumedAt) {
		return false
	}
	return slices.Equal(a.Candidates, b.Candidates)
}
