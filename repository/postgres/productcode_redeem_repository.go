package postgres

import (
	"context"
	"time"

	"crm-core/domain"
	domainmember "crm-core/domain/member"
	domainpoints "crm-core/domain/points"
	domainproductcode "crm-core/domain/productcode"
	domainregistration "crm-core/domain/registration"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// NewProductCodeRedeemUnitOfWork runs one code's redemption in a transaction.
func NewProductCodeRedeemUnitOfWork(db *gorm.DB) domainproductcode.RedeemUnitOfWork {
	return &redeemUnitOfWork{db: db}
}

type redeemUnitOfWork struct{ db *gorm.DB }

func (u *redeemUnitOfWork) Do(ctx context.Context, fn func(ctx context.Context, tx domainproductcode.RedeemTx) error) error {
	return u.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(ctx, &redeemTx{tx: tx})
	})
}

type redeemTx struct{ tx *gorm.DB }

func (r *redeemTx) Points() domainpoints.Repository { return NewPointsRepository(r.tx) }

func (r *redeemTx) LockCodeByHash(ctx context.Context, hash string) (*domainproductcode.Code, *domainproductcode.Batch, error) {
	var code domainproductcode.Code
	if err := forUpdate(r.tx.WithContext(ctx)).Where("code_hash = ?", hash).First(&code).Error; err != nil {
		return nil, nil, notFound(err)
	}
	var batch domainproductcode.Batch
	if err := r.tx.WithContext(ctx).First(&batch, code.BatchID).Error; err != nil {
		return nil, nil, notFound(err)
	}
	return &code, &batch, nil
}

func (r *redeemTx) LockMember(ctx context.Context, id uint) (*domainmember.Member, error) {
	return lockMember(r.tx.WithContext(ctx), id)
}

func (r *redeemTx) CountUsedByMember(ctx context.Context, memberID uint) (int64, error) {
	var n int64
	err := r.tx.WithContext(ctx).Model(&domainproductcode.Code{}).
		Where("used_by_member_id = ? AND status = ?", memberID, domainproductcode.CodeUsed).Count(&n).Error
	return n, err
}

func (r *redeemTx) MarkCodeUsed(ctx context.Context, codeID, memberID, ledgerID uint, at time.Time) error {
	res := r.tx.WithContext(ctx).Model(&domainproductcode.Code{}).
		Where("id = ? AND status = ?", codeID, domainproductcode.CodeActive).
		Updates(map[string]any{
			"status":            domainproductcode.CodeUsed,
			"used_by_member_id": memberID,
			"used_at":           at,
			"ledger_id":         ledgerID,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return domain.ErrConflict // used concurrently (the row lock should prevent it)
	}
	return nil
}

func (r *redeemTx) UpdateMemberSpendAndTier(ctx context.Context, memberID uint, lifetimeSpend float64, tierID uint) error {
	return r.tx.WithContext(ctx).Model(&domainmember.Member{}).Where("id = ?", memberID).Updates(map[string]any{
		"lifetime_spend": lifetimeSpend,
		"tier_id":        tierID,
		"updated_at":     time.Now(),
	}).Error
}

func (r *redeemTx) AddTierHistory(ctx context.Context, memberID uint, fromTierID *uint, toTierID uint, reason string, at time.Time) error {
	return r.tx.WithContext(ctx).Create(&domainregistration.TierHistory{
		MemberID: memberID, FromTierID: fromTierID, ToTierID: toTierID, Reason: reason, CreatedAt: at,
	}).Error
}

func (r *redeemTx) CreateAttempt(ctx context.Context, a *domainproductcode.RedemptionAttempt) error {
	return r.tx.WithContext(ctx).Create(a).Error
}

type attemptRepository struct{ db *gorm.DB }

// tables returns schema-prefixed table names for raw joins.
func (r *attemptRepository) tables() (codes, batches, ledgers, members, attempts, testers string) {
	stmt := &gorm.Statement{DB: r.db}
	return tableOf(stmt, &domainproductcode.Code{}), tableOf(stmt, &domainproductcode.Batch{}),
		tableOf(stmt, &domainpoints.PointLedger{}), tableOf(stmt, &domainmember.Member{}),
		tableOf(stmt, &domainproductcode.RedemptionAttempt{}), tableOf(stmt, &domainproductcode.TestMember{})
}

// NewProductCodeAttemptRepository builds the attempt / history / test member repository.
func NewProductCodeAttemptRepository(db *gorm.DB) domainproductcode.AttemptRepository {
	return &attemptRepository{db: db}
}

func (r *attemptRepository) CreateAttempt(ctx context.Context, a *domainproductcode.RedemptionAttempt) error {
	return r.db.WithContext(ctx).Create(a).Error
}

func (r *attemptRepository) RecentAttemptTimes(ctx context.Context, memberID uint, result string, since time.Time) ([]time.Time, error) {
	var times []time.Time
	err := r.db.WithContext(ctx).Model(&domainproductcode.RedemptionAttempt{}).
		Where("member_id = ? AND result = ? AND created_at >= ?", memberID, result, since).
		Order("created_at DESC").
		Pluck("created_at", &times).Error
	return times, err
}

func (r *attemptRepository) CountAttempts(ctx context.Context, memberID uint, result string, since time.Time) (int64, error) {
	var n int64
	err := r.db.WithContext(ctx).Model(&domainproductcode.RedemptionAttempt{}).
		Where("member_id = ? AND result = ? AND created_at >= ?", memberID, result, since).Count(&n).Error
	return n, err
}

func (r *attemptRepository) IsTestMember(ctx context.Context, memberID uint) (bool, error) {
	var n int64
	err := r.db.WithContext(ctx).Model(&domainproductcode.TestMember{}).Where("member_id = ?", memberID).Count(&n).Error
	return n > 0, err
}

func (r *attemptRepository) FindBatchByCodeHash(ctx context.Context, hash string) (*domainproductcode.Batch, error) {
	var code domainproductcode.Code
	if err := r.db.WithContext(ctx).Select("batch_id").Where("code_hash = ?", hash).First(&code).Error; err != nil {
		return nil, notFound(err)
	}
	var b domainproductcode.Batch
	if err := r.db.WithContext(ctx).First(&b, code.BatchID).Error; err != nil {
		return nil, notFound(err)
	}
	return &b, nil
}

func (r *attemptRepository) History(ctx context.Context, memberID uint, page, limit int) ([]domainproductcode.HistoryItem, int64, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	codes, batches, ledgers, _, _, _ := r.tables()
	base := r.db.WithContext(ctx).Table(codes+" AS c").
		Where("c.used_by_member_id = ? AND c.status = ?", memberID, domainproductcode.CodeUsed)
	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []struct {
		CodeSuffix string
		SKUName    string
		BatchNo    string
		Points     *int64
		UsedAt     time.Time
	}
	err := base.
		Select("c.code_suffix, b.sku_name_snapshot AS sku_name, b.batch_no, l.points, c.used_at").
		Joins("JOIN " + batches + " AS b ON b.id = c.batch_id").
		Joins("LEFT JOIN " + ledgers + " AS l ON l.id = c.ledger_id").
		Order("c.used_at DESC").Order("c.id DESC").
		Offset((page - 1) * limit).Limit(limit).
		Scan(&rows).Error
	if err != nil {
		return nil, 0, err
	}
	out := make([]domainproductcode.HistoryItem, len(rows))
	for i, row := range rows {
		out[i] = domainproductcode.HistoryItem{CodeSuffix: row.CodeSuffix, SKUName: row.SKUName, BatchNo: row.BatchNo, UsedAt: row.UsedAt}
		if row.Points != nil {
			out[i].Points = *row.Points
		}
	}
	return out, total, nil
}

func (r *attemptRepository) Flagged(ctx context.Context, since time.Time, minWrong int) ([]domainproductcode.FlaggedMember, error) {
	_, _, _, members, attempts, _ := r.tables()
	var rows []domainproductcode.FlaggedMember
	err := r.db.WithContext(ctx).Table(attempts+" AS a").
		Select("a.member_id, m.member_code, m.first_name, m.last_name, COUNT(*) AS wrong").
		Joins("JOIN "+members+" AS m ON m.id = a.member_id").
		Where("a.result = ? AND a.created_at >= ?", domainproductcode.AttemptNotFound, since).
		Group("a.member_id, m.member_code, m.first_name, m.last_name").
		Having("COUNT(*) >= ?", minWrong).
		Order("wrong DESC").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	// The latest attempt per member, read separately: MAX(timestamp) scans as
	// text on SQLite.
	for i := range rows {
		var last domainproductcode.RedemptionAttempt
		if err := r.db.WithContext(ctx).Where("member_id = ? AND result = ?", rows[i].MemberID, domainproductcode.AttemptNotFound).
			Order("created_at DESC").First(&last).Error; err == nil {
			rows[i].LastAt = last.CreatedAt
		}
	}
	return rows, nil
}

func (r *attemptRepository) ListTestMembers(ctx context.Context) ([]domainproductcode.TestMemberView, error) {
	var rows []domainproductcode.TestMemberView
	_, _, _, members, _, testers := r.tables()
	err := r.db.WithContext(ctx).Table(testers + " AS t").Select("t.member_id, m.member_code, m.first_name, m.last_name, m.email, t.added_at").
		Joins("JOIN " + members + " AS m ON m.id = t.member_id").
		Order("t.added_at DESC").
		Scan(&rows).Error
	return rows, err
}

func (r *attemptRepository) AddTestMember(ctx context.Context, memberID, addedBy uint, at time.Time) error {
	var m domainmember.Member
	if err := r.db.WithContext(ctx).Select("id").First(&m, memberID).Error; err != nil {
		return notFound(err)
	}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).
		Create(&domainproductcode.TestMember{MemberID: memberID, AddedBy: addedBy, AddedAt: at}).Error
}

func (r *attemptRepository) RemoveTestMember(ctx context.Context, memberID uint) error {
	res := r.db.WithContext(ctx).Where("member_id = ?", memberID).Delete(&domainproductcode.TestMember{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return domain.ErrNotFound
	}
	return nil
}
