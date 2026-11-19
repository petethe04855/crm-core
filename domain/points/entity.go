// Package points defines the immutable point ledger and the point lots that
// back expiry. Every balance change in the CRM (earn, redeem, adjust, refund)
// appends one ledger row; nothing is ever updated in place.
package points

import (
	"context"
	"time"
)

// Ledger entry types.
const (
	TypeEarn    = "earn"
	TypeRedeem  = "redeem"
	TypeExpire  = "expire"
	TypeAdjust  = "adjust"
	TypeRefund  = "refund"
	TypeReverse = "reverse"
)

// Source types. admin is a manual backoffice adjustment.
const (
	SourceProductCode = "product_code"
	SourceOrder       = "order"
	SourcePrivilege   = "privilege"
	SourceRedemption  = "redemption"
	SourceCoupon      = "coupon"
	SourceAdmin       = "admin"
	// SourceExpiry is written by the daily expiry job, one row per expired lot.
	SourceExpiry = "expiry"
	// SourceBonus is a one-off earn-rule grant (signup, birthday, first code).
	SourceBonus = "bonus"
)

// Bangkok is the CRM business time zone. Thailand has no DST, so a fixed zone
// avoids depending on tz files in every deployment.
var Bangkok = time.FixedZone("Asia/Bangkok", 7*60*60)

// PointLedger is one immutable balance movement. BalanceAfter is the member
// balance immediately after this row, so statements never need recomputation.
// Unique (source_type, source_id, type) makes retries idempotent.
type PointLedger struct {
	ID           uint   `gorm:"primaryKey"`
	MemberID     uint   `gorm:"index;not null"`
	Type         string `gorm:"size:20;not null;index;uniqueIndex:ux_point_ledger_source,priority:3"`
	Points       int64  `gorm:"not null"`
	BalanceAfter int64  `gorm:"not null"`
	SourceType   string `gorm:"size:30;not null;uniqueIndex:ux_point_ledger_source,priority:1"`
	SourceID     string `gorm:"size:64;not null;uniqueIndex:ux_point_ledger_source,priority:2"`
	// RuleIDs records which earn rules produced an earn entry (plan §4.1), so
	// later rule edits never change the history.
	RuleIDs        []uint `gorm:"serializer:json;type:jsonb"`
	Note           string `gorm:"size:500"`
	CreatedBy      uint   `gorm:"index;not null;default:0"`
	CreatedByEmail string `gorm:"size:255"`
	CreatedAt      time.Time
}

// PointLot tracks earned points that can expire. Redemption consumes the
// oldest lot first (FIFO); expiry work lands with the earn rules in M4.
type PointLot struct {
	ID              uint       `gorm:"primaryKey"`
	MemberID        uint       `gorm:"index;not null"`
	LedgerID        uint       `gorm:"index;not null"`
	PointsEarned    int64      `gorm:"not null"`
	PointsRemaining int64      `gorm:"not null"`
	ExpiresAt       *time.Time `gorm:"index"`
	CreatedAt       time.Time
}

// Query filters a member's ledger.
type Query struct {
	Type  string
	Page  int
	Limit int
}

// AdjustCommand is one manual balance change. Points is signed; a negative
// value may not take the balance below zero.
type AdjustCommand struct {
	MemberID   uint
	Points     int64
	Reason     string
	ActorID    uint
	ActorEmail string
	SourceType string
	SourceID   string
}

// EarnCommand credits points and opens the lot that backs their expiry.
// ExpiresAt is computed by the usecase from the point_expiry setting; nil means
// the points never expire.
type EarnCommand struct {
	MemberID       uint
	Points         int64
	SourceType     string
	SourceID       string
	Note           string
	RuleIDs        []uint
	ExpiresAt      *time.Time
	CreatedBy      uint
	CreatedByEmail string
}

// SpendCommand deducts points. Points is the positive cost; the ledger row
// stores the negative movement. Lots are consumed oldest-expiry-first (FIFO).
type SpendCommand struct {
	MemberID       uint
	Points         int64
	SourceType     string
	SourceID       string
	Note           string
	CreatedBy      uint
	CreatedByEmail string
}

// RefundCommand returns points to a member after a cancelled redemption. The
// refunded points start a fresh lot, so their expiry counts from now.
type RefundCommand struct {
	MemberID       uint
	Points         int64
	SourceType     string
	SourceID       string
	Note           string
	ExpiresAt      *time.Time
	CreatedBy      uint
	CreatedByEmail string
}

// Repository is the persistence contract for point movements.
//
// Adjust, Earn, Spend and Refund each append the ledger row, update the cached
// member balance and (where relevant) the lots in a single database
// transaction, locking the member row so two requests cannot both read the same
// starting balance. A repository built on an open transaction nests with a
// savepoint, which is how coupons (T4) and rewards (T6) redeem inside their own
// transaction.
type Repository interface {
	Adjust(ctx context.Context, cmd AdjustCommand) (*PointLedger, error)
	Earn(ctx context.Context, cmd EarnCommand) (*PointLedger, error)
	Spend(ctx context.Context, cmd SpendCommand) (*PointLedger, error)
	Refund(ctx context.Context, cmd RefundCommand) (*PointLedger, error)
	// ExpireDue writes one expire entry per due lot and zeroes the lot. It is
	// idempotent: a second run finds no lot with points remaining. It returns
	// the number of lots expired.
	ExpireDue(ctx context.Context, now time.Time) (int, error)
	// RedeemCountBetween counts redeem entries for a member in [from, to), used
	// to enforce the per-day redemption cap.
	RedeemCountBetween(ctx context.Context, memberID uint, from, to time.Time) (int64, error)
	List(ctx context.Context, memberID uint, q Query) ([]PointLedger, int64, error)
	// AdjustedAbsBetween sums |points| of manual adjustments by one staff user
	// in [from, to), used to enforce the per-day cap.
	AdjustedAbsBetween(ctx context.Context, actorID uint, from, to time.Time) (int64, error)
}

// ValidType reports whether t is a known ledger type.
func ValidType(t string) bool {
	switch t {
	case TypeEarn, TypeRedeem, TypeExpire, TypeAdjust, TypeRefund, TypeReverse:
		return true
	}
	return false
}
