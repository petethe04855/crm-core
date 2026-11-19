// Package memberreport is part B of the sales dashboard (plan §6.3): what CRM
// members do — sign-ups per month, active members, and points earned,
// redeemed and expired from the ledger. Marketplace buyers are part A.
package memberreport

import (
	"context"
	"time"

	"crm-core/domain"
	domainpoints "crm-core/domain/points"
	"crm-core/timeutil"
)

// MaxRangeDays bounds one report.
const MaxRangeDays = 366

// Month is one calendar month (Bangkok) of the range.
type Month struct {
	Month      string // "2026-11"
	NewMembers int64
	Earned     int64
	Redeemed   int64 // positive number of points spent
	Expired    int64 // positive number of points expired
}

// Report is the member report of a date range [From, To).
type Report struct {
	From             time.Time
	To               time.Time
	TotalMembers     int64 // all members not deleted, as of now
	NewMembers       int64
	ActiveMembers    int64 // last activity inside the range
	PointsEarned     int64
	PointsRedeemed   int64
	PointsExpired    int64
	PointsAdjusted   int64 // net manual adjustments
	PointsRefunded   int64
	OutstandingPoint int64 // sum of current balances (point liability)
	Months           []Month
}

// Source reads the aggregates. Ranges are half-open [from, to).
type Source interface {
	CountMembers(ctx context.Context) (int64, error)
	CountRegistered(ctx context.Context, from, to time.Time) (int64, error)
	CountActive(ctx context.Context, from, to time.Time) (int64, error)
	SumBalances(ctx context.Context) (int64, error)
	// LedgerTotals sums points per ledger type in the range.
	LedgerTotals(ctx context.Context, from, to time.Time) (map[string]int64, error)
}

// Usecase builds the member report.
type Usecase interface {
	Build(ctx context.Context, from, to time.Time) (*Report, error)
}

type usecase struct {
	src Source
}

// New builds the usecase.
func New(src Source) Usecase { return &usecase{src: src} }

func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

// Build reports on [from, to). Months are cut at Bangkok month boundaries and
// clipped to the range.
func (u *usecase) Build(ctx context.Context, from, to time.Time) (*Report, error) {
	if !from.Before(to) {
		return nil, domain.NewValidationError("dateFrom", "must be before dateTo")
	}
	if to.Sub(from) > MaxRangeDays*24*time.Hour+time.Hour {
		return nil, domain.NewValidationError("dateTo", "the range can be at most 366 days")
	}
	r := &Report{From: from, To: to}
	var err error
	if r.TotalMembers, err = u.src.CountMembers(ctx); err != nil {
		return nil, err
	}
	if r.NewMembers, err = u.src.CountRegistered(ctx, from, to); err != nil {
		return nil, err
	}
	if r.ActiveMembers, err = u.src.CountActive(ctx, from, to); err != nil {
		return nil, err
	}
	if r.OutstandingPoint, err = u.src.SumBalances(ctx); err != nil {
		return nil, err
	}
	totals, err := u.src.LedgerTotals(ctx, from, to)
	if err != nil {
		return nil, err
	}
	r.PointsEarned = totals[domainpoints.TypeEarn]
	r.PointsRedeemed = abs(totals[domainpoints.TypeRedeem])
	r.PointsExpired = abs(totals[domainpoints.TypeExpire])
	r.PointsAdjusted = totals[domainpoints.TypeAdjust]
	r.PointsRefunded = totals[domainpoints.TypeRefund]

	bkk := timeutil.Bangkok()
	start := from.In(bkk)
	cursor, _ := timeutil.MonthRange(start.Year(), start.Month())
	for cursor.Before(to) {
		_, monthEnd := timeutil.MonthRange(cursor.In(bkk).Year(), cursor.In(bkk).Month())
		lo, hi := cursor, monthEnd
		if lo.Before(from) {
			lo = from
		}
		if hi.After(to) {
			hi = to
		}
		m := Month{Month: cursor.In(bkk).Format("2006-01")}
		if m.NewMembers, err = u.src.CountRegistered(ctx, lo, hi); err != nil {
			return nil, err
		}
		mt, err := u.src.LedgerTotals(ctx, lo, hi)
		if err != nil {
			return nil, err
		}
		m.Earned, m.Redeemed, m.Expired = mt[domainpoints.TypeEarn], abs(mt[domainpoints.TypeRedeem]), abs(mt[domainpoints.TypeExpire])
		r.Months = append(r.Months, m)
		cursor = monthEnd
	}
	return r, nil
}
