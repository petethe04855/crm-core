package postgres

import (
	"context"
	"errors"
	"strings"
	"time"

	"crm-core/domain"
	domaincoupon "crm-core/domain/coupon"
	domainmember "crm-core/domain/member"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type couponRepository struct {
	db *gorm.DB
}

// NewCouponRepository builds the coupon repository.
func NewCouponRepository(db *gorm.DB) domaincoupon.Repository {
	return &couponRepository{db: db}
}

// NewCouponUnitOfWork runs coupon workflows (claim, cancel) in one transaction
// together with the point ledger and the member row.
func NewCouponUnitOfWork(db *gorm.DB) domaincoupon.UnitOfWork {
	return &couponUnitOfWork{db: db}
}

type couponUnitOfWork struct {
	db *gorm.DB
}

func (u *couponUnitOfWork) Do(ctx context.Context, fn func(ctx context.Context, r domaincoupon.TxRepos) error) error {
	return u.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(ctx, domaincoupon.TxRepos{
			Coupons: NewCouponRepository(tx),
			Points:  NewPointsRepository(tx),
			Members: NewMemberRepository(tx),
		})
	})
}

func notFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.ErrNotFound
	}
	return err
}

// forUpdate adds FOR UPDATE on Postgres (SQLite tests run on one connection).
func forUpdate(tx *gorm.DB) *gorm.DB {
	if tx.Dialector.Name() == "postgres" {
		return tx.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	return tx
}

func (r *couponRepository) FindAll(ctx context.Context, q domaincoupon.Query) ([]domaincoupon.Coupon, int64, error) {
	tx := r.db.WithContext(ctx).Model(&domaincoupon.Coupon{})
	if q.Status != "" {
		switch q.Status {
		case "public":
			tx = tx.Where("status = ?", domaincoupon.StatusActive)
		case "unpublic":
			tx = tx.Where("status <> ?", domaincoupon.StatusActive)
		default:
			tx = tx.Where("status = ?", q.Status)
		}
	}
	if q.Channel != "" {
		tx = tx.Where("channel = ?", q.Channel)
	}
	if q.Search != "" {
		like := "%" + escapeLike(strings.ToLower(q.Search)) + "%"
		tx = tx.Where(`LOWER(name) LIKE ? ESCAPE '\'`, like)
	}
	// Open-ended claim windows overlap everything on their open side.
	if q.OverlapFrom != nil && q.OverlapTo != nil {
		tx = tx.Where("(claim_start_at IS NULL OR claim_start_at < ?) AND (claim_end_at IS NULL OR claim_end_at > ?)",
			*q.OverlapTo, *q.OverlapFrom)
	}
	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var out []domaincoupon.Coupon
	err := tx.Order("id DESC").Offset((q.Page - 1) * q.Limit).Limit(q.Limit).Find(&out).Error
	return out, total, err
}

func (r *couponRepository) FindByID(ctx context.Context, id uint) (*domaincoupon.Coupon, error) {
	var c domaincoupon.Coupon
	if err := r.db.WithContext(ctx).First(&c, id).Error; err != nil {
		return nil, notFound(err)
	}
	return &c, nil
}

func (r *couponRepository) LockByID(ctx context.Context, id uint) (*domaincoupon.Coupon, error) {
	var c domaincoupon.Coupon
	if err := forUpdate(r.db.WithContext(ctx)).First(&c, id).Error; err != nil {
		return nil, notFound(err)
	}
	return &c, nil
}

func (r *couponRepository) Create(ctx context.Context, c *domaincoupon.Coupon) error {
	return r.db.WithContext(ctx).Create(c).Error
}

func (r *couponRepository) Update(ctx context.Context, c *domaincoupon.Coupon) error {
	// Callers load the row first; Save keeps zero values (counts, flags).
	return r.db.WithContext(ctx).Save(c).Error
}

func (r *couponRepository) Delete(ctx context.Context, id uint) error {
	res := r.db.WithContext(ctx).Delete(&domaincoupon.Coupon{}, id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *couponRepository) CountActiveClaims(ctx context.Context, couponID, memberID uint) (int64, error) {
	var n int64
	err := r.db.WithContext(ctx).Model(&domaincoupon.MemberCoupon{}).
		Where("coupon_id = ? AND member_id = ? AND status <> ?", couponID, memberID, domaincoupon.HolderCancelled).
		Count(&n).Error
	return n, err
}

func (r *couponRepository) CreateMemberCoupon(ctx context.Context, mc *domaincoupon.MemberCoupon) error {
	if err := r.db.WithContext(ctx).Create(mc).Error; err != nil {
		if isDuplicate(err) {
			return domain.ErrConflict
		}
		return err
	}
	return nil
}

func (r *couponRepository) FindMemberCouponByCode(ctx context.Context, code string) (*domaincoupon.MemberCoupon, error) {
	var mc domaincoupon.MemberCoupon
	if err := r.db.WithContext(ctx).Where("code = ?", code).First(&mc).Error; err != nil {
		return nil, notFound(err)
	}
	return &mc, nil
}

func (r *couponRepository) LockMemberCoupon(ctx context.Context, id uint) (*domaincoupon.MemberCoupon, error) {
	var mc domaincoupon.MemberCoupon
	if err := forUpdate(r.db.WithContext(ctx)).First(&mc, id).Error; err != nil {
		return nil, notFound(err)
	}
	return &mc, nil
}

func (r *couponRepository) LockMemberCouponByCode(ctx context.Context, code string) (*domaincoupon.MemberCoupon, error) {
	var mc domaincoupon.MemberCoupon
	if err := forUpdate(r.db.WithContext(ctx)).Where("code = ?", code).First(&mc).Error; err != nil {
		return nil, notFound(err)
	}
	return &mc, nil
}

func (r *couponRepository) UpdateMemberCoupon(ctx context.Context, mc *domaincoupon.MemberCoupon) error {
	return r.db.WithContext(ctx).Save(mc).Error
}

func (r *couponRepository) FindHolders(ctx context.Context, q domaincoupon.HolderQuery) ([]domaincoupon.Holder, int64, error) {
	// Table names come from the naming strategy so the schema prefix applies.
	stmt := &gorm.Statement{DB: r.db}
	mcTable, cTable, mTable := tableOf(stmt, &domaincoupon.MemberCoupon{}), tableOf(stmt, &domaincoupon.Coupon{}), tableOf(stmt, &domainmember.Member{})

	tx := r.db.WithContext(ctx).Table(mcTable + " AS mc").
		Joins("JOIN " + cTable + " AS c ON c.id = mc.coupon_id").
		Joins("JOIN " + mTable + " AS m ON m.id = mc.member_id")
	if q.CouponID != 0 {
		tx = tx.Where("mc.coupon_id = ?", q.CouponID)
	}
	if q.MemberID != 0 {
		tx = tx.Where("mc.member_id = ?", q.MemberID)
	}
	if q.Status != "" {
		tx = tx.Where("mc.status = ?", q.Status)
	}
	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	tx = tx.Select(`mc.*, m.member_code AS member_code, m.first_name AS first_name, m.last_name AS last_name,
		m.email AS email, m.phone_e164 AS phone, c.name AS coupon_name, c.benefit_type AS benefit_type,
		c.shared_code AS shared_code`).
		Order("mc.claimed_at DESC").Order("mc.id DESC")
	if q.Limit > 0 {
		tx = tx.Offset((q.Page - 1) * q.Limit).Limit(q.Limit)
	}
	var out []domaincoupon.Holder
	if err := tx.Scan(&out).Error; err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

func tableOf(stmt *gorm.Statement, model any) string {
	if err := stmt.Parse(model); err != nil {
		return ""
	}
	return stmt.Schema.Table
}

func (r *couponRepository) ExpireDue(ctx context.Context, now time.Time) (int64, error) {
	res := r.db.WithContext(ctx).Model(&domaincoupon.MemberCoupon{}).
		Where("status = ? AND expires_at <= ?", domaincoupon.HolderAvailable, now).
		Updates(map[string]any{"status": domaincoupon.HolderExpired, "updated_at": now})
	return res.RowsAffected, res.Error
}
