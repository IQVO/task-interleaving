package mcp_test

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	inboundmcp "github.com/claudioed/task-interleaving/internal/adapters/inbound/mcp"
	"github.com/claudioed/task-interleaving/internal/adapters/outbound/memory"
	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/application/usecases"
)

// now is the fixture's fixed "now".
var now = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

type fixedClock struct{}

func (fixedClock) Now() time.Time { return now }

// seqIDs mints hint-test-001, hint-test-002, ...
type seqIDs struct{ n atomic.Int32 }

func (s *seqIDs) NewHintID() string { return fmt.Sprintf("hint-test-%03d", s.n.Add(1)) }

// harness is a connected in-memory MCP client over the real server wired to
// in-memory repositories, exactly as cmd/mcp wires them (minus Postgres). The
// write use cases exist ONLY here, to seed data the way the REST adapter and
// the consumers would: the MCP surface itself has none.
type harness struct {
	session   *sdk.ClientSession
	pool      *memory.TaskPool
	book      *memory.LocationBook
	declare   *usecases.DeclarePolicy
	completed *usecases.ApplyTaskCompleted
	events    *memory.EventLog
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	policies, trips := memory.NewPolicyRepo(), memory.NewStationTripRepo()
	pool, book := memory.NewTaskPool(), memory.NewLocationBook()
	events, processed := memory.NewEventLog(), memory.NewProcessedEventRepo()
	uow := memory.NewUnitOfWork(policies, trips, pool, book, events, processed)
	intake := usecases.Intake{UoW: uow, Processed: processed}
	writer := usecases.TripWriter{Trips: trips, Events: events}
	deps := inboundmcp.Deps{
		GetPolicy:  &usecases.GetPolicy{Policies: policies},
		ListHints:  &usecases.ListHints{Trips: trips},
		GetHint:    &usecases.GetHint{Trips: trips},
		GetStation: &usecases.GetStation{Trips: trips},
	}
	return &harness{
		session: connectSession(t, deps), pool: pool, book: book, events: events,
		declare: &usecases.DeclarePolicy{Policies: policies, Events: events, UoW: uow, Clock: fixedClock{}},
		completed: &usecases.ApplyTaskCompleted{
			Intake: intake, TripWriter: writer, Policies: policies, Pool: pool,
			Ranker: usecases.Ranker{Pool: pool, Book: book}, Clock: fixedClock{}, IDs: &seqIDs{},
		},
	}
}

func connectSession(t *testing.T, deps inboundmcp.Deps) *sdk.ClientSession {
	t.Helper()
	ctx := context.Background()
	clientTransport, serverTransport := sdk.NewInMemoryTransports()
	serverSession, err := inboundmcp.NewServer(deps).Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// seedWorld gives SITE-1 a layout and an open-task pool: from A-01 (0,0,0) the
// PICK at B-01 is 4 m away and the REPLENISH at B-02 10 m.
func (h *harness) seedWorld(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	must(t, h.book.SaveZone(ctx, repository.Zone{ID: "z1", SiteCode: "SITE-1", ZoneCode: "FWD"}))
	for code, x := range map[string]float64{"A-01": 0, "B-01": 4, "B-02": 10} {
		must(t, h.book.SaveLocation(ctx, code, "z1"))
		must(t, h.book.SaveGeometry(ctx, code, x, 0, 0))
	}
	for _, task := range []repository.Task{
		{ID: "task-9c2e", Type: "PICK", FromLocation: "B-01", ToLocation: "B-01"},
		{ID: "task-9d10", Type: "REPLENISH", FromLocation: "B-02", ToLocation: "B-02"},
	} {
		task.SiteID, task.State = "SITE-1", repository.TaskAvailable
		must(t, h.pool.Save(ctx, task))
	}
}

// declarePolicy declares SITE-1's policy (version 1) through the write use case.
func (h *harness) declarePolicy(t *testing.T) {
	t.Helper()
	weight := 1.5
	_, err := h.declare.Handle(t.Context(), usecases.DeclarePolicyInput{
		SiteID: "SITE-1", MaxDetourM: 25, MaxCandidates: 3, HintTTL: 90 * time.Second,
		EligibleTaskTypes: []string{"PICK", "REPLENISH"}, VerticalWeight: &weight,
	})
	must(t, err)
}

// complete records a completed task of the station at A-01, which issues a
// hint when the site declared a policy and candidates exist.
func (h *harness) complete(t *testing.T, station, task string) {
	t.Helper()
	ctx := t.Context()
	must(t, h.pool.Save(ctx, repository.Task{
		ID: task, Type: "PICK", SiteID: "SITE-1", FromLocation: "A-01", ToLocation: "A-01",
		State: repository.TaskClaimed, ClaimedBy: station,
	}))
	out, err := h.completed.Handle(ctx, "evt-"+task, usecases.TaskCompletion{TaskID: task, At: now})
	must(t, err)
	if out != usecases.OutcomeApplied {
		t.Fatalf("completion of %s = %s", task, out)
	}
}

// withHint gives STN-07 the Active hint hint-test-001.
func withHint(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.seedWorld(t)
	h.declarePolicy(t)
	h.complete(t, "STN-07", "task-8a1f")
	return h
}

// call invokes a tool and fails the test on a TRANSPORT/protocol error;
// tool-level failures come back as res.IsError.
func call(t *testing.T, session *sdk.ClientSession, name string, args map[string]any) *sdk.CallToolResult {
	t.Helper()
	res, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: transport/protocol error (want a tool result): %v", name, err)
	}
	return res
}

// ok calls a tool, requires success, and returns its structured content.
func (h *harness) ok(t *testing.T, name string, args map[string]any) map[string]any {
	t.Helper()
	res := call(t, h.session, name, args)
	if res.IsError {
		t.Fatalf("%s returned a tool error: %s", name, text(res))
	}
	sc, isMap := res.StructuredContent.(map[string]any)
	if !isMap {
		t.Fatalf("%s: structured content = %#v, want an object", name, res.StructuredContent)
	}
	return sc
}

// failWith calls a tool and requires an isError result whose text starts
// with the slug want.
func failWith(t *testing.T, session *sdk.ClientSession, name string, args map[string]any, want string) {
	t.Helper()
	res := call(t, session, name, args)
	if !res.IsError {
		t.Fatalf("%s: expected an isError tool result, got %#v", name, res.StructuredContent)
	}
	if got := text(res); !strings.HasPrefix(got, want+": ") {
		t.Fatalf("%s: error text %q does not start with %q", name, got, want+": ")
	}
}

func text(res *sdk.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, isText := c.(*sdk.TextContent); isText {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}
