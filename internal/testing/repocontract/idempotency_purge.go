package repocontract

import (
	"testing"
	"time"

	"github.com/claudioed/task-interleaving/internal/application/ports"
	"github.com/claudioed/task-interleaving/internal/application/repository"
)

// IdempotencyPurge runs the retention half of the IdempotencyStore contract
// (PurgeExpired, ADR 0013 Amendment 2) over an EMPTY store; Lookup and Save
// keep their suite in small.go. The memory double and the Postgres store run
// the same cases, so what the double shows is what production does.
func IdempotencyPurge(t *testing.T, newStore func(t *testing.T) ports.IdempotencyStore) {
	t.Helper()
	cases := []struct {
		name string
		run  func(*testing.T, ports.IdempotencyStore)
	}{
		{"expired answers go, live ones stay and still replay", purgeKeepsLive},
		{"one batch per pass, the next passes finish", purgeBatches},
		{"oldest first", purgeOldestFirst},
		{"a limit of zero deletes nothing", purgeZeroLimit},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.run(t, newStore(t)) })
	}
}

// purgeKeepsLive: a row created before the cutoff goes, a row AT the cutoff
// and a later one stay and still replay; a second pass has nothing to delete.
func purgeKeepsLive(t *testing.T, s ports.IdempotencyStore) {
	put(t, s, "old", T0.Add(-2*time.Hour))
	put(t, s, "edge", T0)
	put(t, s, "new", T0.Add(time.Hour))
	if n, err := s.PurgeExpired(bg, T0, 100); err != nil || n != 1 {
		t.Fatalf("purge = %d %v, want 1 (only the row before T0)", n, err)
	}
	if _, ok, err := s.Lookup(bg, "old", T0); err != nil || ok {
		t.Fatalf("the expired row survived: found %v %v", ok, err)
	}
	for _, key := range []string{"edge", "new"} {
		if _, ok, err := s.Lookup(bg, key, T0); err != nil || !ok {
			t.Fatalf("%s after the purge = found %v %v, want the live row kept", key, ok, err)
		}
	}
	if n, err := s.PurgeExpired(bg, T0, 100); err != nil || n != 0 {
		t.Fatalf("second purge = %d %v, want 0: nothing expired is left", n, err)
	}
}

// purgeBatches: one pass deletes at most limit rows; the next passes finish
// the backlog and never touch the live row.
func purgeBatches(t *testing.T, s ports.IdempotencyStore) {
	for i := range 5 {
		put(t, s, string(rune('a'+i)), T0.Add(-time.Duration(6-i)*time.Hour))
	}
	put(t, s, "live", T0.Add(time.Hour))
	for _, want := range []int{2, 2, 1, 0} {
		if n, err := s.PurgeExpired(bg, T0, 2); err != nil || n != want {
			t.Fatalf("purge = %d %v, want %d", n, err, want)
		}
	}
	if _, ok, err := s.Lookup(bg, "live", T0); err != nil || !ok {
		t.Fatalf("the live row = found %v %v, want it kept", ok, err)
	}
}

// purgeOldestFirst: with two expired rows and a limit of one, the older goes.
// A Save whose notBefore is older than both rows changes nothing (the first
// writer wins), so what Lookup then finds tells the survivor apart: the
// deleted key gets the fresh answer, the survivor keeps its stored one.
func purgeOldestFirst(t *testing.T, s ports.IdempotencyStore) {
	const keyOlder, keyNewer = "older", "newer"
	put(t, s, keyOlder, T0.Add(-2*time.Hour))
	put(t, s, keyNewer, T0.Add(-time.Hour))
	if n, err := s.PurgeExpired(bg, T0, 1); err != nil || n != 1 {
		t.Fatalf("purge = %d %v, want 1", n, err)
	}
	fresh := T0.Add(2 * time.Hour)
	live := T0.Add(-3 * time.Hour)
	for _, key := range []string{keyOlder, keyNewer} {
		must(t, s.Save(bg, repository.IdempotencyRecord{Key: key, Fingerprint: "fresh", Status: 200, CreatedAt: fresh}, live))
	}
	for key, want := range map[string]string{keyOlder: "fresh", keyNewer: keyNewer} {
		got, ok, err := s.Lookup(bg, key, live)
		if err != nil || !ok || got.Fingerprint != want {
			t.Fatalf("%s = %q found %v %v, want fingerprint %q", key, got.Fingerprint, ok, err, want)
		}
	}
}

// purgeZeroLimit: a limit of zero is a no-op, not an error.
func purgeZeroLimit(t *testing.T, s ports.IdempotencyStore) {
	put(t, s, "old", T0.Add(-time.Hour))
	if n, err := s.PurgeExpired(bg, T0, 0); err != nil || n != 0 {
		t.Fatalf("purge = %d %v, want 0", n, err)
	}
	if _, ok, err := s.Lookup(bg, "old", T0.Add(-2*time.Hour)); err != nil || !ok {
		t.Fatalf("the row = found %v %v, want it untouched", ok, err)
	}
}

// put stores one answer for key created at (the store's replay contract,
// small.go, covers Lookup and Save themselves).
func put(t *testing.T, s ports.IdempotencyStore, key string, at time.Time) {
	t.Helper()
	must(t, s.Save(bg, repository.IdempotencyRecord{Key: key, Fingerprint: key, Status: 200, CreatedAt: at}, at.Add(-24*time.Hour)))
}
