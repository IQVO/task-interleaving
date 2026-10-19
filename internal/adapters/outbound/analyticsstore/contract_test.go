package analyticsstore

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/claudioed/task-interleaving/internal/analytics/report"
)

// store is what the contract exercises: the writer and the reader over one
// model. The SAME contract runs against Memory (unit) and Postgres
// (integration), so the two stores cannot drift.
type store interface {
	report.Projection
	report.Reader
}

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func issued(id, hint, site, when string) report.Event {
	return report.Event{
		Kind: report.KindHintIssued, EventID: id, At: at(when), HintID: hint, Station: "STN-07", SiteID: site,
		ExpiresAt: at(when).Add(90 * time.Second), CandidateCount: 2,
	}
}

func consumed(id, hint, site, when string, rank int, detour float64) report.Event {
	return report.Event{
		Kind: report.KindHintConsumed, EventID: id, At: at(when), HintID: hint, Station: "STN-07", SiteID: site,
		Rank: rank, DetourM: detour,
	}
}

func expired(id, hint, site, when string) report.Event {
	return report.Event{Kind: report.KindHintExpired, EventID: id, At: at(when), HintID: hint, Station: "STN-07", SiteID: site}
}

func declared(id, site, when string) report.Event {
	return report.Event{Kind: report.KindPolicyDeclared, EventID: id, At: at(when), SiteID: site}
}

func mustApply(t *testing.T, s store, events ...report.Event) {
	t.Helper()
	for _, e := range events {
		if _, err := s.Apply(context.Background(), e); err != nil {
			t.Fatalf("apply %s %s: %v", e.Kind, e.EventID, err)
		}
	}
}

func adoption(t *testing.T, s store, from, to, site string) []report.AdoptionDay {
	t.Helper()
	got, err := s.AdoptionDays(context.Background(), report.AdoptionQuery{
		Range: report.Range{From: at(from), To: at(to)}, SiteID: site, Granularity: report.GranularityDay,
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func approx(a, b *float64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return math.Abs(*a-*b) < 1e-6
}

func f(v float64) *float64 { return &v }

// runContract is the shared behavioural contract of the analytical store.
func runContract(t *testing.T, newStore func(t *testing.T) store) {
	t.Helper()
	runWriterContract(t, newStore)
	runMilestonesContract(t, newStore)
	runDayRowContract(t, newStore)
	runSiteScopeContract(t, newStore)
	runAdoptionContract(t, newStore)
	runPercentileContract(t, newStore)
	runFreshnessContract(t, newStore)
}

// runWriterContract pins the writer semantics: deterministic rejection of
// invalid events, idempotent replay.
func runWriterContract(t *testing.T, newStore func(t *testing.T) store) {
	t.Helper()
	t.Run("invalid events are rejected deterministically", func(t *testing.T) {
		s := newStore(t)
		bad := issued("", "hint-1", "SITE-1", "2026-10-05T10:00:00Z")
		if _, err := s.Apply(context.Background(), bad); !errors.Is(err, report.ErrRejected) {
			t.Fatalf("err = %v, want ErrRejected", err)
		}
	})

	t.Run("a replayed event id is a no-op", func(t *testing.T) {
		s := newStore(t)
		e := issued("e1", "hint-1", "SITE-1", "2026-10-05T10:00:00Z")
		if applied, err := s.Apply(context.Background(), e); err != nil || !applied {
			t.Fatalf("first apply = %v, %v", applied, err)
		}
		if applied, err := s.Apply(context.Background(), e); err != nil || applied {
			t.Fatalf("replay = %v, %v; want false, nil", applied, err)
		}
		days := adoption(t, s, "2026-10-05T00:00:00Z", "2026-10-06T00:00:00Z", "")
		if len(days) != 1 || days[0].Issued != 1 {
			t.Fatalf("days = %+v, want one issue counted once", days)
		}
	})

	t.Run("a policy declaration is recorded without a hint fact", func(t *testing.T) {
		s := newStore(t)
		mustApply(t, s, declared("p1", "SITE-1", "2026-10-05T09:00:00Z"))
		days := adoption(t, s, "2026-10-05T00:00:00Z", "2026-10-06T00:00:00Z", "")
		if days[0].Issued != 0 || days[0].Consumed != 0 || days[0].Expired != 0 {
			t.Fatalf("day = %+v, want no hint counted", days[0])
		}
		if got, err := s.LastEventAt(context.Background()); err != nil || got == nil || !got.Equal(at("2026-10-05T09:00:00Z")) {
			t.Fatalf("LastEventAt = %v, %v; the declaration must advance the freshness", got, err)
		}
	})
}

// runMilestonesContract pins that milestones converge whatever order the
// events arrive in.
func runMilestonesContract(t *testing.T, newStore func(t *testing.T) store) {
	t.Helper()
	t.Run("milestones converge whatever order the events arrive in", func(t *testing.T) {
		s := newStore(t)
		mustApply(t, s,
			consumed("e3", "hint-1", "SITE-1", "2026-10-05T10:00:20Z", 1, 4.5),
			expired("e4", "hint-2", "SITE-1", "2026-10-06T08:01:30Z"),
			issued("e1", "hint-1", "SITE-1", "2026-10-05T10:00:00Z"),
			issued("e2", "hint-2", "SITE-1", "2026-10-06T08:00:00Z"),
		)
		days := adoption(t, s, "2026-10-05T00:00:00Z", "2026-10-07T00:00:00Z", "")
		if len(days) != 2 {
			t.Fatalf("days = %d, want 2", len(days))
		}
		if days[0].Issued != 1 || days[0].Consumed != 1 || days[0].Expired != 0 {
			t.Fatalf("day 5 = %+v", days[0])
		}
		if days[1].Issued != 1 || days[1].Consumed != 0 || days[1].Expired != 1 {
			t.Fatalf("day 6 = %+v", days[1])
		}
		// hint-1 was issued and consumed 20 s later; the events were applied
		// out of order and still agree.
		ttc := days[0].TimeToConsume
		if ttc.Count != 1 || !approx(ttc.P50, f(20)) || !approx(ttc.P95, f(20)) {
			t.Fatalf("day 5 time to consume = %+v, want one sample of 20 s", ttc)
		}
	})
}

// runDayRowContract pins that days are dense and windowed.
func runDayRowContract(t *testing.T, newStore func(t *testing.T) store) {
	t.Helper()
	t.Run("days are dense and windowed, not per event", func(t *testing.T) {
		s := newStore(t)
		mustApply(t, s, issued("e1", "hint-1", "SITE-1", "2026-10-05T23:30:00Z"))
		// A range that starts and ends mid-day: the counts cover only the part
		// of the day inside it, and empty days still appear.
		days := adoption(t, s, "2026-10-05T23:00:00Z", "2026-10-08T01:00:00Z", "")
		if len(days) != 4 {
			t.Fatalf("days = %d, want 4 (three whole days plus the partials)", len(days))
		}
		if days[0].Issued != 1 || days[1].Issued != 0 || days[3].Issued != 0 {
			t.Fatalf("dense days = %+v", days)
		}
		if got := days[1].TimeToConsume; got.Count != 0 || got.P50 != nil || got.P95 != nil {
			t.Fatalf("an empty day's time to consume = %+v, want count 0 and null percentiles", got)
		}
		if days[1].MeanDetourM != nil {
			t.Fatalf("an empty day's mean detour = %v, want nil", *days[1].MeanDetourM)
		}
	})

	t.Run("a milestone at the end of the range is out, at the start is in", func(t *testing.T) {
		s := newStore(t)
		mustApply(t, s,
			issued("e1", "hint-1", "SITE-1", "2026-10-05T00:00:00Z"),
			issued("e2", "hint-2", "SITE-1", "2026-10-06T00:00:00Z"),
		)
		days := adoption(t, s, "2026-10-05T00:00:00Z", "2026-10-06T00:00:00Z", "")
		if len(days) != 1 || days[0].Issued != 1 {
			t.Fatalf("days = %+v, want only the hint at exactly from", days)
		}
	})
}

// runSiteScopeContract pins the optional site scope.
func runSiteScopeContract(t *testing.T, newStore func(t *testing.T) store) {
	t.Helper()
	t.Run("the site scope narrows every counter", func(t *testing.T) {
		s := newStore(t)
		mustApply(t, s,
			issued("e1", "hint-1", "SITE-1", "2026-10-05T10:00:00Z"),
			issued("e2", "hint-2", "SITE-2", "2026-10-05T10:30:00Z"),
			consumed("e3", "hint-2", "SITE-2", "2026-10-05T10:31:00Z", 2, 12),
			consumed("e4", "hint-3", "SITE-3", "2026-10-05T11:30:00Z", 1, 3),
		)
		all := adoption(t, s, "2026-10-05T00:00:00Z", "2026-10-06T00:00:00Z", "")
		if all[0].Issued != 2 || all[0].Consumed != 2 {
			t.Fatalf("unscoped day = %+v", all[0])
		}
		s1 := adoption(t, s, "2026-10-05T00:00:00Z", "2026-10-06T00:00:00Z", "SITE-1")
		if s1[0].Issued != 1 || s1[0].Consumed != 0 || s1[0].TimeToConsume.Count != 0 {
			t.Fatalf("SITE-1 day = %+v", s1[0])
		}
		s2 := adoption(t, s, "2026-10-05T00:00:00Z", "2026-10-06T00:00:00Z", "SITE-2")
		if s2[0].Issued != 1 || s2[0].Consumed != 1 || !approx(s2[0].TimeToConsume.P50, f(60)) {
			t.Fatalf("SITE-2 day = %+v; the 60-second time to consume must be counted", s2[0])
		}
		s3 := adoption(t, s, "2026-10-05T00:00:00Z", "2026-10-06T00:00:00Z", "SITE-3")
		if s3[0].Issued != 0 || s3[0].Consumed != 1 || s3[0].TimeToConsume.Count != 0 {
			t.Fatalf("SITE-3 day = %+v; a consumption without a seen issue has no time to consume", s3[0])
		}
	})
}

// runAdoptionContract pins the consumed-hint statistics.
func runAdoptionContract(t *testing.T, newStore func(t *testing.T) store) {
	t.Helper()
	t.Run("first-rank share and mean detour cover the consumed hints", func(t *testing.T) {
		s := newStore(t)
		mustApply(t, s,
			consumed("e1", "hint-1", "SITE-1", "2026-10-05T10:00:00Z", 1, 4),
			consumed("e2", "hint-2", "SITE-1", "2026-10-05T11:00:00Z", 2, 10),
			consumed("e3", "hint-3", "SITE-1", "2026-10-05T12:00:00Z", 1, 1),
			expired("e4", "hint-4", "SITE-1", "2026-10-05T13:00:00Z"),
		)
		d := adoption(t, s, "2026-10-05T00:00:00Z", "2026-10-06T00:00:00Z", "")[0]
		if d.Consumed != 3 || d.ConsumedFirstRank != 2 || d.Expired != 1 || !approx(d.MeanDetourM, f(5)) {
			t.Fatalf("day = %+v, want 3 consumed (2 first rank, mean detour 5) and 1 expired", d)
		}
	})

	t.Run("a hint expiring and being consumed counts in both days", func(t *testing.T) {
		// Out-of-order delivery of two contradictory facts must not lose either.
		s := newStore(t)
		mustApply(t, s,
			expired("e1", "hint-1", "SITE-1", "2026-10-05T10:01:30Z"),
			consumed("e2", "hint-1", "SITE-1", "2026-10-06T10:00:10Z", 1, 2),
		)
		days := adoption(t, s, "2026-10-05T00:00:00Z", "2026-10-07T00:00:00Z", "")
		if days[0].Expired != 1 || days[1].Consumed != 1 {
			t.Fatalf("days = %+v", days)
		}
	})
}

// runPercentileContract pins the time-to-consume percentile definition.
func runPercentileContract(t *testing.T, newStore func(t *testing.T) store) {
	t.Helper()
	t.Run("time to consume percentiles are linear-interpolated", func(t *testing.T) {
		s := newStore(t)
		// Four hints issued at 08:00:00 and consumed 10, 20, 30 and 40 s later.
		for i := range 4 {
			hint := "hint-" + string(rune('1'+i))
			mustApply(t, s,
				issued("i"+hint, hint, "SITE-1", "2026-10-05T08:00:00Z"),
				consumed("c"+hint, hint, "SITE-1", at("2026-10-05T08:00:00Z").Add(time.Duration(i+1)*10*time.Second).Format(time.RFC3339), 1, 1),
			)
		}
		d := adoption(t, s, "2026-10-05T00:00:00Z", "2026-10-06T00:00:00Z", "")[0]
		ttc := d.TimeToConsume
		if ttc.Count != 4 || !approx(ttc.P50, f(25)) || !approx(ttc.P95, f(38.5)) {
			t.Fatalf("time to consume = %+v, want count 4, p50 25, p95 38.5 by linear interpolation", ttc)
		}
	})
}

// runFreshnessContract pins the freshness basis: the newest applied event.
func runFreshnessContract(t *testing.T, newStore func(t *testing.T) store) {
	t.Helper()
	t.Run("freshness is the newest applied event time", func(t *testing.T) {
		s := newStore(t)
		if got, err := s.LastEventAt(context.Background()); err != nil || got != nil {
			t.Fatalf("empty LastEventAt = %v, %v; want nil", got, err)
		}
		mustApply(t, s,
			issued("e1", "hint-1", "SITE-1", "2026-10-05T10:00:00Z"),
			expired("e2", "hint-2", "SITE-1", "2026-10-07T09:00:00Z"),
			issued("e3", "hint-3", "SITE-1", "2026-10-06T09:00:00Z"),
		)
		got, err := s.LastEventAt(context.Background())
		if err != nil || got == nil || !got.Equal(at("2026-10-07T09:00:00Z")) {
			t.Fatalf("LastEventAt = %v, %v; want 2026-10-07T09:00:00Z", got, err)
		}
	})
}

func TestMemory_Contract(t *testing.T) {
	runContract(t, func(*testing.T) store { return NewMemory() })
}
