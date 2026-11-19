// Package staffrole manages CRM-only staff roles (currently "admin") and
// resolves a staff member's effective roles.
package staffrole

import (
	"context"
	"errors"
	"strings"
	"time"

	"crm-core/domain"
	domainstaffrole "crm-core/domain/staffrole"
)

// GrantableRoles are the CRM roles an owner can grant.
var GrantableRoles = []string{domain.RoleAdmin}

// Usecase manages staff role grants.
type Usecase interface {
	List(ctx context.Context) ([]domainstaffrole.StaffRole, error)
	Grant(ctx context.Context, actor domain.Actor, erpUserID uint, email, role string) (*domainstaffrole.StaffRole, error)
	Revoke(ctx context.Context, actor domain.Actor, erpUserID uint) error
	// EffectiveRoles returns the ERP role plus any CRM role granted to the user.
	EffectiveRoles(ctx context.Context, erpUserID uint, erpRole string) ([]string, error)
}

type usecase struct {
	repo domainstaffrole.Repository
	now  func() time.Time
}

// New builds the staff role usecase.
func New(repo domainstaffrole.Repository) Usecase {
	return &usecase{repo: repo, now: time.Now}
}

func isGrantable(role string) bool {
	for _, r := range GrantableRoles {
		if r == role {
			return true
		}
	}
	return false
}

func (u *usecase) List(ctx context.Context) ([]domainstaffrole.StaffRole, error) {
	return u.repo.FindAll(ctx)
}

func (u *usecase) Grant(ctx context.Context, actor domain.Actor, erpUserID uint, email, role string) (*domainstaffrole.StaffRole, error) {
	if !actor.IsOwner() {
		return nil, domain.ErrForbidden
	}
	role = strings.ToLower(strings.TrimSpace(role))
	if erpUserID == 0 {
		return nil, domain.NewValidationError("erpUserId", "is required")
	}
	if !isGrantable(role) {
		return nil, domain.NewValidationError("role", "must be one of: "+strings.Join(GrantableRoles, ", "))
	}
	now := u.now()
	grant := &domainstaffrole.StaffRole{
		ERPUserID: erpUserID,
		Email:     strings.ToLower(strings.TrimSpace(email)),
		Role:      role,
		GrantedBy: actor.ERPUserID,
		GrantedAt: now,
	}
	if err := u.repo.Upsert(ctx, grant); err != nil {
		return nil, err
	}
	return grant, nil
}

func (u *usecase) Revoke(ctx context.Context, actor domain.Actor, erpUserID uint) error {
	if !actor.IsOwner() {
		return domain.ErrForbidden
	}
	if erpUserID == 0 {
		return domain.NewValidationError("erpUserId", "is required")
	}
	return u.repo.DeleteByERPUserID(ctx, erpUserID)
}

func (u *usecase) EffectiveRoles(ctx context.Context, erpUserID uint, erpRole string) ([]string, error) {
	roles := []string{}
	if r := strings.ToLower(strings.TrimSpace(erpRole)); r != "" {
		roles = append(roles, r)
	}
	grant, err := u.repo.FindByERPUserID(ctx, erpUserID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return roles, nil
		}
		return nil, err
	}
	for _, r := range roles {
		if r == grant.Role {
			return roles, nil
		}
	}
	return append(roles, grant.Role), nil
}
