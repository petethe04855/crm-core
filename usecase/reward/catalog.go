// Package reward implements the reward catalogue (plan §5) and the redemption
// workflow up to shipping (CRM_BACKOFFICE.md §5): redeem with points, approve,
// pack, ship (ERP goods issue first), deliver, or cancel with a refund.
package reward

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"crm-core/domain"
	domainloyalty "crm-core/domain/loyalty"
	domainmember "crm-core/domain/member"
	domainpoints "crm-core/domain/points"
	domainreward "crm-core/domain/reward"
	usecasepoints "crm-core/usecase/points"
)

const (
	defaultLimit = 20
	maxLimit     = 100
)

// Points is what rewards need from the points usecase (T3).
type Points interface {
	Check(ctx context.Context, member *domainmember.Member, cost int64, at time.Time) error
	SpendInTx(ctx context.Context, repo domainpoints.Repository, req usecasepoints.SpendRequest) (*domainpoints.PointLedger, error)
	RefundInTx(ctx context.Context, repo domainpoints.Repository, req usecasepoints.RefundRequest) (*domainpoints.PointLedger, error)
}

// Input is the reward create/update body (PATCH replaces every field).
type Input struct {
	Name             string
	Description      string
	ImageURL         string
	CategoryID       *uint
	Type             string
	ERPSKUID         *uint
	SKUCode          string
	SKUName          string
	ERPWarehouseID   uint
	QtyPerRedemption int
	PointsCost       int64
	StockLimit       *int
	LimitPerMember   *int
	MinTierID        *uint
	AvailableFrom    *time.Time
	AvailableTo      *time.Time
	SortOrder        int
}

// View is a reward with what the backoffice shows about its stock.
type View struct {
	Reward          domainreward.Reward
	ERPAvailableQty *int // nil = never synced
	StockSyncedAt   *time.Time
	StockStale      bool
	RemainingQuota  int
}

// Usecase manages the catalogue and redemptions.
type Usecase interface {
	Categories(ctx context.Context) ([]domainreward.Category, error)
	CreateCategory(ctx context.Context, name string, sortOrder int) (*domainreward.Category, error)
	UpdateCategory(ctx context.Context, id uint, name string, sortOrder int, active bool) (*domainreward.Category, error)
	DeleteCategory(ctx context.Context, id uint) error

	List(ctx context.Context, q domainreward.Query) ([]View, int64, error)
	Get(ctx context.Context, id uint) (*View, error)
	Create(ctx context.Context, actor domain.Actor, in Input) (*View, error)
	Update(ctx context.Context, actor domain.Actor, id uint, in Input) (*View, error)
	SetStatus(ctx context.Context, actor domain.Actor, id uint, status string) (*View, error)
	SyncStock(ctx context.Context) (int, error)

	Redeem(ctx context.Context, req RedeemRequest) (*domainreward.Redemption, error)
	Redemptions(ctx context.Context, q domainreward.RedemptionQuery) ([]domainreward.RedemptionRow, int64, error)
	Redemption(ctx context.Context, id uint) (*domainreward.Redemption, error)
	Approve(ctx context.Context, actor domain.Actor, id uint) (*domainreward.Redemption, error)
	Pack(ctx context.Context, actor domain.Actor, id uint) (*domainreward.Redemption, error)
	Ship(ctx context.Context, actor domain.Actor, id uint, trackingNo string) (*domainreward.Redemption, error)
	Deliver(ctx context.Context, actor domain.Actor, id uint) (*domainreward.Redemption, error)
	Cancel(ctx context.Context, actor domain.Actor, id uint, reason string) (*domainreward.Redemption, error)
}

type usecase struct {
	rewards domainreward.Repository
	uow     domainreward.UnitOfWork
	members domainmember.Repository
	tiers   domainloyalty.TierRepository
	points  Points
	erp     domainreward.ERP
	now     func() time.Time
}

// New builds the reward usecase. erp may be nil (no integration token): stock
// sync is then skipped and shipping fails with ErrERPUnavailable.
func New(rewards domainreward.Repository, uow domainreward.UnitOfWork, members domainmember.Repository,
	tiers domainloyalty.TierRepository, points Points, erp domainreward.ERP) Usecase {
	return &usecase{rewards: rewards, uow: uow, members: members, tiers: tiers, points: points, erp: erp, now: time.Now}
}

// RemainingQuota is how many more redemptions the reward allows (plan §5.3):
// min(floor(ERP available ÷ qty per redemption), stock limit − redeemed).
// A missing cache counts as zero stock for physical rewards; an unset stock
// limit means no cap; the result is never negative.
func RemainingQuota(r domainreward.Reward, cache *domainreward.StockCache) int {
	const unlimited = int(^uint(0) >> 1)
	left := unlimited
	if r.Type == domainreward.TypePhysical {
		available := 0
		if cache != nil {
			available = cache.AvailableQty
		}
		per := r.QtyPerRedemption
		if per < 1 {
			per = 1
		}
		left = available / per
	}
	if r.StockLimit != nil {
		if cap := *r.StockLimit - r.RedeemedCount; cap < left {
			left = cap
		}
	}
	if left < 0 {
		return 0
	}
	return left
}

func (u *usecase) view(r domainreward.Reward, cache *domainreward.StockCache) View {
	v := View{Reward: r, RemainingQuota: RemainingQuota(r, cache)}
	if cache != nil {
		qty, at := cache.AvailableQty, cache.SyncedAt
		v.ERPAvailableQty, v.StockSyncedAt = &qty, &at
		v.StockStale = u.now().Sub(cache.SyncedAt) > domainreward.StockStaleAfter
	} else if r.ERPSKUID != nil {
		v.StockStale = true
	}
	return v
}

func (u *usecase) cacheFor(ctx context.Context, repo domainreward.Repository, r domainreward.Reward) (*domainreward.StockCache, error) {
	if r.ERPSKUID == nil {
		return nil, nil
	}
	return repo.StockFor(ctx, *r.ERPSKUID, r.ERPWarehouseID)
}

// ---- categories ----

func (u *usecase) Categories(ctx context.Context) ([]domainreward.Category, error) {
	return u.rewards.ListCategories(ctx)
}

func validCategoryName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > 80 {
		return "", domain.NewValidationError("name", "is required (80 characters max)")
	}
	return name, nil
}

func (u *usecase) CreateCategory(ctx context.Context, name string, sortOrder int) (*domainreward.Category, error) {
	name, err := validCategoryName(name)
	if err != nil {
		return nil, err
	}
	c := &domainreward.Category{Name: name, SortOrder: sortOrder, IsActive: true}
	if err := u.rewards.CreateCategory(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

func (u *usecase) UpdateCategory(ctx context.Context, id uint, name string, sortOrder int, active bool) (*domainreward.Category, error) {
	name, err := validCategoryName(name)
	if err != nil {
		return nil, err
	}
	c, err := u.rewards.FindCategory(ctx, id)
	if err != nil {
		return nil, err
	}
	c.Name, c.SortOrder, c.IsActive = name, sortOrder, active
	if err := u.rewards.UpdateCategory(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

func (u *usecase) DeleteCategory(ctx context.Context, id uint) error {
	n, err := u.rewards.CountRewardsInCategory(ctx, id)
	if err != nil {
		return err
	}
	if n > 0 {
		return domainreward.ErrCategoryInUse
	}
	return u.rewards.DeleteCategory(ctx, id)
}

// ---- rewards ----

func (u *usecase) List(ctx context.Context, q domainreward.Query) ([]View, int64, error) {
	q.Search = strings.TrimSpace(q.Search)
	if q.Page < 1 {
		q.Page = 1
	}
	if q.Limit < 1 {
		q.Limit = defaultLimit
	}
	if q.Limit > maxLimit {
		q.Limit = maxLimit
	}
	if q.Status != "" && !validStatus(q.Status) {
		return nil, 0, domain.NewValidationError("status", "must be one of draft, active, inactive, archived")
	}
	rows, total, err := u.rewards.FindAll(ctx, q)
	if err != nil {
		return nil, 0, err
	}
	keys := make([]domainreward.SKUWarehouse, 0, len(rows))
	for _, r := range rows {
		if r.ERPSKUID != nil {
			keys = append(keys, domainreward.SKUWarehouse{SKUID: *r.ERPSKUID, WarehouseID: r.ERPWarehouseID})
		}
	}
	caches, err := u.rewards.StockForMany(ctx, keys)
	if err != nil {
		return nil, 0, err
	}
	out := make([]View, 0, len(rows))
	for _, r := range rows {
		var cache *domainreward.StockCache
		if r.ERPSKUID != nil {
			if c, ok := caches[domainreward.SKUWarehouse{SKUID: *r.ERPSKUID, WarehouseID: r.ERPWarehouseID}]; ok {
				cache = &c
			}
		}
		out = append(out, u.view(r, cache))
	}
	return out, total, nil
}

func (u *usecase) Get(ctx context.Context, id uint) (*View, error) {
	if id == 0 {
		return nil, domain.NewValidationError("id", "is required")
	}
	r, err := u.rewards.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	cache, err := u.cacheFor(ctx, u.rewards, *r)
	if err != nil {
		return nil, err
	}
	v := u.view(*r, cache)
	return &v, nil
}

func validStatus(s string) bool {
	switch s {
	case domainreward.StatusDraft, domainreward.StatusActive, domainreward.StatusInactive, domainreward.StatusArchived:
		return true
	}
	return false
}

func normalize(in Input) Input {
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	in.ImageURL = strings.TrimSpace(in.ImageURL)
	in.Type = strings.TrimSpace(in.Type)
	if in.Type == "" {
		in.Type = domainreward.TypePhysical
	}
	in.SKUCode = strings.TrimSpace(in.SKUCode)
	in.SKUName = strings.TrimSpace(in.SKUName)
	if in.ERPWarehouseID == 0 {
		in.ERPWarehouseID = domainreward.DefaultWarehouse
	}
	if in.QtyPerRedemption == 0 {
		in.QtyPerRedemption = 1
	}
	return in
}

func (u *usecase) validate(ctx context.Context, in Input) error {
	if n := utf8.RuneCountInString(in.Name); n < 1 || n > domainreward.MaxNameRunes {
		return domain.NewValidationError("name", "is required (120 characters max)")
	}
	if utf8.RuneCountInString(in.Description) > domainreward.MaxTextRunes {
		return domain.NewValidationError("description", "must be 2000 characters or fewer")
	}
	if in.ImageURL != "" && !strings.HasPrefix(in.ImageURL, "https://") && !strings.HasPrefix(in.ImageURL, "/") {
		return domain.NewValidationError("imageUrl", "must be an https:// URL or an uploaded file")
	}
	switch in.Type {
	case domainreward.TypePhysical:
		if in.ERPSKUID == nil || *in.ERPSKUID == 0 {
			return domain.NewValidationError("erpSkuId", "choose the ERP SKU of this reward")
		}
	case domainreward.TypeDigital, domainreward.TypePrivilege:
		return domainreward.ErrUnsupportedType
	default:
		return domain.NewValidationError("type", "must be physical, digital or privilege")
	}
	if in.QtyPerRedemption < 1 || in.QtyPerRedemption > 100 {
		return domain.NewValidationError("qtyPerRedemption", "must be between 1 and 100")
	}
	if in.PointsCost <= 0 || in.PointsCost > domainreward.MaxPointsCost {
		return domain.NewValidationError("pointsCost", "must be between 1 and 10,000,000")
	}
	if in.StockLimit != nil && *in.StockLimit < 0 {
		return domain.NewValidationError("stockLimit", "must not be negative")
	}
	if in.LimitPerMember != nil && (*in.LimitPerMember < 1 || *in.LimitPerMember > 1000) {
		return domain.NewValidationError("limitPerMember", "must be between 1 and 1000")
	}
	if in.AvailableFrom != nil && in.AvailableTo != nil && !in.AvailableFrom.Before(*in.AvailableTo) {
		return domain.NewValidationError("availableTo", "must be after availableFrom")
	}
	if in.CategoryID != nil {
		if _, err := u.rewards.FindCategory(ctx, *in.CategoryID); err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return domain.NewValidationError("categoryId", "category does not exist")
			}
			return err
		}
	}
	if in.MinTierID != nil && u.tiers != nil {
		if _, err := u.tiers.FindByID(ctx, *in.MinTierID); err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return domain.NewValidationError("minTierId", "tier does not exist")
			}
			return err
		}
	}
	return nil
}

func apply(r *domainreward.Reward, in Input) {
	r.Name, r.Description, r.ImageURL, r.CategoryID, r.Type = in.Name, in.Description, in.ImageURL, in.CategoryID, in.Type
	r.ERPSKUID, r.SKUCodeSnapshot, r.SKUNameSnapshot, r.ERPWarehouseID = in.ERPSKUID, in.SKUCode, in.SKUName, in.ERPWarehouseID
	r.QtyPerRedemption, r.PointsCost, r.StockLimit, r.LimitPerMember = in.QtyPerRedemption, in.PointsCost, in.StockLimit, in.LimitPerMember
	r.MinTierID, r.AvailableFrom, r.AvailableTo, r.SortOrder = in.MinTierID, in.AvailableFrom, in.AvailableTo, in.SortOrder
}

func sameUint(a, b *uint) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func (u *usecase) Create(ctx context.Context, actor domain.Actor, in Input) (*View, error) {
	in = normalize(in)
	if err := u.validate(ctx, in); err != nil {
		return nil, err
	}
	r := &domainreward.Reward{Status: domainreward.StatusDraft, CreatedBy: actor.ERPUserID, CreatedByEmail: actor.Email}
	apply(r, in)
	r.UpdatedBy, r.UpdatedByEmail = actor.ERPUserID, actor.Email
	if err := u.rewards.Create(ctx, r); err != nil {
		return nil, err
	}
	return u.Get(ctx, r.ID)
}

func (u *usecase) Update(ctx context.Context, actor domain.Actor, id uint, in Input) (*View, error) {
	r, err := u.rewards.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if r.Status == domainreward.StatusArchived {
		return nil, domain.ErrConflict
	}
	in = normalize(in)
	if err := u.validate(ctx, in); err != nil {
		return nil, err
	}
	if r.RedeemedCount > 0 && (!sameUint(in.ERPSKUID, r.ERPSKUID) || in.ERPWarehouseID != r.ERPWarehouseID) {
		return nil, domainreward.ErrSKULocked
	}
	apply(r, in)
	r.UpdatedBy, r.UpdatedByEmail = actor.ERPUserID, actor.Email
	if err := u.rewards.Update(ctx, r); err != nil {
		return nil, err
	}
	return u.Get(ctx, r.ID)
}

// SetStatus moves a reward between draft/active/inactive/archived. Archived is
// final. Activating needs a SKU and a future (or open) window.
func (u *usecase) SetStatus(ctx context.Context, actor domain.Actor, id uint, status string) (*View, error) {
	if !validStatus(status) {
		return nil, domain.NewValidationError("status", "must be one of draft, active, inactive, archived")
	}
	r, err := u.rewards.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if r.Status == domainreward.StatusArchived || (status == domainreward.StatusDraft && r.RedeemedCount > 0) {
		return nil, domain.ErrConflict
	}
	if status == domainreward.StatusActive && r.AvailableTo != nil && !r.AvailableTo.After(u.now()) {
		return nil, domain.NewValidationError("availableTo", "must be in the future to activate")
	}
	r.Status = status
	r.UpdatedBy, r.UpdatedByEmail = actor.ERPUserID, actor.Email
	if err := u.rewards.Update(ctx, r); err != nil {
		return nil, err
	}
	return u.Get(ctx, r.ID)
}

// SyncStock refreshes the cache for every active reward SKU. When the ERP is
// down the old values stay (they are only shown as stale).
func (u *usecase) SyncStock(ctx context.Context) (int, error) {
	if u.erp == nil {
		return 0, nil
	}
	keys, err := u.rewards.ActiveSKUs(ctx)
	if err != nil {
		return 0, err
	}
	byWarehouse := map[uint][]uint{}
	for _, k := range keys {
		byWarehouse[k.WarehouseID] = append(byWarehouse[k.WarehouseID], k.SKUID)
	}
	now := u.now()
	synced := 0
	for wh, ids := range byWarehouse {
		levels, err := u.erp.Stock(ctx, ids, wh)
		if err != nil {
			return synced, fmt.Errorf("reward stock sync warehouse %d: %w", wh, err)
		}
		rows := make([]domainreward.StockCache, 0, len(levels))
		for _, l := range levels {
			rows = append(rows, domainreward.StockCache{ERPSKUID: l.SKUID, ERPWarehouseID: wh, AvailableQty: l.AvailableQty, SyncedAt: now})
		}
		if err := u.rewards.UpsertStock(ctx, rows); err != nil {
			return synced, err
		}
		synced += len(rows)
	}
	return synced, nil
}
