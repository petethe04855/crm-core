package points

import (
	"context"
	"testing"
	"time"

	"crm-core/domain"
	domainpoints "crm-core/domain/points"
	domainsettings "crm-core/domain/settings"

	"github.com/stretchr/testify/require"
)

type fakeLedger struct {
	adjustedToday int64
	redeemCount   int64
	lastCmd       domainpoints.AdjustCommand
	lastEarn      domainpoints.EarnCommand
	lastSpend     domainpoints.SpendCommand
	lastRefund    domainpoints.RefundCommand
	lastFrom      time.Time
	lastTo        time.Time
	expired       int
	err           error
}

func (f *fakeLedger) Adjust(_ context.Context, cmd domainpoints.AdjustCommand) (*domainpoints.PointLedger, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.lastCmd = cmd
	return &domainpoints.PointLedger{MemberID: cmd.MemberID, Points: cmd.Points, Note: cmd.Reason}, nil
}

func (f *fakeLedger) Earn(_ context.Context, cmd domainpoints.EarnCommand) (*domainpoints.PointLedger, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.lastEarn = cmd
	return &domainpoints.PointLedger{
		MemberID: cmd.MemberID, Type: domainpoints.TypeEarn, Points: cmd.Points,
		RuleIDs: cmd.RuleIDs, Note: cmd.Note,
	}, nil
}

func (f *fakeLedger) Spend(_ context.Context, cmd domainpoints.SpendCommand) (*domainpoints.PointLedger, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.lastSpend = cmd
	return &domainpoints.PointLedger{
		MemberID: cmd.MemberID, Type: domainpoints.TypeRedeem, Points: -cmd.Points, Note: cmd.Note,
	}, nil
}

func (f *fakeLedger) Refund(_ context.Context, cmd domainpoints.RefundCommand) (*domainpoints.PointLedger, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.lastRefund = cmd
	return &domainpoints.PointLedger{
		MemberID: cmd.MemberID, Type: domainpoints.TypeRefund, Points: cmd.Points, Note: cmd.Note,
	}, nil
}

func (f *fakeLedger) ExpireDue(context.Context, time.Time) (int, error) {
	return f.expired, f.err
}

func (f *fakeLedger) RedeemCountBetween(_ context.Context, _ uint, from, to time.Time) (int64, error) {
	f.lastFrom, f.lastTo = from, to
	return f.redeemCount, f.err
}

func (f *fakeLedger) List(context.Context, uint, domainpoints.Query) ([]domainpoints.PointLedger, int64, error) {
	return nil, 0, nil
}

func (f *fakeLedger) AdjustedAbsBetween(context.Context, uint, time.Time, time.Time) (int64, error) {
	return f.adjustedToday, nil
}

type fakeSettings struct{ values map[string]string }

func (f *fakeSettings) Get(_ context.Context, key string) (string, error) {
	v, ok := f.values[key]
	if !ok {
		return "", domain.ErrNotFound
	}
	return v, nil
}

func (f *fakeSettings) Put(_ context.Context, key, value string, _ uint) error {
	if f.values == nil {
		f.values = map[string]string{}
	}
	f.values[key] = value
	return nil
}

func owner() domain.Actor {
	return domain.Actor{ERPUserID: 1, Email: "owner@example.com", Roles: []string{"owner"}}
}
func admin() domain.Actor {
	return domain.Actor{ERPUserID: 2, Email: "admin@example.com", Roles: []string{"admin"}}
}

func TestAdjustValidation(t *testing.T) {
	uc := New(&fakeLedger{}, nil)
	ctx := context.Background()

	_, err := uc.Adjust(ctx, owner(), 1, AdjustRequest{Points: 0, Reason: "แก้ไข"})
	require.ErrorIs(t, err, domain.ErrValidation)

	_, err = uc.Adjust(ctx, owner(), 0, AdjustRequest{Points: 10, Reason: "แก้ไข"})
	require.ErrorIs(t, err, domain.ErrValidation)

	_, err = uc.Adjust(ctx, owner(), 1, AdjustRequest{Points: 10, Reason: "ok"})
	require.ErrorIs(t, err, domain.ErrValidation, "reason needs at least 3 characters")

	_, err = uc.Adjust(ctx, owner(), 1, AdjustRequest{Points: maxSingleAdjust + 1, Reason: "โอนแต้ม"})
	require.ErrorIs(t, err, domain.ErrValidation)
}

func TestAdjustOwnerIsNotCapped(t *testing.T) {
	ledger := &fakeLedger{adjustedToday: 999999}
	uc := New(ledger, &fakeSettings{values: map[string]string{
		domainsettings.KeyPointAdjustPolicy: `{"maxPerAdjust":100,"maxPerDay":100}`,
	}})

	entry, err := uc.Adjust(context.Background(), owner(), 7, AdjustRequest{Points: 5000, Reason: "ชดเชยลูกค้า"})
	require.NoError(t, err)
	require.EqualValues(t, 5000, entry.Points)
	require.EqualValues(t, 7, ledger.lastCmd.MemberID)
	require.Equal(t, domainpoints.SourceAdmin, ledger.lastCmd.SourceType)
	require.Equal(t, "owner@example.com", ledger.lastCmd.ActorEmail)
}

func TestAdjustAdminPerAdjustCap(t *testing.T) {
	uc := New(&fakeLedger{}, &fakeSettings{values: map[string]string{
		domainsettings.KeyPointAdjustPolicy: `{"maxPerAdjust":1000,"maxPerDay":5000}`,
	}})

	_, err := uc.Adjust(context.Background(), admin(), 7, AdjustRequest{Points: 1001, Reason: "ชดเชย"})
	require.ErrorIs(t, err, domain.ErrValidation)

	_, err = uc.Adjust(context.Background(), admin(), 7, AdjustRequest{Points: -1000, Reason: "หักแต้ม"})
	require.NoError(t, err, "negative adjustments of the same size are allowed")
}

func TestAdjustAdminPerDayCap(t *testing.T) {
	ledger := &fakeLedger{adjustedToday: 4500}
	uc := New(ledger, &fakeSettings{values: map[string]string{
		domainsettings.KeyPointAdjustPolicy: `{"maxPerAdjust":1000,"maxPerDay":5000}`,
	}})

	_, err := uc.Adjust(context.Background(), admin(), 7, AdjustRequest{Points: 600, Reason: "ชดเชย"})
	require.ErrorIs(t, err, domain.ErrValidation)

	_, err = uc.Adjust(context.Background(), admin(), 7, AdjustRequest{Points: 500, Reason: "ชดเชย"})
	require.NoError(t, err)
}

func TestPolicyFallsBackToDefaults(t *testing.T) {
	ctx := context.Background()

	p, err := New(&fakeLedger{}, nil).Policy(ctx)
	require.NoError(t, err)
	require.Equal(t, DefaultPolicy(), p)

	p, err = New(&fakeLedger{}, &fakeSettings{values: map[string]string{
		domainsettings.KeyPointAdjustPolicy: `not json`,
	}}).Policy(ctx)
	require.NoError(t, err)
	require.Equal(t, DefaultPolicy(), p)
}

func TestLedgerValidation(t *testing.T) {
	uc := New(&fakeLedger{}, nil)
	_, _, err := uc.Ledger(context.Background(), 0, domainpoints.Query{})
	require.ErrorIs(t, err, domain.ErrValidation)

	_, _, err = uc.Ledger(context.Background(), 1, domainpoints.Query{Type: "gift"})
	require.ErrorIs(t, err, domain.ErrValidation)
}

func TestBangkokDayBounds(t *testing.T) {
	// 2026-11-17 08:30 UTC is 15:30 in Bangkok, same calendar day.
	from, to := bangkokDayBounds(time.Date(2026, 11, 17, 8, 30, 0, 0, time.UTC))
	require.Equal(t, 17, from.Day())
	require.Equal(t, 0, from.Hour())
	require.Equal(t, 18, to.Day())
	require.Equal(t, time.Duration(24)*time.Hour, to.Sub(from))
}
