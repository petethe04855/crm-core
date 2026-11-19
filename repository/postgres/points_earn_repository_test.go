package postgres

import (
	"context"
	"testing"
	"time"

	"crm-core/domain"
	domainmember "crm-core/domain/member"
	domainpoints "crm-core/domain/points"

	"github.com/stretchr/testify/require"
)

func TestPointsRepositoryEarnCreatesLedgerAndLot(t *testing.T) {
	db := newTestDB(t)
	tier := defaultTier(t, db)
	m := seedMember(t, db, "M2611-000040", "Earn", "earn@example.com", domainmember.StatusActive, time.Now(), tier.ID)
	repo := NewPointsRepository(db)
	ctx := context.Background()
	expires := time.Now().AddDate(1, 0, 0)

	entry, err := repo.Earn(ctx, domainpoints.EarnCommand{
		MemberID: m.ID, Points: 30, SourceType: domainpoints.SourceProductCode, SourceID: "code-1",
		RuleIDs: []uint{1, 2}, Note: "earned", ExpiresAt: &expires,
	})
	require.NoError(t, err)
	require.EqualValues(t, 30, entry.Points)
	require.EqualValues(t, 30, entry.BalanceAfter)
	require.Equal(t, []uint{1, 2}, entry.RuleIDs)

	var lot domainpoints.PointLot
	require.NoError(t, db.Where("ledger_id = ?", entry.ID).First(&lot).Error)
	require.EqualValues(t, 30, lot.PointsEarned)
	require.EqualValues(t, 30, lot.PointsRemaining)
	require.NotNil(t, lot.ExpiresAt)

	var stored domainmember.Member
	require.NoError(t, db.First(&stored, m.ID).Error)
	require.EqualValues(t, 30, stored.PointBalance)

	// The same source may never be credited twice.
	_, err = repo.Earn(ctx, domainpoints.EarnCommand{
		MemberID: m.ID, Points: 30, SourceType: domainpoints.SourceProductCode, SourceID: "code-1",
	})
	require.ErrorIs(t, err, domain.ErrConflict)

	var count int64
	require.NoError(t, db.Model(&domainpoints.PointLot{}).Where("member_id = ?", m.ID).Count(&count).Error)
	require.EqualValues(t, 1, count, "the retry creates no second lot")
}

func TestPointsRepositorySpendConsumesLotsFIFO(t *testing.T) {
	db := newTestDB(t)
	tier := defaultTier(t, db)
	m := seedMember(t, db, "M2611-000041", "Spend", "spend@example.com", domainmember.StatusActive, time.Now(), tier.ID)
	repo := NewPointsRepository(db)
	ctx := context.Background()

	soon := time.Now().AddDate(0, 1, 0)
	later := time.Now().AddDate(0, 6, 0)
	// The later-earned lot expires sooner, so it must be consumed first.
	_, err := repo.Earn(ctx, domainpoints.EarnCommand{
		MemberID: m.ID, Points: 40, SourceType: domainpoints.SourceOrder, SourceID: "o-1", ExpiresAt: &later,
	})
	require.NoError(t, err)
	_, err = repo.Earn(ctx, domainpoints.EarnCommand{
		MemberID: m.ID, Points: 60, SourceType: domainpoints.SourceOrder, SourceID: "o-2", ExpiresAt: &soon,
	})
	require.NoError(t, err)

	_, err = repo.Spend(ctx, domainpoints.SpendCommand{
		MemberID: m.ID, Points: 70, SourceType: domainpoints.SourceRedemption, SourceID: "r-1",
	})
	require.NoError(t, err)

	var lots []domainpoints.PointLot
	require.NoError(t, db.Where("member_id = ?", m.ID).Order("id ASC").Find(&lots).Error)
	require.Len(t, lots, 2)
	// lots[0] was earned first but expires later; lots[1] expires sooner.
	require.EqualValues(t, 30, lots[0].PointsRemaining)
	require.EqualValues(t, 0, lots[1].PointsRemaining, "the early-expiring lot was exhausted first")

	var stored domainmember.Member
	require.NoError(t, db.First(&stored, m.ID).Error)
	require.EqualValues(t, 30, stored.PointBalance)
}

func TestPointsRepositorySpendNeverGoesNegative(t *testing.T) {
	db := newTestDB(t)
	tier := defaultTier(t, db)
	m := seedMember(t, db, "M2611-000042", "Guard", "guard@example.com", domainmember.StatusActive, time.Now(), tier.ID)
	repo := NewPointsRepository(db)
	ctx := context.Background()

	_, err := repo.Earn(ctx, domainpoints.EarnCommand{
		MemberID: m.ID, Points: 100, SourceType: domainpoints.SourceOrder, SourceID: "o-1",
	})
	require.NoError(t, err)

	// Two requests both saw 100 points; the second re-checks under the lock.
	_, err = repo.Spend(ctx, domainpoints.SpendCommand{
		MemberID: m.ID, Points: 80, SourceType: domainpoints.SourceRedemption, SourceID: "r-1",
	})
	require.NoError(t, err)
	_, err = repo.Spend(ctx, domainpoints.SpendCommand{
		MemberID: m.ID, Points: 80, SourceType: domainpoints.SourceRedemption, SourceID: "r-2",
	})
	require.ErrorIs(t, err, domain.ErrInsufficientPoints)

	var stored domainmember.Member
	require.NoError(t, db.First(&stored, m.ID).Error)
	require.EqualValues(t, 20, stored.PointBalance)

	var redeems int64
	require.NoError(t, db.Model(&domainpoints.PointLedger{}).
		Where("member_id = ? AND type = ?", m.ID, domainpoints.TypeRedeem).
		Count(&redeems).Error)
	require.EqualValues(t, 1, redeems, "the rejected spend leaves no ledger row")
}

func TestPointsRepositorySpendCountsForTheDailyCap(t *testing.T) {
	db := newTestDB(t)
	tier := defaultTier(t, db)
	m := seedMember(t, db, "M2611-000043", "Daily", "daily@example.com", domainmember.StatusActive, time.Now(), tier.ID)
	repo := NewPointsRepository(db)
	ctx := context.Background()

	_, err := repo.Earn(ctx, domainpoints.EarnCommand{
		MemberID: m.ID, Points: 100, SourceType: domainpoints.SourceOrder, SourceID: "o-1",
	})
	require.NoError(t, err)
	for _, id := range []string{"r-1", "r-2"} {
		_, err = repo.Spend(ctx, domainpoints.SpendCommand{
			MemberID: m.ID, Points: 10, SourceType: domainpoints.SourceRedemption, SourceID: id,
		})
		require.NoError(t, err)
	}

	from := time.Now().Add(-time.Hour)
	to := time.Now().Add(time.Hour)
	n, err := repo.RedeemCountBetween(ctx, m.ID, from, to)
	require.NoError(t, err)
	require.EqualValues(t, 2, n)

	n, err = repo.RedeemCountBetween(ctx, m.ID, to, to.Add(time.Hour))
	require.NoError(t, err)
	require.EqualValues(t, 0, n)
}

func TestPointsRepositoryRefundStartsAFreshLot(t *testing.T) {
	db := newTestDB(t)
	tier := defaultTier(t, db)
	m := seedMember(t, db, "M2611-000044", "Refund", "refund@example.com", domainmember.StatusActive, time.Now(), tier.ID)
	repo := NewPointsRepository(db)
	ctx := context.Background()

	_, err := repo.Earn(ctx, domainpoints.EarnCommand{
		MemberID: m.ID, Points: 50, SourceType: domainpoints.SourceOrder, SourceID: "o-1",
	})
	require.NoError(t, err)
	_, err = repo.Spend(ctx, domainpoints.SpendCommand{
		MemberID: m.ID, Points: 50, SourceType: domainpoints.SourceRedemption, SourceID: "r-1",
	})
	require.NoError(t, err)

	newExpiry := time.Now().AddDate(1, 0, 0)
	entry, err := repo.Refund(ctx, domainpoints.RefundCommand{
		MemberID: m.ID, Points: 50, SourceType: domainpoints.SourceRedemption, SourceID: "r-1",
		Note: "cancelled", ExpiresAt: &newExpiry,
	})
	require.NoError(t, err)
	require.Equal(t, domainpoints.TypeRefund, entry.Type)
	require.EqualValues(t, 50, entry.BalanceAfter)

	var lot domainpoints.PointLot
	require.NoError(t, db.Where("ledger_id = ?", entry.ID).First(&lot).Error)
	require.EqualValues(t, 50, lot.PointsRemaining)
	require.Equal(t, newExpiry.Unix(), lot.ExpiresAt.Unix())
}

func TestPointsRepositoryExpireDueIsIdempotent(t *testing.T) {
	db := newTestDB(t)
	tier := defaultTier(t, db)
	m := seedMember(t, db, "M2611-000045", "Expire", "expire@example.com", domainmember.StatusActive, time.Now(), tier.ID)
	repo := NewPointsRepository(db)
	ctx := context.Background()

	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)
	_, err := repo.Earn(ctx, domainpoints.EarnCommand{
		MemberID: m.ID, Points: 40, SourceType: domainpoints.SourceOrder, SourceID: "o-old", ExpiresAt: &past,
	})
	require.NoError(t, err)
	_, err = repo.Earn(ctx, domainpoints.EarnCommand{
		MemberID: m.ID, Points: 25, SourceType: domainpoints.SourceOrder, SourceID: "o-new", ExpiresAt: &future,
	})
	require.NoError(t, err)

	n, err := repo.ExpireDue(ctx, time.Now())
	require.NoError(t, err)
	require.Equal(t, 1, n, "one lot was due")

	var stored domainmember.Member
	require.NoError(t, db.First(&stored, m.ID).Error)
	require.EqualValues(t, 25, stored.PointBalance)

	var expireRows []domainpoints.PointLedger
	require.NoError(t, db.Where("member_id = ? AND type = ?", m.ID, domainpoints.TypeExpire).Find(&expireRows).Error)
	require.Len(t, expireRows, 1)
	require.EqualValues(t, -40, expireRows[0].Points)
	require.EqualValues(t, 25, expireRows[0].BalanceAfter, "65 earned − 40 expired")

	// Running again is a no-op: the lot is empty and the unique source blocks a
	// duplicate even if it were not.
	n, err = repo.ExpireDue(ctx, time.Now())
	require.NoError(t, err)
	require.Zero(t, n)

	require.NoError(t, db.First(&stored, m.ID).Error)
	require.EqualValues(t, 25, stored.PointBalance)
	var all int64
	require.NoError(t, db.Model(&domainpoints.PointLedger{}).
		Where("member_id = ? AND type = ?", m.ID, domainpoints.TypeExpire).Count(&all).Error)
	require.EqualValues(t, 1, all)
}
