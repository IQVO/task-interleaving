package features_test

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/claudioed/task-interleaving/internal/application/usecases"
)

// rulesState is the per-scenario state of the documentation-derived steps.
type rulesState struct {
	page usecases.HintPage
}

// registerRulesSteps adds the vocabulary of the documentation-derived rules
// features (*_rules.feature): events with explicit station, instant, priority
// band or CloudEvents id, malformed events, bulk stations and the read side.
func registerRulesSteps(sc *godog.ScenarioContext, w *world) {
	registerRulesEventSteps(sc, w)
	registerRulesInvalidEventSteps(sc, w)
	registerRulesPolicySteps(sc, w)
	registerRulesQuerySteps(sc, w)
	registerRulesAssertionSteps(sc, w)
}

func mustTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("instant %q: %w", s, err)
	}
	return t, nil
}

// deliverWithID hands one consumed event to its use case under the given
// CloudEvents id and remembers how to deliver it again.
func (w *world) deliverWithID(id string, handle func(id string) (usecases.Outcome, error)) {
	w.redeliver = func() {
		w.run(func() error {
			var err error
			w.outcome, err = handle(id)
			return err
		})
	}
	w.redeliver()
}

func registerRulesEventSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^station "([^"]*)" completes task "([^"]*)"$`, w.stationCompletes)
	sc.Step(`^station "([^"]*)" completes task "([^"]*)" at "([^"]*)"$`, w.stationCompletesAt)
	sc.Step(`^station "([^"]*)" claims task "([^"]*)" at "([^"]*)"$`, w.stationClaimsAt)
	sc.Step(`^task "([^"]*)" of type "([^"]*)" is available at site "([^"]*)" from "([^"]*)" to "([^"]*)" in priority band "([^"]*)"$`, w.taskInBand)
	sc.Step(`^a TaskAvailable event with id "([^"]*)" announces task "([^"]*)" at site "([^"]*)" from "([^"]*)"$`, w.availableWithID)
	sc.Step(`^a ZoneRegistered event with id "([^"]*)" registers zone "([^"]*)" of site "([^"]*)"$`, w.zoneWithID)
	sc.Step(`^(\d+) stations complete a task that ends at "([^"]*)" of site "([^"]*)"$`, w.bulkStations)
}

func (w *world) stationCompletes(station, task string) error {
	c := usecases.TaskCompletion{TaskID: task, StationID: station}
	w.deliver(func(id string) (usecases.Outcome, error) { return w.uc.completed.Handle(w.ctx, id, c) })
	return nil
}

func (w *world) stationCompletesAt(station, task, at string) error {
	when, err := mustTime(at)
	if err != nil {
		return err
	}
	c := usecases.TaskCompletion{TaskID: task, StationID: station, At: when}
	w.deliver(func(id string) (usecases.Outcome, error) { return w.uc.completed.Handle(w.ctx, id, c) })
	return nil
}

func (w *world) stationClaimsAt(station, task, at string) error {
	when, err := mustTime(at)
	if err != nil {
		return err
	}
	c := usecases.TaskClaim{TaskID: task, TaskType: "PICK", StationID: station, At: when}
	w.deliver(func(id string) (usecases.Outcome, error) { return w.uc.claimed.Handle(w.ctx, id, c) })
	return nil
}

func (w *world) taskInBand(task, typ, site, from, to, band string) error {
	c := usecases.TaskAvailability{
		TaskID: task, TaskType: typ, SiteID: site, FromLocation: from, ToLocation: to,
		RequiredCapabilities: []string{"RF"}, PriorityBand: band,
	}
	w.deliver(func(id string) (usecases.Outcome, error) { return w.uc.available.Handle(w.ctx, id, c) })
	return nil
}

func (w *world) availableWithID(eventID, task, site, from string) error {
	c := usecases.TaskAvailability{TaskID: task, TaskType: "PICK", SiteID: site, FromLocation: from, ToLocation: from}
	w.deliverWithID(eventID, func(id string) (usecases.Outcome, error) { return w.uc.available.Handle(w.ctx, id, c) })
	return nil
}

func (w *world) zoneWithID(eventID, zone, site string) error {
	c := usecases.ZoneChange{ZoneID: zone, SiteCode: site, ZoneCode: "FWD"}
	w.deliverWithID(eventID, func(id string) (usecases.Outcome, error) { return w.uc.zone.Handle(w.ctx, id, c) })
	return nil
}

// bulkStations makes n stations (BULK-001...) each claim and complete a task
// that ends at the given location, so each ends up with a position and, when
// the pool has candidates, an Active hint.
func (w *world) bulkStations(n int, end, site string) error {
	for i := 1; i <= n; i++ {
		station, task := fmt.Sprintf("BULK-%03d", i), fmt.Sprintf("BT-%03d", i)
		if err := w.taskAvailable(task, "PICK", site, end, end); err != nil {
			return err
		}
		if err := w.stationClaims(station, task); err != nil {
			return err
		}
		if err := w.taskCompleted(task); err != nil {
			return err
		}
	}
	return nil
}

func registerRulesInvalidEventSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^a TaskAvailable event arrives for task "([^"]*)" of type "([^"]*)" at site "([^"]*)" from "([^"]*)"$`, w.invalidAvailable)
	sc.Step(`^a TaskClaimed event arrives for task "([^"]*)" by station "([^"]*)"$`, w.invalidClaim)
	sc.Step(`^a TaskCompleted event arrives for task "([^"]*)"$`, w.invalidCompletion)
	sc.Step(`^a ZoneRegistered event arrives for zone "([^"]*)" of site "([^"]*)"$`, w.invalidZone)
	sc.Step(`^a LocationSlotRegistered event arrives for location "([^"]*)" in zone "([^"]*)"$`, w.invalidLocation)
	sc.Step(`^a LocationSlotDecommissioned event arrives for location "([^"]*)"$`, w.invalidDecommission)
	sc.Step(`^a LocationGeometryUpdated event arrives for location "([^"]*)" at "([^"]*)", "([^"]*)", "([^"]*)"$`, w.invalidGeometry)
	sc.Step(`^a (TaskAvailable|TaskClaimed|TaskCompleted|ZoneRegistered|LocationSlotRegistered|LocationSlotDecommissioned|LocationGeometryUpdated) event arrives without a CloudEvents id$`, w.eventWithoutID)
}

func (w *world) invalidAvailable(task, typ, site, from string) error {
	c := usecases.TaskAvailability{TaskID: task, TaskType: typ, SiteID: site, FromLocation: from, ToLocation: from}
	w.deliver(func(id string) (usecases.Outcome, error) { return w.uc.available.Handle(w.ctx, id, c) })
	return nil
}

func (w *world) invalidClaim(task, station string) error {
	c := usecases.TaskClaim{TaskID: task, TaskType: "PICK", StationID: station}
	w.deliver(func(id string) (usecases.Outcome, error) { return w.uc.claimed.Handle(w.ctx, id, c) })
	return nil
}

func (w *world) invalidCompletion(task string) error {
	c := usecases.TaskCompletion{TaskID: task}
	w.deliver(func(id string) (usecases.Outcome, error) { return w.uc.completed.Handle(w.ctx, id, c) })
	return nil
}

func (w *world) invalidZone(zone, site string) error {
	c := usecases.ZoneChange{ZoneID: zone, SiteCode: site, ZoneCode: "FWD"}
	w.deliver(func(id string) (usecases.Outcome, error) { return w.uc.zone.Handle(w.ctx, id, c) })
	return nil
}

func (w *world) invalidLocation(code, zone string) error {
	c := usecases.LocationChange{Code: code, ZoneID: zone}
	w.deliver(func(id string) (usecases.Outcome, error) { return w.uc.location.Handle(w.ctx, id, c) })
	return nil
}

func (w *world) invalidDecommission(code string) error {
	w.deliver(func(id string) (usecases.Outcome, error) { return w.uc.decommissioned.Handle(w.ctx, id, code) })
	return nil
}

func (w *world) invalidGeometry(code, xs, ys, zs string) error {
	var p [3]float64
	for i, s := range []string{xs, ys, zs} {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return fmt.Errorf("coordinate %q: %w", s, err)
		}
		p[i] = v
	}
	c := usecases.GeometryChange{Code: code, X: p[0], Y: p[1], Z: p[2]}
	w.deliver(func(id string) (usecases.Outcome, error) { return w.uc.geometry.Handle(w.ctx, id, c) })
	return nil
}

// eventWithoutID sends a well-formed event of the named kind with a blank
// CloudEvents id.
func (w *world) eventWithoutID(kind string) error {
	w.run(func() error {
		var err error
		switch kind {
		case "TaskAvailable":
			c := usecases.TaskAvailability{TaskID: "T1", TaskType: "PICK", SiteID: "SITE-1", FromLocation: "A"}
			_, err = w.uc.available.Handle(w.ctx, "", c)
		case "TaskClaimed":
			_, err = w.uc.claimed.Handle(w.ctx, "", usecases.TaskClaim{TaskID: "T1", StationID: "STN-1"})
		case "TaskCompleted":
			_, err = w.uc.completed.Handle(w.ctx, "", usecases.TaskCompletion{TaskID: "T1"})
		case "ZoneRegistered":
			_, err = w.uc.zone.Handle(w.ctx, "", usecases.ZoneChange{ZoneID: "z1", SiteCode: "SITE-1"})
		case "LocationSlotRegistered":
			_, err = w.uc.location.Handle(w.ctx, "", usecases.LocationChange{Code: "A", ZoneID: "z1"})
		case "LocationSlotDecommissioned":
			_, err = w.uc.decommissioned.Handle(w.ctx, "", "A")
		default:
			_, err = w.uc.geometry.Handle(w.ctx, "", usecases.GeometryChange{Code: "A", X: 1, Y: 1, Z: 1})
		}
		return err
	})
	return nil
}

func registerRulesPolicySteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^I declare the policy of site "([^"]*)" with vertical weight (-?\d+(?:\.\d+)?)$`, w.declareWithWeight)
	sc.Step(`^I declare a policy for a site whose id has (\d+) characters$`, w.declareLongSite)
}

func (w *world) declareWithWeight(site string, weight float64) error {
	in := policyInput(site, 25, 3, 90, "PICK")
	in.VerticalWeight = &weight
	w.declareInput(in)
	return nil
}

func (w *world) declareLongSite(n int) error {
	w.declareInput(policyInput(strings.Repeat("S", n), 25, 3, 90, "PICK"))
	return nil
}

func registerRulesQuerySteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^I list the hints with limit (-?\d+)$`, func(limit int) error {
		return w.listHints(usecases.ListHintsQuery{Limit: limit})
	})
	sc.Step(`^I list the hints with limit (\d+) after the cursor of the previous page$`, func(limit int) error {
		return w.listHints(usecases.ListHintsQuery{Limit: limit, Cursor: w.rules.page.NextCursor})
	})
	sc.Step(`^I list the hints with the cursor "([^"]*)"$`, func(cursor string) error {
		return w.listHints(usecases.ListHintsQuery{Cursor: cursor})
	})
	sc.Step(`^I list the hints of station "([^"]*)"$`, func(station string) error {
		return w.listHints(usecases.ListHintsQuery{StationID: station})
	})
	sc.Step(`^I list the hints of site "([^"]*)"$`, func(site string) error {
		return w.listHints(usecases.ListHintsQuery{SiteID: site})
	})
	sc.Step(`^I list the hints in state "([^"]*)"$`, func(state string) error {
		return w.listHints(usecases.ListHintsQuery{State: state})
	})
	sc.Step(`^I read hint "([^"]*)"$`, w.readHint)
	sc.Step(`^I read station "([^"]*)"$`, w.readStation)
}

func (w *world) listHints(q usecases.ListHintsQuery) error {
	w.rules.page = usecases.HintPage{}
	w.run(func() error {
		page, err := w.uc.listHints.Handle(w.ctx, q)
		if err == nil {
			w.rules.page = page
		}
		return err
	})
	return nil
}

func (w *world) readHint(id string) error {
	w.run(func() error {
		_, err := w.uc.getHint.Handle(w.ctx, id)
		return err
	})
	return nil
}

func (w *world) readStation(id string) error {
	w.run(func() error {
		_, err := w.uc.getStation.Handle(w.ctx, id)
		return err
	})
	return nil
}
