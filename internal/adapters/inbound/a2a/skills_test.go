package a2a_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	inbounda2a "github.com/claudioed/task-interleaving/internal/adapters/inbound/a2a"
	"github.com/claudioed/task-interleaving/internal/adapters/outbound/memory"
	"github.com/claudioed/task-interleaving/internal/application/ports"
	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/application/usecases"
	"github.com/claudioed/task-interleaving/internal/domain/stationtrip"
)

var now = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

type fixedClock struct{}

func (fixedClock) Now() time.Time { return now }

// seqIDs mints hint-test-001, hint-test-002, ...
type seqIDs struct{ n atomic.Int32 }

func (s *seqIDs) NewHintID() string { return fmt.Sprintf("hint-test-%03d", s.n.Add(1)) }

// harness is the real A2A executor over in-memory repositories, wired exactly
// as cmd/a2a wires them (minus Postgres). The write use cases exist ONLY here,
// to seed data the way the REST adapter and the consumers would: the A2A
// surface itself holds none.
type harness struct {
	deps      inbounda2a.Deps
	pool      *memory.TaskPool
	book      *memory.LocationBook
	declare   *usecases.DeclarePolicy
	completed *usecases.ApplyTaskCompleted
	events    *memory.EventLog
	clock     int // completions so far: each is one second later, so none is stale
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	policies, trips := memory.NewPolicyRepo(), memory.NewStationTripRepo()
	pool, book := memory.NewTaskPool(), memory.NewLocationBook()
	events, processed := memory.NewEventLog(), memory.NewProcessedEventRepo()
	uow := memory.NewUnitOfWork(policies, trips, pool, book, events, processed)
	intake := usecases.Intake{UoW: uow, Processed: processed}
	writer := usecases.TripWriter{Trips: trips, Events: events}
	return &harness{
		deps: inbounda2a.Deps{
			GetPolicy:  &usecases.GetPolicy{Policies: policies},
			ListHints:  &usecases.ListHints{Trips: trips},
			GetStation: &usecases.GetStation{Trips: trips},
		},
		pool: pool, book: book, events: events,
		declare: &usecases.DeclarePolicy{Policies: policies, Events: events, UoW: uow, Clock: fixedClock{}},
		completed: &usecases.ApplyTaskCompleted{
			Intake: intake, TripWriter: writer, Policies: policies, Pool: pool,
			Ranker: usecases.Ranker{Pool: pool, Book: book}, Clock: fixedClock{}, IDs: &seqIDs{},
		},
	}
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
	at := now.Add(time.Duration(h.clock) * time.Second)
	h.clock++
	out, err := h.completed.Handle(ctx, "evt-"+task, usecases.TaskCompletion{TaskID: task, At: at})
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

// ---- the wire: JSON-RPC over HTTP, driven by the SDK's own client -----------

// wireEnvelope is a TEST-LOCAL copy of the answer contract. If the adapter's
// envelope drifts, this suite fails instead of silently following it.
type wireEnvelope struct {
	Skill string `json:"skill"`
	Data  struct {
		StationHintStatus *struct {
			StationID string `json:"stationId"`
			SiteID    string `json:"siteId"`
			Position  struct {
				Location string `json:"location"`
				Point    *struct {
					XM float64 `json:"xM"`
					YM float64 `json:"yM"`
					ZM float64 `json:"zM"`
				} `json:"point"`
			} `json:"position"`
			LastCompletedTaskID string `json:"lastCompletedTaskId"`
			LastCompletedAt     string `json:"lastCompletedAt"`
			ActiveHint          *struct {
				HintID        string `json:"hintId"`
				State         string `json:"state"`
				ExpiresAt     string `json:"expiresAt"`
				PolicyVersion int    `json:"policyVersion"`
				Candidates    []struct {
					Rank     int     `json:"rank"`
					TaskID   string  `json:"taskId"`
					TaskType string  `json:"taskType"`
					Location string  `json:"location"`
					DetourM  float64 `json:"detourM"`
				} `json:"candidates"`
			} `json:"activeHint"`
		} `json:"stationHintStatus"`
		SiteInterleavingOverview *struct {
			Policy struct {
				SiteID            string   `json:"siteId"`
				MaxDetourM        float64  `json:"maxDetourM"`
				MaxCandidates     int      `json:"maxCandidates"`
				HintTTLSeconds    int      `json:"hintTtlSeconds"`
				EligibleTaskTypes []string `json:"eligibleTaskTypes"`
				VerticalWeight    float64  `json:"verticalWeight"`
				Version           int      `json:"version"`
				DeclaredAt        string   `json:"declaredAt"`
			} `json:"policy"`
			RecentHints []struct {
				HintID       string `json:"hintId"`
				StationID    string `json:"stationId"`
				State        string `json:"state"`
				SupersededAt string `json:"supersededAt"`
			} `json:"recentHints"`
			HintsByState map[string]int `json:"hintsByState"`
			HasMore      bool           `json:"hasMore"`
		} `json:"siteInterleavingOverview"`
	} `json:"data"`
	Problem *struct {
		Type   string `json:"type"`
		Title  string `json:"title"`
		Detail string `json:"detail"`
		Status int    `json:"status"`
	} `json:"problem"`
}

const problemBase = "https://errors.task-interleaving.warehouse-systems.dev/"

type wire struct{ client *a2aclient.Client }

func newWire(t *testing.T, deps inbounda2a.Deps) *wire {
	t.Helper()
	srv := httptest.NewServer(inbounda2a.Handler(inbounda2a.NewExecutor(deps)))
	t.Cleanup(srv.Close)
	card := inbounda2a.Card(inbounda2a.Config{AgentURL: srv.URL + "/", Version: "test"})
	client, err := a2aclient.NewFromCard(context.Background(), card)
	if err != nil {
		t.Fatalf("NewFromCard: %v", err)
	}
	return &wire{client: client}
}

// send sends one message and returns the raw reply message.
func (w *wire) send(t *testing.T, msg *a2a.Message) *a2a.Message {
	t.Helper()
	resp, err := w.client.SendMessage(context.Background(), &a2a.SendMessageRequest{Message: msg})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	reply, ok := resp.(*a2a.Message)
	if !ok || len(reply.Parts) < 2 {
		t.Fatalf("reply = %T, want a message with a data and a text part", resp)
	}
	return reply
}

// call sends one message part and returns the decoded envelope and the
// one-line text summary of the agent's reply.
func (w *wire) call(t *testing.T, part *a2a.Part) (wireEnvelope, string) {
	t.Helper()
	return decodeReply(t, w.send(t, a2a.NewMessage(a2a.MessageRoleUser, part)))
}

func decodeReply(t *testing.T, msg *a2a.Message) (wireEnvelope, string) {
	t.Helper()
	data, ok := msg.Parts[0].Content.(a2a.Data)
	if !ok {
		t.Fatalf("first reply part is %T, want data", msg.Parts[0].Content)
	}
	raw, _ := json.Marshal(data.Value)
	var env wireEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("reply is not the skill envelope: %v (%s)", err, raw)
	}
	text, _ := msg.Parts[1].Content.(a2a.Text)
	return env, string(text)
}

func (w *wire) skill(t *testing.T, skill string, args map[string]any) (wireEnvelope, string) {
	t.Helper()
	return w.call(t, a2a.NewDataPart(map[string]any{"skill": skill, "arguments": args}))
}

func wantProblem(t *testing.T, env wireEnvelope, status int, slug string) {
	t.Helper()
	if env.Problem == nil {
		t.Fatalf("expected a %d %s problem, got data: %+v", status, slug, env.Data)
	}
	if env.Problem.Status != status || env.Problem.Type != problemBase+slug {
		t.Fatalf("problem = %+v, want %d %s", env.Problem, status, slug)
	}
}

// stationFacts flattens the station answer to one comparable line.
func stationFacts(s *struct {
	StationID string `json:"stationId"`
	SiteID    string `json:"siteId"`
	Position  struct {
		Location string `json:"location"`
		Point    *struct {
			XM float64 `json:"xM"`
			YM float64 `json:"yM"`
			ZM float64 `json:"zM"`
		} `json:"point"`
	} `json:"position"`
	LastCompletedTaskID string `json:"lastCompletedTaskId"`
	LastCompletedAt     string `json:"lastCompletedAt"`
	ActiveHint          *struct {
		HintID        string `json:"hintId"`
		State         string `json:"state"`
		ExpiresAt     string `json:"expiresAt"`
		PolicyVersion int    `json:"policyVersion"`
		Candidates    []struct {
			Rank     int     `json:"rank"`
			TaskID   string  `json:"taskId"`
			TaskType string  `json:"taskType"`
			Location string  `json:"location"`
			DetourM  float64 `json:"detourM"`
		} `json:"candidates"`
	} `json:"activeHint"`
}) []string {
	point := "no point"
	if p := s.Position.Point; p != nil {
		point = fmt.Sprintf("(%g,%g,%g)", p.XM, p.YM, p.ZM)
	}
	facts := []string{
		s.StationID, s.SiteID, s.Position.Location, point, s.LastCompletedTaskID, s.LastCompletedAt,
	}
	if h := s.ActiveHint; h != nil {
		facts = append(facts, h.HintID, h.State, h.ExpiresAt, fmt.Sprint(h.PolicyVersion))
		for _, c := range h.Candidates {
			facts = append(facts, fmt.Sprintf("%d/%s/%s/%s/%g", c.Rank, c.TaskID, c.TaskType, c.Location, c.DetourM))
		}
	}
	return facts
}

func TestWireStationHintStatusReadsPositionAndActiveHint(t *testing.T) {
	w := newWire(t, withHint(t).deps)

	env, text := w.skill(t, "station_hint_status", map[string]any{"stationId": "STN-07"})
	s := env.Data.StationHintStatus
	if env.Problem != nil || env.Skill != "station_hint_status" || s == nil {
		t.Fatalf("envelope = %+v", env)
	}
	want := "STN-07 SITE-1 A-01 (0,0,0) task-8a1f 2026-10-17T12:00:00Z " +
		"hint-test-001 Active 2026-10-17T12:01:30Z 1 " +
		"1/task-9c2e/PICK/B-01/4 2/task-9d10/REPLENISH/B-02/10"
	if got := strings.Join(stationFacts(s), " "); got != want {
		t.Fatalf("station facts =\n  %s\nwant\n  %s", got, want)
	}
	const wantText = "STN-07 (SITE-1) at A-01: active hint hint-test-001 (expires 2026-10-17T12:01:30Z) ranks task-9c2e first, 4 m detour (2 candidates)"
	if text != wantText {
		t.Fatalf("summary = %q, want %q", text, wantText)
	}
}

func TestWireStationWithoutAHintIsStillAnswered(t *testing.T) {
	h := newHarness(t)
	h.seedWorld(t)
	h.complete(t, "STN-07", "task-8a1f") // no policy: inert, position recorded, no hint
	w := newWire(t, h.deps)

	env, text := w.skill(t, "station_hint_status", map[string]any{"stationId": "STN-07"})
	if s := env.Data.StationHintStatus; env.Problem != nil || s == nil || s.ActiveHint != nil {
		t.Fatalf("envelope = %+v, want a station without an active hint", env)
	}
	if text != "STN-07 (SITE-1) at A-01: no active hint" {
		t.Fatalf("summary = %q", text)
	}
}

func TestWireSiteInterleavingOverviewComposesPolicyAndRecentHints(t *testing.T) {
	h := withHint(t)
	h.complete(t, "STN-07", "task-8a20") // a NEW hint supersedes hint-test-001
	h.complete(t, "STN-08", "task-8a21")
	w := newWire(t, h.deps)

	env, text := w.skill(t, "site_interleaving_overview", map[string]any{"siteId": "SITE-1"})
	o := env.Data.SiteInterleavingOverview
	if env.Problem != nil || o == nil {
		t.Fatalf("envelope = %+v", env)
	}
	p := o.Policy
	gotPolicy := fmt.Sprintf("%s %g %d %d %v %g v%d %s", p.SiteID, p.MaxDetourM, p.MaxCandidates, p.HintTTLSeconds,
		p.EligibleTaskTypes, p.VerticalWeight, p.Version, p.DeclaredAt)
	if want := "SITE-1 25 3 90 [PICK REPLENISH] 1.5 v1 2026-10-17T12:00:00Z"; gotPolicy != want {
		t.Fatalf("policy = %q, want %q", gotPolicy, want)
	}
	if len(o.RecentHints) != 3 || o.HasMore || o.HintsByState["Active"] != 2 || o.HintsByState["Superseded"] != 1 {
		t.Fatalf("recent hints = %+v by state %v", o.RecentHints, o.HintsByState)
	}
	const want = "SITE-1 policy v1: max detour 25 m, up to 3 candidates, hint TTL 90 s; last 3 hints: 2 Active, 1 Superseded"
	if text != want {
		t.Fatalf("summary = %q, want %q", text, want)
	}

	env, _ = w.skill(t, "site_interleaving_overview", map[string]any{"siteId": "SITE-1", "limit": 2})
	if o := env.Data.SiteInterleavingOverview; o == nil || len(o.RecentHints) != 2 || !o.HasMore {
		t.Fatalf("limit 2 = %+v, want a 2-hint window with hasMore", env)
	}
}

func TestWireOverviewOfADeclaredSiteWithoutHints(t *testing.T) {
	h := newHarness(t)
	h.declarePolicy(t)
	env, text := newWire(t, h.deps).skill(t, "site_interleaving_overview", map[string]any{"siteId": "SITE-1"})
	if o := env.Data.SiteInterleavingOverview; env.Problem != nil || o == nil || len(o.RecentHints) != 0 {
		t.Fatalf("envelope = %+v", env)
	}
	if !strings.HasSuffix(text, "; no hints issued yet") {
		t.Fatalf("summary = %q", text)
	}
}

// Domain rejections are ANSWERED in-band with the status and slug of the REST
// adapter, never raised as JSON-RPC errors.
func TestWireDomainRejectionsAreAnsweredNotRaised(t *testing.T) {
	w := newWire(t, newHarness(t).deps)

	env, _ := w.skill(t, "station_hint_status", map[string]any{"stationId": "STN-99"})
	wantProblem(t, env, 404, "station-not-found")
	env, _ = w.skill(t, "station_hint_status", map[string]any{"stationId": "a/b"})
	wantProblem(t, env, 400, "invalid-station-id")
	env, _ = w.skill(t, "site_interleaving_overview", map[string]any{"siteId": "SITE-9"})
	wantProblem(t, env, 404, "policy-not-found")
	env, _ = w.skill(t, "site_interleaving_overview", map[string]any{"siteId": "a/b"})
	wantProblem(t, env, 400, "invalid-site-id")
}

func TestWireArgumentValidation(t *testing.T) {
	w := newWire(t, withHint(t).deps)
	cases := map[string]struct {
		skill string
		args  map[string]any
		slug  string
	}{
		"station: missing stationId":      {"station_hint_status", map[string]any{}, "invalid-argument"},
		"station: blank stationId":        {"station_hint_status", map[string]any{"stationId": "   "}, "invalid-argument"},
		"station: unknown argument":       {"station_hint_status", map[string]any{"stationId": "STN-07", "siteId": "SITE-1"}, "invalid-argument"},
		"station: typo'd name":            {"station_hint_status", map[string]any{"stationID": "STN-07", "stationid2": "x"}, "invalid-argument"},
		"station: wrong type":             {"station_hint_status", map[string]any{"stationId": 7}, "invalid-argument"},
		"overview: missing siteId":        {"site_interleaving_overview", map[string]any{}, "invalid-argument"},
		"overview: unknown argument":      {"site_interleaving_overview", map[string]any{"siteId": "SITE-1", "lmit": 3}, "invalid-argument"},
		"overview: cursor is not offered": {"site_interleaving_overview", map[string]any{"siteId": "SITE-1", "cursor": "x"}, "invalid-argument"},
		"overview: limit zero":            {"site_interleaving_overview", map[string]any{"siteId": "SITE-1", "limit": 0}, "invalid-limit"},
		"overview: limit negative":        {"site_interleaving_overview", map[string]any{"siteId": "SITE-1", "limit": -1}, "invalid-limit"},
		"overview: limit above window":    {"site_interleaving_overview", map[string]any{"siteId": "SITE-1", "limit": 21}, "invalid-limit"},
		"overview: limit not an integer":  {"site_interleaving_overview", map[string]any{"siteId": "SITE-1", "limit": 2.5}, "invalid-argument"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			env, text := w.skill(t, tc.skill, tc.args)
			wantProblem(t, env, 400, tc.slug)
			if text == "" || env.Problem.Detail == "" {
				t.Fatalf("a problem answers with a text summary and a detail: %q / %+v", text, env.Problem)
			}
		})
	}
}

func TestWireRequestShapeProblems(t *testing.T) {
	w := newWire(t, withHint(t).deps)

	for name, part := range map[string]*a2a.Part{
		"text that is not JSON":     a2a.NewTextPart("not json at all"),
		"no skill field":            a2a.NewDataPart(map[string]any{"arguments": map[string]any{}}),
		"unknown envelope field":    a2a.NewDataPart(map[string]any{"skill": "station_hint_status", "arguments": map[string]any{"stationId": "STN-07"}, "extra": 1}),
		"trailing data after JSON":  a2a.NewTextPart(`{"skill":"station_hint_status","arguments":{"stationId":"STN-07"}} {"x":1}`),
		"raw part is not a request": a2a.NewRawPart([]byte("x")),
	} {
		t.Run(name, func(t *testing.T) {
			env, _ := w.call(t, part)
			wantProblem(t, env, 400, "malformed-skill-request")
		})
	}

	env, _ := w.skill(t, "launch_rocket", map[string]any{})
	wantProblem(t, env, 400, "unknown-skill")
	// The write verbs the MCP surface lacks are unknown skills here too.
	for _, skill := range []string{"declare_policy", "recommend_next_task", "get_hint", "list_hints"} {
		env, _ = w.skill(t, skill, map[string]any{"siteId": "SITE-1"})
		wantProblem(t, env, 400, "unknown-skill")
	}

	// A text part holding the same JSON is accepted (documented contract).
	env, _ = w.call(t, a2a.NewTextPart(`{"skill":"station_hint_status","arguments":{"stationId":"STN-07"}}`))
	if env.Problem != nil || env.Data.StationHintStatus == nil {
		t.Fatalf("a text part holding the skill JSON must be accepted, got %+v", env)
	}
	// Absent arguments reach the skill's own required-field check.
	env, _ = w.call(t, a2a.NewDataPart(map[string]any{"skill": "station_hint_status"}))
	wantProblem(t, env, 400, "invalid-argument")
}

// brokenTrips is a station-trip repository whose reads fail the way a lost
// database does, with an error text that must never reach the caller.
type brokenTrips struct{ ports.StationTripRepo }

var errStorage = errors.New("pq: connection refused to postgres://app:s3cret@db:5432/ti")

func (brokenTrips) Get(context.Context, stationtrip.StationID) (*stationtrip.StationTrip, error) {
	return nil, fmt.Errorf("get trip: %w", errStorage)
}

func (brokenTrips) ListHints(context.Context, repository.HintFilter, stationtrip.HintID, int) ([]repository.HintRecord, error) {
	return nil, fmt.Errorf("list hints: %w", errStorage)
}

// An unclassified failure is answered as a 500 problem whose text names no
// storage detail (same 500 the REST adapter would send).
func TestWireInternalFailuresAreAnsweredAs500WithoutLeaks(t *testing.T) {
	h := newHarness(t)
	h.declarePolicy(t)
	h.deps.GetStation = &usecases.GetStation{Trips: brokenTrips{}}
	h.deps.ListHints = &usecases.ListHints{Trips: brokenTrips{}}
	w := newWire(t, h.deps)

	for skill, args := range map[string]map[string]any{
		"station_hint_status":        {"stationId": "STN-07"},
		"site_interleaving_overview": {"siteId": "SITE-1"},
	} {
		env, text := w.skill(t, skill, args)
		wantProblem(t, env, 500, "internal-error")
		for _, s := range []string{env.Problem.Detail, env.Problem.Title, text} {
			if strings.Contains(s, "s3cret") || strings.Contains(s, "postgres://") || strings.Contains(s, "connection refused") {
				t.Fatalf("%s leaked a storage detail: %q", skill, s)
			}
		}
	}
}

// Problem answers carry adapter-owned text only: no domain package prefix.
func TestWireProblemsAreAdapterOwnedText(t *testing.T) {
	w := newWire(t, newHarness(t).deps)
	for skill, args := range map[string]map[string]any{
		"station_hint_status":        {"stationId": "a/b"},
		"site_interleaving_overview": {"siteId": "a/b"},
	} {
		env, text := w.skill(t, skill, args)
		for _, s := range []string{env.Problem.Detail, env.Problem.Title, text} {
			for _, bad := range []string{"stationtrip:", "policy:", "usecases:", "repository:", "buildingId"} {
				if strings.Contains(s, bad) {
					t.Fatalf("%s answer %q carries domain text %q", skill, s, bad)
				}
			}
		}
	}
}

// The surface is read-only: skills emit no event and write no outbox row.
func TestWireSkillsEmitNothing(t *testing.T) {
	h := withHint(t)
	w := newWire(t, h.deps)
	before := len(h.events.Names())
	w.skill(t, "station_hint_status", map[string]any{"stationId": "STN-07"})
	w.skill(t, "site_interleaving_overview", map[string]any{"siteId": "SITE-1"})
	w.skill(t, "station_hint_status", map[string]any{"stationId": "STN-99"})
	if after := len(h.events.Names()); after != before {
		t.Fatalf("events %d -> %d: a read skill emitted an event", before, after)
	}
}

// The reply is bound to the caller's context so a delegating saga can group
// its interactions.
func TestWireReplyCarriesTheCallersContext(t *testing.T) {
	w := newWire(t, withHint(t).deps)
	msg := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewDataPart(map[string]any{"skill": "station_hint_status", "arguments": map[string]any{"stationId": "STN-07"}}))
	msg.ContextID = "ctx-saga-42"
	reply := w.send(t, msg)
	if reply.ContextID != "ctx-saga-42" {
		t.Fatalf("reply contextId = %q, want the caller's ctx-saga-42", reply.ContextID)
	}
}

// TestCardIsServedAndDialable proves discovery end to end: the card handler
// serves the card at the well-known path and a client built from the FETCHED
// card reaches the agent.
func TestCardIsServedAndDialable(t *testing.T) {
	h := withHint(t)
	mux := http.NewServeMux()
	mux.Handle("/", inbounda2a.Handler(inbounda2a.NewExecutor(h.deps)))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(inbounda2a.Card(inbounda2a.Config{AgentURL: srv.URL + "/", Version: "test"})))

	res, err := http.Get(srv.URL + a2asrv.WellKnownAgentCardPath)
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("card fetch = %v / %v", res, err)
	}
	defer func() { _ = res.Body.Close() }()
	var card a2a.AgentCard
	if err := json.NewDecoder(res.Body).Decode(&card); err != nil {
		t.Fatal(err)
	}
	client, err := a2aclient.NewFromCard(context.Background(), &card)
	if err != nil {
		t.Fatal(err)
	}
	w := &wire{client: client}
	env, _ := w.skill(t, "station_hint_status", map[string]any{"stationId": "STN-07"})
	if env.Data.StationHintStatus == nil {
		t.Fatalf("a client built from the served card could not call the agent: %+v", env)
	}
}
