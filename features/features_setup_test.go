package features_test

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/claudioed/task-interleaving/internal/application/usecases"
)

const policyPattern = `with max detour (\d+(?:\.\d+)?) m, up to (\d+) candidates and a (\d+) s hint ttl for "([^"]*)"`

func registerSetupSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^site "([^"]*)" has these locations:$`, w.siteHasLocations)
	sc.Step(`^the location "([^"]*)" of site "([^"]*)" is registered without geometry$`, w.locationWithoutGeometry)
	sc.Step(`^the location "([^"]*)" is decommissioned$`, w.decommission)
	sc.Step(`^the geometry of "([^"]*)" is updated to (\d+(?:\.\d+)?), (\d+(?:\.\d+)?), (\d+(?:\.\d+)?)$`, w.geometryUpdated)
	sc.Step(`^task "([^"]*)" of type "([^"]*)" is available at site "([^"]*)" from "([^"]*)" to "([^"]*)"$`, w.taskAvailable)
	sc.Step(`^task "([^"]*)" of type "([^"]*)" is available at site "([^"]*)" from "([^"]*)" to nowhere$`, w.taskAvailableWithoutEnd)
	sc.Step(`^task "([^"]*)" is available without a site$`, w.taskWithoutSite)
	sc.Step(`^station "([^"]*)" claims task "([^"]*)"$`, w.stationClaims)
	sc.Step(`^(\d+) seconds pass$`, w.secondsPass)
}

// siteHasLocations registers the site's zone and the locations of the table
// (code | x | y | z); blank coordinates leave the geometry unknown.
func (w *world) siteHasLocations(site string, table *godog.Table) error {
	zoneID := "z-" + site
	zone := usecases.ZoneChange{ZoneID: zoneID, SiteCode: site, ZoneCode: "FWD"}
	w.deliver(func(id string) (usecases.Outcome, error) { return w.uc.zone.Handle(w.ctx, id, zone) })
	for _, row := range table.Rows[1:] {
		cells := make([]string, 0, len(row.Cells))
		for _, c := range row.Cells {
			cells = append(cells, strings.TrimSpace(c.Value))
		}
		if err := w.registerLocation(zoneID, cells); err != nil {
			return err
		}
	}
	return nil
}

func (w *world) registerLocation(zoneID string, cells []string) error {
	if len(cells) != 4 {
		return fmt.Errorf("a location row needs code, x, y, z: %v", cells)
	}
	loc := usecases.LocationChange{Code: cells[0], ZoneID: zoneID}
	w.deliver(func(id string) (usecases.Outcome, error) { return w.uc.location.Handle(w.ctx, id, loc) })
	if cells[1] == "" {
		return nil
	}
	x, y, z, err := parsePoint(cells[1], cells[2], cells[3])
	if err != nil {
		return err
	}
	return w.geometryUpdated(cells[0], x, y, z)
}

func parsePoint(xs, ys, zs string) (x, y, z float64, err error) {
	for i, s := range []string{xs, ys, zs} {
		v, perr := strconv.ParseFloat(s, 64)
		if perr != nil {
			return 0, 0, 0, fmt.Errorf("coordinate %q: %w", s, perr)
		}
		switch i {
		case 0:
			x = v
		case 1:
			y = v
		default:
			z = v
		}
	}
	return x, y, z, nil
}

func (w *world) locationWithoutGeometry(code, site string) error {
	zone := usecases.ZoneChange{ZoneID: "z-" + site, SiteCode: site, ZoneCode: "FWD"}
	w.deliver(func(id string) (usecases.Outcome, error) { return w.uc.zone.Handle(w.ctx, id, zone) })
	loc := usecases.LocationChange{Code: code, ZoneID: zone.ZoneID}
	w.deliver(func(id string) (usecases.Outcome, error) { return w.uc.location.Handle(w.ctx, id, loc) })
	return nil
}

func (w *world) decommission(code string) error {
	w.deliver(func(id string) (usecases.Outcome, error) { return w.uc.decommissioned.Handle(w.ctx, id, code) })
	return nil
}

func (w *world) geometryUpdated(code string, x, y, z float64) error {
	g := usecases.GeometryChange{Code: code, X: x, Y: y, Z: z}
	w.deliver(func(id string) (usecases.Outcome, error) { return w.uc.geometry.Handle(w.ctx, id, g) })
	return nil
}

func (w *world) taskAvailable(task, typ, site, from, to string) error {
	c := usecases.TaskAvailability{
		TaskID: task, TaskType: typ, SiteID: site, FromLocation: from, ToLocation: to,
		RequiredCapabilities: []string{"RF"}, PriorityBand: "B1",
	}
	w.deliver(func(id string) (usecases.Outcome, error) { return w.uc.available.Handle(w.ctx, id, c) })
	return nil
}

func (w *world) taskAvailableWithoutEnd(task, typ, site, from string) error {
	return w.taskAvailable(task, typ, site, from, "")
}

func (w *world) taskWithoutSite(task string) error {
	c := usecases.TaskAvailability{TaskID: task, TaskType: "PICK", FromLocation: "A"}
	w.deliver(func(id string) (usecases.Outcome, error) { return w.uc.available.Handle(w.ctx, id, c) })
	return nil
}

func (w *world) stationClaims(station, task string) error {
	c := usecases.TaskClaim{TaskID: task, TaskType: "PICK", StationID: station}
	w.deliver(func(id string) (usecases.Outcome, error) { return w.uc.claimed.Handle(w.ctx, id, c) })
	return nil
}

func (w *world) secondsPass(n int) error {
	w.clock.now = w.clock.now.Add(time.Duration(n) * time.Second)
	return nil
}

func registerPolicySteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^site "([^"]*)" declares a policy `+policyPattern+`$`, w.declare)
	sc.Step(`^site "([^"]*)" declares a policy `+policyPattern+` and vertical weight (\d+(?:\.\d+)?)$`, w.declareWeighted)
	sc.Step(`^I declare the policy of site "([^"]*)" `+policyPattern+`$`, w.declare)
	sc.Step(`^I declare the policy of site "([^"]*)" expecting version (\d+) `+policyPattern+`$`, w.replace)
	sc.Step(`^I declare the policy of site "([^"]*)" expecting version (\d+) `+policyPattern+` and vertical weight (\d+(?:\.\d+)?)$`, w.replaceWeighted)
	sc.Step(`^I declare the policy of site "([^"]*)" for task types "([^"]*)"$`, w.declareTypes)
	sc.Step(`^I read the policy of site "([^"]*)"$`, w.readPolicy)
}

func policyInput(site string, detour float64, candidates, ttl int, types string) usecases.DeclarePolicyInput {
	return usecases.DeclarePolicyInput{
		SiteID: site, MaxDetourM: detour, MaxCandidates: candidates,
		HintTTL: time.Duration(ttl) * time.Second, EligibleTaskTypes: splitList(types),
	}
}

func (w *world) declareInput(in usecases.DeclarePolicyInput) {
	w.run(func() error {
		rec, err := w.uc.declarePolicy.Handle(w.ctx, in)
		if err == nil {
			w.policy = rec
		}
		return err
	})
}

func (w *world) declare(site string, detour float64, candidates, ttl int, types string) error {
	w.declareInput(policyInput(site, detour, candidates, ttl, types))
	return nil
}

func (w *world) declareWeighted(site string, detour float64, candidates, ttl int, types string, weight float64) error {
	in := policyInput(site, detour, candidates, ttl, types)
	in.VerticalWeight = &weight
	w.declareInput(in)
	return nil
}

func (w *world) replace(site string, version int64, detour float64, candidates, ttl int, types string) error {
	in := policyInput(site, detour, candidates, ttl, types)
	in.IfMatch = &version
	w.declareInput(in)
	return nil
}

func (w *world) replaceWeighted(site string, version int64, detour float64, candidates, ttl int, types string, weight float64) error {
	in := policyInput(site, detour, candidates, ttl, types)
	in.IfMatch, in.VerticalWeight = &version, &weight
	w.declareInput(in)
	return nil
}

func (w *world) declareTypes(site, types string) error {
	w.declareInput(policyInput(site, 25, 3, 90, types))
	return nil
}

func (w *world) readPolicy(site string) error {
	w.run(func() error {
		rec, err := w.uc.getPolicy.Handle(w.ctx, site)
		if err == nil {
			w.policy = rec
		}
		return err
	})
	return nil
}

func splitList(raw string) []string {
	out := []string{}
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
