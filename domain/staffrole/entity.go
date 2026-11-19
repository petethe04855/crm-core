// Package staffrole stores CRM-only roles granted to ERP staff accounts.
// The ERP removed its "admin" role on purpose, so CRM keeps its own grant
// table instead of reintroducing the role in the ERP.
package staffrole

import (
	"context"
	"time"
)

// StaffRole grants a CRM role to an ERP user. One CRM role per user.
type StaffRole struct {
	ID        uint   `gorm:"primaryKey"`
	ERPUserID uint   `gorm:"column:erp_user_id;uniqueIndex;not null"`
	Email     string `gorm:"size:255"` // display snapshot; identity is ERPUserID
	Role      string `gorm:"size:30;not null"`
	GrantedBy uint   `gorm:"not null"`
	GrantedAt time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Repository is the persistence contract for staff roles.
type Repository interface {
	FindAll(ctx context.Context) ([]StaffRole, error)
	FindByERPUserID(ctx context.Context, erpUserID uint) (*StaffRole, error)
	Upsert(ctx context.Context, role *StaffRole) error
	DeleteByERPUserID(ctx context.Context, erpUserID uint) error
}
