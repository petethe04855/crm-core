// Package coupon implements coupon management (plan CRM_BACKOFFICE_MARKETING.md
// §8): templates staff configure, claiming with points, using a coupon at the
// store, cancelling with a refund and the daily expiry.
package coupon

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"crm-core/domain"
	domaincoupon "crm-core/domain/coupon"
	domainloyalty "crm-core/domain/loyalty"
	domainmember "crm-core/domain/member"
	domainpoints "crm-core/domain/points"
	"crm-core/timeutil"
	usecasepoints "crm-core/usecase/points"
)

const (
	defaultLimit = 20
	maxLimit     = 100
	// maxHolderExport bounds one holder export.
	maxHolderExport = 50000
)

// Points is what coupons need from the points usecase (T3).
type Points interface {
	Check(ctx context.Context, member *domainmember.Member, cost int64, at time.Time) error
	SpendInTx(ctx context.Context, repo domainpoints.Repository, req usecasepoints.SpendRequest) (*domainpoints.PointLedger, error)
	RefundInTx(ctx context.Context, repo domainpoints.Repository, req usecasepoints.RefundRequest) (*domainpoints.PointLedger, error)
}

// Input is the create/update body; PATCH replaces every editable field.
type Input struct {
	Name           string
	Description    string
	ImageURL       string
	Terms          string
	BenefitType    string
	BenefitValue   float64
	MaxDiscount    *float64
	MinSpend       float64
	PointsCost     int64
	ClaimStartAt   *time.Time
	ClaimEndAt     *time.Time
	ExpiryMode     string
	ExpiresAt      *time.Time
	ValidDays      int
	TotalQuota     int
	LimitPerMember int
	MinTierID      *uint
	Channel        string
	CodeType       string
	SharedCode     string
}

// ClaimRequest claims one coupon for a member. Grant is a backoffice gift: it is
// only allowed for coupons that cost no points.
type ClaimRequest struct {
	MemberID uint
	CouponID uint
	At       time.Time
	Grant    bool
	Actor    domain.Actor
}

// CodeView is what staff see when they look a code up before using it.
type CodeView struct {
	Holder domaincoupon.MemberCoupon
	Coupon domaincoupon.Coupon
	Member domainmember.Member
	// Usable reports whether Use would succeed now.
	Usable bool
	// Reason explains why it is not usable ("used", "expired", "cancelled").
	Reason string
}

// Usecase manages coupons.
type Usecase interface {
	List(ctx context.Context, q domaincoupon.Query, month string) ([]domaincoupon.Coupon, int64, error)
	Get(ctx context.Context, id uint) (*domaincoupon.Coupon, error)
	Create(ctx context.Context, actor domain.Actor, in Input) (*domaincoupon.Coupon, error)
	Update(ctx context.Context, actor domain.Actor, id uint, in Input) (*domaincoupon.Coupon, error)
	Delete(ctx context.Context, id uint) error
	Activate(ctx context.Context, actor domain.Actor, id uint) (*domaincoupon.Coupon, error)
	Pause(ctx context.Context, actor domain.Actor, id uint) (*domaincoupon.Coupon, error)
	End(ctx context.Context, actor domain.Actor, id uint) (*domaincoupon.Coupon, error)

	Claim(ctx context.Context, req ClaimRequest) (*domaincoupon.MemberCoupon, error)
	Lookup(ctx context.Context, code string, at time.Time) (*CodeView, error)
	Use(ctx context.Context, actor domain.Actor, code, usedRef string, at time.Time) (*CodeView, error)
	Cancel(ctx context.Context, actor domain.Actor, memberCouponID uint, reason string, at time.Time) (*domaincoupon.MemberCoupon, error)
	ExpireDue(ctx context.Context, now time.Time) (int64, error)

	Holders(ctx context.Context, q domaincoupon.HolderQuery) ([]domaincoupon.Holder, int64, error)
	ExportHolders(ctx context.Context, couponID uint) ([]domaincoupon.Holder, error)
}

type usecase struct {
	coupons domaincoupon.Repository
	uow     domaincoupon.UnitOfWork
	members domainmember.Repository
	tiers   domainloyalty.TierRepository
	points  Points
	now     func() time.Time
	newCode func() (string, error)
}

// New builds the coupon usecase. members and tiers are read outside the claim
// transaction (see Claim).
func New(coupons domaincoupon.Repository, uow domaincoupon.UnitOfWork, members domainmember.Repository,
	tiers domainloyalty.TierRepository, points Points) Usecase {
	return &usecase{coupons: coupons, uow: uow, members: members, tiers: tiers, points: points, now: time.Now, newCode: RandomCode}
}

// RandomCode returns a CodeLength code from CodeAlphabet using crypto/rand.
func RandomCode() (string, error) {
	var b strings.Builder
	max := big.NewInt(int64(len(domaincoupon.CodeAlphabet)))
	for i := 0; i < domaincoupon.CodeLength; i++ {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b.WriteByte(domaincoupon.CodeAlphabet[n.Int64()])
	}
	return b.String(), nil
}

// ExpiresAt is when a coupon claimed at claimedAt stops being usable (plan
// §8.2): the template date for fixed_date; otherwise the end of the Bangkok day
// claimedAt + ValidDays, capped by the template date when one is set.
func ExpiresAt(c domaincoupon.Coupon, claimedAt time.Time) time.Time {
	if c.ExpiryMode == domaincoupon.ExpiryFixedDate && c.ExpiresAt != nil {
		return *c.ExpiresAt
	}
	end := timeutil.EndOfDay(claimedAt.AddDate(0, 0, c.ValidDays))
	if c.ExpiresAt != nil && c.ExpiresAt.Before(end) {
		return *c.ExpiresAt
	}
	return end
}

// NormalizeQuery applies paging defaults.
func NormalizeQuery(q domaincoupon.Query) domaincoupon.Query {
	q.Search = strings.TrimSpace(q.Search)
	if q.Page < 1 {
		q.Page = 1
	}
	if q.Limit < 1 {
		q.Limit = defaultLimit
	}
	if q.Limit > maxLimit {
		q.Limit = maxLimit
	}
	return q
}

var monthPattern = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)

func (u *usecase) List(ctx context.Context, q domaincoupon.Query, month string) ([]domaincoupon.Coupon, int64, error) {
	q = NormalizeQuery(q)
	if q.Status != "" && !validStatus(q.Status) {
		return nil, 0, domain.NewValidationError("status", "must be one of draft, active, paused, ended")
	}
	if q.Channel != "" && !validChannel(q.Channel) {
		return nil, 0, domain.NewValidationError("channel", "must be one of store, line, shopee, tiktok")
	}
	if month != "" {
		if !monthPattern.MatchString(month) {
			return nil, 0, domain.NewValidationError("month", "must be YYYY-MM")
		}
		var y, m int
		_, _ = fmt.Sscanf(month, "%d-%d", &y, &m)
		from, to := timeutil.MonthRange(y, time.Month(m))
		q.OverlapFrom, q.OverlapTo = &from, &to
	}
	return u.coupons.FindAll(ctx, q)
}

func (u *usecase) Get(ctx context.Context, id uint) (*domaincoupon.Coupon, error) {
	if id == 0 {
		return nil, domain.NewValidationError("id", "is required")
	}
	return u.coupons.FindByID(ctx, id)
}

func (u *usecase) Create(ctx context.Context, actor domain.Actor, in Input) (*domaincoupon.Coupon, error) {
	in = normalizeInput(in)
	if err := u.validate(ctx, in); err != nil {
		return nil, err
	}
	c := &domaincoupon.Coupon{Status: domaincoupon.StatusDraft, CreatedBy: actor.ERPUserID, CreatedByEmail: actor.Email}
	apply(c, in)
	c.UpdatedBy, c.UpdatedByEmail = actor.ERPUserID, actor.Email
	if err := u.coupons.Create(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

func (u *usecase) Update(ctx context.Context, actor domain.Actor, id uint, in Input) (*domaincoupon.Coupon, error) {
	c, err := u.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if c.Status == domaincoupon.StatusEnded {
		return nil, domain.ErrConflict
	}
	in = normalizeInput(in)
	if err := u.validate(ctx, in); err != nil {
		return nil, err
	}
	if c.ClaimedCount > 0 {
		// Members already hold coupons of this template: what the coupon is
		// worth and where it is used must not change under them. Points cost and
		// expiry only affect later claims, so they stay editable.
		if in.BenefitType != c.BenefitType || in.BenefitValue != c.BenefitValue ||
			!sameFloat(in.MaxDiscount, c.MaxDiscount) || in.MinSpend != c.MinSpend ||
			in.Channel != c.Channel || in.CodeType != c.CodeType || in.SharedCode != c.SharedCode {
			return nil, domain.NewValidationError("benefitType",
				"benefit, channel and code cannot change after members claimed this coupon")
		}
		if in.TotalQuota > 0 && in.TotalQuota < c.ClaimedCount {
			return nil, domain.NewValidationError("totalQuota",
				fmt.Sprintf("must be at least the %d coupons already claimed", c.ClaimedCount))
		}
	}
	if c.Status == domaincoupon.StatusActive {
		if err := readyToActivate(in, u.now()); err != nil {
			return nil, err
		}
	}
	apply(c, in)
	c.UpdatedBy, c.UpdatedByEmail = actor.ERPUserID, actor.Email
	if err := u.coupons.Update(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

func (u *usecase) Delete(ctx context.Context, id uint) error {
	c, err := u.Get(ctx, id)
	if err != nil {
		return err
	}
	if c.ClaimedCount > 0 {
		return domain.ErrConflict
	}
	return u.coupons.Delete(ctx, id)
}

func (u *usecase) Activate(ctx context.Context, actor domain.Actor, id uint) (*domaincoupon.Coupon, error) {
	c, err := u.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if c.Status != domaincoupon.StatusDraft && c.Status != domaincoupon.StatusPaused {
		return nil, domain.ErrConflict
	}
	if err := readyToActivate(inputOf(*c), u.now()); err != nil {
		return nil, err
	}
	return u.setStatus(ctx, actor, c, domaincoupon.StatusActive)
}

func (u *usecase) Pause(ctx context.Context, actor domain.Actor, id uint) (*domaincoupon.Coupon, error) {
	c, err := u.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if c.Status != domaincoupon.StatusActive {
		return nil, domain.ErrConflict
	}
	return u.setStatus(ctx, actor, c, domaincoupon.StatusPaused)
}

func (u *usecase) End(ctx context.Context, actor domain.Actor, id uint) (*domaincoupon.Coupon, error) {
	c, err := u.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if c.Status != domaincoupon.StatusActive && c.Status != domaincoupon.StatusPaused {
		return nil, domain.ErrConflict
	}
	return u.setStatus(ctx, actor, c, domaincoupon.StatusEnded)
}

func (u *usecase) setStatus(ctx context.Context, actor domain.Actor, c *domaincoupon.Coupon, status string) (*domaincoupon.Coupon, error) {
	c.Status = status
	c.UpdatedBy, c.UpdatedByEmail = actor.ERPUserID, actor.Email
	if err := u.coupons.Update(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

// Claim gives a member one coupon.
//
// Checks that read through their own connection (tier, the points redemption
// guard with its settings and daily count) run first, outside the transaction,
// so a claim never holds one pooled connection while waiting for another.
// The transaction then locks the template, re-checks window and quota, counts
// the member's coupons, deducts points (the ledger re-reads the balance under
// the member row lock), counts the claim and creates the coupon. Concurrent
// claims serialise on the template row, so the quota is never exceeded and the
// balance never goes negative.
func (u *usecase) Claim(ctx context.Context, req ClaimRequest) (*domaincoupon.MemberCoupon, error) {
	if req.MemberID == 0 || req.CouponID == 0 {
		return nil, domain.NewValidationError("memberId", "member and coupon are required")
	}
	at := req.At
	if at.IsZero() {
		at = u.now()
	}
	// Pre-checks (no transaction held).
	pre, err := u.coupons.FindByID(ctx, req.CouponID)
	if err != nil {
		return nil, err
	}
	if req.Grant && pre.PointsCost > 0 {
		return nil, domain.NewValidationError("couponId", "only coupons that cost no points can be granted")
	}
	if err := claimable(*pre, at); err != nil {
		return nil, err
	}
	member, err := u.members.FindByID(ctx, req.MemberID)
	if err != nil {
		return nil, err
	}
	if member.Status != domainmember.StatusActive {
		return nil, domain.ErrMemberNotAllowed
	}
	if err := u.checkTier(ctx, *pre, *member); err != nil {
		return nil, err
	}
	if pre.PointsCost > 0 {
		if err := u.points.Check(ctx, member, pre.PointsCost, at); err != nil {
			return nil, err
		}
	}

	var out *domaincoupon.MemberCoupon
	err = u.uow.Do(ctx, func(ctx context.Context, r domaincoupon.TxRepos) error {
		c, err := r.Coupons.LockByID(ctx, req.CouponID)
		if err != nil {
			return err
		}
		if req.Grant && c.PointsCost > 0 {
			return domain.NewValidationError("couponId", "only coupons that cost no points can be granted")
		}
		if err := claimable(*c, at); err != nil {
			return err
		}
		m, err := r.Members.FindByID(ctx, req.MemberID)
		if err != nil {
			return err
		}
		held, err := r.Coupons.CountActiveClaims(ctx, c.ID, m.ID)
		if err != nil {
			return err
		}
		if held >= int64(c.LimitPerMember) {
			return domaincoupon.ErrLimitReached
		}

		code, err := u.freshCode(ctx, r.Coupons)
		if err != nil {
			return err
		}
		mc := &domaincoupon.MemberCoupon{
			CouponID: c.ID, MemberID: m.ID, Code: code, Status: domaincoupon.HolderAvailable,
			ClaimedAt: at, ExpiresAt: ExpiresAt(*c, at), GrantedBy: req.Actor.ERPUserID,
		}
		if c.PointsCost > 0 {
			entry, err := u.points.SpendInTx(ctx, r.Points, usecasepoints.SpendRequest{
				MemberID: m.ID, Points: c.PointsCost, SourceType: domainpoints.SourceCoupon,
				SourceID: "claim:" + code, Note: "แลกคูปอง " + c.Name, At: at,
			})
			if err != nil {
				return err
			}
			mc.PointsSpent = c.PointsCost
			mc.LedgerID = &entry.ID
		}
		if err := r.Coupons.CreateMemberCoupon(ctx, mc); err != nil {
			return err
		}
		c.ClaimedCount++
		if err := r.Coupons.Update(ctx, c); err != nil {
			return err
		}
		out = mc
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// freshCode draws codes until one is unused. A failed insert would abort the
// Postgres transaction, so the check happens before the insert; with 32^10
// codes a second draw is already vanishingly rare.
func (u *usecase) freshCode(ctx context.Context, repo domaincoupon.Repository) (string, error) {
	for i := 0; i < 5; i++ {
		code, err := u.newCode()
		if err != nil {
			return "", err
		}
		_, err = repo.FindMemberCouponByCode(ctx, code)
		if errors.Is(err, domain.ErrNotFound) {
			return code, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", errors.New("coupon: could not draw an unused code")
}

func claimable(c domaincoupon.Coupon, at time.Time) error {
	if c.Status != domaincoupon.StatusActive {
		return domaincoupon.ErrNotActive
	}
	if c.ClaimStartAt != nil && at.Before(*c.ClaimStartAt) {
		return domaincoupon.ErrNotActive
	}
	if c.ClaimEndAt != nil && !at.Before(*c.ClaimEndAt) {
		return domaincoupon.ErrNotActive
	}
	if !ExpiresAt(c, at).After(at) {
		return domaincoupon.ErrExpired
	}
	if c.TotalQuota > 0 && c.ClaimedCount >= c.TotalQuota {
		return domaincoupon.ErrQuotaReached
	}
	return nil
}

func (u *usecase) checkTier(ctx context.Context, c domaincoupon.Coupon, m domainmember.Member) error {
	if c.MinTierID == nil || *c.MinTierID == m.TierID || u.tiers == nil {
		return nil
	}
	min, err := u.tiers.FindByID(ctx, *c.MinTierID)
	if err != nil {
		return err
	}
	have, err := u.tiers.FindByID(ctx, m.TierID)
	if err != nil {
		return err
	}
	if have.SortOrder < min.SortOrder {
		return domaincoupon.ErrTierTooLow
	}
	return nil
}

func normalizeCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

func (u *usecase) Lookup(ctx context.Context, code string, at time.Time) (*CodeView, error) {
	code = normalizeCode(code)
	if code == "" {
		return nil, domain.NewValidationError("code", "is required")
	}
	if at.IsZero() {
		at = u.now()
	}
	var view *CodeView
	err := u.uow.Do(ctx, func(ctx context.Context, r domaincoupon.TxRepos) error {
		mc, err := r.Coupons.FindMemberCouponByCode(ctx, code)
		if err != nil {
			return err
		}
		view, err = buildView(ctx, r, *mc, at)
		return err
	})
	return view, err
}

func buildView(ctx context.Context, r domaincoupon.TxRepos, mc domaincoupon.MemberCoupon, at time.Time) (*CodeView, error) {
	c, err := r.Coupons.FindByID(ctx, mc.CouponID)
	if err != nil {
		return nil, err
	}
	m, err := r.Members.FindByID(ctx, mc.MemberID)
	if err != nil {
		return nil, err
	}
	view := &CodeView{Holder: mc, Coupon: *c, Member: *m, Usable: true}
	switch {
	case mc.Status == domaincoupon.HolderUsed:
		view.Usable, view.Reason = false, domaincoupon.HolderUsed
	case mc.Status == domaincoupon.HolderCancelled:
		view.Usable, view.Reason = false, domaincoupon.HolderCancelled
	case mc.Status == domaincoupon.HolderExpired || !at.Before(mc.ExpiresAt):
		view.Usable, view.Reason = false, domaincoupon.HolderExpired
	}
	return view, nil
}

// Use marks a coupon used against a bill. It is idempotent per usedRef: the
// same bill again returns the same result; a different bill is refused.
func (u *usecase) Use(ctx context.Context, actor domain.Actor, code, usedRef string, at time.Time) (*CodeView, error) {
	code = normalizeCode(code)
	usedRef = strings.TrimSpace(usedRef)
	if code == "" {
		return nil, domain.NewValidationError("code", "is required")
	}
	if usedRef == "" || utf8.RuneCountInString(usedRef) > 100 {
		return nil, domain.NewValidationError("usedRef", "bill or order number is required (100 characters max)")
	}
	if at.IsZero() {
		at = u.now()
	}
	var view *CodeView
	err := u.uow.Do(ctx, func(ctx context.Context, r domaincoupon.TxRepos) error {
		mc, err := r.Coupons.LockMemberCouponByCode(ctx, code)
		if err != nil {
			return err
		}
		switch {
		case mc.Status == domaincoupon.HolderUsed && mc.UsedRef == usedRef:
			view, err = buildView(ctx, r, *mc, at)
			return err
		case mc.Status == domaincoupon.HolderUsed:
			return domaincoupon.ErrAlreadyUsed
		case mc.Status == domaincoupon.HolderCancelled:
			return domaincoupon.ErrNotUsable
		case mc.Status == domaincoupon.HolderExpired || !at.Before(mc.ExpiresAt):
			return domaincoupon.ErrExpired
		}
		mc.Status = domaincoupon.HolderUsed
		mc.UsedAt, mc.UsedRef = &at, usedRef
		mc.UsedBy, mc.UsedByEmail = actor.ERPUserID, actor.Email
		if err := r.Coupons.UpdateMemberCoupon(ctx, mc); err != nil {
			return err
		}
		c, err := r.Coupons.LockByID(ctx, mc.CouponID)
		if err != nil {
			return err
		}
		c.UsedCount++
		if err := r.Coupons.Update(ctx, c); err != nil {
			return err
		}
		view, err = buildView(ctx, r, *mc, at)
		return err
	})
	return view, err
}

// Cancel voids an unused coupon, refunds its points and frees its quota slot.
func (u *usecase) Cancel(ctx context.Context, actor domain.Actor, memberCouponID uint, reason string, at time.Time) (*domaincoupon.MemberCoupon, error) {
	reason = strings.TrimSpace(reason)
	if n := utf8.RuneCountInString(reason); n < 3 || n > 500 {
		return nil, domain.NewValidationError("reason", "is required (3–500 characters)")
	}
	if at.IsZero() {
		at = u.now()
	}
	var out *domaincoupon.MemberCoupon
	err := u.uow.Do(ctx, func(ctx context.Context, r domaincoupon.TxRepos) error {
		mc, err := r.Coupons.LockMemberCoupon(ctx, memberCouponID)
		if err != nil {
			return err
		}
		switch mc.Status {
		case domaincoupon.HolderUsed:
			return domaincoupon.ErrAlreadyUsed
		case domaincoupon.HolderCancelled, domaincoupon.HolderExpired:
			return domaincoupon.ErrNotUsable
		}
		mc.Status = domaincoupon.HolderCancelled
		mc.CancelledAt, mc.CancelReason = &at, reason
		mc.CancelledBy, mc.CancelledEmail = actor.ERPUserID, actor.Email
		if err := r.Coupons.UpdateMemberCoupon(ctx, mc); err != nil {
			return err
		}
		c, err := r.Coupons.LockByID(ctx, mc.CouponID)
		if err != nil {
			return err
		}
		if c.ClaimedCount > 0 {
			c.ClaimedCount--
		}
		if err := r.Coupons.Update(ctx, c); err != nil {
			return err
		}
		if mc.PointsSpent > 0 {
			if _, err := u.points.RefundInTx(ctx, r.Points, usecasepoints.RefundRequest{
				MemberID: mc.MemberID, Points: mc.PointsSpent, SourceType: domainpoints.SourceCoupon,
				SourceID: "cancel:" + mc.Code, Note: "ยกเลิกคูปอง: " + reason, At: at,
			}); err != nil {
				return err
			}
		}
		out = mc
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ExpireDue marks coupons past their expiry as expired. Points are not
// refunded (plan §8.2). Running it again changes nothing.
func (u *usecase) ExpireDue(ctx context.Context, now time.Time) (int64, error) {
	return u.coupons.ExpireDue(ctx, now)
}

func (u *usecase) Holders(ctx context.Context, q domaincoupon.HolderQuery) ([]domaincoupon.Holder, int64, error) {
	if q.Status != "" && !validHolderStatus(q.Status) {
		return nil, 0, domain.NewValidationError("status", "must be one of available, used, expired, cancelled")
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
	return u.coupons.FindHolders(ctx, q)
}

func (u *usecase) ExportHolders(ctx context.Context, couponID uint) ([]domaincoupon.Holder, error) {
	if _, err := u.Get(ctx, couponID); err != nil {
		return nil, err
	}
	rows, _, err := u.coupons.FindHolders(ctx, domaincoupon.HolderQuery{CouponID: couponID, Page: 1, Limit: maxHolderExport})
	return rows, err
}

func sameFloat(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
