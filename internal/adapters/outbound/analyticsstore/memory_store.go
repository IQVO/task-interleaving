package analyticsstore

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/claudioed/task-interleaving/internal/analytics/report"
)

// Memory is the in-memory twin of the analytical store: the same Projection and
// the same Reader semantics (earliest milestone wins, dedupe on the CloudEvents
// id, dense day rows, percentile_cont percentiles), so the same contract test
// can run against it and against Postgres. It is NOT the production store:
// nothing in cmd/ ever wires it.
type Memory struct {
	mu      sync.Mutex
	applied map[string]time.Time // event id -> CloudEvents time
	hints   map[string]*memoryHint
}

// memoryHint mirrors one hint_facts row.
type memoryHint struct {
	site, station string
	issued        *time.Time
	consumed      *time.Time
	expired       *time.Time
	rank          int
	detourM       float64
	hasConsumed   bool
}

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{applied: map[string]time.Time{}, hints: map[string]*memoryHint{}}
}

var (
	_ report.Projection = (*Memory)(nil)
	_ report.Reader     = (*Memory)(nil)
)

func least(existing, candidate *time.Time) *time.Time {
	if candidate == nil {
		return existing // LEAST ignores NULLs: a milestone this event does not mark keeps the stored value
	}
	if existing == nil || candidate.Before(*existing) {
		return candidate
	}
	return existing
}

// Apply implements report.Projection with the same first-writer-wins semantics
// as the SQL store.
func (m *Memory) Apply(_ context.Context, e report.Event) (bool, error) {
	if err := e.Validate(); err != nil {
		return false, fmt.Errorf("%w: %w", report.ErrRejected, err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, seen := m.applied[e.EventID]; seen {
		return false, nil
	}
	m.applied[e.EventID] = e.At.UTC()
	if !e.Kind.IsHint() {
		return true, nil
	}
	h, ok := m.hints[e.HintID]
	if !ok {
		h = &memoryHint{site: e.SiteID, station: e.Station}
		m.hints[e.HintID] = h
	}
	ms := report.MilestonesOf(e)
	h.issued = least(h.issued, ms.Issued)
	h.expired = least(h.expired, ms.Expired)
	if e.Kind == report.KindHintConsumed && !h.hasConsumed {
		h.hasConsumed, h.rank, h.detourM = true, e.Rank, e.DetourM
	}
	h.consumed = least(h.consumed, ms.Consumed)
	return true, nil
}

// in reports whether t falls in the half-open window [w.From, w.To); a nil time
// never does.
func in(t *time.Time, w report.DayWindow) bool {
	return t != nil && !t.Before(w.From) && t.Before(w.To)
}

// countDay folds the hints of one site scope into the single window w, exactly
// like adoptionDaysSQL.
func countDay(hints []*memoryHint, w report.DayWindow) report.AdoptionDay {
	d := report.AdoptionDay{Day: w.Day}
	var (
		ttc     []float64
		detours float64
	)
	for _, h := range hints {
		if in(h.issued, w) {
			d.Issued++
		}
		if in(h.consumed, w) {
			d.Consumed++
			detours += h.detourM
			if h.rank == 1 {
				d.ConsumedFirstRank++
			}
			if h.issued != nil {
				ttc = append(ttc, max(0, h.consumed.Sub(*h.issued).Seconds()))
			}
		}
		if in(h.expired, w) {
			d.Expired++
		}
	}
	if d.Consumed > 0 {
		mean := detours / float64(d.Consumed)
		d.MeanDetourM = &mean
	}
	sort.Float64s(ttc)
	d.TimeToConsume = report.TimingOf(ttc)
	return d
}

// AdoptionDays implements report.Reader: one dense row per DayWindow.
func (m *Memory) AdoptionDays(_ context.Context, q report.AdoptionQuery) ([]report.AdoptionDay, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	scoped := make([]*memoryHint, 0, len(m.hints))
	for _, h := range m.hints {
		if q.SiteID == "" || h.site == q.SiteID {
			scoped = append(scoped, h)
		}
	}
	var out []report.AdoptionDay
	for _, w := range report.DayWindows(q.Range) {
		out = append(out, countDay(scoped, w))
	}
	return out, nil
}

// LastEventAt implements report.Reader: the newest applied CloudEvents time.
func (m *Memory) LastEventAt(_ context.Context) (*time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var newest *time.Time
	for _, at := range m.applied {
		if newest == nil || at.After(*newest) {
			newest = &at
		}
	}
	if newest != nil {
		utc := newest.UTC()
		return &utc, nil
	}
	return nil, nil
}
