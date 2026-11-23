package reward

import (
	"time"

	"crm-core/domain/linevis"
	domainreward "crm-core/domain/reward"
)

// LineVisibility reports whether the reward is listed in the LINE OA catalogue
// at now. It follows Catalog (active, inside its window, quota left); a
// minimum tier still lists it for everyone, only redeeming is limited.
func LineVisibility(v View, now time.Time) linevis.Visibility {
	r := v.Reward
	switch r.Status {
	case domainreward.StatusDraft:
		return linevis.Hidden(linevis.ReasonDraft)
	case domainreward.StatusInactive:
		return linevis.Hidden(linevis.ReasonInactive)
	case domainreward.StatusArchived:
		return linevis.Hidden(linevis.ReasonArchived)
	case domainreward.StatusActive:
	default:
		return linevis.Hidden(linevis.ReasonDraft)
	}
	if r.AvailableFrom != nil && now.Before(*r.AvailableFrom) {
		return linevis.NotStarted(*r.AvailableFrom)
	}
	if r.AvailableTo != nil && !now.Before(*r.AvailableTo) {
		return linevis.Hidden(linevis.ReasonExpired)
	}
	if v.RemainingQuota <= 0 {
		return linevis.Hidden(linevis.ReasonOutOfStock)
	}
	out := linevis.Live(r.AvailableTo)
	if r.MinTierID != nil {
		out.Reason = linevis.ReasonLimited
	}
	return out
}
