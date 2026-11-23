package member

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm/schema"
)

// MaxAddresses is how many shipping addresses a member may keep.
const MaxAddresses = 10

// Profile errors.
var (
	// ErrBirthDateLocked: the birth date was already corrected once.
	ErrBirthDateLocked = errors.New("birth date can only be changed once")
	// ErrAddressLimit: the member already has MaxAddresses addresses.
	ErrAddressLimit = errors.New("address limit reached")
)

// MemberAddress is a shipping address for reward deliveries. Exactly one of a
// member's addresses is the default once they have any.
type MemberAddress struct {
	ID          uint   `gorm:"primaryKey"`
	MemberID    uint   `gorm:"index;not null"`
	Recipient   string `gorm:"size:100;not null"`
	Phone       string `gorm:"size:20;not null"` // E.164
	Address     string `gorm:"size:500;not null"`
	Subdistrict string `gorm:"size:100;not null"`
	District    string `gorm:"size:100;not null"`
	Province    string `gorm:"size:100;not null"`
	Postcode    string `gorm:"size:5;not null"`
	IsDefault   bool   `gorm:"not null;default:false"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// TableName names the table member_addresses and keeps the schema prefix.
func (MemberAddress) TableName(namer schema.Namer) string { return namer.TableName("MemberAddress") }

// ProfileChanges are the member-editable fields (already validated).
type ProfileChanges struct {
	FirstName          string
	LastName           string
	PhoneE164          *string
	Gender             string
	BirthDate          *time.Time
	BirthDateChangedAt *time.Time
}

// ProfileRepository writes the member's own profile, email and addresses.
type ProfileRepository interface {
	FindMember(ctx context.Context, id uint) (*Member, error)
	UpdateProfile(ctx context.Context, memberID uint, c ProfileChanges) error
	// UpdateEmail changes the email; a duplicate returns ErrEmailInUse.
	UpdateEmail(ctx context.Context, memberID uint, email string, verifiedAt time.Time) error

	ListAddresses(ctx context.Context, memberID uint) ([]MemberAddress, error)
	FindAddress(ctx context.Context, memberID, id uint) (*MemberAddress, error)
	CountAddresses(ctx context.Context, memberID uint) (int64, error)
	// SaveAddress inserts or updates; when a.IsDefault is true every other
	// address of the member is un-defaulted in the same transaction.
	SaveAddress(ctx context.Context, a *MemberAddress) error
	// DeleteAddress removes it; if it was the default, the newest remaining
	// address becomes the default.
	DeleteAddress(ctx context.Context, memberID, id uint) error
}

// ErrEmailInUse: another member already uses the email.
var ErrEmailInUse = errors.New("email is used by another member")
