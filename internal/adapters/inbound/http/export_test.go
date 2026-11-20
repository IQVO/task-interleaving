package http

import "net/http"

// ReplayEntry is one problem type as the replay-class tests read it.
type ReplayEntry struct {
	Slug   string
	Status int
	Class  string
}

// OutcomeProbeSlug names a synthetic OUTCOME problem that exists in tests only:
// no real problem type of this service is an OUTCOME (ADR 0013), so this is how
// the OUTCOME mechanism of the middleware is exercised.
const OutcomeProbeSlug = "outcome-probe"

var outcomeProbe = problem{status: http.StatusConflict, slug: OutcomeProbeSlug, title: "Synthetic OUTCOME", class: replayOutcome}

// ReplayCatalogue lists every problem type of the adapter (the registry
// newProblem fills), one entry per slug, with the class its entry declares.
func ReplayCatalogue() []ReplayEntry {
	out := make([]ReplayEntry, 0, len(registry))
	for _, p := range registry {
		out = append(out, ReplayEntry{Slug: p.slug, Status: p.status, Class: string(p.class)})
	}
	return out
}

// RefuseForTest answers the problem type slug through the production writer:
// writeError (the catalogue's own error mapping) when an error maps to it,
// writeProblem for the problems the adapter writes itself.
func RefuseForTest(w http.ResponseWriter, r *http.Request, slug string) {
	if slug == OutcomeProbeSlug {
		writeProblem(w, r, outcomeProbe, "test")
		return
	}
	for _, e := range problemCatalogue {
		if e.p.slug == slug {
			writeError(w, r, e.err)
			return
		}
	}
	for _, p := range registry {
		if p.slug == slug {
			writeProblem(w, r, p, "test")
			return
		}
	}
	panic("unknown problem type " + slug)
}
