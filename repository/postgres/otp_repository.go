package postgres

import (
	"context"
	"errors"
	"time"

	"crm-core/domain"
	domainotp "crm-core/domain/otp"

	"gorm.io/gorm"
)

type otpRepository struct {
	db *gorm.DB
}

// NewOTPRepository builds the OTP request repository.
func NewOTPRepository(db *gorm.DB) domainotp.Repository {
	return &otpRepository{db: db}
}

func (r *otpRepository) Latest(ctx context.Context, destination, purpose, lineUserID string) (*domainotp.Request, error) {
	var req domainotp.Request
	err := r.db.WithContext(ctx).
		Where("destination = ? AND purpose = ? AND line_user_id = ? AND superseded_at IS NULL", destination, purpose, lineUserID).
		Order("id DESC").
		First(&req).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	return &req, nil
}

// Create supersedes every open request of the same LINE user and purpose (not
// only the same email), so a code sent to an earlier address stops working
// as soon as the member asks for a code to a new one.
func (r *otpRepository) Create(ctx context.Context, req *domainotp.Request) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		q := tx.Model(&domainotp.Request{}).
			Where("purpose = ? AND superseded_at IS NULL AND verified_at IS NULL", req.Purpose)
		if req.LineUserID != "" {
			q = q.Where("line_user_id = ?", req.LineUserID)
		} else {
			q = q.Where("destination = ?", req.Destination)
		}
		if err := q.Update("superseded_at", req.CreatedAt).Error; err != nil {
			return err
		}
		return tx.Create(req).Error
	})
}

func (r *otpRepository) Cancel(ctx context.Context, id uint, at time.Time) error {
	return r.db.WithContext(ctx).Model(&domainotp.Request{}).
		Where("id = ? AND superseded_at IS NULL", id).
		Update("superseded_at", at).Error
}

func (r *otpRepository) IncrementAttempts(ctx context.Context, id uint) (int, error) {
	var count int
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&domainotp.Request{}).
			Where("id = ?", id).
			Update("attempt_count", gorm.Expr("attempt_count + 1")).Error; err != nil {
			return err
		}
		return tx.Model(&domainotp.Request{}).Where("id = ?", id).Select("attempt_count").Scan(&count).Error
	})
	return count, err
}

func (r *otpRepository) MarkVerified(ctx context.Context, id uint, at time.Time) (bool, error) {
	res := r.db.WithContext(ctx).Model(&domainotp.Request{}).
		Where("id = ? AND verified_at IS NULL AND superseded_at IS NULL", id).
		Update("verified_at", at)
	return res.RowsAffected == 1, res.Error
}

func (r *otpRepository) countSince(ctx context.Context, column, value string, since time.Time) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&domainotp.Request{}).
		Where(column+" = ? AND created_at >= ?", value, since).
		Count(&count).Error
	return count, err
}

func (r *otpRepository) CountByDestinationSince(ctx context.Context, destination string, since time.Time) (int64, error) {
	return r.countSince(ctx, "destination", destination, since)
}

func (r *otpRepository) CountByLineUserSince(ctx context.Context, lineUserID string, since time.Time) (int64, error) {
	return r.countSince(ctx, "line_user_id", lineUserID, since)
}

func (r *otpRepository) CountByIPSince(ctx context.Context, ip string, since time.Time) (int64, error) {
	return r.countSince(ctx, "ip", ip, since)
}
