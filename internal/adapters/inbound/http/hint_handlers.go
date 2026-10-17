package http

import (
	"net/http"

	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/application/usecases"
	"github.com/claudioed/task-interleaving/internal/domain/policy"
	"github.com/claudioed/task-interleaving/internal/domain/stationtrip"
)

// handleListHints backs GET /hints.
func (s *Server) handleListHints(w http.ResponseWriter, r *http.Request) {
	limit, err := intParam(r, "limit", usecases.ErrInvalidLimit)
	if err != nil {
		writeError(w, r, err)
		return
	}
	q, err := hintQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	q.Limit = limit
	page, err := s.ListHints.Handle(r.Context(), q)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toHintPage(page))
}

// hintQuery reads the filters and the cursor. A filter that is present but
// empty fails the contract's `pattern: '\S'` like a blank one does.
func hintQuery(r *http.Request) (usecases.ListHintsQuery, error) {
	v := r.URL.Query()
	if v.Has("stationId") && isBlank(v.Get("stationId")) {
		return usecases.ListHintsQuery{}, stationtrip.ErrInvalidStationID
	}
	if v.Has("siteId") && isBlank(v.Get("siteId")) {
		return usecases.ListHintsQuery{}, policy.ErrInvalidSiteID
	}
	if v.Has("state") && v.Get("state") == "" {
		return usecases.ListHintsQuery{}, usecases.ErrInvalidState
	}
	return usecases.ListHintsQuery{
		StationID: v.Get("stationId"), SiteID: v.Get("siteId"), State: v.Get("state"), Cursor: v.Get("cursor"),
	}, nil
}

// handleGetHint backs GET /hints/{hintId}.
func (s *Server) handleGetHint(w http.ResponseWriter, r *http.Request) {
	id, err := pathParam(r, "hintId", stationtrip.ErrInvalidHintID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	rec, err := s.GetHint.Handle(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(rec.Version))
	writeJSON(w, http.StatusOK, toHint(rec))
}

// handleGetStation backs GET /stations/{stationId}. A trip that never recorded
// a completion has no position to report and is as unknown as a missing one.
func (s *Server) handleGetStation(w http.ResponseWriter, r *http.Request) {
	id, err := pathParam(r, "stationId", stationtrip.ErrInvalidStationID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	trip, err := s.GetStation.Handle(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	pos, ok := trip.Position()
	if !ok {
		writeError(w, r, repository.ErrStationNotFound)
		return
	}
	w.Header().Set("ETag", etag(trip.Version()))
	writeJSON(w, http.StatusOK, toStation(trip, pos))
}
