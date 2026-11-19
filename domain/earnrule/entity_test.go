package earnrule

import (
	"testing"
	"time"

	"crm-core/domain"

	"github.com/stretchr/testify/require"
)

func tierID(id uint) *uint { return &id }

func TestNormalizeClearsFieldsThatDoNotApply(t *testing.T) {
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 30)

	tier := EarnRule{
		Type: TypeTierMultiplier, TierID: tierID(3), Multiplier: 1.5,
		Trigger: TriggerSignup, Points: 50, SKUIDs: []string{"a"},
	}
	tier.Normalize()
	require.Empty(t, tier.Trigger)
	require.Zero(t, tier.Points)
	require.Nil(t, tier.SKUIDs)
	require.Equal(t, uint(3), *tier.TierID)
	require.NoError(t, tier.Validate())

	campaign := EarnRule{
		Type: TypeCampaignMultiplier, TierID: tierID(3), Multiplier: 2,
		Trigger: TriggerBirthday, Points: 10, SKUIDs: []string{" sku-1 ", "sku-1", "", "sku-2"},
		StartAt: &start, EndAt: &end,
	}
	campaign.Normalize()
	require.Nil(t, campaign.TierID)
	require.Empty(t, campaign.Trigger)
	require.Zero(t, campaign.Points)
	require.Equal(t, []string{"sku-1", "sku-2"}, campaign.SKUIDs)
	require.NoError(t, campaign.Validate())

	bonus := EarnRule{
		Type: "BONUS_FIXED", Trigger: " Signup ", Points: 100, Multiplier: 7,
		TierID: tierID(1), SKUIDs: []string{"x"},
	}
	bonus.Normalize()
	require.Equal(t, TypeBonusFixed, bonus.Type)
	require.Equal(t, TriggerSignup, bonus.Trigger)
	require.Nil(t, bonus.TierID)
	require.Nil(t, bonus.SKUIDs)
	require.Equal(t, 1.0, bonus.Multiplier)
	require.NoError(t, bonus.Validate())
}

func TestValidateRequiredFieldsPerType(t *testing.T) {
	cases := []struct {
		name  string
		rule  EarnRule
		field string
	}{
		{
			name:  "unknown type",
			rule:  EarnRule{Type: "product_code"},
			field: "type",
		},
		{
			name:  "tier multiplier needs a tier",
			rule:  EarnRule{Type: TypeTierMultiplier, Multiplier: 1.5},
			field: "tierId",
		},
		{
			name:  "tier multiplier needs a positive multiplier",
			rule:  EarnRule{Type: TypeTierMultiplier, TierID: tierID(1)},
			field: "multiplier",
		},
		{
			name:  "campaign needs a positive multiplier",
			rule:  EarnRule{Type: TypeCampaignMultiplier, Multiplier: 0},
			field: "multiplier",
		},
		{
			name:  "campaign multiplier is bounded",
			rule:  EarnRule{Type: TypeCampaignMultiplier, Multiplier: 1000},
			field: "multiplier",
		},
		{
			name:  "bonus needs a known trigger",
			rule:  EarnRule{Type: TypeBonusFixed, Trigger: "anniversary", Points: 10},
			field: "trigger",
		},
		{
			name:  "bonus needs points",
			rule:  EarnRule{Type: TypeBonusFixed, Trigger: TriggerSignup},
			field: "points",
		},
		{
			name:  "window must be complete",
			rule:  EarnRule{Type: TypeCampaignMultiplier, Multiplier: 2, StartAt: ptr(time.Now())},
			field: "startAt",
		},
		{
			name: "window must be ordered",
			rule: EarnRule{
				Type: TypeCampaignMultiplier, Multiplier: 2,
				StartAt: ptr(time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)),
				EndAt:   ptr(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)),
			},
			field: "endAt",
		},
		{
			name:  "priority is bounded",
			rule:  EarnRule{Type: TypeCampaignMultiplier, Multiplier: 2, Priority: 10000},
			field: "priority",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.rule.Validate()
			require.ErrorIs(t, err, domain.ErrValidation, tc.name)
			var ve *domain.ValidationError
			require.ErrorAs(t, err, &ve)
			require.Equal(t, tc.field, ve.Field, tc.name)
		})
	}
}

func TestValidateAcceptsAWindowEndingInThePast(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 7)
	rule := EarnRule{Type: TypeCampaignMultiplier, Multiplier: 2, StartAt: &start, EndAt: &end}
	require.NoError(t, rule.Validate(), "an expired campaign is history, not an error")
}

func TestValidTypeAndTrigger(t *testing.T) {
	for _, typ := range []string{TypeTierMultiplier, TypeCampaignMultiplier, TypeBonusFixed} {
		require.True(t, ValidType(typ), typ)
	}
	require.False(t, ValidType("product_code"))
	require.False(t, ValidType(""))

	for _, trig := range []string{TriggerSignup, TriggerBirthday, TriggerFirstCode} {
		require.True(t, ValidTrigger(trig), trig)
	}
	require.False(t, ValidTrigger("order"))
}

func ptr(t time.Time) *time.Time { return &t }
