package coupon

import (
	"context"
	"testing"

	domaincoupon "crm-core/domain/coupon"
	domainloyalty "crm-core/domain/loyalty"

	"github.com/stretchr/testify/require"
)

func TestAvailableCouponsForMember(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.member(t, 1, 100)

	open := e.activeCoupon(t, baseInput())
	gold := domainloyalty.Tier{Code: "gold", Name: "Gold", MinSpend: 20000, SortOrder: 5, IsActive: true, EarnMultiplier: 1}
	require.NoError(t, e.db.Create(&gold).Error)
	vip := baseInput()
	vip.Name = "เฉพาะ Gold"
	vip.MinTierID = &gold.ID
	e.activeCoupon(t, vip)
	_, err := e.uc.Create(ctx, e.owner, baseInput()) // draft: hidden
	require.NoError(t, err)

	list, err := e.uc.Available(ctx, m.ID, e.now)
	require.NoError(t, err)
	require.Len(t, list, 2)
	byName := map[string]AvailableCoupon{}
	for _, c := range list {
		byName[c.Coupon.Name] = c
	}
	require.True(t, byName["ลด 50 บาท"].CanClaim)
	require.False(t, byName["เฉพาะ Gold"].CanClaim)
	require.Equal(t, ReasonTier, byName["เฉพาะ Gold"].Reason)

	_, err = e.uc.Claim(ctx, ClaimRequest{MemberID: m.ID, CouponID: open.ID, At: e.now})
	require.NoError(t, err)
	list, err = e.uc.Available(ctx, m.ID, e.now)
	require.NoError(t, err)
	for _, c := range list {
		if c.Coupon.ID == open.ID {
			require.False(t, c.CanClaim)
			require.Equal(t, ReasonLimit, c.Reason)
			require.EqualValues(t, 1, c.Held)
		}
	}
}

func TestMemberCouponsShowCodes(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.member(t, 1, 100)

	unique := e.activeCoupon(t, baseInput())
	shared := baseInput()
	shared.Name = "Shopee"
	shared.PointsCost = 0
	shared.Channel, shared.CodeType, shared.SharedCode = domaincoupon.ChannelShopee, domaincoupon.CodeShared, "CHAWY11"
	sh := e.activeCoupon(t, shared)

	u1, err := e.uc.Claim(ctx, ClaimRequest{MemberID: m.ID, CouponID: unique.ID, At: e.now})
	require.NoError(t, err)
	_, err = e.uc.Claim(ctx, ClaimRequest{MemberID: m.ID, CouponID: sh.ID, At: e.now})
	require.NoError(t, err)

	rows, total, err := e.uc.MemberCoupons(ctx, m.ID, "", 1, 20)
	require.NoError(t, err)
	require.EqualValues(t, 2, total)
	codes := map[string]string{}
	for _, r := range rows {
		codes[r.Coupon.Name] = r.DisplayCode
	}
	require.Equal(t, u1.Code, codes["ลด 50 บาท"])
	require.Equal(t, "CHAWY11", codes["Shopee"], "shared templates show the shared code")

	rows, _, err = e.uc.MemberCoupons(ctx, m.ID, domaincoupon.HolderUsed, 1, 20)
	require.NoError(t, err)
	require.Empty(t, rows)
	_, _, err = e.uc.MemberCoupons(ctx, m.ID, "weird", 1, 20)
	require.Error(t, err)
}
