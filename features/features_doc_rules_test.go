package features_test

import (
	"fmt"

	"github.com/cucumber/godog"

	"github.com/claudioed/task-interleaving/internal/application/usecases"
)

// registerDocRulesSteps adds the vocabulary of the third documentation-derived
// wave (doc_rules.feature): the default listing limit of the read side, the
// candidate payload of a hint and page-shape assertions for bulk listings.
func registerDocRulesSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^I list the hints with the default limit$`, func() error {
		return w.listHints(usecases.ListHintsQuery{})
	})
	sc.Step(`^the page holds (\d+) hints?$`, w.pageHolds)
	sc.Step(`^the first hint of the page is number (\d+)$`, w.firstPageHint)
	sc.Step(`^candidate (\d+) of the active hint of station "([^"]*)" is task "([^"]*)" of type "([^"]*)" at location "([^"]*)"$`, w.candidateDetails)
}

// pageHolds asserts how many hints the last listing returned.
func (w *world) pageHolds(want int) error {
	w.errChecked = true
	if w.err != nil {
		return fmt.Errorf("the listing failed: %w", w.err)
	}
	if len(w.rules.page.Items) != want {
		return fmt.Errorf("the page holds %d hints, want %d", len(w.rules.page.Items), want)
	}
	return nil
}

// firstPageHint asserts the newest hint of the last listing by its number.
func (w *world) firstPageHint(n int) error {
	if len(w.rules.page.Items) == 0 {
		return fmt.Errorf("the page is empty, want it to start with %s", hintID(n))
	}
	if got := string(w.rules.page.Items[0].Hint.ID); got != hintID(n) {
		return fmt.Errorf("the page starts with %s, want %s", got, hintID(n))
	}
	return nil
}

// candidateDetails asserts the payload of one ranked candidate: its task id,
// its task type and the location the task starts at (the from-location).
func (w *world) candidateDetails(rank int, station, task, typ, location string) error {
	h, err := w.activeHint(station)
	if err != nil {
		return err
	}
	for _, c := range h.Candidates {
		if c.Rank != rank {
			continue
		}
		if string(c.TaskID) != task || string(c.TaskType) != typ || c.Location != location {
			return fmt.Errorf("candidate %d = %s of type %s at %s, want %s of type %s at %s",
				rank, c.TaskID, c.TaskType, c.Location, task, typ, location)
		}
		return nil
	}
	return fmt.Errorf("the hint has no candidate of rank %d", rank)
}
