package domain

import "strings"

// Role names. ERPRoleOwner comes from the ERP JWT; RoleAdmin is a CRM-only
// role stored in crm.staff_roles (the ERP deliberately has no "admin" role).
const (
	ERPRoleOwner      = "owner"
	ERPRoleSales      = "sales"
	ERPRoleWarehouse  = "warehouse"
	ERPRoleAccountant = "accountant"
	RoleAdmin         = "admin"
)

// Actor is the staff member performing a backoffice action. Roles holds the
// effective roles: the ERP role plus any CRM roles granted in crm.staff_roles.
type Actor struct {
	ERPUserID uint
	Email     string
	Roles     []string
}

// HasRole reports whether the actor holds any of the given roles.
func (a Actor) HasRole(roles ...string) bool {
	for _, have := range a.Roles {
		for _, want := range roles {
			if strings.EqualFold(have, want) {
				return true
			}
		}
	}
	return false
}

// IsOwner reports whether the actor is an ERP owner.
func (a Actor) IsOwner() bool { return a.HasRole(ERPRoleOwner) }
