package postgres

import (
	"context"
	"errors"

	"crm-core/domain"
	domainstaffrole "crm-core/domain/staffrole"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type staffRoleRepository struct {
	db *gorm.DB
}

// NewStaffRoleRepository builds the staff role repository.
func NewStaffRoleRepository(db *gorm.DB) domainstaffrole.Repository {
	return &staffRoleRepository{db: db}
}

func (r *staffRoleRepository) FindAll(ctx context.Context) ([]domainstaffrole.StaffRole, error) {
	var roles []domainstaffrole.StaffRole
	err := r.db.WithContext(ctx).Order("granted_at DESC").Find(&roles).Error
	return roles, err
}

func (r *staffRoleRepository) FindByERPUserID(ctx context.Context, erpUserID uint) (*domainstaffrole.StaffRole, error) {
	var role domainstaffrole.StaffRole
	err := r.db.WithContext(ctx).Where("erp_user_id = ?", erpUserID).First(&role).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	return &role, nil
}

// Upsert inserts the grant or replaces the existing grant for the same ERP user.
func (r *staffRoleRepository) Upsert(ctx context.Context, role *domainstaffrole.StaffRole) error {
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "erp_user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"email", "role", "granted_by", "granted_at", "updated_at"}),
	}).Create(role).Error
}

func (r *staffRoleRepository) DeleteByERPUserID(ctx context.Context, erpUserID uint) error {
	res := r.db.WithContext(ctx).Where("erp_user_id = ?", erpUserID).Delete(&domainstaffrole.StaffRole{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return domain.ErrNotFound
	}
	return nil
}
