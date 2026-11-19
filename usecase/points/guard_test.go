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

func redeemSettings(policy string) *fakeSettings {
	return &fakeSettings{values: map[string]string{domainsettings.KeyRedeemPolicy: policy}}
}

func testMember(createdAt time.Time) *domainmember.Member {
	verified := createdAt.Add(time.Hour)
	return &domainmember.Member{
		ID: 7, Status: domainmember.StatusActive, EmailVerifiedAt: &verified,
		CreatedAt: createdAt, PointBalance: 500,
	}
}

func TestCheckDefaultsAllowAnActiveMember(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	uc := New(&fakeLedger{}, nil)
	require.NoError(t, uc.Check(context.Background(), testMember(now.AddDate(0, 0, -30)), 100, now))
}

func TestCheckRejectsDisallowedStatuses(t *testing.T) {
	now := time.Now()
	member := testMember(now.AddDate(0, 0, -30))
	member.Status = domainmember.StatusSuspended

	uc := New(&fakeLedger{}, redeemSettings(`{"allowedStatuses":["active"]}`))
	err := uc.Check(context.Background(), member, 100, now)
	require.ErrorIs(t, err, domain.ErrMemberNotAllowed)

	// The default policy is the same list, so an unset row also rejects it.
	uc = New(&fakeLedger{}, nil)
	require.ErrorIs(t, uc.Check(context.Background(), member, 100, now), domain.ErrMemberNotAllowed)
}

func TestCheckRequiresAVerifiedEmail(t *testing.T) {
	now := time.Now()
	member := testMember(now.AddDate(0, 0, -30))
	member.EmailVerifiedAt = nil

	uc := New(&fakeLedger{}, redeemSettings(`{"requireEmailVerified":true,"allowedStatuses":["active"]}`))
	require.ErrorIs(t, uc.Check(context.Background(), member, 100, now), domain.ErrEmailNotVerified)

	uc = New(&fakeLedger{}, nil)
	require.NoError(t, uc.Check(context.Background(), member, 100, now),
		"the default policy does not require a verified email")
}

func TestCheckUsesTwentyFourHourMemberAge(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	uc := New(&fakeLedger{}, redeemSettings(`{"minMemberAgeDays":1,"allowedStatuses":["active"]}`))

	// Registered 23 hours ago: not yet one full day old even though it was
	// yesterday on the calendar.
	require.ErrorIs(t,
		uc.Check(context.Background(), testMember(now.Add(-23*time.Hour)), 100, now),
		domain.ErrMemberTooNew)
	require.NoError(t,
		uc.Check(context.Background(), testMember(now.Add(-25*time.Hour)), 100, now))
}

func TestCheckEnforcesTheDailyCapInBangkokDays(t *testing.T) {
	// 2026-11-17 23:30 in Bangkok is 16:30 UTC.
	at := time.Date(2026, 11, 17, 16, 30, 0, 0, time.UTC)
	ledger := &fakeLedger{redeemCount: 3}
	uc := New(ledger, redeemSettings(`{"maxRedemptionsPerDay":3,"allowedStatuses":["active"]}`))

	err := uc.Check(context.Background(), testMember(at.AddDate(0, 0, -30)), 100, at)
	require.ErrorIs(t, err, domain.ErrDailyLimitReached)

	// The count covered the Thai calendar day, not the UTC one.
	require.Equal(t, "2026-11-17", ledger.lastFrom.In(domainpoints.Bangkok).Format("2006-01-02"))
	require.Equal(t, "2026-11-18", ledger.lastTo.In(domainpoints.Bangkok).Format("2006-01-02"))

	// One less redemption today still passes.
	ledger.redeemCount = 2
	require.NoError(t, uc.Check(context.Background(), testMember(at.AddDate(0, 0, -30)), 100, at))
}

func TestCheckRejectsAnInsufficientBalance(t *testing.T) {
	now := time.Now()
	member := testMember(now.AddDate(0, 0, -30))
	member.PointBalance = 50

	uc := New(&fakeLedger{}, nil)
	require.ErrorIs(t, uc.Check(context.Background(), member, 100, now), domain.ErrInsufficientPoints)

	member.PointBalance = 100
	require.NoError(t, uc.Check(context.Background(), member, 100, now), "exactly enough is enough")
}

func TestCheckRejectsABalanceBelowTheMinimum(t *testing.T) {
	now := time.Now()
	member := testMember(now.AddDate(0, 0, -30))
	member.PointBalance = 500

	uc := New(&fakeLedger{}, redeemSettings(`{"minBalanceToRedeem":1000,"allowedStatuses":["active"]}`))
	require.ErrorIs(t, uc.Check(context.Background(), member, 100, now), domain.ErrInsufficientPoints)
}

func TestCheckValidatesInput(t *testing.T) {
	uc := New(&fakeLedger{}, nil)
	ctx := context.Background()

	require.ErrorIs(t, uc.Check(ctx, nil, 10, time.Now()), domain.ErrNotFound)
	require.ErrorIs(t, uc.Check(ctx, &domainmember.Member{}, 10, time.Now()), domain.ErrNotFound)
	require.ErrorIs(t, uc.Check(ctx, testMember(time.Now().AddDate(0, 0, -1)), 0, time.Now()), domain.ErrValidation)
}

func TestCheckRunsTheCheapestRejectionFirst(t *testing.T) {
	now := time.Now()
	member := testMember(now.AddDate(0, 0, -30))
	member.Status = domainmember.StatusSuspended
	member.PointBalance = 0

	ledger := &fakeLedger{redeemCount: 99}
	uc := New(ledger, redeemSettings(`{"requireEmailVerified":true,"maxRedemptionsPerDay":1,"allowedStatuses":["active"]}`))
	require.ErrorIs(t, uc.Check(context.Background(), member, 100, now), domain.ErrMemberNotAllowed)
	require.True(t, ledger.lastFrom.IsZero(), "a rejected member is never counted for the day")
}
