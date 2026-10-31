// Command a2a is the composition root for the task-interleaving A2A server
// (docs/adr/0009-a2a-inbound-adapter.md): it wires env config to the outbound
// Postgres repositories, the repositories to the SAME read use cases the REST
// and MCP adapters use (GetPolicy, ListHints, GetStation), and those to the
// inbound A2A adapter, then serves the Agent2Agent JSON-RPC endpoint over HTTP
// with the Agent Card at /.well-known/agent-card.json. It is a third,
// independent deployable alongside cmd/api and cmd/mcp.
//
// The surface is READ-ONLY, like cmd/mcp (the A2A write gate: a skill may
// change state only if the MCP surface already offers that write, and it
// offers none): no write use case is wired here at all, so this binary can
// never insert into the outbox, and it has no event publisher to start a relay
// for. It never runs the outbox relay, the local-copy consumers or the
// hint-expiry worker and never dials Kafka (no KAFKA_BROKERS, no consumer
// groups): those run in cmd/api only. It makes no REST, MCP or A2A call to any
// sibling context.
//
// There is NO auth of any kind (fleet-wide revert 2026-09-11): no keys, no
// bearer checks. Access control is the in-cluster ClusterIP boundary.
//
// Environment:
//
//	A2A_ADDR                 listen address (default :8093)
//	A2A_AGENT_URL            REQUIRED. Absolute http(s) URL of the JSON-RPC endpoint, advertised on the Agent Card
//	DATABASE_URL             REQUIRED. Postgres DSN (the service database cmd/api owns)
//	MIGRATIONS_DATABASE_URL  direct DSN for the boot migrations (default DATABASE_URL)
//	LOG_LEVEL                debug | info | warn | error (default info)
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	inbounda2a "github.com/claudioed/task-interleaving/internal/adapters/inbound/a2a"
	"github.com/claudioed/task-interleaving/internal/adapters/outbound/postgres"
	"github.com/claudioed/task-interleaving/internal/application/usecases"
	"github.com/claudioed/task-interleaving/internal/bootretry"
)

// shutdownTimeout bounds the HTTP server's graceful drain.
const shutdownTimeout = 10 * time.Second

// errNoDatabase is the boot error of a missing DATABASE_URL: this binary has
// no in-memory mode.
var errNoDatabase = errors.New("DATABASE_URL is required: this context runs on real Postgres")

// agentVersion is the Agent Card version of this agent.
const agentVersion = "1.0.0"

// bootConfig is the validated environment of the binary.
type bootConfig struct {
	addr          string
	agentURL      string
	databaseURL   string
	migrationsURL string
}

// loadBootConfig validates the environment before anything is dialled or
// bound: every refusal is a boot error that names its variable.
func loadBootConfig() (bootConfig, error) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return bootConfig{}, errNoDatabase
	}
	agentURL, err := parseAgentURL(os.Getenv("A2A_AGENT_URL"))
	if err != nil {
		return bootConfig{}, err
	}
	return bootConfig{
		addr:          getenv("A2A_ADDR", ":8093"),
		agentURL:      agentURL,
		databaseURL:   databaseURL,
		migrationsURL: getenv("MIGRATIONS_DATABASE_URL", databaseURL),
	}, nil
}

// parseAgentURL requires A2A_AGENT_URL: the Agent Card advertises it, so it
// must be an absolute http(s) URL a delegating agent can dial. There is no
// default (a card pointing at localhost would silently mislead every client).
func parseAgentURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("A2A_AGENT_URL is required: the Agent Card must advertise the absolute http(s) URL of this agent's JSON-RPC endpoint")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("A2A_AGENT_URL=%q is not an absolute http(s) URL", raw)
	}
	return raw, nil
}

func main() {
	if err := run(); err != nil {
		slog.Error("a2a server exited with error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	logger := newLogger(getenv("LOG_LEVEL", "info"))
	slog.SetDefault(logger)

	cfg, err := loadBootConfig()
	if err != nil {
		return err
	}
	deps, closeAdapters, err := buildDeps(context.Background(), logger, cfg.databaseURL, cfg.migrationsURL)
	if err != nil {
		return err
	}
	defer closeAdapters()

	card := inbounda2a.Card(inbounda2a.Config{AgentURL: cfg.agentURL, Version: agentVersion})
	srv := &http.Server{
		Addr:              cfg.addr,
		Handler:           newRouter(inbounda2a.Handler(inbounda2a.NewExecutor(deps)), card),
		ReadHeaderTimeout: 5 * time.Second,
	}
	return serveA2A(logger, srv)
}

// buildDeps migrates the database, connects a pgx pool and wires the three
// read use cases over the Postgres repositories. The returned close func
// releases the pool. An empty databaseURL is a boot error: there is no
// fallback runtime.
//
// Like cmd/mcp this binary runs the idempotent embedded
// migrations on start, so it can boot against a fresh database before cmd/api
// has; golang-migrate's advisory lock makes concurrent starts safe.
// migrationsDatabaseURL is used ONLY for that step (a DIRECT Postgres DSN where
// DATABASE_URL points at PgBouncer, which cannot honour the session-scoped
// advisory lock); the runtime pool always uses databaseURL.
func buildDeps(ctx context.Context, logger *slog.Logger, databaseURL, migrationsDatabaseURL string) (inbounda2a.Deps, func(), error) {
	noop := func() {}
	if databaseURL == "" {
		return inbounda2a.Deps{}, noop, errNoDatabase
	}
	// The first outbound dial of an injected pod can be reset (Istio native
	// sidecars), so migrations and the first ping retry with backoff; on
	// exhaustion the process refuses to boot.
	if err := bootretry.Retry(ctx, logger, "run migrations", func() error {
		return postgres.RunMigrations(migrationsDatabaseURL)
	}); err != nil {
		return inbounda2a.Deps{}, noop, err
	}
	pool, err := postgres.NewPool(ctx, databaseURL)
	if err != nil {
		return inbounda2a.Deps{}, noop, err
	}
	if err := bootretry.Retry(ctx, logger, "ping postgres", func() error { return pool.Ping(ctx) }); err != nil {
		pool.Close()
		return inbounda2a.Deps{}, noop, err
	}
	logger.Info("postgres repositories configured")
	policies, trips := postgres.NewPolicyRepo(pool), postgres.NewStationTripRepo(pool)
	return inbounda2a.Deps{
		GetPolicy:  &usecases.GetPolicy{Policies: policies},
		ListHints:  &usecases.ListHints{Trips: trips},
		GetStation: &usecases.GetStation{Trips: trips},
	}, pool.Close, nil
}

// serveA2A runs srv until it fails or SIGINT/SIGTERM arrives, then drains it
// gracefully.
func serveA2A(logger *slog.Logger, srv *http.Server) error {
	serverErr := make(chan error, 1)
	go func() {
		logger.Info("a2a server listening (JSON-RPC over HTTP)", "addr", srv.Addr)
		serverErr <- srv.ListenAndServe()
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// newLogger builds the process-wide JSON logger. LOG_LEVEL maps
// debug|info|warn|warning|error (case-insensitive; "warning" is accepted
// like "warn" fleet-wide), defaulting to Info.
func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
