package usecases_test

import (
	"errors"
	"testing"
	"time"

	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/application/usecases"
)

// faultCase runs act on a seeded env with each of the named methods failing in
// turn and expects the failure to come back (wrapped is fine).
type faultCase struct {
	name   string
	hinted bool
	fail   []string
	act    func(e *env) error
}

func TestEveryPortFailureSurfaces(t *testing.T) {
	var cases []faultCase
	cases = append(cases, policyFaults()...)
	cases = append(cases, taskFaults()...)
	cases = append(cases, layoutFaults()...)
	cases = append(cases, readFaults()...)
	for _, c := range cases {
		for _, key := range c.fail {
			t.Run(c.name+"/"+key, func(t *testing.T) {
				e := newEnv()
				if c.hinted {
					e.seedHinted(t)
				} else {
					e.seed(t)
				}
				e.fail(key)
				if err := c.act(e); !errors.Is(err, errBoom) {
					t.Fatalf("%s with %s failing = %v, want boom", c.name, key, err)
				}
			})
		}
	}
}

func declareIn(site string, ifMatch *int64) usecases.DeclarePolicyInput {
	return usecases.DeclarePolicyInput{
		SiteID: site, MaxDetourM: 10, MaxCandidates: 2, HintTTL: time.Minute,
		EligibleTaskTypes: []string{"PICK"}, IfMatch: ifMatch,
	}
}

func one() *int64 { v := int64(1); return &v }

func policyFaults() []faultCase {
	return []faultCase{
		{name: "declare new", fail: []string{"PolicyRepo.Get", "PolicyRepo.Save", "EventSink.PublishPolicyDeclared"},
			act: func(e *env) error {
				_, err := e.declare().Handle(bg, declareIn("S2", nil))
				return err
			}},
		{name: "declare change", fail: []string{"PolicyRepo.Get", "PolicyRepo.Save"},
			act: func(e *env) error {
				_, err := e.declare().Handle(bg, declareIn("S1", one()))
				return err
			}},
		{name: "get policy", fail: []string{"PolicyRepo.Get"},
			act: func(e *env) error {
				_, err := (&usecases.GetPolicy{Policies: e.policies}).Handle(bg, "S1")
				return err
			}},
	}
}

func taskFaults() []faultCase {
	return []faultCase{
		{name: "task available", fail: []string{"ProcessedEvents.Claim", "TaskPool.Get", "TaskPool.Save"},
			act: func(e *env) error {
				_, err := e.available().Handle(bg, "evt-1", usecases.TaskAvailability{
					TaskID: "T-9", TaskType: "PICK", SiteID: "S1", FromLocation: "A",
				})
				return err
			}},
		{name: "task claimed", hinted: true,
			fail: []string{"ProcessedEvents.Claim", "TaskPool.Get", "TaskPool.Save", "StationTripRepo.Get", "StationTripRepo.Save", "EventSink.PublishTripEvents"},
			act: func(e *env) error {
				_, err := e.claimed().Handle(bg, "evt-1", usecases.TaskClaim{TaskID: "T-1", TaskType: "PICK", StationID: "STN-1"})
				return err
			}},
		{name: "task completed",
			fail: []string{
				"ProcessedEvents.Claim", "TaskPool.Get", "TaskPool.Save", "LocationBook.Places", "StationTripRepo.Get",
				"PolicyRepo.Get", "TaskPool.Available", "StationTripRepo.Save", "EventSink.PublishTripEvents",
			},
			act: func(e *env) error {
				_, err := e.completed().Handle(bg, "evt-1", usecases.TaskCompletion{TaskID: "T0"})
				return err
			}},
		{name: "expire", hinted: true,
			fail: []string{"StationTripRepo.ExpiredActive", "StationTripRepo.Get", "StationTripRepo.Save", "EventSink.PublishTripEvents"},
			act: func(e *env) error {
				_, err := later(e).Handle(bg)
				return err
			}},
	}
}

type laterClock struct{}

func (laterClock) Now() time.Time { return t0.Add(10 * time.Minute) }

// later is an ExpireHints whose clock is past the seeded hint's expiry.
func later(e *env) *usecases.ExpireHints {
	return &usecases.ExpireHints{TripWriter: e.writer, UoW: e.uow, Clock: laterClock{}}
}

func layoutFaults() []faultCase {
	return []faultCase{
		{name: "zone", fail: []string{"ProcessedEvents.Claim", "LocationBook.SaveZone"}, act: func(e *env) error {
			_, err := (&usecases.ApplyZone{Intake: e.intake, Book: e.book}).Handle(bg, "evt-1",
				usecases.ZoneChange{ZoneID: "z9", SiteCode: "S1", ZoneCode: "FWD"})
			return err
		}},
		{name: "location", fail: []string{"ProcessedEvents.Claim", "LocationBook.SaveLocation"}, act: func(e *env) error {
			_, err := (&usecases.ApplyLocationRegistered{Intake: e.intake, Book: e.book}).Handle(bg, "evt-1",
				usecases.LocationChange{Code: "N", ZoneID: "z1"})
			return err
		}},
		{name: "decommission", fail: []string{"ProcessedEvents.Claim", "LocationBook.DecommissionLocation"}, act: func(e *env) error {
			_, err := (&usecases.ApplyLocationDecommissioned{Intake: e.intake, Book: e.book}).Handle(bg, "evt-1", "A")
			return err
		}},
		{name: "geometry", fail: []string{"ProcessedEvents.Claim", "LocationBook.SaveGeometry"}, act: func(e *env) error {
			_, err := (&usecases.ApplyGeometryUpdated{Intake: e.intake, Book: e.book}).Handle(bg, "evt-1",
				usecases.GeometryChange{Code: "A", X: 1})
			return err
		}},
	}
}

func readFaults() []faultCase {
	return []faultCase{
		{name: "recommend", hinted: true,
			fail: []string{"StationTripRepo.Get", "LocationBook.Places", "PolicyRepo.Get", "TaskPool.Available"},
			act: func(e *env) error {
				_, err := e.recommend().Handle(bg, usecases.RecommendInput{StationID: "STN-1"})
				return err
			}},
		{name: "list hints", hinted: true, fail: []string{"StationTripRepo.ListHints"}, act: func(e *env) error {
			_, err := (&usecases.ListHints{Trips: e.trips}).Handle(bg, usecases.ListHintsQuery{})
			return err
		}},
	}
}

// TestRankingReadFailuresSurface fails the second read of the location book,
// the one the ranker makes after the origin was resolved.
func TestRankingReadFailuresSurface(t *testing.T) {
	acts := map[string]func(e *env) error{
		"task completed": func(e *env) error {
			_, err := e.completed().Handle(bg, "evt-1", usecases.TaskCompletion{TaskID: "T0"})
			return err
		},
		"recommend": func(e *env) error {
			_, err := e.recommend().Handle(bg, usecases.RecommendInput{StationID: "STN-1"})
			return err
		},
	}
	for name, act := range acts {
		t.Run(name, func(t *testing.T) {
			e := newEnv()
			e.seed(t)
			mustNil(t, e.trips.Save(bg, completedTrip(t), 0))
			e.failNth("LocationBook.Places", 2)
			if err := act(e); !errors.Is(err, errBoom) {
				t.Fatalf("err = %v, want boom", err)
			}
		})
	}
}

func TestExpireSkipsAStationThatLostTheRace(t *testing.T) {
	e := newEnv()
	e.seedHinted(t)
	e.faults.always["StationTripRepo.Save"] = repository.ErrConcurrentModification
	n, err := later(e).Handle(bg)
	if n != 0 || err != nil {
		t.Fatalf("Handle = %d, %v, want 0, nil", n, err)
	}
}

func TestExpireSkipsAStationThatVanished(t *testing.T) {
	e := newEnv()
	e.seedHinted(t)
	e.faults.always["StationTripRepo.Get"] = repository.ErrStationNotFound
	n, err := later(e).Handle(bg)
	if n != 0 || err != nil {
		t.Fatalf("Handle = %d, %v, want 0, nil", n, err)
	}
}

func TestExpireKeepsGoingAfterAFailure(t *testing.T) {
	e := newEnv()
	e.seedHinted(t)
	// a second station with its own hint
	mustNil(t, e.pool.Save(bg, repositoryTask("T9", "STN-2")))
	second := e.completed()
	second.IDs = &seqIDs{n: 1} // hint-2: a hint id belongs to one station
	if _, err := second.Handle(bg, "evt-9", usecases.TaskCompletion{TaskID: "T9"}); err != nil {
		t.Fatal(err)
	}
	e.failNth("StationTripRepo.Save", 1)
	n, err := later(e).Handle(bg)
	if !errors.Is(err, errBoom) || n != 1 {
		t.Fatalf("Handle = %d, %v, want 1 expired and boom", n, err)
	}
}

func repositoryTask(id, station string) repository.Task {
	return repository.Task{
		ID: id, Type: "PICK", SiteID: "S1", FromLocation: "L0", ToLocation: "L0",
		State: repository.TaskClaimed, ClaimedBy: station,
	}
}
