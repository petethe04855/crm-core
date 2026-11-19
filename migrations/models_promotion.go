package migrations

import (
	domainpromotion "crm-core/domain/promotion"
)

// promotionModels lists the marketing promotion table.
func promotionModels() []any {
	return []any{&domainpromotion.Promotion{}}
}
