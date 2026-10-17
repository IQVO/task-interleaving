package policy_test

import (
	"errors"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/task-interleaving/internal/domain/policy"
)

func validSettings() policy.Settings {
	return policy.Settings{
		MaxDetourM:        25,
		MaxCandidates:     3,
		HintTTL:           90 * time.Second,
		EligibleTaskTypes: []policy.TaskType{policy.TaskPick, policy.TaskReplenish},
		VerticalWeight:    1.5,
	}
}

func mustSite(t *testing.T, s string) policy.SiteID {
	t.Helper()
	id, err := policy.NewSiteID(s)
	if err != nil {
		t.Fatalf("NewSiteID(%q): %v", s, err)
	}
	return id
}

func TestNewSiteID(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		{"plain", "SITE-1", true},
		{"max length", strings.Repeat("a", 64), true},
		{"empty", "", false},
		{"too long", strings.Repeat("a", 65), false},
		{"multibyte counts runes", strings.Repeat("é", 64), true},
		{"multibyte too long", strings.Repeat("é", 65), false},
		{"space", "SITE 1", false},
		{"slash", "SITE/1", false},
		{"control", "SITE\x011", false},
		{"tab", "SITE\t1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := policy.NewSiteID(tc.in)
			if tc.ok && (err != nil || string(got) != tc.in) {
				t.Fatalf("want ok, got %q, %v", got, err)
			}
			if !tc.ok && !errors.Is(err, policy.ErrInvalidSiteID) {
				t.Fatalf("want ErrInvalidSiteID, got %v", err)
			}
		})
	}
}

func TestTaskTypeValid(t *testing.T) {
	known := []policy.TaskType{
		policy.TaskPick, policy.TaskPack, policy.TaskRebin,
		policy.TaskSlam, policy.TaskRelocate, policy.TaskReplenish,
	}
	for _, tt := range known {
		if !tt.Valid() {
			t.Errorf("%q should be valid", tt)
		}
	}
	for _, tt := range []policy.TaskType{"", "pick", "DISPATCH", "ARRIVAL"} {
		if tt.Valid() {
			t.Errorf("%q should be invalid", tt)
		}
	}
}

func TestNewDeclaresVersionOne(t *testing.T) {
	site := mustSite(t, "SITE-1")
	p, err := policy.New(site, validSettings())
	if err != nil {
		t.Fatal(err)
	}
	if p.SiteID() != site || p.Version() != 1 {
		t.Fatalf("site %q version %d", p.SiteID(), p.Version())
	}
	if p.MaxDetourM() != 25 || p.MaxCandidates() != 3 || p.HintTTL() != 90*time.Second || p.VerticalWeight() != 1.5 {
		t.Fatalf("settings not kept: %+v", p.Settings())
	}
	if !slices.Equal(p.EligibleTaskTypes(), []policy.TaskType{policy.TaskPick, policy.TaskReplenish}) {
		t.Fatalf("types %v", p.EligibleTaskTypes())
	}
}

func TestNewRejectsInvalidSite(t *testing.T) {
	if _, err := policy.New("bad site", validSettings()); !errors.Is(err, policy.ErrInvalidSiteID) {
		t.Fatalf("want ErrInvalidSiteID, got %v", err)
	}
}

func TestMaxDetourBoundary(t *testing.T) {
	cases := []struct {
		name string
		v    float64
		ok   bool
	}{
		{"just above zero", 0.1, true},
		{"large", 1e6, true},
		{"zero", 0, false},
		{"negative", -1, false},
		{"NaN", math.NaN(), false},
		{"+Inf", math.Inf(1), false},
		{"-Inf", math.Inf(-1), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := validSettings()
			s.MaxDetourM = tc.v
			expectSettings(t, s, tc.ok, policy.ErrInvalidMaxDetour)
		})
	}
}

func TestMaxCandidatesBoundary(t *testing.T) {
	cases := []struct {
		v  int
		ok bool
	}{{0, false}, {1, true}, {2, true}, {4, true}, {5, true}, {6, false}, {-1, false}}
	for _, tc := range cases {
		s := validSettings()
		s.MaxCandidates = tc.v
		expectSettings(t, s, tc.ok, policy.ErrInvalidCandidates)
	}
}

func TestHintTTLBoundary(t *testing.T) {
	cases := []struct {
		v  time.Duration
		ok bool
	}{{time.Nanosecond, true}, {time.Hour, true}, {0, false}, {-time.Second, false}}
	for _, tc := range cases {
		s := validSettings()
		s.HintTTL = tc.v
		expectSettings(t, s, tc.ok, policy.ErrInvalidHintTTL)
	}
}

func TestVerticalWeightBoundary(t *testing.T) {
	cases := []struct {
		name string
		v    float64
		ok   bool
	}{
		{"zero is allowed", 0, true},
		{"positive", 3, true},
		{"negative", -0.1, false},
		{"NaN", math.NaN(), false},
		{"+Inf", math.Inf(1), false},
		{"-Inf", math.Inf(-1), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := validSettings()
			s.VerticalWeight = tc.v
			expectSettings(t, s, tc.ok, policy.ErrInvalidWeight)
		})
	}
}

func TestEligibleTaskTypesRules(t *testing.T) {
	cases := []struct {
		name string
		in   []policy.TaskType
		ok   bool
	}{
		{"nil", nil, false},
		{"empty", []policy.TaskType{}, false},
		{"unknown", []policy.TaskType{policy.TaskPick, "DISPATCH"}, false},
		{"lowercase", []policy.TaskType{"pick"}, false},
		{"single", []policy.TaskType{policy.TaskSlam}, true},
		{"all", []policy.TaskType{
			policy.TaskPick, policy.TaskPack, policy.TaskRebin,
			policy.TaskSlam, policy.TaskRelocate, policy.TaskReplenish,
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := validSettings()
			s.EligibleTaskTypes = tc.in
			expectSettings(t, s, tc.ok, policy.ErrInvalidTaskTypes)
		})
	}
}

func TestEligibleTaskTypesAreSortedAndDeduplicated(t *testing.T) {
	s := validSettings()
	s.EligibleTaskTypes = []policy.TaskType{
		policy.TaskReplenish, policy.TaskPick, policy.TaskReplenish, policy.TaskPack, policy.TaskPick,
	}
	p, err := policy.New(mustSite(t, "S"), s)
	if err != nil {
		t.Fatal(err)
	}
	want := []policy.TaskType{policy.TaskPack, policy.TaskPick, policy.TaskReplenish}
	if !slices.Equal(p.EligibleTaskTypes(), want) {
		t.Fatalf("got %v want %v", p.EligibleTaskTypes(), want)
	}
}

func TestEligible(t *testing.T) {
	p, err := policy.New(mustSite(t, "S"), validSettings())
	if err != nil {
		t.Fatal(err)
	}
	if !p.Eligible(policy.TaskPick) || !p.Eligible(policy.TaskReplenish) {
		t.Fatal("declared types must be eligible")
	}
	if p.Eligible(policy.TaskPack) || p.Eligible("") {
		t.Fatal("undeclared types must not be eligible")
	}
}

func TestPolicyIsImmutableThroughAccessors(t *testing.T) {
	p, err := policy.New(mustSite(t, "S"), validSettings())
	if err != nil {
		t.Fatal(err)
	}
	p.EligibleTaskTypes()[0] = policy.TaskSlam
	s := p.Settings()
	s.EligibleTaskTypes[0] = policy.TaskSlam
	if !p.Eligible(policy.TaskPick) || p.Eligible(policy.TaskSlam) {
		t.Fatal("mutating a returned slice must not change the policy")
	}
}

func TestNewDoesNotAliasTheCallersSlice(t *testing.T) {
	s := validSettings()
	p, err := policy.New(mustSite(t, "S"), s)
	if err != nil {
		t.Fatal(err)
	}
	s.EligibleTaskTypes[0] = policy.TaskSlam
	if p.Eligible(policy.TaskSlam) || !p.Eligible(policy.TaskPick) {
		t.Fatal("the policy must not alias the settings it was built from")
	}
}

func TestWithChangesBumpsVersionAndReplacesSettings(t *testing.T) {
	site := mustSite(t, "SITE-1")
	p1, err := policy.New(site, validSettings())
	if err != nil {
		t.Fatal(err)
	}
	next := validSettings()
	next.MaxDetourM = 40
	next.MaxCandidates = 5
	next.HintTTL = 30 * time.Second
	next.EligibleTaskTypes = []policy.TaskType{policy.TaskPack}
	next.VerticalWeight = 0
	p2, err := p1.WithChanges(next)
	if err != nil {
		t.Fatal(err)
	}
	if p2.Version() != 2 || p2.SiteID() != site {
		t.Fatalf("version %d site %q", p2.Version(), p2.SiteID())
	}
	if p2.MaxDetourM() != 40 || p2.MaxCandidates() != 5 || p2.HintTTL() != 30*time.Second || p2.VerticalWeight() != 0 {
		t.Fatalf("settings not replaced: %+v", p2.Settings())
	}
	if !p2.Eligible(policy.TaskPack) || p2.Eligible(policy.TaskPick) {
		t.Fatal("eligible types must be replaced as a whole")
	}
	if p1.Version() != 1 || p1.MaxDetourM() != 25 {
		t.Fatal("the previous version must be untouched")
	}
	p3, err := p2.WithChanges(next)
	if err != nil || p3.Version() != 3 {
		t.Fatalf("same values still advance the version: %v %d", err, p3.Version())
	}
}

func TestWithChangesValidates(t *testing.T) {
	p, err := policy.New(mustSite(t, "S"), validSettings())
	if err != nil {
		t.Fatal(err)
	}
	bad := validSettings()
	bad.MaxCandidates = 6
	if _, err := p.WithChanges(bad); !errors.Is(err, policy.ErrInvalidCandidates) {
		t.Fatalf("want ErrInvalidCandidates, got %v", err)
	}
	if p.Version() != 1 {
		t.Fatal("a refused change must not advance the version")
	}
}

func TestRestore(t *testing.T) {
	site := mustSite(t, "SITE-1")
	p, err := policy.Restore(site, validSettings(), 7)
	if err != nil || p.Version() != 7 {
		t.Fatalf("restore: %v version %d", err, p.Version())
	}
	if _, err := policy.Restore(site, validSettings(), 1); err != nil {
		t.Fatalf("version 1 is the boundary and must restore: %v", err)
	}
	if _, err := policy.Restore(site, validSettings(), 0); !errors.Is(err, policy.ErrInvalidVersion) {
		t.Fatalf("want ErrInvalidVersion, got %v", err)
	}
	bad := validSettings()
	bad.HintTTL = 0
	if _, err := policy.Restore(site, bad, 3); !errors.Is(err, policy.ErrInvalidHintTTL) {
		t.Fatalf("a corrupt row must not become an aggregate: %v", err)
	}
}

func expectSettings(t *testing.T, s policy.Settings, ok bool, want error) {
	t.Helper()
	_, err := policy.New("SITE-1", s)
	if ok && err != nil {
		t.Fatalf("want ok, got %v for %+v", err, s)
	}
	if !ok && !errors.Is(err, want) {
		t.Fatalf("want %v, got %v for %+v", want, err, s)
	}
}
