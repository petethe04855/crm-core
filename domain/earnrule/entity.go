// Package earnrule defines the point earning rules used by the point
// calculator (plan: CRM_BACKOFFICE_MARKETING.md §4.1).
//
// A rule is one of three kinds:
//
//   - tier_multiplier:   multiplies the base points for members of one tier
//   - campaign_multiplier: multiplies the base points during a window,
//     optionally only for some SKUs; stackable rules all apply,
//     non-stackable rules compete by priority
//   - bonus_fixed:       a one-off fixed grant (signup, birthday, first code)
//
// The base points themselves come from the product-code lot; nothing here
// stores them.
package earnrule

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"crm-core/domain"
)

// Rule types.
const (
	TypeTierMultiplier     = "tier_multiplier"
	TypeCampaignMultiplier = "campaign_multiplier"
	TypeBonusFixed         = "bonus_fixed"
)

// One-off triggers for bonus_fixed rules.
const (
	TriggerSignup    = "signup"
	TriggerBirthday  = "birthday"
	TriggerFirstCode = "first_code"
)

// MaxMultiplier is the largest multiplier numeric(6,3) can hold without
// rounding. Anything above is a data-entry mistake.
const MaxMultiplier = 999.999

// MaxFixedPoints bounds a single bonus grant.
const MaxFixedPoints = 1_000_000

// EarnRule is one earning rule. TierID, SKUIDs and the window are only used by
// the rule kinds that need them; Normalize clears the rest so the table stays
// honest.
type EarnRule struct {
	ID         uint     `gorm:"primaryKey"`
	Type       string   `gorm:"size:30;not null;index"`
	TierID     *uint    `gorm:"index"`
	Multiplier float64  `gorm:"type:numeric(6,3);not null;default:1"`
	SKUIDs     []string `gorm:"serializer:json;type:jsonb"`
	Trigger    string   `gorm:"size:20"`
	Points     int64    `gorm:"not null;default:0"`
	StartAt    *time.Time
	EndAt      *time.Time
	Stackable  bool `gorm:"not null;default:false"`
	Priority   int  `gorm:"not null;default:0;index"`
	// No DB default: GORM skips zero values on insert, so default:true would
	// silently turn a rule created as disabled into an active one.
	IsActive bool `gorm:"not null;index"`

	CreatedBy      uint   `gorm:"not null;default:0"`
	CreatedByEmail string `gorm:"size:255"`
	UpdatedBy      uint   `gorm:"not null;default:0"`
	UpdatedByEmail string `gorm:"size:255"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Query lists the rules. An empty Type means every type.
type Query struct {
	Type string
}

// Repository is the persistence contract for earning rules.
type Repository interface {
	FindAll(ctx context.Context, q Query) ([]EarnRule, error)
	FindByID(ctx context.Context, id uint) (*EarnRule, error)
	// FindActive returns the rules that may fire at `at`: enabled and, when a
	// window is set, start <= at < end.
	FindActive(ctx context.Context, at time.Time) ([]EarnRule, error)
	Create(ctx context.Context, rule *EarnRule) error
	Update(ctx context.Context, rule *EarnRule) error
}

// ValidType reports whether t is a known rule type.
func ValidType(t string) bool {
	switch t {
	case TypeTierMultiplier, TypeCampaignMultiplier, TypeBonusFixed:
		return true
	}
	return false
}

// ValidTrigger reports whether t is a known one-off trigger.
func ValidTrigger(t string) bool {
	switch t {
	case TriggerSignup, TriggerBirthday, TriggerFirstCode:
		return true
	}
	return false
}

// Normalize trims text, deduplicates SKUs and clears the fields that do not
// apply to the rule's type. It does not validate; call Validate afterwards.
func (r *EarnRule) Normalize() {
	r.Type = strings.ToLower(strings.TrimSpace(r.Type))
	r.Trigger = strings.ToLower(strings.TrimSpace(r.Trigger))

	seen := make(map[string]struct{}, len(r.SKUIDs))
	skus := make([]string, 0, len(r.SKUIDs))
	for _, raw := range r.SKUIDs {
		sku := strings.TrimSpace(raw)
		if sku == "" {
			continue
		}
		if _, dup := seen[sku]; dup {
			continue
		}
		seen[sku] = struct{}{}
		skus = append(skus, sku)
	}
	r.SKUIDs = skus

	switch r.Type {
	case TypeTierMultiplier:
		r.Trigger = ""
		r.Points = 0
		r.SKUIDs = nil
	case TypeCampaignMultiplier:
		r.TierID = nil
		r.Trigger = ""
		r.Points = 0
	case TypeBonusFixed:
		r.TierID = nil
		r.Multiplier = 1
		r.SKUIDs = nil
	}
}

// Validate checks the fields each rule type needs (plan §4.1).
func (r *EarnRule) Validate() error {
	if !ValidType(r.Type) {
		return domain.NewValidationError("type",
			"must be one of: tier_multiplier, campaign_multiplier, bonus_fixed")
	}
	if r.Priority < 0 || r.Priority > 9999 {
		return domain.NewValidationError("priority", "must be between 0 and 9999")
	}

	switch r.Type {
	case TypeTierMultiplier:
		if r.TierID == nil || *r.TierID == 0 {
			return domain.NewValidationError("tierId", "is required for a tier multiplier")
		}
		if err := validateMultiplier(r.Multiplier); err != nil {
			return err
		}
	case TypeCampaignMultiplier:
		if err := validateMultiplier(r.Multiplier); err != nil {
			return err
		}
	case TypeBonusFixed:
		if !ValidTrigger(r.Trigger) {
			return domain.NewValidationError("trigger",
				"must be one of: signup, birthday, first_code")
		}
		if r.Points <= 0 {
			return domain.NewValidationError("points", "must be greater than zero")
		}
		if r.Points > MaxFixedPoints {
			return domain.NewValidationError("points",
				fmt.Sprintf("must not exceed %d", MaxFixedPoints))
		}
	}

	if (r.StartAt == nil) != (r.EndAt == nil) {
		return domain.NewValidationError("startAt", "start and end must be set together")
	}
	if r.StartAt != nil && !r.StartAt.Before(*r.EndAt) {
		return domain.NewValidationError("endAt", "must be after startAt")
	}
	return nil
}

func validateMultiplier(m float64) error {
	if math.IsNaN(m) || math.IsInf(m, 0) {
		return domain.NewValidationError("multiplier", "must be a finite number")
	}
	if m <= 0 {
		return domain.NewValidationError("multiplier", "must be greater than zero")
	}
	if m > MaxMultiplier {
		return domain.NewValidationError("multiplier",
			fmt.Sprintf("must not exceed %.3f", MaxMultiplier))
	}
	return nil
}
