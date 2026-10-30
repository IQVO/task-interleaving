//go:build integration

package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"

	inbounda2a "github.com/claudioed/task-interleaving/internal/adapters/inbound/a2a"
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
// already migrated, a second run must be a no-op), then answers A2A skills over
// the real router with exactly what the write use cases persisted, and its
// reads add no outbox row: the read-only surface leaves no UNPUBLISHED rows.
func TestA2AAgainstPostgres_ReadsWhatTheAPIWroteAndNeverWrites(t *testing.T) {
	url := pgtest.URL(t) // a fresh, already migrated database
	ctx := context.Background()

	pool, err := postgres.NewPool(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	countOutbox := func() (total, unpublished int) {
		t.Helper()
		if err := pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE published_at IS NULL) FROM outbox_events`).Scan(&total, &unpublished); err != nil {
			t.Fatalf("count outbox_events: %v", err)
		}
		return total, unpublished
	}

	seedThroughWriteUseCases(t, url)
	beforeTotal, beforeUnpublished := countOutbox()
	if beforeTotal < 2 {
		t.Fatalf("outbox rows after seeding = %d, want the policy declaration and the hint", beforeTotal)
	}

	deps, closeFn, err := buildDeps(ctx, quietLogger(), url, url)
	if err != nil {
		t.Fatalf("buildDeps: %v", err)
	}
	t.Cleanup(closeFn)

	srv := httptest.NewServer(newRouter(inbounda2a.Handler(inbounda2a.NewExecutor(deps)),
		inbounda2a.Card(inbounda2a.Config{AgentURL: testAgentURL, Version: "test"})))
	t.Cleanup(srv.Close)
	client, err := a2aclient.NewFromCard(ctx, inbounda2a.Card(inbounda2a.Config{AgentURL: srv.URL + "/a2a", Version: "test"}))
	if err != nil {
		t.Fatal(err)
	}

	assertSeededReads(t, client)

	afterTotal, afterUnpublished := countOutbox()
	if afterTotal != beforeTotal || afterUnpublished != beforeUnpublished {
		t.Fatalf("outbox rows %d (%d unpublished) -> %d (%d unpublished): the A2A binary wrote an event",
			beforeTotal, beforeUnpublished, afterTotal, afterUnpublished)
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

// call sends one skill request over the JSON-RPC wire and returns the data
// object of the answer (failing the test on a problem or a transport error).
func call(t *testing.T, client *a2aclient.Client, skill string, args map[string]any) map[string]any {
	t.Helper()
	resp, err := client.SendMessage(context.Background(), &a2a.SendMessageRequest{
		Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewDataPart(map[string]any{"skill": skill, "arguments": args})),
	})
	if err != nil {
		t.Fatalf("%s: %v", skill, err)
	}
	msg, ok := resp.(*a2a.Message)
	if !ok || len(msg.Parts) < 2 {
		t.Fatalf("%s: reply = %T, want a message with a data and a text part", skill, resp)
	}
	data, ok := msg.Parts[0].Content.(a2a.Data)
	if !ok {
		t.Fatalf("%s: first reply part is %T, want data", skill, msg.Parts[0].Content)
	}
	raw, _ := json.Marshal(data.Value)
	var env struct {
		Data    map[string]any `json:"data"`
		Problem map[string]any `json:"problem"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || env.Problem != nil {
		t.Fatalf("%s: answer = %s (%v)", skill, raw, err)
	}
	return env.Data
}

// assertSeededReads reads back, through both skills, what
// seedThroughWriteUseCases persisted.
func assertSeededReads(t *testing.T, client *a2aclient.Client) {
	t.Helper()
	station, _ := call(t, client, "station_hint_status", map[string]any{"stationId": "STN-07"})["stationHintStatus"].(map[string]any)
	active, _ := station["activeHint"].(map[string]any)
	if station["siteId"] != "SITE-1" || station["lastCompletedTaskId"] != "task-8a1f" || active["hintId"] != "hint-it-001" {
		t.Fatalf("station_hint_status = %v", station)
	}
	cands, _ := active["candidates"].([]any)
	if len(cands) != 1 || cands[0].(map[string]any)["taskId"] != "task-9c2e" {
		t.Fatalf("candidates = %v", active["candidates"])
	}

	overview, _ := call(t, client, "site_interleaving_overview", map[string]any{"siteId": "SITE-1"})["siteInterleavingOverview"].(map[string]any)
	pol, _ := overview["policy"].(map[string]any)
	hints, _ := overview["recentHints"].([]any)
	if pol["siteId"] != "SITE-1" || pol["maxDetourM"] != 25.0 || pol["version"] != 1.0 || pol["hintTtlSeconds"] != 3600.0 ||
		len(hints) != 1 || hints[0].(map[string]any)["hintId"] != "hint-it-001" {
		t.Fatalf("site_interleaving_overview = %v", overview)
	}
}
