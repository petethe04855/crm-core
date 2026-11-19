package points

import (
	"context"
	"errors"
	"strings"
	"time"

	"crm-core/domain"
	domainpoints "crm-core/domain/points"
)

// Wiring errors, not user errors.
var (
	// errMembersNotConfigured means the usecase was built without WithMembers
	// while an earn still needs the member's tier.
	errMembersNotConfigured = errors.New("points: member repository is not configured")
	errLedgerNotConfigured  = errors.New("points: ledger repository is not configured")
)

// EarnRequest credits the points earned by one source (a product code, an ERP
// order). BasePoints is the lot's points_per_code; the calculator applies the
// tier and campaign rules on top.
type EarnRequest struct {
	MemberID   uint
	SourceType string
	SourceID   string
	BasePoints int64
	SKUID      string
	Note       string
	// At is when the points were earned (zero means now); it selects the
	// campaign window and starts the expiry period.
	At time.Time
}

// Earn calculates the award (plan §4.1) and writes the ledger row plus its lot
// in one transaction. A retried source returns domain.ErrConflict instead of
// crediting twice.
func (u *usecase) Earn(ctx context.Context, req EarnRequest) (*domainpoints.PointLedger, error) {
	if u.members == nil {
		return nil, errMembersNotConfigured
	}
	if req.MemberID == 0 {
		return nil, domain.NewValidationError("memberId", "is required")
	}
	if req.BasePoints < 0 {
		return nil, domain.NewValidationError("basePoints", "must not be negative")
	}
	if err := validateSource(req.SourceType, req.SourceID); err != nil {
		return nil, err
	}
	at := req.At
	if at.IsZero() {
		at = u.now()
	}

	member, err := u.members.FindByID(ctx, req.MemberID)
	if err != nil {
		return nil, err
	}
	result, err := u.Calculate(ctx, CalculateInput{
		TierID:     member.TierID,
		SKUID:      req.SKUID,
		BasePoints: req.BasePoints,
		At:         at,
	})
	if err != nil {
		return nil, err
	}
	expiresAt, err := u.expiryAt(ctx, at)
	if err != nil {
		return nil, err
	}

	ruleIDs := make([]uint, 0, len(result.Rules))
	for _, rule := range result.Rules {
		ruleIDs = append(ruleIDs, rule.ID)
	}
	return u.ledger.Earn(ctx, domainpoints.EarnCommand{
		MemberID:   member.ID,
		Points:     result.Points,
		SourceType: req.SourceType,
		SourceID:   req.SourceID,
		Note:       req.Note,
		RuleIDs:    ruleIDs,
		ExpiresAt:  expiresAt,
	})
}

// SpendRequest deducts points for a redemption. Points is the positive cost.
type SpendRequest struct {
	MemberID   uint
	Points     int64
	SourceType string
	SourceID   string
	Note       string
	At         time.Time
}

// Spend runs the guard and then deducts. Use it when the redemption owns no
// transaction of its own; T4/T6 call Check and SpendInTx instead.
func (u *usecase) Spend(ctx context.Context, req SpendRequest) (*domainpoints.PointLedger, error) {
	if u.members == nil {
		return nil, errMembersNotConfigured
	}
	if req.MemberID == 0 {
		return nil, domain.NewValidationError("memberId", "is required")
	}
	if req.Points <= 0 {
		return nil, domain.NewValidationError("points", "must be greater than zero")
	}
	at := req.At
	if at.IsZero() {
		at = u.now()
	}
	member, err := u.members.FindByID(ctx, req.MemberID)
	if err != nil {
		return nil, err
	}
	if err := u.Check(ctx, member, req.Points, at); err != nil {
		return nil, err
	}
	return u.SpendInTx(ctx, u.ledger, req)
}

// SpendInTx deducts points through a repository the caller bound to its own
// transaction. The caller is responsible for running Check first; the
// repository still re-reads the balance under a row lock, so a race can only
// fail with ErrInsufficientPoints, never take the balance negative.
func (u *usecase) SpendInTx(ctx context.Context, repo domainpoints.Repository, req SpendRequest) (*domainpoints.PointLedger, error) {
	if repo == nil {
		return nil, errLedgerNotConfigured
	}
	if req.MemberID == 0 {
		return nil, domain.NewValidationError("memberId", "is required")
	}
	if req.Points <= 0 {
		return nil, domain.NewValidationError("points", "must be greater than zero")
	}
	if err := validateSource(req.SourceType, req.SourceID); err != nil {
		return nil, err
	}
	return repo.Spend(ctx, domainpoints.SpendCommand{
		MemberID:   req.MemberID,
		Points:     req.Points,
		SourceType: req.SourceType,
		SourceID:   req.SourceID,
		Note:       req.Note,
	})
}

// RefundRequest returns points after a cancelled redemption. Points is the
// positive amount to give back.
type RefundRequest struct {
	MemberID   uint
	Points     int64
	SourceType string
	SourceID   string
	Note       string
	At         time.Time
}

// Refund returns points and opens a fresh lot, so the refunded points expire
// from today rather than inheriting the cancelled lot's date.
func (u *usecase) Refund(ctx context.Context, req RefundRequest) (*domainpoints.PointLedger, error) {
	return u.RefundInTx(ctx, u.ledger, req)
}

// RefundInTx is Refund through a repository bound to the caller's transaction.
func (u *usecase) RefundInTx(ctx context.Context, repo domainpoints.Repository, req RefundRequest) (*domainpoints.PointLedger, error) {
	if repo == nil {
		return nil, errLedgerNotConfigured
	}
	if req.MemberID == 0 {
		return nil, domain.NewValidationError("memberId", "is required")
	}
	if req.Points <= 0 {
		return nil, domain.NewValidationError("points", "must be greater than zero")
	}
	if err := validateSource(req.SourceType, req.SourceID); err != nil {
		return nil, err
	}
	at := req.At
	if at.IsZero() {
		at = u.now()
	}
	expiresAt, err := u.expiryAt(ctx, at)
	if err != nil {
		return nil, err
	}
	return repo.Refund(ctx, domainpoints.RefundCommand{
		MemberID:   req.MemberID,
		Points:     req.Points,
		SourceType: req.SourceType,
		SourceID:   req.SourceID,
		Note:       req.Note,
		ExpiresAt:  expiresAt,
	})
}

// ExpireDue writes the expire entries for every due lot. Running it twice in a
// row is safe: the second run finds no lot with points left.
func (u *usecase) ExpireDue(ctx context.Context, now time.Time) (int, error) {
	if now.IsZero() {
		now = u.now()
	}
	return u.ledger.ExpireDue(ctx, now)
}

// expiryAt turns the point_expiry policy into the lot's expiry instant. A zero
// month count means the points never expire.
func (u *usecase) expiryAt(ctx context.Context, at time.Time) (*time.Time, error) {
	policy, err := u.ExpiryPolicy(ctx)
	if err != nil {
		return nil, err
	}
	if policy.Months <= 0 {
		return nil, nil
	}
	expires := at.AddDate(0, policy.Months, 0)
	return &expires, nil
}

func validateSource(sourceType, sourceID string) error {
	if strings.TrimSpace(sourceType) == "" {
		return domain.NewValidationError("sourceType", "is required")
	}
	if strings.TrimSpace(sourceID) == "" {
		return domain.NewValidationError("sourceID", "is required")
	}
	return nil
}
