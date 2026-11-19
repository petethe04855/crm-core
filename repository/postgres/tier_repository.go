package postgres

import (
	"context"
	"errors"

	"crm-core/domain"
	domainloyalty "crm-core/domain/loyalty"

	"gorm.io/gorm"
)

type tierRepository struct {
	db *gorm.DB
}

// NewTierRepository builds the tier repository.
func NewTierRepository(db *gorm.DB) domainloyalty.TierRepository {
	return &tierRepository{db: db}
}

func (r *tierRepository) FindAll(ctx context.Context) ([]domainloyalty.Tier, error) {
	var tiers []domainloyalty.Tier
	err := r.db.WithContext(ctx).Order("sort_order ASC").Order("min_spend ASC").Find(&tiers).Error
	return tiers, err
}

func (r *tierRepository) FindByID(ctx context.Context, id uint) (*domainloyalty.Tier, error) {
	var t domainloyalty.Tier
	if err := r.db.WithContext(ctx).First(&t, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	return &t, nil
}
