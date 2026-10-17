package repocontract

import (
	"testing"
	"time"

	"github.com/claudioed/task-interleaving/internal/application/ports"
	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/domain/policy"
)

// PolicyRepo runs the PolicyRepo contract. newRepo must return an EMPTY
// repository on every call.
func PolicyRepo(t *testing.T, newRepo func(t *testing.T) ports.PolicyRepo) {
	t.Helper()
	cases := map[string]func(t *testing.T, r ports.PolicyRepo){
		"an unknown policy is not found":             policyNotFound,
		"save then get round trips every field":      policyRoundTrip,
		"inserting over an existing policy is lost":  policyInsertTwice,
		"an update is guarded by the loaded version": policyUpdateGuard,
		"policies of different sites are separate":   policySitesSeparate,
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) { fn(t, newRepo(t)) })
	}
}

func record(t *testing.T, site policy.SiteID, at time.Time) repository.PolicyRecord {
	t.Helper()
	return repository.PolicyRecord{Policy: Policy(t, site), DeclaredAt: at}
}

func samePolicy(a, b repository.PolicyRecord) bool {
	x, y := a.Policy, b.Policy
	if x.SiteID() != y.SiteID() || x.Version() != y.Version() || !a.DeclaredAt.Equal(b.DeclaredAt) {
		return false
	}
	if x.MaxDetourM() != y.MaxDetourM() || x.MaxCandidates() != y.MaxCandidates() {
		return false
	}
	if x.HintTTL() != y.HintTTL() || x.VerticalWeight() != y.VerticalWeight() {
		return false
	}
	return equalSlice(x.EligibleTaskTypes(), y.EligibleTaskTypes())
}

func policyNotFound(t *testing.T, r ports.PolicyRepo) {
	_, err := r.Get(bg, Site)
	wantErr(t, err, repository.ErrPolicyNotFound)
}

func policyRoundTrip(t *testing.T, r ports.PolicyRepo) {
	in := record(t, Site, T0)
	must(t, r.Save(bg, in, 0))
	got, err := r.Get(bg, Site)
	must(t, err)
	if !samePolicy(got, in) {
		t.Fatalf("got %+v, want %+v", got, in)
	}
}

func policyInsertTwice(t *testing.T, r ports.PolicyRepo) {
	must(t, r.Save(bg, record(t, Site, T0), 0))
	wantErr(t, r.Save(bg, record(t, Site, T0.Add(time.Hour)), 0), repository.ErrConcurrentModification)
	got, err := r.Get(bg, Site)
	must(t, err)
	if !got.DeclaredAt.Equal(T0) {
		t.Fatalf("the losing insert changed the stored policy: %+v", got)
	}
}

func policyUpdateGuard(t *testing.T, r ports.PolicyRepo) {
	first := record(t, Site, T0)
	must(t, r.Save(bg, first, 0))
	s := first.Policy.Settings()
	s.MaxDetourM, s.HintTTL = 40, 2*time.Minute
	s.EligibleTaskTypes = []policy.TaskType{policy.TaskPack}
	next, err := first.Policy.WithChanges(s)
	must(t, err)
	later := T0.Add(2 * time.Hour)
	wantErr(t, r.Save(bg, repository.PolicyRecord{Policy: next, DeclaredAt: later}, 7), repository.ErrConcurrentModification)
	must(t, r.Save(bg, repository.PolicyRecord{Policy: next, DeclaredAt: later}, 1))
	got, err := r.Get(bg, Site)
	must(t, err)
	if !samePolicy(got, repository.PolicyRecord{Policy: next, DeclaredAt: later}) {
		t.Fatalf("after update: %+v", got)
	}
	// the same loaded version again is stale now
	wantErr(t, r.Save(bg, repository.PolicyRecord{Policy: next, DeclaredAt: T0}, 1), repository.ErrConcurrentModification)
	// an update of a site that never declared is a lost race too
	wantErr(t, r.Save(bg, record(t, "SITE-9", T0), 1), repository.ErrConcurrentModification)
}

func policySitesSeparate(t *testing.T, r ports.PolicyRepo) {
	must(t, r.Save(bg, record(t, "SITE-A", T0), 0))
	must(t, r.Save(bg, record(t, "SITE-B", T0.Add(time.Minute)), 0))
	a, err := r.Get(bg, "SITE-A")
	must(t, err)
	b, err := r.Get(bg, "SITE-B")
	must(t, err)
	if a.Policy.SiteID() != "SITE-A" || b.Policy.SiteID() != "SITE-B" || !b.DeclaredAt.Equal(T0.Add(time.Minute)) {
		t.Fatalf("a = %+v, b = %+v", a, b)
	}
}
