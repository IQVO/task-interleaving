package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	inboundhttp "github.com/claudioed/task-interleaving/internal/adapters/inbound/http"
	"github.com/claudioed/task-interleaving/internal/analytics/report"
)

var reportsNow = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

// fakeReader is a scripted report.Reader.
type fakeReader struct {
	days    []report.AdoptionDay
	query   report.AdoptionQuery
	lastAt  *time.Time
	failing error
}

func (f *fakeReader) AdoptionDays(_ context.Context, q report.AdoptionQuery) ([]report.AdoptionDay, error) {
	f.query = q
	return f.days, f.failing
}

func (f *fakeReader) LastEventAt(context.Context) (*time.Time, error) { return f.lastAt, f.failing }

func reportsServer(t *testing.T, r *fakeReader) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(inboundhttp.NewReportsRouter(&inboundhttp.ReportsServer{Reader: r, Now: func() time.Time { return reportsNow }}))
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, url string) (int, http.Header, map[string]any, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	return resp.StatusCode, resp.Header, body, string(raw)
}

func f64(v float64) *float64 { return &v }

func adoptionFixture() *fakeReader {
	return &fakeReader{days: []report.AdoptionDay{
		{
			Day: time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC), Issued: 12, Consumed: 6, Expired: 2, ConsumedFirstRank: 4, MeanDetourM: f64(5.5),
			TimeToConsume: report.Timing{Count: 5, P50: f64(20), P95: f64(70)},
		},
		{Day: time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)},
	}}
}

func expectFields(t *testing.T, got map[string]any, want map[string]any) {
	t.Helper()
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
}

func TestReports_HintAdoptionShape(t *testing.T) {
	r := adoptionFixture()
	srv := reportsServer(t, r)
	status, header, body, raw := get(t, srv.URL+"/reports/hint-adoption?from=2026-10-05T00:00:00Z&to=2026-10-07T00:00:00Z&siteId=SITE-1")
	if status != 200 || header.Get("Content-Type") != "application/json" {
		t.Fatalf("status %d %s", status, raw)
	}
	expectFields(t, body, map[string]any{
		"site_id": "SITE-1", "granularity": "day", "from": "2026-10-05T00:00:00Z", "to": "2026-10-07T00:00:00Z",
	})
	days, _ := body["days"].([]any)
	if len(days) != 2 {
		t.Fatalf("days: %s", raw)
	}
	d0 := days[0].(map[string]any)
	expectFields(t, d0, map[string]any{
		"day": "2026-10-05", "hints_issued": 12.0, "hints_consumed": 6.0, "hints_expired": 2.0, "adoption_rate": 0.75,
		"consumed_first_rank": 4.0, "mean_detour_m": 5.5,
	})
	expectFields(t, d0["time_to_consume"].(map[string]any), map[string]any{"count": 5.0, "p50_seconds": 20.0, "p95_seconds": 70.0})
	if r.query.SiteID != "SITE-1" || r.query.Granularity != report.GranularityDay ||
		!r.query.Range.From.Equal(time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("query = %+v", r.query)
	}
}

// An empty day carries explicit nulls, never omitted fields.
func TestReports_EmptyDayHasExplicitNulls(t *testing.T) {
	srv := reportsServer(t, adoptionFixture())
	_, _, body, _ := get(t, srv.URL+"/reports/hint-adoption?from=2026-10-05T00:00:00Z&to=2026-10-07T00:00:00Z")
	d1 := body["days"].([]any)[1].(map[string]any)
	for _, k := range []string{"adoption_rate", "mean_detour_m"} {
		if v, ok := d1[k]; !ok || v != nil {
			t.Errorf("empty day %s = %v (present %v), want an explicit null", k, v, ok)
		}
	}
	expectFields(t, d1["time_to_consume"].(map[string]any), map[string]any{"count": 0.0, "p50_seconds": nil, "p95_seconds": nil})
}

func TestReports_DefaultRangeIsTheLast30Days(t *testing.T) {
	r := &fakeReader{}
	srv := reportsServer(t, r)
	status, _, body, raw := get(t, srv.URL+"/reports/hint-adoption")
	if status != 200 {
		t.Fatalf("status %d %s", status, raw)
	}
	if body["to"] != "2026-10-07T12:00:00Z" || body["from"] != "2026-09-07T12:00:00Z" {
		t.Fatalf("range: %s", raw)
	}
	if _, ok := body["site_id"]; ok {
		t.Fatalf("site_id must be omitted when unscoped: %s", raw)
	}
	if days, ok := body["days"].([]any); !ok || len(days) != 0 {
		t.Fatalf("days must be an array even when empty: %s", raw)
	}
}

func TestReports_BadQueriesAreProblems(t *testing.T) {
	srv := reportsServer(t, &fakeReader{})
	for query, slug := range map[string]string{
		"from=yesterday": "invalid-report-range",
		"to=tomorrow":    "invalid-report-range",
		"from=2026-10-06T00:00:00Z&to=2026-10-05T00:00:00Z": "invalid-report-range",
		"from=2024-01-01T00:00:00Z&to=2026-01-01T00:00:00Z": "invalid-report-range",
		"granularity=hour": "invalid-report-granularity",
	} {
		status, header, body, raw := get(t, srv.URL+"/reports/hint-adoption?"+query)
		if status != 400 || header.Get("Content-Type") != "application/problem+json" ||
			body["type"] != "https://errors.task-interleaving.warehouse-systems.dev/"+slug {
			t.Errorf("?%s = %d %s, want a 400 %s problem", query, status, raw, slug)
		}
	}
}

func TestReports_Freshness(t *testing.T) {
	asOf := reportsNow.Add(-4200 * time.Millisecond)
	srv := reportsServer(t, &fakeReader{lastAt: &asOf})
	status, _, body, raw := get(t, srv.URL+"/reports/freshness")
	if status != 200 || body["as_of"] != "2026-10-07T11:59:55.8Z" || body["lag_seconds"] != 4.2 {
		t.Fatalf("freshness = %d %s", status, raw)
	}
	empty := reportsServer(t, &fakeReader{})
	status, _, body, raw = get(t, empty.URL+"/reports/freshness")
	if status != 200 || body["as_of"] != nil || body["lag_seconds"] != nil || !strings.Contains(raw, `"as_of":null`) {
		t.Fatalf("empty freshness = %d %s, want explicit nulls", status, raw)
	}
}

func TestReports_StoreFailureIsAnOpaque500(t *testing.T) {
	srv := reportsServer(t, &fakeReader{failing: errors.New("password=hunter2 host=db.internal")})
	for _, path := range []string{"/reports/hint-adoption", "/reports/freshness"} {
		status, header, body, raw := get(t, srv.URL+path)
		if status != 500 || header.Get("Content-Type") != "application/problem+json" ||
			body["type"] != "https://errors.task-interleaving.warehouse-systems.dev/report-store-error" {
			t.Fatalf("%s = %d %s", path, status, raw)
		}
		if strings.Contains(raw, "hunter2") || strings.Contains(raw, "db.internal") {
			t.Fatalf("%s leaked the cause: %s", path, raw)
		}
	}
}

func TestReports_HealthzAndNothingElse(t *testing.T) {
	srv := reportsServer(t, &fakeReader{})
	if status, _, body, raw := get(t, srv.URL+"/healthz"); status != 200 || body["status"] != "ok" {
		t.Fatalf("healthz = %d %s", status, raw)
	}
	// The reports binary serves no OLTP route.
	for _, path := range []string{"/hints", "/stations/STN-1", "/sites/SITE-1/policy", "/readyz"} {
		if status, _, _, _ := get(t, srv.URL+path); status != 404 {
			t.Errorf("GET %s = %d, want 404", path, status)
		}
	}
	// Read-only: a write verb is refused.
	resp, err := http.Post(srv.URL+"/reports/hint-adoption", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d, want 405", resp.StatusCode)
	}
}
