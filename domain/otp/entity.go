// Package otp stores one-time passwords sent to a member's email. Only an HMAC
// of the code is stored; the plain code exists only in the email.
package otp

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm/schema"
)

// Channel values.
const ChannelEmail = "email"

// Purpose values.
const (
	// PurposeRegister proves the email during LINE onboarding (new member or
	// linking an existing member).
	PurposeRegister    = "register"
	PurposeChangeEmail = "change_email"
)

// Rules (plan CRM_LINE_OA_MEMBERSHIP.md §5.4).
const (
	CodeLength       = 6
	RefLength        = 4
	TTL              = 10 * time.Minute
	ResendCooldown   = 60 * time.Second
	MaxAttempts      = 5
	PerEmailPerHour  = 5
	PerLineUserDaily = 10
	PerIPPerHour     = 20
)

// OTP errors. The delivery layer maps each to its own error code.
var (
	ErrInvalidCode     = errors.New("otp code is incorrect")
	ErrExpired         = errors.New("otp has expired")
	ErrTooManyAttempts = errors.New("otp has too many wrong attempts")
	ErrNoActiveRequest = errors.New("no active otp request")
	ErrResendTooSoon   = errors.New("otp was requested too recently")
	ErrRateLimited     = errors.New("too many otp requests")
	ErrInvalidEmail    = errors.New("email address is not acceptable")
	ErrSendFailed      = errors.New("otp email could not be sent")
)

// InvalidCodeError is a wrong code; Remaining is how many tries are left.
type InvalidCodeError struct{ Remaining int }

func (e *InvalidCodeError) Error() string { return ErrInvalidCode.Error() }
func (e *InvalidCodeError) Unwrap() error { return ErrInvalidCode }

// ResendError tells the caller when a new code can be requested.
type ResendError struct{ RetryAt time.Time }

func (e *ResendError) Error() string { return ErrResendTooSoon.Error() }
func (e *ResendError) Unwrap() error { return ErrResendTooSoon }

// Request is one OTP sent to a destination. Requesting a new code supersedes
// every earlier open request for the same destination, purpose and LINE user.
type Request struct {
	ID                uint   `gorm:"primaryKey"`
	Channel           string `gorm:"size:20;not null"`
	Destination       string `gorm:"size:255;not null;index"`
	Purpose           string `gorm:"size:30;not null"`
	CodeHash          string `gorm:"size:128;not null"`
	RefCode           string `gorm:"size:10;not null"`
	ExpiresAt         time.Time
	AttemptCount      int `gorm:"not null;default:0"`
	MaxAttempts       int `gorm:"not null;default:5"`
	VerifiedAt        *time.Time
	SupersededAt      *time.Time
	ResendAvailableAt time.Time
	LineUserID        string    `gorm:"size:64;index"`
	IP                string    `gorm:"size:64;index"`
	CreatedAt         time.Time `gorm:"index"`
}

// TableName names the table otp_requests and keeps the schema prefix.
func (Request) TableName(namer schema.Namer) string { return namer.TableName("OTPRequest") }

// Open reports whether the request can still be verified at now.
func (r Request) Open(now time.Time) bool {
	return r.VerifiedAt == nil && r.SupersededAt == nil && now.Before(r.ExpiresAt) && r.AttemptCount < r.MaxAttempts
}

// Repository is the persistence contract for OTP requests.
type Repository interface {
	// Latest returns the newest request that is not superseded, or ErrNotFound.
	Latest(ctx context.Context, destination, purpose, lineUserID string) (*Request, error)
	// Create supersedes earlier open requests (same destination, purpose and
	// LINE user) and inserts req, in one transaction.
	Create(ctx context.Context, req *Request) error
	// Cancel supersedes one request (its email could not be sent), so the
	// member can ask again at once.
	Cancel(ctx context.Context, id uint, at time.Time) error
	// IncrementAttempts adds one wrong attempt and returns the new count.
	IncrementAttempts(ctx context.Context, id uint) (int, error)
	// MarkVerified sets verified_at once; false if it was already verified.
	MarkVerified(ctx context.Context, id uint, at time.Time) (bool, error)
	CountByDestinationSince(ctx context.Context, destination string, since time.Time) (int64, error)
	CountByLineUserSince(ctx context.Context, lineUserID string, since time.Time) (int64, error)
	CountByIPSince(ctx context.Context, ip string, since time.Time) (int64, error)
}

// Mailer delivers OTP emails. Implementations live in the API project
// (SMTP, or a console mailer in development).
type Mailer interface {
	SendOTP(ctx context.Context, to, code, ref string, expiresIn time.Duration) error
}
