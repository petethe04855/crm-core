// Package loyalty defines tiers, the point ledger and earn rules.
// This skeleton holds tiers only; the ledger arrives with phase P2.
package loyalty

import (
	"context"
	"time"
)

// Tier is a membership level. Members qualify by lifetime spend (baht),
// not by points.
type Tier struct {
	ID             uint    `gorm:"primaryKey"`
	Code           string  `gorm:"size:30;uniqueIndex;not null"`
	Name           string  `gorm:"size:100;not null"`
	MinSpend       float64 `gorm:"type:numeric(14,2);not null;default:0"`
	EarnMultiplier float64 `gorm:"type:numeric(6,3);not null;default:1"`
	SortOrder      int     `gorm:"not null;default:0"`
	CardTheme      string  `gorm:"size:50"`
	IsActive       bool    `gorm:"not null;default:true"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// DefaultTierCode is the entry tier every new member starts in.
const DefaultTierCode = "bronze"

// TierRepository is the persistence contract for tiers.
type TierRepository interface {
	FindAll(ctx context.Context) ([]Tier, error)
	FindByID(ctx context.Context, id uint) (*Tier, error)
}
