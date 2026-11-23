package productcode

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"crm-core/domain"
	domainearnrule "crm-core/domain/earnrule"
	domainloyalty "crm-core/domain/loyalty"
	domainmember "crm-core/domain/member"
	domainpoints "crm-core/domain/points"
	domainproductcode "crm-core/domain/productcode"
	domainregistration "crm-core/domain/registration"
	"crm-core/repository/postgres"
	usecasepoints "crm-core/usecase/points"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type redeemEnv struct {
	batches Usecase
	r       Redeemer
	db      *gorm.DB
	clock   *time.Time
}

func newRedeemEnv(t *testing.T) redeemEnv {
	t.Helper()
	batches, db := setup(t)
	clock := time.Now()
	pts := usecasepoints.New(postgres.NewPointsRepository(db), postgres.NewSettingsRepository(db),
		usecasepoints.WithEarnRules(postgres.NewEarnRuleRepository(db)))
	r := NewRedeemer(RedeemerDeps{
		UnitOfWork: postgres.NewProductCodeRedeemUnitOfWork(db),
		Attempts:   postgres.NewProductCodeAttemptRepository(db),
		Members:    postgres.NewMemberIdentityRepository(db),
		Tiers:      postgres.NewTierRepository(db),
		Rules:      postgres.NewEarnRuleRepository(db),
		Settings:   postgres.NewSettingsRepository(db),
		Points:     pts,
		Secret:     secret,
		Now:        func() time.Time { return clock },
	})
	e := redeemEnv{batches: batches, r: r, db: db, clock: &clock}
	return e
}

func (e redeemEnv) member(t *testing.T, code string) domainmember.Member {
	t.Helper()
	var tier domainloyalty.Tier
	require.NoError(t, e.db.Where("code = ?", domainloyalty.DefaultTierCode).First(&tier).Error)
	m := domainmember.Member{MemberCode: code, FirstName: "a", LastName: "b", Email: code + "@x.com", TierID: tier.ID,
		Status: domainmember.StatusActive, Gender: domainmember.GenderUnspecified}
	require.NoError(t, e.db.Create(&m).Error)
	return m
}

// activeCodes creates, exports and activates a batch and returns its codes.
func (e redeemEnv) activeCodes(t *testing.T, qty int, mod func(*BatchInput)) []string {
	t.Helper()
	in := input(qty)
	if mod != nil {
		mod(&in)
	}
	b, err := e.batches.Create(context.Background(), owner, in)
	require.NoError(t, err)
	exp, err := e.batches.Export(context.Background(), owner, b.ID)
	require.NoError(t, err)
	_, err = e.batches.Activate(context.Background(), owner, b.ID)
	require.NoError(t, err)
	return exp.Codes
}

func (e redeemEnv) balance(t *testing.T, id uint) (int64, float64) {
	t.Helper()
	var m domainmember.Member
	require.NoError(t, e.db.First(&m, id).Error)
	return m.PointBalance, m.LifetimeSpend
}

// wrongCode is a well-formed code (valid checksum) that was never issued.
func wrongCode(i int) string {
	random := fmt.Sprintf("ZZZZZZZ%04d", i)
	return random + string(domainproductcode.Checksum(random))
}

func TestRedeemCreditsPointsSpendAndMarksUsed(t *testing.T) {
	e := newRedeemEnv(t)
	m := e.member(t, "M1")
	codes := e.activeCodes(t, 3, nil)

	res, err := e.r.Redeem(context.Background(), m.ID, []string{codes[0], codes[1]}, "1.1.1.1")
	require.NoError(t, err)
	require.Len(t, res.Items, 2)
	for _, it := range res.Items {
		require.Equal(t, domainproductcode.ItemSuccess, it.Status)
		require.EqualValues(t, 10, it.Points)
		require.Equal(t, "ขนมไก่", it.SKUName)
		require.Contains(t, it.Masked, "••••-••••-")
	}
	require.EqualValues(t, 20, res.TotalPoints)
	require.Equal(t, 2, res.Quota.Used)
	require.Equal(t, 18, res.Quota.Remaining)

	points, spend := e.balance(t, m.ID)
	require.EqualValues(t, 20, points)
	require.InDelta(t, 118, spend, 0.001, "59 baht per code into lifetime spend")

	res, err = e.r.Redeem(context.Background(), m.ID, []string{codes[0]}, "")
	require.NoError(t, err)
	require.Equal(t, domainproductcode.ItemUsed, res.Items[0].Status)

	hist, total, err := e.r.History(context.Background(), m.ID, 1, 20)
	require.NoError(t, err)
	require.EqualValues(t, 2, total)
	require.EqualValues(t, 10, hist[0].Points)
	require.Equal(t, "ขนมไก่", hist[0].SKUName)
}

func TestWrongCodeDoesNotRollBackRightOne(t *testing.T) {
	e := newRedeemEnv(t)
	m := e.member(t, "M1")
	codes := e.activeCodes(t, 1, nil)
	res, err := e.r.Redeem(context.Background(), m.ID, []string{wrongCode(1), codes[0], "not-a-code", codes[0]}, "")
	require.NoError(t, err)
	require.Equal(t, domainproductcode.ItemNotFound, res.Items[0].Status)
	require.Equal(t, domainproductcode.ItemSuccess, res.Items[1].Status)
	require.Equal(t, domainproductcode.ItemInvalidFormat, res.Items[2].Status)
	require.Equal(t, domainproductcode.ItemDuplicate, res.Items[3].Status)
	points, _ := e.balance(t, m.ID)
	require.EqualValues(t, 10, points)
}

func TestSameCodeConcurrentlyOnlyOneWins(t *testing.T) {
	e := newRedeemEnv(t)
	codes := e.activeCodes(t, 1, nil)
	members := []domainmember.Member{e.member(t, "M1"), e.member(t, "M2"), e.member(t, "M3")}
	var wg sync.WaitGroup
	statuses := make([]string, len(members))
	for i, m := range members {
		wg.Add(1)
		go func(i int, id uint) {
			defer wg.Done()
			res, err := e.r.Redeem(context.Background(), id, []string{codes[0]}, "")
			require.NoError(t, err)
			statuses[i] = res.Items[0].Status
		}(i, m.ID)
	}
	wg.Wait()
	wins := 0
	for _, s := range statuses {
		if s == domainproductcode.ItemSuccess {
			wins++
		} else {
			require.Equal(t, domainproductcode.ItemUsed, s)
		}
	}
	require.Equal(t, 1, wins)
	var ledgers int64
	require.NoError(t, e.db.Model(&domainpoints.PointLedger{}).Where("source_type = ?", domainpoints.SourceProductCode).Count(&ledgers).Error)
	require.EqualValues(t, 1, ledgers)
}

func TestLockAfterWrongCodes(t *testing.T) {
	e := newRedeemEnv(t)
	m := e.member(t, "M1")
	codes := e.activeCodes(t, 1, nil)

	wrong := []string{wrongCode(1), wrongCode(2), wrongCode(3), wrongCode(4), wrongCode(5), codes[0]}
	res, err := e.r.Redeem(context.Background(), m.ID, wrong, "")
	require.NoError(t, err)
	require.Equal(t, domainproductcode.ItemNotFound, res.Items[4].Status)
	require.Equal(t, domainproductcode.ItemLocked, res.Items[5].Status, "the 5th miss locks the rest of the request")
	require.NotNil(t, res.Quota.LockedUntil)

	_, err = e.r.Redeem(context.Background(), m.ID, []string{codes[0]}, "")
	var le *domainproductcode.LockedError
	require.ErrorAs(t, err, &le)
	require.WithinDuration(t, e.clock.Add(time.Hour), le.Until, time.Second)

	*e.clock = e.clock.Add(61 * time.Minute)
	res, err = e.r.Redeem(context.Background(), m.ID, []string{codes[0]}, "")
	require.NoError(t, err, "the 1-hour lock expires")
	require.Equal(t, domainproductcode.ItemSuccess, res.Items[0].Status)

	// 20 misses within 24 hours lock for a day and flag the member.
	for i := 0; i < 3; i++ {
		*e.clock = e.clock.Add(61 * time.Minute)
		batch := []string{}
		for j := 0; j < 5; j++ {
			batch = append(batch, wrongCode(100+i*10+j))
		}
		_, err = e.r.Redeem(context.Background(), m.ID, batch, "")
		require.NoError(t, err)
	}
	*e.clock = e.clock.Add(61 * time.Minute)
	q, err := e.r.Quota(context.Background(), m.ID)
	require.NoError(t, err)
	require.NotNil(t, q.LockedUntil, "20 misses in 24h")
	require.True(t, q.LockedUntil.After(e.clock.Add(18*time.Hour)), "24h from the first of the 20 misses")
	flagged, err := e.r.Flagged(context.Background())
	require.NoError(t, err)
	require.Len(t, flagged, 1)
	require.Equal(t, "M1", flagged[0].MemberCode)
	require.EqualValues(t, 20, flagged[0].Wrong)
}

func TestDailyQuota(t *testing.T) {
	e := newRedeemEnv(t)
	m := e.member(t, "M1")
	ctx := context.Background()
	_, err := e.r.PutPolicy(ctx, owner, domainproductcode.Policy{DailyQuota: 2, WrongPerHour: 5, WrongPerDay: 20})
	require.NoError(t, err)
	_, err = e.r.PutPolicy(ctx, domain.Actor{Roles: []string{"admin"}}, domainproductcode.Policy{DailyQuota: 9, WrongPerHour: 5, WrongPerDay: 20})
	require.ErrorIs(t, err, domain.ErrForbidden, "owners set the limits")

	codes := e.activeCodes(t, 3, nil)
	res, err := e.r.Redeem(ctx, m.ID, codes, "")
	require.NoError(t, err)
	require.Equal(t, domainproductcode.ItemSuccess, res.Items[1].Status)
	require.Equal(t, domainproductcode.ItemQuotaExceeded, res.Items[2].Status)
	_, err = e.r.Redeem(ctx, m.ID, codes[2:], "")
	require.ErrorIs(t, err, domainproductcode.ErrQuotaReached)
}

func TestTestBatchOnlyForTesters(t *testing.T) {
	e := newRedeemEnv(t)
	ctx := context.Background()
	normal := e.member(t, "M1")
	tester := e.member(t, "M2")
	require.NoError(t, e.r.AddTestMember(ctx, owner, tester.ID))
	codes := e.activeCodes(t, 2, func(in *BatchInput) { in.IsTest = true; in.LotNumber = "" })

	res, err := e.r.Redeem(ctx, normal.ID, []string{codes[0]}, "")
	require.NoError(t, err)
	require.Equal(t, domainproductcode.ItemNotFound, res.Items[0].Status, "test codes look unknown to members")
	res, err = e.r.Redeem(ctx, tester.ID, []string{codes[0]}, "")
	require.NoError(t, err)
	require.Equal(t, domainproductcode.ItemSuccess, res.Items[0].Status)

	list, err := e.r.TestMembers(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, "M2", list[0].MemberCode)
	require.NoError(t, e.r.RemoveTestMember(ctx, tester.ID))
	require.ErrorIs(t, e.r.RemoveTestMember(ctx, tester.ID), domain.ErrNotFound)
}

func TestExpiredVoidAndInactiveBatches(t *testing.T) {
	e := newRedeemEnv(t)
	ctx := context.Background()
	m := e.member(t, "M1")
	codes := e.activeCodes(t, 2, nil)

	var batch domainproductcode.Batch
	require.NoError(t, e.db.Order("id DESC").First(&batch).Error)
	_, err := e.batches.Void(ctx, owner, batch.ID, "recall")
	require.NoError(t, err)
	res, err := e.r.Redeem(ctx, m.ID, []string{codes[0]}, "")
	require.NoError(t, err)
	require.Equal(t, domainproductcode.ItemVoid, res.Items[0].Status)

	soon := time.Now().Add(time.Hour)
	expiring := e.activeCodes(t, 1, func(in *BatchInput) { in.CodeExpiresAt = &soon })
	*e.clock = e.clock.Add(2 * time.Hour)
	res, err = e.r.Redeem(ctx, m.ID, expiring, "")
	require.NoError(t, err)
	require.Equal(t, domainproductcode.ItemExpired, res.Items[0].Status)

	// Exported but not activated: unknown.
	b, err := e.batches.Create(ctx, owner, input(1))
	require.NoError(t, err)
	exp, err := e.batches.Export(ctx, owner, b.ID)
	require.NoError(t, err)
	res, err = e.r.Redeem(ctx, m.ID, exp.Codes, "")
	require.NoError(t, err)
	require.Equal(t, domainproductcode.ItemNotFound, res.Items[0].Status)
}

func TestTierMultiplierUpgradeAndFirstCodeBonus(t *testing.T) {
	e := newRedeemEnv(t)
	ctx := context.Background()
	m := e.member(t, "M1")
	silver := domainloyalty.Tier{Code: "silver", Name: "Silver", MinSpend: 100, EarnMultiplier: 1.5, SortOrder: 1, IsActive: true}
	require.NoError(t, e.db.Create(&silver).Error)
	require.NoError(t, e.db.Create(&domainearnrule.EarnRule{Type: domainearnrule.TypeTierMultiplier, TierID: &silver.ID, Multiplier: 1.5, IsActive: true}).Error)
	require.NoError(t, e.db.Create(&domainearnrule.EarnRule{Type: domainearnrule.TypeBonusFixed, Trigger: domainearnrule.TriggerFirstCode, Points: 25, Multiplier: 1, IsActive: true}).Error)
	codes := e.activeCodes(t, 3, nil)

	res, err := e.r.Redeem(ctx, m.ID, []string{codes[0]}, "")
	require.NoError(t, err)
	require.EqualValues(t, 35, res.Items[0].Points, "10 + first-code bonus 25")
	require.Nil(t, res.TierUpgrade)

	res, err = e.r.Redeem(ctx, m.ID, []string{codes[1]}, "")
	require.NoError(t, err)
	require.EqualValues(t, 10, res.Items[0].Points, "bonus only once; still bronze when earning")
	require.NotNil(t, res.TierUpgrade, "118 baht reaches silver (100)")
	require.Equal(t, "silver", res.TierUpgrade.Code)

	res, err = e.r.Redeem(ctx, m.ID, []string{codes[2]}, "")
	require.NoError(t, err)
	require.EqualValues(t, 15, res.Items[0].Points, "silver earns ×1.5")

	var hist []domainregistration.TierHistory
	require.NoError(t, e.db.Where("member_id = ?", m.ID).Find(&hist).Error)
	require.Len(t, hist, 1)
	require.Equal(t, domainregistration.TierReasonUpgrade, hist[0].Reason)
	points, _ := e.balance(t, m.ID)
	require.EqualValues(t, 60, points)
}

func TestRedeemInputLimits(t *testing.T) {
	e := newRedeemEnv(t)
	m := e.member(t, "M1")
	_, err := e.r.Redeem(context.Background(), m.ID, nil, "")
	require.ErrorIs(t, err, domainproductcode.ErrNoCodes)
	many := make([]string, 11)
	_, err = e.r.Redeem(context.Background(), m.ID, many, "")
	require.ErrorIs(t, err, domainproductcode.ErrTooManyCodes)
	require.NoError(t, e.db.Model(&domainmember.Member{}).Where("id = ?", m.ID).Update("status", "suspended").Error)
	_, err = e.r.Redeem(context.Background(), m.ID, []string{wrongCode(1)}, "")
	require.ErrorIs(t, err, domain.ErrMemberNotAllowed)
}
