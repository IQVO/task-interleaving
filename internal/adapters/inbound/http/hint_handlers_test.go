package http_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/task-interleaving/internal/application/usecases"
)

// withHint gives STN-07 the Active hint hint-test-001: the site declared a
// policy, STN-07 completed task-8a1f at A-01 and two tasks qualify.
func withHint(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	f.seedWorld(t)
	f.declare(t)
	f.complete(t, "STN-07", "task-8a1f")
	return f
}

const wantCandidates = `[{"rank":1,"taskId":"task-9c2e","taskType":"PICK","location":"B-01","detourM":4},` +
	`{"rank":2,"taskId":"task-9d10","taskType":"REPLENISH","location":"B-02","detourM":10}]`

func candidatesJSON(t *testing.T, r response, field string) string {
	t.Helper()
	cs, ok := r.body[field].([]any)
	if !ok || len(cs) == 0 {
		t.Fatalf("%s missing in %s", field, r.raw)
	}
	return r.raw
}

func TestAnActiveHintIsServed(t *testing.T) {
	f := withHint(t)
	r := f.mustCall(t, http.MethodGet, "/hints/hint-test-001", "", http.StatusOK)
	if r.header.Get("ETag") != `"1"` {
		t.Fatalf("ETag = %q", r.header.Get("ETag"))
	}
	want := map[string]any{
		"hintId": "hint-test-001", "stationId": "STN-07", "siteId": "SITE-1", "state": "Active",
		"afterTaskId": "task-8a1f", "originLocation": "A-01", "issuedAt": "2026-10-17T12:00:00Z",
		"expiresAt": "2026-10-17T12:01:30Z", "policyVersion": 1.0, "version": 1.0,
	}
	for k, v := range want {
		if !equalJSON(r.body[k], v) {
			t.Errorf("%s = %v, want %v", k, r.body[k], v)
		}
	}
	if !strings.Contains(candidatesJSON(t, r, "candidates"), strings.ReplaceAll(wantCandidates, "\n", "")) {
		t.Fatalf("candidates: %s", r.raw)
	}
	for _, absent := range []string{"consumedTaskId", "consumedAt", "supersededAt"} {
		if _, ok := r.body[absent]; ok {
			t.Errorf("%s must be absent on an Active hint: %s", absent, r.raw)
		}
	}
}

func TestAStationReportsItsPositionAndActiveHint(t *testing.T) {
	f := withHint(t)
	r := f.mustCall(t, http.MethodGet, "/stations/STN-07", "", http.StatusOK)
	if r.header.Get("ETag") == "" {
		t.Fatal("no ETag")
	}
	if r.body["stationId"] != "STN-07" || r.body["siteId"] != "SITE-1" || r.body["lastCompletedTaskId"] != "task-8a1f" ||
		r.body["lastCompletedAt"] != "2026-10-17T12:00:00Z" {
		t.Fatalf("station: %s", r.raw)
	}
	pos, _ := r.body["position"].(map[string]any)
	point, _ := pos["point"].(map[string]any)
	if pos["location"] != "A-01" || point["xM"] != 0.0 || point["yM"] != 0.0 || point["zM"] != 0.0 {
		t.Fatalf("position: %s", r.raw)
	}
	active, _ := r.body["activeHint"].(map[string]any)
	if active["hintId"] != "hint-test-001" || active["state"] != "Active" || active["policyVersion"] != 1.0 {
		t.Fatalf("activeHint: %s", r.raw)
	}
	if _, ok := active["stationId"]; ok {
		t.Fatalf("the active hint is the short form: %s", r.raw)
	}
}

func TestAStationWithoutAHintHasNoActiveHint(t *testing.T) {
	f := newFixture(t)
	f.seedWorld(t)
	f.complete(t, "STN-07", "task-8a1f") // no policy: inert, position recorded, no hint
	r := f.mustCall(t, http.MethodGet, "/stations/STN-07", "", http.StatusOK)
	if _, ok := r.body["activeHint"]; ok {
		t.Fatalf("an inert site issued a hint: %s", r.raw)
	}
	f.mustCall(t, http.MethodGet, "/hints", "", http.StatusOK)
}

func TestHintOutcomesAreReported(t *testing.T) {
	t.Run("consumed", func(t *testing.T) {
		f := withHint(t)
		_, err := f.claimed.Handle(t.Context(), "evt-claim", usecases.TaskClaim{TaskID: "task-9c2e", TaskType: "PICK", StationID: "STN-07", At: f.clock.Now()})
		must(t, err)
		r := f.mustCall(t, http.MethodGet, "/hints/hint-test-001", "", http.StatusOK)
		if r.body["state"] != "Consumed" || r.body["consumedTaskId"] != "task-9c2e" || r.body["consumedAt"] != "2026-10-17T12:00:00Z" || r.body["version"] != 2.0 {
			t.Fatalf("consumed: %s", r.raw)
		}
		st := f.mustCall(t, http.MethodGet, "/stations/STN-07", "", http.StatusOK)
		if _, ok := st.body["activeHint"]; ok {
			t.Fatalf("a consumed hint is not active: %s", st.raw)
		}
	})
	t.Run("expired", func(t *testing.T) {
		f := withHint(t)
		f.clock.Advance(91 * time.Second)
		n, err := f.expire.Handle(t.Context())
		must(t, err)
		if n != 1 {
			t.Fatalf("expired %d hints", n)
		}
		if r := f.mustCall(t, http.MethodGet, "/hints/hint-test-001", "", http.StatusOK); r.body["state"] != "Expired" || r.body["version"] != 2.0 {
			t.Fatalf("expired: %s", r.raw)
		}
	})
	t.Run("superseded", func(t *testing.T) {
		f := withHint(t)
		f.clock.Advance(10 * time.Second)
		f.complete(t, "STN-07", "task-8a20")
		r := f.mustCall(t, http.MethodGet, "/hints/hint-test-001", "", http.StatusOK)
		if r.body["state"] != "Superseded" || r.body["supersededAt"] != "2026-10-17T12:00:10Z" {
			t.Fatalf("superseded: %s", r.raw)
		}
		if f.mustCall(t, http.MethodGet, "/hints/hint-test-002", "", http.StatusOK).body["state"] != "Active" {
			t.Fatal("the newer hint must be Active")
		}
	})
}

func TestListingHintsNewestFirstInPagesWithFilters(t *testing.T) {
	f := newFixture(t)
	f.seedWorld(t)
	f.declare(t)
	for i, station := range []string{"STN-07", "STN-08", "STN-09"} {
		f.complete(t, station, "task-done-"+string(rune('a'+i)))
	}
	ids := func(r response) []string {
		var out []string
		for _, it := range r.body["items"].([]any) {
			out = append(out, it.(map[string]any)["hintId"].(string))
		}
		return out
	}
	first := f.mustCall(t, http.MethodGet, "/hints?limit=2", "", http.StatusOK)
	if got := strings.Join(ids(first), ","); got != "hint-test-003,hint-test-002" || first.body["nextCursor"] == nil {
		t.Fatalf("first page = %s", first.raw)
	}
	second := f.mustCall(t, http.MethodGet, "/hints?limit=2&cursor="+first.body["nextCursor"].(string), "", http.StatusOK)
	if got := strings.Join(ids(second), ","); got != "hint-test-001" {
		t.Fatalf("second page = %s", second.raw)
	}
	if _, ok := second.body["nextCursor"]; ok {
		t.Fatalf("the last page has no nextCursor: %s", second.raw)
	}
	filters := map[string]int{
		"stationId=STN-08": 1, "siteId=SITE-1": 3, "siteId=OTHER": 0, "state=Active": 3, "state=Consumed": 0,
		"stationId=STN-08&state=Active&siteId=SITE-1": 1, "stationId=STN-08&state=Expired": 0,
	}
	for query, want := range filters {
		if got := len(ids(f.mustCall(t, http.MethodGet, "/hints?"+query, "", http.StatusOK))); got != want {
			t.Errorf("?%s returned %d hints, want %d", query, got, want)
		}
	}
}

func TestAnEmptyListIsAnEmptyArray(t *testing.T) {
	f := newFixture(t)
	if r := f.mustCall(t, http.MethodGet, "/hints", "", http.StatusOK); r.raw != "{\"items\":[]}\n" {
		t.Fatalf("empty page = %q", r.raw)
	}
}

func TestListQueriesAreValidated(t *testing.T) {
	f := newFixture(t)
	cases := map[string]string{
		"limit=0": "invalid-limit", "limit=501": "invalid-limit", "limit=abc": "invalid-limit", "limit=-5": "invalid-limit", "limit=1.5": "invalid-limit",
		"cursor=***": "invalid-cursor", "cursor=!!": "invalid-cursor",
		"state=Open": "invalid-state", "state=active": "invalid-state", "state=": "invalid-state",
		"stationId=": "invalid-station-id", "stationId=a%2Fb": "invalid-station-id", "stationId=%20%20": "invalid-station-id",
		"siteId=": "invalid-site-id", "siteId=a%20b": "invalid-site-id", "siteId=%EF%BB%BF": "invalid-site-id",
	}
	for query, slug := range cases {
		expectProblem(t, f.call(t, http.MethodGet, "/hints?"+query, ""), http.StatusBadRequest, slug)
	}
	f.mustCall(t, http.MethodGet, "/hints?limit=500", "", http.StatusOK)
	f.mustCall(t, http.MethodGet, "/hints?limit=1", "", http.StatusOK)
}

func TestHintsAndStationsThatDoNotExist(t *testing.T) {
	f := newFixture(t)
	expectProblem(t, f.call(t, http.MethodGet, "/hints/hint-unknown", ""), http.StatusNotFound, "hint-not-found")
	expectProblem(t, f.call(t, http.MethodGet, "/stations/STN-99", ""), http.StatusNotFound, "station-not-found")
}

func TestInvalidHintAndStationIds(t *testing.T) {
	f := newFixture(t)
	for _, id := range []string{"nope", "hint-", "hint-a%2Fb", "hint-a%20b", "hint-" + strings.Repeat("x", 60), "HINT-1"} {
		expectProblem(t, f.call(t, http.MethodGet, "/hints/"+id, ""), http.StatusBadRequest, "invalid-hint-id")
	}
	for _, id := range []string{"bad%20station", "a%2Fb", strings.Repeat("s", 65), "tab%09id"} {
		expectProblem(t, f.call(t, http.MethodGet, "/stations/"+id, ""), http.StatusBadRequest, "invalid-station-id")
	}
	f.mustCall(t, http.MethodGet, "/stations/"+strings.Repeat("s", 64), "", http.StatusNotFound)
}

func TestReadStoreFailuresAreInternalErrors(t *testing.T) {
	f := withHint(t)
	f.trips.hintFail.set(errDB)
	f.trips.getFail.set(errDB)
	f.trips.listHintErr.set(errDB)
	for _, path := range []string{"/hints/hint-test-001", "/stations/STN-07", "/hints"} {
		r := f.call(t, http.MethodGet, path, "")
		expectProblem(t, r, http.StatusInternalServerError, "internal-error")
		if strings.Contains(r.raw, "db down") {
			t.Fatalf("internal error leaked: %s", r.raw)
		}
	}
}
