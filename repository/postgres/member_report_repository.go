package postgres

import (
	"context"
	"sort"
	"time"

	domainmember "crm-core/domain/member"
	domainpoints "crm-core/domain/points"
	domainproductcode "crm-core/domain/productcode"
	domainreward "crm-core/domain/reward"
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

// GenderCounts counts the members registered in [from, to) per gender. A blank
// gender is folded into unspecified so the split always adds up.
func (r *memberReportRepository) GenderCounts(ctx context.Context, from, to time.Time) (map[string]int64, error) {
	var rows []struct {
		Gender string `gorm:"column:gender"`
		Total  int64  `gorm:"column:total"`
	}
	err := r.members(ctx).
		Select("gender, COUNT(*) AS total").
		Where("created_at >= ? AND created_at < ?", from, to).
		Group("gender").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(rows))
	for _, row := range rows {
		key := row.Gender
		switch key {
		case domainmember.GenderMale, domainmember.GenderFemale, domainmember.GenderOther:
		default:
			key = domainmember.GenderUnspecified
		}
		out[key] += row.Total
	}
	return out, nil
}

// BirthDates lists the birth date of every member registered in [from, to).
func (r *memberReportRepository) BirthDates(ctx context.Context, from, to time.Time) ([]*time.Time, error) {
	var rows []struct {
		BirthDate *time.Time `gorm:"column:birth_date"`
	}
	err := r.members(ctx).
		Select("birth_date").
		Where("created_at >= ? AND created_at < ?", from, to).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]*time.Time, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.BirthDate)
	}
	return out, nil
}

// FirstSKUs groups each member's earliest used product code by SKU. One row per
// member (the first code they ever typed), joined to its batch for the SKU, and
// only members whose first code falls inside [from, to) are counted.
//
// The earliest row per member is picked in Go rather than with MIN(used_at):
// SQLite hands an aggregate back as a string, so scanning it into a time.Time
// fails there while Postgres is fine.
func (r *memberReportRepository) FirstSKUs(ctx context.Context, from, to time.Time) ([]memberreport.FirstSKU, error) {
	// Parse on a throwaway statement: r.db.Statement is shared by every query on
	// the root handle, and parsing it here would leave the wrong schema behind.
	stmt := &gorm.Statement{DB: r.db}
	codeTable := tableOf(stmt, &domainproductcode.Code{})
	batchTable := tableOf(stmt, &domainproductcode.Batch{})
	if codeTable == "" || batchTable == "" {
		return nil, nil
	}

	var used []struct {
		MemberID uint      `gorm:"column:used_by_member_id"`
		UsedAt   time.Time `gorm:"column:used_at"`
		SKUCode  string    `gorm:"column:sku_code"`
		SKUName  string    `gorm:"column:sku_name_snapshot"`
	}
	err := r.db.WithContext(ctx).Table(codeTable+" AS c").
		Select("c.used_by_member_id, c.used_at, b.sku_code, b.sku_name_snapshot").
		Joins("JOIN "+batchTable+" AS b ON b.id = c.batch_id").
		Where("c.used_by_member_id IS NOT NULL AND c.used_at IS NOT NULL").
		Scan(&used).Error
	if err != nil {
		return nil, err
	}

	// Earliest code per member, across all time.
	type first struct {
		at   time.Time
		code string
		name string
	}
	earliest := make(map[uint]first, len(used))
	for _, u := range used {
		cur, ok := earliest[u.MemberID]
		if !ok || u.UsedAt.Before(cur.at) {
			earliest[u.MemberID] = first{at: u.UsedAt, code: u.SKUCode, name: u.SKUName}
		}
	}

	// Count only the members who started inside the range.
	type agg struct {
		name    string
		members int64
	}
	bySKU := make(map[string]*agg)
	for _, f := range earliest {
		if f.at.Before(from) || !f.at.Before(to) {
			continue
		}
		a, ok := bySKU[f.code]
		if !ok {
			a = &agg{name: f.name}
			bySKU[f.code] = a
		}
		a.members++
	}

	out := make([]memberreport.FirstSKU, 0, len(bySKU))
	for code, a := range bySKU {
		out = append(out, memberreport.FirstSKU{SKUCode: code, SKUName: a.name, Members: a.members})
	}
	// Biggest first; the code breaks ties so the order is stable.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Members != out[j].Members {
			return out[i].Members > out[j].Members
		}
		return out[i].SKUCode < out[j].SKUCode
	})
	return out, nil
}
// FirstRewards groups each member's earliest non-cancelled redemption by reward.
// One row per member, and only members whose first redemption falls inside
// [from, to) are counted. The earliest row per member is picked in Go rather
// than with MIN(created_at): SQLite returns an aggregate as a string, which
// would not scan into a time.Time.
func (r *memberReportRepository) FirstRewards(ctx context.Context, from, to time.Time) ([]memberreport.FirstReward, error) {
	var rows []struct {
		MemberID   uint      `gorm:"column:member_id"`
		RewardID   uint      `gorm:"column:reward_id"`
		RewardName string    `gorm:"column:reward_name"`
		CreatedAt  time.Time `gorm:"column:created_at"`
	}
	err := r.db.WithContext(ctx).Model(&domainreward.Redemption{}).
		Select("member_id, reward_id, reward_name, created_at").
		Where("status <> ?", domainreward.RedemptionCancelled).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	type first struct {
		at   time.Time
		id   uint
		name string
	}
	earliest := make(map[uint]first, len(rows))
	for _, row := range rows {
		cur, ok := earliest[row.MemberID]
		if !ok || row.CreatedAt.Before(cur.at) {
			earliest[row.MemberID] = first{at: row.CreatedAt, id: row.RewardID, name: row.RewardName}
		}
	}

	type agg struct {
		name    string
		members int64
	}
	byReward := make(map[uint]*agg)
	for _, f := range earliest {
		if f.at.Before(from) || !f.at.Before(to) {
			continue
		}
		a, ok := byReward[f.id]
		if !ok {
			a = &agg{name: f.name}
			byReward[f.id] = a
		}
		a.members++
	}

	out := make([]memberreport.FirstReward, 0, len(byReward))
	for id, a := range byReward {
		out = append(out, memberreport.FirstReward{RewardID: id, RewardName: a.name, Members: a.members})
	}
	// Biggest first; the id breaks ties so the order is stable.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Members != out[j].Members {
			return out[i].Members > out[j].Members
		}
		return out[i].RewardID < out[j].RewardID
	})
	return out, nil
}
// RewardsRedeemed groups the redemptions created in [from, to) by reward,
// leaving out cancelled ones (their points were refunded).
func (r *memberReportRepository) RewardsRedeemed(ctx context.Context, from, to time.Time) ([]memberreport.RewardRedeemed, int64, error) {
	base := func() *gorm.DB {
		return r.db.WithContext(ctx).Model(&domainreward.Redemption{}).
			Where("created_at >= ? AND created_at < ? AND status <> ?", from, to, domainreward.RedemptionCancelled)
	}
	var rows []struct {
		RewardID    uint
		RewardName  string
		Redemptions int64
		Qty         int64
		Points      int64
	}
	err := base().
		Select("reward_id, MAX(reward_name) AS reward_name, COUNT(*) AS redemptions, COALESCE(SUM(qty), 0) AS qty, COALESCE(SUM(points), 0) AS points").
		Group("reward_id").
		Order("redemptions DESC").Order("reward_id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, 0, err
	}
	var members int64
	if err := base().Distinct("member_id").Count(&members).Error; err != nil {
		return nil, 0, err
	}
	out := make([]memberreport.RewardRedeemed, 0, len(rows))
	for _, row := range rows {
		out = append(out, memberreport.RewardRedeemed{
			RewardID: row.RewardID, RewardName: row.RewardName, Redemptions: row.Redemptions, Qty: row.Qty, Points: row.Points,
		})
	}
	return out, members, nil
}
