package points

import (
	"context"
	"time"

	"crm-core/domain"
	domainmember "crm-core/domain/member"
)

// Check is the central redemption guard (plan §4.2). Coupons (T4) and rewards
// (T6) call it before deducting points; every failure is a domain error the
// delivery layer maps to its own 409 code.
//
// The checks run cheapest-first in a deliberate order: status, verified email,
// member age, daily redemption count, then balance. A member who fails an
// earlier check is never counted for the day.
func (u *usecase) Check(ctx context.Context, member *domainmember.Member, cost int64, at time.Time) error {
	if member == nil || member.ID == 0 {
		return domain.ErrNotFound
	}
	if cost <= 0 {
		return domain.NewValidationError("cost", "must be greater than zero")
	}
	if at.IsZero() {
		at = u.now()
	}

	policy, err := u.RedeemPolicy(ctx)
	if err != nil {
		return err
	}

	if !statusAllowed(member.Status, policy.AllowedStatuses) {
		return domain.ErrMemberNotAllowed
	}
	if policy.RequireEmailVerified && member.EmailVerifiedAt == nil {
		return domain.ErrEmailNotVerified
	}
	if policy.MinMemberAgeDays > 0 {
		// Age is 24-hour periods, not calendar days: a member registered at
		// 23:00 yesterday is not "1 day old" at 08:00 today.
		minAge := time.Duration(policy.MinMemberAgeDays) * 24 * time.Hour
		if at.Sub(member.CreatedAt) < minAge {
			return domain.ErrMemberTooNew
		}
	}
	if policy.MaxRedemptionsPerDay > 0 {
		from, to := bangkokDayBounds(at)
		count, err := u.ledger.RedeemCountBetween(ctx, member.ID, from, to)
		if err != nil {
			return err
		}
		if count >= int64(policy.MaxRedemptionsPerDay) {
			return domain.ErrDailyLimitReached
		}
	}

	// The minimum balance is a floor on what may remain, so it is checked
	// together with the cost: either can make this redemption impossible.
	if member.PointBalance < cost || member.PointBalance < policy.MinBalanceToRedeem {
		return domain.ErrInsufficientPoints
	}
	return nil
}

func statusAllowed(status string, allowed []string) bool {
	for _, a := range allowed {
		if a == status {
			return true
		}
	}
	return false
}
