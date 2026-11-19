package postgres

import (
	"context"
	"errors"
	"time"

	"crm-core/domain"
	domainsettings "crm-core/domain/settings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type settingsRepository struct {
	db *gorm.DB
}

// NewSettingsRepository builds the settings repository.
func NewSettingsRepository(db *gorm.DB) domainsettings.Repository {
	return &settingsRepository{db: db}
}

func (r *settingsRepository) Get(ctx context.Context, key string) (string, error) {
	var s domainsettings.Setting
	if err := r.db.WithContext(ctx).Where("key = ?", key).First(&s).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", domain.ErrNotFound
		}
		return "", err
	}
	return s.Value, nil
}

func (r *settingsRepository) Put(ctx context.Context, key, value string, updatedBy uint) error {
	row := domainsettings.Setting{Key: key, Value: value, UpdatedBy: updatedBy, UpdatedAt: time.Now()}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "updated_by", "updated_at"}),
	}).Create(&row).Error
}
