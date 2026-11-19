package postgres

import (
	"context"
	"errors"
	"time"

	"crm-core/domain"
	domainearnrule "crm-core/domain/earnrule"

	"gorm.io/gorm"
)

type earnRuleRepository struct {
	db *gorm.DB
}

// NewEarnRuleRepository builds the earning-rule repository.
func NewEarnRuleRepository(db *gorm.DB) domainearnrule.Repository {
	return &earnRuleRepository{db: db}
}

func (r *earnRuleRepository) FindAll(ctx context.Context, q domainearnrule.Query) ([]domainearnrule.EarnRule, error) {
	tx := r.db.WithContext(ctx).Model(&domainearnrule.EarnRule{})
	if q.Type != "" {
		tx = tx.Where("type = ?", q.Type)
	}
	var rows []domainearnrule.EarnRule
	err := tx.Order("priority DESC").Order("id ASC").Find(&rows).Error
	return rows, err
}

func (r *earnRuleRepository) FindByID(ctx context.Context, id uint) (*domainearnrule.EarnRule, error) {
	var rule domainearnrule.EarnRule
	if err := r.db.WithContext(ctx).First(&rule, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	return &rule, nil
}

func (r *earnRuleRepository) FindActive(ctx context.Context, at time.Time) ([]domainearnrule.EarnRule, error) {
	var rows []domainearnrule.EarnRule
	err := r.db.WithContext(ctx).
		Where("is_active = ?", true).
		Where("(start_at IS NULL OR start_at <= ?)", at).
		Where("(end_at IS NULL OR end_at > ?)", at).
		Order("priority DESC").Order("id ASC").
		Find(&rows).Error
	return rows, err
}

func (r *earnRuleRepository) Create(ctx context.Context, rule *domainearnrule.EarnRule) error {
	return r.db.WithContext(ctx).Create(rule).Error
}

// Update writes every column. Callers load the row first (the usecase does), so
// zero values such as a cleared window or an empty SKU list persist.
func (r *earnRuleRepository) Update(ctx context.Context, rule *domainearnrule.EarnRule) error {
	return r.db.WithContext(ctx).Save(rule).Error
}
