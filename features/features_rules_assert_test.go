package features_test

import (
	"fmt"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/claudioed/task-interleaving/internal/application/usecases"
	"github.com/claudioed/task-interleaving/internal/domain/stationtrip"
)

func registerRulesAssertionSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the last HintIssued follows task "([^"]*)" from origin "([^"]*)" under policy version (\d+) and expires at "([^"]*)"$`, w.issuedFollows)
	sc.Step(`^the last HintIssued supersedes hint number (\d+)$`, w.issuedSupersedes)
	sc.Step(`^the last HintIssued supersedes no hint$`, func() error { return w.issuedSupersedes(0) })
	sc.Step(`^the last HintConsumed is of task "([^"]*)" at rank (\d+) with detour (\d+(?:\.\d+)?) m$`, w.consumedIs)
	sc.Step(`^the last HintExpired is of hint number (\d+) with expiry "([^"]*)"$`, w.expiredIs)
	sc.Step(`^hint number (\d+) was superseded at "([^"]*)"$`, w.supersededAt)
	sc.Step(`^hint number (\d+) was consumed by task "([^"]*)" at "([^"]*)"$`, w.consumedBy)
	sc.Step(`^the last announced policy declaration is version (\d+) of site "([^"]*)" with max detour (\d+(?:\.\d+)?) m, (\d+) candidates, a (\d+) s ttl, types "([^"]*)" and vertical weight (\d+(?:\.\d+)?), declared at "([^"]*)"$`, w.declarationIs)
	sc.Step(`^station "([^"]*)" last completed task "([^"]*)" at "([^"]*)"$`, w.lastCompleted)
	sc.Step(`^the page lists hint numbers "([^"]*)"$`, w.pageLists)
	sc.Step(`^the page has a next cursor$`, func() error { return w.pageCursor(true) })
	sc.Step(`^the page has no next cursor$`, func() error { return w.pageCursor(false) })
	sc.Step(`^(\d+) hints? (?:is|are) in state "([^"]*)"$`, w.hintsInState)
	sc.Step(`^the simulated candidate (\d+) is "([^"]*)" with detour (\d+(?:\.\d+)?) m$`, w.simulatedCandidate)
}

// lastTripEvent is the newest published station fact with the given name.
func (w *world) lastTripEvent(name string) (stationtrip.Event, error) {
	events := w.log.TripEvents()
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].EventName() == name {
			return events[i], nil
		}
	}
	return nil, fmt.Errorf("no %s was published (published: %s)", name, strings.Join(w.log.Names(), ","))
}

func (w *world) issuedFollows(task, origin string, version int64, expires string) error {
	e, err := w.lastTripEvent("HintIssued")
	if err != nil {
		return err
	}
	want, err := mustTime(expires)
	if err != nil {
		return err
	}
	h := e.(stationtrip.HintIssued)
	if string(h.AfterTask) != task || h.Origin != origin || h.PolicyVersion != version || !h.ExpiresAt.Equal(want) {
		return fmt.Errorf("HintIssued = after %s from %s under v%d expiring %s; want %s, %s, v%d, %s",
			h.AfterTask, h.Origin, h.PolicyVersion, h.ExpiresAt.Format(time.RFC3339), task, origin, version, expires)
	}
	return nil
}

func (w *world) issuedSupersedes(n int) error {
	e, err := w.lastTripEvent("HintIssued")
	if err != nil {
		return err
	}
	want := ""
	if n > 0 {
		want = hintID(n)
	}
	if got := string(e.(stationtrip.HintIssued).SupersededHintID); got != want {
		return fmt.Errorf("HintIssued supersedes %q, want %q", got, want)
	}
	return nil
}

func (w *world) consumedIs(task string, rank int, detour float64) error {
	e, err := w.lastTripEvent("HintConsumed")
	if err != nil {
		return err
	}
	h := e.(stationtrip.HintConsumed)
	if string(h.TaskID) != task || h.Rank != rank || h.DetourM != detour {
		return fmt.Errorf("HintConsumed = %s rank %d detour %v; want %s rank %d detour %v",
			h.TaskID, h.Rank, h.DetourM, task, rank, detour)
	}
	return nil
}

func (w *world) expiredIs(n int, expiry string) error {
	e, err := w.lastTripEvent("HintExpired")
	if err != nil {
		return err
	}
	want, err := mustTime(expiry)
	if err != nil {
		return err
	}
	h := e.(stationtrip.HintExpired)
	if h.HintID != stationtrip.HintID(hintID(n)) || !h.ExpiresAt.Equal(want) {
		return fmt.Errorf("HintExpired = %s expiring %s; want %s expiring %s", h.HintID, h.ExpiresAt, hintID(n), expiry)
	}
	return nil
}

func (w *world) supersededAt(n int, at string) error {
	rec, err := w.uc.getHint.Handle(w.ctx, hintID(n))
	if err != nil {
		return err
	}
	want, err := mustTime(at)
	if err != nil {
		return err
	}
	if !rec.SupersededAt.Equal(want) {
		return fmt.Errorf("hint %d superseded at %v, want %s", n, rec.SupersededAt, at)
	}
	return nil
}

func (w *world) consumedBy(n int, task, at string) error {
	rec, err := w.uc.getHint.Handle(w.ctx, hintID(n))
	if err != nil {
		return err
	}
	want, err := mustTime(at)
	if err != nil {
		return err
	}
	if string(rec.Hint.ConsumedTask) != task || !rec.Hint.ConsumedAt.Equal(want) {
		return fmt.Errorf("hint %d consumed by %q at %v, want %q at %s", n, rec.Hint.ConsumedTask, rec.Hint.ConsumedAt, task, at)
	}
	return nil
}

func (w *world) declarationIs(version int64, site string, detour float64, candidates, ttl int, types string, weight float64, at string) error {
	ds := w.log.Declarations()
	if len(ds) == 0 {
		return fmt.Errorf("no policy declaration was announced")
	}
	d := ds[len(ds)-1]
	when, err := mustTime(at)
	if err != nil {
		return err
	}
	if d.Version != version || d.SiteID != site || d.MaxDetourM != detour || d.MaxCandidates != candidates ||
		int(d.HintTTLSeconds) != ttl || strings.Join(d.EligibleTaskTypes, ",") != types ||
		d.VerticalWeight != weight || !d.DeclaredAt.Equal(when) {
		return fmt.Errorf("announced %+v", d)
	}
	return nil
}

func (w *world) lastCompleted(station, task, at string) error {
	trip, err := w.trip(station)
	if err != nil {
		return fmt.Errorf("station %q: %w", station, err)
	}
	want, err := mustTime(at)
	if err != nil {
		return err
	}
	gotTask, gotAt := trip.LastCompleted()
	if string(gotTask) != task || !gotAt.Equal(want) {
		return fmt.Errorf("station %q last completed %s at %v, want %s at %s", station, gotTask, gotAt, task, at)
	}
	return nil
}

func (w *world) pageLists(want string) error {
	w.errChecked = true
	if w.err != nil {
		return fmt.Errorf("the listing failed: %w", w.err)
	}
	var wantIDs []string
	for _, n := range splitList(want) {
		var num int
		if _, err := fmt.Sscanf(n, "%d", &num); err != nil {
			return fmt.Errorf("hint number %q: %w", n, err)
		}
		wantIDs = append(wantIDs, hintID(num))
	}
	var got []string
	for _, rec := range w.rules.page.Items {
		got = append(got, string(rec.Hint.ID))
	}
	if strings.Join(got, ",") != strings.Join(wantIDs, ",") {
		return fmt.Errorf("the page lists %v, want %v", got, wantIDs)
	}
	return nil
}

func (w *world) pageCursor(want bool) error {
	if has := w.rules.page.NextCursor != ""; has != want {
		return fmt.Errorf("next cursor present = %v, want %v", has, want)
	}
	return nil
}

func (w *world) hintsInState(want int, state string) error {
	page, err := w.uc.listHints.Handle(w.ctx, usecases.ListHintsQuery{State: state, Limit: usecases.MaxListLimit})
	if err != nil {
		return err
	}
	if len(page.Items) != want {
		return fmt.Errorf("%d hints are %s, want %d", len(page.Items), state, want)
	}
	return nil
}

func (w *world) simulatedCandidate(rank int, task string, detour float64) error {
	w.errChecked = true
	if w.err != nil {
		return fmt.Errorf("the simulation failed: %w", w.err)
	}
	for _, c := range w.sim.Candidates {
		if c.Rank != rank {
			continue
		}
		if string(c.TaskID) != task || c.DetourM != detour {
			return fmt.Errorf("simulated candidate %d = %s at %v m, want %s at %v m", rank, c.TaskID, c.DetourM, task, detour)
		}
		return nil
	}
	return fmt.Errorf("the simulation has no candidate of rank %d", rank)
}
