package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	"crm-core/domain"
	domainmember "crm-core/domain/member"
	domainpoints "crm-core/domain/points"

	"github.com/stretchr/testify/require"
)

func TestPointsRepositoryAdjustKeepsLedgerAndBalanceInSync(t *testing.T) {
	db := newTestDB(t)
	tier := defaultTier(t, db)
	m := seedMember(t, db, "M2611-000020", "Points", "points@example.com", domainmember.StatusActive, time.Now(), tier.ID)
	repo := NewPointsRepository(db)
	ctx := context.Background()

	first, err := repo.Adjust(ctx, domainpoints.AdjustCommand{
		MemberID: m.ID, Points: 120, Reason: "ชดเชยลูกค้า", ActorID: 1, ActorEmail: "owner@example.com",
		SourceType: domainpoints.SourceAdmin, SourceID: "manual-1",
	})
	require.NoError(t, err)
	require.EqualValues(t, 120, first.BalanceAfter)
	require.Equal(t, domainpoints.TypeAdjust, first.Type)

	second, err := repo.Adjust(ctx, domainpoints.AdjustCommand{
		MemberID: m.ID, Points: -20, Reason: "แก้ไขน้ำหนัก", ActorID: 1,
		SourceType: domainpoints.SourceAdmin, SourceID: "manual-2",
	})
	require.NoError(t, err)
	require.EqualValues(t, 100, second.BalanceAfter)

	var stored domainmember.Member
	require.NoError(t, db.First(&stored, m.ID).Error)
	require.EqualValues(t, 100, stored.PointBalance, "cached balance must match the ledger")

	// A negative adjustment may never take the balance below zero.
	_, err = repo.Adjust(ctx, domainpoints.AdjustCommand{
		MemberID: m.ID, Points: -101, Reason: "หักเกินยอด", ActorID: 1,
		SourceType: domainpoints.SourceAdmin, SourceID: "manual-3",
	})
	require.ErrorIs(t, err, domain.ErrInsufficientPoints)

	var entries int64
	require.NoError(t, db.Model(&domainpoints.PointLedger{}).Where("member_id = ?", m.ID).Count(&entries).Error)
	require.EqualValues(t, 2, entries, "rejected adjustments leave no ledger row")
}

func TestPointsRepositoryRetriesAreIdempotent(t *testing.T) {
	db := newTestDB(t)
	tier := defaultTier(t, db)
	m := seedMember(t, db, "M2611-000021", "Retry", "retry@example.com", domainmember.StatusActive, time.Now(), tier.ID)
	repo := NewPointsRepository(db)
	ctx := context.Background()

	cmd := domainpoints.AdjustCommand{
		MemberID: m.ID, Points: 10, Reason: "ให้แต้มจากรหัส", ActorID: 2,
		SourceType: domainpoints.SourceProductCode, SourceID: "same-source",
	}
	_, err := repo.Adjust(ctx, cmd)
	require.NoError(t, err)
	_, err = repo.Adjust(ctx, cmd)
	require.ErrorIs(t, err, domain.ErrConflict)

	var stored domainmember.Member
	require.NoError(t, db.First(&stored, m.ID).Error)
	require.EqualValues(t, 10, stored.PointBalance, "the duplicate attempt must not double-credit")
}

func TestPointsRepositoryListAndDailySum(t *testing.T) {
	db := newTestDB(t)
	tier := defaultTier(t, db)
	m := seedMember(t, db, "M2611-000022", "List", "list@example.com", domainmember.StatusActive, time.Now(), tier.ID)
	repo := NewPointsRepository(db)
	ctx := context.Background()

	for i, pts := range []int64{10, 20, -5} {
		_, err := repo.Adjust(ctx, domainpoints.AdjustCommand{
			MemberID: m.ID, Points: pts, Reason: "ทดสอบรายการ", ActorID: 4,
			SourceType: domainpoints.SourceAdmin, SourceID: fmt.Sprintf("manual-list-%d", i),
		})
		require.NoError(t, err)
	}

	rows, total, err := repo.List(ctx, m.ID, domainpoints.Query{Page: 1, Limit: 2})
	require.NoError(t, err)
	require.EqualValues(t, 3, total)
	require.Len(t, rows, 2)

	rows, total, err = repo.List(ctx, m.ID, domainpoints.Query{Type: domainpoints.TypeAdjust, Page: 1, Limit: 20})
	require.NoError(t, err)
	require.EqualValues(t, 3, total)
	require.Len(t, rows, 3)

	from := time.Now().Add(-time.Hour)
	to := time.Now().Add(time.Hour)
	sum, err := repo.AdjustedAbsBetween(ctx, 4, from, to)
	require.NoError(t, err)
	require.EqualValues(t, 35, sum, "sum of |points| = 10 + 20 + 5")

	sum, err = repo.AdjustedAbsBetween(ctx, 99, from, to)
	require.NoError(t, err)
	require.EqualValues(t, 0, sum)
}

func TestMemberRepositoryManagement(t *testing.T) {
	db := newTestDB(t)
	tier := defaultTier(t, db)
	base := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	old := seedMember(t, db, "M2611-000030", "Old", "old@example.com", domainmember.StatusActive, base, tier.ID)
	require.NoError(t, db.Model(&domainmember.Member{}).Where("id = ?", old.ID).Update("point_balance", 50).Error)
	seedMember(t, db, "M2611-000031", "New", "new@example.com", domainmember.StatusActive, time.Now(), tier.ID)

	repo := NewMemberRepository(db)
	ctx := context.Background()

	t.Run("points range", func(t *testing.T) {
		min, max := int64(1), int64(100)
		got, total, err := repo.FindAll(ctx, domainmember.Query{MinPoints: &min, MaxPoints: &max, Page: 1, Limit: 20})
		require.NoError(t, err)
		require.EqualValues(t, 1, total)
		require.Equal(t, "M2611-000030", got[0].MemberCode)
	})

	t.Run("inactive before", func(t *testing.T) {
		cutoff := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
		got, total, err := repo.FindAll(ctx, domainmember.Query{InactiveBefore: &cutoff, Page: 1, Limit: 20})
		require.NoError(t, err)
		require.EqualValues(t, 1, total)
		require.Equal(t, "M2611-000030", got[0].MemberCode)
	})

	t.Run("status", func(t *testing.T) {
		require.NoError(t, repo.UpdateStatus(ctx, old.ID, domainmember.StatusSuspended))
		m, err := repo.FindByID(ctx, old.ID)
		require.NoError(t, err)
		require.Equal(t, domainmember.StatusSuspended, m.Status)

		require.ErrorIs(t, repo.UpdateStatus(ctx, 9999, domainmember.StatusActive), domain.ErrNotFound)
	})

	t.Run("tags", func(t *testing.T) {
		require.NoError(t, repo.AddTag(ctx, &domainmember.MemberTag{MemberID: old.ID, Tag: "vip", CreatedBy: 1}))
		require.ErrorIs(t, repo.AddTag(ctx, &domainmember.MemberTag{MemberID: old.ID, Tag: "vip", CreatedBy: 1}), domain.ErrConflict)

		tags, err := repo.FindTags(ctx, old.ID)
		require.NoError(t, err)
		require.Len(t, tags, 1)

		require.NoError(t, repo.RemoveTag(ctx, old.ID, "vip"))
		require.ErrorIs(t, repo.RemoveTag(ctx, old.ID, "vip"), domain.ErrNotFound)
	})

	t.Run("notes", func(t *testing.T) {
		require.NoError(t, repo.AddNote(ctx, &domainmember.MemberNote{MemberID: old.ID, Body: "โน้ตที่หนึ่ง", CreatedBy: 1}))
		require.NoError(t, repo.AddNote(ctx, &domainmember.MemberNote{MemberID: old.ID, Body: "โน้ตที่สอง", CreatedBy: 1}))

		notes, total, err := repo.FindNotes(ctx, old.ID, 1, 1)
		require.NoError(t, err)
		require.EqualValues(t, 2, total)
		require.Len(t, notes, 1)
		require.Equal(t, "โน้ตที่สอง", notes[0].Body, "newest first")
	})

	t.Run("unlink line", func(t *testing.T) {
		require.ErrorIs(t, repo.UnlinkActiveLineAccount(ctx, old.ID), domain.ErrNotFound)
		require.NoError(t, db.Create(&domainmember.MemberLineAccount{MemberID: old.ID, LineUserID: "U-x", LinkedAt: time.Now()}).Error)
		require.NoError(t, repo.UnlinkActiveLineAccount(ctx, old.ID))
		require.ErrorIs(t, repo.UnlinkActiveLineAccount(ctx, old.ID), domain.ErrNotFound)
	})
}

func TestSettingsRepositoryRoundTrip(t *testing.T) {
	db := newTestDB(t)
	repo := NewSettingsRepository(db)
	ctx := context.Background()

	_, err := repo.Get(ctx, "missing")
	require.ErrorIs(t, err, domain.ErrNotFound)

	require.NoError(t, repo.Put(ctx, "point_adjust_policy", `{"maxPerAdjust":1000}`, 1))
	require.NoError(t, repo.Put(ctx, "point_adjust_policy", `{"maxPerAdjust":2000}`, 2))

	v, err := repo.Get(ctx, "point_adjust_policy")
	require.NoError(t, err)
	require.JSONEq(t, `{"maxPerAdjust":2000}`, v)
}
