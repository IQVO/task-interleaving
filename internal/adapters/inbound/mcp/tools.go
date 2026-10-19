package mcp

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/application/usecases"
	"github.com/claudioed/task-interleaving/internal/domain/recommend"
	"github.com/claudioed/task-interleaving/internal/domain/stationtrip"
	"github.com/claudioed/task-interleaving/internal/domain/travel"
)

// Deps is everything the MCP tools need, injected by the composition root.
// It carries the SAME read use cases the REST adapter uses (GET
// /sites/{siteId}/policy, /hints, /hints/{hintId}, /stations/{stationId});
// the adapter never constructs an outbound adapter itself, and it holds no
// write use case at all (docs/adr/0007). POST /recommendations is a
// simulation and stays REST-only.
type Deps struct {
	GetPolicy  *usecases.GetPolicy
	ListHints  *usecases.ListHints
	GetHint    *usecases.GetHint
	GetStation *usecases.GetStation
}

// --- inputs -------------------------------------------------------------------

type siteInput struct {
	SiteID string `json:"siteId" jsonschema:"the site whose interleaving policy to read, e.g. SITE-1"`
}

type hintIDInput struct {
	HintID string `json:"hintId" jsonschema:"the hint id, e.g. hint-1b4e28ba-2fa1-4d3c-8f6e-0a1b2c3d4e5f"`
}

type stationInput struct {
	StationID string `json:"stationId" jsonschema:"the station to read, e.g. STN-07"`
}

type listHintsInput struct {
	StationID string `json:"stationId,omitempty" jsonschema:"keep only hints issued to this station; omitted means every station"`
	SiteID    string `json:"siteId,omitempty" jsonschema:"keep only hints of this site; omitted means every site"`
	State     string `json:"state,omitempty" jsonschema:"keep only hints in this state: Active, Consumed, Expired or Superseded"`
	Limit     int    `json:"limit,omitempty" jsonschema:"page size, 1..500; omitted or 0 means the default page size"`
	Cursor    string `json:"cursor,omitempty" jsonschema:"the opaque nextCursor returned by the previous page; omitted means the first page"`
}

// --- outputs (the REST bodies, camelCase like the REST contract) --------------

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
	Version        int64           `json:"version"`
}

type hintPageView struct {
	Items      []hintView `json:"items"`
	NextCursor string     `json:"nextCursor,omitempty"`
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

type stationView struct {
	StationID           string          `json:"stationId"`
	SiteID              string          `json:"siteId"`
	Position            positionView    `json:"position"`
	LastCompletedTaskID string          `json:"lastCompletedTaskId"`
	LastCompletedAt     string          `json:"lastCompletedAt"`
	ActiveHint          *activeHintView `json:"activeHint,omitempty"`
	Version             int64           `json:"version"`
}

// --- mapping ------------------------------------------------------------------

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
		ConsumedAt: instant(h.ConsumedAt), SupersededAt: instant(rec.SupersededAt), Version: rec.Version,
	}
}

func toHintPage(page usecases.HintPage) hintPageView {
	items := make([]hintView, 0, len(page.Items))
	for _, rec := range page.Items {
		items = append(items, toHint(rec))
	}
	return hintPageView{Items: items, NextCursor: page.NextCursor}
}

func toPoint(p *travel.Point) *pointView {
	if p == nil {
		return nil
	}
	return &pointView{XM: p.X, YM: p.Y, ZM: p.Z}
}

func toStation(trip *stationtrip.StationTrip, pos stationtrip.Position) stationView {
	task, at := trip.LastCompleted()
	out := stationView{
		StationID: string(trip.ID()), SiteID: string(pos.Site),
		Position:            positionView{Location: pos.Place.Location, Point: toPoint(pos.Place.Point)},
		LastCompletedTaskID: string(task), LastCompletedAt: instant(at), Version: trip.Version(),
	}
	if h, ok := trip.ActiveHint(); ok {
		out.ActiveHint = &activeHintView{
			HintID: string(h.ID), State: string(h.State), IssuedAt: instant(h.IssuedAt), ExpiresAt: instant(h.ExpiresAt),
			PolicyVersion: h.PolicyVersion, Candidates: toCandidates(h.Candidates),
		}
	}
	return out
}

// --- handlers -----------------------------------------------------------------

func (d Deps) getSitePolicy(ctx context.Context, in siteInput) (policyView, error) {
	rec, err := d.GetPolicy.Handle(ctx, in.SiteID)
	if err != nil {
		return policyView{}, mapError(err)
	}
	return toPolicy(rec), nil
}

func (d Deps) listHints(ctx context.Context, in listHintsInput) (hintPageView, error) {
	page, err := d.ListHints.Handle(ctx, usecases.ListHintsQuery{
		StationID: in.StationID, SiteID: in.SiteID, State: in.State, Cursor: in.Cursor, Limit: in.Limit,
	})
	if err != nil {
		return hintPageView{}, mapError(err)
	}
	return toHintPage(page), nil
}

func (d Deps) getHint(ctx context.Context, in hintIDInput) (hintView, error) {
	rec, err := d.GetHint.Handle(ctx, in.HintID)
	if err != nil {
		return hintView{}, mapError(err)
	}
	return toHint(rec), nil
}

// getStation mirrors GET /stations/{stationId}: a trip that never recorded a
// completion has no position to report and is as unknown as a missing one.
func (d Deps) getStation(ctx context.Context, in stationInput) (stationView, error) {
	trip, err := d.GetStation.Handle(ctx, in.StationID)
	if err != nil {
		return stationView{}, mapError(err)
	}
	pos, ok := trip.Position()
	if !ok {
		return stationView{}, mapError(repository.ErrStationNotFound)
	}
	return toStation(trip, pos), nil
}

// --- registry -----------------------------------------------------------------

// registerTools adds the four read-only tools. Every tool is annotated
// read-only, idempotent and closed-world; TestToolSurface pins the set and
// fails the build on any write-verb name (docs/adr/0007).
func (d Deps) registerTools(server *mcp.Server) {
	closedWorld := false
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &closedWorld}

	addTool(server, &mcp.Tool{
		Name: "get_site_policy",
		Description: "Read the interleaving policy a site declared: maxDetourM (the farthest a candidate may lie from the " +
			"station), maxCandidates (1..5), hintTtlSeconds, eligibleTaskTypes, verticalWeight, version and declaredAt. " +
			"A site that never declared a policy is inert (no hint is ever issued for it) and fails with policy-not-found. " +
			"Read-only; a malformed site fails with invalid-site-id.",
		Annotations: readOnly,
	}, d.getSitePolicy)

	addTool(server, &mcp.Tool{
		Name: "list_hints",
		Description: "List interleaving hints newest first, a page at a time, optionally only those issued to one station " +
			"(stationId), of one site (siteId) and/or in one state (Active, Consumed, Expired or Superseded). Each hint " +
			"carries its ranked candidates (rank, taskId, taskType, location, detourM) and how it ended. Pass the " +
			"returned nextCursor to get the next page; it is absent on the last page. Read-only; a bad limit fails with " +
			"invalid-limit, a bad cursor with invalid-cursor, an unknown state with invalid-state and a malformed " +
			"station or site with invalid-station-id or invalid-site-id.",
		Annotations: readOnly,
	}, d.listHints)

	addTool(server, &mcp.Tool{
		Name: "get_hint",
		Description: "Read one interleaving hint by id: stationId, siteId, state (Active, Consumed, Expired or Superseded), " +
			"the task it follows (afterTaskId) and the origin location, issuedAt and expiresAt, the policyVersion it was " +
			"ranked under, its ranked candidates, and consumedTaskId/consumedAt or supersededAt once it ended. Read-only; " +
			"fails with hint-not-found for an unknown id and invalid-hint-id for a malformed one.",
		Annotations: readOnly,
	}, d.getHint)

	addTool(server, &mcp.Tool{
		Name: "get_station",
		Description: "Read what the service knows about a station: its site, its position (location code and the point in " +
			"metres when the geometry is known), the last task it completed and when, and its active hint if it holds " +
			"one (the short form: hintId, state, issuedAt, expiresAt, policyVersion, candidates). Read-only; fails with " +
			"station-not-found when no completion was recorded for the station and invalid-station-id for a malformed id.",
		Annotations: readOnly,
	}, d.getStation)
}

// addTool registers one tool. A handler error is returned to the SDK as the
// handler's error, which the SDK turns into an isError tool result (never a
// transport/protocol failure), carrying the error text.
func addTool[In, Out any](
	server *mcp.Server,
	tool *mcp.Tool,
	handle func(context.Context, In) (Out, error),
) {
	mcp.AddTool(server, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		out, err := handle(ctx, in)
		if err != nil {
			var zero Out
			return nil, zero, err
		}
		return nil, out, nil
	})
}
