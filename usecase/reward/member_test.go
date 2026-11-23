package reward

import (
	"context"
	"sync"
	"testing"

	"crm-core/domain"
	domainreward "crm-core/domain/reward"

	"github.com/stretchr/testify/require"
)

func TestRedeemIsIdempotentPerKey(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.member(t, 1, 200)
	r := e.activeReward(t, baseInput())

	first, err := e.uc.Redeem(ctx, RedeemRequest{MemberID: m.ID, RewardID: r.ID, Address: addr, IdempotencyKey: "tap-1"})
	require.NoError(t, err)
	again, err := e.uc.Redeem(ctx, RedeemRequest{MemberID: m.ID, RewardID: r.ID, Address: addr, IdempotencyKey: "tap-1"})
	require.NoError(t, err)
	require.Equal(t, first.ID, again.ID, "same key returns the same redemption")
	require.EqualValues(t, 160, e.balance(t, m.ID), "charged once")

	_, err = e.uc.Redeem(ctx, RedeemRequest{MemberID: m.ID, RewardID: r.ID, Address: addr, IdempotencyKey: "tap-2"})
	require.NoError(t, err)
	require.EqualValues(t, 120, e.balance(t, m.ID), "a new key is a new redemption")

	_, err = e.uc.Redeem(ctx, RedeemRequest{MemberID: m.ID, RewardID: r.ID, Address: addr, IdempotencyKey: "bad key!"})
	require.ErrorIs(t, err, domain.ErrValidation)

	other := e.activeReward(t, Input{Name: "อีกชิ้น", ERPSKUID: sku(101), SKUCode: "CHK-01", SKUName: "ขนมไก่", PointsCost: 10})
	_, err = e.uc.Redeem(ctx, RedeemRequest{MemberID: m.ID, RewardID: other.ID, Address: addr, IdempotencyKey: "tap-1"})
	require.ErrorIs(t, err, domain.ErrValidation, "a key cannot be reused for another reward")

	// Another member may use the same key value.
	m2 := e.member(t, 2, 100)
	_, err = e.uc.Redeem(ctx, RedeemRequest{MemberID: m2.ID, RewardID: r.ID, Address: addr, IdempotencyKey: "tap-1"})
	require.NoError(t, err)
}

func TestConcurrentSameKeyChargesOnce(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.member(t, 1, 200)
	r := e.activeReward(t, baseInput())
	var wg sync.WaitGroup
	ids := make([]uint, 4)
	errs := make([]error, 4)
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rd, err := e.uc.Redeem(ctx, RedeemRequest{MemberID: m.ID, RewardID: r.ID, Address: addr, IdempotencyKey: "double-tap"})
			errs[i] = err
			if err == nil {
				ids[i] = rd.ID
			}
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		require.NoError(t, err)
		require.Equal(t, ids[0], ids[i])
	}
	require.EqualValues(t, 160, e.balance(t, m.ID))
}

func TestConcurrentRedeemsNeverGoNegative(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.member(t, 1, 100) // enough for two 40-point redemptions, not three
	r := e.activeReward(t, baseInput())
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = e.uc.Redeem(ctx, RedeemRequest{MemberID: m.ID, RewardID: r.ID, Address: addr})
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
			continue
		}
		require.ErrorIs(t, err, domain.ErrInsufficientPoints)
	}
	require.Equal(t, 2, ok)
	require.EqualValues(t, 20, e.balance(t, m.ID))
}

func TestCatalogShowsOnlyOpenRewardsWithStock(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	open := e.activeReward(t, baseInput())
	empty := baseInput()
	empty.Name = "หมดสต็อก"
	empty.ERPSKUID = sku(102)
	e.activeReward(t, empty)
	_, err := e.uc.Create(ctx, e.owner, Input{Name: "ร่าง", ERPSKUID: sku(101), SKUCode: "CHK-01", SKUName: "ขนมไก่", PointsCost: 5})
	require.NoError(t, err)
	later := baseInput()
	later.Name = "ยังไม่เปิด"
	from := e.now.AddDate(0, 0, 7)
	later.AvailableFrom = &from
	e.activeReward(t, later)

	rows, total, err := e.uc.Catalog(ctx, domainreward.Query{}, e.now)
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Equal(t, open.ID, rows[0].Reward.ID)

	rows, total, err = e.uc.Catalog(ctx, domainreward.Query{Search: "ไม่มีชื่อนี้"}, e.now)
	require.NoError(t, err)
	require.Zero(t, total)
	require.Empty(t, rows)
}
