// Package settings stores CRM configuration as key/value JSON documents so new
// policies do not need a migration. Values are validated by the usecase that
// owns the key, never here.
package settings

import (
	"context"
	"time"
)

// Known keys.
const (
	// KeyPointAdjustPolicy caps manual point adjustments for non-owners.
	KeyPointAdjustPolicy = "point_adjust_policy"
	// KeyRedeemPolicy holds the central redemption conditions (plan §4.2).
	KeyRedeemPolicy = "redeem_policy"
	// KeyPointExpiry holds the point lifetime, e.g. {"mode":"rolling_months","months":12}.
	KeyPointExpiry = "point_expiry"
)

// Setting is one configuration document.
type Setting struct {
	Key       string `gorm:"primaryKey;size:64"`
	Value     string `gorm:"type:text;not null"`
	UpdatedBy uint   `gorm:"not null;default:0"`
	UpdatedAt time.Time
}

// Repository is the persistence contract for settings.
type Repository interface {
	// Get returns the raw value. A missing key returns domain.ErrNotFound.
	Get(ctx context.Context, key string) (string, error)
	Put(ctx context.Context, key, value string, updatedBy uint) error
}
