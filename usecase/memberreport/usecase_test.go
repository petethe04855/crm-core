package memberreport_test

import (
	"context"
	"testing"
	"time"

	"crm-core/database"
	"crm-core/domain"
	domainloyalty "crm-core/domain/loyalty"
	domainmember "crm-core/domain/member"
	domainpoints "crm-core/domain/points"
	"crm-core/migrations"
	"crm-core/repository/postgres"
	"crm-core/timeutil"
	"crm-core/usecase/memberreport"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestMemberReport(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), database.GormConfig("", logger.Silent))
	require.NoError(t, err)
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, migrations.Run(db, ""))
	var tier domainloyalty.Tier
	require.NoError(t, db.Where("code = ?", domainloyalty.DefaultTierCode).First(&tier).Error)

	bkk := timeutil.Bangkok()
	at := func(y int, m time.Month, d, h int) time.Time { return time.Date(y, m, d, h, 0, 0, 0, bkk) }
	active := at(2026, 11, 10, 9)
	seed := func(code, email string, created time.Time, lastActive *time.Time, status string) domainmember.Member {
		m := domainmember.Member{MemberCode: code, FirstName: "T", LastName: code, Email: email, TierID: tier.ID,
			Gender: "unspecified", Status: status, RegisteredChannel: "line", CreatedAt: created, LastActiveAt: lastActive}
		require.NoError(t, db.Create(&m).Error)
		return m
	}
	// 31 Oct 23:30 Bangkok is October even though it is still the 31st in UTC.
	a := seed("M1", "a@x.com", at(2026, 10, 31, 23), nil, domainmember.StatusActive)
	b := seed("M2", "b@x.com", at(2026, 11, 5, 10), &active, domainmember.StatusActive)
	seed("M3", "c@x.com", at(2026, 11, 20, 10), &active, domainmember.StatusSuspended)
	seed("M4", "d@x.com", at(2026, 11, 21, 10), nil, domainmember.StatusDeleted)

	ledger := postgres.NewPointsRepository(db)
	ctx := context.Background()
	_, err = ledger.Earn(ctx, domainpoints.EarnCommand{MemberID: b.ID, Points: 100, SourceType: domainpoints.SourceOrder, SourceID: "o1"})
	require.NoError(t, err)
	_, err = ledger.Spend(ctx, domainpoints.SpendCommand{MemberID: b.ID, Points: 30, SourceType: domainpoints.SourceRedemption, SourceID: "r1"})
	require.NoError(t, err)
	_, err = ledger.Earn(ctx, domainpoints.EarnCommand{MemberID: a.ID, Points: 20, SourceType: domainpoints.SourceOrder, SourceID: "o2"})
	require.NoError(t, err)

	uc := memberreport.New(postgres.NewMemberReportRepository(db))
	from := at(2026, 10, 1, 0)
	to := at(2026, 12, 1, 0)
	r, err := uc.Build(ctx, from, to)
	require.NoError(t, err)
	require.EqualValues(t, 3, r.TotalMembers, "deleted members are not counted")
	require.EqualValues(t, 3, r.NewMembers)
	require.EqualValues(t, 2, r.ActiveMembers)
	require.EqualValues(t, 120, r.PointsEarned)
	require.EqualValues(t, 30, r.PointsRedeemed, "reported as a positive number")
	require.EqualValues(t, 90, r.OutstandingPoint)
	require.Len(t, r.Months, 2)
	require.Equal(t, "2026-10", r.Months[0].Month)
	require.EqualValues(t, 1, r.Months[0].NewMembers)
	require.EqualValues(t, 2, r.Months[1].NewMembers)

	_, err = uc.Build(ctx, to, from)
	require.ErrorIs(t, err, domain.ErrValidation)
	_, err = uc.Build(ctx, from, from.AddDate(2, 0, 0))
	require.ErrorIs(t, err, domain.ErrValidation)
}
