package points

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"crm-core/domain"
	domainearnrule "crm-core/domain/earnrule"
)

// errRulesNotConfigured means the usecase was built without WithEarnRules,
// which is a wiring bug rather than a user error.
var errRulesNotConfigured = errors.New("points: earn rules are not configured")

// EarnRuleInput is the create/update body. PATCH replaces every editable field,
// matching what the form sends.
type EarnRuleInput struct {
	Type       string
	TierID     *uint
	Multiplier float64
	SKUIDs     []string
	Trigger    string
	Points     int64
	StartAt    *time.Time
	EndAt      *time.Time
	Stackable  bool
	Priority   int
	// IsActive is a pointer so an omitted value keeps the current state (new
	// rules default to active).
	IsActive *bool
}

// ListEarnRules returns every rule, priority first.
func (u *usecase) ListEarnRules(ctx context.Context, q domainearnrule.Query) ([]domainearnrule.EarnRule, error) {
	if u.rules == nil {
		return nil, errRulesNotConfigured
	}
	q.Type = strings.ToLower(strings.TrimSpace(q.Type))
	if q.Type != "" && !domainearnrule.ValidType(q.Type) {
		return nil, domain.NewValidationError("type",
			"must be one of: tier_multiplier, campaign_multiplier, bonus_fixed")
	}
	return u.rules.FindAll(ctx, q)
}

// CreateEarnRule validates the rule for its type and stores it.
func (u *usecase) CreateEarnRule(ctx context.Context, actor domain.Actor, in EarnRuleInput) (*domainearnrule.EarnRule, error) {
	if u.rules == nil {
		return nil, errRulesNotConfigured
	}
	rule := &domainearnrule.EarnRule{
		Type:           in.Type,
		TierID:         in.TierID,
		Multiplier:     in.Multiplier,
		SKUIDs:         in.SKUIDs,
		Trigger:        in.Trigger,
		Points:         in.Points,
		StartAt:        in.StartAt,
		EndAt:          in.EndAt,
		Stackable:      in.Stackable,
		Priority:       in.Priority,
		IsActive:       true,
		CreatedBy:      actor.ERPUserID,
		CreatedByEmail: actor.Email,
		UpdatedBy:      actor.ERPUserID,
		UpdatedByEmail: actor.Email,
	}
	if in.IsActive != nil {
		rule.IsActive = *in.IsActive
	}
	if err := u.prepareRule(ctx, rule); err != nil {
		return nil, err
	}
	if err := u.rules.Create(ctx, rule); err != nil {
		return nil, err
	}
	return rule, nil
}

// UpdateEarnRule replaces the editable fields, including the enable toggle.
// Ledger rows keep the rule IDs they were written with, so history never
// changes when a rule is edited.
func (u *usecase) UpdateEarnRule(ctx context.Context, actor domain.Actor, id uint, in EarnRuleInput) (*domainearnrule.EarnRule, error) {
	if u.rules == nil {
		return nil, errRulesNotConfigured
	}
	if id == 0 {
		return nil, domain.NewValidationError("id", "is required")
	}
	rule, err := u.rules.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}

	rule.Type = in.Type
	rule.TierID = in.TierID
	rule.Multiplier = in.Multiplier
	rule.SKUIDs = in.SKUIDs
	rule.Trigger = in.Trigger
	rule.Points = in.Points
	rule.StartAt = in.StartAt
	rule.EndAt = in.EndAt
	rule.Stackable = in.Stackable
	rule.Priority = in.Priority
	if in.IsActive != nil {
		rule.IsActive = *in.IsActive
	}
	rule.Normalize()
	rule.UpdatedBy = actor.ERPUserID
	rule.UpdatedByEmail = actor.Email

	if err := u.prepareRule(ctx, rule); err != nil {
		return nil, err
	}
	if err := u.rules.Update(ctx, rule); err != nil {
		return nil, err
	}
	return rule, nil
}

// prepareRule normalises, validates and checks the tier reference.
func (u *usecase) prepareRule(ctx context.Context, rule *domainearnrule.EarnRule) error {
	rule.Normalize()
	if err := rule.Validate(); err != nil {
		return err
	}
	if rule.TierID != nil && u.tiers != nil {
		if _, err := u.tiers.FindByID(ctx, *rule.TierID); err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return domain.NewValidationError("tierId", "does not exist")
			}
			return err
		}
	}
	return nil
}

// CalculateInput is the calculator's input. At is the moment the points are
// earned (zero means now), so backdated orders use their own window.
type CalculateInput struct {
	TierID     uint
	SKUID      string
	BasePoints int64
	At         time.Time
}

// AppliedRule is one rule that changed the result.
type AppliedRule struct {
	ID         uint    `json:"id"`
	Type       string  `json:"type"`
	Multiplier float64 `json:"multiplier"`
}

// CalculateResult is floor(base × tier multiplier × campaign multiplier) plus
// the rules that were used.
type CalculateResult struct {
	Points int64         `json:"points"`
	Rules  []AppliedRule `json:"rules"`
}

// maxBasePoints bounds a single earn calculation so a fat-fingered product-code
// lot cannot credit millions at once.
const maxBasePoints = 1_000_000

// Calculate applies the active earn rules (plan §4.1):
//
//	floor(base × tier multiplier × campaign multiplier)
//
// Stackable campaigns multiply together; among non-stackable campaigns only the
// highest priority applies (ties go to the lower id). Disabled and expired rules
// are already filtered out by the repository.
func (u *usecase) Calculate(ctx context.Context, in CalculateInput) (CalculateResult, error) {
	if u.rules == nil {
		return CalculateResult{}, errRulesNotConfigured
	}
	if in.TierID == 0 {
		return CalculateResult{}, domain.NewValidationError("tierId", "is required")
	}
	if in.BasePoints < 0 {
		return CalculateResult{}, domain.NewValidationError("basePoints", "must not be negative")
	}
	if in.BasePoints > maxBasePoints {
		return CalculateResult{}, domain.NewValidationError("basePoints",
			fmt.Sprintf("must not exceed %d", maxBasePoints))
	}
	at := in.At
	if at.IsZero() {
		at = u.now()
	}

	rules, err := u.rules.FindActive(ctx, at)
	if err != nil {
		return CalculateResult{}, err
	}

	tierMultiplier := 1.0
	campaignMultiplier := 1.0
	applied := make([]AppliedRule, 0, 4)

	var bestNonStackable *domainearnrule.EarnRule
	for i := range rules {
		rule := rules[i]
		switch rule.Type {
		case domainearnrule.TypeTierMultiplier:
			// FindActive orders by priority desc, id asc, so the first rule for
			// this tier is the intended one.
			if rule.TierID != nil && *rule.TierID == in.TierID && !tierApplied(applied) {
				tierMultiplier = rule.Multiplier
				applied = append(applied, appliedRule(rule))
			}
		case domainearnrule.TypeCampaignMultiplier:
			if !matchesSKU(rule, in.SKUID) {
				continue
			}
			if rule.Stackable {
				campaignMultiplier *= rule.Multiplier
				applied = append(applied, appliedRule(rule))
				continue
			}
			if bestNonStackable == nil || outranks(rule, *bestNonStackable) {
				best := rule
				bestNonStackable = &best
			}
		}
	}
	if bestNonStackable != nil {
		campaignMultiplier *= bestNonStackable.Multiplier
		applied = append(applied, appliedRule(*bestNonStackable))
	}

	// The epsilon keeps 1.1 × 10 from flooring to 10 instead of 11.
	points := int64(math.Floor(float64(in.BasePoints)*tierMultiplier*campaignMultiplier + 1e-9))
	if points < 0 {
		points = 0
	}
	return CalculateResult{Points: points, Rules: applied}, nil
}

func appliedRule(r domainearnrule.EarnRule) AppliedRule {
	return AppliedRule{ID: r.ID, Type: r.Type, Multiplier: r.Multiplier}
}

// tierApplied reports whether a tier multiplier is already in the result.
func tierApplied(applied []AppliedRule) bool {
	for _, a := range applied {
		if a.Type == domainearnrule.TypeTierMultiplier {
			return true
		}
	}
	return false
}

func matchesSKU(rule domainearnrule.EarnRule, sku string) bool {
	if len(rule.SKUIDs) == 0 {
		return true
	}
	if sku == "" {
		return false
	}
	for _, want := range rule.SKUIDs {
		if want == sku {
			return true
		}
	}
	return false
}

// outranks reports whether a wins over b: higher priority, then lower id.
func outranks(a, b domainearnrule.EarnRule) bool {
	if a.Priority != b.Priority {
		return a.Priority > b.Priority
	}
	return a.ID < b.ID
}
