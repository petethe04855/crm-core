package productcode

import (
	"context"
	"errors"
	"time"

	domainmember "crm-core/domain/member"
	domainpoints "crm-core/domain/points"
)

// KeyPolicy is the settings key of the member-side limits.
const KeyPolicy = "product_code_policy"

// Policy holds the redeem limits (plan §5.4). Owners change it in the
// backoffice; Default applies when it was never saved.
type Policy struct {
	DailyQuota   int `json:"dailyQuota"`   // successful codes per member per Bangkok day
	WrongPerHour int `json:"wrongPerHour"` // not_found tries in 1 hour → locked 1 hour
	WrongPerDay  int `json:"wrongPerDay"`  // not_found tries in 24 hours → locked 24 hours + flagged
}

// DefaultPolicy is the plan's default.
func DefaultPolicy() Policy { return Policy{DailyQuota: 20, WrongPerHour: 5, WrongPerDay: 20} }

// MaxCodesPerRequest bounds one redeem call.
const MaxCodesPerRequest = 10

// Item statuses returned to the member, one per typed code.
const (
	ItemSuccess       = AttemptSuccess
	ItemNotFound      = AttemptNotFound
	ItemUsed          = AttemptUsed
	ItemExpired       = AttemptExpired
	ItemVoid          = AttemptVoid
	ItemInvalidFormat = AttemptInvalidFormat
	ItemDuplicate     = "duplicate"      // typed twice in the same request
	ItemQuotaExceeded = "quota_exceeded" // today's quota ran out mid-request
	ItemLocked        = "locked"         // too many wrong codes, try later
)

// Errors.
var (
	ErrQuotaReached = errors.New("daily code quota reached")
	ErrNoCodes      = errors.New("no codes given")
	ErrTooManyCodes = errors.New("too many codes in one request")
)

// LockedError means the member typed too many wrong codes.
type LockedError struct{ Until time.Time }

func (e *LockedError) Error() string { return "code entry is locked" }

// RedeemTx is one code's redemption, bound to one transaction.
type RedeemTx interface {
	// LockCodeByHash reads the code FOR UPDATE with its batch, or ErrNotFound.
	LockCodeByHash(ctx context.Context, hash string) (*Code, *Batch, error)
	LockMember(ctx context.Context, id uint) (*domainmember.Member, error)
	// CountUsedByMember counts codes the member redeemed before.
	CountUsedByMember(ctx context.Context, memberID uint) (int64, error)
	MarkCodeUsed(ctx context.Context, codeID, memberID, ledgerID uint, at time.Time) error
	// UpdateMemberSpendAndTier sets lifetime_spend and tier_id.
	UpdateMemberSpendAndTier(ctx context.Context, memberID uint, lifetimeSpend float64, tierID uint) error
	AddTierHistory(ctx context.Context, memberID uint, fromTierID *uint, toTierID uint, reason string, at time.Time) error
	CreateAttempt(ctx context.Context, a *RedemptionAttempt) error
	Points() domainpoints.Repository
}

// RedeemUnitOfWork runs one code's redemption in a transaction.
type RedeemUnitOfWork interface {
	Do(ctx context.Context, fn func(ctx context.Context, tx RedeemTx) error) error
}

// HistoryItem is one code the member redeemed.
type HistoryItem struct {
	CodeSuffix string
	SKUName    string
	BatchNo    string
	Points     int64
	UsedAt     time.Time
}

// FlaggedMember typed too many wrong codes in the last 24 hours.
type FlaggedMember struct {
	MemberID   uint
	MemberCode string
	FirstName  string
	LastName   string
	Wrong      int64
	LastAt     time.Time
}

// AttemptRepository reads and writes redemption attempts outside a redeem
// transaction (failures, limits, history, backoffice review).
type AttemptRepository interface {
	CreateAttempt(ctx context.Context, a *RedemptionAttempt) error
	// RecentAttemptTimes returns the times of the member's attempts with the
	// result since `since`, newest first.
	RecentAttemptTimes(ctx context.Context, memberID uint, result string, since time.Time) ([]time.Time, error)
	CountAttempts(ctx context.Context, memberID uint, result string, since time.Time) (int64, error)
	IsTestMember(ctx context.Context, memberID uint) (bool, error)
	// FindBatchByCodeHash reads (without locking) the batch of a code, so the
	// redeemer can prepare the point calculation before its transaction.
	FindBatchByCodeHash(ctx context.Context, hash string) (*Batch, error)
	History(ctx context.Context, memberID uint, page, limit int) ([]HistoryItem, int64, error)
	Flagged(ctx context.Context, since time.Time, minWrong int) ([]FlaggedMember, error)

	ListTestMembers(ctx context.Context) ([]TestMemberView, error)
	AddTestMember(ctx context.Context, memberID, addedBy uint, at time.Time) error
	RemoveTestMember(ctx context.Context, memberID uint) error
}

// TestMemberView is a test member with the member's display data.
type TestMemberView struct {
	MemberID   uint
	MemberCode string
	FirstName  string
	LastName   string
	Email      string
	AddedAt    time.Time
}
