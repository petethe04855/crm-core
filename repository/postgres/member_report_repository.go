package postgres

import (
	"context"
	"time"

	domainmember "crm-core/domain/member"
	domainpoints "crm-core/domain/points"
	"crm-core/usecase/memberreport"

	"gorm.io/gorm"
)

type memberReportRepository struct {
	db *gorm.DB
}

// NewMemberReportRepository builds the source of the CRM member report.
func NewMemberReportRepository(db *gorm.DB) memberreport.Source {
	return &memberReportRepository{db: db}
}

func (r *memberReportRepository) members(ctx context.Context) *gorm.DB {
	return r.db.WithContext(ctx).Model(&domainmember.Member{}).
		Where("deleted_at IS NULL AND status <> ?", domainmember.StatusDeleted)
}

func (r *memberReportRepository) CountMembers(ctx context.Context) (int64, error) {
	var n int64
	err := r.members(ctx).Count(&n).Error
	return n, err
}

func (r *memberReportRepository) CountRegistered(ctx context.Context, from, to time.Time) (int64, error) {
	var n int64
	err := r.members(ctx).Where("created_at >= ? AND created_at < ?", from, to).Count(&n).Error
	return n, err
}

func (r *memberReportRepository) CountActive(ctx context.Context, from, to time.Time) (int64, error) {
	var n int64
	err := r.members(ctx).Where("last_active_at >= ? AND last_active_at < ?", from, to).Count(&n).Error
	return n, err
}

func (r *memberReportRepository) SumBalances(ctx context.Context) (int64, error) {
	var sum *int64
	err := r.members(ctx).Select("SUM(point_balance)").Scan(&sum).Error
	if err != nil || sum == nil {
		return 0, err
	}
	return *sum, nil
}

func (r *memberReportRepository) LedgerTotals(ctx context.Context, from, to time.Time) (map[string]int64, error) {
	var rows []struct {
		Type  string `gorm:"column:type"`
		Total int64  `gorm:"column:total"`
	}
	err := r.db.WithContext(ctx).Model(&domainpoints.PointLedger{}).
		Select("type, SUM(points) AS total").
		Where("created_at >= ? AND created_at < ?", from, to).
		Group("type").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(rows))
	for _, row := range rows {
		out[row.Type] = row.Total
	}
	return out, nil
}
