package http_test

import (
	"net/http"
	"strings"
	"testing"
)

const recommendPath = "/recommendations"

func candidateIDs(r response) []string {
	var out []string
	cs, _ := r.body["candidates"].([]any)
	for _, c := range cs {
		out = append(out, c.(map[string]any)["taskId"].(string))
	}
	return out
}

func TestRecommendFromAWhatIfLocation(t *testing.T) {
	f := newFixture(t)
	f.seedWorld(t)
	f.declare(t)
	r := f.mustCall(t, http.MethodPost, recommendPath, `{"stationId":"STN-07","fromLocation":"A-01"}`, http.StatusOK)
	if r.contentType() != "application/json" || r.header.Get("ETag") != "" {
		t.Fatalf("headers: %v", r.header)
	}
	if r.body["stationId"] != "STN-07" || r.body["siteId"] != "SITE-1" || r.body["originLocation"] != "A-01" || r.body["policyVersion"] != 1.0 {
		t.Fatalf("recommendation: %s", r.raw)
	}
	if got := strings.Join(candidateIDs(r), ","); got != "task-9c2e,task-9d10" {
		t.Fatalf("candidates = %s (the far, geometry-less and PACK tasks must not qualify): %s", got, r.raw)
	}
	if !strings.Contains(r.raw, wantCandidates) {
		t.Fatalf("candidate shape: %s", r.raw)
	}
}

func TestRecommendFromTheRecordedPosition(t *testing.T) {
	f := withHint(t)
	r := f.mustCall(t, http.MethodPost, recommendPath, `{"stationId":"STN-07"}`, http.StatusOK)
	if r.body["originLocation"] != "A-01" || strings.Join(candidateIDs(r), ",") != "task-9c2e,task-9d10" {
		t.Fatalf("from the recorded position: %s", r.raw)
	}
	// A what-if origin wins over the recorded position; the station's site stays.
	near := f.mustCall(t, http.MethodPost, recommendPath, `{"stationId":"STN-07","fromLocation":"REMOTE-01"}`, http.StatusOK)
	if near.body["originLocation"] != "REMOTE-01" || near.body["siteId"] != "SITE-1" || len(candidateIDs(near)) != 0 {
		t.Fatalf("what-if origin: %s", near.raw)
	}
}

func TestNoCandidatesIsAnEmptyListNotAnError(t *testing.T) {
	f := newFixture(t)
	f.seedWorld(t)
	f.declare(t)
	r := f.mustCall(t, http.MethodPost, recommendPath, `{"stationId":"STN-07","fromLocation":"REMOTE-01"}`, http.StatusOK)
	if !strings.Contains(r.raw, `"candidates":[]`) {
		t.Fatalf("empty candidates must be [] not null: %s", r.raw)
	}
}

// The simulation issues nothing, publishes nothing and changes no state.
func TestRecommendIsReadOnly(t *testing.T) {
	f := withHint(t)
	eventsBefore := strings.Join(f.events.Names(), ",")
	station := f.mustCall(t, http.MethodGet, "/stations/STN-07", "", http.StatusOK).raw
	hints := f.mustCall(t, http.MethodGet, "/hints", "", http.StatusOK).raw
	for range 3 {
		f.mustCall(t, http.MethodPost, recommendPath, `{"stationId":"STN-07"}`, http.StatusOK)
		f.mustCall(t, http.MethodPost, recommendPath, `{"stationId":"STN-08","fromLocation":"A-01"}`, http.StatusOK)
	}
	if got := strings.Join(f.events.Names(), ","); got != eventsBefore {
		t.Fatalf("the simulation enqueued events: %q -> %q", eventsBefore, got)
	}
	if f.mustCall(t, http.MethodGet, "/stations/STN-07", "", http.StatusOK).raw != station ||
		f.mustCall(t, http.MethodGet, "/hints", "", http.StatusOK).raw != hints {
		t.Fatal("the simulation changed the station or the hints")
	}
	expectProblem(t, f.call(t, http.MethodGet, "/stations/STN-08", ""), http.StatusNotFound, "station-not-found")
}

func TestRecommendRefusals(t *testing.T) {
	f := newFixture(t)
	f.seedWorld(t)
	cases := []struct{ name, body, slug string }{
		{"site is inert", `{"stationId":"STN-07","fromLocation":"A-01"}`, "policy-not-declared"},
		{"no position and no origin", `{"stationId":"STN-99"}`, "station-position-unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expectProblem(t, f.call(t, http.MethodPost, recommendPath, tc.body), http.StatusUnprocessableEntity, tc.slug)
		})
	}
	f.declare(t)
	r := f.call(t, http.MethodPost, recommendPath, `{"stationId":"STN-07","fromLocation":"NOGEO"}`)
	expectProblem(t, r, http.StatusUnprocessableEntity, "origin-unknown")
	if !strings.Contains(r.body["detail"].(string), "NOGEO") {
		t.Fatalf("the detail names the location: %s", r.raw)
	}
	expectProblem(t, f.call(t, http.MethodPost, recommendPath, `{"stationId":"STN-07","fromLocation":"NOWHERE"}`), http.StatusUnprocessableEntity, "origin-unknown")
}

func TestRecommendBodyValidation(t *testing.T) {
	long := strings.Repeat("L", 129)
	cases := []struct{ name, body, slug string }{
		{"empty body", "", "malformed-request"},
		{"not json", "{", "malformed-request"},
		{"unknown field", `{"stationId":"STN-07","extra":1}`, "malformed-request"},
		{"station is a number", `{"stationId":7}`, "malformed-request"},
		{"trailing data", `{"stationId":"STN-07"}{}`, "malformed-request"},
		{"no station", `{}`, "invalid-station-id"},
		{"empty station", `{"stationId":""}`, "invalid-station-id"},
		{"blank station", `{"stationId":"  "}`, "invalid-station-id"},
		{"byte order mark station", "{\"stationId\":\"\\ufeff\"}", "invalid-station-id"},
		{"station with a slash", `{"stationId":"a/b"}`, "invalid-station-id"},
		{"station too long", `{"stationId":"` + strings.Repeat("s", 65) + `"}`, "invalid-station-id"},
		{"empty location", `{"stationId":"STN-07","fromLocation":""}`, "malformed-request"},
		{"blank location", `{"stationId":"STN-07","fromLocation":" \t"}`, "malformed-request"},
		{"location too long", `{"stationId":"STN-07","fromLocation":"` + long + `"}`, "malformed-request"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			expectProblem(t, f.call(t, http.MethodPost, recommendPath, tc.body), http.StatusBadRequest, tc.slug)
		})
	}
	f := newFixture(t)
	f.seedWorld(t)
	f.declare(t)
	max := strings.Repeat("L", 128)
	expectProblem(t, f.call(t, http.MethodPost, recommendPath, `{"stationId":"STN-07","fromLocation":"`+max+`"}`), http.StatusUnprocessableEntity, "origin-unknown")
}

func TestRecommendStoreFailureIsAnInternalError(t *testing.T) {
	f := newFixture(t)
	f.seedWorld(t)
	f.declare(t)
	f.trips.getFail.set(errDB)
	r := f.call(t, http.MethodPost, recommendPath, `{"stationId":"STN-07","fromLocation":"A-01"}`)
	expectProblem(t, r, http.StatusInternalServerError, "internal-error")
	f.trips.getFail.set(nil)
	f.policies.getFail.set(errDB)
	expectProblem(t, f.call(t, http.MethodPost, recommendPath, `{"stationId":"STN-07","fromLocation":"A-01"}`), http.StatusInternalServerError, "internal-error")
}
