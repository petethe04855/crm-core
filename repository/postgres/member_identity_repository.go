package postgres

import (
	"context"
	"errors"

	"crm-core/domain"
	domainmember "crm-core/domain/member"

	"gorm.io/gorm"
)

type memberIdentityRepository struct {
	db *gorm.DB
}

// NewMemberIdentityRepository builds the LINE / email identity lookup.
func NewMemberIdentityRepository(db *gorm.DB) domainmember.IdentityRepository {
	return &memberIdentityRepository{db: db}
}

func (r *memberIdentityRepository) FindByLineUserID(ctx context.Context, lineUserID string) (*domainmember.Member, *domainmember.MemberLineAccount, error) {
	var acc domainmember.MemberLineAccount
	err := r.db.WithContext(ctx).
		Where("line_user_id = ? AND unlinked_at IS NULL", lineUserID).
		First(&acc).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, domain.ErrNotFound
		}
		return nil, nil, err
	}
	m, err := r.FindByID(ctx, acc.MemberID)
	if err != nil {
		return nil, nil, err
	}
	return m, &acc, nil
}

func (r *memberIdentityRepository) FindByID(ctx context.Context, id uint) (*domainmember.Member, error) {
	var m domainmember.Member
	if err := r.db.WithContext(ctx).First(&m, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	return &m, nil
}

func (r *memberIdentityRepository) FindByEmail(ctx context.Context, email string) (*domainmember.Member, error) {
	var m domainmember.Member
	if err := r.db.WithContext(ctx).Where("email = ?", email).First(&m).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	return &m, nil
}
