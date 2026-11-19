// Package points implements the point ledger workflows used by the backoffice:
// reading a member's statement, applying capped manual adjustments, earning and
// spending points under the earn rules, and the central redemption guard that
// coupons and rewards call before deducting.
package points

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"crm-core/domain"
	domainearnrule "crm-core/domain/earnrule"
	domainloyalty "crm-core/domain/loyalty"
	domainmember "crm-core/domain/member"
	domainpoints "crm-core/domain/points"
	domainsettings "crm-core/domain/settings"
)

const (
	defaultLimit = 20
	maxLimit     = 100
	// maxSingleAdjust is a hard sanity bound independent of the policy.
	maxSingleAdjust = 100000
	minReasonRunes  = 3
	maxReasonRunes  = 500
)

// Policy caps manual adjustments for staff who are not owners. Owners are not
// capped; the ERP owner is the trusted account.
type Policy struct {
	MaxPerAdjust int64 `json:"maxPerAdjust"`
	MaxPerDay    int64 `json:"maxPerDay"`
}

// DefaultPolicy is used when crm.settings has no point_adjust_policy row yet.
func DefaultPolicy() Policy { return Policy{MaxPerAdjust: 1000, MaxPerDay: 5000} }

// AdjustRequest is a manual balance change.
type AdjustRequest struct {
	Points int64
	Reason string
}

// Usecase reads the ledger, owns the earning rules and policies, and exposes
// the redemption guard that coupons (T4) and rewards (T6) call.
type Usecase interface {
	Ledger(ctx context.Context, memberID uint, q domainpoints.Query) ([]domainpoints.PointLedger, int64, error)
	Adjust(ctx context.Context, actor domain.Actor, memberID uint, req AdjustRequest) (*domainpoints.PointLedger, error)
	Policy(ctx context.Context) (Policy, error)
	PutAdjustPolicy(ctx context.Context, actor domain.Actor, p Policy) (Policy, error)

	// Earning rules and the calculator (plan §4.1).
	ListEarnRules(ctx context.Context, q domainearnrule.Query) ([]domainearnrule.EarnRule, error)
	CreateEarnRule(ctx context.Context, actor domain.Actor, in EarnRuleInput) (*domainearnrule.EarnRule, error)
	UpdateEarnRule(ctx context.Context, actor domain.Actor, id uint, in EarnRuleInput) (*domainearnrule.EarnRule, error)
	Calculate(ctx context.Context, in CalculateInput) (CalculateResult, error)

	// Policies (plan §4.2 and §4.3).
	RedeemPolicy(ctx context.Context) (RedeemPolicy, error)
	PutRedeemPolicy(ctx context.Context, actor domain.Actor, p RedeemPolicy) (RedeemPolicy, error)
	ExpiryPolicy(ctx context.Context) (ExpiryPolicy, error)
	PutExpiryPolicy(ctx context.Context, actor domain.Actor, p ExpiryPolicy) (ExpiryPolicy, error)

	// Redemption and lifetime. T4/T6 call Check and then SpendInTx/RefundInTx
	// with a repository bound to their own transaction.
	Check(ctx context.Context, member *domainmember.Member, cost int64, at time.Time) error
	Earn(ctx context.Context, req EarnRequest) (*domainpoints.PointLedger, error)
	Spend(ctx context.Context, req SpendRequest) (*domainpoints.PointLedger, error)
	SpendInTx(ctx context.Context, repo domainpoints.Repository, req SpendRequest) (*domainpoints.PointLedger, error)
	Refund(ctx context.Context, req RefundRequest) (*domainpoints.PointLedger, error)
	RefundInTx(ctx context.Context, repo domainpoints.Repository, req RefundRequest) (*domainpoints.PointLedger, error)
	ExpireDue(ctx context.Context, now time.Time) (int, error)
}

type usecase struct {
	ledger   domainpoints.Repository
	settings domainsettings.Repository
	rules    domainearnrule.Repository
	members  domainmember.Repository
	tiers    domainloyalty.TierRepository
	now      func() time.Time
	newID    func() string
}

// Option adds an optional repository. New keeps working without options so the
// existing tests and the member ledger keep their narrow dependencies.
type Option func(*usecase)

// WithEarnRules enables the calculator and the earn-rule screens.
func WithEarnRules(rules domainearnrule.Repository) Option {
	return func(u *usecase) { u.rules = rules }
}

// WithMembers enables Earn and the member lookup the guard needs.
func WithMembers(members domainmember.Repository) Option {
	return func(u *usecase) { u.members = members }
}

// WithTiers lets earn-rule writes refuse a tier that does not exist.
func WithTiers(tiers domainloyalty.TierRepository) Option {
	return func(u *usecase) { u.tiers = tiers }
}

// New builds the points usecase. settings may be nil in tests; the default
// policies then apply.
func New(ledger domainpoints.Repository, settings domainsettings.Repository, opts ...Option) Usecase {
	u := &usecase{
		ledger:   ledger,
		settings: settings,
		now:      time.Now,
		newID:    func() string { return fmt.Sprintf("manual-%d", time.Now().UnixNano()) },
	}
	for _, opt := range opts {
		opt(u)
	}
	return u
}

// Policy reads the adjustment caps, falling back to the defaults when the key
// is missing or unreadable.
func (u *usecase) Policy(ctx context.Context) (Policy, error) {
	policy := DefaultPolicy()
	if u.settings == nil {
		return policy, nil
	}
	raw, err := u.settings.Get(ctx, domainsettings.KeyPointAdjustPolicy)
	switch {
	case err == nil:
		if jsonErr := json.Unmarshal([]byte(raw), &policy); jsonErr != nil {
			return DefaultPolicy(), nil
		}
	case err != nil && !isNotFound(err):
		return policy, err
	}
	if policy.MaxPerAdjust <= 0 {
		policy.MaxPerAdjust = DefaultPolicy().MaxPerAdjust
	}
	if policy.MaxPerDay <= 0 {
		policy.MaxPerDay = DefaultPolicy().MaxPerDay
	}
	return policy, nil
}

func isNotFound(err error) bool {
	return err != nil && (err == domain.ErrNotFound)
}

func (u *usecase) Ledger(ctx context.Context, memberID uint, q domainpoints.Query) ([]domainpoints.PointLedger, int64, error) {
	if memberID == 0 {
		return nil, 0, domain.NewValidationError("id", "is required")
	}
	q.Type = strings.ToLower(strings.TrimSpace(q.Type))
	if q.Type != "" && !domainpoints.ValidType(q.Type) {
		return nil, 0, domain.NewValidationError("type", "unknown ledger type")
	}
	if q.Page < 1 {
		q.Page = 1
	}
	if q.Limit < 1 {
		q.Limit = defaultLimit
	}
	if q.Limit > maxLimit {
		q.Limit = maxLimit
	}
	return u.ledger.List(ctx, memberID, q)
}

func (u *usecase) Adjust(ctx context.Context, actor domain.Actor, memberID uint, req AdjustRequest) (*domainpoints.PointLedger, error) {
	if memberID == 0 {
		return nil, domain.NewValidationError("id", "is required")
	}
	if req.Points == 0 {
		return nil, domain.NewValidationError("points", "must not be zero")
	}
	if req.Points > maxSingleAdjust || req.Points < -maxSingleAdjust {
		return nil, domain.NewValidationError("points", fmt.Sprintf("must be within ±%d", maxSingleAdjust))
	}
	reason := strings.TrimSpace(req.Reason)
	switch n := utf8.RuneCountInString(reason); {
	case n < minReasonRunes:
		return nil, domain.NewValidationError("reason", "is required (at least 3 characters)")
	case n > maxReasonRunes:
		return nil, domain.NewValidationError("reason", "must be 500 characters or fewer")
	}

	policy, err := u.Policy(ctx)
	if err != nil {
		return nil, err
	}
	if !actor.IsOwner() {
		if abs(req.Points) > policy.MaxPerAdjust {
			return nil, domain.NewValidationError("points",
				fmt.Sprintf("exceeds the per-adjust limit of %d points", policy.MaxPerAdjust))
		}
		from, to := bangkokDayBounds(u.now())
		used, err := u.ledger.AdjustedAbsBetween(ctx, actor.ERPUserID, from, to)
		if err != nil {
			return nil, err
		}
		if used+abs(req.Points) > policy.MaxPerDay {
			return nil, domain.NewValidationError("points",
				fmt.Sprintf("exceeds the daily limit of %d points (%d already adjusted today)", policy.MaxPerDay, used))
		}
	}

	return u.ledger.Adjust(ctx, domainpoints.AdjustCommand{
		MemberID:   memberID,
		Points:     req.Points,
		Reason:     reason,
		ActorID:    actor.ERPUserID,
		ActorEmail: actor.Email,
		SourceType: domainpoints.SourceAdmin,
		SourceID:   u.newID(),
	})
}

func bangkokDayBounds(now time.Time) (time.Time, time.Time) {
	local := now.In(domainpoints.Bangkok)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, domainpoints.Bangkok)
	return start, start.AddDate(0, 0, 1)
}

func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}
