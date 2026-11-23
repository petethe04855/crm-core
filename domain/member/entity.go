// Package member defines CRM members (customers who registered through the
// LINE OA) and their linked LINE accounts.
package member

import (
	"context"
	"time"
)

// Member status values.
const (
	StatusActive    = "active"
	StatusSuspended = "suspended"
	StatusDeleted   = "deleted"
)

// Gender values. Unspecified is the default; the form never pre-selects one.
const (
	GenderMale        = "male"
	GenderFemale      = "female"
	GenderOther       = "other"
	GenderUnspecified = "unspecified"
)

// Member is a CRM member. Email is the verified identity (OTP by email);
// phone is optional contact data and is never treated as verified.
type Member struct {
	ID              uint   `gorm:"primaryKey"`
	MemberCode      string `gorm:"size:20;uniqueIndex;not null"`
	FirstName       string `gorm:"size:100;not null"`
	LastName        string `gorm:"size:100;not null"`
	Email           string `gorm:"size:255;uniqueIndex;not null"`
	EmailVerifiedAt *time.Time
	PhoneE164       *string    `gorm:"size:20;index"`
	BirthDate       *time.Time `gorm:"type:date"`
	// BirthDateChangedAt is set the one time a member corrects their birth
	// date (birthday bonuses depend on it, so it is not freely editable).
	BirthDateChangedAt *time.Time
	Gender             string  `gorm:"size:20;not null;default:unspecified"`
	TierID             uint    `gorm:"index;not null"`
	PointBalance       int64   `gorm:"not null;default:0"`
	LifetimeSpend      float64 `gorm:"type:numeric(14,2);not null;default:0"`
	// CustomerID references a customer in the ERP database (no cross-DB FK).
	CustomerID        *uint  `gorm:"index"`
	Status            string `gorm:"size:20;not null;default:active;index"`
	RegisteredChannel string `gorm:"size:20;not null;default:line"`
	// LastActiveAt is written by api-application on each LIFF session; the
	// backoffice only reads it.
	LastActiveAt *time.Time `gorm:"index"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
	DeletedAt    *time.Time `gorm:"index"`
}

// FullName returns "first last".
func (m Member) FullName() string { return m.FirstName + " " + m.LastName }

// MemberLineAccount links a LINE user to a member. A LINE user maps to at most
// one member and a member has at most one active LINE account.
type MemberLineAccount struct {
	ID          uint   `gorm:"primaryKey"`
	MemberID    uint   `gorm:"index;not null"`
	LineUserID  string `gorm:"size:64;uniqueIndex;not null"`
	DisplayName string `gorm:"size:255"`
	PictureURL  string `gorm:"size:1000"`
	IsFriend    bool   `gorm:"not null;default:false"`
	LinkedAt    time.Time
	UnlinkedAt  *time.Time
}

// MemberTag is a backoffice label. Tags are unique per member.
type MemberTag struct {
	ID        uint   `gorm:"primaryKey"`
	MemberID  uint   `gorm:"not null;uniqueIndex:ux_member_tags_member_tag,priority:1"`
	Tag       string `gorm:"size:40;not null;uniqueIndex:ux_member_tags_member_tag,priority:2"`
	CreatedBy uint   `gorm:"not null;default:0"`
	CreatedAt time.Time
}

// MemberNote is an internal note written by staff. Notes are never shown to
// the member.
type MemberNote struct {
	ID             uint   `gorm:"primaryKey"`
	MemberID       uint   `gorm:"index;not null"`
	Body           string `gorm:"type:text;not null"`
	CreatedBy      uint   `gorm:"not null;default:0"`
	CreatedByEmail string `gorm:"size:255"`
	CreatedAt      time.Time
}

// Query filters the member list.
type Query struct {
	Search         string // matches member code, name, email, phone
	TierID         uint
	Status         string
	RegisteredFrom *time.Time
	RegisteredTo   *time.Time // exclusive upper bound
	MinPoints      *int64     // inclusive lower bound on the cached balance
	MaxPoints      *int64     // inclusive upper bound on the cached balance
	InactiveBefore *time.Time // last activity (or registration) strictly before this instant
	Page           int
	Limit          int
}

// Repository is the persistence contract for members.
type Repository interface {
	FindAll(ctx context.Context, q Query) ([]Member, int64, error)
	FindByID(ctx context.Context, id uint) (*Member, error)
	FindActiveLineAccount(ctx context.Context, memberID uint) (*MemberLineAccount, error)
	UpdateStatus(ctx context.Context, id uint, status string) error
	UnlinkActiveLineAccount(ctx context.Context, memberID uint) error
	FindTags(ctx context.Context, memberID uint) ([]MemberTag, error)
	AddTag(ctx context.Context, tag *MemberTag) error
	RemoveTag(ctx context.Context, memberID uint, tag string) error
	FindNotes(ctx context.Context, memberID uint, page, limit int) ([]MemberNote, int64, error)
	AddNote(ctx context.Context, note *MemberNote) error
}

// IdentityRepository resolves who a LINE user or an email belongs to. The
// customer-facing auth flow uses it; it is separate from Repository so the
// backoffice contract stays small.
type IdentityRepository interface {
	// FindByLineUserID returns the member and the active LINE link of a LINE
	// user, or domain.ErrNotFound when the LINE user is not linked.
	FindByLineUserID(ctx context.Context, lineUserID string) (*Member, *MemberLineAccount, error)
	FindByID(ctx context.Context, id uint) (*Member, error)
	// FindByEmail matches the normalized (lowercase, trimmed) email.
	FindByEmail(ctx context.Context, email string) (*Member, error)
}
