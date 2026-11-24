package postgres

import (
	"context"
	"errors"
	"strings"
	"time"

	"crm-core/domain"
	domainpromotion "crm-core/domain/promotion"

	"gorm.io/gorm"
)

type promotionRepository struct {
	db *gorm.DB
}

// NewPromotionRepository builds the promotion repository.
func NewPromotionRepository(db *gorm.DB) domainpromotion.Repository {
	return &promotionRepository{db: db}
}

func (r *promotionRepository) FindAll(ctx context.Context, q domainpromotion.Query) ([]domainpromotion.Promotion, int64, error) {
	tx := r.db.WithContext(ctx).Model(&domainpromotion.Promotion{})

	if q.Status != "" {
		switch q.Status {
		case "public":
			tx = tx.Where("status = ?", domainpromotion.StatusPublished)
		case "unpublic":
			tx = tx.Where("status <> ?", domainpromotion.StatusPublished)
		default:
			tx = tx.Where("status = ?", q.Status)
		}
	}
	if q.Placement != "" {
		tx = tx.Where("placement = ?", q.Placement)
	}
	if q.Search != "" {
		like := "%" + escapeLike(strings.ToLower(q.Search)) + "%"
		tx = tx.Where(`LOWER(title) LIKE ? ESCAPE '\'`, like)
	}
	// Half-open overlap: window start is before the range end AND the window
	// end is after the range start.
	if q.OverlapFrom != nil && q.OverlapTo != nil {
		tx = tx.Where("display_start_at < ? AND display_end_at > ?", *q.OverlapTo, *q.OverlapFrom)
	}

	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var promotions []domainpromotion.Promotion
	err := tx.Order(domainpromotion.DisplayOrderSQL).
		Offset((q.Page - 1) * q.Limit).Limit(q.Limit).
		Find(&promotions).Error
	if err != nil {
		return nil, 0, err
	}
	return promotions, total, nil
}

func (r *promotionRepository) FindByID(ctx context.Context, id string) (*domainpromotion.Promotion, error) {
	var p domainpromotion.Promotion
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&p).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	return &p, nil
}

func (r *promotionRepository) FindByPriority(ctx context.Context, priority int, excludeID string) (*domainpromotion.Promotion, error) {
	var found []domainpromotion.Promotion
	err := r.db.WithContext(ctx).
		Where("priority = ? AND id <> ? AND status <> ?", priority, excludeID, domainpromotion.StatusArchived).
		Order("id ASC").Limit(1).Find(&found).Error
	if err != nil || len(found) == 0 {
		return nil, err
	}
	return &found[0], nil
}

func (r *promotionRepository) Create(ctx context.Context, p *domainpromotion.Promotion) error {
	return r.db.WithContext(ctx).Create(p).Error
}

func (r *promotionRepository) Update(ctx context.Context, p *domainpromotion.Promotion) error {
	// Save writes every column (map updates would need extra care around the
	// jsonb audience); callers always load the row first, so created_* keep
	// their values.
	return r.db.WithContext(ctx).Save(p).Error
}

func (r *promotionRepository) Delete(ctx context.Context, id string) error {
	res := r.db.WithContext(ctx).Where("id = ?", id).Delete(&domainpromotion.Promotion{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *promotionRepository) CountPublishedOverlapping(ctx context.Context, placement string, start, end time.Time, excludeID string) (int64, error) {
	tx := r.db.WithContext(ctx).Model(&domainpromotion.Promotion{}).
		Where("status = ? AND placement = ?", domainpromotion.StatusPublished, placement).
		Where("display_start_at < ? AND display_end_at > ?", end, start)
	if excludeID != "" {
		tx = tx.Where("id <> ?", excludeID)
	}
	var count int64
	err := tx.Count(&count).Error
	return count, err
}

func (r *promotionRepository) ListLive(ctx context.Context, placement string, now time.Time) ([]domainpromotion.Promotion, error) {
	var promotions []domainpromotion.Promotion
	err := r.db.WithContext(ctx).
		Where("status = ? AND placement = ?", domainpromotion.StatusPublished, placement).
		Where("display_start_at <= ? AND display_end_at > ?", now, now).
		Order(domainpromotion.DisplayOrderSQL).
		Find(&promotions).Error
	return promotions, err
}
