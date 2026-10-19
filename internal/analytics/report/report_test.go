package report_test

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/claudioed/task-interleaving/internal/analytics/report"
)

var now = time.Date(2026, 10, 17, 12, 30, 0, 0, time.UTC)

func TestParseRange(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, 10, d, 0, 0, 0, 0, time.UTC) }
	cases := []struct {
		name, from, to string
		want           report.Range
		err            error
	}{
		{"no bounds is the 30 days ending now", "", "", report.Range{From: now.Add(-30 * 24 * time.Hour), To: now}, nil},
		{"only to", "", "2026-10-10T00:00:00Z", report.Range{From: day(10).Add(-30 * 24 * time.Hour), To: day(10)}, nil},
		{"only from", "2026-10-01T00:00:00Z", "", report.Range{From: day(1), To: now}, nil},
		{"both, with an offset, in UTC", "2026-10-05T03:00:00+03:00", "2026-10-06T00:00:00Z", report.Range{From: day(5), To: day(6)}, nil},
		{"bad from", "yesterday", "", report.Range{}, report.ErrInvalidFrom},
		{"bad to", "", "tomorrow", report.Range{}, report.ErrInvalidTo},
		{"empty", "2026-10-05T00:00:00Z", "2026-10-05T00:00:00Z", report.Range{}, report.ErrEmptyRange},
		{"inverted", "2026-10-06T00:00:00Z", "2026-10-05T00:00:00Z", report.Range{}, report.ErrEmptyRange},
		{"366 days is allowed", "2025-10-01T00:00:00Z", "2026-10-02T00:00:00Z", report.Range{From: time.Date(2025, 10, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)}, nil},
		{"367 days is too large", "2025-10-01T00:00:00Z", "2026-10-03T00:00:00Z", report.Range{}, report.ErrRangeTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := report.ParseRange(tc.from, tc.to, now)
			if !errors.Is(err, tc.err) {
				t.Fatalf("err = %v, want %v", err, tc.err)
			}
			if err == nil && (!got.From.Equal(tc.want.From) || !got.To.Equal(tc.want.To)) {
				t.Fatalf("range = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseGranularity(t *testing.T) {
	for _, raw := range []string{"", "day"} {
		if g, err := report.ParseGranularity(raw); err != nil || g != report.GranularityDay {
			t.Fatalf("ParseGranularity(%q) = %v, %v", raw, g, err)
		}
	}
	for _, raw := range []string{"hour", "week", "DAY"} {
		if _, err := report.ParseGranularity(raw); !errors.Is(err, report.ErrInvalidGranularity) {
			t.Fatalf("ParseGranularity(%q) err = %v", raw, err)
		}
	}
}

func TestDayWindows(t *testing.T) {
	r := report.Range{
		From: time.Date(2026, 10, 5, 23, 0, 0, 0, time.UTC),
		To:   time.Date(2026, 10, 8, 1, 0, 0, 0, time.UTC),
	}
	ws := report.DayWindows(r)
	if len(ws) != 4 {
		t.Fatalf("windows = %d, want 4", len(ws))
	}
	if !ws[0].From.Equal(r.From) || !ws[0].To.Equal(time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("first window = %+v", ws[0])
	}
	if !ws[1].From.Equal(ws[1].Day) || !ws[1].To.Equal(ws[1].Day.AddDate(0, 0, 1)) {
		t.Fatalf("a middle window must be a whole day: %+v", ws[1])
	}
	if !ws[3].To.Equal(r.To) {
		t.Fatalf("last window = %+v", ws[3])
	}
	if got := report.DayWindows(report.Range{From: r.To, To: r.From}); got != nil {
		t.Fatalf("an inverted range yields %v, want none", got)
	}
}

func TestComputeFreshness(t *testing.T) {
	if f := report.ComputeFreshness(nil, now); f.AsOf != nil || f.LagSeconds != nil {
		t.Fatalf("freshness of nothing = %+v, want both nil", f)
	}
	asOf := now.Add(-90 * time.Second)
	f := report.ComputeFreshness(&asOf, now)
	if f.AsOf == nil || !f.AsOf.Equal(asOf) || f.LagSeconds == nil || *f.LagSeconds != 90 {
		t.Fatalf("freshness = %+v", f)
	}
	future := now.Add(time.Hour)
	if f := report.ComputeFreshness(&future, now); f.LagSeconds == nil || *f.LagSeconds != 0 {
		t.Fatalf("a skewed clock must read zero lag, got %+v", f)
	}
}

func TestPercentileAndTiming(t *testing.T) {
	if report.Percentile(nil, 0.5) != 0 {
		t.Fatal("percentile of nothing must be 0")
	}
	if got := report.Percentile([]float64{7}, 0.95); got != 7 {
		t.Fatalf("single sample = %v", got)
	}
	if got := report.Percentile([]float64{10, 20, 30, 40}, 0.5); got != 25 {
		t.Fatalf("p50 = %v, want 25", got)
	}
	if got := report.Percentile([]float64{10, 20, 30, 40}, 0.95); math.Abs(got-38.5) > 1e-9 {
		t.Fatalf("p95 = %v, want 38.5", got)
	}
	if tm := report.TimingOf(nil); tm.Count != 0 || tm.P50 != nil || tm.P95 != nil {
		t.Fatalf("empty timing = %+v", tm)
	}
	in := []float64{40, 10, 30, 20}
	tm := report.TimingOf(in)
	if tm.Count != 4 || *tm.P50 != 25 || in[0] != 40 {
		t.Fatalf("timing = %+v (input must not be reordered: %v)", tm, in)
	}
}

func TestAdoptionRate(t *testing.T) {
	if report.AdoptionRate(0, 0) != nil {
		t.Fatal("no ended hint must read nil, not 0")
	}
	if r := report.AdoptionRate(3, 1); r == nil || *r != 0.75 {
		t.Fatalf("rate = %v, want 0.75", r)
	}
	if r := report.AdoptionRate(0, 2); r == nil || *r != 0 {
		t.Fatalf("rate = %v, want 0", r)
	}
}

func TestValidate(t *testing.T) {
	ok := func() report.Event {
		return report.Event{Kind: report.KindHintIssued, EventID: "e1", At: now, HintID: "h", Station: "s", SiteID: "x", CandidateCount: 1}
	}
	if err := ok().Validate(); err != nil {
		t.Fatalf("a valid event: %v", err)
	}
	policy := report.Event{Kind: report.KindPolicyDeclared, EventID: "p", At: now, SiteID: "x"}
	if err := policy.Validate(); err != nil {
		t.Fatalf("a valid policy event: %v", err)
	}
	cases := map[string]func(e *report.Event){
		"unknown kind":          func(e *report.Event) { e.Kind = "Nope" },
		"no id":                 func(e *report.Event) { e.EventID = "" },
		"no time":               func(e *report.Event) { e.At = time.Time{} },
		"no site":               func(e *report.Event) { e.SiteID = "" },
		"no hint":               func(e *report.Event) { e.HintID = "" },
		"no station":            func(e *report.Event) { e.Station = "" },
		"issued, no candidates": func(e *report.Event) { e.CandidateCount = 0 },
		"consumed, rank 0":      func(e *report.Event) { e.Kind, e.Rank = report.KindHintConsumed, 0 },
		"consumed, negative":    func(e *report.Event) { e.Kind, e.Rank, e.DetourM = report.KindHintConsumed, 1, -1 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			e := ok()
			mutate(&e)
			if err := e.Validate(); !errors.Is(err, report.ErrInvalidEvent) {
				t.Fatalf("err = %v, want ErrInvalidEvent", err)
			}
		})
	}
}

func TestMilestonesOf(t *testing.T) {
	for kind, want := range map[report.Kind][3]bool{
		report.KindHintIssued:     {true, false, false},
		report.KindHintConsumed:   {false, true, false},
		report.KindHintExpired:    {false, false, true},
		report.KindPolicyDeclared: {false, false, false},
	} {
		m := report.MilestonesOf(report.Event{Kind: kind, At: now})
		got := [3]bool{m.Issued != nil, m.Consumed != nil, m.Expired != nil}
		if got != want {
			t.Errorf("%s milestones = %v, want %v", kind, got, want)
		}
	}
}

func TestKinds(t *testing.T) {
	if len(report.Kinds()) != 4 {
		t.Fatalf("kinds = %v", report.Kinds())
	}
	for _, k := range report.Kinds() {
		if !k.Known() {
			t.Errorf("%s not known", k)
		}
	}
	if report.Kind("x").Known() || report.KindPolicyDeclared.IsHint() || !report.KindHintExpired.IsHint() {
		t.Fatal("Known/IsHint wrong")
	}
}
