//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/application/usecases"
	"github.com/claudioed/task-interleaving/internal/domain/policy"
	"github.com/claudioed/task-interleaving/internal/testing/repocontract"
)

func declarer(s stores) *usecases.DeclarePolicy {
	return &usecases.DeclarePolicy{
		Policies: s.policies, Events: &recordingSink{}, UoW: s.uow, Clock: &testClock{now: repocontract.T0},
	}
}

func declaration(ifMatch *int64) usecases.DeclarePolicyInput {
	return usecases.DeclarePolicyInput{
		SiteID: "SITE-1", MaxDetourM: 20, MaxCandidates: 3, HintTTL: time.Minute,
		EligibleTaskTypes: []string{"PICK"}, IfMatch: ifMatch,
	}
}

func version(v int64) *int64 { return &v }

func storedVersion(t *testing.T, s stores) int64 {
	t.Helper()
	rec, err := s.policies.Get(context.Background(), "SITE-1")
	if err != nil {
		t.Fatal(err)
	}
	return rec.Policy.Version()
}

// TestRacingFirstDeclarationsLetExactlyOneWin: a site declared by several
// writers at once ends with one policy at version 1; a loser either lost the
// insert race (409) or already saw the winner and has no If-Match (412).
func TestRacingFirstDeclarationsLetExactlyOneWin(t *testing.T) {
	s := newStores(t)
	uc := declarer(s)
	results := race(6, func(int) error {
		_, err := uc.Handle(context.Background(), declaration(nil))
		return err
	})
	if won := wins(t, results, repository.ErrConcurrentModification, usecases.ErrVersionMismatch); won != 1 {
		t.Fatalf("%d declarations won, want exactly 1", won)
	}
	if v := storedVersion(t, s); v != 1 {
		t.Fatalf("stored version = %d, want 1", v)
	}
	if n := s.count("interleaving_policies"); n != 1 {
		t.Fatalf("policies = %d, want 1", n)
	}
}

// TestRacingUpdatesWithOneIfMatchLetExactlyOneWin: writers holding the same
// If-Match version race; one advances the version, a loser sees either the new
// version (412) or loses the guarded UPDATE (409).
func TestRacingUpdatesWithOneIfMatchLetExactlyOneWin(t *testing.T) {
	s := newStores(t)
	uc := declarer(s)
	if _, err := uc.Handle(context.Background(), declaration(nil)); err != nil {
		t.Fatal(err)
	}
	results := race(6, func(int) error {
		_, err := uc.Handle(context.Background(), declaration(version(1)))
		return err
	})
	if won := wins(t, results, repository.ErrConcurrentModification, usecases.ErrVersionMismatch); won != 1 {
		t.Fatalf("%d updates won, want exactly 1", won)
	}
	if v := storedVersion(t, s); v != 2 {
		t.Fatalf("stored version = %d, want 2", v)
	}
}

// TestALostWriteRaceIsAConflictNotAPrecondition: below the use case, writers
// that loaded the same version and save race on the guarded UPDATE; every loser
// gets ErrConcurrentModification, never anything else.
func TestALostWriteRaceIsAConflictNotAPrecondition(t *testing.T) {
	s := newStores(t)
	ctx := context.Background()
	first := repository.PolicyRecord{Policy: repocontract.Policy(t, "SITE-1"), DeclaredAt: repocontract.T0}
	if err := s.policies.Save(ctx, first, 0); err != nil {
		t.Fatal(err)
	}
	next, err := first.Policy.WithChanges(policy.Settings{
		MaxDetourM: 30, MaxCandidates: 2, HintTTL: time.Minute, EligibleTaskTypes: []policy.TaskType{policy.TaskPick},
	})
	if err != nil {
		t.Fatal(err)
	}
	results := race(6, func(int) error {
		return s.policies.Save(ctx, repository.PolicyRecord{Policy: next, DeclaredAt: repocontract.T0.Add(time.Hour)}, 1)
	})
	if won := wins(t, results, repository.ErrConcurrentModification); won != 1 {
		t.Fatalf("%d updates won, want exactly 1", won)
	}
}

// TestAStaleOrMissingIfMatchIsAPrecondition: no race at all, the caller's
// version is wrong, so the answer is the 412 error and the policy is untouched.
func TestAStaleOrMissingIfMatchIsAPrecondition(t *testing.T) {
	s := newStores(t)
	uc := declarer(s)
	ctx := context.Background()
	if _, err := uc.Handle(ctx, declaration(nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := uc.Handle(ctx, declaration(version(1))); err != nil {
		t.Fatal(err)
	}
	for name, ifMatch := range map[string]*int64{"stale": version(1), "missing": nil, "ahead": version(9)} {
		_, err := uc.Handle(ctx, declaration(ifMatch))
		if !errors.Is(err, usecases.ErrVersionMismatch) || errors.Is(err, repository.ErrConcurrentModification) {
			t.Fatalf("%s If-Match: err = %v, want ErrVersionMismatch", name, err)
		}
	}
	if v := storedVersion(t, s); v != 2 {
		t.Fatalf("stored version = %d, want 2", v)
	}
}
