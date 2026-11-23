// Package linemsg is the LINE Messaging side of the CRM (L6): an outbox of
// pushes and rich-menu changes that backoffice-api's worker sends, and the
// webhook events line-oa-api receives from LINE.
package linemsg

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm/schema"
)

// Outbox kinds.
const (
	KindPush       = "push"        // Payload is a JSON array of LINE message objects
	KindLinkMenu   = "link_menu"   // link the member rich menu to the user
	KindUnlinkMenu = "unlink_menu" // back to the default (guest) rich menu
)

// Outbox statuses.
const (
	StatusPending = "pending"
	StatusSent    = "sent"
	StatusDead    = "dead" // gave up (permanent error or too many tries)
)

// Topics (what the message is about; marketing needs consent).
const (
	TopicWelcome        = "welcome"
	TopicPointsEarned   = "points_earned"
	TopicTierUpgrade    = "tier_upgrade"
	TopicRedemptionMade = "redemption_created"
	TopicRedemptionShip = "redemption_shipped"
	TopicRichMenu       = "rich_menu"
	TopicMarketing      = "marketing"
	TopicExpiring       = "points_expiring"
	TopicBirthday       = "birthday"
)

// MaxAttempts before a message is dead.
const MaxAttempts = 6

// Backoff is the wait before attempt n+1 (index = attempts so far − 1).
var Backoff = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 12 * time.Hour}

// Outbox is one message waiting to be sent to LINE. DedupeKey (optional)
// stops the same event from being queued twice (e.g. "shipped:<id>").
type Outbox struct {
	ID            uint      `gorm:"primaryKey"`
	Kind          string    `gorm:"size:20;not null"`
	Topic         string    `gorm:"size:30;not null;index"`
	LineUserID    string    `gorm:"size:64;not null;index"`
	MemberID      *uint     `gorm:"index"`
	Payload       string    `gorm:"type:text;not null"`
	DedupeKey     *string   `gorm:"size:100;uniqueIndex"`
	Status        string    `gorm:"size:10;not null;index:idx_line_outbox_due,priority:1"`
	Attempts      int       `gorm:"not null;default:0"`
	NextAttemptAt time.Time `gorm:"not null;index:idx_line_outbox_due,priority:2"`
	LastError     string    `gorm:"size:500"`
	SentAt        *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// TableName names the table line_outboxes.
func (Outbox) TableName(namer schema.Namer) string { return namer.TableName("LineOutbox") }

// Webhook event statuses.
const (
	EventReceived = "received"
	EventDone     = "done"
	EventFailed   = "failed"
)

// WebhookEvent is one event LINE delivered; WebhookEventID makes redelivery
// (LINE retries) harmless.
type WebhookEvent struct {
	ID             uint   `gorm:"primaryKey"`
	WebhookEventID string `gorm:"size:64;uniqueIndex;not null"`
	Type           string `gorm:"size:30;not null;index"`
	LineUserID     string `gorm:"size:64;index"`
	Payload        string `gorm:"type:text"`
	Status         string `gorm:"size:10;not null"`
	Error          string `gorm:"size:500"`
	ReceivedAt     time.Time
	ProcessedAt    *time.Time
}

// TableName names the table line_webhook_events.
func (WebhookEvent) TableName(namer schema.Namer) string { return namer.TableName("LineWebhookEvent") }

// ErrPermanent marks a send error that must not be retried (blocked user,
// bad request). Senders wrap it.
var ErrPermanent = errors.New("permanent LINE error")

// OutboxRepository stores outgoing messages.
type OutboxRepository interface {
	// Enqueue inserts the message; a duplicate DedupeKey is silently skipped.
	Enqueue(ctx context.Context, m *Outbox) error
	// Due returns pending messages whose next attempt is at or before now.
	Due(ctx context.Context, now time.Time, limit int) ([]Outbox, error)
	MarkSent(ctx context.Context, id uint, at time.Time) error
	// MarkFailed records a failed attempt; dead stops further tries.
	MarkFailed(ctx context.Context, id uint, attempts int, next time.Time, lastError string, dead bool) error
	// FindByDedupeKeys returns the messages queued under these keys (for the
	// backoffice to show whether a member was notified).
	FindByDedupeKeys(ctx context.Context, keys []string) ([]Outbox, error)
}

// WebhookRepository stores received events and applies friend status.
type WebhookRepository interface {
	// Record inserts the event; false when the id was already recorded.
	Record(ctx context.Context, e *WebhookEvent) (bool, error)
	Finish(ctx context.Context, id uint, status, errText string, at time.Time) error
	// SetFriend updates is_friend on the LINE user's active link (no-op when
	// the LINE user is not a member).
	SetFriend(ctx context.Context, lineUserID string, friend bool) error
}
