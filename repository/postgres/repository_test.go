package postgres

import (
	"context"
	"testing"
	"time"

	"crm-core/database"
	"crm-core/domain"
	domainloyalty "crm-core/domain/loyalty"
	domainmember "crm-core/domain/member"
	domainstaffrole "crm-core/domain/staffrole"
	"crm-core/migrations"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), database.GormConfig("", logger.Silent))
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1) // keep a single in-memory database
	require.NoError(t, migrations.Run(db, ""))
	return db
}

func defaultTier(t *testing.T, db *gorm.DB) domainloyalty.Tier {
	t.Helper()
	var tier domainloyalty.Tier
	require.NoError(t, db.Where("code = ?", domainloyalty.DefaultTierCode).First(&tier).Error)
	return tier
}

func seedMember(t *testing.T, db *gorm.DB, code, first, email, status string, createdAt time.Time, tierID uint) domainmember.Member {
	t.Helper()
	m := domainmember.Member{
		MemberCode: code, FirstName: first, LastName: "Test", Email: email,
		Gender: domainmember.GenderUnspecified, TierID: tierID, Status: status,
		RegisteredChannel: "line", CreatedAt: createdAt, UpdatedAt: createdAt,
	}
	require.NoError(t, db.Create(&m).Error)
	return m
}

func TestMigrationsSeedDefaultTierOnce(t *testing.T) {
	db := newTestDB(t)
	require.NoError(t, migrations.Run(db, "")) // second run must not duplicate

	var count int64
	require.NoError(t, db.Model(&domainloyalty.Tier{}).Where("code = ?", domainloyalty.DefaultTierCode).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestMemberRepositoryFindAll(t *testing.T) {
	db := newTestDB(t)
	tier := defaultTier(t, db)
	base := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)

	seedMember(t, db, "M2611-000001", "Nopharat", "nop@example.com", domainmember.StatusActive, base, tier.ID)
	seedMember(t, db, "M2611-000002", "Somchai", "som@example.com", domainmember.StatusSuspended, base.Add(time.Hour), tier.ID)
	seedMember(t, db, "M2611-000003", "Deleted", "gone@example.com", domainmember.StatusDeleted, base.Add(2*time.Hour), tier.ID)
	seedMember(t, db, "M2611-000004", "Under_score", "under@example.com", domainmember.StatusActive, base.Add(3*time.Hour), tier.ID)

	repo := NewMemberRepository(db)
	ctx := context.Background()

	t.Run("default excludes deleted and orders newest first", func(t *testing.T) {
		got, total, err := repo.FindAll(ctx, domainmember.Query{Page: 1, Limit: 20})
		require.NoError(t, err)
		require.EqualValues(t, 3, total)
		require.Equal(t, "M2611-000004", got[0].MemberCode)
		require.Equal(t, "M2611-000001", got[2].MemberCode)
	})

	t.Run("status filter", func(t *testing.T) {
		got, total, err := repo.FindAll(ctx, domainmember.Query{Status: domainmember.StatusDeleted, Page: 1, Limit: 20})
		require.NoError(t, err)
		require.EqualValues(t, 1, total)
		require.Equal(t, "M2611-000003", got[0].MemberCode)
	})

	t.Run("search is case-insensitive across code, name and email", func(t *testing.T) {
		got, total, err := repo.FindAll(ctx, domainmember.Query{Search: "NOP", Page: 1, Limit: 20})
		require.NoError(t, err)
		require.EqualValues(t, 1, total)
		require.Equal(t, "nop@example.com", got[0].Email)

		_, total, err = repo.FindAll(ctx, domainmember.Query{Search: "000002", Page: 1, Limit: 20})
		require.NoError(t, err)
		require.EqualValues(t, 1, total)
	})

	t.Run("search treats LIKE wildcards literally", func(t *testing.T) {
		got, total, err := repo.FindAll(ctx, domainmember.Query{Search: "_", Page: 1, Limit: 20})
		require.NoError(t, err)
		require.EqualValues(t, 1, total)
		require.Equal(t, "Under_score", got[0].FirstName)
	})

	t.Run("registered date range", func(t *testing.T) {
		from := base.Add(30 * time.Minute)
		to := base.Add(90 * time.Minute)
		got, total, err := repo.FindAll(ctx, domainmember.Query{RegisteredFrom: &from, RegisteredTo: &to, Page: 1, Limit: 20})
		require.NoError(t, err)
		require.EqualValues(t, 1, total)
		require.Equal(t, "M2611-000002", got[0].MemberCode)
	})

	t.Run("paging", func(t *testing.T) {
		got, total, err := repo.FindAll(ctx, domainmember.Query{Page: 2, Limit: 2})
		require.NoError(t, err)
		require.EqualValues(t, 3, total)
		require.Len(t, got, 1)
	})
}

func TestMemberRepositoryFindByIDAndLineAccount(t *testing.T) {
	db := newTestDB(t)
	tier := defaultTier(t, db)
	m := seedMember(t, db, "M2611-000010", "Line", "line@example.com", domainmember.StatusActive, time.Now(), tier.ID)
	repo := NewMemberRepository(db)
	ctx := context.Background()

	got, err := repo.FindByID(ctx, m.ID)
	require.NoError(t, err)
	require.Equal(t, m.Email, got.Email)

	_, err = repo.FindByID(ctx, 9999)
	require.ErrorIs(t, err, domain.ErrNotFound)

	_, err = repo.FindActiveLineAccount(ctx, m.ID)
	require.ErrorIs(t, err, domain.ErrNotFound)

	unlinked := time.Now()
	require.NoError(t, db.Create(&domainmember.MemberLineAccount{MemberID: m.ID, LineUserID: "U-old", LinkedAt: time.Now().Add(-time.Hour), UnlinkedAt: &unlinked}).Error)
	require.NoError(t, db.Create(&domainmember.MemberLineAccount{MemberID: m.ID, LineUserID: "U-new", LinkedAt: time.Now()}).Error)

	acc, err := repo.FindActiveLineAccount(ctx, m.ID)
	require.NoError(t, err)
	require.Equal(t, "U-new", acc.LineUserID)
}

func TestStaffRoleRepository(t *testing.T) {
	db := newTestDB(t)
	repo := NewStaffRoleRepository(db)
	ctx := context.Background()

	_, err := repo.FindByERPUserID(ctx, 7)
	require.ErrorIs(t, err, domain.ErrNotFound)

	now := time.Now()
	require.NoError(t, repo.Upsert(ctx, &domainstaffrole.StaffRole{ERPUserID: 7, Email: "a@example.com", Role: "admin", GrantedBy: 1, GrantedAt: now}))
	// Upsert for the same ERP user replaces the grant instead of failing.
	require.NoError(t, repo.Upsert(ctx, &domainstaffrole.StaffRole{ERPUserID: 7, Email: "b@example.com", Role: "admin", GrantedBy: 2, GrantedAt: now}))

	all, err := repo.FindAll(ctx)
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.Equal(t, "b@example.com", all[0].Email)
	require.EqualValues(t, 2, all[0].GrantedBy)

	require.NoError(t, repo.DeleteByERPUserID(ctx, 7))
	require.ErrorIs(t, repo.DeleteByERPUserID(ctx, 7), domain.ErrNotFound)
}
