package features_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/cucumber/godog"

	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/application/usecases"
	"github.com/claudioed/task-interleaving/internal/domain/recommend"
	"github.com/claudioed/task-interleaving/internal/domain/stationtrip"
)

func registerAssertionSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the command succeeds$`, w.commandSucceeds)
	sc.Step(`^the command fails with "([^"]*)"$`, w.commandFails)
	sc.Step(`^the consumer outcome is "([^"]*)"$`, w.outcomeIs)
	sc.Step(`^the published events are "([^"]*)"$`, w.eventsAre)
	sc.Step(`^no event is published$`, func() error { return w.eventsAre("") })
	sc.Step(`^the sweep expired (\d+) hints?$`, w.sweepExpired)
	registerHintAssertions(sc, w)
	registerPolicyAssertions(sc, w)
	registerSimulationAssertions(sc, w)
}

func registerHintAssertions(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^station "([^"]*)" has an active hint listing "([^"]*)"$`, w.activeHintLists)
	sc.Step(`^station "([^"]*)" has no active hint$`, w.noActiveHint)
	sc.Step(`^station "([^"]*)" stands at "([^"]*)"$`, w.stationStandsAt)
	sc.Step(`^station "([^"]*)" is unknown$`, w.stationUnknown)
	sc.Step(`^candidate (\d+) of the active hint of station "([^"]*)" is "([^"]*)" with detour (\d+(?:\.\d+)?) m$`, w.candidateIs)
	sc.Step(`^the active hint of station "([^"]*)" expires in (\d+) seconds$`, w.hintExpiresIn)
	sc.Step(`^hint number (\d+) is "([^"]*)"$`, w.hintIsInState)
	sc.Step(`^(\d+) hints? (?:was|were) issued$`, w.hintsIssued)
}

func registerPolicyAssertions(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the policy is at version (\d+) with (\d+) candidates and a (\d+) s hint ttl$`, w.policyIs)
	sc.Step(`^the policy has vertical weight (\d+(?:\.\d+)?) and task types "([^"]*)"$`, w.policyDetails)
	sc.Step(`^site "([^"]*)" has no policy$`, w.noPolicy)
}

func registerSimulationAssertions(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the simulation lists "([^"]*)"$`, w.simulationLists)
	sc.Step(`^the simulation lists nothing$`, func() error { return w.simulationLists("") })
	sc.Step(`^the simulation is for site "([^"]*)" from "([^"]*)" under policy version (\d+)$`, w.simulationIs)
}

func (w *world) commandSucceeds() error {
	w.errChecked = true
	if w.err != nil {
		return fmt.Errorf("the command failed: %w", w.err)
	}
	return nil
}

func (w *world) commandFails(want string) error {
	w.errChecked = true
	if w.err == nil {
		return fmt.Errorf("the command succeeded, want an error containing %q", want)
	}
	if !strings.Contains(w.err.Error(), want) {
		return fmt.Errorf("the command failed with %q, want it to contain %q", w.err, want)
	}
	return nil
}

func (w *world) outcomeIs(want string) error {
	w.errChecked = true
	if w.err != nil {
		return fmt.Errorf("the command failed: %w", w.err)
	}
	if string(w.outcome) != want {
		return fmt.Errorf("outcome = %q, want %q", w.outcome, want)
	}
	return nil
}

func (w *world) eventsAre(want string) error {
	got := strings.Join(w.log.Names(), ",")
	if got != want {
		return fmt.Errorf("published events = %q, want %q", got, want)
	}
	return nil
}

func (w *world) sweepExpired(want int) error {
	w.errChecked = true
	if w.err != nil {
		return fmt.Errorf("the sweep failed: %w", w.err)
	}
	if w.expired != want {
		return fmt.Errorf("the sweep expired %d hints, want %d", w.expired, want)
	}
	return nil
}

func (w *world) trip(station string) (*stationtrip.StationTrip, error) {
	return w.uc.getStation.Handle(w.ctx, station)
}

func (w *world) activeHint(station string) (stationtrip.Hint, error) {
	trip, err := w.trip(station)
	if err != nil {
		return stationtrip.Hint{}, fmt.Errorf("station %q: %w", station, err)
	}
	h, ok := trip.ActiveHint()
	if !ok {
		return stationtrip.Hint{}, fmt.Errorf("station %q has no active hint", station)
	}
	return h, nil
}

func candidateIDs(cs []recommend.Candidate) string {
	ids := make([]string, 0, len(cs))
	for _, c := range cs {
		ids = append(ids, string(c.TaskID))
	}
	return strings.Join(ids, ",")
}

func (w *world) activeHintLists(station, want string) error {
	h, err := w.activeHint(station)
	if err != nil {
		return err
	}
	if got := candidateIDs(h.Candidates); got != want {
		return fmt.Errorf("the hint lists %q, want %q", got, want)
	}
	return nil
}

func (w *world) noActiveHint(station string) error {
	trip, err := w.trip(station)
	if err != nil {
		return fmt.Errorf("station %q: %w", station, err)
	}
	if h, ok := trip.ActiveHint(); ok {
		return fmt.Errorf("station %q has the active hint %s", station, h.ID)
	}
	return nil
}

func (w *world) stationStandsAt(station, location string) error {
	trip, err := w.trip(station)
	if err != nil {
		return fmt.Errorf("station %q: %w", station, err)
	}
	pos, ok := trip.Position()
	if !ok || pos.Place.Location != location {
		return fmt.Errorf("station %q stands at %q (known %v), want %q", station, pos.Place.Location, ok, location)
	}
	return nil
}

func (w *world) stationUnknown(station string) error {
	_, err := w.trip(station)
	if err == nil {
		return fmt.Errorf("station %q is known", station)
	}
	return nil
}

func (w *world) candidateIs(rank int, station, task string, detour float64) error {
	h, err := w.activeHint(station)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(h.Candidates, func(c recommend.Candidate) bool { return c.Rank == rank })
	if i < 0 {
		return fmt.Errorf("the hint has no candidate of rank %d", rank)
	}
	if c := h.Candidates[i]; string(c.TaskID) != task || c.DetourM != detour {
		return fmt.Errorf("candidate %d = %s at %v m, want %s at %v m", rank, c.TaskID, c.DetourM, task, detour)
	}
	return nil
}

func (w *world) hintExpiresIn(station string, seconds int) error {
	h, err := w.activeHint(station)
	if err != nil {
		return err
	}
	if got := int(h.ExpiresAt.Sub(h.IssuedAt).Seconds()); got != seconds {
		return fmt.Errorf("the hint lives %d s, want %d s", got, seconds)
	}
	return nil
}

func (w *world) hintIsInState(n int, want string) error {
	rec, err := w.uc.getHint.Handle(w.ctx, hintID(n))
	if err != nil {
		return fmt.Errorf("hint %d: %w", n, err)
	}
	if string(rec.Hint.State) != want {
		return fmt.Errorf("hint %d is %s, want %s", n, rec.Hint.State, want)
	}
	return nil
}

func (w *world) hintsIssued(want int) error {
	page, err := w.uc.listHints.Handle(w.ctx, usecases.ListHintsQuery{Limit: usecases.MaxListLimit})
	if err != nil {
		return err
	}
	if len(page.Items) != want {
		return fmt.Errorf("%d hints were issued, want %d", len(page.Items), want)
	}
	return nil
}

func (w *world) policyIs(version int64, candidates, ttl int) error {
	w.errChecked = true
	if w.err != nil {
		return fmt.Errorf("the command failed: %w", w.err)
	}
	p := w.policy.Policy
	if p.Version() != version || p.MaxCandidates() != candidates || int(p.HintTTL().Seconds()) != ttl {
		return fmt.Errorf("policy = v%d, %d candidates, %v ttl; want v%d, %d candidates, %d s",
			p.Version(), p.MaxCandidates(), p.HintTTL(), version, candidates, ttl)
	}
	return nil
}

func (w *world) policyDetails(weight float64, types string) error {
	p := w.policy.Policy
	var got []string
	for _, t := range p.EligibleTaskTypes() {
		got = append(got, string(t))
	}
	if p.VerticalWeight() != weight || strings.Join(got, ",") != types {
		return fmt.Errorf("policy weight %v types %v, want %v and %s", p.VerticalWeight(), got, weight, types)
	}
	return nil
}

func (w *world) noPolicy(site string) error {
	_, err := w.uc.getPolicy.Handle(w.ctx, site)
	if !errors.Is(err, repository.ErrPolicyNotFound) {
		return fmt.Errorf("site %q policy lookup = %v, want not found", site, err)
	}
	return nil
}

func (w *world) simulationLists(want string) error {
	w.errChecked = true
	if w.err != nil {
		return fmt.Errorf("the simulation failed: %w", w.err)
	}
	if got := candidateIDs(w.sim.Candidates); got != want {
		return fmt.Errorf("the simulation lists %q, want %q", got, want)
	}
	return nil
}

func (w *world) simulationIs(site, origin string, version int64) error {
	s := w.sim
	if string(s.SiteID) != site || s.OriginLocation != origin || s.PolicyVersion != version {
		return fmt.Errorf("simulation = site %s origin %s policy v%d, want %s %s v%d",
			s.SiteID, s.OriginLocation, s.PolicyVersion, site, origin, version)
	}
	return nil
}
