// Package recommend is the pure interleaving recommender: given where a station
// stands, the open-task pool and the site's policy, it ranks the few tasks the
// operator could do next with the smallest detour. No I/O, no clock, no
// randomness: the same input, in any pool order, yields the same output.
package recommend

import (
	"cmp"
	"errors"
	"slices"

	"github.com/claudioed/task-interleaving/internal/domain/policy"
	"github.com/claudioed/task-interleaving/internal/domain/travel"
)

var (
	// ErrOriginUnknown is returned when the origin has no known geometry: there
	// is nothing to measure from, so no hint can be computed.
	ErrOriginUnknown = errors.New("recommend: the origin has no known geometry")
	// ErrNoModel is returned when the input carries no travel model.
	ErrNoModel = errors.New("recommend: a travel model is required")
)

// TaskID identifies a task of fulfillment-execution.
type TaskID string

// OpenTask is one task of the open-task pool, as the local copy holds it.
// FromLocation is where the operator must go first; ToLocation is where the
// operator ends and does not enter the ranking. A nil FromPoint means the
// geometry of FromLocation is unknown.
type OpenTask struct {
	ID           TaskID
	Type         policy.TaskType
	Site         policy.SiteID
	FromLocation string
	FromPoint    *travel.Point
	ToLocation   string
}

// Input is everything the recommender reads.
type Input struct {
	// Site is the site of the station the hint is for.
	Site policy.SiteID
	// Origin is where the station stands now.
	Origin travel.Place
	// Pool is the open-task pool; its order is irrelevant.
	Pool []OpenTask
	// Policy is the site's policy.
	Policy policy.Policy
	// Completed is the task the station just completed; it is never recommended.
	Completed TaskID
	// Model is the travel model; RectilinearFor(Policy) is the v1 choice.
	Model travel.Model
}

// Candidate is one recommended task, ranked from 1.
type Candidate struct {
	Rank     int
	TaskID   TaskID
	TaskType policy.TaskType
	Location string
	DetourM  float64
}

// RectilinearFor returns the v1 travel model for a policy: rectilinear-v1 with
// the policy's vertical weight. A policy guarantees a finite weight of at
// least 0, so the model can always be built.
func RectilinearFor(p policy.Policy) travel.Model {
	m, _ := travel.NewRectilinear(p.VerticalWeight())
	return m
}

// Recommend returns the ranked candidates, possibly none (no hint).
//
// A pool task is a candidate when it is of the station's site, its type is
// eligible, it is not the task just completed, its geometry is known and its
// detour is at most the policy's MaxDetourM. Candidates are ordered by DetourM
// ascending then TaskID ascending, distinct by task id, cut to MaxCandidates and
// numbered from 1.
func Recommend(in Input) ([]Candidate, error) {
	if in.Model == nil {
		return nil, ErrNoModel
	}
	if !in.Origin.Known() {
		return nil, ErrOriginUnknown
	}
	var found []Candidate
	for _, t := range in.Pool {
		if c, ok := candidateFor(in, t); ok {
			found = append(found, c)
		}
	}
	slices.SortFunc(found, byDetourThenID)
	found = distinct(found)
	if len(found) > in.Policy.MaxCandidates() {
		found = found[:in.Policy.MaxCandidates()]
	}
	for i := range found {
		found[i].Rank = i + 1
	}
	return found, nil
}

func candidateFor(in Input, t OpenTask) (Candidate, bool) {
	if t.Site != in.Site {
		return Candidate{}, false
	}
	if !in.Policy.Eligible(t.Type) {
		return Candidate{}, false
	}
	if t.ID == in.Completed {
		return Candidate{}, false
	}
	d, ok := in.Model.Distance(in.Origin, travel.Place{Location: t.FromLocation, Point: t.FromPoint})
	if !ok {
		return Candidate{}, false
	}
	if d > in.Policy.MaxDetourM() {
		return Candidate{}, false
	}
	return Candidate{TaskID: t.ID, TaskType: t.Type, Location: t.FromLocation, DetourM: d}, true
}

func byDetourThenID(a, b Candidate) int {
	if c := cmp.Compare(a.DetourM, b.DetourM); c != 0 {
		return c
	}
	return cmp.Compare(a.TaskID, b.TaskID)
}

// distinct drops repeated task ids from a sorted slice, keeping the first
// (smallest detour) occurrence.
func distinct(sorted []Candidate) []Candidate {
	seen := make(map[TaskID]struct{}, len(sorted))
	out := sorted[:0]
	for _, c := range sorted {
		if _, dup := seen[c.TaskID]; dup {
			continue
		}
		seen[c.TaskID] = struct{}{}
		out = append(out, c)
	}
	return out
}
