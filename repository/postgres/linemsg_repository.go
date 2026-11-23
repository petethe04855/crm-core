package postgres

import (
	"context"
	"time"

	domainlinemsg "crm-core/domain/linemsg"
	domainmember "crm-core/domain/member"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type outboxRepository struct{ db *gorm.DB }

// NewLineOutboxRepository builds the LINE outbox repository.
func NewLineOutboxRepository(db *gorm.DB) domainlinemsg.OutboxRepository {
	return &outboxRepository{db: db}
}

// Enqueue uses ON CONFLICT DO NOTHING without a target, so the customer-side
// role only needs INSERT on the table.
func (r *outboxRepository) Enqueue(ctx context.Context, m *domainlinemsg.Outbox) error {
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(m).Error
}

func (r *outboxRepository) Due(ctx context.Context, now time.Time, limit int) ([]domainlinemsg.Outbox, error) {
	if limit < 1 {
		limit = 50
	}
	var rows []domainlinemsg.Outbox
	q := r.db.WithContext(ctx).
		Where("status = ? AND next_attempt_at <= ?", domainlinemsg.StatusPending, now).
		Order("next_attempt_at ASC").Order("id ASC").Limit(limit)
	if r.db.Dialector.Name() == "postgres" {
		// Two workers never pick the same rows.
		q = q.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"})
	}
	err := q.Find(&rows).Error
	return rows, err
}

func (r *outboxRepository) MarkSent(ctx context.Context, id uint, at time.Time) error {
	return r.db.WithContext(ctx).Model(&domainlinemsg.Outbox{}).Where("id = ?", id).Updates(map[string]any{
		"status": domainlinemsg.StatusSent, "sent_at": at, "updated_at": at, "last_error": "",
	}).Error
}

func (r *outboxRepository) FindByDedupeKeys(ctx context.Context, keys []string) ([]domainlinemsg.Outbox, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	var rows []domainlinemsg.Outbox
	err := r.db.WithContext(ctx).Where("dedupe_key IN ?", keys).Find(&rows).Error
	return rows, err
}

func (r *outboxRepository) MarkFailed(ctx context.Context, id uint, attempts int, next time.Time, lastError string, dead bool) error {
	status := domainlinemsg.StatusPending
	if dead {
		status = domainlinemsg.StatusDead
	}
	if len(lastError) > 500 {
		lastError = lastError[:500]
	}
	return r.db.WithContext(ctx).Model(&domainlinemsg.Outbox{}).Where("id = ?", id).Updates(map[string]any{
		"status": status, "attempts": attempts, "next_attempt_at": next, "last_error": lastError, "updated_at": time.Now(),
	}).Error
}

type webhookRepository struct{ db *gorm.DB }

// NewLineWebhookRepository builds the webhook event repository.
func NewLineWebhookRepository(db *gorm.DB) domainlinemsg.WebhookRepository {
	return &webhookRepository{db: db}
}

func (r *webhookRepository) Record(ctx context.Context, e *domainlinemsg.WebhookEvent) (bool, error) {
	res := r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(e)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

func (r *webhookRepository) Finish(ctx context.Context, id uint, status, errText string, at time.Time) error {
	if len(errText) > 500 {
		errText = errText[:500]
	}
	return r.db.WithContext(ctx).Model(&domainlinemsg.WebhookEvent{}).Where("id = ?", id).Updates(map[string]any{
		"status": status, "error": errText, "processed_at": at,
	}).Error
}

func (r *webhookRepository) SetFriend(ctx context.Context, lineUserID string, friend bool) error {
	return r.db.WithContext(ctx).Model(&domainmember.MemberLineAccount{}).
		Where("line_user_id = ? AND unlinked_at IS NULL", lineUserID).
		Update("is_friend", friend).Error
}
