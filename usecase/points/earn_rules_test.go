package points

import (
	"context"
	"testing"
	"time"

	"crm-core/domain"
	domainearnrule "crm-core/domain/earnrule"
	domainmember "crm-core/domain/member"
	domainpoints "crm-core/domain/points"
	domainsettings "crm-core/domain/settings"

	"github.com/stretchr/testify/require"
)

func TestCalculateStacksAndPicksTheBestNonStackable(t *testing.T) {
	at := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	rules := &fakeRules{active: []domainearnrule.EarnRule{
		{ID: 1, Type: domainearnrule.TypeTierMultiplier, TierID: uintPtr(2), Multiplier: 1.5},
		{ID: 2, Type: domainearnrule.TypeTierMultiplier, TierID: uintPtr(3), Multiplier: 2},
		{ID: 3, Type: domainearnrule.TypeCampaignMultiplier, Multiplier: 2, Stackable: true},
		{ID: 4, Type: domainearnrule.TypeCampaignMultiplier, Multiplier: 1.5, Stackable: true,
			SKUIDs: []string{"sku-a"}},
		{ID: 5, Type: domainearnrule.TypeCampaignMultiplier, Multiplier: 3, Priority: 1},
		{ID: 6, Type: domainearnrule.TypeCampaignMultiplier, Multiplier: 4, Priority: 5},
	}}

	uc := New(&fakeLedger{}, nil, WithEarnRules(rules))
	result, err := uc.Calculate(context.Background(), CalculateInput{
		TierID: 2, SKUID: "sku-a", BasePoints: 100, At: at,
	})
	require.NoError(t, err)
	// 100 × 1.5 (tier) × (2 × 1.5 stackable) × 4 (best non-stackable) = 1800.
	require.EqualValues(t, 1800, result.Points)
	require.Equal(t, at, rules.lastAt)

	ids := make([]uint, 0, len(result.Rules))
	for _, rule := range result.Rules {
		ids = append(ids, rule.ID)
	}
	require.ElementsMatch(t, []uint{1, 3, 4, 6}, ids)
}

func TestCalculateTieBreaksOnTheLowerID(t *testing.T) {
	rules := &fakeRules{active: []domainearnrule.EarnRule{
		{ID: 9, Type: domainearnrule.TypeCampaignMultiplier, Multiplier: 5, Priority: 3},
		{ID: 7, Type: domainearnrule.TypeCampaignMultiplier, Multiplier: 2, Priority: 3},
	}}
	uc := New(&fakeLedger{}, nil, WithEarnRules(rules))
	result, err := uc.Calculate(context.Background(), CalculateInput{TierID: 1, BasePoints: 10})
	require.NoError(t, err)
	require.EqualValues(t, 20, result.Points)
	require.Len(t, result.Rules, 1)
	require.Equal(t, uint(7), result.Rules[0].ID)
}

func TestCalculateSkipsCampaignsForOtherSKUs(t *testing.T) {
	rules := &fakeRules{active: []domainearnrule.EarnRule{
		{ID: 1, Type: domainearnrule.TypeCampaignMultiplier, Multiplier: 10, Stackable: true,
			SKUIDs: []string{"sku-b"}},
	}}
	uc := New(&fakeLedger{}, nil, WithEarnRules(rules))

	result, err := uc.Calculate(context.Background(), CalculateInput{TierID: 1, SKUID: "sku-a", BasePoints: 10})
	require.NoError(t, err)
	require.EqualValues(t, 10, result.Points)
	require.Empty(t, result.Rules)

	// With no SKU the SKU-limited rule can never match; a wildcard rule still does.
	rules.active = append(rules.active, domainearnrule.EarnRule{
		ID: 2, Type: domainearnrule.TypeCampaignMultiplier, Multiplier: 3, Stackable: true,
	})
	result, err = uc.Calculate(context.Background(), CalculateInput{TierID: 1, BasePoints: 10})
	require.NoError(t, err)
	require.EqualValues(t, 30, result.Points)
}

func TestCalculateFloorsTheResult(t *testing.T) {
	rules := &fakeRules{active: []domainearnrule.EarnRule{
		{ID: 1, Type: domainearnrule.TypeTierMultiplier, TierID: uintPtr(1), Multiplier: 1.5},
	}}
	uc := New(&fakeLedger{}, nil, WithEarnRules(rules))

	result, err := uc.Calculate(context.Background(), CalculateInput{TierID: 1, BasePoints: 3})
	require.NoError(t, err)
	require.EqualValues(t, 4, result.Points, "3 × 1.5 = 4.5 floors to 4")
}

func TestCalculateWithoutRulesReturnsTheBasePoints(t *testing.T) {
	uc := New(&fakeLedger{}, nil, WithEarnRules(&fakeRules{}))
	result, err := uc.Calculate(context.Background(), CalculateInput{TierID: 1, BasePoints: 42})
	require.NoError(t, err)
	require.EqualValues(t, 42, result.Points)
	require.Empty(t, result.Rules)
}

func TestCalculateValidation(t *testing.T) {
	uc := New(&fakeLedger{}, nil, WithEarnRules(&fakeRules{}))
	ctx := context.Background()

	_, err := uc.Calculate(ctx, CalculateInput{TierID: 0, BasePoints: 10})
	require.ErrorIs(t, err, domain.ErrValidation)

	_, err = uc.Calculate(ctx, CalculateInput{TierID: 1, BasePoints: -1})
	require.ErrorIs(t, err, domain.ErrValidation)

	_, err = uc.Calculate(ctx, CalculateInput{TierID: 1, BasePoints: maxBasePoints + 1})
	require.ErrorIs(t, err, domain.ErrValidation)
}

func TestListEarnRulesRejectsAnUnknownType(t *testing.T) {
	uc := New(&fakeLedger{}, nil, WithEarnRules(&fakeRules{}))
	_, err := uc.ListEarnRules(context.Background(), domainearnrule.Query{Type: "product_code"})
	require.ErrorIs(t, err, domain.ErrValidation)
}

func TestCreateEarnRuleValidatesPerType(t *testing.T) {
	ctx := context.Background()
	rules := &fakeRules{byID: map[uint]*domainearnrule.EarnRule{}}
	var created *domainearnrule.EarnRule
	rules.create = func(rule *domainearnrule.EarnRule) { created = rule }
	uc := New(&fakeLedger{}, nil, WithEarnRules(rules))

	// Missing tier: rejected before the repository is touched.
	_, err := uc.CreateEarnRule(ctx, owner(), EarnRuleInput{
		Type: domainearnrule.TypeTierMultiplier, Multiplier: 1.5,
	})
	require.ErrorIs(t, err, domain.ErrValidation)
	require.Nil(t, created)

	rule, err := uc.CreateEarnRule(ctx, owner(), EarnRuleInput{
		Type: domainearnrule.TypeBonusFixed, Trigger: domainearnrule.TriggerSignup,
		Points: 100, IsActive: boolPtr(false),
	})
	require.NoError(t, err)
	require.Equal(t, uint(1), rule.CreatedBy)
	require.False(t, rule.IsActive, "an explicit toggle wins over the active default")
	require.Equal(t, 1.0, rule.Multiplier, "Normalize resets the unused multiplier")
	require.Same(t, created, rule)
}

func TestUpdateEarnRuleKeepsTheToggleWhenOmitted(t *testing.T) {
	ctx := context.Background()
	existing := &domainearnrule.EarnRule{
		ID: 5, Type: domainearnrule.TypeCampaignMultiplier, Multiplier: 2, IsActive: true,
	}
	existing.Normalize()
	rules := &fakeRules{byID: map[uint]*domainearnrule.EarnRule{5: existing}}
	var updated *domainearnrule.EarnRule
	rules.update = func(rule *domainearnrule.EarnRule) { updated = rule }
	uc := New(&fakeLedger{}, nil, WithEarnRules(rules))

	rule, err := uc.UpdateEarnRule(ctx, owner(), 5, EarnRuleInput{
		Type: domainearnrule.TypeCampaignMultiplier, Multiplier: 3,
	})
	require.NoError(t, err)
	require.True(t, rule.IsActive)
	require.Equal(t, 3.0, rule.Multiplier)
	require.Same(t, updated, rule)

	_, err = uc.UpdateEarnRule(ctx, owner(), 999, EarnRuleInput{Type: domainearnrule.TypeCampaignMultiplier, Multiplier: 2})
	require.ErrorIs(t, err, domain.ErrNotFound)
}

func TestEarnUsesTheCalculatorAndTheExpiryPolicy(t *testing.T) {
	at := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	rules := &fakeRules{active: []domainearnrule.EarnRule{
		{ID: 11, Type: domainearnrule.TypeCampaignMultiplier, Multiplier: 2, Stackable: true},
	}}
	members := &fakeMembers{member: &domainmember.Member{ID: 7, TierID: 2}}
	ledger := &fakeLedger{}
	settings := &fakeSettings{values: map[string]string{
		domainsettings.KeyPointExpiry: `{"mode":"rolling_months","months":12}`,
	}}
	uc := New(ledger, settings, WithEarnRules(rules), WithMembers(members))

	entry, err := uc.Earn(context.Background(), EarnRequest{
		MemberID: 7, SourceType: domainpoints.SourceProductCode, SourceID: "code-9",
		BasePoints: 100, SKUID: "sku-a", Note: "earned", At: at,
	})
	require.NoError(t, err)
	require.EqualValues(t, 200, entry.Points)
	require.Equal(t, []uint{11}, ledger.lastEarn.RuleIDs)
	require.EqualValues(t, 200, ledger.lastEarn.Points)
	require.NotNil(t, ledger.lastEarn.ExpiresAt)
	require.Equal(t, at.AddDate(0, 12, 0).Unix(), ledger.lastEarn.ExpiresAt.Unix())
}

func TestEarnRecordsAZeroAwardWithoutALot(t *testing.T) {
	members := &fakeMembers{member: &domainmember.Member{ID: 7, TierID: 2}}
	ledger := &fakeLedger{}
	uc := New(ledger, &fakeSettings{values: map[string]string{
		domainsettings.KeyPointExpiry: `{"mode":"rolling_months","months":0}`,
	}}, WithEarnRules(&fakeRules{}), WithMembers(members))

	entry, err := uc.Earn(context.Background(), EarnRequest{
		MemberID: 7, SourceType: domainpoints.SourceProductCode, SourceID: "code-0", BasePoints: 0,
	})
	require.NoError(t, err)
	require.EqualValues(t, 0, entry.Points)
	require.Nil(t, ledger.lastEarn.ExpiresAt, "months 0 means the points never expire")
}

func TestEarnRequiresASourceAndMembers(t *testing.T) {
	ctx := context.Background()

	uc := New(&fakeLedger{}, nil, WithEarnRules(&fakeRules{}))
	_, err := uc.Earn(ctx, EarnRequest{MemberID: 1, BasePoints: 10})
	require.ErrorIs(t, err, errMembersNotConfigured)

	members := &fakeMembers{member: &domainmember.Member{ID: 1, TierID: 1}}
	uc = New(&fakeLedger{}, nil, WithEarnRules(&fakeRules{}), WithMembers(members))
	_, err = uc.Earn(ctx, EarnRequest{MemberID: 1, BasePoints: 10})
	require.ErrorIs(t, err, domain.ErrValidation, "source type and id are required")

	_, err = uc.Earn(ctx, EarnRequest{MemberID: 0, SourceType: "order", SourceID: "o-1"})
	require.ErrorIs(t, err, domain.ErrValidation)
}

func uintPtr(v uint) *uint { return &v }
func boolPtr(v bool) *bool { return &v }
