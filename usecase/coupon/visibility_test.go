package coupon

import (
	"testing"
	"time"

	domaincoupon "crm-core/domain/coupon"
	"crm-core/domain/linevis"
)

func TestCouponLineVisibility(t *testing.T) {
	now := time.Date(2026, 11, 22, 20, 0, 0, 0, time.UTC)
	start, end, past := now.Add(-time.Hour), now.Add(24*time.Hour), now.Add(-time.Minute)
	tier := uint(2)
	base := domaincoupon.Coupon{
		Status: domaincoupon.StatusActive, ClaimStartAt: &start, ClaimEndAt: &end,
		ExpiryMode: domaincoupon.ExpiryDaysAfterClaim, ValidDays: 30, TotalQuota: 10, ClaimedCount: 3, LimitPerMember: 1,
	}
	cases := []struct {
		name    string
		edit    func(*domaincoupon.Coupon)
		visible bool
		reason  string
	}{
		{"draft", func(c *domaincoupon.Coupon) { c.Status = domaincoupon.StatusDraft }, false, linevis.ReasonDraft},
		{"paused", func(c *domaincoupon.Coupon) { c.Status = domaincoupon.StatusPaused }, false, linevis.ReasonPaused},
		{"ended", func(c *domaincoupon.Coupon) { c.Status = domaincoupon.StatusEnded }, false, linevis.ReasonArchived},
		{"not started", func(c *domaincoupon.Coupon) { s := now.Add(time.Minute); c.ClaimStartAt = &s }, false, linevis.ReasonNotStarted},
		{"claim window over", func(c *domaincoupon.Coupon) { c.ClaimEndAt = &past }, false, linevis.ReasonExpired},
		{"fixed expiry passed", func(c *domaincoupon.Coupon) { c.ExpiryMode = domaincoupon.ExpiryFixedDate; c.ExpiresAt = &past }, false, linevis.ReasonExpired},
		{"quota full", func(c *domaincoupon.Coupon) { c.ClaimedCount = 10 }, false, linevis.ReasonQuotaFull},
		{"unlimited quota", func(c *domaincoupon.Coupon) { c.TotalQuota = 0; c.ClaimedCount = 99 }, true, linevis.ReasonLive},
		{"no window", func(c *domaincoupon.Coupon) { c.ClaimStartAt, c.ClaimEndAt = nil, nil }, true, linevis.ReasonLive},
		{"live", func(*domaincoupon.Coupon) {}, true, linevis.ReasonLive},
		{"tier limited", func(c *domaincoupon.Coupon) { c.MinTierID = &tier }, true, linevis.ReasonLimited},
	}
	for _, tc := range cases {
		c := base
		tc.edit(&c)
		got := LineVisibility(c, now)
		if got.Visible != tc.visible || got.Reason != tc.reason {
			t.Errorf("%s: got visible=%v reason=%q, want %v %q", tc.name, got.Visible, got.Reason, tc.visible, tc.reason)
		}
		// must agree with the LIFF "available" filter
		if got.Visible != (claimable(c, now) == nil) {
			t.Errorf("%s: visible=%v but claimable=%v", tc.name, got.Visible, claimable(c, now))
		}
	}
}
