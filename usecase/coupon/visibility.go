package coupon

import (
	"time"

	domaincoupon "crm-core/domain/coupon"
	"crm-core/domain/linevis"
)

// LineVisibility reports whether the coupon is listed in the LINE OA "รับคูปอง"
// tab at now. It follows claimable (the rule Available uses); a minimum tier
// still lists it, but only members of that tier or above can claim.
func LineVisibility(c domaincoupon.Coupon, now time.Time) linevis.Visibility {
	switch c.Status {
	case domaincoupon.StatusDraft:
		return linevis.Hidden(linevis.ReasonDraft)
	case domaincoupon.StatusPaused:
		return linevis.Hidden(linevis.ReasonPaused)
	case domaincoupon.StatusEnded:
		return linevis.Hidden(linevis.ReasonArchived)
	case domaincoupon.StatusActive:
	default:
		return linevis.Hidden(linevis.ReasonDraft)
	}
	if c.ClaimStartAt != nil && now.Before(*c.ClaimStartAt) {
		return linevis.NotStarted(*c.ClaimStartAt)
	}
	if c.ClaimEndAt != nil && !now.Before(*c.ClaimEndAt) {
		return linevis.Hidden(linevis.ReasonExpired)
	}
	if !ExpiresAt(c, now).After(now) {
		return linevis.Hidden(linevis.ReasonExpired)
	}
	if c.TotalQuota > 0 && c.ClaimedCount >= c.TotalQuota {
		return linevis.Hidden(linevis.ReasonQuotaFull)
	}
	v := linevis.Live(c.ClaimEndAt)
	if c.MinTierID != nil {
		v.Reason = linevis.ReasonLimited
	}
	return v
}
