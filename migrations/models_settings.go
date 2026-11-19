package migrations

import (
	domainsettings "crm-core/domain/settings"
)

// settingsModels lists the key/value CRM settings table.
func settingsModels() []any {
	return []any{&domainsettings.Setting{}}
}
