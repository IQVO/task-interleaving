package memory_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/claudioed/task-interleaving/internal/adapters/outbound/memory"
	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/domain/policy"
	"github.com/claudioed/task-interleaving/internal/domain/recommend"
	"github.com/claudioed/task-interleaving/internal/domain/stationtrip"
	"github.com/claudioed/task-interleaving/internal/domain/travel"
)

var t0 = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

func testPolicy(t *testing.T) policy.Policy {
	t.Helper()
	p, err := policy.New("SITE-1", policy.Settings{
		MaxDetourM: 25, MaxCandidates: 3, HintTTL: 90 * time.Second,
		EligibleTaskTypes: []policy.TaskType{policy.TaskPick}, VerticalWeight: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// hinted builds a trip of station that completed a task and got hint id.
func hinted(t *testing.T, station, id string, at time.Time) *stationtrip.StationTrip {
	t.Helper()
	trip, err := stationtrip.New(stationtrip.StationID(station))
	if err != nil {
		t.Fatal(err)
	}
	pos := stationtrip.Position{Site: "SITE-1", Place: travel.Place{Location: "LOC-0", Point: &travel.Point{X: 1}}}
	if _, err := trip.RecordCompletion("task-0", at, pos); err != nil {
		t.Fatal(err)
	}
	return withHint(t, trip, id, at.Add(time.Second))
}

func withHint(t *testing.T, trip *stationtrip.StationTrip, id string, at time.Time) *stationtrip.StationTrip {
	t.Helper()
	cs := []recommend.Candidate{{Rank: 1, TaskID: "task-a", TaskType: policy.TaskPick, Location: "LOC-A", DetourM: 3}}
	if err := trip.IssueHint(stationtrip.HintID(id), cs, testPolicy(t), at); err != nil {
		t.Fatal(err)
	}
	return trip
}

func TestUnitOfWorkRollsBackEveryParticipant(t *testing.T) {
	ctx := context.Background()
	policies, processed := memory.NewPolicyRepo(), memory.NewProcessedEventRepo()
	uow := memory.NewUnitOfWork(policies, processed)
	boom := errors.New("boom")
	err := uow.Do(ctx, func(ctx context.Context) error {
		if err := policies.Save(ctx, repository.PolicyRecord{Policy: testPolicy(t), DeclaredAt: t0}, 0); err != nil {
			return err
		}
		if _, err := processed.Claim(ctx, "c", "e1"); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if _, err := policies.Get(ctx, "SITE-1"); !errors.Is(err, repository.ErrPolicyNotFound) {
		t.Fatalf("policy survived the rollback: %v", err)
	}
	if processed.Has("c", "e1") {
		t.Fatal("processed-event claim survived the rollback")
	}
}

func TestUnitOfWorkCommitsAndJoins(t *testing.T) {
	ctx := context.Background()
	processed := memory.NewProcessedEventRepo()
	uow := memory.NewUnitOfWork(processed)
	err := uow.Do(ctx, func(ctx context.Context) error {
		inner := uow.Do(ctx, func(ctx context.Context) error {
			_, err := processed.Claim(ctx, "c", "e1")
			return err
		})
		if inner != nil {
			return inner
		}
		return nil
	})
	if err != nil || !processed.Has("c", "e1") {
		t.Fatalf("commit failed: %v has=%v", err, processed.Has("c", "e1"))
	}
}

func TestProcessedEventClaimIsOncePerConsumer(t *testing.T) {
	ctx := context.Background()
	r := memory.NewProcessedEventRepo()
	first, _ := r.Claim(ctx, "a", "e1")
	again, _ := r.Claim(ctx, "a", "e1")
	other, _ := r.Claim(ctx, "b", "e1")
	if !first || again || !other {
		t.Fatalf("claims = %v %v %v, want true false true", first, again, other)
	}
}

func TestIdempotencyStoreFirstWriterWins(t *testing.T) {
	ctx := context.Background()
	s := memory.NewIdempotencyStore()
	if _, ok, _ := s.Lookup(ctx, "k"); ok {
		t.Fatal("empty store found a key")
	}
	_ = s.Save(ctx, repository.IdempotencyRecord{Key: "k", Status: 200})
	_ = s.Save(ctx, repository.IdempotencyRecord{Key: "k", Status: 422})
	rec, ok, _ := s.Lookup(ctx, "k")
	if !ok || rec.Status != 200 {
		t.Fatalf("got %+v %v, want the first answer", rec, ok)
	}
}

func TestPolicyRepoVersionGuard(t *testing.T) {
	ctx := context.Background()
	r := memory.NewPolicyRepo()
	p := testPolicy(t)
	if err := r.Save(ctx, repository.PolicyRecord{Policy: p, DeclaredAt: t0}, 0); err != nil {
		t.Fatal(err)
	}
	if err := r.Save(ctx, repository.PolicyRecord{Policy: p}, 0); !errors.Is(err, repository.ErrConcurrentModification) {
		t.Fatalf("second insert: %v", err)
	}
	next, _ := p.WithChanges(p.Settings())
	if err := r.Save(ctx, repository.PolicyRecord{Policy: next}, 7); !errors.Is(err, repository.ErrConcurrentModification) {
		t.Fatalf("stale update: %v", err)
	}
	if err := r.Save(ctx, repository.PolicyRecord{Policy: next, DeclaredAt: t0}, 1); err != nil {
		t.Fatal(err)
	}
	got, err := r.Get(ctx, "SITE-1")
	if err != nil || got.Policy.Version() != 2 {
		t.Fatalf("got %+v %v", got, err)
	}
	other, _ := policy.New("SITE-2", p.Settings())
	if err := r.Save(ctx, repository.PolicyRecord{Policy: other}, 1); !errors.Is(err, repository.ErrConcurrentModification) {
		t.Fatalf("update of a missing policy: %v", err)
	}
}

func TestStationTripRepoRoundTripAndVersionGuard(t *testing.T) {
	ctx := context.Background()
	r := memory.NewStationTripRepo()
	if _, err := r.Get(ctx, "STN-1"); !errors.Is(err, repository.ErrStationNotFound) {
		t.Fatalf("missing station: %v", err)
	}
	trip := hinted(t, "STN-1", "hint-1", t0)
	if err := r.Save(ctx, trip, 0); err != nil {
		t.Fatal(err)
	}
	if err := r.Save(ctx, trip, 0); !errors.Is(err, repository.ErrConcurrentModification) {
		t.Fatalf("second insert: %v", err)
	}
	if err := r.Save(ctx, trip, 9); !errors.Is(err, repository.ErrConcurrentModification) {
		t.Fatalf("stale update: %v", err)
	}
	got, err := r.Get(ctx, "STN-1")
	if err != nil || got.Version() != trip.Version() {
		t.Fatalf("got %v %v", got, err)
	}
	other := hinted(t, "STN-2", "hint-9", t0)
	if err := r.Save(ctx, other, 1); !errors.Is(err, repository.ErrConcurrentModification) {
		t.Fatalf("update of a missing station: %v", err)
	}
}

func TestStationTripRepoDoesNotShareTheLiveAggregate(t *testing.T) {
	ctx := context.Background()
	r := memory.NewStationTripRepo()
	trip := hinted(t, "STN-1", "hint-1", t0)
	if err := r.Save(ctx, trip, 0); err != nil {
		t.Fatal(err)
	}
	trip.Expire(t0.Add(time.Hour))
	got, _ := r.Get(ctx, "STN-1")
	if _, active := got.ActiveHint(); !active {
		t.Fatal("a change to the live aggregate leaked into the store")
	}
}

func saved(t *testing.T, r *memory.StationTripRepo, trip *stationtrip.StationTrip, loaded int64) {
	t.Helper()
	if err := r.Save(context.Background(), trip, loaded); err != nil {
		t.Fatal(err)
	}
}

func TestHintHistorySupersedesConsumesAndExpires(t *testing.T) {
	ctx := context.Background()
	r := memory.NewStationTripRepo()
	trip := hinted(t, "STN-1", "hint-1", t0)
	saved(t, r, trip, 0)
	v := trip.Version()
	// a newer hint supersedes the Active one
	trip.ClearDomainEvents()
	if _, err := trip.RecordCompletion("task-1", t0.Add(time.Minute), stationtrip.Position{Site: "SITE-1", Place: travel.Place{Location: "LOC-1"}}); err != nil {
		t.Fatal(err)
	}
	withHint(t, trip, "hint-2", t0.Add(time.Minute+time.Second))
	saved(t, r, trip, v)
	first, err := r.GetHint(ctx, "hint-1")
	if err != nil || first.Hint.State != stationtrip.StateSuperseded || first.Version != 2 {
		t.Fatalf("hint-1 = %+v %v", first, err)
	}
	if !first.SupersededAt.Equal(t0.Add(time.Minute + time.Second)) {
		t.Fatalf("superseded at %v", first.SupersededAt)
	}
	second, _ := r.GetHint(ctx, "hint-2")
	if second.Hint.State != stationtrip.StateActive || second.Version != 1 || second.SiteID != "SITE-1" || second.StationID != "STN-1" {
		t.Fatalf("hint-2 = %+v", second)
	}
	// consuming moves the current hint on, once
	v = trip.Version()
	if !trip.RecordClaim("task-a", t0.Add(time.Minute+2*time.Second)) {
		t.Fatal("claim should consume")
	}
	saved(t, r, trip, v)
	saved(t, r, trip, trip.Version())
	second, _ = r.GetHint(ctx, "hint-2")
	if second.Hint.State != stationtrip.StateConsumed || second.Version != 2 || second.Hint.ConsumedTask != "task-a" {
		t.Fatalf("hint-2 after consume = %+v", second)
	}
	if first, _ = r.GetHint(ctx, "hint-1"); first.Version != 2 {
		t.Fatalf("a consumed neighbour must not touch hint-1: %+v", first)
	}
}

func TestHintHistoryKeepsTerminalHintsOnSupersede(t *testing.T) {
	ctx := context.Background()
	r := memory.NewStationTripRepo()
	trip := hinted(t, "STN-1", "hint-1", t0)
	saved(t, r, trip, 0)
	v := trip.Version()
	trip.RecordClaim("task-a", t0.Add(2*time.Second))
	saved(t, r, trip, v)
	v = trip.Version()
	_, _ = trip.RecordCompletion("task-1", t0.Add(time.Minute), stationtrip.Position{Site: "SITE-1", Place: travel.Place{Location: "LOC-1"}})
	withHint(t, trip, "hint-2", t0.Add(time.Minute))
	saved(t, r, trip, v)
	first, _ := r.GetHint(ctx, "hint-1")
	if first.Hint.State != stationtrip.StateConsumed || !first.SupersededAt.IsZero() {
		t.Fatalf("a Consumed hint must stay Consumed: %+v", first)
	}
}

func TestExpiredActiveListsDueStationsOnly(t *testing.T) {
	ctx := context.Background()
	r := memory.NewStationTripRepo()
	saved(t, r, hinted(t, "STN-B", "hint-b", t0), 0)
	saved(t, r, hinted(t, "STN-A", "hint-a", t0), 0)
	saved(t, r, hinted(t, "STN-C", "hint-c", t0.Add(time.Hour)), 0)
	empty, _ := stationtrip.New("STN-D")
	saved(t, r, empty, 0)
	due := t0.Add(time.Second + 90*time.Second)
	ids, _ := r.ExpiredActive(ctx, due.Add(-time.Nanosecond), 10)
	if len(ids) != 0 {
		t.Fatalf("before expiry: %v", ids)
	}
	ids, _ = r.ExpiredActive(ctx, due, 10)
	if len(ids) != 2 || ids[0] != "STN-A" || ids[1] != "STN-B" {
		t.Fatalf("at expiry: %v", ids)
	}
	ids, _ = r.ExpiredActive(ctx, due, 1)
	if len(ids) != 1 || ids[0] != "STN-A" {
		t.Fatalf("limit: %v", ids)
	}
}

func TestGetHintUnknown(t *testing.T) {
	if _, err := memory.NewStationTripRepo().GetHint(context.Background(), "hint-x"); !errors.Is(err, repository.ErrHintNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestListHintsNewestFirstFilteredAndPaged(t *testing.T) {
	ctx := context.Background()
	r := memory.NewStationTripRepo()
	saved(t, r, hinted(t, "STN-1", "hint-1", t0), 0)
	saved(t, r, hinted(t, "STN-2", "hint-2", t0.Add(time.Minute)), 0)
	saved(t, r, hinted(t, "STN-3", "hint-3", t0.Add(time.Minute)), 0)
	all, _ := r.ListHints(ctx, repository.HintFilter{}, "", 10)
	if ids := hintIDs(all); len(ids) != 3 || ids[0] != "hint-3" || ids[1] != "hint-2" || ids[2] != "hint-1" {
		t.Fatalf("order = %v", ids)
	}
	page, _ := r.ListHints(ctx, repository.HintFilter{}, "hint-3", 1)
	if ids := hintIDs(page); len(ids) != 1 || ids[0] != "hint-2" {
		t.Fatalf("page = %v", ids)
	}
	if none, _ := r.ListHints(ctx, repository.HintFilter{}, "hint-zz", 5); len(none) != 0 {
		t.Fatalf("unknown cursor = %v", hintIDs(none))
	}
	byStation, _ := r.ListHints(ctx, repository.HintFilter{StationID: "STN-1"}, "", 10)
	bySite, _ := r.ListHints(ctx, repository.HintFilter{SiteID: "SITE-9"}, "", 10)
	byState, _ := r.ListHints(ctx, repository.HintFilter{State: stationtrip.StateExpired}, "", 10)
	if len(byStation) != 1 || len(bySite) != 0 || len(byState) != 0 {
		t.Fatalf("filters = %d %d %d", len(byStation), len(bySite), len(byState))
	}
	active, _ := r.ListHints(ctx, repository.HintFilter{State: stationtrip.StateActive, SiteID: "SITE-1"}, "", 10)
	if len(active) != 3 {
		t.Fatalf("active at SITE-1 = %d", len(active))
	}
}

func hintIDs(recs []repository.HintRecord) []string {
	ids := make([]string, 0, len(recs))
	for _, r := range recs {
		ids = append(ids, string(r.Hint.ID))
	}
	return ids
}

func TestTaskPoolKeepsCopiesAndListsAvailableOfASite(t *testing.T) {
	ctx := context.Background()
	p := memory.NewTaskPool()
	caps := []string{"FORKLIFT"}
	_ = p.Save(ctx, repository.Task{ID: "t2", SiteID: "S1", State: repository.TaskAvailable, RequiredCapabilities: caps})
	_ = p.Save(ctx, repository.Task{ID: "t1", SiteID: "S1", State: repository.TaskAvailable})
	_ = p.Save(ctx, repository.Task{ID: "t3", SiteID: "S1", State: repository.TaskClaimed})
	_ = p.Save(ctx, repository.Task{ID: "t4", SiteID: "S2", State: repository.TaskAvailable})
	caps[0] = "mutated"
	got, ok, _ := p.Get(ctx, "t2")
	if !ok || got.RequiredCapabilities[0] != "FORKLIFT" {
		t.Fatalf("pool shares the caller's slice: %+v", got)
	}
	if _, ok, _ := p.Get(ctx, "nope"); ok {
		t.Fatal("unknown task found")
	}
	open, _ := p.Available(ctx, "S1")
	if len(open) != 2 || open[0].ID != "t1" || open[1].ID != "t2" {
		t.Fatalf("available = %+v", open)
	}
}

func TestLocationBookJoinsZoneSiteAndGeometry(t *testing.T) {
	ctx := context.Background()
	b := memory.NewLocationBook()
	_ = b.SaveGeometry(ctx, "LOC-1", 1, 2, 3) // geometry before registration
	places, _ := b.Places(ctx, []string{"LOC-1", "LOC-2", "LOC-9"})
	if p := places["LOC-1"]; p.SiteCode != "" || p.Point == nil || *p.Point != (travel.Point{X: 1, Y: 2, Z: 3}) {
		t.Fatalf("geometry only = %+v", p)
	}
	_ = b.SaveZone(ctx, repository.Zone{ID: "z1", SiteCode: "SITE-1", ZoneCode: "FWD"})
	_ = b.SaveLocation(ctx, "LOC-1", "z1")
	_ = b.SaveLocation(ctx, "LOC-2", "z1")
	places, _ = b.Places(ctx, []string{"LOC-1", "LOC-2", "LOC-9"})
	if p := places["LOC-1"]; p.SiteCode != "SITE-1" || p.Point == nil || p.Decommissioned {
		t.Fatalf("joined = %+v", p)
	}
	if p := places["LOC-2"]; p.SiteCode != "SITE-1" || p.Point != nil {
		t.Fatalf("no geometry = %+v", p)
	}
	if _, ok := places["LOC-9"]; ok {
		t.Fatal("unseen location returned")
	}
}

func TestLocationBookDecommissionIsForGood(t *testing.T) {
	ctx := context.Background()
	b := memory.NewLocationBook()
	_ = b.SaveZone(ctx, repository.Zone{ID: "z1", SiteCode: "SITE-1"})
	_ = b.SaveLocation(ctx, "LOC-1", "z1")
	_ = b.DecommissionLocation(ctx, "LOC-1")
	_ = b.SaveLocation(ctx, "LOC-1", "z1") // a replayed registration
	_ = b.DecommissionLocation(ctx, "LOC-X")
	places, _ := b.Places(ctx, []string{"LOC-1", "LOC-X"})
	if !places["LOC-1"].Decommissioned || places["LOC-1"].SiteCode != "SITE-1" {
		t.Fatalf("LOC-1 = %+v", places["LOC-1"])
	}
	if !places["LOC-X"].Decommissioned {
		t.Fatalf("a never seen location can be decommissioned: %+v", places["LOC-X"])
	}
}

func TestLocationBookPlacesDoNotShareThePoint(t *testing.T) {
	ctx := context.Background()
	b := memory.NewLocationBook()
	_ = b.SaveGeometry(ctx, "LOC-1", 1, 2, 3)
	first, _ := b.Places(ctx, []string{"LOC-1"})
	first["LOC-1"].Point.X = 99
	again, _ := b.Places(ctx, []string{"LOC-1"})
	if again["LOC-1"].Point.X != 1 {
		t.Fatal("Places shares the stored point")
	}
}

func TestEventLogRecordsInOrderAndRollsBack(t *testing.T) {
	ctx := context.Background()
	log := memory.NewEventLog()
	trip := hinted(t, "STN-1", "hint-1", t0)
	restore := log.Snapshot()
	_ = log.PublishTripEvents(ctx, trip.DomainEvents()...)
	_ = log.PublishPolicyDeclared(ctx, repository.NewPolicyDeclaration(testPolicy(t), t0))
	names := log.Names()
	if len(names) != 2 || names[0] != "HintIssued" || names[1] != "InterleavingPolicyDeclared" {
		t.Fatalf("names = %v", names)
	}
	if len(log.TripEvents()) != 1 || len(log.Declarations()) != 1 {
		t.Fatal("recorded facts missing")
	}
	restore()
	if len(log.Names()) != 0 || len(log.TripEvents()) != 0 || len(log.Declarations()) != 0 {
		t.Fatal("restore did not forget the facts")
	}
}
