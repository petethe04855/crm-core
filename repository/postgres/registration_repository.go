package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	domainmember "crm-core/domain/member"
	domainpoints "crm-core/domain/points"
	domainregistration "crm-core/domain/registration"

	"gorm.io/gorm"
)

// NewRegistrationUnitOfWork runs registrations in one transaction.
func NewRegistrationUnitOfWork(db *gorm.DB) domainregistration.UnitOfWork {
	return &registrationUnitOfWork{db: db}
}

type registrationUnitOfWork struct{ db *gorm.DB }

func (u *registrationUnitOfWork) Do(ctx context.Context, fn func(ctx context.Context, tx domainregistration.Tx) error) error {
	return u.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(ctx, &registrationTx{tx: tx})
	})
}

type registrationTx struct{ tx *gorm.DB }

func (r *registrationTx) Points() domainpoints.Repository { return NewPointsRepository(r.tx) }

func (r *registrationTx) FindMemberByEmail(ctx context.Context, email string) (*domainmember.Member, error) {
	var m domainmember.Member
	if err := r.tx.WithContext(ctx).Where("email = ?", email).First(&m).Error; err != nil {
		return nil, notFound(err)
	}
	return &m, nil
}

func (r *registrationTx) FindActiveLink(ctx context.Context, lineUserID string) (*domainmember.MemberLineAccount, error) {
	var acc domainmember.MemberLineAccount
	err := r.tx.WithContext(ctx).Where("line_user_id = ? AND unlinked_at IS NULL", lineUserID).First(&acc).Error
	if err != nil {
		return nil, notFound(err)
	}
	return &acc, nil
}

func (r *registrationTx) FindMemberActiveLink(ctx context.Context, memberID uint) (*domainmember.MemberLineAccount, error) {
	var acc domainmember.MemberLineAccount
	err := r.tx.WithContext(ctx).Where("member_id = ? AND unlinked_at IS NULL", memberID).
		Order("linked_at DESC").First(&acc).Error
	if err != nil {
		return nil, notFound(err)
	}
	return &acc, nil
}

func (r *registrationTx) LockMember(ctx context.Context, id uint) (*domainmember.Member, error) {
	return lockMember(r.tx.WithContext(ctx), id)
}

func (r *registrationTx) CreateMember(ctx context.Context, m *domainmember.Member) error {
	db := r.tx.WithContext(ctx)
	now := m.CreatedAt
	if now.IsZero() {
		now = time.Now()
	}
	// member_code is unique and not null: insert a placeholder, then derive
	// the real code from the id the sequence gave us.
	// The placeholder must be unique even for concurrent sign-ups (the clock
	// can return the same instant twice), so it is random, not time-based.
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return err
	}
	m.MemberCode = "TMP-" + hex.EncodeToString(buf[:])
	// A savepoint keeps the outer transaction usable on Postgres when the
	// unique email check fails.
	err := db.Transaction(func(sp *gorm.DB) error { return sp.Create(m).Error })
	if err != nil {
		if isDuplicate(err) {
			return domainregistration.ErrEmailTaken
		}
		return err
	}
	m.MemberCode = MemberCode(m.ID, now)
	return db.Model(&domainmember.Member{}).Where("id = ?", m.ID).Update("member_code", m.MemberCode).Error
}

// MemberCode formats M<YYMM>-<id> in Bangkok time, e.g. M2611-000033.
func MemberCode(id uint, at time.Time) string {
	return fmt.Sprintf("M%s-%06d", at.In(domainpoints.Bangkok).Format("0601"), id)
}

func (r *registrationTx) MarkEmailVerified(ctx context.Context, memberID uint, at time.Time) error {
	return r.tx.WithContext(ctx).Model(&domainmember.Member{}).
		Where("id = ?", memberID).
		Update("email_verified_at", at).Error
}

func (r *registrationTx) UnlinkLine(ctx context.Context, linkID uint, at time.Time) error {
	return r.tx.WithContext(ctx).Model(&domainmember.MemberLineAccount{}).
		Where("id = ? AND unlinked_at IS NULL", linkID).
		Update("unlinked_at", at).Error
}

func (r *registrationTx) LinkLine(ctx context.Context, link *domainmember.MemberLineAccount) error {
	db := r.tx.WithContext(ctx)
	var existing domainmember.MemberLineAccount
	err := db.Where("line_user_id = ?", link.LineUserID).First(&existing).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		if err := db.Transaction(func(sp *gorm.DB) error { return sp.Create(link).Error }); err != nil {
			if isDuplicate(err) {
				return domainregistration.ErrLineAlreadyLinked
			}
			return err
		}
		return nil
	case err != nil:
		return err
	case existing.UnlinkedAt == nil:
		return domainregistration.ErrLineAlreadyLinked
	}
	// Reuse the old (unlinked) row: line_user_id is unique.
	res := db.Model(&domainmember.MemberLineAccount{}).
		Where("id = ? AND unlinked_at IS NOT NULL", existing.ID).
		Updates(map[string]any{
			"member_id":    link.MemberID,
			"display_name": link.DisplayName,
			"picture_url":  link.PictureURL,
			"linked_at":    link.LinkedAt,
			"unlinked_at":  nil,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return domainregistration.ErrLineAlreadyLinked // relinked concurrently
	}
	link.ID = existing.ID
	return nil
}

func (r *registrationTx) AddTierHistory(ctx context.Context, h *domainregistration.TierHistory) error {
	return r.tx.WithContext(ctx).Create(h).Error
}
