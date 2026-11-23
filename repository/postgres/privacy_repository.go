package postgres

import (
	"context"
	"fmt"
	"time"

	domaincoupon "crm-core/domain/coupon"
	domainmember "crm-core/domain/member"
	domainprivacy "crm-core/domain/privacy"
	domainreward "crm-core/domain/reward"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type privacyRepository struct{ db *gorm.DB }

// NewPrivacyRepository builds the preferences / data request repository.
func NewPrivacyRepository(db *gorm.DB) domainprivacy.Repository {
	return &privacyRepository{db: db}
}

func (r *privacyRepository) Prefs(ctx context.Context, memberID uint) (domainprivacy.NotificationPrefs, error) {
	var p domainprivacy.NotificationPrefs
	err := r.db.WithContext(ctx).Where("member_id = ?", memberID).Limit(1).Find(&p).Error
	if err != nil {
		return p, err
	}
	if p.MemberID == 0 {
		return domainprivacy.DefaultPrefs(memberID), nil
	}
	return p, nil
}

func (r *privacyRepository) SavePrefs(ctx context.Context, p domainprivacy.NotificationPrefs) error {
	p.UpdatedAt = time.Now()
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "member_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"points_updates", "expiring_points", "birthday", "updated_at"}),
	}).Create(&p).Error
}

func (r *privacyRepository) PrefsFor(ctx context.Context, memberIDs []uint) (map[uint]domainprivacy.NotificationPrefs, error) {
	out := make(map[uint]domainprivacy.NotificationPrefs, len(memberIDs))
	for _, id := range memberIDs {
		out[id] = domainprivacy.DefaultPrefs(id)
	}
	if len(memberIDs) == 0 {
		return out, nil
	}
	var rows []domainprivacy.NotificationPrefs
	if err := r.db.WithContext(ctx).Where("member_id IN ?", memberIDs).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, p := range rows {
		out[p.MemberID] = p
	}
	return out, nil
}

func (r *privacyRepository) PendingRequest(ctx context.Context, memberID uint, typ string) (*domainprivacy.DataRequest, error) {
	var req domainprivacy.DataRequest
	err := r.db.WithContext(ctx).Where("member_id = ? AND type = ? AND status = ?", memberID, typ, domainprivacy.RequestPending).
		Order("id DESC").First(&req).Error
	if err != nil {
		return nil, notFound(err)
	}
	return &req, nil
}

func (r *privacyRepository) CreateRequest(ctx context.Context, req *domainprivacy.DataRequest) error {
	return r.db.WithContext(ctx).Create(req).Error
}

func (r *privacyRepository) FindRequests(ctx context.Context, q domainprivacy.RequestQuery) ([]domainprivacy.RequestRow, int64, error) {
	stmt := &gorm.Statement{DB: r.db}
	reqTable, mTable := tableOf(stmt, &domainprivacy.DataRequest{}), tableOf(stmt, &domainmember.Member{})
	tx := r.db.WithContext(ctx).Table(reqTable + " AS dr").Joins("JOIN " + mTable + " AS m ON m.id = dr.member_id")
	if q.Status != "" {
		tx = tx.Where("dr.status = ?", q.Status)
	}
	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if q.Page < 1 {
		q.Page = 1
	}
	if q.Limit < 1 || q.Limit > 100 {
		q.Limit = 20
	}
	var rows []domainprivacy.RequestRow
	err := tx.Select("dr.*, m.first_name AS first_name, m.last_name AS last_name, m.email AS email").
		Order("dr.created_at ASC").Order("dr.id ASC").
		Offset((q.Page - 1) * q.Limit).Limit(q.Limit).Scan(&rows).Error
	return rows, total, err
}

// NewPrivacyUnitOfWork runs account deletions in one transaction.
func NewPrivacyUnitOfWork(db *gorm.DB) domainprivacy.UnitOfWork { return &privacyUoW{db: db} }

type privacyUoW struct{ db *gorm.DB }

func (u *privacyUoW) Do(ctx context.Context, fn func(ctx context.Context, tx domainprivacy.AnonymizeTx) error) error {
	return u.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(ctx, &anonymizeTx{tx: tx})
	})
}

type anonymizeTx struct{ tx *gorm.DB }

func (t *anonymizeTx) LockRequest(ctx context.Context, id uint) (*domainprivacy.DataRequest, error) {
	var req domainprivacy.DataRequest
	if err := forUpdate(t.tx.WithContext(ctx)).First(&req, id).Error; err != nil {
		return nil, notFound(err)
	}
	return &req, nil
}

func (t *anonymizeTx) LockMember(ctx context.Context, id uint) (*domainmember.Member, error) {
	return lockMember(t.tx.WithContext(ctx), id)
}

func (t *anonymizeTx) CountOpenRedemptions(ctx context.Context, memberID uint) (int64, error) {
	var n int64
	err := t.tx.WithContext(ctx).Model(&domainreward.Redemption{}).
		Where("member_id = ? AND status IN ?", memberID, []string{
			domainreward.RedemptionPending, domainreward.RedemptionApproved, domainreward.RedemptionPacking,
		}).Count(&n).Error
	return n, err
}

func (t *anonymizeTx) ActiveLineUserID(ctx context.Context, memberID uint) (string, error) {
	var acc domainmember.MemberLineAccount
	err := t.tx.WithContext(ctx).Where("member_id = ? AND unlinked_at IS NULL", memberID).Limit(1).Find(&acc).Error
	return acc.LineUserID, err
}

func (t *anonymizeTx) Anonymize(ctx context.Context, memberID uint, at time.Time) error {
	db := t.tx.WithContext(ctx)
	if err := db.Model(&domainmember.Member{}).Where("id = ?", memberID).Updates(map[string]any{
		"first_name":            "สมาชิกที่ลบแล้ว",
		"last_name":             "-",
		"email":                 fmt.Sprintf("deleted-%d@deleted.invalid", memberID),
		"email_verified_at":     nil,
		"phone_e164":            nil,
		"birth_date":            nil,
		"birth_date_changed_at": nil,
		"gender":                domainmember.GenderUnspecified,
		"customer_id":           nil,
		"status":                domainmember.StatusDeleted,
		"deleted_at":            at,
		"updated_at":            at,
	}).Error; err != nil {
		return err
	}
	if err := db.Model(&domainmember.MemberLineAccount{}).Where("member_id = ?", memberID).Updates(map[string]any{
		"display_name": "", "picture_url": "", "unlinked_at": gorm.Expr("COALESCE(unlinked_at, ?)", at),
	}).Error; err != nil {
		return err
	}
	if err := db.Where("member_id = ?", memberID).Delete(&domainmember.MemberAddress{}).Error; err != nil {
		return err
	}
	if err := db.Where("member_id = ?", memberID).Delete(&domainprivacy.NotificationPrefs{}).Error; err != nil {
		return err
	}
	// Unused coupons die; used ones stay as sales history.
	if err := db.Model(&domaincoupon.MemberCoupon{}).
		Where("member_id = ? AND status = ?", memberID, domaincoupon.HolderAvailable).
		Updates(map[string]any{"status": domaincoupon.HolderCancelled, "cancelled_at": at, "cancel_reason": "PDPA account deletion"}).Error; err != nil {
		return err
	}
	// Shipping snapshots on finished redemptions hold addresses.
	return db.Model(&domainreward.Redemption{}).Where("member_id = ?", memberID).Updates(map[string]any{
		"ship_name": "-", "ship_phone": "", "ship_address": "-", "ship_postcode": "", "member_note": "",
	}).Error
}

func (t *anonymizeTx) CloseRequest(ctx context.Context, id uint, status string, by uint, note string, at time.Time) error {
	return t.tx.WithContext(ctx).Model(&domainprivacy.DataRequest{}).Where("id = ?", id).Updates(map[string]any{
		"status": status, "processed_by": by, "processed_at": at, "staff_note": note,
	}).Error
}
