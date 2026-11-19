package postgres

import (
	"context"
	"testing"
	"time"

	"crm-core/domain"
	domainearnrule "crm-core/domain/earnrule"

	"github.com/stretchr/testify/require"
)

func TestEarnRuleRepositoryCRUD(t *testing.T) {
	db := newTestDB(t)
	repo := NewEarnRuleRepository(db)
	ctx := context.Background()
	tier := defaultTier(t, db)

	_, err := repo.FindByID(ctx, 999)
	require.ErrorIs(t, err, domain.ErrNotFound)

	rule := &domainearnrule.EarnRule{
		Type: domainearnrule.TypeTierMultiplier, TierID: &tier.ID, Multiplier: 1.5,
		IsActive: true, CreatedBy: 1, CreatedByEmail: "owner@example.com",
	}
	require.NoError(t, repo.Create(ctx, rule))
	require.NotZero(t, rule.ID)

	got, err := repo.FindByID(ctx, rule.ID)
	require.NoError(t, err)
	require.Equal(t, 1.5, got.Multiplier)
	require.Equal(t, tier.ID, *got.TierID)

	// Zero values persist through Update (a cleared SKU list stays empty).
	got.Multiplier = 2
	got.SKUIDs = []string{}
	got.IsActive = false
	require.NoError(t, repo.Update(ctx, got))
	again, err := repo.FindByID(ctx, rule.ID)
	require.NoError(t, err)
	require.Equal(t, 2.0, again.Multiplier)
	require.Empty(t, again.SKUIDs)
	require.False(t, again.IsActive)
}

func TestEarnRuleRepositoryFindAllAndActive(t *testing.T) {
	db := newTestDB(t)
	repo := NewEarnRuleRepository(db)
	ctx := context.Background()

	at := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	windowStart := at.AddDate(0, 0, -1)
	windowEnd := at.AddDate(0, 0, 1)

	rules := []*domainearnrule.EarnRule{
		{Type: domainearnrule.TypeCampaignMultiplier, Multiplier: 2, IsActive: true, Priority: 5,
			SKUIDs: []string{"sku-1"}, StartAt: &windowStart, EndAt: &windowEnd},
		{Type: domainearnrule.TypeCampaignMultiplier, Multiplier: 3, IsActive: true, Priority: 1},
		{Type: domainearnrule.TypeCampaignMultiplier, Multiplier: 4, IsActive: false, Priority: 9},
		{Type: domainearnrule.TypeBonusFixed, Trigger: domainearnrule.TriggerSignup, Points: 100, IsActive: true},
	}
	for _, rule := range rules {
		require.NoError(t, repo.Create(ctx, rule))
	}

	all, err := repo.FindAll(ctx, domainearnrule.Query{})
	require.NoError(t, err)
	require.Len(t, all, 4)
	require.Equal(t, 9, all[0].Priority, "priority desc, then id asc")

	campaigns, err := repo.FindAll(ctx, domainearnrule.Query{Type: domainearnrule.TypeCampaignMultiplier})
	require.NoError(t, err)
	require.Len(t, campaigns, 3)

	active, err := repo.FindActive(ctx, at)
	require.NoError(t, err)
	require.Len(t, active, 3, "the disabled rule and nothing else is excluded")

	// Outside the window the campaign with a window drops out.
	before := windowStart.Add(-time.Hour)
	active, err = repo.FindActive(ctx, before)
	require.NoError(t, err)
	require.Len(t, active, 2)

	// The end of a window is exclusive, like every other window in the CRM.
	active, err = repo.FindActive(ctx, windowEnd)
	require.NoError(t, err)
	require.Len(t, active, 2)
	for _, rule := range active {
		require.NotEqual(t, rules[0].ID, rule.ID, "the window ended at exactly this instant")
	}
}
