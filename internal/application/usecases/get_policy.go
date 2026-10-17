package usecases

import (
	"context"

	"github.com/claudioed/task-interleaving/internal/application/ports"
	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/domain/policy"
)

// GetPolicy reads a site's policy.
type GetPolicy struct {
	Policies ports.PolicyRepo
}

// Handle returns the policy, repository.ErrPolicyNotFound for a site that never
// declared one (it is inert), or policy.ErrInvalidSiteID.
func (uc *GetPolicy) Handle(ctx context.Context, siteID string) (repository.PolicyRecord, error) {
	site, err := policy.NewSiteID(siteID)
	if err != nil {
		return repository.PolicyRecord{}, err
	}
	return uc.Policies.Get(ctx, site)
}
