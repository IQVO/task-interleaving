package http

import (
	"net/http"
	"time"

	"github.com/claudioed/task-interleaving/internal/application/usecases"
	"github.com/claudioed/task-interleaving/internal/domain/policy"
)

// handleDeclarePolicy backs PUT /sites/{siteId}/policy: 200 with the policy
// and its version as ETag. The first declaration sends no If-Match; replacing
// an existing policy needs the current version (412 version-mismatch
// otherwise, decided by the use case).
func (s *Server) handleDeclarePolicy(w http.ResponseWriter, r *http.Request) {
	site, err := pathParam(r, "siteId", policy.ErrInvalidSiteID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	in, err := declareInput(r, site)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if ifMatch, present := parseIfMatch(r); present {
		in.IfMatch = &ifMatch
	}
	rec, err := s.DeclarePolicy.Handle(r.Context(), in)
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(rec.Policy.Version()))
	writeJSON(w, http.StatusOK, toPolicy(rec))
}

// declareInput decodes the PUT body. A missing required field is reported as
// the domain error of that setting; the policy package owns the ranges.
func declareInput(r *http.Request, site string) (usecases.DeclarePolicyInput, error) {
	var body declarePolicyBody
	if err := decodeJSON(r, &body); err != nil {
		return usecases.DeclarePolicyInput{}, err
	}
	if err := requiredSettings(body); err != nil {
		return usecases.DeclarePolicyInput{}, err
	}
	if *body.HintTTLSeconds > maxHintTTLSeconds {
		return usecases.DeclarePolicyInput{}, errHintTTLTooLarge
	}
	return usecases.DeclarePolicyInput{
		SiteID: site, MaxDetourM: *body.MaxDetourM, MaxCandidates: *body.MaxCandidates,
		HintTTL:           time.Duration(*body.HintTTLSeconds) * time.Second,
		EligibleTaskTypes: body.EligibleTaskTypes, VerticalWeight: body.VerticalWeight,
	}, nil
}

// requiredSettings rejects a body that leaves out a required field.
func requiredSettings(b declarePolicyBody) error {
	if b.MaxDetourM == nil {
		return policy.ErrInvalidMaxDetour
	}
	if b.MaxCandidates == nil {
		return policy.ErrInvalidCandidates
	}
	if b.HintTTLSeconds == nil {
		return policy.ErrInvalidHintTTL
	}
	if b.EligibleTaskTypes == nil {
		return policy.ErrInvalidTaskTypes
	}
	return nil
}

// handleGetPolicy backs GET /sites/{siteId}/policy.
func (s *Server) handleGetPolicy(w http.ResponseWriter, r *http.Request) {
	site, err := pathParam(r, "siteId", policy.ErrInvalidSiteID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	rec, err := s.GetPolicy.Handle(r.Context(), site)
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(rec.Policy.Version()))
	writeJSON(w, http.StatusOK, toPolicy(rec))
}
