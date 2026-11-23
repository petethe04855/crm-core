package postgres

import (
	"context"
	"strings"

	"crm-core/domain"
	domainmember "crm-core/domain/member"
	domainreward "crm-core/domain/reward"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type rewardRepository struct {
	db *gorm.DB
}

// NewRewardRepository builds the reward repository.
func NewRewardRepository(db *gorm.DB) domainreward.Repository {
	return &rewardRepository{db: db}
}

// NewRewardUnitOfWork runs redemption workflows in one transaction together
// with the point ledger and the member row.
func NewRewardUnitOfWork(db *gorm.DB) domainreward.UnitOfWork {
	return &rewardUnitOfWork{db: db}
}

type rewardUnitOfWork struct {
	db *gorm.DB
}

func (u *rewardUnitOfWork) Do(ctx context.Context, fn func(ctx context.Context, r domainreward.TxRepos) error) error {
	return u.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(ctx, domainreward.TxRepos{
			Rewards: NewRewardRepository(tx),
			Points:  NewPointsRepository(tx),
			Members: NewMemberRepository(tx),
		})
	})
}

// ---- categories ----

func (r *rewardRepository) ListCategories(ctx context.Context) ([]domainreward.Category, error) {
	var out []domainreward.Category
	err := r.db.WithContext(ctx).Order("sort_order ASC").Order("id ASC").Find(&out).Error
	return out, err
}

func (r *rewardRepository) FindCategory(ctx context.Context, id uint) (*domainreward.Category, error) {
	var c domainreward.Category
	if err := r.db.WithContext(ctx).First(&c, id).Error; err != nil {
		return nil, notFound(err)
	}
	return &c, nil
}

func (r *rewardRepository) CreateCategory(ctx context.Context, c *domainreward.Category) error {
	if err := r.db.WithContext(ctx).Create(c).Error; err != nil {
		return conflictOn(err)
	}
	return nil
}

func (r *rewardRepository) UpdateCategory(ctx context.Context, c *domainreward.Category) error {
	if err := r.db.WithContext(ctx).Save(c).Error; err != nil {
		return conflictOn(err)
	}
	return nil
}

func (r *rewardRepository) DeleteCategory(ctx context.Context, id uint) error {
	res := r.db.WithContext(ctx).Delete(&domainreward.Category{}, id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return notFound(gorm.ErrRecordNotFound)
	}
	return nil
}

func (r *rewardRepository) CountRewardsInCategory(ctx context.Context, id uint) (int64, error) {
	var n int64
	err := r.db.WithContext(ctx).Model(&domainreward.Reward{}).Where("category_id = ?", id).Count(&n).Error
	return n, err
}

// ---- rewards ----

func (r *rewardRepository) FindAll(ctx context.Context, q domainreward.Query) ([]domainreward.Reward, int64, error) {
	stmt := &gorm.Statement{DB: r.db}
	rTable, sTable := tableOf(stmt, &domainreward.Reward{}), tableOf(stmt, &domainreward.StockCache{})

	tx := r.db.WithContext(ctx).Model(&domainreward.Reward{})
	if q.Status != "" {
		tx = tx.Where(rTable+".status = ?", q.Status)
	}
	if q.CategoryID != 0 {
		tx = tx.Where(rTable+".category_id = ?", q.CategoryID)
	}
	if q.Search != "" {
		like := "%" + escapeLike(strings.ToLower(q.Search)) + "%"
		tx = tx.Where(`(LOWER(`+rTable+`.name) LIKE ? ESCAPE '\' OR LOWER(`+rTable+`.sku_code_snapshot) LIKE ? ESCAPE '\')`, like, like)
	}
	if q.LowStock {
		// Rewards whose cached ERP stock is at or below the low-stock mark
		// (or that were never synced).
		tx = tx.Joins("LEFT JOIN "+sTable+" AS sc ON sc.erp_sku_id = "+rTable+".erp_sku_id AND sc.erp_warehouse_id = "+rTable+".erp_warehouse_id").
			Where(rTable+".erp_sku_id IS NOT NULL AND (sc.available_qty IS NULL OR sc.available_qty <= ?)", domainreward.LowStockAt)
	}
	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var out []domainreward.Reward
	err := tx.Select(rTable + ".*").Order(rTable + ".sort_order ASC").Order(rTable + ".id DESC").
		Offset((q.Page - 1) * q.Limit).Limit(q.Limit).Find(&out).Error
	return out, total, err
}

func (r *rewardRepository) FindByID(ctx context.Context, id uint) (*domainreward.Reward, error) {
	var rw domainreward.Reward
	if err := r.db.WithContext(ctx).First(&rw, id).Error; err != nil {
		return nil, notFound(err)
	}
	return &rw, nil
}

func (r *rewardRepository) LockByID(ctx context.Context, id uint) (*domainreward.Reward, error) {
	var rw domainreward.Reward
	if err := forUpdate(r.db.WithContext(ctx)).First(&rw, id).Error; err != nil {
		return nil, notFound(err)
	}
	return &rw, nil
}

func (r *rewardRepository) Create(ctx context.Context, rw *domainreward.Reward) error {
	return r.db.WithContext(ctx).Create(rw).Error
}

func (r *rewardRepository) Update(ctx context.Context, rw *domainreward.Reward) error {
	return r.db.WithContext(ctx).Save(rw).Error
}

func (r *rewardRepository) Delete(ctx context.Context, id uint) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var n int64
		if err := tx.Model(&domainreward.Redemption{}).Where("reward_id = ?", id).Count(&n).Error; err != nil {
			return err
		}
		if n > 0 {
			return domain.ErrConflict
		}
		res := tx.Delete(&domainreward.Reward{}, id)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return notFound(gorm.ErrRecordNotFound)
		}
		return nil
	})
}

func (r *rewardRepository) ActiveSKUs(ctx context.Context) ([]domainreward.SKUWarehouse, error) {
	var rows []struct {
		ERPSKUID       uint `gorm:"column:erp_sku_id"`
		ERPWarehouseID uint `gorm:"column:erp_warehouse_id"`
	}
	err := r.db.WithContext(ctx).Model(&domainreward.Reward{}).
		Distinct("erp_sku_id", "erp_warehouse_id").
		Where("status = ? AND erp_sku_id IS NOT NULL", domainreward.StatusActive).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]domainreward.SKUWarehouse, 0, len(rows))
	for _, row := range rows {
		out = append(out, domainreward.SKUWarehouse{SKUID: row.ERPSKUID, WarehouseID: row.ERPWarehouseID})
	}
	return out, nil
}

// ---- stock cache ----

func (r *rewardRepository) StockFor(ctx context.Context, skuID, warehouseID uint) (*domainreward.StockCache, error) {
	var s domainreward.StockCache
	err := r.db.WithContext(ctx).Where("erp_sku_id = ? AND erp_warehouse_id = ?", skuID, warehouseID).Limit(1).Find(&s).Error
	if err != nil {
		return nil, err
	}
	if s.ID == 0 {
		return nil, nil
	}
	return &s, nil
}

func (r *rewardRepository) StockForMany(ctx context.Context, keys []domainreward.SKUWarehouse) (map[domainreward.SKUWarehouse]domainreward.StockCache, error) {
	out := make(map[domainreward.SKUWarehouse]domainreward.StockCache, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	ids := make([]uint, 0, len(keys))
	for _, k := range keys {
		ids = append(ids, k.SKUID)
	}
	var rows []domainreward.StockCache
	if err := r.db.WithContext(ctx).Where("erp_sku_id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[domainreward.SKUWarehouse{SKUID: row.ERPSKUID, WarehouseID: row.ERPWarehouseID}] = row
	}
	return out, nil
}

func (r *rewardRepository) UpsertStock(ctx context.Context, rows []domainreward.StockCache) error {
	if len(rows) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "erp_sku_id"}, {Name: "erp_warehouse_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"available_qty", "synced_at"}),
	}).Create(&rows).Error
}

// ---- redemptions ----

func (r *rewardRepository) CountActiveRedemptions(ctx context.Context, rewardID, memberID uint) (int64, error) {
	var n int64
	err := r.db.WithContext(ctx).Model(&domainreward.Redemption{}).
		Where("reward_id = ? AND member_id = ? AND status <> ?", rewardID, memberID, domainreward.RedemptionCancelled).
		Count(&n).Error
	return n, err
}

func (r *rewardRepository) CreateRedemption(ctx context.Context, rd *domainreward.Redemption) error {
	if err := r.db.WithContext(ctx).Create(rd).Error; err != nil {
		return conflictOn(err)
	}
	return nil
}

func (r *rewardRepository) FindRedemptionByKey(ctx context.Context, memberID uint, key string) (*domainreward.Redemption, error) {
	var rd domainreward.Redemption
	if err := r.db.WithContext(ctx).Where("member_id = ? AND idempotency_key = ?", memberID, key).First(&rd).Error; err != nil {
		return nil, notFound(err)
	}
	return &rd, nil
}

func (r *rewardRepository) UpdateRedemption(ctx context.Context, rd *domainreward.Redemption) error {
	return r.db.WithContext(ctx).Save(rd).Error
}

func (r *rewardRepository) FindRedemption(ctx context.Context, id uint) (*domainreward.Redemption, error) {
	var rd domainreward.Redemption
	if err := r.db.WithContext(ctx).First(&rd, id).Error; err != nil {
		return nil, notFound(err)
	}
	return &rd, nil
}

func (r *rewardRepository) LockRedemption(ctx context.Context, id uint) (*domainreward.Redemption, error) {
	var rd domainreward.Redemption
	if err := forUpdate(r.db.WithContext(ctx)).First(&rd, id).Error; err != nil {
		return nil, notFound(err)
	}
	return &rd, nil
}

func (r *rewardRepository) FindRedemptions(ctx context.Context, q domainreward.RedemptionQuery) ([]domainreward.RedemptionRow, int64, error) {
	stmt := &gorm.Statement{DB: r.db}
	rdTable, mTable := tableOf(stmt, &domainreward.Redemption{}), tableOf(stmt, &domainmember.Member{})

	tx := r.db.WithContext(ctx).Table(rdTable + " AS rd").Joins("JOIN " + mTable + " AS m ON m.id = rd.member_id")
	if q.Status != "" {
		tx = tx.Where("rd.status = ?", q.Status)
	}
	if q.MemberID != 0 {
		tx = tx.Where("rd.member_id = ?", q.MemberID)
	}
	if q.RewardID != 0 {
		tx = tx.Where("rd.reward_id = ?", q.RewardID)
	}
	if q.From != nil {
		tx = tx.Where("rd.created_at >= ?", *q.From)
	}
	if q.To != nil {
		tx = tx.Where("rd.created_at < ?", *q.To)
	}
	if q.Search != "" {
		like := "%" + escapeLike(strings.ToLower(q.Search)) + "%"
		tx = tx.Where(`(LOWER(rd.redemption_no) LIKE ? ESCAPE '\' OR LOWER(m.member_code) LIKE ? ESCAPE '\' OR LOWER(m.first_name) LIKE ? ESCAPE '\' OR LOWER(m.last_name) LIKE ? ESCAPE '\')`,
			like, like, like, like)
	}
	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	tx = tx.Select("rd.*, m.member_code AS member_code, m.first_name AS first_name, m.last_name AS last_name").
		Order("rd.created_at DESC").Order("rd.id DESC")
	if q.Limit > 0 {
		tx = tx.Offset((q.Page - 1) * q.Limit).Limit(q.Limit)
	}
	var out []domainreward.RedemptionRow
	if err := tx.Scan(&out).Error; err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// conflictOn maps a unique-key violation to domain.ErrConflict.
func conflictOn(err error) error {
	if isDuplicate(err) {
		return domain.ErrConflict
	}
	return err
}
