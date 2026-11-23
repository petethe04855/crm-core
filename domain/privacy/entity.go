// Package privacy is the member self-service of L7: notification preferences,
// a copy of one's data, and account deletion requests that staff complete by
// anonymizing the member (the point ledger stays, without personal data).
package privacy

import (
	"context"
	"errors"
	"time"

	domainmember "crm-core/domain/member"

	"gorm.io/gorm/schema"
)

// NotificationPrefs are the member's LINE message choices. A member without a
// row gets DefaultPrefs. Marketing consent is a consent document, not a pref.
type NotificationPrefs struct {
	MemberID uint `gorm:"primaryKey;autoIncrement:false"`
	// PointsUpdates: points earned and tier upgrades.
	PointsUpdates bool `gorm:"not null"`
	// ExpiringPoints: reminder before points expire.
	ExpiringPoints bool `gorm:"not null"`
	// Birthday: birthday greeting (the bonus is credited either way).
	Birthday  bool `gorm:"not null"`
	UpdatedAt time.Time
}

// TableName names the table member_notification_prefs.
func (NotificationPrefs) TableName(namer schema.Namer) string {
	return namer.TableName("MemberNotificationPref")
}

// DefaultPrefs is everything on.
func DefaultPrefs(memberID uint) NotificationPrefs {
	return NotificationPrefs{MemberID: memberID, PointsUpdates: true, ExpiringPoints: true, Birthday: true}
}

// Request types and statuses.
const (
	RequestDelete = "delete"

	RequestPending   = "pending"
	RequestCompleted = "completed"
	RequestRejected  = "rejected"
)

// Errors.
var (
	ErrRequestPending  = errors.New("a deletion request is already pending")
	ErrRequestClosed   = errors.New("the request was already processed")
	ErrOpenRedemptions = errors.New("member has redemptions that are not shipped or cancelled")
)

// DataRequest is a member's PDPA request handled by staff.
type DataRequest struct {
	ID       uint   `gorm:"primaryKey"`
	MemberID uint   `gorm:"index;not null"`
	Type     string `gorm:"size:20;not null"`
	Status   string `gorm:"size:20;not null;index"`
	Reason   string `gorm:"size:500"`
	// MemberCode snapshot: the member row is anonymized on completion.
	MemberCode  string `gorm:"size:20"`
	ProcessedBy uint   `gorm:"not null;default:0"`
	ProcessedAt *time.Time
	StaffNote   string `gorm:"size:500"`
	CreatedAt   time.Time
}

// TableName names the table data_requests.
func (DataRequest) TableName(namer schema.Namer) string { return namer.TableName("DataRequest") }

// RequestQuery filters the backoffice queue.
type RequestQuery struct {
	Status string
	Page   int
	Limit  int
}

// RequestRow is a request with the member's current display data.
type RequestRow struct {
	DataRequest
	FirstName string
	LastName  string
	Email     string
}

// Repository stores preferences and requests.
type Repository interface {
	Prefs(ctx context.Context, memberID uint) (NotificationPrefs, error)
	SavePrefs(ctx context.Context, p NotificationPrefs) error
	// PrefsFor returns preferences for many members (defaults filled in).
	PrefsFor(ctx context.Context, memberIDs []uint) (map[uint]NotificationPrefs, error)

	PendingRequest(ctx context.Context, memberID uint, typ string) (*DataRequest, error)
	CreateRequest(ctx context.Context, r *DataRequest) error
	FindRequests(ctx context.Context, q RequestQuery) ([]RequestRow, int64, error)
}

// AnonymizeTx is the deletion, bound to one transaction.
type AnonymizeTx interface {
	LockRequest(ctx context.Context, id uint) (*DataRequest, error)
	LockMember(ctx context.Context, id uint) (*domainmember.Member, error)
	CountOpenRedemptions(ctx context.Context, memberID uint) (int64, error)
	// ActiveLineUserID returns the member's linked LINE user ("" if none).
	ActiveLineUserID(ctx context.Context, memberID uint) (string, error)
	// Anonymize wipes personal data: member fields, LINE links, addresses,
	// unused coupons; the ledger and consent evidence stay.
	Anonymize(ctx context.Context, memberID uint, at time.Time) error
	CloseRequest(ctx context.Context, id uint, status string, by uint, note string, at time.Time) error
}

// UnitOfWork runs fn in one transaction.
type UnitOfWork interface {
	Do(ctx context.Context, fn func(ctx context.Context, tx AnonymizeTx) error) error
}
