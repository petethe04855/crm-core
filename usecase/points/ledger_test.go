package points

import (
	"context"
	"testing"
	"time"

	"crm-core/domain"
	domainmember "crm-core/domain/member"
	domainpoints "crm-core/domain/points"
	domainsettings "crm-core/domain/settings"

	"github.com/stretchr/testify/require"
)

func TestSpendRunsTheGuardBeforeDeducting(t *testing.T) {
	at := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	member := testMember(at.AddDate(0, 0, -30))
	ledger := &fakeLedger{}
	uc := New(ledger, nil, WithMembers(&fakeMembers{member: member}))

	entry, err := uc.Spend(context.Background(), SpendRequest{
		MemberID: member.ID, Points: 100,
		SourceType: domainpoints.SourceRedemption, SourceID: "r-1", At: at,
	})
	require.NoError(t, err)
	require.EqualValues(t, -100, entry.Points, "the ledger row stores the negative movement")
	require.EqualValues(t, 100, ledger.lastSpend.Points, "the command carries the positive cost")
	require.Equal(t, domainpoints.SourceRedemption, ledger.lastSpend.SourceType)

	// A member the guard rejects never reaches the repository.
	member.Status = domainmember.StatusSuspended
	_, err = uc.Spend(context.Background(), SpendRequest{
		MemberID: member.ID, Points: 100,
		SourceType: domainpoints.SourceRedemption, SourceID: "r-2", At: at,
	})
	require.ErrorIs(t, err, domain.ErrMemberNotAllowed)
	require.Equal(t, "r-1", ledger.lastSpend.SourceID)
}

func TestSpendInTxSkipsTheGuard(t *testing.T) {
	at := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	ledger := &fakeLedger{}
	// No WithMembers: SpendInTx must not need the guard's member lookup.
	uc := New(&fakeLedger{}, nil)

	entry, err := uc.SpendInTx(context.Background(), ledger, SpendRequest{
		MemberID: 7, Points: 50,
		SourceType: domainpoints.SourceRedemption, SourceID: "rw-1", At: at,
	})
	require.NoError(t, err)
	require.EqualValues(t, -50, entry.Points)
	require.EqualValues(t, 50, ledger.lastSpend.Points)
}

func TestSpendRejectsBadInput(t *testing.T) {
	ctx := context.Background()
	uc := New(&fakeLedger{}, nil, WithMembers(&fakeMembers{member: &domainmember.Member{ID: 7}}))

	_, err := uc.SpendInTx(ctx, &fakeLedger{}, SpendRequest{MemberID: 7, Points: 0, SourceType: "redemption", SourceID: "r-1"})
	require.ErrorIs(t, err, domain.ErrValidation)

	_, err = uc.SpendInTx(ctx, &fakeLedger{}, SpendRequest{MemberID: 7, Points: 10, SourceID: "r-1"})
	require.ErrorIs(t, err, domain.ErrValidation)

	_, err = uc.SpendInTx(ctx, nil, SpendRequest{MemberID: 7, Points: 10, SourceType: "redemption", SourceID: "r-1"})
	require.ErrorIs(t, err, errLedgerNotConfigured)

	_, err = uc.Spend(ctx, SpendRequest{MemberID: 0, Points: 10, SourceType: "redemption", SourceID: "r-1"})
	require.ErrorIs(t, err, domain.ErrValidation)
}

func TestRefundStartsANewExpiryWindow(t *testing.T) {
	at := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	ledger := &fakeLedger{}
	uc := New(ledger, &fakeSettings{values: map[string]string{
		domainsettings.KeyPointExpiry: `{"mode":"rolling_months","months":6}`,
	}})

	entry, err := uc.Refund(context.Background(), RefundRequest{
		MemberID: 7, Points: 80, SourceType: domainpoints.SourceRedemption, SourceID: "r-1",
		Note: "cancelled", At: at,
	})
	require.NoError(t, err)
	require.Equal(t, domainpoints.TypeRefund, entry.Type)
	require.EqualValues(t, 80, entry.Points)
	require.NotNil(t, ledger.lastRefund.ExpiresAt)
	require.Equal(t, at.AddDate(0, 6, 0).Unix(), ledger.lastRefund.ExpiresAt.Unix())
}

func TestRefundWithAnUnlimitedPolicyOpensALotWithoutExpiry(t *testing.T) {
	ledger := &fakeLedger{}
	uc := New(ledger, &fakeSettings{values: map[string]string{
		domainsettings.KeyPointExpiry: `{"mode":"rolling_months","months":0}`,
	}})

	_, err := uc.RefundInTx(context.Background(), ledger, RefundRequest{
		MemberID: 7, Points: 80, SourceType: domainpoints.SourceRedemption, SourceID: "r-1",
	})
	require.NoError(t, err)
	require.Nil(t, ledger.lastRefund.ExpiresAt)
}

func TestExpireDueDefaultsToNowAndDelegates(t *testing.T) {
	ledger := &fakeLedger{expired: 3}
	uc := New(ledger, nil)

	n, err := uc.ExpireDue(context.Background(), time.Time{})
	require.NoError(t, err)
	require.Equal(t, 3, n)
}
