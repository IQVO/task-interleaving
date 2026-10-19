package http_test

import (
	"testing"
	"time"

	"github.com/claudioed/task-interleaving/internal/analytics/report"
)

// The ti-reports responses must have exactly the shape apis/openapi.yaml
// documents (required properties present, no undocumented property, types and
// nullability respected).
func TestReportsResponsesConformToSpec(t *testing.T) {
	doc := loadOpenAPI(t)
	asOf := reportsNow.Add(-time.Minute)
	r := &fakeReader{
		lastAt: &asOf,
		days: []report.AdoptionDay{
			{Day: time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC), Issued: 3, Consumed: 2, Expired: 1, ConsumedFirstRank: 1, MeanDetourM: f64(5),
				TimeToConsume: report.Timing{Count: 2, P50: f64(20), P95: f64(30)}},
			{Day: time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)},
		},
	}
	srv := reportsServer(t, r)

	_, _, body, raw := get(t, srv.URL+"/reports/hint-adoption?from=2026-10-05T00:00:00Z&to=2026-10-07T00:00:00Z&siteId=SITE-1")
	if problems := doc.conforms("HintAdoptionReport", body); len(problems) != 0 {
		t.Errorf("hint-adoption does not conform: %v\n%s", problems, raw)
	}
	_, _, body, raw = get(t, srv.URL+"/reports/freshness")
	if problems := doc.conforms("ReportsFreshness", body); len(problems) != 0 {
		t.Errorf("freshness does not conform: %v\n%s", problems, raw)
	}
	empty := reportsServer(t, &fakeReader{})
	_, _, body, raw = get(t, empty.URL+"/reports/freshness")
	if problems := doc.conforms("ReportsFreshness", body); len(problems) != 0 {
		t.Errorf("empty freshness does not conform: %v\n%s", problems, raw)
	}
}
