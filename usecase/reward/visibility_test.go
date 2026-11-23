package reward

import (
	"testing"
	"time"

	"crm-core/domain/linevis"
	domainreward "crm-core/domain/reward"
)

func TestRewardLineVisibility(t *testing.T) {
	now := time.Date(2026, 11, 22, 20, 0, 0, 0, time.UTC)
	from, to, past := now.Add(-time.Hour), now.Add(48*time.Hour), now.Add(-time.Minute)
	tier := uint(2)
	base := View{Reward: domainreward.Reward{Status: domainreward.StatusActive, AvailableFrom: &from, AvailableTo: &to}, RemainingQuota: 5}
	cases := []struct {
		name    string
		edit    func(*View)
		visible bool
		reason  string
	}{
		{"draft", func(v *View) { v.Reward.Status = domainreward.StatusDraft }, false, linevis.ReasonDraft},
		{"inactive", func(v *View) { v.Reward.Status = domainreward.StatusInactive }, false, linevis.ReasonInactive},
		{"archived", func(v *View) { v.Reward.Status = domainreward.StatusArchived }, false, linevis.ReasonArchived},
		{"not started", func(v *View) { f := now.Add(time.Hour); v.Reward.AvailableFrom = &f }, false, linevis.ReasonNotStarted},
		{"ended", func(v *View) { v.Reward.AvailableTo = &past }, false, linevis.ReasonExpired},
		{"out of stock", func(v *View) { v.RemainingQuota = 0 }, false, linevis.ReasonOutOfStock},
		{"live", func(*View) {}, true, linevis.ReasonLive},
		{"tier limited", func(v *View) { v.Reward.MinTierID = &tier }, true, linevis.ReasonLimited},
	}
	u := &usecase{}
	for _, tc := range cases {
		v := base
		tc.edit(&v)
		got := LineVisibility(v, now)
		if got.Visible != tc.visible || got.Reason != tc.reason {
			t.Errorf("%s: got visible=%v reason=%q, want %v %q", tc.name, got.Visible, got.Reason, tc.visible, tc.reason)
		}
		// must agree with the Catalog filter
		if want := u.open(v.Reward, now) == nil && v.RemainingQuota > 0; got.Visible != want {
			t.Errorf("%s: visible=%v but catalog shows=%v", tc.name, got.Visible, want)
		}
	}
}
