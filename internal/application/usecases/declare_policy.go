package usecases

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/claudioed/task-interleaving/internal/application/ports"
	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/domain/policy"
)

// DeclarePolicyInput is the input of DeclarePolicy. A nil VerticalWeight means
// DefaultVerticalWeight. IfMatch nil declares the first version; a value
// replaces the policy only when it is the current version.
type DeclarePolicyInput struct {
	SiteID            string
	MaxDetourM        float64
	MaxCandidates     int
	HintTTL           time.Duration
	EligibleTaskTypes []string
	VerticalWeight    *float64
	IfMatch           *int64
}

// DeclarePolicy declares or replaces the interleaving policy of a site and
// enqueues the analytics-only InterleavingPolicyDeclared fact.
type DeclarePolicy struct {
	Policies ports.PolicyRepo
	Events   ports.EventSink
	UoW      ports.UnitOfWork
	Clock    ports.Clock
}

// Handle runs the use case. An existing policy needs IfMatch = its version, a
// first declaration needs none; anything else is ErrVersionMismatch. A write
// that loses a race is repository.ErrConcurrentModification. Invalid settings
// are the policy package's errors.
func (uc *DeclarePolicy) Handle(ctx context.Context, in DeclarePolicyInput) (repository.PolicyRecord, error) {
	var rec repository.PolicyRecord
	err := uc.UoW.Do(ctx, func(ctx context.Context) error {
		var err error
		rec, err = uc.declare(ctx, in)
		return err
	})
	if err != nil {
		return repository.PolicyRecord{}, err
	}
	return rec, nil
}

func (uc *DeclarePolicy) declare(ctx context.Context, in DeclarePolicyInput) (repository.PolicyRecord, error) {
	next, loaded, err := uc.next(ctx, in)
	if err != nil {
		return repository.PolicyRecord{}, err
	}
	now := uc.Clock.Now().UTC()
	rec := repository.PolicyRecord{Policy: next, DeclaredAt: now}
	if err := uc.Policies.Save(ctx, rec, loaded); err != nil {
		return repository.PolicyRecord{}, err
	}
	if err := uc.Events.PublishPolicyDeclared(ctx, repository.NewPolicyDeclaration(next, now)); err != nil {
		return repository.PolicyRecord{}, fmt.Errorf("enqueue events: %w", err)
	}
	return rec, nil
}

// next builds the policy to store and the version the save is guarded by.
func (uc *DeclarePolicy) next(ctx context.Context, in DeclarePolicyInput) (policy.Policy, int64, error) {
	site, err := policy.NewSiteID(in.SiteID)
	if err != nil {
		return policy.Policy{}, 0, err
	}
	settings := in.settings()
	cur, err := uc.Policies.Get(ctx, site)
	if errors.Is(err, repository.ErrPolicyNotFound) {
		if in.IfMatch != nil {
			return policy.Policy{}, 0, ErrVersionMismatch
		}
		p, err := policy.New(site, settings)
		return p, 0, err
	}
	if err != nil {
		return policy.Policy{}, 0, fmt.Errorf("load policy: %w", err)
	}
	if in.IfMatch == nil || *in.IfMatch != cur.Policy.Version() {
		return policy.Policy{}, 0, ErrVersionMismatch
	}
	p, err := cur.Policy.WithChanges(settings)
	return p, cur.Policy.Version(), err
}

func (in DeclarePolicyInput) settings() policy.Settings {
	weight := DefaultVerticalWeight
	if in.VerticalWeight != nil {
		weight = *in.VerticalWeight
	}
	types := make([]policy.TaskType, 0, len(in.EligibleTaskTypes))
	for _, t := range in.EligibleTaskTypes {
		types = append(types, policy.TaskType(t))
	}
	return policy.Settings{
		MaxDetourM:        in.MaxDetourM,
		MaxCandidates:     in.MaxCandidates,
		HintTTL:           in.HintTTL,
		EligibleTaskTypes: types,
		VerticalWeight:    weight,
	}
}
