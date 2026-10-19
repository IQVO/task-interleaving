package mcp_test

import (
	"reflect"
	"testing"
)

func TestGetSitePolicy(t *testing.T) {
	h := newHarness(t)
	h.declarePolicy(t)
	got := h.ok(t, "get_site_policy", map[string]any{"siteId": "SITE-1"})
	want := map[string]any{
		"siteId": "SITE-1", "maxDetourM": 25.0, "maxCandidates": 3.0, "hintTtlSeconds": 90.0,
		"eligibleTaskTypes": []any{"PICK", "REPLENISH"}, "verticalWeight": 1.5, "version": 1.0,
		"declaredAt": "2026-10-17T12:00:00Z",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("get_site_policy = %v, want %v", got, want)
	}
}

func TestGetSitePolicyErrors(t *testing.T) {
	h := newHarness(t)
	failWith(t, h.session, "get_site_policy", map[string]any{"siteId": "SITE-9"}, "policy-not-found")
	failWith(t, h.session, "get_site_policy", map[string]any{"siteId": "a/b"}, "invalid-site-id")
}

func TestGetHint(t *testing.T) {
	h := withHint(t)
	got := h.ok(t, "get_hint", map[string]any{"hintId": "hint-test-001"})
	for k, v := range map[string]any{
		"hintId": "hint-test-001", "stationId": "STN-07", "siteId": "SITE-1", "state": "Active",
		"afterTaskId": "task-8a1f", "originLocation": "A-01", "issuedAt": "2026-10-17T12:00:00Z",
		"expiresAt": "2026-10-17T12:01:30Z", "policyVersion": 1.0, "version": 1.0,
	} {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	cands, _ := got["candidates"].([]any)
	if len(cands) != 2 {
		t.Fatalf("candidates = %v, want 2", got["candidates"])
	}
	first := cands[0].(map[string]any)
	if first["rank"] != 1.0 || first["taskId"] != "task-9c2e" || first["taskType"] != "PICK" || first["location"] != "B-01" || first["detourM"] != 4.0 {
		t.Fatalf("first candidate = %v", first)
	}
	for _, absent := range []string{"consumedTaskId", "consumedAt", "supersededAt"} {
		if _, ok := got[absent]; ok {
			t.Errorf("%s must be absent on an Active hint: %v", absent, got)
		}
	}
}

func TestGetHintErrors(t *testing.T) {
	h := newHarness(t)
	failWith(t, h.session, "get_hint", map[string]any{"hintId": "hint-nope"}, "hint-not-found")
	failWith(t, h.session, "get_hint", map[string]any{"hintId": "no-prefix"}, "invalid-hint-id")
}

func TestListHintsFiltersAndPages(t *testing.T) {
	h := withHint(t)
	h.complete(t, "STN-08", "task-8a20")

	all := h.ok(t, "list_hints", map[string]any{})
	if got := len(all["items"].([]any)); got != 2 {
		t.Fatalf("list_hints = %d items, want 2: %v", got, all)
	}
	byStation := h.ok(t, "list_hints", map[string]any{"stationId": "STN-07"})
	items := byStation["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["hintId"] != "hint-test-001" {
		t.Fatalf("stationId filter = %v", byStation)
	}
	if got := h.ok(t, "list_hints", map[string]any{"siteId": "SITE-2"}); len(got["items"].([]any)) != 0 {
		t.Fatalf("siteId filter = %v", got)
	}
	if got := h.ok(t, "list_hints", map[string]any{"state": "Active", "siteId": "SITE-1"}); len(got["items"].([]any)) != 2 {
		t.Fatalf("state filter = %v", got)
	}
	if got := h.ok(t, "list_hints", map[string]any{"state": "Expired"}); len(got["items"].([]any)) != 0 {
		t.Fatalf("state=Expired = %v", got)
	}

	first := h.ok(t, "list_hints", map[string]any{"limit": 1})
	cursor, _ := first["nextCursor"].(string)
	if len(first["items"].([]any)) != 1 || cursor == "" {
		t.Fatalf("first page = %v", first)
	}
	second := h.ok(t, "list_hints", map[string]any{"limit": 1, "cursor": cursor})
	if len(second["items"].([]any)) != 1 || second["nextCursor"] != nil {
		t.Fatalf("second page = %v", second)
	}
	if first["items"].([]any)[0].(map[string]any)["hintId"] == second["items"].([]any)[0].(map[string]any)["hintId"] {
		t.Fatal("the pages overlap")
	}
}

func TestListHintsErrors(t *testing.T) {
	h := newHarness(t)
	failWith(t, h.session, "list_hints", map[string]any{"state": "Bogus"}, "invalid-state")
	failWith(t, h.session, "list_hints", map[string]any{"limit": 9999}, "invalid-limit")
	failWith(t, h.session, "list_hints", map[string]any{"cursor": "!!"}, "invalid-cursor")
	failWith(t, h.session, "list_hints", map[string]any{"stationId": "a/b"}, "invalid-station-id")
	failWith(t, h.session, "list_hints", map[string]any{"siteId": "a/b"}, "invalid-site-id")
}

func TestGetStationReportsPositionAndActiveHint(t *testing.T) {
	h := withHint(t)
	got := h.ok(t, "get_station", map[string]any{"stationId": "STN-07"})
	if got["stationId"] != "STN-07" || got["siteId"] != "SITE-1" || got["lastCompletedTaskId"] != "task-8a1f" ||
		got["lastCompletedAt"] != "2026-10-17T12:00:00Z" {
		t.Fatalf("station = %v", got)
	}
	pos := got["position"].(map[string]any)
	point := pos["point"].(map[string]any)
	if pos["location"] != "A-01" || point["xM"] != 0.0 || point["yM"] != 0.0 || point["zM"] != 0.0 {
		t.Fatalf("position = %v", pos)
	}
	active := got["activeHint"].(map[string]any)
	if active["hintId"] != "hint-test-001" || active["state"] != "Active" || active["policyVersion"] != 1.0 {
		t.Fatalf("activeHint = %v", active)
	}
	if _, ok := active["stationId"]; ok {
		t.Fatalf("the active hint is the short form: %v", active)
	}
}

func TestGetStationWithoutAHint(t *testing.T) {
	h := newHarness(t)
	h.seedWorld(t)
	h.complete(t, "STN-07", "task-8a1f") // no policy: inert, position recorded, no hint
	got := h.ok(t, "get_station", map[string]any{"stationId": "STN-07"})
	if _, ok := got["activeHint"]; ok {
		t.Fatalf("activeHint must be absent: %v", got)
	}
}

func TestGetStationErrors(t *testing.T) {
	h := newHarness(t)
	failWith(t, h.session, "get_station", map[string]any{"stationId": "STN-99"}, "station-not-found")
	failWith(t, h.session, "get_station", map[string]any{"stationId": "a/b"}, "invalid-station-id")
}

func TestReadsEmitNothing(t *testing.T) {
	h := withHint(t)
	before := len(h.events.Names())
	h.ok(t, "get_site_policy", map[string]any{"siteId": "SITE-1"})
	h.ok(t, "list_hints", map[string]any{})
	h.ok(t, "get_hint", map[string]any{"hintId": "hint-test-001"})
	h.ok(t, "get_station", map[string]any{"stationId": "STN-07"})
	if after := len(h.events.Names()); after != before {
		t.Fatalf("events %d -> %d: a read tool emitted an event", before, after)
	}
}
