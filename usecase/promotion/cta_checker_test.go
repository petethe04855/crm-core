package promotion

import (
	"context"
	"testing"
	"time"

	"crm-core/database"
	"crm-core/domain"
	domaincoupon "crm-core/domain/coupon"
	domainpromotion "crm-core/domain/promotion"
	domainreward "crm-core/domain/reward"
	"crm-core/migrations"
	"crm-core/repository/postgres"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func checkerDB(t *testing.T) (*TargetChecker, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), database.GormConfig("", logger.Silent))
	require.NoError(t, err)
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, migrations.Run(db, ""))
	return NewTargetChecker(postgres.NewCouponRepository(db), postgres.NewRewardRepository(db)), db
}

func TestCTATargetChecker(t *testing.T) {
	c, db := checkerDB(t)
	ctx := context.Background()
	start := time.Now()
	end := start.AddDate(0, 1, 0)

	_, err := c.Validate(ctx, domainpromotion.CTACoupon, "abc", start, end)
	require.ErrorIs(t, err, domain.ErrValidation)
	_, err = c.Validate(ctx, domainpromotion.CTACoupon, "99", start, end)
	require.ErrorIs(t, err, domain.ErrValidation, "missing coupon")

	closes := start.AddDate(0, 0, 3)
	cp := domaincoupon.Coupon{Name: "c", BenefitType: "amount_off", ExpiryMode: "days_after_claim", ValidDays: 7, LimitPerMember: 1,
		Channel: "store", CodeType: "unique", Status: domaincoupon.StatusActive, ClaimEndAt: &closes}
	require.NoError(t, db.Create(&cp).Error)
	warning, err := c.Validate(ctx, domainpromotion.CTACoupon, "1", start, end)
	require.NoError(t, err)
	require.Contains(t, warning, "ปิดรับก่อน")

	require.NoError(t, db.Model(&cp).Update("status", domaincoupon.StatusEnded).Error)
	_, err = c.Validate(ctx, domainpromotion.CTACoupon, "1", start, end)
	require.ErrorIs(t, err, domain.ErrValidation, "ended coupon")

	rw := domainreward.Reward{Name: "r", Type: domainreward.TypePhysical, QtyPerRedemption: 1, PointsCost: 10, Status: domainreward.StatusActive}
	require.NoError(t, db.Create(&rw).Error)
	warning, err = c.Validate(ctx, domainpromotion.CTAReward, "1", start, end)
	require.NoError(t, err)
	require.Empty(t, warning)
	require.NoError(t, db.Model(&rw).Update("status", domainreward.StatusArchived).Error)
	_, err = c.Validate(ctx, domainpromotion.CTAReward, "1", start, end)
	require.ErrorIs(t, err, domain.ErrValidation)
}
