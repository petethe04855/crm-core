// Package reward holds the reward catalogue (an ERP SKU and its point price),
// the cached ERP stock and the redemptions members make (plan
// CRM_BACKOFFICE_MARKETING.md §5, CRM_BACKOFFICE.md §5).
package reward

import (
	"context"
	"errors"
	"time"

	domainmember "crm-core/domain/member"
	domainpoints "crm-core/domain/points"
)

// Reward types. Only physical rewards are supported in the first round.
const (
	TypePhysical  = "physical"
	TypeDigital   = "digital"
	TypePrivilege = "privilege"
)

// Reward statuses.
const (
	StatusDraft    = "draft"
	StatusActive   = "active"
	StatusInactive = "inactive"
	StatusArchived = "archived"
)

// Redemption statuses, in order.
const (
	RedemptionPending   = "pending"
	RedemptionApproved  = "approved"
	RedemptionPacking   = "packing"
	RedemptionShipped   = "shipped"
	RedemptionDelivered = "delivered"
	RedemptionCancelled = "cancelled"
)

// Limits.
const (
	MaxNameRunes     = 120
	MaxTextRunes     = 2000
	MaxPointsCost    = 10_000_000
	DefaultWarehouse = 1
	// StockStaleAfter is when the backoffice warns that the ERP stock is old.
	StockStaleAfter = 30 * time.Minute
	// LowStockAt marks rewards as running low.
	LowStockAt = 10
)

// Errors the delivery layer maps to 409 with their own codes.
var (
	ErrNotAvailable    = errors.New("reward is not open for redemption")
	ErrOutOfStock      = errors.New("reward is out of stock")
	ErrLimitReached    = errors.New("member reached the redemption limit for this reward")
	ErrTierTooLow      = errors.New("member tier is below the reward minimum")
	ErrSKULocked       = errors.New("reward SKU cannot change after redemptions")
	ErrInvalidStatus   = errors.New("redemption is not in a status that allows this")
	ErrInsufficientERP = errors.New("ERP stock is not enough to ship")
	ErrERPUnavailable  = errors.New("ERP is unavailable")
	ErrCategoryInUse   = errors.New("category still has rewards")
	ErrUnsupportedType = errors.New("reward type is not supported yet")
)

// Category groups rewards (table crm.reward_categories).
type Category struct {
	ID        uint   `gorm:"primaryKey"`
	Name      string `gorm:"size:80;not null;uniqueIndex"`
	SortOrder int    `gorm:"not null"`
	IsActive  bool   `gorm:"not null"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Reward is one item members can redeem with points (table crm.rewards).
type Reward struct {
	ID               uint   `gorm:"primaryKey"`
	Name             string `gorm:"size:120;not null"`
	Description      string `gorm:"type:text"`
	ImageURL         string `gorm:"size:500"`
	CategoryID       *uint  `gorm:"index"`
	Type             string `gorm:"size:20;not null"`
	ERPSKUID         *uint  `gorm:"column:erp_sku_id;index"`
	SKUCodeSnapshot  string `gorm:"size:100"`
	SKUNameSnapshot  string `gorm:"size:255"`
	ERPWarehouseID   uint   `gorm:"column:erp_warehouse_id;not null"`
	QtyPerRedemption int    `gorm:"not null"`
	PointsCost       int64  `gorm:"not null;index"`
	// StockLimit caps redemptions on top of the ERP stock (nil = no cap).
	StockLimit     *int
	LimitPerMember *int
	MinTierID      *uint
	AvailableFrom  *time.Time
	AvailableTo    *time.Time
	Status         string `gorm:"size:10;not null;index"`
	RedeemedCount  int    `gorm:"not null"`
	SortOrder      int    `gorm:"not null"`

	CreatedBy      uint   `gorm:"not null"`
	CreatedByEmail string `gorm:"size:255"`
	UpdatedBy      uint   `gorm:"not null"`
	UpdatedByEmail string `gorm:"size:255"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// StockCache is the last ERP available quantity per SKU and warehouse
// (table crm.reward_stock_caches), refreshed by the stock-sync job.
type StockCache struct {
	ID             uint      `gorm:"primaryKey"`
	ERPSKUID       uint      `gorm:"column:erp_sku_id;not null;uniqueIndex:idx_reward_stock_sku_wh"`
	ERPWarehouseID uint      `gorm:"column:erp_warehouse_id;not null;uniqueIndex:idx_reward_stock_sku_wh"`
	AvailableQty   int       `gorm:"not null"`
	SyncedAt       time.Time `gorm:"not null"`
}

// TableName keeps the plural convention for the cache table.
func (StockCache) TableName() string { return "reward_stock_caches" }

// Redemption is one reward a member redeemed (table crm.redemptions).
type Redemption struct {
	ID           uint   `gorm:"primaryKey"`
	RedemptionNo string `gorm:"size:20;not null;uniqueIndex"`
	MemberID     uint   `gorm:"not null;index"`
	RewardID     uint   `gorm:"not null;index"`
	Qty          int    `gorm:"not null"`
	Points       int64  `gorm:"not null"`
	LedgerID     *uint
	Status       string `gorm:"size:12;not null;index"`

	// Shipping address snapshot (the member may change theirs later).
	ShipName     string `gorm:"size:150"`
	ShipPhone    string `gorm:"size:30"`
	ShipAddress  string `gorm:"size:500"`
	ShipProvince string `gorm:"size:100"`
	ShipPostcode string `gorm:"size:10"`
	MemberNote   string `gorm:"size:500"`

	// Snapshots so the list reads without the reward row.
	RewardName  string `gorm:"size:120"`
	SKUCode     string `gorm:"size:100"`
	ERPSKUID    *uint  `gorm:"column:erp_sku_id"`
	WarehouseID uint   `gorm:"not null"`

	TrackingNo    string `gorm:"size:100"`
	StockIssueRef string `gorm:"size:40"`
	CancelReason  string `gorm:"size:500"`

	ApprovedAt     *time.Time
	PackedAt       *time.Time
	ShippedAt      *time.Time
	DeliveredAt    *time.Time
	CancelledAt    *time.Time
	LastActorID    uint   `gorm:"not null"`
	LastActorEmail string `gorm:"size:255"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Query filters the reward list.
type Query struct {
	Search     string
	CategoryID uint
	Status     string
	LowStock   bool
	Page       int
	Limit      int
}

// RedemptionQuery filters redemptions.
type RedemptionQuery struct {
	Status   string
	MemberID uint
	RewardID uint
	From     *time.Time
	To       *time.Time // exclusive
	Search   string     // redemption no, member code or name
	Page     int
	Limit    int
}

// RedemptionRow is a redemption with its member for display.
type RedemptionRow struct {
	Redemption
	MemberCode string
	FirstName  string
	LastName   string
}

// Repository stores the catalogue, the stock cache and redemptions. Lock*
// read FOR UPDATE on Postgres; use them inside UnitOfWork.Do.
type Repository interface {
	ListCategories(ctx context.Context) ([]Category, error)
	FindCategory(ctx context.Context, id uint) (*Category, error)
	CreateCategory(ctx context.Context, c *Category) error
	UpdateCategory(ctx context.Context, c *Category) error
	DeleteCategory(ctx context.Context, id uint) error
	CountRewardsInCategory(ctx context.Context, id uint) (int64, error)

	FindAll(ctx context.Context, q Query) ([]Reward, int64, error)
	FindByID(ctx context.Context, id uint) (*Reward, error)
	LockByID(ctx context.Context, id uint) (*Reward, error)
	Create(ctx context.Context, r *Reward) error
	Update(ctx context.Context, r *Reward) error
	// ActiveSKUs lists (sku, warehouse) pairs of active physical rewards.
	ActiveSKUs(ctx context.Context) ([]SKUWarehouse, error)

	StockFor(ctx context.Context, skuID, warehouseID uint) (*StockCache, error)
	StockForMany(ctx context.Context, keys []SKUWarehouse) (map[SKUWarehouse]StockCache, error)
	UpsertStock(ctx context.Context, rows []StockCache) error

	CountActiveRedemptions(ctx context.Context, rewardID, memberID uint) (int64, error)
	CreateRedemption(ctx context.Context, r *Redemption) error
	UpdateRedemption(ctx context.Context, r *Redemption) error
	FindRedemption(ctx context.Context, id uint) (*Redemption, error)
	LockRedemption(ctx context.Context, id uint) (*Redemption, error)
	FindRedemptions(ctx context.Context, q RedemptionQuery) ([]RedemptionRow, int64, error)
}

// SKUWarehouse identifies one cached stock row.
type SKUWarehouse struct {
	SKUID       uint
	WarehouseID uint
}

// TxRepos are repositories bound to one transaction.
type TxRepos struct {
	Rewards Repository
	Points  domainpoints.Repository
	Members domainmember.Repository
}

// UnitOfWork runs fn in one transaction.
type UnitOfWork interface {
	Do(ctx context.Context, fn func(ctx context.Context, r TxRepos) error) error
}

// StockLevel is what the ERP reports for one SKU.
type StockLevel struct {
	SKUID        uint
	WarehouseID  uint
	AvailableQty int
}

// GoodsIssue asks the ERP to deduct stock when a reward ships.
type GoodsIssue struct {
	IdempotencyKey string
	WarehouseID    uint
	SKUID          uint
	Qty            int
	Reference      string
	Note           string
}

// ERP is what the reward module needs from erp-api-v2. backoffice-api adapts
// its HTTP client to it. IssueGoods returns ErrInsufficientERP when stock is
// short and ErrERPUnavailable when the ERP cannot be reached (retry is safe:
// the idempotency key prevents a double deduction).
type ERP interface {
	Stock(ctx context.Context, skuIDs []uint, warehouseID uint) ([]StockLevel, error)
	IssueGoods(ctx context.Context, gi GoodsIssue) (ref string, err error)
}
