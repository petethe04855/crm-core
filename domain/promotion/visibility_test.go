package promotion

import (
	"testing"
	"time"

	"crm-core/domain/linevis"
)

func TestLineVisibility(t *testing.T) {
	now := time.Date(2026, 11, 22, 20, 0, 0, 0, time.UTC)
	base := Promotion{Status: StatusPublished, DisplayStartAt: now.Add(-time.Hour), DisplayEndAt: now.Add(time.Hour)}
	days := 30
	cases := []struct {
		name    string
		edit    func(*Promotion)
		visible bool
		reason  string
	}{
		{"draft", func(p *Promotion) { p.Status = StatusDraft }, false, linevis.ReasonDraft},
		{"archived", func(p *Promotion) { p.Status = StatusArchived }, false, linevis.ReasonArchived},
		{"not started", func(p *Promotion) { p.DisplayStartAt = now.Add(time.Minute) }, false, linevis.ReasonNotStarted},
		{"start boundary is live", func(p *Promotion) { p.DisplayStartAt = now }, true, linevis.ReasonLive},
		{"end boundary is expired", func(p *Promotion) { p.DisplayEndAt = now }, false, linevis.ReasonExpired},
		{"live", func(*Promotion) {}, true, linevis.ReasonLive},
		{"audience limited", func(p *Promotion) { p.Audience.NewMemberDays = &days }, true, linevis.ReasonLimited},
	}
	for _, tc := range cases {
		p := base
		tc.edit(&p)
		got := LineVisibility(p, now)
		if got.Visible != tc.visible || got.Reason != tc.reason {
			t.Errorf("%s: got visible=%v reason=%q, want %v %q", tc.name, got.Visible, got.Reason, tc.visible, tc.reason)
		}
		// must agree with the LIFF filter
		if got.Visible != IsLiveAt(p, now) {
			t.Errorf("%s: visible=%v but IsLiveAt=%v", tc.name, got.Visible, IsLiveAt(p, now))
		}
	}
	if v := LineVisibility(Promotion{Status: StatusPublished, DisplayStartAt: now.Add(time.Hour), DisplayEndAt: now.Add(2 * time.Hour)}, now); v.From == nil || !v.From.Equal(now.Add(time.Hour)) {
		t.Errorf("not started: From = %v, want the start time", v.From)
	}
}
