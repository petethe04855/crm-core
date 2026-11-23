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

// GenderCount is one slice of the gender split of the members who registered
// inside the range. Unspecified is reported, never dropped.
type GenderCount struct {
	Gender       string // male | female | other | unspecified
	Members      int64
	SharePercent float64
}

// AgeBucket is one age band of the members who registered inside the range.
// Members without a birth date are reported as Unknown, never guessed.
type AgeBucket struct {
	Key          string // "18-24" | "unknown"
	Label        string // "18–24 ปี" | "ไม่ระบุวันเกิด"
	Members      int64
	SharePercent float64
}

// FirstSKU is the SKU a member typed a product code for the very first time,
// counted once per member. It answers "what did they start with".
type FirstSKU struct {
	SKUCode      string
	SKUName      string
	Members      int64
	SharePercent float64
}

// RewardRedeemed is one reward members redeemed inside the range (cancelled
// redemptions excluded, they were refunded).
type RewardRedeemed struct {
	RewardID     uint
	RewardName   string
	Redemptions  int64
	Qty          int64
	Points       int64
	SharePercent float64 // of all redemptions in the range
}

// FirstReward is the reward a member redeemed for the very first time, counted
// once per member. It answers "what did they take first".
type FirstReward struct {
	RewardID     uint
	RewardName   string
	Members      int64
	SharePercent float64 // of the members whose first redemption falls in the range
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
	// Registration split of the members created inside the range.
	Genders    []GenderCount
	AgeBuckets []AgeBucket
	// FirstSKU is the first product code each member ever redeemed, grouped by
	// SKU. Only members whose first code falls inside the range are counted.
	FirstSKUs []FirstSKU
	// FirstReward is the reward each member redeemed first, grouped by reward.
	// Only members whose first redemption falls inside the range are counted.
	FirstRewards []FirstReward
	// Rewards redeemed inside the range (not cancelled), most redeemed first.
	Redemptions       int64
	RedemptionMembers int64 // distinct members who redeemed
	Rewards           []RewardRedeemed
}

// Source reads the aggregates. Ranges are half-open [from, to).
type Source interface {
	CountMembers(ctx context.Context) (int64, error)
	CountRegistered(ctx context.Context, from, to time.Time) (int64, error)
	CountActive(ctx context.Context, from, to time.Time) (int64, error)
	SumBalances(ctx context.Context) (int64, error)
	// LedgerTotals sums points per ledger type in the range.
	LedgerTotals(ctx context.Context, from, to time.Time) (map[string]int64, error)
	// GenderCounts counts the members created in the range per gender.
	GenderCounts(ctx context.Context, from, to time.Time) (map[string]int64, error)
	// BirthDates returns the birth date of every member created in the range;
	// nil means the member never supplied one.
	BirthDates(ctx context.Context, from, to time.Time) ([]*time.Time, error)
	// FirstSKUs groups the first product code of each member by SKU, counting
	// only members whose first code was used inside the range.
	FirstSKUs(ctx context.Context, from, to time.Time) ([]FirstSKU, error)
	// FirstRewards groups the first non-cancelled redemption of each member by
	// reward, counting only members whose first redemption is inside the range.
	FirstRewards(ctx context.Context, from, to time.Time) ([]FirstReward, error)
	// RewardsRedeemed groups the non-cancelled redemptions created in the
	// range by reward, and counts the distinct members who redeemed.
	RewardsRedeemed(ctx context.Context, from, to time.Time) ([]RewardRedeemed, int64, error)
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

// sharePercent is one decimal place, and 0 when the base is 0.
func sharePercent(part, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(int64(float64(part)/float64(total)*1000+0.5)) / 10
}

// ageBands are the buckets the dashboard shows, in display order.
var ageBands = []struct {
	Key   string
	Label string
	Min   int // inclusive
	Max   int // inclusive; 0 means no upper bound
}{
	{Key: "under18", Label: "ต่ำกว่า 18 ปี", Min: 0, Max: 17},
	{Key: "18-24", Label: "18–24 ปี", Min: 18, Max: 24},
	{Key: "25-34", Label: "25–34 ปี", Min: 25, Max: 34},
	{Key: "35-44", Label: "35–44 ปี", Min: 35, Max: 44},
	{Key: "45-54", Label: "45–54 ปี", Min: 45, Max: 54},
	{Key: "55plus", Label: "55 ปีขึ้นไป", Min: 55, Max: 0},
}

// ageAt returns the full years between birth and at, in the Bangkok calendar.
func ageAt(birth, at time.Time) int {
	bkk := timeutil.Bangkok()
	b, a := birth.In(bkk), at.In(bkk)
	years := a.Year() - b.Year()
	if a.Month() < b.Month() || (a.Month() == b.Month() && a.Day() < b.Day()) {
		years--
	}
	if years < 0 {
		return 0
	}
	return years
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

	if r.Genders, err = u.genders(ctx, from, to, r.NewMembers); err != nil {
		return nil, err
	}
	if r.AgeBuckets, err = u.ages(ctx, from, to, r.NewMembers); err != nil {
		return nil, err
	}
	if r.FirstSKUs, err = u.src.FirstSKUs(ctx, from, to); err != nil {
		return nil, err
	}
	for i := range r.FirstSKUs {
		r.FirstSKUs[i].SharePercent = sharePercent(r.FirstSKUs[i].Members, r.NewMembers)
	}

	if r.FirstRewards, err = u.src.FirstRewards(ctx, from, to); err != nil {
		return nil, err
	}
	var firstTotal int64
	for _, fr := range r.FirstRewards {
		firstTotal += fr.Members
	}
	for i := range r.FirstRewards {
		r.FirstRewards[i].SharePercent = sharePercent(r.FirstRewards[i].Members, firstTotal)
	}

	if r.Rewards, r.RedemptionMembers, err = u.src.RewardsRedeemed(ctx, from, to); err != nil {
		return nil, err
	}
	for _, rw := range r.Rewards {
		r.Redemptions += rw.Redemptions
	}
	for i := range r.Rewards {
		r.Rewards[i].SharePercent = sharePercent(r.Rewards[i].Redemptions, r.Redemptions)
	}

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

// genders returns every gender value in a stable order, including the ones
// nobody picked, so the chart never shifts between two requests.
func (u *usecase) genders(ctx context.Context, from, to time.Time, total int64) ([]GenderCount, error) {
	counts, err := u.src.GenderCounts(ctx, from, to)
	if err != nil {
		return nil, err
	}
	order := []string{"male", "female", "other", "unspecified"}
	out := make([]GenderCount, 0, len(order))
	for _, g := range order {
		n := counts[g]
		out = append(out, GenderCount{Gender: g, Members: n, SharePercent: sharePercent(n, total)})
	}
	return out, nil
}

// ages buckets the birth dates of the members created in the range. A member
// with no birth date lands in "unknown"; nobody is estimated.
func (u *usecase) ages(ctx context.Context, from, to time.Time, total int64) ([]AgeBucket, error) {
	births, err := u.src.BirthDates(ctx, from, to)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	counts := make(map[string]int64, len(ageBands))
	var unknown int64
	for _, b := range births {
		if b == nil {
			unknown++
			continue
		}
		age := ageAt(*b, now)
		placed := false
		for _, band := range ageBands {
			if age >= band.Min && (band.Max == 0 || age <= band.Max) {
				counts[band.Key]++
				placed = true
				break
			}
		}
		if !placed {
			unknown++
		}
	}
	out := make([]AgeBucket, 0, len(ageBands)+1)
	for _, band := range ageBands {
		n := counts[band.Key]
		out = append(out, AgeBucket{Key: band.Key, Label: band.Label, Members: n, SharePercent: sharePercent(n, total)})
	}
	out = append(out, AgeBucket{Key: "unknown", Label: "ไม่ระบุวันเกิด", Members: unknown, SharePercent: sharePercent(unknown, total)})
	return out, nil
}