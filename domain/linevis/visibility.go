// Package linevis describes whether a backoffice item (promotion, coupon,
// reward) is shown to members in the LINE OA (LIFF) right now, and if not,
// why. The rules live next to each entity (promotion.LineVisibility,
// coupon.LineVisibility, reward.LineVisibility) and use the same conditions
// the LIFF endpoints filter on, so the backoffice badge never disagrees with
// what members actually see.
package linevis

import "time"

// Reasons. Visible items carry ReasonLive or ReasonLimited.
const (
	ReasonLive       = "live"         // shown to every member
	ReasonLimited    = "limited"      // shown, but only to part of the members (audience / tier)
	ReasonDraft      = "draft"        // not published / activated yet
	ReasonPaused     = "paused"       // coupon paused by staff
	ReasonInactive   = "inactive"     // reward switched off by staff
	ReasonArchived   = "archived"     // archived or ended by staff
	ReasonNotStarted = "not_started"  // published, but its start time is in the future
	ReasonExpired    = "expired"      // its end time has passed
	ReasonQuotaFull  = "quota_full"   // coupon quota fully claimed
	ReasonOutOfStock = "out_of_stock" // reward has no stock / quota left
)

// Visibility is the LINE OA status of one item at a given time.
type Visibility struct {
	Visible bool
	Reason  string
	// From is when a not-yet-started item will appear.
	From *time.Time
	// Until is when a visible item disappears (nil = no end).
	Until *time.Time
}

// Live is a visible result.
func Live(until *time.Time) Visibility {
	return Visibility{Visible: true, Reason: ReasonLive, Until: until}
}

// Hidden is a hidden result.
func Hidden(reason string) Visibility {
	return Visibility{Reason: reason}
}

// NotStarted is hidden until from.
func NotStarted(from time.Time) Visibility {
	return Visibility{Reason: ReasonNotStarted, From: &from}
}
