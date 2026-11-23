package migrations

import domainproductcode "crm-core/domain/productcode"

// productCodeModels lists the packaging code tables (L4B): batches, codes
// (HMAC + suffix only), redemption attempts and test members (used by L4).
func productCodeModels() []any {
	return []any{
		&domainproductcode.Batch{},
		&domainproductcode.Code{},
		&domainproductcode.RedemptionAttempt{},
		&domainproductcode.TestMember{},
	}
}
