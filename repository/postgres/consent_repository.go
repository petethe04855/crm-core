package postgres

import (
	"context"
	"errors"
	"time"

	"crm-core/domain"
	domainconsent "crm-core/domain/consent"

	"gorm.io/gorm"
)

type consentRepository struct {
	db *gorm.DB
}

// NewConsentRepository builds the consent repository.
func NewConsentRepository(db *gorm.DB) domainconsent.Repository {
	return &consentRepository{db: db}
}

func (r *consentRepository) ListDocuments(ctx context.Context) ([]domainconsent.Document, error) {
	var docs []domainconsent.Document
	err := r.db.WithContext(ctx).Order("type ASC, version DESC").Find(&docs).Error
	return docs, err
}

func (r *consentRepository) FindDocument(ctx context.Context, id uint) (*domainconsent.Document, error) {
	var doc domainconsent.Document
	if err := r.db.WithContext(ctx).First(&doc, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	return &doc, nil
}

// CurrentDocuments picks the highest active, effective version of each type.
// There are only a handful of rows, so the choice is made in Go.
func (r *consentRepository) CurrentDocuments(ctx context.Context, now time.Time) ([]domainconsent.Document, error) {
	var docs []domainconsent.Document
	err := r.db.WithContext(ctx).
		Where("is_active = ? AND effective_at <= ?", true, now).
		Order("type ASC, version DESC").
		Find(&docs).Error
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := make([]domainconsent.Document, 0, len(docs))
	for _, d := range docs {
		if seen[d.Type] {
			continue
		}
		seen[d.Type] = true
		out = append(out, d)
	}
	return out, nil
}

func (r *consentRepository) MaxVersion(ctx context.Context, docType string) (int, error) {
	var max *int
	err := r.db.WithContext(ctx).Model(&domainconsent.Document{}).
		Where("type = ?", docType).
		Select("MAX(version)").
		Scan(&max).Error
	if err != nil || max == nil {
		return 0, err
	}
	return *max, nil
}

func (r *consentRepository) CreateDocument(ctx context.Context, doc *domainconsent.Document) error {
	return r.db.WithContext(ctx).Create(doc).Error
}

func (r *consentRepository) UpdateDocument(ctx context.Context, doc *domainconsent.Document) error {
	return r.db.WithContext(ctx).Save(doc).Error
}

func (r *consentRepository) HasRecords(ctx context.Context, documentID uint) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&domainconsent.Record{}).
		Where("document_id = ?", documentID).
		Limit(1).
		Count(&count).Error
	return count > 0, err
}

func (r *consentRepository) LatestActions(ctx context.Context, lineUserID string, documentIDs []uint) (map[uint]string, error) {
	out := map[uint]string{}
	if lineUserID == "" || len(documentIDs) == 0 {
		return out, nil
	}
	var records []domainconsent.Record
	err := r.db.WithContext(ctx).
		Where("line_user_id = ? AND document_id IN ?", lineUserID, documentIDs).
		Order("id ASC").
		Find(&records).Error
	if err != nil {
		return nil, err
	}
	for _, rec := range records { // ascending ids: the last one wins
		out[rec.DocumentID] = rec.Action
	}
	return out, nil
}

func (r *consentRepository) CreateRecords(ctx context.Context, records []domainconsent.Record) error {
	if len(records) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Create(&records).Error
}

func (r *consentRepository) Records(ctx context.Context, lineUserID string) ([]domainconsent.Record, error) {
	var out []domainconsent.Record
	err := r.db.WithContext(ctx).Where("line_user_id = ?", lineUserID).Order("id ASC").Find(&out).Error
	return out, err
}
