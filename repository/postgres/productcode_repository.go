package postgres

import (
	"context"
	"strings"

	domainproductcode "crm-core/domain/productcode"

	"gorm.io/gorm"
)

type productCodeRepository struct{ db *gorm.DB }

// NewProductCodeRepository builds the batch repository.
func NewProductCodeRepository(db *gorm.DB) domainproductcode.Repository {
	return &productCodeRepository{db: db}
}

func (r *productCodeRepository) FindAll(ctx context.Context, q domainproductcode.Query) ([]domainproductcode.BatchWithStats, int64, error) {
	tx := r.db.WithContext(ctx).Model(&domainproductcode.Batch{})
	if q.Status != "" {
		tx = tx.Where("status = ?", q.Status)
	}
	if s := strings.ToLower(strings.TrimSpace(q.Search)); s != "" {
		like := "%" + escapeLike(s) + "%"
		tx = tx.Where(`LOWER(batch_no) LIKE ? ESCAPE '\' OR LOWER(sku_code) LIKE ? ESCAPE '\' OR LOWER(sku_name_snapshot) LIKE ? ESCAPE '\' OR LOWER(lot_number) LIKE ? ESCAPE '\'`,
			like, like, like, like)
	}
	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page, limit := q.Page, q.Limit
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	var batches []domainproductcode.Batch
	if err := tx.Order("id DESC").Offset((page - 1) * limit).Limit(limit).Find(&batches).Error; err != nil {
		return nil, 0, err
	}
	out, err := r.withStats(ctx, batches)
	return out, total, err
}

func (r *productCodeRepository) FindByID(ctx context.Context, id uint) (*domainproductcode.BatchWithStats, error) {
	var b domainproductcode.Batch
	if err := r.db.WithContext(ctx).First(&b, id).Error; err != nil {
		return nil, notFound(err)
	}
	out, err := r.withStats(ctx, []domainproductcode.Batch{b})
	if err != nil {
		return nil, err
	}
	return &out[0], nil
}

func (r *productCodeRepository) withStats(ctx context.Context, batches []domainproductcode.Batch) ([]domainproductcode.BatchWithStats, error) {
	out := make([]domainproductcode.BatchWithStats, len(batches))
	if len(batches) == 0 {
		return out, nil
	}
	ids := make([]uint, len(batches))
	index := map[uint]int{}
	for i, b := range batches {
		ids[i] = b.ID
		index[b.ID] = i
		out[i].Batch = b
	}
	var rows []struct {
		BatchID uint
		Status  string
		N       int64
	}
	err := r.db.WithContext(ctx).Model(&domainproductcode.Code{}).
		Select("batch_id, status, COUNT(*) AS n").
		Where("batch_id IN ?", ids).
		Group("batch_id, status").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		s := &out[index[row.BatchID]].Stats
		s.Total += row.N
		switch row.Status {
		case domainproductcode.CodeActive:
			s.Active += row.N
		case domainproductcode.CodeUsed:
			s.Used += row.N
		case domainproductcode.CodeVoid:
			s.Void += row.N
		}
	}
	return out, nil
}

func (r *productCodeRepository) Create(ctx context.Context, b *domainproductcode.Batch) error {
	return r.db.WithContext(ctx).Create(b).Error
}

func (r *productCodeRepository) Update(ctx context.Context, b *domainproductcode.Batch) error {
	return r.db.WithContext(ctx).Save(b).Error
}

// NewProductCodeUnitOfWork runs exports and voids in one transaction.
func NewProductCodeUnitOfWork(db *gorm.DB) domainproductcode.UnitOfWork {
	return &productCodeUnitOfWork{db: db}
}

type productCodeUnitOfWork struct{ db *gorm.DB }

func (u *productCodeUnitOfWork) Do(ctx context.Context, fn func(ctx context.Context, tx domainproductcode.Tx) error) error {
	return u.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(ctx, &productCodeTx{tx: tx})
	})
}

type productCodeTx struct{ tx *gorm.DB }

func (t *productCodeTx) LockBatch(ctx context.Context, id uint) (*domainproductcode.Batch, error) {
	var b domainproductcode.Batch
	if err := forUpdate(t.tx.WithContext(ctx)).First(&b, id).Error; err != nil {
		return nil, notFound(err)
	}
	return &b, nil
}

func (t *productCodeTx) UpdateBatch(ctx context.Context, b *domainproductcode.Batch) error {
	return t.tx.WithContext(ctx).Save(b).Error
}

// insertChunk keeps each statement well under Postgres' 65535 parameters.
const insertChunk = 1000

func (t *productCodeTx) InsertCodes(ctx context.Context, codes []domainproductcode.Code) (map[string]bool, error) {
	db := t.tx.WithContext(ctx)
	skipped := map[string]bool{}
	for start := 0; start < len(codes); start += insertChunk {
		end := min(start+insertChunk, len(codes))
		chunk := codes[start:end]
		hashes := make([]string, len(chunk))
		for i, c := range chunk {
			hashes[i] = c.CodeHash
		}
		var existing []string
		if err := db.Model(&domainproductcode.Code{}).Where("code_hash IN ?", hashes).Pluck("code_hash", &existing).Error; err != nil {
			return nil, err
		}
		fresh := chunk
		if len(existing) > 0 {
			taken := map[string]bool{}
			for _, h := range existing {
				taken[h] = true
				skipped[h] = true
			}
			fresh = make([]domainproductcode.Code, 0, len(chunk))
			for _, c := range chunk {
				if !taken[c.CodeHash] {
					fresh = append(fresh, c)
				}
			}
		}
		if len(fresh) == 0 {
			continue
		}
		if err := db.Create(&fresh).Error; err != nil {
			return nil, err
		}
	}
	return skipped, nil
}

func (t *productCodeTx) VoidActiveCodes(ctx context.Context, batchID uint) (int64, error) {
	res := t.tx.WithContext(ctx).Model(&domainproductcode.Code{}).
		Where("batch_id = ? AND status = ?", batchID, domainproductcode.CodeActive).
		Update("status", domainproductcode.CodeVoid)
	return res.RowsAffected, res.Error
}
