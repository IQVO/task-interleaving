package http_test

import (
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDeclareThenGetThePolicy(t *testing.T) {
	f := newFixture(t)
	put := f.mustCall(t, http.MethodPut, "/sites/SITE-1/policy", policyBody, http.StatusOK)
	if put.header.Get("ETag") != `"1"` || put.contentType() != "application/json" {
		t.Fatalf("PUT headers: %v", put.header)
	}
	want := map[string]any{
		"siteId": "SITE-1", "maxDetourM": 25.0, "maxCandidates": 3.0, "hintTtlSeconds": 90.0,
		"eligibleTaskTypes": []any{"PICK", "REPLENISH"}, "verticalWeight": 1.5, "version": 1.0,
		"declaredAt": "2026-10-17T12:00:00Z",
	}
	for k, v := range want {
		if got := put.body[k]; !equalJSON(got, v) {
			t.Errorf("%s = %v, want %v", k, got, v)
		}
	}
	get := f.mustCall(t, http.MethodGet, "/sites/SITE-1/policy", "", http.StatusOK)
	if get.raw != put.raw || get.header.Get("ETag") != `"1"` {
		t.Fatalf("GET differs from PUT:\n%s\n%s", put.raw, get.raw)
	}
	if got := f.events.Declarations(); len(got) != 1 || got[0].Version != 1 {
		t.Fatalf("declarations = %+v, want one at version 1", got)
	}
}

func TestVerticalWeightDefaultsToOne(t *testing.T) {
	f := newFixture(t)
	body := `{"maxDetourM":12.5,"maxCandidates":5,"hintTtlSeconds":30,"eligibleTaskTypes":["PICK","PICK","SLAM"]}`
	r := f.mustCall(t, http.MethodPut, "/sites/SITE-1/policy", body, http.StatusOK)
	if r.body["verticalWeight"] != 1.0 || !equalJSON(r.body["eligibleTaskTypes"], []any{"PICK", "SLAM"}) {
		t.Fatalf("defaults: %s", r.raw)
	}
}

func TestReplacingNeedsTheCurrentVersionInIfMatch(t *testing.T) {
	f := newFixture(t)
	f.declare(t)
	expectProblem(t, f.call(t, http.MethodPut, "/sites/SITE-1/policy", policyBody), http.StatusPreconditionFailed, "version-mismatch")
	expectProblem(t, f.call(t, http.MethodPut, "/sites/SITE-1/policy", policyBody, "If-Match", `"7"`), http.StatusPreconditionFailed, "version-mismatch")

	quoted := f.mustCall(t, http.MethodPut, "/sites/SITE-1/policy", policyBody, http.StatusOK, "If-Match", `"1"`)
	if quoted.header.Get("ETag") != `"2"` || quoted.body["version"] != 2.0 {
		t.Fatalf("quoted If-Match: %v %s", quoted.header, quoted.raw)
	}
	unquoted := f.mustCall(t, http.MethodPut, "/sites/SITE-1/policy", policyBody, http.StatusOK, "If-Match", "2")
	if unquoted.header.Get("ETag") != `"3"` {
		t.Fatalf("unquoted If-Match (same values still advance the version): %v", unquoted.header)
	}
}

func TestIfMatchOnAFirstDeclarationIsAMismatch(t *testing.T) {
	f := newFixture(t)
	expectProblem(t, f.call(t, http.MethodPut, "/sites/SITE-1/policy", policyBody, "If-Match", `"1"`), http.StatusPreconditionFailed, "version-mismatch")
	expectProblem(t, f.call(t, http.MethodGet, "/sites/SITE-1/policy", ""), http.StatusNotFound, "policy-not-found")
}

// The contract documents no answer for an unreadable If-Match but 412: it
// cannot match any version.
func TestAnUnreadableIfMatchIsStale(t *testing.T) {
	f := newFixture(t)
	f.declare(t)
	for _, raw := range []string{"abc", `W/"1"`, "*", `""`, `"`, "1.0", "-1", "99999999999999999999"} {
		r := f.call(t, http.MethodPut, "/sites/SITE-1/policy", policyBody, "If-Match", raw)
		if r.status != http.StatusPreconditionFailed {
			t.Errorf("If-Match %q = %d %s, want 412", raw, r.status, r.raw)
		}
	}
	if v := f.mustCall(t, http.MethodGet, "/sites/SITE-1/policy", "", http.StatusOK).body["version"]; v != 1.0 {
		t.Fatalf("a refused PUT moved the version to %v", v)
	}
}

func TestInvalidPolicyValues(t *testing.T) {
	cases := []struct {
		name, body, slug string
	}{
		{"zero detour", `{"maxDetourM":0,"maxCandidates":3,"hintTtlSeconds":90,"eligibleTaskTypes":["PICK"]}`, "invalid-policy"},
		{"negative detour", `{"maxDetourM":-1,"maxCandidates":3,"hintTtlSeconds":90,"eligibleTaskTypes":["PICK"]}`, "invalid-policy"},
		{"no detour", `{"maxCandidates":3,"hintTtlSeconds":90,"eligibleTaskTypes":["PICK"]}`, "invalid-policy"},
		{"six candidates", `{"maxDetourM":5,"maxCandidates":6,"hintTtlSeconds":90,"eligibleTaskTypes":["PICK"]}`, "invalid-policy"},
		{"zero candidates", `{"maxDetourM":5,"maxCandidates":0,"hintTtlSeconds":90,"eligibleTaskTypes":["PICK"]}`, "invalid-policy"},
		{"no candidates field", `{"maxDetourM":5,"hintTtlSeconds":90,"eligibleTaskTypes":["PICK"]}`, "invalid-policy"},
		{"zero ttl", `{"maxDetourM":5,"maxCandidates":3,"hintTtlSeconds":0,"eligibleTaskTypes":["PICK"]}`, "invalid-policy"},
		{"no ttl", `{"maxDetourM":5,"maxCandidates":3,"eligibleTaskTypes":["PICK"]}`, "invalid-policy"},
		{"ttl beyond int32", `{"maxDetourM":5,"maxCandidates":3,"hintTtlSeconds":2147483648,"eligibleTaskTypes":["PICK"]}`, "invalid-policy"},
		{"negative weight", `{"maxDetourM":5,"maxCandidates":3,"hintTtlSeconds":9,"eligibleTaskTypes":["PICK"],"verticalWeight":-0.1}`, "invalid-policy"},
		{"empty types", `{"maxDetourM":5,"maxCandidates":3,"hintTtlSeconds":9,"eligibleTaskTypes":[]}`, "invalid-task-type"},
		{"no types", `{"maxDetourM":5,"maxCandidates":3,"hintTtlSeconds":9}`, "invalid-task-type"},
		{"unknown type", `{"maxDetourM":5,"maxCandidates":3,"hintTtlSeconds":9,"eligibleTaskTypes":["FLY"]}`, "invalid-task-type"},
		{"lowercase type", `{"maxDetourM":5,"maxCandidates":3,"hintTtlSeconds":9,"eligibleTaskTypes":["pick"]}`, "invalid-task-type"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			expectProblem(t, f.call(t, http.MethodPut, "/sites/SITE-1/policy", tc.body), http.StatusBadRequest, tc.slug)
			assertNoPolicyWritten(t, f)
		})
	}
}

func TestMalformedPolicyBodies(t *testing.T) {
	bodies := map[string]string{
		"empty":            "",
		"not json":         "{nope",
		"array":            "[]",
		"unknown field":    `{"maxDetourM":5,"maxCandidates":3,"hintTtlSeconds":9,"eligibleTaskTypes":["PICK"],"colour":"red"}`,
		"string number":    `{"maxDetourM":"5","maxCandidates":3,"hintTtlSeconds":9,"eligibleTaskTypes":["PICK"]}`,
		"fractional count": `{"maxDetourM":5,"maxCandidates":2.5,"hintTtlSeconds":9,"eligibleTaskTypes":["PICK"]}`,
		"trailing data":    `{"maxDetourM":5,"maxCandidates":3,"hintTtlSeconds":9,"eligibleTaskTypes":["PICK"]} {}`,
		"types not array":  `{"maxDetourM":5,"maxCandidates":3,"hintTtlSeconds":9,"eligibleTaskTypes":"PICK"}`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			r := f.call(t, http.MethodPut, "/sites/SITE-1/policy", body)
			expectProblem(t, r, http.StatusBadRequest, "malformed-request")
			if strings.Contains(r.body["detail"].(string), "json:") {
				t.Fatalf("the Go decoder message leaked: %s", r.raw)
			}
			assertNoPolicyWritten(t, f)
		})
	}
}

// A rejected declaration leaves no row and no outbox fact behind.
func assertNoPolicyWritten(t *testing.T, f *fixture) {
	t.Helper()
	expectProblem(t, f.call(t, http.MethodGet, "/sites/SITE-1/policy", ""), http.StatusNotFound, "policy-not-found")
	if n := len(f.events.Declarations()); n != 0 {
		t.Fatalf("%d declarations were enqueued by a rejected request", n)
	}
}

func TestInvalidSiteIdsAreRejectedOnBothVerbs(t *testing.T) {
	f := newFixture(t)
	for _, site := range []string{"bad%20site", "a%2Fb", "tab%09id", strings.Repeat("s", 65), "%E2%80%83"} {
		for _, method := range []string{http.MethodGet, http.MethodPut} {
			expectProblem(t, f.call(t, method, "/sites/"+site+"/policy", policyBody), http.StatusBadRequest, "invalid-site-id")
		}
	}
	f.mustCall(t, http.MethodPut, "/sites/"+strings.Repeat("s", 64)+"/policy", policyBody, http.StatusOK)
}

func TestLosingARaceAtTheDatabaseIs409(t *testing.T) {
	f := newFixture(t)
	f.declare(t)
	f.policies.saveFail.set(repoConflict)
	expectProblem(t, f.call(t, http.MethodPut, "/sites/SITE-1/policy", policyBody, "If-Match", `"1"`), http.StatusConflict, "concurrent-modification")
}

// Eight writers present the same If-Match: exactly one wins, the others see the
// new version and are refused with 412.
func TestConcurrentReplacementsHaveOneWinner(t *testing.T) {
	f := newFixture(t)
	f.declare(t)
	var won, stale atomic.Int32
	parallel(8, func(int) {
		switch r := f.call(t, http.MethodPut, "/sites/SITE-1/policy", policyBody, "If-Match", `"1"`); r.status {
		case http.StatusOK:
			won.Add(1)
		case http.StatusPreconditionFailed:
			stale.Add(1)
		default:
			t.Errorf("unexpected %d %s", r.status, r.raw)
		}
	})
	if won.Load() != 1 || stale.Load() != 7 {
		t.Fatalf("winners=%d stale=%d, want 1 and 7", won.Load(), stale.Load())
	}
	if v := f.mustCall(t, http.MethodGet, "/sites/SITE-1/policy", "", http.StatusOK).body["version"]; v != 2.0 {
		t.Fatalf("version = %v, want 2", v)
	}
	if n := len(f.events.Declarations()); n != 2 {
		t.Fatalf("declarations = %d, want the first and the winner's", n)
	}
}

func TestPolicyStoreFailuresAreInternalErrorsWithoutLeaks(t *testing.T) {
	f := newFixture(t)
	f.policies.getFail.set(errDB)
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		r := f.call(t, method, "/sites/SITE-1/policy", policyBody)
		expectProblem(t, r, http.StatusInternalServerError, "internal-error")
		if strings.Contains(r.raw, "db down") {
			t.Fatalf("internal error leaked: %s", r.raw)
		}
	}
}
