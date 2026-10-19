//go:build integration

package main

import (
	"context"
	"os"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	inboundmcp "github.com/claudioed/task-interleaving/internal/adapters/inbound/mcp"
	"github.com/claudioed/task-interleaving/internal/adapters/outbound/events"
	"github.com/claudioed/task-interleaving/internal/adapters/outbound/postgres"
	"github.com/claudioed/task-interleaving/internal/adapters/outbound/system"
	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/application/usecases"
	"github.com/claudioed/task-interleaving/internal/testing/pgtest"
)

func TestMain(m *testing.M) {
	code := m.Run()
	pgtest.Shutdown()
	os.Exit(code)
}

// The binary migrates a database itself (idempotently: pgtest's databases are
// already migrated, a second run must be a no-op), then reads through MCP tools
// exactly what the write use cases persisted, and its reads add no outbox row.
func TestMCPAgainstPostgres_ReadsWhatTheAPIWroteAndNeverWrites(t *testing.T) {
	url := pgtest.URL(t) // a fresh, already migrated database
	ctx := context.Background()

	pool, err := postgres.NewPool(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	countOutbox := func() int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events`).Scan(&n); err != nil {
			t.Fatalf("count outbox_events: %v", err)
		}
		return n
	}

	seedThroughWriteUseCases(t, url)
	before := countOutbox()
	if before < 2 {
		t.Fatalf("outbox rows after seeding = %d, want the policy declaration and the hint", before)
	}

	deps, closeFn, err := buildDeps(ctx, quietLogger(), url, url)
	if err != nil {
		t.Fatalf("buildDeps: %v", err)
	}
	t.Cleanup(closeFn)

	ct, st := sdk.NewInMemoryTransports()
	ss, err := inboundmcp.NewServer(deps).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	session, err := sdk.NewClient(&sdk.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	assertSeededReads(t, session)

	if after := countOutbox(); after != before {
		t.Fatalf("outbox rows %d -> %d: the MCP binary wrote an event", before, after)
	}
}

type fixedIDs struct{}

func (fixedIDs) NewHintID() string { return "hint-it-001" }

// seedThroughWriteUseCases declares SITE-1's policy and completes a task so a
// hint is issued, the way cmd/api does (same use cases, Postgres adapters,
// transactional outbox).
func seedThroughWriteUseCases(t *testing.T, url string) {
	t.Helper()
	ctx := context.Background()
	pool, err := postgres.NewPool(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	policies, trips := postgres.NewPolicyRepo(pool), postgres.NewStationTripRepo(pool)
	taskPool, book := postgres.NewTaskPool(pool), postgres.NewLocationBook(pool)
	sink, uow := events.NewSink(postgres.NewOutboxRepo(pool)), postgres.NewUnitOfWork(pool)
	processed := postgres.NewProcessedEventRepo(pool)

	if err := book.SaveZone(ctx, repository.Zone{ID: "z1", SiteCode: "SITE-1", ZoneCode: "FWD"}); err != nil {
		t.Fatal(err)
	}
	for code, x := range map[string]float64{"A-01": 0, "B-01": 4} {
		if err := book.SaveLocation(ctx, code, "z1"); err != nil {
			t.Fatal(err)
		}
		if err := book.SaveGeometry(ctx, code, x, 0, 0); err != nil {
			t.Fatal(err)
		}
	}
	tasks := []repository.Task{
		{ID: "task-9c2e", Type: "PICK", SiteID: "SITE-1", FromLocation: "B-01", ToLocation: "B-01", State: repository.TaskAvailable},
		{ID: "task-8a1f", Type: "PICK", SiteID: "SITE-1", FromLocation: "A-01", ToLocation: "A-01", State: repository.TaskClaimed, ClaimedBy: "STN-07"},
	}
	for _, task := range tasks {
		if err := taskPool.Save(ctx, task); err != nil {
			t.Fatal(err)
		}
	}

	weight := 1.5
	if _, err := (&usecases.DeclarePolicy{Policies: policies, Events: sink, UoW: uow, Clock: system.Clock{}}).Handle(ctx, usecases.DeclarePolicyInput{
		SiteID: "SITE-1", MaxDetourM: 25, MaxCandidates: 3, HintTTL: time.Hour,
		EligibleTaskTypes: []string{"PICK"}, VerticalWeight: &weight,
	}); err != nil {
		t.Fatalf("declare policy: %v", err)
	}
	out, err := (&usecases.ApplyTaskCompleted{
		Intake:     usecases.Intake{UoW: uow, Processed: processed},
		TripWriter: usecases.TripWriter{Trips: trips, Events: sink},
		Policies:   policies, Pool: taskPool, Ranker: usecases.Ranker{Pool: taskPool, Book: book},
		Clock: system.Clock{}, IDs: fixedIDs{},
	}).Handle(ctx, "evt-it-1", usecases.TaskCompletion{TaskID: "task-8a1f", At: time.Now()})
	if err != nil || out != usecases.OutcomeApplied {
		t.Fatalf("complete task = %v, %v", out, err)
	}
}

// assertSeededReads reads back, through every MCP tool, what
// seedThroughWriteUseCases persisted.
func assertSeededReads(t *testing.T, session *sdk.ClientSession) {
	t.Helper()
	ctx := context.Background()
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		res, err := session.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
		if err != nil || res.IsError {
			t.Fatalf("%s: err=%v res=%+v", name, err, res)
		}
		return res.StructuredContent.(map[string]any)
	}

	pol := call("get_site_policy", map[string]any{"siteId": "SITE-1"})
	if pol["siteId"] != "SITE-1" || pol["maxDetourM"] != 25.0 || pol["version"] != 1.0 || pol["hintTtlSeconds"] != 3600.0 {
		t.Fatalf("get_site_policy = %v", pol)
	}
	hint := call("get_hint", map[string]any{"hintId": "hint-it-001"})
	cands := hint["candidates"].([]any)
	if hint["stationId"] != "STN-07" || hint["state"] != "Active" || len(cands) != 1 || cands[0].(map[string]any)["taskId"] != "task-9c2e" {
		t.Fatalf("get_hint = %v", hint)
	}
	page := call("list_hints", map[string]any{"stationId": "STN-07", "state": "Active"})
	items := page["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["hintId"] != "hint-it-001" {
		t.Fatalf("list_hints = %v", page)
	}
	station := call("get_station", map[string]any{"stationId": "STN-07"})
	active, _ := station["activeHint"].(map[string]any)
	if station["siteId"] != "SITE-1" || station["lastCompletedTaskId"] != "task-8a1f" || active["hintId"] != "hint-it-001" {
		t.Fatalf("get_station = %v", station)
	}
}
