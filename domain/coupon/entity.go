// Package coupon holds the coupon templates staff configure and the coupons
// members hold (plan CRM_BACKOFFICE_MARKETING.md §8).
package coupon

import (
	"context"
	"errors"
	"time"

	domainmember "crm-core/domain/member"
	domainpoints "crm-core/domain/points"
)

// Benefit types.
const (
	BenefitAmountOff    = "amount_off"
	BenefitPercentOff   = "percent_off"
	BenefitFreeShipping = "free_shipping"
	BenefitFreeItem     = "free_item"
)

// Expiry modes: one date for every coupon, or N days after each claim.
const (
	ExpiryFixedDate      = "fixed_date"
	ExpiryDaysAfterClaim = "days_after_claim"
)

// Code types: unique = one code per holder (verifiable); shared = one code
// printed for everybody (marketplace shops), which the CRM cannot verify.
const (
	CodeUnique = "unique"
	CodeShared = "shared"
)

// Channels where a coupon is used.
const (
	ChannelStore  = "store"
	ChannelLine   = "line"
	ChannelShopee = "shopee"
	ChannelTikTok = "tiktok"
)

// Template statuses.
const (
	StatusDraft  = "draft"
	StatusActive = "active"
	StatusPaused = "paused"
	StatusEnded  = "ended"
)

// Member coupon statuses.
const (
	HolderAvailable = "available"
	HolderUsed      = "used"
	HolderExpired   = "expired"
	HolderCancelled = "cancelled"
)

// Limits.
const (
	MaxNameRunes  = 120
	MaxTextRunes  = 2000
	MaxValidDays  = 365
	MaxQuota      = 1_000_000
	MaxPointsCost = 10_000_000
	CodeLength    = 10
	// CodeAlphabet leaves out characters staff confuse when reading aloud (0/O, 1/I).
	CodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
)

// Errors the delivery layer maps to 409 with their own codes.
var (
	ErrQuotaReached = errors.New("coupon quota reached")
	ErrLimitReached = errors.New("member coupon limit reached")
	ErrNotActive    = errors.New("coupon is not open for claiming")
	ErrExpired      = errors.New("coupon has expired")
	ErrAlreadyUsed  = errors.New("coupon was already used")
	ErrNotUsable    = errors.New("coupon cannot be used")
	ErrTierTooLow   = errors.New("member tier is below the coupon minimum")
)

// Coupon is a template (table crm.coupons).
type Coupon struct {
	ID          uint   `gorm:"primaryKey"`
	Name        string `gorm:"size:120;not null"`
	Description string `gorm:"type:text"`
	ImageURL    string `gorm:"size:500"`
	Terms       string `gorm:"type:text"`

	BenefitType  string   `gorm:"size:20;not null"`
	BenefitValue float64  `gorm:"type:numeric(12,2);not null"`
	MaxDiscount  *float64 `gorm:"type:numeric(12,2)"`
	MinSpend     float64  `gorm:"type:numeric(12,2);not null"`

	// PointsCost 0 means the coupon is a gift (claimed without points).
	PointsCost int64 `gorm:"not null;index"`

	ClaimStartAt *time.Time
	ClaimEndAt   *time.Time

	ExpiryMode string     `gorm:"size:20;not null"`
	ExpiresAt  *time.Time // fixed_date: every coupon; days_after_claim: optional cap
	ValidDays  int        `gorm:"not null"`

	// TotalQuota 0 means unlimited.
	TotalQuota     int   `gorm:"not null"`
	ClaimedCount   int   `gorm:"not null"`
	UsedCount      int   `gorm:"not null"`
	LimitPerMember int   `gorm:"not null"`
	MinTierID      *uint `gorm:"index"`

	Channel    string `gorm:"size:20;not null"`
	CodeType   string `gorm:"size:10;not null"`
	SharedCode string `gorm:"size:40"`

	Status string `gorm:"size:10;not null;index"`

	CreatedBy      uint   `gorm:"not null"`
	CreatedByEmail string `gorm:"size:255"`
	UpdatedBy      uint   `gorm:"not null"`
	UpdatedByEmail string `gorm:"size:255"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// MemberCoupon is one coupon a member holds (table crm.member_coupons). Code is
// unique for every row, also for shared templates, so staff can always look a
// holder up; members of a shared template are shown the template's SharedCode.
type MemberCoupon struct {
	ID        uint      `gorm:"primaryKey"`
	CouponID  uint      `gorm:"not null;index:idx_member_coupons_coupon_status,priority:1"`
	MemberID  uint      `gorm:"not null;index:idx_member_coupons_member_status,priority:1"`
	Code      string    `gorm:"size:20;not null;uniqueIndex"`
	Status    string    `gorm:"size:10;not null;index:idx_member_coupons_coupon_status,priority:2;index:idx_member_coupons_member_status,priority:2"`
	ClaimedAt time.Time `gorm:"not null"`
	ExpiresAt time.Time `gorm:"not null;index"`
	// PointsSpent is what the member paid, kept for the refund.
	PointsSpent int64 `gorm:"not null"`
	LedgerID    *uint
	GrantedBy   uint `gorm:"not null"`

	UsedAt      *time.Time
	UsedRef     string `gorm:"size:100"`
	UsedBy      uint   `gorm:"not null"`
	UsedByEmail string `gorm:"size:255"`

	CancelledAt    *time.Time
	CancelReason   string `gorm:"size:500"`
	CancelledBy    uint   `gorm:"not null"`
	CancelledEmail string `gorm:"size:255"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Query filters the template list. Overlap* (half-open, computed by the
// usecase) keeps templates whose claim window overlaps the range.
type Query struct {
	Search      string
	Status      string
	Channel     string
	OverlapFrom *time.Time
	OverlapTo   *time.Time
	Page        int
	Limit       int
}

// HolderQuery filters member coupons.
type HolderQuery struct {
	CouponID uint
	MemberID uint
	Status   string
	Page     int
	Limit    int
}

// Holder is a member coupon with its member and template for display.
type Holder struct {
	MemberCoupon
	MemberCode  string
	FirstName   string
	LastName    string
	Email       string
	Phone       *string
	CouponName  string
	BenefitType string
	SharedCode  string
}

// Repository stores templates and member coupons. Lock* read the row FOR
// UPDATE (Postgres); callers use them inside UnitOfWork.Do.
type Repository interface {
	FindAll(ctx context.Context, q Query) ([]Coupon, int64, error)
	FindByID(ctx context.Context, id uint) (*Coupon, error)
	LockByID(ctx context.Context, id uint) (*Coupon, error)
	Create(ctx context.Context, c *Coupon) error
	Update(ctx context.Context, c *Coupon) error
	Delete(ctx context.Context, id uint) error

	CountActiveClaims(ctx context.Context, couponID, memberID uint) (int64, error)
	CreateMemberCoupon(ctx context.Context, mc *MemberCoupon) error
	FindMemberCouponByCode(ctx context.Context, code string) (*MemberCoupon, error)
	LockMemberCoupon(ctx context.Context, id uint) (*MemberCoupon, error)
	LockMemberCouponByCode(ctx context.Context, code string) (*MemberCoupon, error)
	UpdateMemberCoupon(ctx context.Context, mc *MemberCoupon) error
	FindHolders(ctx context.Context, q HolderQuery) ([]Holder, int64, error)
	// ExpireDue marks available coupons past their expiry as expired.
	ExpireDue(ctx context.Context, now time.Time) (int64, error)
}

// TxRepos are repositories bound to one database transaction.
type TxRepos struct {
	Coupons Repository
	Points  domainpoints.Repository
	Members domainmember.Repository
}

// UnitOfWork runs fn in one transaction; any error rolls everything back.
type UnitOfWork interface {
	Do(ctx context.Context, fn func(ctx context.Context, r TxRepos) error) error
}
