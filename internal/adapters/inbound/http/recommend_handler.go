package http

import (
	"net/http"

	"github.com/claudioed/task-interleaving/internal/application/usecases"
	"github.com/claudioed/task-interleaving/internal/domain/stationtrip"
)

// handleRecommend backs POST /recommendations: the read-only simulation. It
// issues no hint, enqueues no event and writes nothing but the idempotency
// answer; the 200 carries no ETag because nothing is versioned.
func (s *Server) handleRecommend(w http.ResponseWriter, r *http.Request) {
	in, err := recommendInput(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	rec, err := s.Recommend.Handle(r.Context(), in)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toRecommendation(rec))
}

// recommendInput decodes and checks the POST body against the request schema:
// a blank station id is invalid-station-id, a fromLocation that is present but
// empty, blank or longer than 128 characters is a malformed request.
func recommendInput(r *http.Request) (usecases.RecommendInput, error) {
	var body recommendationBody
	if err := decodeJSON(r, &body); err != nil {
		return usecases.RecommendInput{}, err
	}
	if isBlank(body.StationID) {
		return usecases.RecommendInput{}, stationtrip.ErrInvalidStationID
	}
	in := usecases.RecommendInput{StationID: body.StationID}
	if body.FromLocation == nil {
		return in, nil
	}
	if !validLocationRef(*body.FromLocation) {
		return usecases.RecommendInput{}, errInvalidLocation
	}
	in.FromLocation = *body.FromLocation
	return in, nil
}
