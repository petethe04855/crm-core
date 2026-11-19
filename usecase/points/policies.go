package points

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"crm-core/domain"
	domainmember "crm-core/domain/member"
	domainsettings "crm-core/domain/settings"
)

// errSettingsNotConfigured means the usecase was built without a settings
// repository, which is a wiring bug rather than a user error.
var errSettingsNotConfigured = errors.New("points: settings repository is not configured")

// RedeemPolicy is the central condition every redemption must pass (plan §4.2).
// A zero value means "no limit", except AllowedStatuses, which defaults to
// ["active"].
type RedeemPolicy struct {
	MinBalanceToRedeem   int64    `json:"minBalanceToRedeem"`
	MinMemberAgeDays     int      `json:"minMemberAgeDays"`
	MaxRedemptionsPerDay int      `json:"maxRedemptionsPerDay"`
	RequireEmailVerified bool     `json:"requireEmailVerified"`
	AllowedStatuses      []string `json:"allowedStatuses"`
}

// DefaultRedeemPolicy is deliberately permissive: the owner opts into fraud
// guards instead of being blocked by defaults nobody chose.
func DefaultRedeemPolicy() RedeemPolicy {
	return RedeemPolicy{AllowedStatuses: []string{domainmember.StatusActive}}
}

// RedeemPolicy reads the stored policy, falling back to the defaults when the
// key is missing or unreadable.
func (u *usecase) RedeemPolicy(ctx context.Context) (RedeemPolicy, error) {
	policy := DefaultRedeemPolicy()
	ok, err := u.readJSON(ctx, domainsettings.KeyRedeemPolicy, &policy)
	if err != nil {
		return DefaultRedeemPolicy(), err
	}
	if !ok {
		return policy, nil
	}
	return policy.normalized(), nil
}

// PutRedeemPolicy validates and stores the policy.
func (u *usecase) PutRedeemPolicy(ctx context.Context, actor domain.Actor, p RedeemPolicy) (RedeemPolicy, error) {
	if u.settings == nil {
		return RedeemPolicy{}, errSettingsNotConfigured
	}
	if !actor.IsOwner() {
		return RedeemPolicy{}, domain.ErrForbidden
	}
	if err := p.validate(); err != nil {
		return RedeemPolicy{}, err
	}
	p = p.normalized()
	if err := u.putJSON(ctx, domainsettings.KeyRedeemPolicy, p, actor.ERPUserID); err != nil {
		return RedeemPolicy{}, err
	}
	return p, nil
}

// normalized trims and bounds what the guard reads. It is forgiving on read: a
// hand-edited row still produces a usable policy.
func (p RedeemPolicy) normalized() RedeemPolicy {
	if p.MinBalanceToRedeem < 0 {
		p.MinBalanceToRedeem = 0
	}
	if p.MinMemberAgeDays < 0 {
		p.MinMemberAgeDays = 0
	}
	if p.MaxRedemptionsPerDay < 0 {
		p.MaxRedemptionsPerDay = 0
	}
	p.AllowedStatuses = normalizeStatuses(p.AllowedStatuses)
	return p
}

// validate rejects input that would make the guard confusing.
func (p RedeemPolicy) validate() error {
	if p.MinBalanceToRedeem < 0 {
		return domain.NewValidationError("minBalanceToRedeem", "must not be negative")
	}
	if p.MinBalanceToRedeem > 1_000_000_000 {
		return domain.NewValidationError("minBalanceToRedeem", "is unreasonably large")
	}
	if p.MinMemberAgeDays < 0 || p.MinMemberAgeDays > 3650 {
		return domain.NewValidationError("minMemberAgeDays", "must be between 0 and 3650")
	}
	if p.MaxRedemptionsPerDay < 0 || p.MaxRedemptionsPerDay > 1000 {
		return domain.NewValidationError("maxRedemptionsPerDay", "must be between 0 and 1000")
	}
	if len(p.AllowedStatuses) == 0 {
		return domain.NewValidationError("allowedStatuses", "at least one status is required")
	}
	for _, status := range p.AllowedStatuses {
		if !validMemberStatus(strings.ToLower(strings.TrimSpace(status))) {
			return domain.NewValidationError("allowedStatuses",
				fmt.Sprintf("unknown member status %q", status))
		}
	}
	return nil
}

// ExpiryPolicy is the point lifetime (plan §4.3). Months == 0 means points
// never expire.
type ExpiryPolicy struct {
	Mode   string `json:"mode"`
	Months int    `json:"months"`
}

// ExpiryModeRollingMonths is the only supported mode: a lot expires N calendar
// months after it was earned.
const ExpiryModeRollingMonths = "rolling_months"

// DefaultExpiryPolicy mirrors the decision recorded in the work order.
func DefaultExpiryPolicy() ExpiryPolicy {
	return ExpiryPolicy{Mode: ExpiryModeRollingMonths, Months: 12}
}

// ExpiryPolicy reads the stored lifetime, falling back to the defaults when the
// key is missing or unreadable.
func (u *usecase) ExpiryPolicy(ctx context.Context) (ExpiryPolicy, error) {
	policy := DefaultExpiryPolicy()
	ok, err := u.readJSON(ctx, domainsettings.KeyPointExpiry, &policy)
	if err != nil {
		return DefaultExpiryPolicy(), err
	}
	if !ok {
		return policy, nil
	}
	if policy.Mode == "" {
		policy.Mode = ExpiryModeRollingMonths
	}
	if policy.Months < 0 {
		policy.Months = DefaultExpiryPolicy().Months
	}
	return policy, nil
}

// PutExpiryPolicy validates and stores the lifetime.
func (u *usecase) PutExpiryPolicy(ctx context.Context, actor domain.Actor, p ExpiryPolicy) (ExpiryPolicy, error) {
	if u.settings == nil {
		return ExpiryPolicy{}, errSettingsNotConfigured
	}
	if !actor.IsOwner() {
		return ExpiryPolicy{}, domain.ErrForbidden
	}
	p.Mode = strings.ToLower(strings.TrimSpace(p.Mode))
	if p.Mode == "" {
		p.Mode = ExpiryModeRollingMonths
	}
	if p.Mode != ExpiryModeRollingMonths {
		return ExpiryPolicy{}, domain.NewValidationError("mode",
			"must be rolling_months (the only supported mode)")
	}
	if p.Months < 0 || p.Months > 120 {
		return ExpiryPolicy{}, domain.NewValidationError("months", "must be between 0 and 120")
	}
	if err := u.putJSON(ctx, domainsettings.KeyPointExpiry, p, actor.ERPUserID); err != nil {
		return ExpiryPolicy{}, err
	}
	return p, nil
}

// PutAdjustPolicy stores the manual-adjustment caps. It reuses the reading rules
// of Policy so the next read returns exactly what was written.
func (u *usecase) PutAdjustPolicy(ctx context.Context, actor domain.Actor, p Policy) (Policy, error) {
	if u.settings == nil {
		return Policy{}, errSettingsNotConfigured
	}
	if !actor.IsOwner() {
		return Policy{}, domain.ErrForbidden
	}
	if p.MaxPerAdjust <= 0 {
		return Policy{}, domain.NewValidationError("maxPerAdjust", "must be greater than zero")
	}
	if p.MaxPerDay <= 0 {
		return Policy{}, domain.NewValidationError("maxPerDay", "must be greater than zero")
	}
	if p.MaxPerAdjust > p.MaxPerDay {
		return Policy{}, domain.NewValidationError("maxPerAdjust", "must not exceed maxPerDay")
	}
	if p.MaxPerDay > 1_000_000 {
		return Policy{}, domain.NewValidationError("maxPerDay", "is unreasonably large")
	}
	if err := u.putJSON(ctx, domainsettings.KeyPointAdjustPolicy, p, actor.ERPUserID); err != nil {
		return Policy{}, err
	}
	return p, nil
}

// readJSON loads one settings document. It reports whether a usable row
// existed; a missing row and unreadable JSON both mean "use the defaults".
func (u *usecase) readJSON(ctx context.Context, key string, v any) (bool, error) {
	if u.settings == nil {
		return false, nil
	}
	raw, err := u.settings.Get(ctx, key)
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, err
	}
	if jsonErr := json.Unmarshal([]byte(raw), v); jsonErr != nil {
		return false, nil
	}
	return true, nil
}

func (u *usecase) putJSON(ctx context.Context, key string, v any, actorID uint) error {
	if u.settings == nil {
		return errSettingsNotConfigured
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return u.settings.Put(ctx, key, string(raw), actorID)
}

// normalizeStatuses lower-cases, de-duplicates and drops unknown statuses. An
// empty result falls back to the default so a hand-edited row cannot lock
// everybody out.
func normalizeStatuses(statuses []string) []string {
	seen := make(map[string]struct{}, len(statuses))
	out := make([]string, 0, len(statuses))
	for _, raw := range statuses {
		status := strings.ToLower(strings.TrimSpace(raw))
		if !validMemberStatus(status) {
			continue
		}
		if _, dup := seen[status]; dup {
			continue
		}
		seen[status] = struct{}{}
		out = append(out, status)
	}
	if len(out) == 0 {
		return DefaultRedeemPolicy().AllowedStatuses
	}
	return out
}

func validMemberStatus(status string) bool {
	switch status {
	case domainmember.StatusActive, domainmember.StatusSuspended, domainmember.StatusDeleted:
		return true
	}
	return false
}
