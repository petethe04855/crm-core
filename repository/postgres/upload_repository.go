package postgres

import (
	"context"
	"errors"

	"crm-core/domain"
	domainupload "crm-core/domain/upload"

	"gorm.io/gorm"
)

type uploadRepository struct {
	db *gorm.DB
}

// NewUploadRepository builds the uploaded-file repository.
func NewUploadRepository(db *gorm.DB) domainupload.Repository {
	return &uploadRepository{db: db}
}

func (r *uploadRepository) Create(ctx context.Context, file *domainupload.UploadedFile) error {
	return r.db.WithContext(ctx).Create(file).Error
}

func (r *uploadRepository) FindByID(ctx context.Context, id string) (*domainupload.UploadedFile, error) {
	var file domainupload.UploadedFile
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&file).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	return &file, nil
}
