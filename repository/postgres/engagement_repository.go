package postgres

import (
	"context"
	"time"

	domainmember "crm-core/domain/member"
	domainpoints "crm-core/domain/points"
	usecaseengagement "crm-core/usecase/engagement"

	"gorm.io/gorm"
)

type engagementRepository struct{ db *gorm.DB }

// NewEngagementRepository builds the daily-job read model.
func NewEngagementRepository(db *gorm.DB) usecaseengagement.Repository {
	return &engagementRepository{db: db}
}

func (r *engagementRepository) ExpiringByMember(ctx context.Context, from, until time.Time) ([]usecaseengagement.Expiring, error) {
	var lots []domainpoints.PointLot
	err := r.db.WithContext(ctx).
		Where("points_remaining > 0 AND expires_at IS NOT NULL AND expires_at >= ? AND expires_at < ?", from, until).
		Order("member_id ASC").Order("expires_at ASC").
		Find(&lots).Error
	if err != nil {
		return nil, err
	}
	// Only active members are reminded.
	ids := map[uint]bool{}
	for _, l := range lots {
		ids[l.MemberID] = true
	}
	active := map[uint]bool{}
	if len(ids) > 0 {
		list := make([]uint, 0, len(ids))
		for id := range ids {
			list = append(list, id)
		}
		var members []domainmember.Member
		if err := r.db.WithContext(ctx).Select("id").
			Where("id IN ? AND status = ? AND deleted_at IS NULL", list, domainmember.StatusActive).
			Find(&members).Error; err != nil {
			return nil, err
		}
		for _, m := range members {
			active[m.ID] = true
		}
	}
	var out []usecaseengagement.Expiring
	for _, l := range lots {
		if !active[l.MemberID] {
			continue
		}
		if n := len(out); n > 0 && out[n-1].MemberID == l.MemberID {
			out[n-1].Points += l.PointsRemaining
			continue
		}
		out = append(out, usecaseengagement.Expiring{MemberID: l.MemberID, Points: l.PointsRemaining, Earliest: *l.ExpiresAt})
	}
	return out, nil
}

func (r *engagementRepository) BirthdayMembers(ctx context.Context, month time.Month, day int, includeLeapDay bool) ([]domainmember.Member, error) {
	monthExpr, dayExpr := "EXTRACT(MONTH FROM birth_date)", "EXTRACT(DAY FROM birth_date)"
	if r.db.Dialector.Name() == "sqlite" {
		monthExpr, dayExpr = "CAST(strftime('%m', birth_date) AS INTEGER)", "CAST(strftime('%d', birth_date) AS INTEGER)"
	}
	q := r.db.WithContext(ctx).
		Where("status = ? AND deleted_at IS NULL AND birth_date IS NOT NULL", domainmember.StatusActive)
	if includeLeapDay {
		q = q.Where("("+monthExpr+" = ? AND "+dayExpr+" = ?) OR ("+monthExpr+" = 2 AND "+dayExpr+" = 29)", int(month), day)
	} else {
		q = q.Where(monthExpr+" = ? AND "+dayExpr+" = ?", int(month), day)
	}
	var out []domainmember.Member
	err := q.Order("id ASC").Find(&out).Error
	return out, err
}
