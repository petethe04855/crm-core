package points

import (
	"context"
	"testing"

	"crm-core/domain"
	domainsettings "crm-core/domain/settings"

	"github.com/stretchr/testify/require"
)

func TestRedeemPolicyDefaultsWhenTheRowIsMissingOrBroken(t *testing.T) {
	ctx := context.Background()

	p, err := New(&fakeLedger{}, nil).RedeemPolicy(ctx)
	require.NoError(t, err)
	require.Equal(t, DefaultRedeemPolicy(), p)

	p, err = New(&fakeLedger{}, redeemSettings(`not json`)).RedeemPolicy(ctx)
	require.NoError(t, err)
	require.Equal(t, DefaultRedeemPolicy(), p, "a broken row falls back instead of locking everyone out")
}

func TestRedeemPolicyNormalizesAHandEditedRow(t *testing.T) {
	uc := New(&fakeLedger{}, redeemSettings(
		`{"minBalanceToRedeem":-5,"minMemberAgeDays":7,"maxRedemptionsPerDay":3,"requireEmailVerified":true,`+
			`"allowedStatuses":[" Active ","active","banned"]}`))
	p, err := uc.RedeemPolicy(context.Background())
	require.NoError(t, err)
	require.Zero(t, p.MinBalanceToRedeem)
	require.Equal(t, 7, p.MinMemberAgeDays)
	require.Equal(t, 3, p.MaxRedemptionsPerDay)
	require.True(t, p.RequireEmailVerified)
	require.Equal(t, []string{"active"}, p.AllowedStatuses)
}

func TestPutRedeemPolicyValidatesAndRequiresTheOwner(t *testing.T) {
	ctx := context.Background()
	settings := &fakeSettings{}
	uc := New(&fakeLedger{}, settings)

	_, err := uc.PutRedeemPolicy(ctx, admin(), DefaultRedeemPolicy())
	require.ErrorIs(t, err, domain.ErrForbidden)

	_, err = uc.PutRedeemPolicy(ctx, owner(), RedeemPolicy{})
	require.ErrorIs(t, err, domain.ErrValidation, "at least one status is required")

	_, err = uc.PutRedeemPolicy(ctx, owner(), RedeemPolicy{
		AllowedStatuses: []string{"banned"},
	})
	require.ErrorIs(t, err, domain.ErrValidation)

	_, err = uc.PutRedeemPolicy(ctx, owner(), RedeemPolicy{
		MinMemberAgeDays: -1, AllowedStatuses: []string{"active"},
	})
	require.ErrorIs(t, err, domain.ErrValidation)

	want := RedeemPolicy{
		MinBalanceToRedeem: 100, MinMemberAgeDays: 7, MaxRedemptionsPerDay: 3,
		RequireEmailVerified: true, AllowedStatuses: []string{"Active"},
	}
	got, err := uc.PutRedeemPolicy(ctx, owner(), want)
	require.NoError(t, err)
	require.Equal(t, []string{"active"}, got.AllowedStatuses)
	require.JSONEq(t, `{"minBalanceToRedeem":100,"minMemberAgeDays":7,"maxRedemptionsPerDay":3,`+
		`"requireEmailVerified":true,"allowedStatuses":["active"]}`,
		settings.values[domainsettings.KeyRedeemPolicy])
}

func TestExpiryPolicyDefaultsAndRoundTrip(t *testing.T) {
	ctx := context.Background()

	p, err := New(&fakeLedger{}, nil).ExpiryPolicy(ctx)
	require.NoError(t, err)
	require.Equal(t, DefaultExpiryPolicy(), p)
	require.Equal(t, 12, p.Months)

	settings := &fakeSettings{}
	uc := New(&fakeLedger{}, settings)
	got, err := uc.PutExpiryPolicy(ctx, owner(), ExpiryPolicy{Months: 0})
	require.NoError(t, err)
	require.Equal(t, ExpiryModeRollingMonths, got.Mode, "an empty mode defaults to rolling months")
	require.Zero(t, got.Months, "zero months means points never expire")

	read, err := uc.ExpiryPolicy(ctx)
	require.NoError(t, err)
	require.Equal(t, got, read)
}

func TestPutExpiryPolicyValidationAndPermission(t *testing.T) {
	ctx := context.Background()
	uc := New(&fakeLedger{}, &fakeSettings{})

	_, err := uc.PutExpiryPolicy(ctx, admin(), DefaultExpiryPolicy())
	require.ErrorIs(t, err, domain.ErrForbidden)

	_, err = uc.PutExpiryPolicy(ctx, owner(), ExpiryPolicy{Mode: "calendar_year", Months: 12})
	require.ErrorIs(t, err, domain.ErrValidation)

	_, err = uc.PutExpiryPolicy(ctx, owner(), ExpiryPolicy{Months: 121})
	require.ErrorIs(t, err, domain.ErrValidation)
}

func TestPutAdjustPolicyValidationAndPermission(t *testing.T) {
	ctx := context.Background()
	settings := &fakeSettings{}
	uc := New(&fakeLedger{}, settings)

	_, err := uc.PutAdjustPolicy(ctx, admin(), DefaultPolicy())
	require.ErrorIs(t, err, domain.ErrForbidden)

	_, err = uc.PutAdjustPolicy(ctx, owner(), Policy{MaxPerAdjust: 0, MaxPerDay: 100})
	require.ErrorIs(t, err, domain.ErrValidation)

	_, err = uc.PutAdjustPolicy(ctx, owner(), Policy{MaxPerAdjust: 500, MaxPerDay: 100})
	require.ErrorIs(t, err, domain.ErrValidation)

	got, err := uc.PutAdjustPolicy(ctx, owner(), Policy{MaxPerAdjust: 500, MaxPerDay: 1000})
	require.NoError(t, err)
	require.Equal(t, Policy{MaxPerAdjust: 500, MaxPerDay: 1000}, got)
	require.NotEmpty(t, settings.values[domainsettings.KeyPointAdjustPolicy])
}
