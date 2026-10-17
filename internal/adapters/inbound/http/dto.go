package http

import (
	"time"

	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/application/usecases"
	"github.com/claudioed/task-interleaving/internal/domain/recommend"
	"github.com/claudioed/task-interleaving/internal/domain/stationtrip"
	"github.com/claudioed/task-interleaving/internal/domain/travel"
)

// declarePolicyBody is the body of PUT /sites/{siteId}/policy. The fields are
// pointers so a missing one is told from a zero.
type declarePolicyBody struct {
	MaxDetourM        *float64 `json:"maxDetourM"`
	MaxCandidates     *int     `json:"maxCandidates"`
	HintTTLSeconds    *int64   `json:"hintTtlSeconds"`
	EligibleTaskTypes []string `json:"eligibleTaskTypes"`
	VerticalWeight    *float64 `json:"verticalWeight"`
}

// recommendationBody is the body of POST /recommendations.
type recommendationBody struct {
	StationID    string  `json:"stationId"`
	FromLocation *string `json:"fromLocation"`
}

type policyDTO struct {
	SiteID            string    `json:"siteId"`
	MaxDetourM        float64   `json:"maxDetourM"`
	MaxCandidates     int       `json:"maxCandidates"`
	HintTTLSeconds    int64     `json:"hintTtlSeconds"`
	EligibleTaskTypes []string  `json:"eligibleTaskTypes"`
	VerticalWeight    float64   `json:"verticalWeight"`
	Version           int64     `json:"version"`
	DeclaredAt        time.Time `json:"declaredAt"`
}

func toPolicy(rec repository.PolicyRecord) policyDTO {
	p := rec.Policy
	types := make([]string, 0, len(p.EligibleTaskTypes()))
	for _, t := range p.EligibleTaskTypes() {
		types = append(types, string(t))
	}
	return policyDTO{
		SiteID: string(p.SiteID()), MaxDetourM: p.MaxDetourM(), MaxCandidates: p.MaxCandidates(),
		HintTTLSeconds: int64(p.HintTTL() / time.Second), EligibleTaskTypes: types,
		VerticalWeight: p.VerticalWeight(), Version: p.Version(), DeclaredAt: rec.DeclaredAt.UTC(),
	}
}

type candidateDTO struct {
	Rank     int     `json:"rank"`
	TaskID   string  `json:"taskId"`
	TaskType string  `json:"taskType"`
	Location string  `json:"location"`
	DetourM  float64 `json:"detourM"`
}

// toCandidates maps the ranked candidates; the result is never nil, so an
// empty list is `[]`.
func toCandidates(cs []recommend.Candidate) []candidateDTO {
	out := make([]candidateDTO, 0, len(cs))
	for _, c := range cs {
		out = append(out, candidateDTO{
			Rank: c.Rank, TaskID: string(c.TaskID), TaskType: string(c.TaskType),
			Location: c.Location, DetourM: c.DetourM,
		})
	}
	return out
}

type hintDTO struct {
	HintID         string         `json:"hintId"`
	StationID      string         `json:"stationId"`
	SiteID         string         `json:"siteId"`
	State          string         `json:"state"`
	AfterTaskID    string         `json:"afterTaskId"`
	OriginLocation string         `json:"originLocation"`
	IssuedAt       time.Time      `json:"issuedAt"`
	ExpiresAt      time.Time      `json:"expiresAt"`
	PolicyVersion  int64          `json:"policyVersion"`
	Candidates     []candidateDTO `json:"candidates"`
	ConsumedTaskID string         `json:"consumedTaskId,omitempty"`
	ConsumedAt     *time.Time     `json:"consumedAt,omitempty"`
	SupersededAt   *time.Time     `json:"supersededAt,omitempty"`
	Version        int64          `json:"version"`
}

func toHint(rec repository.HintRecord) hintDTO {
	h := rec.Hint
	return hintDTO{
		HintID: string(h.ID), StationID: string(rec.StationID), SiteID: string(rec.SiteID),
		State: string(h.State), AfterTaskID: string(h.AfterTask), OriginLocation: h.Origin,
		IssuedAt: h.IssuedAt.UTC(), ExpiresAt: h.ExpiresAt.UTC(), PolicyVersion: h.PolicyVersion,
		Candidates: toCandidates(h.Candidates), ConsumedTaskID: string(h.ConsumedTask),
		ConsumedAt: optionalTime(h.ConsumedAt), SupersededAt: optionalTime(rec.SupersededAt),
		Version: rec.Version,
	}
}

type hintPageDTO struct {
	Items      []hintDTO `json:"items"`
	NextCursor string    `json:"nextCursor,omitempty"`
}

func toHintPage(page usecases.HintPage) hintPageDTO {
	items := make([]hintDTO, 0, len(page.Items))
	for _, rec := range page.Items {
		items = append(items, toHint(rec))
	}
	return hintPageDTO{Items: items, NextCursor: page.NextCursor}
}

type pointDTO struct {
	XM float64 `json:"xM"`
	YM float64 `json:"yM"`
	ZM float64 `json:"zM"`
}

type positionDTO struct {
	Location string    `json:"location"`
	Point    *pointDTO `json:"point,omitempty"`
}

type activeHintDTO struct {
	HintID        string         `json:"hintId"`
	State         string         `json:"state"`
	IssuedAt      time.Time      `json:"issuedAt"`
	ExpiresAt     time.Time      `json:"expiresAt"`
	PolicyVersion int64          `json:"policyVersion"`
	Candidates    []candidateDTO `json:"candidates"`
}

type stationDTO struct {
	StationID           string         `json:"stationId"`
	SiteID              string         `json:"siteId"`
	Position            positionDTO    `json:"position"`
	LastCompletedTaskID string         `json:"lastCompletedTaskId"`
	LastCompletedAt     time.Time      `json:"lastCompletedAt"`
	ActiveHint          *activeHintDTO `json:"activeHint,omitempty"`
	Version             int64          `json:"version"`
}

// toStation maps a trip that has a recorded position (the caller checked).
func toStation(trip *stationtrip.StationTrip, pos stationtrip.Position) stationDTO {
	task, at := trip.LastCompleted()
	dto := stationDTO{
		StationID: string(trip.ID()), SiteID: string(pos.Site),
		Position:            positionDTO{Location: pos.Place.Location, Point: toPoint(pos.Place.Point)},
		LastCompletedTaskID: string(task), LastCompletedAt: at.UTC(), Version: trip.Version(),
	}
	if h, ok := trip.ActiveHint(); ok {
		dto.ActiveHint = &activeHintDTO{
			HintID: string(h.ID), State: string(h.State), IssuedAt: h.IssuedAt.UTC(), ExpiresAt: h.ExpiresAt.UTC(),
			PolicyVersion: h.PolicyVersion, Candidates: toCandidates(h.Candidates),
		}
	}
	return dto
}

func toPoint(p *travel.Point) *pointDTO {
	if p == nil {
		return nil
	}
	return &pointDTO{XM: p.X, YM: p.Y, ZM: p.Z}
}

type recommendationDTO struct {
	StationID      string         `json:"stationId"`
	SiteID         string         `json:"siteId"`
	OriginLocation string         `json:"originLocation"`
	PolicyVersion  int64          `json:"policyVersion"`
	Candidates     []candidateDTO `json:"candidates"`
}

func toRecommendation(r usecases.Recommendation) recommendationDTO {
	return recommendationDTO{
		StationID: string(r.StationID), SiteID: string(r.SiteID), OriginLocation: r.OriginLocation,
		PolicyVersion: r.PolicyVersion, Candidates: toCandidates(r.Candidates),
	}
}

func optionalTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}
