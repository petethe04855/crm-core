package migrations

import domainprivacy "crm-core/domain/privacy"

// privacyModels lists the L7 tables: notification preferences and PDPA data
// requests (account deletion queue).
func privacyModels() []any {
	return []any{
		&domainprivacy.NotificationPrefs{},
		&domainprivacy.DataRequest{},
	}
}
