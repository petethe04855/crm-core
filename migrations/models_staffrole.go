package migrations

import (
	domainstaffrole "crm-core/domain/staffrole"
)

// staffRoleModels lists the CRM role grants that are added to an ERP role.
func staffRoleModels() []any {
	return []any{&domainstaffrole.StaffRole{}}
}
