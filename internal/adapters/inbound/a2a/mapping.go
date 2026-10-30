package a2a

import (
	"fmt"
	"strings"
	"time"

	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/application/usecases"
	"github.com/claudioed/task-interleaving/internal/domain/recommend"
	"github.com/claudioed/task-interleaving/internal/domain/stationtrip"
	"github.com/claudioed/task-interleaving/internal/domain/travel"
)

// problem is the RFC 7807-shaped rejection an agent client receives instead of
// a result: domain rejections and request-shape failures are business answers,
// not transport errors, so they travel in the message envelope. Type is the
// REST adapter's problem type URI (same namespace, same slug) so a delegating
// agent sees the outcome a REST caller would.
type problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Status int    `json:"status"`
}

// problemBaseURI is the namespace of this service's RFC 7807 `type` URIs, the
// same one the REST adapter uses.
const problemBaseURI = "https://errors.task-interleaving.warehouse-systems.dev/"

func restProblem(status int, slug, title, detail string) *problem {
	return &problem{Type: problemBaseURI + slug, Title: title, Detail: detail, Status: status}
}

func problemMalformedRequest(detail string) *problem {
	return restProblem(400, "malformed-skill-request", "Malformed skill request", detail)
}

func problemUnknownSkill(skill string) *problem {
	return restProblem(400, "unknown-skill", "Unknown skill",
		fmt.Sprintf("skill %q is not offered by this agent; the Agent Card lists the supported skills", skill))
}

func problemInvalidArgument(err error) *problem {
	return restProblem(400, "invalid-argument", "Invalid skill argument", err.Error())
}

func problemInvalidLimit() *problem {
	return restProblem(400, "invalid-limit", "Limit is invalid",
		fmt.Sprintf("limit must be between 1 and %d", maxRecentHints))
}

// summary renders the one-line form used for the reply's text part.
func (p *problem) summary() string {
	return fmt.Sprintf("%s (%d): %s", p.Title, p.Status, p.Detail)
}

// envelope is the structured payload of every skill answer: exactly one of
// Data (a skill DTO) or Problem (why there is no result), plus the skill id
// the answer belongs to.
type envelope struct {
	Skill   string   `json:"skill"`
	Data    skillDTO `json:"data,omitempty"`
	Problem *problem `json:"problem,omitempty"`
}

// skillDTO is the union of the skill result DTOs. A2A parts are
// machine-readable JSON; each skill fills its own field and summary, keeping
// the envelope flat and versioned as one contract.
type skillDTO struct {
	StationHintStatus        *stationHintStatus        `json:"stationHintStatus,omitempty"`
	SiteInterleavingOverview *siteInterleavingOverview `json:"siteInterleavingOverview,omitempty"`
	summary                  string
}

// Summary is the human-readable one-liner for the reply's text part.
func (s skillDTO) Summary() string { return s.summary }

// --- DTOs (the REST/MCP bodies, camelCase; domain types never cross) -----------

type policyView struct {
	SiteID            string   `json:"siteId"`
	MaxDetourM        float64  `json:"maxDetourM"`
	MaxCandidates     int      `json:"maxCandidates"`
	HintTTLSeconds    int64    `json:"hintTtlSeconds"`
	EligibleTaskTypes []string `json:"eligibleTaskTypes"`
	VerticalWeight    float64  `json:"verticalWeight"`
	Version           int64    `json:"version"`
	DeclaredAt        string   `json:"declaredAt"`
}

type candidateView struct {
	Rank     int     `json:"rank"`
	TaskID   string  `json:"taskId"`
	TaskType string  `json:"taskType"`
	Location string  `json:"location"`
	DetourM  float64 `json:"detourM"`
}

type hintView struct {
	HintID         string          `json:"hintId"`
	StationID      string          `json:"stationId"`
	SiteID         string          `json:"siteId"`
	State          string          `json:"state"`
	AfterTaskID    string          `json:"afterTaskId"`
	OriginLocation string          `json:"originLocation"`
	IssuedAt       string          `json:"issuedAt"`
	ExpiresAt      string          `json:"expiresAt"`
	PolicyVersion  int64           `json:"policyVersion"`
	Candidates     []candidateView `json:"candidates"`
	ConsumedTaskID string          `json:"consumedTaskId,omitempty"`
	ConsumedAt     string          `json:"consumedAt,omitempty"`
	SupersededAt   string          `json:"supersededAt,omitempty"`
}

type pointView struct {
	XM float64 `json:"xM"`
	YM float64 `json:"yM"`
	ZM float64 `json:"zM"`
}

type positionView struct {
	Location string     `json:"location"`
	Point    *pointView `json:"point,omitempty"`
}

type activeHintView struct {
	HintID        string          `json:"hintId"`
	State         string          `json:"state"`
	IssuedAt      string          `json:"issuedAt"`
	ExpiresAt     string          `json:"expiresAt"`
	PolicyVersion int64           `json:"policyVersion"`
	Candidates    []candidateView `json:"candidates"`
}

// stationHintStatus is the DTO of the station_hint_status skill: the
// GetStation read model (position, last completion, active hint).
type stationHintStatus struct {
	StationID           string          `json:"stationId"`
	SiteID              string          `json:"siteId"`
	Position            positionView    `json:"position"`
	LastCompletedTaskID string          `json:"lastCompletedTaskId"`
	LastCompletedAt     string          `json:"lastCompletedAt"`
	ActiveHint          *activeHintView `json:"activeHint,omitempty"`
	summary             string
}

// siteInterleavingOverview is the DTO of the site_interleaving_overview skill:
// the declared policy and a window of the newest hints with a count per state.
type siteInterleavingOverview struct {
	Policy       policyView     `json:"policy"`
	RecentHints  []hintView     `json:"recentHints"`
	HintsByState map[string]int `json:"hintsByState"`
	HasMore      bool           `json:"hasMore"`
	summary      string
}

// --- mapping -------------------------------------------------------------------

// instant formats an instant as RFC 3339 UTC; the zero time is "" (omitted).
func instant(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func toPolicy(rec repository.PolicyRecord) policyView {
	p := rec.Policy
	types := make([]string, 0, len(p.EligibleTaskTypes()))
	for _, t := range p.EligibleTaskTypes() {
		types = append(types, string(t))
	}
	return policyView{
		SiteID: string(p.SiteID()), MaxDetourM: p.MaxDetourM(), MaxCandidates: p.MaxCandidates(),
		HintTTLSeconds: int64(p.HintTTL() / time.Second), EligibleTaskTypes: types,
		VerticalWeight: p.VerticalWeight(), Version: p.Version(), DeclaredAt: instant(rec.DeclaredAt),
	}
}

// toCandidates maps the ranked candidates; the result is never nil.
func toCandidates(cs []recommend.Candidate) []candidateView {
	out := make([]candidateView, 0, len(cs))
	for _, c := range cs {
		out = append(out, candidateView{
			Rank: c.Rank, TaskID: string(c.TaskID), TaskType: string(c.TaskType),
			Location: c.Location, DetourM: c.DetourM,
		})
	}
	return out
}

func toHint(rec repository.HintRecord) hintView {
	h := rec.Hint
	return hintView{
		HintID: string(h.ID), StationID: string(rec.StationID), SiteID: string(rec.SiteID),
		State: string(h.State), AfterTaskID: string(h.AfterTask), OriginLocation: h.Origin,
		IssuedAt: instant(h.IssuedAt), ExpiresAt: instant(h.ExpiresAt), PolicyVersion: h.PolicyVersion,
		Candidates: toCandidates(h.Candidates), ConsumedTaskID: string(h.ConsumedTask),
		ConsumedAt: instant(h.ConsumedAt), SupersededAt: instant(rec.SupersededAt),
	}
}

func toPoint(p *travel.Point) *pointView {
	if p == nil {
		return nil
	}
	return &pointView{XM: p.X, YM: p.Y, ZM: p.Z}
}

func toStationHintStatus(trip *stationtrip.StationTrip, pos stationtrip.Position) stationHintStatus {
	task, at := trip.LastCompleted()
	out := stationHintStatus{
		StationID: string(trip.ID()), SiteID: string(pos.Site),
		Position:            positionView{Location: pos.Place.Location, Point: toPoint(pos.Place.Point)},
		LastCompletedTaskID: string(task), LastCompletedAt: instant(at),
	}
	if h, ok := trip.ActiveHint(); ok {
		out.ActiveHint = &activeHintView{
			HintID: string(h.ID), State: string(h.State), IssuedAt: instant(h.IssuedAt), ExpiresAt: instant(h.ExpiresAt),
			PolicyVersion: h.PolicyVersion, Candidates: toCandidates(h.Candidates),
		}
	}
	out.summary = out.describe()
	return out
}

// describe is the one-line answer of station_hint_status.
func (s stationHintStatus) describe() string {
	head := fmt.Sprintf("%s (%s) at %s", s.StationID, s.SiteID, s.Position.Location)
	if s.ActiveHint == nil {
		return head + ": no active hint"
	}
	h := s.ActiveHint
	if len(h.Candidates) == 0 {
		return fmt.Sprintf("%s: active hint %s (expires %s) with no candidates", head, h.HintID, h.ExpiresAt)
	}
	first := h.Candidates[0]
	return fmt.Sprintf("%s: active hint %s (expires %s) ranks %s first, %g m detour (%d candidates)",
		head, h.HintID, h.ExpiresAt, first.TaskID, first.DetourM, len(h.Candidates))
}

func toSiteInterleavingOverview(rec repository.PolicyRecord, page usecases.HintPage) siteInterleavingOverview {
	out := siteInterleavingOverview{
		Policy:       toPolicy(rec),
		RecentHints:  make([]hintView, 0, len(page.Items)),
		HintsByState: map[string]int{},
		HasMore:      page.NextCursor != "",
	}
	for _, h := range page.Items {
		view := toHint(h)
		out.RecentHints = append(out.RecentHints, view)
		out.HintsByState[view.State]++
	}
	out.summary = out.describe()
	return out
}

// describe is the one-line answer of site_interleaving_overview.
func (o siteInterleavingOverview) describe() string {
	p := o.Policy
	head := fmt.Sprintf("%s policy v%d: max detour %g m, up to %d candidates, hint TTL %d s", p.SiteID, p.Version, p.MaxDetourM, p.MaxCandidates, p.HintTTLSeconds)
	if len(o.RecentHints) == 0 {
		return head + "; no hints issued yet"
	}
	var states []string
	for _, state := range []string{"Active", "Consumed", "Expired", "Superseded"} {
		if n := o.HintsByState[state]; n > 0 {
			states = append(states, fmt.Sprintf("%d %s", n, state))
		}
	}
	return fmt.Sprintf("%s; last %d hints: %s", head, len(o.RecentHints), strings.Join(states, ", "))
}
