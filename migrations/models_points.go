package migrations

import (
	domainpoints "crm-core/domain/points"
)

// pointsModels lists the point ledger and its lots. The ledger is append-only;
// lots track expiry in FIFO order.
func pointsModels() []any {
	return []any{
		&domainpoints.PointLedger{},
		&domainpoints.PointLot{},
	}
}
