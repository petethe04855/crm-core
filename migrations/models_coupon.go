package migrations

import (
	domaincoupon "crm-core/domain/coupon"
)

// couponModels lists the coupon templates and the coupons members hold.
func couponModels() []any {
	return []any{&domaincoupon.Coupon{}, &domaincoupon.MemberCoupon{}}
}
