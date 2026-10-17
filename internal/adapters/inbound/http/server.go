// Package http is the REST inbound adapter: every route of apis/openapi.yaml
// on a chi router, RFC 7807 problem documents for every error, the
// Idempotency-Key middleware of the POST and the liveness/readiness
// endpoints. It has no auth middleware (fleet decision 2026-09-11;
// internal/architecture's TestNoAuthMiddlewareReintroduced).
package http

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/claudioed/task-interleaving/internal/application/usecases"
)

// Server holds the use cases the handlers call.
type Server struct {
	DeclarePolicy *usecases.DeclarePolicy
	GetPolicy     *usecases.GetPolicy
	ListHints     *usecases.ListHints
	GetHint       *usecases.GetHint
	GetStation    *usecases.GetStation
	Recommend     *usecases.Recommend

	// Idempotency wraps the POSTs (the fleet Idempotency-Key middleware,
	// Idempotency.Middleware, built by cmd/ over the Postgres adapters); nil
	// leaves the routes unwrapped, which is the in-memory development mode.
	Idempotency func(http.Handler) http.Handler
	// Readiness backs GET /readyz; nil means always ready.
	Readiness *Readiness
}

// NewRouter builds the chi router for every operation of apis/openapi.yaml
// (.claude/rules/rest-api.md) and nothing else.
func NewRouter(s *Server) http.Handler {
	r := chi.NewRouter()
	r.Use(recoverPanic)

	r.Get("/healthz", handleHealthz)
	r.Get("/readyz", s.handleReadyz)

	r.Put("/sites/{siteId}/policy", s.handleDeclarePolicy)
	r.Get("/sites/{siteId}/policy", s.handleGetPolicy)

	r.Get("/hints", s.handleListHints)
	r.Get("/hints/{hintId}", s.handleGetHint)
	r.Get("/stations/{stationId}", s.handleGetStation)

	r.Group(func(r chi.Router) {
		if s.Idempotency != nil {
			r.Use(s.Idempotency)
		}
		r.Post("/recommendations", s.handleRecommend)
	})
	return r
}
