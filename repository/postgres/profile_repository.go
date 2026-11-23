package postgres

import (
	"context"
	"time"

	"crm-core/domain"
	domainmember "crm-core/domain/member"

	"gorm.io/gorm"
)

type profileRepository struct{ db *gorm.DB }

// NewProfileRepository builds the member self-service repository (L3).
func NewProfileRepository(db *gorm.DB) domainmember.ProfileRepository {
	return &profileRepository{db: db}
}

func (r *profileRepository) FindMember(ctx context.Context, id uint) (*domainmember.Member, error) {
	var m domainmember.Member
	if err := r.db.WithContext(ctx).First(&m, id).Error; err != nil {
		return nil, notFound(err)
	}
	return &m, nil
}

func (r *profileRepository) UpdateProfile(ctx context.Context, memberID uint, c domainmember.ProfileChanges) error {
	res := r.db.WithContext(ctx).Model(&domainmember.Member{}).Where("id = ?", memberID).Updates(map[string]any{
		"first_name":            c.FirstName,
		"last_name":             c.LastName,
		"phone_e164":            c.PhoneE164,
		"gender":                c.Gender,
		"birth_date":            c.BirthDate,
		"birth_date_changed_at": c.BirthDateChangedAt,
		"updated_at":            time.Now(),
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *profileRepository) UpdateEmail(ctx context.Context, memberID uint, email string, verifiedAt time.Time) error {
	res := r.db.WithContext(ctx).Model(&domainmember.Member{}).Where("id = ?", memberID).Updates(map[string]any{
		"email":             email,
		"email_verified_at": verifiedAt,
		"updated_at":        time.Now(),
	})
	if res.Error != nil {
		if isDuplicate(res.Error) {
			return domainmember.ErrEmailInUse
		}
		return res.Error
	}
	if res.RowsAffected == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *profileRepository) ListAddresses(ctx context.Context, memberID uint) ([]domainmember.MemberAddress, error) {
	var out []domainmember.MemberAddress
	err := r.db.WithContext(ctx).Where("member_id = ?", memberID).
		Order("is_default DESC").Order("updated_at DESC").Order("id DESC").
		Find(&out).Error
	return out, err
}

func (r *profileRepository) FindAddress(ctx context.Context, memberID, id uint) (*domainmember.MemberAddress, error) {
	var a domainmember.MemberAddress
	if err := r.db.WithContext(ctx).Where("id = ? AND member_id = ?", id, memberID).First(&a).Error; err != nil {
		return nil, notFound(err)
	}
	return &a, nil
}

func (r *profileRepository) CountAddresses(ctx context.Context, memberID uint) (int64, error) {
	var n int64
	err := r.db.WithContext(ctx).Model(&domainmember.MemberAddress{}).Where("member_id = ?", memberID).Count(&n).Error
	return n, err
}

func (r *profileRepository) SaveAddress(ctx context.Context, a *domainmember.MemberAddress) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if a.IsDefault {
			q := tx.Model(&domainmember.MemberAddress{}).Where("member_id = ? AND is_default = ?", a.MemberID, true)
			if a.ID != 0 {
				q = q.Where("id <> ?", a.ID)
			}
			if err := q.Update("is_default", false).Error; err != nil {
				return err
			}
		}
		return tx.Save(a).Error
	})
}

func (r *profileRepository) DeleteAddress(ctx context.Context, memberID, id uint) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var a domainmember.MemberAddress
		if err := tx.Where("id = ? AND member_id = ?", id, memberID).First(&a).Error; err != nil {
			return notFound(err)
		}
		if err := tx.Delete(&a).Error; err != nil {
			return err
		}
		if !a.IsDefault {
			return nil
		}
		var next domainmember.MemberAddress
		err := tx.Where("member_id = ?", memberID).Order("updated_at DESC").Order("id DESC").First(&next).Error
		if err != nil {
			if notFound(err) == domain.ErrNotFound {
				return nil // that was the last address
			}
			return err
		}
		return tx.Model(&next).Update("is_default", true).Error
	})
}
