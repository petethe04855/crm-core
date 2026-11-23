package memberreport_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"crm-core/database"
	"crm-core/domain"
	domainloyalty "crm-core/domain/loyalty"
	domainmember "crm-core/domain/member"
	domainpoints "crm-core/domain/points"
	domainproductcode "crm-core/domain/productcode"
	domainreward "crm-core/domain/reward"
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

// TestMemberReportRegistrationSplit covers the gender split, the age bands and
// the first-SKU table: the three things the dashboard shows about who signed up.
func TestMemberReportRegistrationSplit(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), database.GormConfig("", logger.Silent))
	require.NoError(t, err)
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, migrations.Run(db, ""))
	var tier domainloyalty.Tier
	require.NoError(t, db.Where("code = ?", domainloyalty.DefaultTierCode).First(&tier).Error)

	bkk := timeutil.Bangkok()
	at := func(y int, m time.Month, d, h int) time.Time { return time.Date(y, m, d, h, 0, 0, 0, bkk) }
	registered := at(2026, 11, 10, 10)

	// Birth dates chosen so the age on 2026-11-23 is unambiguous.
	birth := func(y int) *time.Time { v := at(y, 1, 15, 0); return &v }
	seed := func(code, gender string, birthDate *time.Time) domainmember.Member {
		m := domainmember.Member{MemberCode: code, FirstName: "T", LastName: code, Email: code + "@x.com",
			TierID: tier.ID, Gender: gender, BirthDate: birthDate, Status: domainmember.StatusActive,
			RegisteredChannel: "line", CreatedAt: registered}
		require.NoError(t, db.Create(&m).Error)
		return m
	}
	male := seed("G1", domainmember.GenderMale, birth(1995))     // 31
	female := seed("G2", domainmember.GenderFemale, birth(2005)) // 21
	other := seed("G3", domainmember.GenderOther, nil)           // no birth date
	seed("G4", "", birth(1970))                                  // 56, blank gender

	// Two batches with different SKUs, and a code per member.
	skuA := domainproductcode.Batch{BatchNo: "B-A", SKUID: 1, SKUCode: "SKU-A", SKUNameSnapshot: "สินค้า A",
		Quantity: 10, PointsPerCode: 5, Status: domainproductcode.BatchActive}
	skuB := domainproductcode.Batch{BatchNo: "B-B", SKUID: 2, SKUCode: "SKU-B", SKUNameSnapshot: "สินค้า B",
		Quantity: 10, PointsPerCode: 5, Status: domainproductcode.BatchActive}
	require.NoError(t, db.Create(&skuA).Error)
	require.NoError(t, db.Create(&skuB).Error)

	usedAt := at(2026, 11, 15, 12)
	code := func(batchID uint, suffix string, memberID uint, used time.Time) {
		c := domainproductcode.Code{BatchID: batchID, CodeHash: "h-" + suffix, CodeSuffix: suffix,
			Status: domainproductcode.CodeUsed, UsedByMemberID: &memberID, UsedAt: &used}
		require.NoError(t, db.Create(&c).Error)
	}
	// male's first code is SKU-A, then SKU-B later: only SKU-A counts.
	code(skuA.ID, "aaaa", male.ID, usedAt)
	code(skuB.ID, "bbbb", male.ID, usedAt.Add(time.Hour))
	// female's first code is SKU-A too.
	code(skuA.ID, "cccc", female.ID, usedAt)
	// other's first code is SKU-B.
	code(skuB.ID, "dddd", other.ID, usedAt)
	// blank never typed a code.

	uc := memberreport.New(postgres.NewMemberReportRepository(db))
	r, err := uc.Build(context.Background(), at(2026, 11, 1, 0), at(2026, 12, 1, 0))
	require.NoError(t, err)
	require.EqualValues(t, 4, r.NewMembers)

	// Gender: a blank value is folded into unspecified, and every value is
	// present even when nobody picked it.
	byGender := map[string]int64{}
	for _, g := range r.Genders {
		byGender[g.Gender] = g.Members
	}
	require.EqualValues(t, 1, byGender[domainmember.GenderMale])
	require.EqualValues(t, 1, byGender[domainmember.GenderFemale])
	require.EqualValues(t, 1, byGender[domainmember.GenderOther])
	require.EqualValues(t, 1, byGender[domainmember.GenderUnspecified], "blank gender counts as unspecified")
	require.Len(t, r.Genders, 4)

	// Age: 31 → 25-34, 21 → 18-24, 56 → 55plus, no birth date → unknown.
	byAge := map[string]int64{}
	for _, bucket := range r.AgeBuckets {
		byAge[bucket.Key] = bucket.Members
	}
	require.EqualValues(t, 1, byAge["25-34"])
	require.EqualValues(t, 1, byAge["18-24"])
	require.EqualValues(t, 1, byAge["55plus"])
	require.EqualValues(t, 1, byAge["unknown"], "a member without a birth date is not guessed")

	// First SKU: one row per member, and the later SKU-B code of the same member
	// does not add a second count.
	require.Len(t, r.FirstSKUs, 2)
	require.Equal(t, "SKU-A", r.FirstSKUs[0].SKUCode, "biggest first")
	require.EqualValues(t, 2, r.FirstSKUs[0].Members)
	require.Equal(t, "สินค้า A", r.FirstSKUs[0].SKUName)
	require.Equal(t, "SKU-B", r.FirstSKUs[1].SKUCode)
	require.EqualValues(t, 1, r.FirstSKUs[1].Members)
	require.InDelta(t, 50.0, r.FirstSKUs[0].SharePercent, 0.01)
}
// TestMemberReportRewardsRedeemed counts redemptions per reward inside the
// range, leaving out cancelled ones and those outside the range.
func TestMemberReportRewardsRedeemed(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), database.GormConfig("", logger.Silent))
	require.NoError(t, err)
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, migrations.Run(db, ""))

	bkk := timeutil.Bangkok()
	at := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 12, 0, 0, 0, bkk) }
	n := 0
	redeem := func(member, reward uint, name, status string, qty int, points int64, created time.Time) {
		n++
		rd := domainreward.Redemption{RedemptionNo: fmt.Sprintf("RD-%d", n), MemberID: member, RewardID: reward, RewardName: name,
			Qty: qty, Points: points, Status: status, WarehouseID: 1, CreatedAt: created}
		require.NoError(t, db.Create(&rd).Error)
	}
	in := at(2026, 11, 10)
	redeem(1, 10, "กระเป๋า", domainreward.RedemptionShipped, 1, 500, in)
	redeem(2, 10, "กระเป๋า", domainreward.RedemptionPending, 1, 500, in)
	redeem(2, 20, "แก้วน้ำ", domainreward.RedemptionApproved, 2, 200, in)
	redeem(3, 20, "แก้วน้ำ", domainreward.RedemptionCancelled, 1, 100, in) // refunded: not counted
	redeem(4, 10, "กระเป๋า", domainreward.RedemptionShipped, 1, 500, at(2026, 9, 1)) // outside the range

	r, err := memberreport.New(postgres.NewMemberReportRepository(db)).Build(context.Background(), at(2026, 11, 1), at(2026, 12, 1))
	require.NoError(t, err)
	require.EqualValues(t, 3, r.Redemptions)
	require.EqualValues(t, 2, r.RedemptionMembers)
	require.Len(t, r.Rewards, 2)
	require.Equal(t, "กระเป๋า", r.Rewards[0].RewardName)
	require.EqualValues(t, 2, r.Rewards[0].Redemptions)
	require.EqualValues(t, 1000, r.Rewards[0].Points)
	require.InDelta(t, 66.7, r.Rewards[0].SharePercent, 0.01)
	require.Equal(t, "แก้วน้ำ", r.Rewards[1].RewardName)
	require.EqualValues(t, 2, r.Rewards[1].Qty)
}
