// Package membercard reads what the LINE member card shows besides the member
// row itself: points about to expire.
package membercard

import (
	"context"
	"time"
)

// Expiring is the sum of points whose lots expire before a cut-off, and the
// earliest of those expiry dates.
type Expiring struct {
	Points     int64
	EarliestAt *time.Time
}

// TierChange is one row of the member's tier history with tier names.
type TierChange struct {
	FromName string // "" on signup
	ToName   string
	Reason   string
	At       time.Time
}

// Repository is the read model for the member card.
type Repository interface {
	ExpiringPoints(ctx context.Context, memberID uint, from, until time.Time) (Expiring, error)
	// TierHistory lists the member's tier changes, newest first.
	TierHistory(ctx context.Context, memberID uint) ([]TierChange, error)
}
