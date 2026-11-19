package coupon

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"crm-core/database"
	"crm-core/domain"
	domaincoupon "crm-core/domain/coupon"
	domainloyalty "crm-core/domain/loyalty"
	domainmember "crm-core/domain/member"
	domainpoints "crm-core/domain/points"
	"crm-core/migrations"
	"crm-core/repository/postgres"
	"crm-core/timeutil"
	usecasepoints "crm-core/usecase/points"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type env struct {
	db     *gorm.DB
	uc     *usecase
	points domainpoints.Repository
	tierID uint
	owner  domain.Actor
	now    time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	// A file DB with several connections: the points usecase reads settings and
	// the daily count outside the claim transaction (as it does on Postgres).
	// _txlock=immediate makes write transactions queue at BEGIN, which stands in
	// for Postgres row locks; WAL lets those outside reads proceed meanwhile.
	dsn := filepath.Join(t.TempDir(), "coupon.db") +
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
	uc := New(postgres.NewCouponRepository(db), postgres.NewCouponUnitOfWork(db), postgres.NewMemberRepository(db),
		postgres.NewTierRepository(db), pointsUC).(*usecase)
	now := time.Date(2026, 11, 18, 3, 0, 0, 0, time.UTC) // 10:00 Bangkok
	uc.now = func() time.Time { return now }
	return &env{db: db, uc: uc, points: pointsRepo, tierID: tier.ID, owner: domain.Actor{ERPUserID: 1, Email: "owner@example.com", Roles: []string{"owner"}}, now: now}
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

func baseInput() Input {
	return Input{
		Name: "ลด 50 บาท", BenefitType: domaincoupon.BenefitAmountOff, BenefitValue: 50,
		PointsCost: 30, ExpiryMode: domaincoupon.ExpiryDaysAfterClaim, ValidDays: 7,
		TotalQuota: 10, LimitPerMember: 1, Channel: domaincoupon.ChannelStore, CodeType: domaincoupon.CodeUnique,
	}
}

func (e *env) activeCoupon(t *testing.T, in Input) domaincoupon.Coupon {
	t.Helper()
	ctx := context.Background()
	c, err := e.uc.Create(ctx, e.owner, in)
	require.NoError(t, err)
	c, err = e.uc.Activate(ctx, e.owner, c.ID)
	require.NoError(t, err)
	return *c
}

func TestExpiresAt(t *testing.T) {
	bkk := timeutil.Bangkok()
	fixed := time.Date(2026, 12, 31, 0, 0, 0, 0, bkk)

	c := domaincoupon.Coupon{ExpiryMode: domaincoupon.ExpiryFixedDate, ExpiresAt: &fixed}
	require.True(t, ExpiresAt(c, time.Now()).Equal(fixed))

	// Late evening and just after midnight Bangkok both count from the Bangkok day.
	c = domaincoupon.Coupon{ExpiryMode: domaincoupon.ExpiryDaysAfterClaim, ValidDays: 1}
	want := time.Date(2026, 11, 20, 0, 0, 0, 0, bkk)
	require.True(t, ExpiresAt(c, time.Date(2026, 11, 18, 23, 30, 0, 0, bkk)).Equal(want))
	require.True(t, ExpiresAt(c, time.Date(2026, 11, 18, 0, 10, 0, 0, bkk)).Equal(want),
		"00:10 Bangkok is still the previous day in UTC")

	// A template date caps days_after_claim.
	capAt := time.Date(2026, 11, 19, 12, 0, 0, 0, bkk)
	c.ExpiresAt = &capAt
	require.True(t, ExpiresAt(c, time.Date(2026, 11, 18, 10, 0, 0, 0, bkk)).Equal(capAt))
}

func TestValidateRejectsBadTemplates(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	future := e.now.AddDate(0, 1, 0)
	cases := map[string]func(*Input){
		"empty name":            func(in *Input) { in.Name = "" },
		"zero amount":           func(in *Input) { in.BenefitValue = 0 },
		"percent without cap":   func(in *Input) { in.BenefitType = domaincoupon.BenefitPercentOff; in.BenefitValue = 10 },
		"percent over 100":      func(in *Input) { in.BenefitType = domaincoupon.BenefitPercentOff; in.BenefitValue = 120 },
		"fixed without date":    func(in *Input) { in.ExpiryMode = domaincoupon.ExpiryFixedDate },
		"zero valid days":       func(in *Input) { in.ValidDays = 0 },
		"marketplace unique":    func(in *Input) { in.Channel = domaincoupon.ChannelShopee },
		"bad shared code":       func(in *Input) { in.CodeType = domaincoupon.CodeShared; in.SharedCode = "a" },
		"negative cost":         func(in *Input) { in.PointsCost = -1 },
		"unknown tier":          func(in *Input) { id := uint(999); in.MinTierID = &id },
		"claim window reversed": func(in *Input) { in.ClaimStartAt = &future; past := e.now; in.ClaimEndAt = &past },
	}
	for name, mutate := range cases {
		in := baseInput()
		mutate(&in)
		_, err := e.uc.Create(ctx, e.owner, in)
		require.ErrorIs(t, err, domain.ErrValidation, name)
	}

	in := baseInput()
	in.Channel, in.CodeType, in.SharedCode = domaincoupon.ChannelShopee, domaincoupon.CodeShared, "chawy-11"
	c, err := e.uc.Create(ctx, e.owner, in)
	require.NoError(t, err)
	require.Equal(t, "CHAWY-11", c.SharedCode)
	require.Equal(t, domaincoupon.StatusDraft, c.Status)
}

func TestClaimSpendsPointsAndEnforcesLimits(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.member(t, 1, 100)

	draft, err := e.uc.Create(ctx, e.owner, baseInput())
	require.NoError(t, err)
	_, err = e.uc.Claim(ctx, ClaimRequest{MemberID: m.ID, CouponID: draft.ID})
	require.ErrorIs(t, err, domaincoupon.ErrNotActive)

	c := e.activeCoupon(t, baseInput())
	mc, err := e.uc.Claim(ctx, ClaimRequest{MemberID: m.ID, CouponID: c.ID})
	require.NoError(t, err)
	require.Len(t, mc.Code, domaincoupon.CodeLength)
	require.EqualValues(t, 30, mc.PointsSpent)
	require.NotNil(t, mc.LedgerID)
	require.NotZero(t, *mc.LedgerID)
	require.True(t, mc.ExpiresAt.Equal(time.Date(2026, 11, 26, 0, 0, 0, 0, timeutil.Bangkok())))
	require.EqualValues(t, 70, e.balance(t, m.ID))

	_, err = e.uc.Claim(ctx, ClaimRequest{MemberID: m.ID, CouponID: c.ID})
	require.ErrorIs(t, err, domaincoupon.ErrLimitReached)

	poor := e.member(t, 2, 10)
	_, err = e.uc.Claim(ctx, ClaimRequest{MemberID: poor.ID, CouponID: c.ID})
	require.ErrorIs(t, err, domain.ErrInsufficientPoints)

	got, err := e.uc.Get(ctx, c.ID)
	require.NoError(t, err)
	require.Equal(t, 1, got.ClaimedCount, "failed claims roll back the count")

	// Claim window closed.
	end := e.now.Add(time.Hour)
	in := baseInput()
	in.ClaimEndAt = &end
	closing := e.activeCoupon(t, in)
	e.uc.now = func() time.Time { return end }
	_, err = e.uc.Claim(ctx, ClaimRequest{MemberID: poor.ID, CouponID: closing.ID})
	require.ErrorIs(t, err, domaincoupon.ErrNotActive)
}

func TestConcurrentClaimsRespectQuotaAndBalance(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	in := baseInput()
	in.TotalQuota = 1
	c := e.activeCoupon(t, in)
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
			_, errs[i] = e.uc.Claim(ctx, ClaimRequest{MemberID: id, CouponID: c.ID})
		}(i, m.ID)
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
			continue
		}
		require.ErrorIs(t, err, domaincoupon.ErrQuotaReached)
	}
	require.Equal(t, 1, ok)

	// One member firing several claims cannot overdraw.
	in = baseInput()
	in.PointsCost, in.LimitPerMember, in.TotalQuota = 20, 10, 0
	many := e.activeCoupon(t, in)
	m := e.member(t, 99, 50)
	errs = make([]error, 5)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = e.uc.Claim(ctx, ClaimRequest{MemberID: m.ID, CouponID: many.ID})
		}(i)
	}
	wg.Wait()
	ok = 0
	for _, err := range errs {
		if err == nil {
			ok++
			continue
		}
		require.ErrorIs(t, err, domain.ErrInsufficientPoints)
	}
	require.Equal(t, 2, ok)
	require.EqualValues(t, 10, e.balance(t, m.ID))
}

func TestUseIsIdempotentPerBill(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.member(t, 1, 100)
	c := e.activeCoupon(t, baseInput())
	mc, err := e.uc.Claim(ctx, ClaimRequest{MemberID: m.ID, CouponID: c.ID})
	require.NoError(t, err)

	staff := domain.Actor{ERPUserID: 2, Email: "sales@example.com", Roles: []string{"sales"}}
	view, err := e.uc.Lookup(ctx, mc.Code, time.Time{})
	require.NoError(t, err)
	require.True(t, view.Usable)

	_, err = e.uc.Use(ctx, staff, mc.Code, "", time.Time{})
	require.ErrorIs(t, err, domain.ErrValidation)

	view, err = e.uc.Use(ctx, staff, " "+mc.Code+" ", "INV-001", time.Time{})
	require.NoError(t, err)
	require.Equal(t, domaincoupon.HolderUsed, view.Holder.Status)
	require.False(t, view.Usable)

	again, err := e.uc.Use(ctx, staff, mc.Code, "INV-001", time.Time{})
	require.NoError(t, err, "same bill again is a no-op")
	require.Equal(t, "INV-001", again.Holder.UsedRef)

	_, err = e.uc.Use(ctx, staff, mc.Code, "INV-002", time.Time{})
	require.ErrorIs(t, err, domaincoupon.ErrAlreadyUsed)

	got, err := e.uc.Get(ctx, c.ID)
	require.NoError(t, err)
	require.Equal(t, 1, got.UsedCount)

	_, err = e.uc.Use(ctx, staff, "NOPE", "INV-1", time.Time{})
	require.ErrorIs(t, err, domain.ErrNotFound)
}

func TestCancelRefundsAndFreesQuota(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.member(t, 1, 100)
	in := baseInput()
	in.TotalQuota = 1
	c := e.activeCoupon(t, in)
	mc, err := e.uc.Claim(ctx, ClaimRequest{MemberID: m.ID, CouponID: c.ID})
	require.NoError(t, err)
	require.EqualValues(t, 70, e.balance(t, m.ID))

	_, err = e.uc.Cancel(ctx, e.owner, mc.ID, "x", time.Time{})
	require.ErrorIs(t, err, domain.ErrValidation, "a reason is required")

	cancelled, err := e.uc.Cancel(ctx, e.owner, mc.ID, "ลูกค้าขอยกเลิก", time.Time{})
	require.NoError(t, err)
	require.Equal(t, domaincoupon.HolderCancelled, cancelled.Status)
	require.EqualValues(t, 100, e.balance(t, m.ID))

	got, err := e.uc.Get(ctx, c.ID)
	require.NoError(t, err)
	require.Equal(t, 0, got.ClaimedCount)

	_, err = e.uc.Cancel(ctx, e.owner, mc.ID, "ซ้ำ", time.Time{})
	require.ErrorIs(t, err, domaincoupon.ErrNotUsable)

	// The freed slot can be claimed again (the cancelled one no longer counts
	// against the per-member limit either).
	again, err := e.uc.Claim(ctx, ClaimRequest{MemberID: m.ID, CouponID: c.ID})
	require.NoError(t, err)
	_, err = e.uc.Use(ctx, e.owner, again.Code, "INV-9", time.Time{})
	require.NoError(t, err)
	_, err = e.uc.Cancel(ctx, e.owner, again.ID, "ใช้ไปแล้ว", time.Time{})
	require.ErrorIs(t, err, domaincoupon.ErrAlreadyUsed)
}

func TestExpireDueKeepsPointsAndIsIdempotent(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.member(t, 1, 100)
	in := baseInput()
	in.ValidDays = 1
	c := e.activeCoupon(t, in)
	mc, err := e.uc.Claim(ctx, ClaimRequest{MemberID: m.ID, CouponID: c.ID})
	require.NoError(t, err)

	later := mc.ExpiresAt.Add(time.Minute)
	n, err := e.uc.ExpireDue(ctx, mc.ExpiresAt.Add(-time.Minute))
	require.NoError(t, err)
	require.Zero(t, n)
	n, err = e.uc.ExpireDue(ctx, later)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	n, err = e.uc.ExpireDue(ctx, later)
	require.NoError(t, err)
	require.Zero(t, n)
	require.EqualValues(t, 70, e.balance(t, m.ID), "expiry does not refund")

	_, err = e.uc.Use(ctx, e.owner, mc.Code, "INV-1", later)
	require.ErrorIs(t, err, domaincoupon.ErrExpired)
}

func TestUseRefusesExpiredBeforeTheJobRuns(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.member(t, 1, 100)
	c := e.activeCoupon(t, baseInput())
	mc, err := e.uc.Claim(ctx, ClaimRequest{MemberID: m.ID, CouponID: c.ID})
	require.NoError(t, err)
	view, err := e.uc.Lookup(ctx, mc.Code, mc.ExpiresAt)
	require.NoError(t, err)
	require.False(t, view.Usable)
	require.Equal(t, domaincoupon.HolderExpired, view.Reason)
	_, err = e.uc.Use(ctx, e.owner, mc.Code, "INV-1", mc.ExpiresAt)
	require.ErrorIs(t, err, domaincoupon.ErrExpired)
}

func TestGrantOnlyForFreeCoupons(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.member(t, 1, 0)
	paid := e.activeCoupon(t, baseInput())
	_, err := e.uc.Claim(ctx, ClaimRequest{MemberID: m.ID, CouponID: paid.ID, Grant: true, Actor: e.owner})
	require.ErrorIs(t, err, domain.ErrValidation)

	in := baseInput()
	in.PointsCost = 0
	free := e.activeCoupon(t, in)
	mc, err := e.uc.Claim(ctx, ClaimRequest{MemberID: m.ID, CouponID: free.ID, Grant: true, Actor: e.owner})
	require.NoError(t, err)
	require.Nil(t, mc.LedgerID)
	require.EqualValues(t, 1, mc.GrantedBy)

	var ledger int64
	require.NoError(t, e.db.Model(&domainpoints.PointLedger{}).Where("member_id = ?", m.ID).Count(&ledger).Error)
	require.Zero(t, ledger)
}

func TestTemplateLifecycleAfterClaims(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.member(t, 1, 100)
	c := e.activeCoupon(t, baseInput())
	_, err := e.uc.Claim(ctx, ClaimRequest{MemberID: m.ID, CouponID: c.ID})
	require.NoError(t, err)

	in := baseInput()
	in.BenefitValue = 80
	_, err = e.uc.Update(ctx, e.owner, c.ID, in)
	require.ErrorIs(t, err, domain.ErrValidation, "benefit is frozen once claimed")

	in = baseInput()
	in.PointsCost, in.ValidDays = 40, 14
	updated, err := e.uc.Update(ctx, e.owner, c.ID, in)
	require.NoError(t, err, "cost and expiry stay editable")
	require.EqualValues(t, 40, updated.PointsCost)

	require.ErrorIs(t, e.uc.Delete(ctx, c.ID), domain.ErrConflict)

	_, err = e.uc.Pause(ctx, e.owner, c.ID)
	require.NoError(t, err)
	_, err = e.uc.Claim(ctx, ClaimRequest{MemberID: m.ID, CouponID: c.ID})
	require.ErrorIs(t, err, domaincoupon.ErrNotActive)
	_, err = e.uc.End(ctx, e.owner, c.ID)
	require.NoError(t, err)
	_, err = e.uc.Activate(ctx, e.owner, c.ID)
	require.ErrorIs(t, err, domain.ErrConflict)
	_, err = e.uc.Update(ctx, e.owner, c.ID, in)
	require.ErrorIs(t, err, domain.ErrConflict)

	draft, err := e.uc.Create(ctx, e.owner, baseInput())
	require.NoError(t, err)
	require.NoError(t, e.uc.Delete(ctx, draft.ID))
}

func TestHoldersJoinMembers(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.member(t, 7, 100)
	c := e.activeCoupon(t, baseInput())
	_, err := e.uc.Claim(ctx, ClaimRequest{MemberID: m.ID, CouponID: c.ID})
	require.NoError(t, err)

	rows, total, err := e.uc.Holders(ctx, domaincoupon.HolderQuery{CouponID: c.ID})
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Equal(t, m.MemberCode, rows[0].MemberCode)
	require.Equal(t, c.Name, rows[0].CouponName)

	rows, total, err = e.uc.Holders(ctx, domaincoupon.HolderQuery{MemberID: m.ID, Status: domaincoupon.HolderUsed})
	require.NoError(t, err)
	require.Zero(t, total)
	require.Empty(t, rows)

	_, _, err = e.uc.Holders(ctx, domaincoupon.HolderQuery{Status: "nope"})
	require.ErrorIs(t, err, domain.ErrValidation)
}

func TestListFiltersByMonth(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	bkk := timeutil.Bangkok()
	start := time.Date(2026, 10, 31, 23, 30, 0, 0, bkk)
	end := time.Date(2026, 11, 1, 0, 0, 0, 0, bkk)
	in := baseInput()
	in.ClaimStartAt, in.ClaimEndAt = &start, &end
	_, err := e.uc.Create(ctx, e.owner, in)
	require.NoError(t, err)
	_, err = e.uc.Create(ctx, e.owner, baseInput()) // no window = always

	oct, total, err := e.uc.List(ctx, domaincoupon.Query{}, "2026-10")
	require.NoError(t, err)
	require.EqualValues(t, 2, total)
	require.Len(t, oct, 2)
	_, total, err = e.uc.List(ctx, domaincoupon.Query{}, "2026-11")
	require.NoError(t, err)
	require.EqualValues(t, 1, total, "a window ending exactly at 1 Nov 00:00 is October only")

	_, _, err = e.uc.List(ctx, domaincoupon.Query{}, "2026-13")
	require.ErrorIs(t, err, domain.ErrValidation)
	_, _, err = e.uc.List(ctx, domaincoupon.Query{Status: "x"}, "")
	require.True(t, errors.Is(err, domain.ErrValidation))
}
