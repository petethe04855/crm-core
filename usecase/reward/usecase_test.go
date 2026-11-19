package reward

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"crm-core/database"
	"crm-core/domain"
	domainloyalty "crm-core/domain/loyalty"
	domainmember "crm-core/domain/member"
	domainpoints "crm-core/domain/points"
	domainreward "crm-core/domain/reward"
	"crm-core/migrations"
	"crm-core/repository/postgres"
	usecasepoints "crm-core/usecase/points"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// fakeERP plays erp-api-v2: stock levels plus an idempotent goods issue.
type fakeERP struct {
	mu        sync.Mutex
	stock     map[uint]int
	issued    map[string]string
	deducted  int
	stockErr  error
	shortNext bool
	downNext  bool
}

func (f *fakeERP) Stock(_ context.Context, ids []uint, _ uint) ([]domainreward.StockLevel, error) {
	if f.stockErr != nil {
		return nil, f.stockErr
	}
	out := make([]domainreward.StockLevel, 0, len(ids))
	for _, id := range ids {
		out = append(out, domainreward.StockLevel{SKUID: id, WarehouseID: 1, AvailableQty: f.stock[id]})
	}
	return out, nil
}

func (f *fakeERP) IssueGoods(_ context.Context, gi domainreward.GoodsIssue) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.downNext {
		f.downNext = false
		return "", domainreward.ErrERPUnavailable
	}
	if ref, ok := f.issued[gi.IdempotencyKey]; ok {
		return ref, nil
	}
	if f.shortNext || f.stock[gi.SKUID] < gi.Qty {
		f.shortNext = false
		return "", domainreward.ErrInsufficientERP
	}
	f.stock[gi.SKUID] -= gi.Qty
	f.deducted += gi.Qty
	ref := fmt.Sprintf("CRMGI-%06d", len(f.issued)+1)
	f.issued[gi.IdempotencyKey] = ref
	return ref, nil
}

type env struct {
	db     *gorm.DB
	uc     *usecase
	erp    *fakeERP
	points domainpoints.Repository
	tierID uint
	owner  domain.Actor
	now    time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	// File DB + WAL + immediate transactions: see usecase/coupon tests.
	dsn := filepath.Join(t.TempDir(), "reward.db") +
		"?_txlock=immediate&_pragma=journal_mode(WAL)&_pragma=busy_timeout(20000)"
	db, err := gorm.Open(sqlite.Open(dsn), database.GormConfig("", logger.Silent))
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, migrations.Run(db, ""))

	var tier domainloyalty.Tier
	require.NoError(t, db.Where("code = ?", domainloyalty.DefaultTierCode).First(&tier).Error)

	pointsRepo := postgres.NewPointsRepository(db)
	pointsUC := usecasepoints.New(pointsRepo, postgres.NewSettingsRepository(db),
		usecasepoints.WithMembers(postgres.NewMemberRepository(db)))
	erp := &fakeERP{stock: map[uint]int{101: 5, 102: 0}, issued: map[string]string{}}
	uc := New(postgres.NewRewardRepository(db), postgres.NewRewardUnitOfWork(db), postgres.NewMemberRepository(db),
		postgres.NewTierRepository(db), pointsUC, erp).(*usecase)
	now := time.Date(2026, 11, 19, 3, 0, 0, 0, time.UTC)
	uc.now = func() time.Time { return now }
	return &env{db: db, uc: uc, erp: erp, points: pointsRepo, tierID: tier.ID, now: now,
		owner: domain.Actor{ERPUserID: 1, Email: "owner@example.com", Roles: []string{"owner"}}}
}

func (e *env) member(t *testing.T, n int, points int64) domainmember.Member {
	t.Helper()
	m := domainmember.Member{
		MemberCode: fmt.Sprintf("M2611-%06d", n), FirstName: "Test", LastName: fmt.Sprint(n),
		Email: fmt.Sprintf("m%d@example.com", n), TierID: e.tierID, Gender: "unspecified",
		Status: domainmember.StatusActive, RegisteredChannel: "line", CreatedAt: e.now.AddDate(0, -1, 0),
	}
	require.NoError(t, e.db.Create(&m).Error)
	if points > 0 {
		_, err := e.points.Earn(context.Background(), domainpoints.EarnCommand{
			MemberID: m.ID, Points: points, SourceType: domainpoints.SourceOrder, SourceID: fmt.Sprintf("seed-%d", n),
		})
		require.NoError(t, err)
	}
	return m
}

func (e *env) balance(t *testing.T, id uint) int64 {
	t.Helper()
	var m domainmember.Member
	require.NoError(t, e.db.First(&m, id).Error)
	return m.PointBalance
}

func sku(id uint) *uint { return &id }

func baseInput() Input {
	return Input{Name: "ขนมไก่ 1 ถุง", ERPSKUID: sku(101), SKUCode: "CHK-01", SKUName: "ขนมไก่", PointsCost: 40}
}

// activeReward creates, activates and syncs the stock cache of a reward.
func (e *env) activeReward(t *testing.T, in Input) domainreward.Reward {
	t.Helper()
	ctx := context.Background()
	v, err := e.uc.Create(ctx, e.owner, in)
	require.NoError(t, err)
	v, err = e.uc.SetStatus(ctx, e.owner, v.Reward.ID, domainreward.StatusActive)
	require.NoError(t, err)
	_, err = e.uc.SyncStock(ctx)
	require.NoError(t, err)
	return v.Reward
}

var addr = Address{Name: "สมชาย", Phone: "0812345678", Address: "1 ถนนสุขุมวิท", Province: "กรุงเทพมหานคร", Postcode: "10110"}

func TestRemainingQuota(t *testing.T) {
	limit := func(n int) *int { return &n }
	cache := func(n int) *domainreward.StockCache { return &domainreward.StockCache{AvailableQty: n} }
	r := domainreward.Reward{Type: domainreward.TypePhysical, QtyPerRedemption: 2}

	require.Equal(t, 0, RemainingQuota(r, nil), "never synced = no stock")
	require.Equal(t, 3, RemainingQuota(r, cache(7)), "floor(7 / 2)")
	r.StockLimit, r.RedeemedCount = limit(10), 8
	require.Equal(t, 2, RemainingQuota(r, cache(7)), "the CRM cap is smaller")
	r.RedeemedCount = 12
	require.Equal(t, 0, RemainingQuota(r, cache(7)), "never negative")
	r.StockLimit, r.RedeemedCount = limit(100), 0
	require.Equal(t, 0, RemainingQuota(r, cache(-4)), "negative ERP stock counts as none")
}

func TestCatalogueRules(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	for name, mutate := range map[string]func(*Input){
		"no name":    func(in *Input) { in.Name = "" },
		"no sku":     func(in *Input) { in.ERPSKUID = nil },
		"zero cost":  func(in *Input) { in.PointsCost = 0 },
		"bad window": func(in *Input) { a, b := e.now, e.now.Add(-time.Hour); in.AvailableFrom, in.AvailableTo = &a, &b },
		"bad cat":    func(in *Input) { id := uint(99); in.CategoryID = &id },
	} {
		in := baseInput()
		mutate(&in)
		_, err := e.uc.Create(ctx, e.owner, in)
		require.ErrorIs(t, err, domain.ErrValidation, name)
	}
	in := baseInput()
	in.Type = domainreward.TypeDigital
	_, err := e.uc.Create(ctx, e.owner, in)
	require.ErrorIs(t, err, domainreward.ErrUnsupportedType)

	cat, err := e.uc.CreateCategory(ctx, "ขนม", 1)
	require.NoError(t, err)
	_, err = e.uc.CreateCategory(ctx, "ขนม", 2)
	require.ErrorIs(t, err, domain.ErrConflict, "category names are unique")

	in = baseInput()
	in.CategoryID = &cat.ID
	r := e.activeReward(t, in)
	require.ErrorIs(t, e.uc.DeleteCategory(ctx, cat.ID), domainreward.ErrCategoryInUse)

	v, err := e.uc.Get(ctx, r.ID)
	require.NoError(t, err)
	require.NotNil(t, v.ERPAvailableQty)
	require.Equal(t, 5, *v.ERPAvailableQty)
	require.Equal(t, 5, v.RemainingQuota)
	require.False(t, v.StockStale)

	// The SKU locks once anyone redeemed.
	m := e.member(t, 1, 100)
	_, err = e.uc.Redeem(ctx, RedeemRequest{MemberID: m.ID, RewardID: r.ID, Address: addr})
	require.NoError(t, err)
	changed := baseInput()
	changed.ERPSKUID = sku(102)
	_, err = e.uc.Update(ctx, e.owner, r.ID, changed)
	require.ErrorIs(t, err, domainreward.ErrSKULocked)
	price := baseInput()
	price.CategoryID, price.PointsCost = &cat.ID, 60
	v, err = e.uc.Update(ctx, e.owner, r.ID, price)
	require.NoError(t, err, "price stays editable")
	require.EqualValues(t, 60, v.Reward.PointsCost)
}

func TestStockSyncKeepsOldValuesWhenERPIsDown(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	r := e.activeReward(t, baseInput())

	e.erp.stockErr = errors.New("connection refused")
	e.uc.now = func() time.Time { return e.now.Add(2 * time.Hour) }
	_, err := e.uc.SyncStock(ctx)
	require.Error(t, err)

	v, err := e.uc.Get(ctx, r.ID)
	require.NoError(t, err)
	require.Equal(t, 5, *v.ERPAvailableQty, "last known value is kept")
	require.True(t, v.StockStale, "but flagged as stale")

	rows, total, err := e.uc.List(ctx, domainreward.Query{LowStock: true})
	require.NoError(t, err)
	require.EqualValues(t, 1, total, "5 is at or below the low-stock mark")
	require.Equal(t, r.ID, rows[0].Reward.ID)
}

func TestRedeemChecksAndSpendsPoints(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.member(t, 1, 100)

	draft, err := e.uc.Create(ctx, e.owner, baseInput())
	require.NoError(t, err)
	_, err = e.uc.Redeem(ctx, RedeemRequest{MemberID: m.ID, RewardID: draft.Reward.ID, Address: addr})
	require.ErrorIs(t, err, domainreward.ErrNotAvailable)

	in := baseInput()
	one := 1
	in.LimitPerMember = &one
	r := e.activeReward(t, in)
	_, err = e.uc.Redeem(ctx, RedeemRequest{MemberID: m.ID, RewardID: r.ID})
	require.ErrorIs(t, err, domain.ErrValidation, "an address is required")

	rd, err := e.uc.Redeem(ctx, RedeemRequest{MemberID: m.ID, RewardID: r.ID, Address: addr})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(rd.RedemptionNo, "RD2611-"), rd.RedemptionNo)
	require.Equal(t, domainreward.RedemptionPending, rd.Status)
	require.NotNil(t, rd.LedgerID)
	require.EqualValues(t, 60, e.balance(t, m.ID))
	require.Equal(t, "กรุงเทพมหานคร", rd.ShipProvince)

	_, err = e.uc.Redeem(ctx, RedeemRequest{MemberID: m.ID, RewardID: r.ID, Address: addr})
	require.ErrorIs(t, err, domainreward.ErrLimitReached)

	poor := e.member(t, 2, 10)
	_, err = e.uc.Redeem(ctx, RedeemRequest{MemberID: poor.ID, RewardID: r.ID, Address: addr})
	require.ErrorIs(t, err, domain.ErrInsufficientPoints)

	none := baseInput()
	none.ERPSKUID = sku(102) // ERP has 0
	empty := e.activeReward(t, none)
	_, err = e.uc.Redeem(ctx, RedeemRequest{MemberID: m.ID, RewardID: empty.ID, Address: addr})
	require.ErrorIs(t, err, domainreward.ErrOutOfStock)
}

func TestConcurrentRedeemsRespectStock(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	in := baseInput()
	limit := 1
	in.StockLimit = &limit
	r := e.activeReward(t, in)

	var members []domainmember.Member
	for i := 0; i < 6; i++ {
		members = append(members, e.member(t, 10+i, 100))
	}
	var wg sync.WaitGroup
	errs := make([]error, len(members))
	for i, m := range members {
		wg.Add(1)
		go func(i int, id uint) {
			defer wg.Done()
			_, errs[i] = e.uc.Redeem(ctx, RedeemRequest{MemberID: id, RewardID: r.ID, Address: addr})
		}(i, m.ID)
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
			continue
		}
		require.ErrorIs(t, err, domainreward.ErrOutOfStock)
	}
	require.Equal(t, 1, ok)
	var spent int64
	for _, m := range members {
		spent += 100 - e.balance(t, m.ID)
	}
	require.EqualValues(t, 40, spent, "only the winner paid")
}

func TestShippingFlow(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.member(t, 1, 100)
	r := e.activeReward(t, baseInput())
	rd, err := e.uc.Redeem(ctx, RedeemRequest{MemberID: m.ID, RewardID: r.ID, Address: addr})
	require.NoError(t, err)
	wh := domain.Actor{ERPUserID: 3, Email: "wh@example.com", Roles: []string{"warehouse"}}

	_, err = e.uc.Ship(ctx, wh, rd.ID, "TH123")
	require.ErrorIs(t, err, domainreward.ErrInvalidStatus, "cannot ship before packing")
	_, err = e.uc.Pack(ctx, wh, rd.ID)
	require.ErrorIs(t, err, domainreward.ErrInvalidStatus, "cannot pack before approval")

	_, err = e.uc.Approve(ctx, e.owner, rd.ID)
	require.NoError(t, err)
	_, err = e.uc.Pack(ctx, wh, rd.ID)
	require.NoError(t, err)
	_, err = e.uc.Ship(ctx, wh, rd.ID, "")
	require.ErrorIs(t, err, domain.ErrValidation)

	// ERP short: stays in packing.
	e.erp.shortNext = true
	_, err = e.uc.Ship(ctx, wh, rd.ID, "TH123")
	require.ErrorIs(t, err, domainreward.ErrInsufficientERP)
	got, err := e.uc.Redemption(ctx, rd.ID)
	require.NoError(t, err)
	require.Equal(t, domainreward.RedemptionPacking, got.Status)

	// ERP down: still packing, retry later is safe.
	e.erp.downNext = true
	_, err = e.uc.Ship(ctx, wh, rd.ID, "TH123")
	require.ErrorIs(t, err, domainreward.ErrERPUnavailable)

	shipped, err := e.uc.Ship(ctx, wh, rd.ID, "TH123")
	require.NoError(t, err)
	require.Equal(t, domainreward.RedemptionShipped, shipped.Status)
	require.Equal(t, "CRMGI-000001", shipped.StockIssueRef)
	require.Equal(t, 1, e.erp.deducted)

	// The ERP replays the same key without a second deduction.
	ref, err := e.erp.IssueGoods(ctx, domainreward.GoodsIssue{IdempotencyKey: rd.RedemptionNo, SKUID: 101, Qty: 1})
	require.NoError(t, err)
	require.Equal(t, "CRMGI-000001", ref)
	require.Equal(t, 1, e.erp.deducted)

	_, err = e.uc.Cancel(ctx, e.owner, rd.ID, "ลูกค้าเปลี่ยนใจ")
	require.ErrorIs(t, err, domainreward.ErrInvalidStatus, "shipped cannot be cancelled")
	delivered, err := e.uc.Deliver(ctx, wh, rd.ID)
	require.NoError(t, err)
	require.NotNil(t, delivered.DeliveredAt)
}

func TestCancelRefundsAndFreesQuota(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.member(t, 1, 100)
	in := baseInput()
	limit := 1
	in.StockLimit = &limit
	r := e.activeReward(t, in)
	rd, err := e.uc.Redeem(ctx, RedeemRequest{MemberID: m.ID, RewardID: r.ID, Address: addr})
	require.NoError(t, err)
	_, err = e.uc.Approve(ctx, e.owner, rd.ID)
	require.NoError(t, err)

	_, err = e.uc.Cancel(ctx, e.owner, rd.ID, "x")
	require.ErrorIs(t, err, domain.ErrValidation)
	cancelled, err := e.uc.Cancel(ctx, e.owner, rd.ID, "สินค้าหมด")
	require.NoError(t, err)
	require.Equal(t, domainreward.RedemptionCancelled, cancelled.Status)
	require.EqualValues(t, 100, e.balance(t, m.ID))

	v, err := e.uc.Get(ctx, r.ID)
	require.NoError(t, err)
	require.Equal(t, 0, v.Reward.RedeemedCount)
	require.Equal(t, 1, v.RemainingQuota, "the slot is free again")

	_, err = e.uc.Cancel(ctx, e.owner, rd.ID, "ซ้ำ")
	require.ErrorIs(t, err, domainreward.ErrInvalidStatus)

	rows, total, err := e.uc.Redemptions(ctx, domainreward.RedemptionQuery{Status: domainreward.RedemptionCancelled, Search: "m2611-000001"})
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Equal(t, "M2611-000001", rows[0].MemberCode)
}
