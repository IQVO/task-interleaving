package stationtrip_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/claudioed/task-interleaving/internal/domain/stationtrip"
)

func consumedTrip(t *testing.T) *stationtrip.StationTrip {
	t.Helper()
	s := hintedTrip(t)
	if !s.RecordClaim("task-b", t0.Add(10*time.Second)) {
		t.Fatal("claim of a candidate should consume the hint")
	}
	return s
}

func expiredTrip(t *testing.T) *stationtrip.StationTrip {
	t.Helper()
	s := hintedTrip(t)
	if !s.Expire(expiresAt()) {
		t.Fatal("the hint should expire")
	}
	return s
}

func TestSnapshotRoundTripsEveryShape(t *testing.T) {
	for name, trip := range map[string]*stationtrip.StationTrip{
		"empty":    newTrip(t),
		"position": completedTrip(t),
		"active":   hintedTrip(t),
		"consumed": consumedTrip(t),
		"expired":  expiredTrip(t),
	} {
		t.Run(name, func(t *testing.T) {
			snap := trip.Snapshot()
			back, err := stationtrip.Restore(snap)
			if err != nil {
				t.Fatalf("Restore: %v", err)
			}
			if !reflect.DeepEqual(back.Snapshot(), snap) {
				t.Fatalf("round trip:\n got %+v\nwant %+v", back.Snapshot(), snap)
			}
			if len(back.DomainEvents()) != 0 {
				t.Fatalf("a restored trip has no pending events, got %d", len(back.DomainEvents()))
			}
			if back.Version() != trip.Version() {
				t.Fatalf("version = %d, want %d", back.Version(), trip.Version())
			}
		})
	}
}

func TestRestoredTripKeepsWorking(t *testing.T) {
	back, err := stationtrip.Restore(hintedTrip(t).Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if !back.RecordClaim("task-a", t0.Add(5*time.Second)) {
		t.Fatal("a restored Active hint must still be consumable")
	}
	if back.Version() != 3 {
		t.Fatalf("version = %d, want 3", back.Version())
	}
}

func TestSnapshotIsADeepCopy(t *testing.T) {
	trip := hintedTrip(t)
	snap := trip.Snapshot()
	snap.Hint.Candidates[0].TaskID = "mutated"
	snap.Position.Place.Point.X = 999
	h, _ := trip.Hint()
	if h.Candidates[0].TaskID != "task-a" {
		t.Fatal("the snapshot shares the hint candidates with the trip")
	}
	pos, _ := trip.Position()
	if pos.Place.Point.X != 4.2 {
		t.Fatal("the snapshot shares the position point with the trip")
	}
}

func TestRestoreIsADeepCopy(t *testing.T) {
	snap := hintedTrip(t).Snapshot()
	back, err := stationtrip.Restore(snap)
	if err != nil {
		t.Fatal(err)
	}
	snap.Hint.Candidates[0].TaskID = "mutated"
	snap.Position.Place.Point.X = 999
	h, _ := back.Hint()
	pos, _ := back.Position()
	if h.Candidates[0].TaskID != "task-a" || pos.Place.Point.X != 4.2 {
		t.Fatal("the restored trip shares memory with the snapshot")
	}
}

func TestRestoreNormalisesToUTC(t *testing.T) {
	local := time.FixedZone("UTC-3", -3*3600)
	snap := consumedTrip(t).Snapshot()
	snap.LastAt = snap.LastAt.In(local)
	snap.Hint.IssuedAt = snap.Hint.IssuedAt.In(local)
	snap.Hint.ExpiresAt = snap.Hint.ExpiresAt.In(local)
	snap.Hint.ConsumedAt = snap.Hint.ConsumedAt.In(local)
	back, err := stationtrip.Restore(snap)
	if err != nil {
		t.Fatal(err)
	}
	got := back.Snapshot()
	for name, ts := range map[string]time.Time{
		"lastAt": got.LastAt, "issuedAt": got.Hint.IssuedAt,
		"expiresAt": got.Hint.ExpiresAt, "consumedAt": got.Hint.ConsumedAt,
	} {
		if ts.Location() != time.UTC {
			t.Errorf("%s is not UTC: %v", name, ts.Location())
		}
	}
}

type snapCase struct {
	name    string
	base    func(*testing.T) *stationtrip.StationTrip
	mutate  func(*stationtrip.Snapshot)
	wantErr error
}

func runSnapCases(t *testing.T, cases []snapCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snap := tc.base(t).Snapshot()
			tc.mutate(&snap)
			trip, err := stationtrip.Restore(snap)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr != nil && trip != nil {
				t.Fatal("a rejected snapshot must return no trip")
			}
		})
	}
}

func TestRestoreRejectsACorruptTrip(t *testing.T) {
	inv := stationtrip.ErrInvalidSnapshot
	runSnapCases(t, []snapCase{
		{"blank station", hintedTrip, func(s *stationtrip.Snapshot) { s.ID = "" }, stationtrip.ErrInvalidStationID},
		{"negative version", newTrip, func(s *stationtrip.Snapshot) { s.Version = -1 }, inv},
		{"empty trip with a version", newTrip, func(s *stationtrip.Snapshot) { s.Version = 1 }, inv},
		{"empty trip with a task", newTrip, func(s *stationtrip.Snapshot) { s.LastTask = "task-1" }, inv},
		{"empty trip with a hint", hintedTrip, func(s *stationtrip.Snapshot) { s.LastAt = time.Time{} }, inv},
		{"completion without a version", completedTrip, func(s *stationtrip.Snapshot) { s.Version = 0 }, inv},
		{"blank task", completedTrip, func(s *stationtrip.Snapshot) { s.LastTask = " " }, stationtrip.ErrInvalidTask},
		{"blank location", completedTrip, func(s *stationtrip.Snapshot) { s.Position.Place.Location = "" }, stationtrip.ErrInvalidPosition},
		{"invalid site", completedTrip, func(s *stationtrip.Snapshot) { s.Position.Site = "a b" }, stationtrip.ErrInvalidPosition},
		{"one is a fine version", completedTrip, func(s *stationtrip.Snapshot) { s.Version = 1 }, nil},
		{"an unknown point is fine", completedTrip, func(s *stationtrip.Snapshot) { s.Position.Place.Point = nil }, nil},
	})
}

func TestRestoreRejectsACorruptHint(t *testing.T) {
	inv := stationtrip.ErrInvalidSnapshot
	b := hintedTrip
	runSnapCases(t, []snapCase{
		{"invalid hint id", b, func(s *stationtrip.Snapshot) { s.Hint.ID = "nope" }, stationtrip.ErrInvalidHintID},
		{"unknown state", b, func(s *stationtrip.Snapshot) { s.Hint.State = "Open" }, inv},
		{"blank after task", b, func(s *stationtrip.Snapshot) { s.Hint.AfterTask = "" }, inv},
		{"blank origin", b, func(s *stationtrip.Snapshot) { s.Hint.Origin = " " }, inv},
		{"no issue time", b, func(s *stationtrip.Snapshot) { s.Hint.IssuedAt = time.Time{} }, inv},
		{"expiry equal to issue", b, func(s *stationtrip.Snapshot) { s.Hint.ExpiresAt = s.Hint.IssuedAt }, inv},
		{"expiry before issue", b, func(s *stationtrip.Snapshot) { s.Hint.ExpiresAt = s.Hint.IssuedAt.Add(-time.Second) }, inv},
		{"policy version zero", b, func(s *stationtrip.Snapshot) { s.Hint.PolicyVersion = 0 }, inv},
		{"policy version one is fine", b, func(s *stationtrip.Snapshot) { s.Hint.PolicyVersion = 1 }, nil},
		{"no candidates", b, func(s *stationtrip.Snapshot) { s.Hint.Candidates = nil }, inv},
		{"rank gap", b, func(s *stationtrip.Snapshot) { s.Hint.Candidates[1].Rank = 3 }, inv},
		{"rank starts at zero", b, func(s *stationtrip.Snapshot) { s.Hint.Candidates[0].Rank = 0 }, inv},
		{"blank candidate task", b, func(s *stationtrip.Snapshot) { s.Hint.Candidates[0].TaskID = "" }, inv},
		{"duplicate candidate", b, func(s *stationtrip.Snapshot) { s.Hint.Candidates[1].TaskID = "task-a" }, inv},
		{"consumed task on an active hint", b, func(s *stationtrip.Snapshot) { s.Hint.ConsumedTask = "task-a" }, inv},
		{"consumed time on an active hint", b, func(s *stationtrip.Snapshot) { s.Hint.ConsumedAt = t0 }, inv},
	})
}

func TestRestoreRejectsInconsistentConsumption(t *testing.T) {
	inv := stationtrip.ErrInvalidSnapshot
	b := consumedTrip
	runSnapCases(t, []snapCase{
		{"no consumed time", b, func(s *stationtrip.Snapshot) { s.Hint.ConsumedAt = time.Time{} }, inv},
		{"no consumed task", b, func(s *stationtrip.Snapshot) { s.Hint.ConsumedTask = "" }, inv},
		{"consumed task is not a candidate", b, func(s *stationtrip.Snapshot) { s.Hint.ConsumedTask = "task-z" }, inv},
		{"consumed hint restores", b, func(s *stationtrip.Snapshot) {}, nil},
		{"consumed task on an expired hint", expiredTrip, func(s *stationtrip.Snapshot) { s.Hint.ConsumedTask = "task-a" }, inv},
		{"consumed time on an expired hint", expiredTrip, func(s *stationtrip.Snapshot) { s.Hint.ConsumedAt = t0 }, inv},
	})
}
