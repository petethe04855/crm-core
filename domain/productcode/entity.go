// Package productcode defines the codes printed on product packaging: batches
// created in the backoffice (L4B) and redeemed by members in the LINE OA (L4).
// Only an HMAC and the last four characters of each code are stored; the full
// codes exist once, in the print file exported from the backoffice.
package productcode

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm/schema"
)

// Batch statuses: draft → exported (codes generated into the print file) →
// active (members can redeem) ; any → void.
const (
	BatchDraft    = "draft"
	BatchExported = "exported"
	BatchActive   = "active"
	BatchVoid     = "void"
)

// Code statuses.
const (
	CodeActive = "active"
	CodeUsed   = "used"
	CodeVoid   = "void"
)

// Redemption attempt results (used by L4).
const (
	AttemptSuccess       = "success"
	AttemptNotFound      = "not_found"
	AttemptUsed          = "used"
	AttemptExpired       = "expired"
	AttemptVoid          = "void"
	AttemptInvalidFormat = "invalid_format"
)

// Limits.
const (
	MaxBatchQuantity = 100_000
	MaxPointsPerCode = 100_000
)

// Errors.
var (
	ErrNotDraft        = errors.New("batch is not a draft")
	ErrAlreadyExported = errors.New("batch codes were already exported")
	ErrNotExported     = errors.New("batch must be exported before activation")
	ErrLotRequired     = errors.New("lot number is required to activate a non-test batch")
	ErrExpired         = errors.New("batch code expiry has passed")
	ErrAlreadyVoid     = errors.New("batch is already void")
	ErrNotConfigured   = errors.New("PRODUCT_CODE_HMAC_SECRET is not configured")
)

// Batch is one print run of codes for one SKU (and one ERP stock lot).
type Batch struct {
	ID      uint   `gorm:"primaryKey"`
	BatchNo string `gorm:"size:20;uniqueIndex;not null"`
	// ERP SKU snapshot (no cross-database FK).
	SKUID            uint    `gorm:"column:sku_id;index;not null"`
	SKUCode          string  `gorm:"column:sku_code;size:100;not null"`
	SKUNameSnapshot  string  `gorm:"column:sku_name_snapshot;size:255;not null"`
	SKUPriceSnapshot float64 `gorm:"column:sku_price_snapshot;type:numeric(14,2);not null;default:0"`
	Quantity         int     `gorm:"not null"`
	PointsPerCode    int64   `gorm:"not null"`
	// SpendValuePerCode is added to the member's lifetime_spend per code.
	SpendValuePerCode float64 `gorm:"type:numeric(14,2);not null;default:0"`
	// ERPLotID links an ERP stock lot once the ERP exposes lots; LotNumber is
	// typed by staff until then.
	ERPLotID      *uint      `gorm:"column:erp_lot_id;index"`
	LotNumber     string     `gorm:"size:100"`
	LotExpiryDate *time.Time `gorm:"type:date"`
	CodeExpiresAt *time.Time `gorm:"index"`
	IsTest        bool       `gorm:"not null;default:false"`
	Status        string     `gorm:"size:20;not null;index"`
	Note          string     `gorm:"size:500"`

	ExportedAt     *time.Time
	ExportedBy     uint `gorm:"not null;default:0"`
	ActivatedAt    *time.Time
	ActivatedBy    uint `gorm:"not null;default:0"`
	VoidedAt       *time.Time
	VoidedBy       uint   `gorm:"not null;default:0"`
	VoidReason     string `gorm:"size:500"`
	CreatedBy      uint   `gorm:"not null;default:0"`
	CreatedByEmail string `gorm:"size:255"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// TableName names the table product_code_batches and keeps the schema prefix.
func (Batch) TableName(namer schema.Namer) string { return namer.TableName("ProductCodeBatch") }

// Code is one printed code. CodeHash is the HMAC of the normalized code.
type Code struct {
	ID             uint   `gorm:"primaryKey"`
	BatchID        uint   `gorm:"index;not null"`
	CodeHash       string `gorm:"size:64;uniqueIndex;not null"`
	CodeSuffix     string `gorm:"size:4;index;not null"`
	Status         string `gorm:"size:20;not null;index"`
	UsedByMemberID *uint  `gorm:"index"`
	UsedAt         *time.Time
	LedgerID       *uint
	CreatedAt      time.Time
}

// TableName names the table product_codes and keeps the schema prefix.
func (Code) TableName(namer schema.Namer) string { return namer.TableName("ProductCode") }

// RedemptionAttempt is one code a member typed (rate limits, fraud review,
// history). Written by L4.
type RedemptionAttempt struct {
	ID            uint      `gorm:"primaryKey"`
	MemberID      uint      `gorm:"index;not null"`
	InputSuffix   string    `gorm:"size:4"`
	Result        string    `gorm:"size:20;not null;index"`
	ProductCodeID *uint     `gorm:"index"`
	IP            string    `gorm:"size:64"`
	CreatedAt     time.Time `gorm:"index"`
}

// TableName names the table code_redemption_attempts.
func (RedemptionAttempt) TableName(namer schema.Namer) string {
	return namer.TableName("CodeRedemptionAttempt")
}

// TestMember may redeem codes from test batches (everyone else gets not_found).
type TestMember struct {
	ID       uint `gorm:"primaryKey"`
	MemberID uint `gorm:"uniqueIndex;not null"`
	AddedBy  uint `gorm:"not null;default:0"`
	AddedAt  time.Time
}

// TableName names the table test_members.
func (TestMember) TableName(namer schema.Namer) string { return namer.TableName("TestMember") }

// Stats counts a batch's codes by status.
type Stats struct {
	Total  int64
	Active int64
	Used   int64
	Void   int64
}

// BatchWithStats is a batch plus its code counts.
type BatchWithStats struct {
	Batch
	Stats Stats
}

// Query filters the batch list.
type Query struct {
	Search string // batch no, SKU code / name, lot number
	Status string
	Page   int
	Limit  int
}

// Repository is the persistence contract for batches.
type Repository interface {
	FindAll(ctx context.Context, q Query) ([]BatchWithStats, int64, error)
	FindByID(ctx context.Context, id uint) (*BatchWithStats, error)
	Create(ctx context.Context, b *Batch) error
	Update(ctx context.Context, b *Batch) error
}

// Tx is the export / void work bound to one transaction.
type Tx interface {
	LockBatch(ctx context.Context, id uint) (*Batch, error)
	UpdateBatch(ctx context.Context, b *Batch) error
	// InsertCodes inserts the codes, skipping any whose hash already exists,
	// and returns the hashes that were skipped.
	InsertCodes(ctx context.Context, codes []Code) (skipped map[string]bool, err error)
	VoidActiveCodes(ctx context.Context, batchID uint) (int64, error)
}

// UnitOfWork runs fn in one transaction.
type UnitOfWork interface {
	Do(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error
}
