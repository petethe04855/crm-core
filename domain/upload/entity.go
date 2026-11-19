// Package upload stores the metadata of images uploaded through the backoffice
// (promotion banners, reward pictures). The bytes live on the storage backend;
// this row is the source of truth for the public file URL.
package upload

import (
	"context"
	"time"
)

// MaxImageBytes is the largest image the API accepts (2 MB).
const MaxImageBytes = 2 << 20

// Allowed image MIME types, detected from the file's magic bytes.
const (
	MIMEJPEG = "image/jpeg"
	MIMEPNG  = "image/png"
	MIMEWebP = "image/webp"
)

// UploadedFile is one stored image.
type UploadedFile struct {
	// ID is the uuid used in the public URL and as the stored file name.
	ID string `gorm:"primaryKey;size:36"`
	// Path is the storage key (relative path on disk), never a user path.
	Path string `gorm:"size:255;not null"`
	MIME string `gorm:"size:100;not null"`
	Size int64  `gorm:"not null"`
	// UploadedBy is the staff email that uploaded the file.
	UploadedBy string    `gorm:"size:255;not null"`
	CreatedAt  time.Time `gorm:"not null"`
}

// Repository reads and writes upload metadata.
type Repository interface {
	Create(ctx context.Context, file *UploadedFile) error
	FindByID(ctx context.Context, id string) (*UploadedFile, error)
}
