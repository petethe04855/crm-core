// Package postgres implements crm-core repository interfaces with GORM.
// Queries stay portable (LOWER/LIKE instead of ILIKE) so tests run on SQLite.
package postgres

import (
	"context"
	"errors"
	"strings"
	"time"

	"crm-core/domain"
	domainmember "crm-core/domain/member"

	"gorm.io/gorm"
)

type memberRepository struct {
	db *gorm.DB
}

// NewMemberRepository builds the member repository.
func NewMemberRepository(db *gorm.DB) domainmember.Repository {
	return &memberRepository{db: db}
}

// escapeLike escapes LIKE wildcards so user input matches literally.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

func (r *memberRepository) FindAll(ctx context.Context, q domainmember.Query) ([]domainmember.Member, int64, error) {
	tx := r.db.WithContext(ctx).Model(&domainmember.Member{})

	if q.Status != "" {
		tx = tx.Where("status = ?", q.Status)
	} else {
		tx = tx.Where("status <> ?", domainmember.StatusDeleted)
	}
	if q.TierID != 0 {
		tx = tx.Where("tier_id = ?", q.TierID)
	}
	if q.RegisteredFrom != nil {
		tx = tx.Where("created_at >= ?", *q.RegisteredFrom)
	}
	if q.RegisteredTo != nil {
		tx = tx.Where("created_at < ?", *q.RegisteredTo)
	}
	if q.MinPoints != nil {
		tx = tx.Where("point_balance >= ?", *q.MinPoints)
	}
	if q.MaxPoints != nil {
		tx = tx.Where("point_balance <= ?", *q.MaxPoints)
	}
	if q.InactiveBefore != nil {
		// Members who never opened the LIFF app count from their registration.
		tx = tx.Where("COALESCE(last_active_at, created_at) < ?", *q.InactiveBefore)
	}
	if q.Search != "" {
		like := "%" + escapeLike(strings.ToLower(q.Search)) + "%"
		tx = tx.Where(
			`LOWER(member_code) LIKE ? ESCAPE '\' OR LOWER(first_name) LIKE ? ESCAPE '\' OR LOWER(last_name) LIKE ? ESCAPE '\' OR LOWER(email) LIKE ? ESCAPE '\' OR phone_e164 LIKE ? ESCAPE '\'`,
			like, like, like, like, like,
		)
	}

	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var members []domainmember.Member
	err := tx.Order("created_at DESC").Order("id DESC").
		Offset((q.Page - 1) * q.Limit).Limit(q.Limit).
		Find(&members).Error
	if err != nil {
		return nil, 0, err
	}
	return members, total, nil
}

func (r *memberRepository) FindByID(ctx context.Context, id uint) (*domainmember.Member, error) {
	var m domainmember.Member
	if err := r.db.WithContext(ctx).First(&m, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	return &m, nil
}

func (r *memberRepository) FindActiveLineAccount(ctx context.Context, memberID uint) (*domainmember.MemberLineAccount, error) {
	var acc domainmember.MemberLineAccount
	err := r.db.WithContext(ctx).
		Where("member_id = ? AND unlinked_at IS NULL", memberID).
		Order("linked_at DESC").
		First(&acc).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	return &acc, nil
}

func (r *memberRepository) UpdateStatus(ctx context.Context, id uint, status string) error {
	res := r.db.WithContext(ctx).Model(&domainmember.Member{}).
		Where("id = ?", id).
		Update("status", status)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// UnlinkActiveLineAccount clears the active LINE link. The account row is kept
// so the member's link history stays auditable.
func (r *memberRepository) UnlinkActiveLineAccount(ctx context.Context, memberID uint) error {
	now := time.Now()
	res := r.db.WithContext(ctx).Model(&domainmember.MemberLineAccount{}).
		Where("member_id = ? AND unlinked_at IS NULL", memberID).
		Update("unlinked_at", now)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *memberRepository) FindTags(ctx context.Context, memberID uint) ([]domainmember.MemberTag, error) {
	var tags []domainmember.MemberTag
	if err := r.db.WithContext(ctx).
		Where("member_id = ?", memberID).
		Order("created_at ASC").Order("id ASC").
		Find(&tags).Error; err != nil {
		return nil, err
	}
	return tags, nil
}

func (r *memberRepository) AddTag(ctx context.Context, tag *domainmember.MemberTag) error {
	if err := r.db.WithContext(ctx).Create(tag).Error; err != nil {
		if isDuplicate(err) {
			return domain.ErrConflict
		}
		return err
	}
	return nil
}

func (r *memberRepository) RemoveTag(ctx context.Context, memberID uint, tag string) error {
	res := r.db.WithContext(ctx).
		Where("member_id = ? AND tag = ?", memberID, tag).
		Delete(&domainmember.MemberTag{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *memberRepository) FindNotes(ctx context.Context, memberID uint, page, limit int) ([]domainmember.MemberNote, int64, error) {
	tx := r.db.WithContext(ctx).Model(&domainmember.MemberNote{}).Where("member_id = ?", memberID)

	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var notes []domainmember.MemberNote
	err := tx.Order("created_at DESC").Order("id DESC").
		Offset((page - 1) * limit).Limit(limit).
		Find(&notes).Error
	if err != nil {
		return nil, 0, err
	}
	return notes, total, nil
}

func (r *memberRepository) AddNote(ctx context.Context, note *domainmember.MemberNote) error {
	if note.CreatedAt.IsZero() {
		note.CreatedAt = time.Now()
	}
	return r.db.WithContext(ctx).Create(note).Error
}
