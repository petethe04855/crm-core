package postgres

import (
	"context"
	"time"

	domainloyalty "crm-core/domain/loyalty"
	domainmembercard "crm-core/domain/membercard"
	domainpoints "crm-core/domain/points"
	domainregistration "crm-core/domain/registration"

	"gorm.io/gorm"
)

type memberCardRepository struct{ db *gorm.DB }

// NewMemberCardRepository builds the member card read model.
func NewMemberCardRepository(db *gorm.DB) domainmembercard.Repository {
	return &memberCardRepository{db: db}
}

func (r *memberCardRepository) ExpiringPoints(ctx context.Context, memberID uint, from, until time.Time) (domainmembercard.Expiring, error) {
	var lots []domainpoints.PointLot
	err := r.db.WithContext(ctx).
		Where("member_id = ? AND points_remaining > 0 AND expires_at IS NOT NULL AND expires_at >= ? AND expires_at < ?", memberID, from, until).
		Order("expires_at ASC").
		Find(&lots).Error
	if err != nil {
		return domainmembercard.Expiring{}, err
	}
	var out domainmembercard.Expiring
	for i, lot := range lots {
		out.Points += lot.PointsRemaining
		if i == 0 {
			at := *lot.ExpiresAt
			out.EarliestAt = &at
		}
	}
	return out, nil
}

func (r *memberCardRepository) TierHistory(ctx context.Context, memberID uint) ([]domainmembercard.TierChange, error) {
	var rows []domainregistration.TierHistory
	if err := r.db.WithContext(ctx).Where("member_id = ?", memberID).Order("created_at DESC").Order("id DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	var tiers []domainloyalty.Tier
	if err := r.db.WithContext(ctx).Find(&tiers).Error; err != nil {
		return nil, err
	}
	names := map[uint]string{}
	for _, t := range tiers {
		names[t.ID] = t.Name
	}
	out := make([]domainmembercard.TierChange, 0, len(rows))
	for _, h := range rows {
		c := domainmembercard.TierChange{ToName: names[h.ToTierID], Reason: h.Reason, At: h.CreatedAt}
		if h.FromTierID != nil {
			c.FromName = names[*h.FromTierID]
		}
		out = append(out, c)
	}
	return out, nil
}
