package migrations

import (
	domainreward "crm-core/domain/reward"
)

// rewardModels lists the reward catalogue, the ERP stock cache and redemptions.
func rewardModels() []any {
	return []any{
		&domainreward.Category{},
		&domainreward.Reward{},
		&domainreward.StockCache{},
		&domainreward.Redemption{},
	}
}
