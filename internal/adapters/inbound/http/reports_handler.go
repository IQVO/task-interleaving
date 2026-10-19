package http

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/claudioed/task-interleaving/internal/analytics/report"
)

// ReportsServer is the inbound HTTP adapter of cmd/ti-reports: the read-only
// hint-adoption report of ADR 0008. It depends only on the report.Reader port
// over the analytical database; it never touches the OLTP use cases, and it
// writes nothing.
type ReportsServer struct {
	Reader report.Reader
	// Now is the clock the default range ends at and the freshness lag is
	// measured against (time.Now when nil).
	Now func() time.Time
}

const dayLayout = "2006-01-02"

// The wire shapes follow the fleet's analytics charter (snake_case, like every
// other context's reports and the freshness body); days are UTC calendar dates
// and the report structs never leak onto the wire.
type timingDTO struct {
	Count      int      `json:"count"`
	P50Seconds *float64 `json:"p50_seconds"`
	P95Seconds *float64 `json:"p95_seconds"`
}

type adoptionDayDTO struct {
	Day               string    `json:"day"`
	HintsIssued       int       `json:"hints_issued"`
	HintsConsumed     int       `json:"hints_consumed"`
	HintsExpired      int       `json:"hints_expired"`
	AdoptionRate      *float64  `json:"adoption_rate"`
	ConsumedFirstRank int       `json:"consumed_first_rank"`
	MeanDetourM       *float64  `json:"mean_detour_m"`
	TimeToConsume     timingDTO `json:"time_to_consume"`
}

type adoptionReportDTO struct {
	From        time.Time        `json:"from"`
	To          time.Time        `json:"to"`
	SiteID      string           `json:"site_id,omitempty"`
	Granularity string           `json:"granularity"`
	Days        []adoptionDayDTO `json:"days"`
}

// ReportsServiceName labels the reports binary in logs, distinct from the
// api's.
const ReportsServiceName = "task-interleaving-reports"

// NewReportsRouter builds the router of cmd/ti-reports: /healthz,
// GET /reports/hint-adoption and GET /reports/freshness. No auth middleware
// (fleet rule), no CORS: the service is cluster-internal.
func NewReportsRouter(s *ReportsServer) http.Handler {
	r := chi.NewRouter()
	r.Use(recoverPanic)
	r.Get("/healthz", handleHealthz)
	r.Get("/reports/hint-adoption", s.handleHintAdoption)
	r.Get("/reports/freshness", s.handleFreshness)
	return r
}

func (s *ReportsServer) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func timingToDTO(t report.Timing) timingDTO {
	return timingDTO{Count: t.Count, P50Seconds: t.P50, P95Seconds: t.P95}
}

var (
	invalidReportRange = problem{status: http.StatusBadRequest, slug: "invalid-report-range",
		title: "from and to must form a valid RFC 3339 range of at most 366 days"}
	invalidReportGranularity = problem{status: http.StatusBadRequest, slug: "invalid-report-granularity",
		title: "granularity must be \"day\""}
	reportStoreError = problem{status: http.StatusInternalServerError, slug: "report-store-error",
		title: "The report could not be served"}
)

// handleHintAdoption serves the per-day adoption over ?from=&to=&siteId= and
// ?granularity=day (the only granularity; default day).
func (s *ReportsServer) handleHintAdoption(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rg, err := report.ParseRange(q.Get("from"), q.Get("to"), s.now())
	if err != nil {
		writeProblem(w, r, invalidReportRange, err.Error())
		return
	}
	granularity, err := report.ParseGranularity(q.Get("granularity"))
	if err != nil {
		writeProblem(w, r, invalidReportGranularity, err.Error())
		return
	}
	site := q.Get("siteId")
	days, err := s.Reader.AdoptionDays(r.Context(), report.AdoptionQuery{Range: rg, SiteID: site, Granularity: granularity})
	if err != nil {
		writeReportStoreError(w, r, err)
		return
	}
	out := adoptionReportDTO{
		From: rg.From, To: rg.To, SiteID: site, Granularity: string(granularity),
		Days: make([]adoptionDayDTO, 0, len(days)),
	}
	for _, d := range days {
		out.Days = append(out.Days, adoptionDayDTO{
			Day: d.Day.UTC().Format(dayLayout), HintsIssued: d.Issued, HintsConsumed: d.Consumed, HintsExpired: d.Expired,
			AdoptionRate: report.AdoptionRate(d.Consumed, d.Expired), ConsumedFirstRank: d.ConsumedFirstRank,
			MeanDetourM: d.MeanDetourM, TimeToConsume: timingToDTO(d.TimeToConsume),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleFreshness serves how far the projection is behind (now - the newest
// applied event time). Both fields are null until the first event is applied.
func (s *ReportsServer) handleFreshness(w http.ResponseWriter, r *http.Request) {
	asOf, err := s.Reader.LastEventAt(r.Context())
	if err != nil {
		writeReportStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, report.ComputeFreshness(asOf, s.now()))
}

// writeReportStoreError is the 500 for a failed analytical query. The cause is
// logged, never echoed: it can carry SQL and connection details.
func writeReportStoreError(w http.ResponseWriter, r *http.Request, err error) {
	slog.ErrorContext(r.Context(), "report query failed", "error", err, "path", logSafe(r.URL.Path))
	writeProblem(w, r, reportStoreError, "the analytical database query failed")
}
