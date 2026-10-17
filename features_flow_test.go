package main_test

import (
	"fmt"
	"time"

	"github.com/cucumber/godog"

	"github.com/claudioed/task-interleaving/internal/application/usecases"
)

func registerFlowSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^task "([^"]*)" is completed$`, w.taskCompleted)
	sc.Step(`^task "([^"]*)" is completed at "([^"]*)"$`, w.taskCompletedAt)
	sc.Step(`^the last event is delivered again$`, func() error { w.redeliver(); return nil })
	sc.Step(`^the expiry sweep runs$`, w.sweep)
	sc.Step(`^I simulate for station "([^"]*)"$`, w.simulate)
	sc.Step(`^I simulate for station "([^"]*)" from "([^"]*)"$`, w.simulateFrom)
}

func (w *world) taskCompleted(task string) error {
	c := usecases.TaskCompletion{TaskID: task}
	w.deliver(func(id string) (usecases.Outcome, error) { return w.uc.completed.Handle(w.ctx, id, c) })
	return nil
}

func (w *world) taskCompletedAt(task, at string) error {
	when, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return fmt.Errorf("completion time %q: %w", at, err)
	}
	c := usecases.TaskCompletion{TaskID: task, At: when}
	w.deliver(func(id string) (usecases.Outcome, error) { return w.uc.completed.Handle(w.ctx, id, c) })
	return nil
}

func (w *world) sweep() error {
	w.run(func() error {
		var err error
		w.expired, err = w.uc.expire.Handle(w.ctx)
		return err
	})
	return nil
}

func (w *world) simulate(station string) error {
	return w.simulateFrom(station, "")
}

func (w *world) simulateFrom(station, from string) error {
	w.run(func() error {
		var err error
		w.sim, err = w.uc.recommend.Handle(w.ctx, usecases.RecommendInput{StationID: station, FromLocation: from})
		return err
	})
	return nil
}
