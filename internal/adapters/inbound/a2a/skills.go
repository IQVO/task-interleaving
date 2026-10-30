package a2a

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/application/usecases"
	"github.com/claudioed/task-interleaving/internal/domain/policy"
	"github.com/claudioed/task-interleaving/internal/domain/stationtrip"
)

// tracerName is the OTel instrumentation scope for A2A skill spans, mirroring
// the MCP adapter's mcp.tool spans.
const tracerName = "github.com/claudioed/task-interleaving/internal/adapters/inbound/a2a"

// Skill ids advertised on the Agent Card (see Card).
const (
	skillStationHintStatus        = "station_hint_status"
	skillSiteInterleavingOverview = "site_interleaving_overview"
)

// Bounds of the recent-hint window of site_interleaving_overview.
const (
	defaultRecentHints = 5
	maxRecentHints     = 20
)

// skillRequest is the wire contract of every skill call: the first meaningful
// message part carries (as a Data part, or a Text part holding the same JSON)
// the skill id and its arguments. Strict on purpose: an agent client is
// machine-to-machine, so unknown fields are rejected, never ignored, and a
// misspelled name like "lmit" must not silently drop the window size. (Go's
// encoding/json matches field names case-insensitively, so "LIMIT" still
// reaches limit; only genuinely unknown names are refused.)
type skillRequest struct {
	Skill     string          `json:"skill"`
	Arguments json.RawMessage `json:"arguments"`
}

// stationHintStatusArgs and siteInterleavingOverviewArgs are the per-skill
// argument shapes. Limit is a pointer so an explicit 0 is refused instead of
// being read as "default".
type stationHintStatusArgs struct {
	StationID string `json:"stationId"`
}

type siteInterleavingOverviewArgs struct {
	SiteID string `json:"siteId"`
	Limit  *int   `json:"limit"`
}

// answer maps one incoming message to a skill and returns the agent's reply
// Message. It is the whole executor body: this is a message-only agent, so a
// skill run is exactly one request mapped to one answer (docs/adr/0009).
func answer(ctx context.Context, deps Deps, ec *a2asrv.ExecutorContext) a2a.Event {
	req, problem := parseSkillRequest(ec)
	if problem != nil {
		return reply(ec, envelope{Skill: "", Problem: problem}, problem.summary())
	}

	ctx, span := otel.Tracer(tracerName).Start(ctx, "a2a.skill "+req.Skill)
	defer span.End()
	span.SetAttributes(attribute.String("a2a.skill", req.Skill))

	dto, problem := run(ctx, deps, req)
	if problem != nil {
		span.SetStatus(codes.Error, problem.Detail)
		return reply(ec, envelope{Skill: req.Skill, Problem: problem}, problem.summary())
	}
	return reply(ec, envelope{Skill: req.Skill, Data: dto}, dto.Summary())
}

// run dispatches one validated request to its skill.
func run(ctx context.Context, deps Deps, req skillRequest) (skillDTO, *problem) {
	switch req.Skill {
	case skillStationHintStatus:
		data, problem := deps.stationHintStatus(ctx, req.Arguments)
		if problem != nil {
			return skillDTO{}, problem
		}
		return skillDTO{StationHintStatus: &data, summary: data.summary}, nil
	case skillSiteInterleavingOverview:
		data, problem := deps.siteInterleavingOverview(ctx, req.Arguments)
		if problem != nil {
			return skillDTO{}, problem
		}
		return skillDTO{SiteInterleavingOverview: &data, summary: data.summary}, nil
	default:
		return skillDTO{}, problemUnknownSkill(req.Skill)
	}
}

// reply builds the agent Message for one skill run: a Data part carrying the
// structured envelope and a Text part carrying the one-line summary, bound to
// the caller's context (and task, when the client referenced one).
func reply(ec *a2asrv.ExecutorContext, env envelope, summary string) a2a.Event {
	return a2a.NewMessageForTask(a2a.MessageRoleAgent, ec, a2a.NewDataPart(env), a2a.NewTextPart(summary))
}

// parseSkillRequest extracts the skillRequest from the message's first
// meaningful part. A Data part (or a Text part holding the same JSON object)
// is accepted; anything else is a request-shape problem, answered, not raised:
// a malformed delegation is a business answer for the calling agent, not a
// transport failure.
func parseSkillRequest(ec *a2asrv.ExecutorContext) (skillRequest, *problem) {
	msg := ec.Message
	if msg == nil || len(msg.Parts) == 0 {
		return skillRequest{}, problemMalformedRequest("the message carries no parts")
	}
	raw, err := partJSON(msg.Parts[0])
	if err != nil {
		return skillRequest{}, problemMalformedRequest(fmt.Sprintf("the first message part is not a skill request: %v", err))
	}
	var req skillRequest
	if err := decodeStrict(raw, &req); err != nil {
		return skillRequest{}, problemMalformedRequest(fmt.Sprintf("the skill request is not valid: %v", err))
	}
	if req.Skill == "" {
		return skillRequest{}, problemMalformedRequest(`the skill request carries no "skill" field`)
	}
	return req, nil
}

// partJSON renders one part's content as the JSON bytes of a skill request.
func partJSON(part *a2a.Part) ([]byte, error) {
	switch content := part.Content.(type) {
	case a2a.Data:
		return json.Marshal(content.Value)
	case a2a.Text:
		return []byte(string(content)), nil
	default:
		return nil, errors.New("expected a data part (or a text part holding the skill JSON)")
	}
}

// decodeStrict decodes one JSON document into dst, rejecting unknown fields
// and trailing data. Absent arguments decode as the empty object so each
// skill's own required-field checks produce the specific problem.
func decodeStrict(raw []byte, dst any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("unexpected data after the JSON document")
	}
	return nil
}

// stationHintStatus mirrors the MCP get_station tool: a trip that never
// recorded a completion has no position to report and is as unknown as a
// missing one (the REST GET /stations/{stationId} rule).
func (d Deps) stationHintStatus(ctx context.Context, rawArgs json.RawMessage) (stationHintStatus, *problem) {
	var args stationHintStatusArgs
	if err := decodeStrict(rawArgs, &args); err != nil {
		return stationHintStatus{}, problemInvalidArgument(fmt.Errorf("arguments: %w", err))
	}
	if strings.TrimSpace(args.StationID) == "" {
		return stationHintStatus{}, problemInvalidArgument(errors.New("stationId is required"))
	}
	trip, err := d.GetStation.Handle(ctx, args.StationID)
	if err != nil {
		return stationHintStatus{}, problemFromUseCase(err)
	}
	pos, ok := trip.Position()
	if !ok {
		return stationHintStatus{}, problemFromUseCase(repository.ErrStationNotFound)
	}
	return toStationHintStatus(trip, pos), nil
}

// siteInterleavingOverview composes the policy read and the newest hints of
// the site. A site without a declared policy is inert and answered as
// policy-not-found, like GET /sites/{siteId}/policy and the MCP
// get_site_policy tool.
func (d Deps) siteInterleavingOverview(ctx context.Context, rawArgs json.RawMessage) (siteInterleavingOverview, *problem) {
	var args siteInterleavingOverviewArgs
	if err := decodeStrict(rawArgs, &args); err != nil {
		return siteInterleavingOverview{}, problemInvalidArgument(fmt.Errorf("arguments: %w", err))
	}
	if strings.TrimSpace(args.SiteID) == "" {
		return siteInterleavingOverview{}, problemInvalidArgument(errors.New("siteId is required"))
	}
	limit := defaultRecentHints
	if args.Limit != nil {
		limit = *args.Limit
	}
	if limit < 1 || limit > maxRecentHints {
		return siteInterleavingOverview{}, problemInvalidLimit()
	}
	rec, err := d.GetPolicy.Handle(ctx, args.SiteID)
	if err != nil {
		return siteInterleavingOverview{}, problemFromUseCase(err)
	}
	page, err := d.ListHints.Handle(ctx, usecases.ListHintsQuery{SiteID: args.SiteID, Limit: limit})
	if err != nil {
		return siteInterleavingOverview{}, problemFromUseCase(err)
	}
	return toSiteInterleavingOverview(rec, page), nil
}

// problemFromUseCase maps a use case error to a problem answer, using the SAME
// classification and slugs as the REST adapter's problemCatalogue
// (internal/adapters/inbound/http/errors.go) restricted to what a read can
// raise, so a delegating agent sees the outcome a REST caller would. Domain
// rejections are EXPECTED business outcomes, answered as RFC 7807-shaped
// problems; anything unrecognised is an internal failure, still answered as a
// problem (status 500) so the calling agent always receives a structured,
// machine-readable result. Details are adapter-owned text: a domain error's own
// message (package prefixes, wrapped storage errors) never reaches the caller.
func problemFromUseCase(err error) *problem {
	switch {
	case errors.Is(err, policy.ErrInvalidSiteID):
		return restProblem(http.StatusBadRequest, "invalid-site-id", "Site id is invalid",
			"siteId must be 1..64 characters without whitespace, control characters or '/'")
	case errors.Is(err, stationtrip.ErrInvalidStationID):
		return restProblem(http.StatusBadRequest, "invalid-station-id", "Station id is invalid",
			"stationId must be 1..64 characters without whitespace, control characters or '/'")
	case errors.Is(err, usecases.ErrInvalidLimit):
		return problemInvalidLimit()
	case errors.Is(err, repository.ErrPolicyNotFound):
		return restProblem(http.StatusNotFound, "policy-not-found", "Interleaving policy not found",
			"the site has not declared an interleaving policy")
	case errors.Is(err, repository.ErrStationNotFound):
		return restProblem(http.StatusNotFound, "station-not-found", "Station not found",
			"no completion has been recorded for the station")
	default:
		slog.Error("a2a skill failed", "error", err)
		return restProblem(http.StatusInternalServerError, "internal-error", "Internal server error",
			"the skill failed; see server logs (trace id in the response metadata)")
	}
}
