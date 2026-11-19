// Package promotion defines the monthly marketing promotions shown in the
// LINE OA (home banner, promotion list, popup). A promotion has a display
// window; whether it is scheduled/live/expired is computed from the clock and
// never stored, so the backoffice and the LINE app always agree.
package promotion

import (
	"context"
	"time"
)

// Placement values: where the promotion appears in the LINE app.
const (
	PlacementHomeBanner    = "home_banner"
	PlacementPromotionList = "promotion_list"
	PlacementPopup         = "popup"
)

// CTA types. reward and coupon target the modules that own those records.
const (
	CTANone   = "none"
	CTAURL    = "url"
	CTAReward = "reward"
	CTACoupon = "coupon"
)

// Stored status, set by staff.
const (
	StatusDraft     = "draft"
	StatusPublished = "published"
	StatusArchived  = "archived"
)

// DisplayStatus values (computed, never stored).
const (
	DisplayDraft     = "draft"
	DisplayScheduled = "scheduled"
	DisplayLive      = "live"
	DisplayExpired   = "expired"
	DisplayArchived  = "archived"
)

// MaxTitleRunes and MaxDescriptionRunes mirror the UI limits.
const (
	MaxTitleRunes       = 120
	MaxDescriptionRunes = 2000
)

// Audience narrows who sees a promotion. An empty Audience means everyone.
//
// When both fields are set the member must match both (AND): "gold members
// who registered in the last 30 days".
type Audience struct {
	Tiers         []string `json:"tiers,omitempty"`
	NewMemberDays *int     `json:"newMemberDays,omitempty"`
}

// IsEmpty reports whether the audience targets everyone.
func (a Audience) IsEmpty() bool {
	return len(a.Tiers) == 0 && a.NewMemberDays == nil
}

// MemberFacts are the member attributes an audience test needs.
type MemberFacts struct {
	// TierCode is the member's current loyalty tier code (e.g. "gold").
	TierCode string
	// RegisteredAt is when the member joined (Thai wall clock is irrelevant;
	// the comparison is absolute time).
	RegisteredAt time.Time
}

// Matches reports whether member belongs to the audience. now is passed in so
// callers (usecase tests, the LINE app) control the clock.
func (a Audience) Matches(member MemberFacts, now time.Time) bool {
	if len(a.Tiers) > 0 {
		found := false
		for _, tier := range a.Tiers {
			if tier == member.TierCode {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if a.NewMemberDays != nil {
		days := *a.NewMemberDays
		if days < 0 {
			return false
		}
		age := now.Sub(member.RegisteredAt)
		// A registration in the future is data from another clock; treat it as
		// brand new rather than excluding the member.
		if age > time.Duration(days)*24*time.Hour {
			return false
		}
	}
	return true
}

// Promotion is one marketing promotion (table crm.promotions).
//
// Zero display timestamps are allowed while a promotion is a draft; publishing
// requires both.
type Promotion struct {
	ID          string `gorm:"primaryKey;size:36"`
	Title       string `gorm:"size:120;not null"`
	Description string `gorm:"type:text"`
	ImageURL    string `gorm:"size:500"`

	Placement string `gorm:"size:30;not null;index"`
	// DisplayStartAt/DisplayEndAt form a half-open window [start, end).
	DisplayStartAt time.Time `gorm:"not null;index:idx_promotion_window,priority:2"`
	DisplayEndAt   time.Time `gorm:"not null;index:idx_promotion_window,priority:3"`

	CTAType   string `gorm:"column:cta_type;size:20;not null;default:none"`
	CTATarget string `gorm:"column:cta_target;size:500"`

	Audience Audience `gorm:"serializer:json;type:jsonb"`

	Priority int `gorm:"not null;default:0"`

	Status string `gorm:"size:20;not null;index:idx_promotion_window,priority:1"`

	CreatedBy      uint   `gorm:"not null;default:0"`
	CreatedByEmail string `gorm:"size:255"`
	UpdatedBy      uint   `gorm:"not null;default:0"`
	UpdatedByEmail string `gorm:"size:255"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// DisplayStatus is the status shown on screen (plan §3.2). The stored status
// wins for draft/archived; a published promotion is scheduled, live or expired
// depending on now. Boundaries: start is live, end is expired.
func DisplayStatus(p Promotion, now time.Time) string {
	switch p.Status {
	case StatusDraft:
		return DisplayDraft
	case StatusArchived:
		return DisplayArchived
	case StatusPublished:
		switch {
		case now.Before(p.DisplayStartAt):
			return DisplayScheduled
		case now.Before(p.DisplayEndAt):
			return DisplayLive
		default:
			return DisplayExpired
		}
	}
	return p.Status
}

// IsLiveAt reports whether a published promotion is live at now.
func IsLiveAt(p Promotion, now time.Time) bool {
	return p.Status == StatusPublished &&
		!now.Before(p.DisplayStartAt) && now.Before(p.DisplayEndAt)
}

// Query filters the promotion list. OverlapFrom/OverlapTo implement the
// "month" filter: a promotion is included when its display window overlaps
// [from, to). The usecase cuts those bounds in Asia/Bangkok.
type Query struct {
	Status    string
	Placement string
	Search    string

	OverlapFrom *time.Time
	OverlapTo   *time.Time

	Page  int
	Limit int
}

// Repository is the persistence contract for promotions.
type Repository interface {
	FindAll(ctx context.Context, q Query) ([]Promotion, int64, error)
	FindByID(ctx context.Context, id string) (*Promotion, error)
	Create(ctx context.Context, p *Promotion) error
	Update(ctx context.Context, p *Promotion) error
	Delete(ctx context.Context, id string) error
	// CountPublishedOverlapping counts published promotions in placement whose
	// window overlaps [start, end), excluding excludeID. It powers the
	// "more than 5 home banners at once" warning.
	CountPublishedOverlapping(ctx context.Context, placement string, start, end time.Time, excludeID string) (int64, error)
	// ListLive returns published promotions of one placement whose window
	// contains now, ordered by priority (highest first).
	ListLive(ctx context.Context, placement string, now time.Time) ([]Promotion, error)
}

// ValidPlacement reports whether p is a known placement.
func ValidPlacement(p string) bool {
	switch p {
	case PlacementHomeBanner, PlacementPromotionList, PlacementPopup:
		return true
	}
	return false
}

// ValidCTAType reports whether t is a known CTA type.
func ValidCTAType(t string) bool {
	switch t {
	case CTANone, CTAURL, CTAReward, CTACoupon:
		return true
	}
	return false
}

// ValidStatus reports whether s is a stored status.
func ValidStatus(s string) bool {
	switch s {
	case StatusDraft, StatusPublished, StatusArchived:
		return true
	}
	return false
}
