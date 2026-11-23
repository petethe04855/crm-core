package promotion

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"crm-core/domain"
	domaincoupon "crm-core/domain/coupon"
	domainpromotion "crm-core/domain/promotion"
	domainreward "crm-core/domain/reward"
)

// TargetChecker validates promotion CTAs that point at a coupon or a reward
// (the T4/T6 modules). Publishing fails for a target that does not exist or
// is closed, and warns when it may close before the promotion ends.
type TargetChecker struct {
	coupons domaincoupon.Repository
	rewards domainreward.Repository
}

// NewTargetChecker builds the checker used by promotion.New.
func NewTargetChecker(coupons domaincoupon.Repository, rewards domainreward.Repository) *TargetChecker {
	return &TargetChecker{coupons: coupons, rewards: rewards}
}

var _ CTATargetChecker = (*TargetChecker)(nil)

func parseTarget(target string) (uint, error) {
	id, err := strconv.ParseUint(strings.TrimSpace(target), 10, 64)
	if err != nil || id == 0 {
		return 0, domain.NewValidationError("ctaTarget", "must be the id of the coupon or reward")
	}
	return uint(id), nil
}

// Validate implements CTATargetChecker.
func (c *TargetChecker) Validate(ctx context.Context, ctaType, target string, start, end time.Time) (string, error) {
	id, err := parseTarget(target)
	if err != nil {
		return "", err
	}
	switch ctaType {
	case domainpromotion.CTACoupon:
		if c.coupons == nil {
			return "", nil
		}
		cp, err := c.coupons.FindByID(ctx, id)
		if errors.Is(err, domain.ErrNotFound) {
			return "", domain.NewValidationError("ctaTarget", "coupon not found")
		}
		if err != nil {
			return "", err
		}
		switch {
		case cp.Status == domaincoupon.StatusEnded:
			return "", domain.NewValidationError("ctaTarget", "the coupon has ended")
		case cp.Status != domaincoupon.StatusActive:
			return "คูปองยังไม่เปิดใช้งาน — สมาชิกจะกดรับไม่ได้จนกว่าจะเปิด", nil
		case cp.ClaimEndAt != nil && cp.ClaimEndAt.Before(end):
			return "คูปองปิดรับก่อนโปรโมชั่นสิ้นสุด", nil
		case cp.TotalQuota > 0 && cp.ClaimedCount >= cp.TotalQuota:
			return "คูปองถูกรับครบโควตาแล้ว", nil
		}
	case domainpromotion.CTAReward:
		if c.rewards == nil {
			return "", nil
		}
		rw, err := c.rewards.FindByID(ctx, id)
		if errors.Is(err, domain.ErrNotFound) {
			return "", domain.NewValidationError("ctaTarget", "reward not found")
		}
		if err != nil {
			return "", err
		}
		switch {
		case rw.Status == domainreward.StatusArchived:
			return "", domain.NewValidationError("ctaTarget", "the reward is archived")
		case rw.Status != domainreward.StatusActive:
			return "ของรางวัลยังไม่เปิดให้แลก", nil
		case rw.AvailableTo != nil && rw.AvailableTo.Before(end):
			return "ของรางวัลปิดแลกก่อนโปรโมชั่นสิ้นสุด", nil
		}
	}
	return "", nil
}
