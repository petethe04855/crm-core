package promotion

import (
	"time"

	"crm-core/domain/linevis"
)

// LineVisibility reports whether the promotion is shown in the LINE OA at now.
// It matches the LIFF query (published, start <= now < end); an audience
// restriction still shows it, but only to the matching members.
func LineVisibility(p Promotion, now time.Time) linevis.Visibility {
	switch p.Status {
	case StatusDraft:
		return linevis.Hidden(linevis.ReasonDraft)
	case StatusArchived:
		return linevis.Hidden(linevis.ReasonArchived)
	case StatusPublished:
	default:
		return linevis.Hidden(linevis.ReasonDraft)
	}
	if now.Before(p.DisplayStartAt) {
		return linevis.NotStarted(p.DisplayStartAt)
	}
	if !now.Before(p.DisplayEndAt) {
		return linevis.Hidden(linevis.ReasonExpired)
	}
	end := p.DisplayEndAt
	v := linevis.Live(&end)
	if !p.Audience.IsEmpty() {
		v.Reason = linevis.ReasonLimited
	}
	return v
}
