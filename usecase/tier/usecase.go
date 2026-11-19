// Package tier exposes membership tiers to delivery layers.
package tier

import (
	"context"

	domainloyalty "crm-core/domain/loyalty"
)

// Usecase lists tiers. Editing tiers (owner-only settings) arrives in P1's
// settings screen.
type Usecase interface {
	List(ctx context.Context) ([]domainloyalty.Tier, error)
}

type usecase struct {
	tiers domainloyalty.TierRepository
}

// New builds the tier usecase.
func New(tiers domainloyalty.TierRepository) Usecase {
	return &usecase{tiers: tiers}
}

func (u *usecase) List(ctx context.Context) ([]domainloyalty.Tier, error) {
	return u.tiers.FindAll(ctx)
}
