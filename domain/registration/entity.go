// Package registration holds the contracts for joining the membership from
// the LINE OA: registering a new member, linking LINE to an existing member,
// and the tier history written when a member enters (or changes) a tier.
package registration

import (
	"context"
	"errors"
	"time"

	domainmember "crm-core/domain/member"
	domainpoints "crm-core/domain/points"

	"gorm.io/gorm/schema"
)

// Tier history reasons.
const (
	TierReasonSignup  = "signup"
	TierReasonUpgrade = "upgrade"
	TierReasonManual  = "manual"
)

// SignupSourcePrefix + member id is the ledger source of the welcome bonus;
// the ledger's unique (source_type, source_id, type) stops a second grant.
const SignupSourcePrefix = "signup:"

// Errors.
var (
	// ErrEmailTaken: the email already belongs to a member (link instead).
	ErrEmailTaken = errors.New("email already belongs to a member")
	// ErrLineAlreadyLinked: this LINE account is linked to a member already.
	ErrLineAlreadyLinked = errors.New("LINE account already linked to a member")
	// ErrEmailNotMember: link was asked for an email that is not a member.
	ErrEmailNotMember = errors.New("email does not belong to a member")
)

// TierHistory records every tier a member enters. FromTierID is nil on signup.
type TierHistory struct {
	ID         uint   `gorm:"primaryKey"`
	MemberID   uint   `gorm:"index;not null"`
	FromTierID *uint  `gorm:"index"`
	ToTierID   uint   `gorm:"index;not null"`
	Reason     string `gorm:"size:30;not null"`
	CreatedAt  time.Time
}

// TableName names the table member_tier_histories and keeps the schema prefix.
func (TierHistory) TableName(namer schema.Namer) string {
	return namer.TableName("MemberTierHistory")
}

// Tx is the set of writes a registration makes, bound to one transaction.
type Tx interface {
	FindMemberByEmail(ctx context.Context, email string) (*domainmember.Member, error)
	// FindActiveLink returns the active link of a LINE user, or ErrNotFound.
	FindActiveLink(ctx context.Context, lineUserID string) (*domainmember.MemberLineAccount, error)
	// FindMemberActiveLink returns the member's active LINE link, or ErrNotFound.
	FindMemberActiveLink(ctx context.Context, memberID uint) (*domainmember.MemberLineAccount, error)
	// LockMember reads the member row FOR UPDATE.
	LockMember(ctx context.Context, id uint) (*domainmember.Member, error)
	// CreateMember inserts the member and assigns member_code from its id
	// (M<YYMM>-<id, 6 digits>). A duplicate email returns ErrEmailTaken.
	CreateMember(ctx context.Context, m *domainmember.Member) error
	// MarkEmailVerified sets email_verified_at.
	MarkEmailVerified(ctx context.Context, memberID uint, at time.Time) error
	// UnlinkLine ends an active link (the row stays as the audit trail).
	UnlinkLine(ctx context.Context, linkID uint, at time.Time) error
	// LinkLine links the LINE user to the member. A LINE user that was linked
	// before (and unlinked) gets its row reused, since line_user_id is unique.
	LinkLine(ctx context.Context, link *domainmember.MemberLineAccount) error
	AddTierHistory(ctx context.Context, h *TierHistory) error
	// Points is the point ledger inside the same transaction.
	Points() domainpoints.Repository
}

// UnitOfWork runs fn in one transaction; any error rolls everything back.
type UnitOfWork interface {
	Do(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error
}
