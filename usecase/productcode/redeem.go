package productcode

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strconv"
	"time"

	"crm-core/domain"
	domainearnrule "crm-core/domain/earnrule"
	domainloyalty "crm-core/domain/loyalty"
	domainmember "crm-core/domain/member"
	domainpoints "crm-core/domain/points"
	domainproductcode "crm-core/domain/productcode"
	domainregistration "crm-core/domain/registration"
	domainsettings "crm-core/domain/settings"
	"crm-core/timeutil"
	usecasepoints "crm-core/usecase/points"
)

// Points is the part of the points usecase redeeming needs.
type Points interface {
	Calculate(ctx context.Context, in usecasepoints.CalculateInput) (usecasepoints.CalculateResult, error)
	ExpiryPolicy(ctx context.Context) (usecasepoints.ExpiryPolicy, error)
}

// Item is the result for one typed code. Masked shows only the last four
// characters back to the member.
type Item struct {
	Masked  string
	Status  string
	Points  int64 // points credited (including a first-code bonus)
	SKUName string
}

// Quota is today's remaining allowance.
type Quota struct {
	Limit       int
	Used        int
	Remaining   int
	LockedUntil *time.Time
}

// RedeemResult is the outcome of one redeem request.
type RedeemResult struct {
	Items       []Item
	TotalPoints int64
	Quota       Quota
	TierUpgrade *domainloyalty.Tier // set when the member moved up a tier
}

// Redeemer is the member-side usecase (L4) plus the owner's limits and the
// test-member list (backoffice).
type Redeemer interface {
	Quota(ctx context.Context, memberID uint) (*Quota, error)
	Redeem(ctx context.Context, memberID uint, codes []string, ip string) (*RedeemResult, error)
	History(ctx context.Context, memberID uint, page, limit int) ([]domainproductcode.HistoryItem, int64, error)

	Policy(ctx context.Context) (domainproductcode.Policy, error)
	PutPolicy(ctx context.Context, actor domain.Actor, p domainproductcode.Policy) (domainproductcode.Policy, error)
	Flagged(ctx context.Context) ([]domainproductcode.FlaggedMember, error)
	TestMembers(ctx context.Context) ([]domainproductcode.TestMemberView, error)
	AddTestMember(ctx context.Context, actor domain.Actor, memberID uint) error
	RemoveTestMember(ctx context.Context, memberID uint) error
}

type redeemer struct {
	uow      domainproductcode.RedeemUnitOfWork
	attempts domainproductcode.AttemptRepository
	members  domainmember.IdentityRepository
	tiers    domainloyalty.TierRepository
	rules    domainearnrule.Repository
	settings domainsettings.Repository
	points   Points
	secret   []byte
	notify   CodeNotifier
	now      func() time.Time
}

// RedeemerDeps groups the redeemer's dependencies.
type RedeemerDeps struct {
	UnitOfWork domainproductcode.RedeemUnitOfWork
	Attempts   domainproductcode.AttemptRepository
	Members    domainmember.IdentityRepository
	Tiers      domainloyalty.TierRepository
	Rules      domainearnrule.Repository
	Settings   domainsettings.Repository
	Points     Points
	// Secret is PRODUCT_CODE_HMAC_SECRET (same value as backoffice-api).
	Secret []byte
	Now    func() time.Time
	// Notifier (optional) tells the member about points and tier changes.
	Notifier CodeNotifier
}

// CodeNotifier receives the outcome of a redeem request (LINE messages).
type CodeNotifier interface {
	PointsEarned(ctx context.Context, memberID uint, points, balance int64, ref string) error
	TierUpgraded(ctx context.Context, memberID, tierID uint, tierName string) error
}

// NewRedeemer builds the redeemer.
func NewRedeemer(d RedeemerDeps) Redeemer {
	now := d.Now
	if now == nil {
		now = time.Now
	}
	return &redeemer{
		uow: d.UnitOfWork, attempts: d.Attempts, members: d.Members, tiers: d.Tiers, rules: d.Rules,
		settings: d.Settings, points: d.Points, secret: d.Secret, notify: d.Notifier, now: now,
	}
}

func (r *redeemer) Policy(ctx context.Context) (domainproductcode.Policy, error) {
	p := domainproductcode.DefaultPolicy()
	if r.settings == nil {
		return p, nil
	}
	raw, err := r.settings.Get(ctx, domainproductcode.KeyPolicy)
	if errors.Is(err, domain.ErrNotFound) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	if json.Unmarshal([]byte(raw), &p) != nil {
		return domainproductcode.DefaultPolicy(), nil
	}
	d := domainproductcode.DefaultPolicy()
	if p.DailyQuota <= 0 {
		p.DailyQuota = d.DailyQuota
	}
	if p.WrongPerHour <= 0 {
		p.WrongPerHour = d.WrongPerHour
	}
	if p.WrongPerDay <= 0 {
		p.WrongPerDay = d.WrongPerDay
	}
	return p, nil
}

func (r *redeemer) PutPolicy(ctx context.Context, actor domain.Actor, p domainproductcode.Policy) (domainproductcode.Policy, error) {
	if !actor.IsOwner() {
		return p, domain.ErrForbidden
	}
	switch {
	case p.DailyQuota < 1 || p.DailyQuota > 1000:
		return p, domain.NewValidationError("dailyQuota", "must be between 1 and 1000")
	case p.WrongPerHour < 1 || p.WrongPerHour > 100:
		return p, domain.NewValidationError("wrongPerHour", "must be between 1 and 100")
	case p.WrongPerDay < p.WrongPerHour || p.WrongPerDay > 1000:
		return p, domain.NewValidationError("wrongPerDay", "must be at least wrongPerHour and at most 1000")
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return p, err
	}
	if err := r.settings.Put(ctx, domainproductcode.KeyPolicy, string(raw), actor.ERPUserID); err != nil {
		return p, err
	}
	return p, nil
}

func (r *redeemer) activeMember(ctx context.Context, id uint) (*domainmember.Member, error) {
	m, err := r.members.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if m.Status != domainmember.StatusActive || m.DeletedAt != nil {
		return nil, domain.ErrMemberNotAllowed
	}
	return m, nil
}

// lockedUntil applies the two brute-force rules to the member's recent
// not_found attempts.
func (r *redeemer) lockedUntil(ctx context.Context, memberID uint, p domainproductcode.Policy, now time.Time) (*time.Time, error) {
	times, err := r.attempts.RecentAttemptTimes(ctx, memberID, domainproductcode.AttemptNotFound, now.Add(-24*time.Hour))
	if err != nil {
		return nil, err
	}
	var until *time.Time
	consider := func(n int, window time.Duration) {
		if len(times) < n {
			return
		}
		// times is newest first: the n-th newest decides when the window clears.
		t := times[n-1].Add(window)
		if t.After(now) && (until == nil || t.After(*until)) {
			until = &t
		}
	}
	consider(p.WrongPerHour, time.Hour)
	consider(p.WrongPerDay, 24*time.Hour)
	return until, nil
}

func (r *redeemer) usedToday(ctx context.Context, memberID uint, now time.Time) (int, error) {
	n, err := r.attempts.CountAttempts(ctx, memberID, domainproductcode.AttemptSuccess, timeutil.StartOfDay(now))
	return int(n), err
}

func (r *redeemer) Quota(ctx context.Context, memberID uint) (*Quota, error) {
	if _, err := r.activeMember(ctx, memberID); err != nil {
		return nil, err
	}
	p, err := r.Policy(ctx)
	if err != nil {
		return nil, err
	}
	now := r.now()
	used, err := r.usedToday(ctx, memberID, now)
	if err != nil {
		return nil, err
	}
	locked, err := r.lockedUntil(ctx, memberID, p, now)
	if err != nil {
		return nil, err
	}
	return &Quota{Limit: p.DailyQuota, Used: used, Remaining: max(0, p.DailyQuota-used), LockedUntil: locked}, nil
}

func mask(input string) string {
	code, err := domainproductcode.Normalize(input)
	if err != nil {
		runes := []rune(input)
		if len(runes) > 4 {
			runes = runes[len(runes)-4:]
		}
		return "••••-••••-" + string(runes)
	}
	return "••••-••••-" + domainproductcode.Suffix(code)
}

func (r *redeemer) Redeem(ctx context.Context, memberID uint, codes []string, ip string) (*RedeemResult, error) {
	if len(r.secret) == 0 {
		return nil, domainproductcode.ErrNotConfigured
	}
	switch {
	case len(codes) == 0:
		return nil, domainproductcode.ErrNoCodes
	case len(codes) > domainproductcode.MaxCodesPerRequest:
		return nil, domainproductcode.ErrTooManyCodes
	}
	if _, err := r.activeMember(ctx, memberID); err != nil {
		return nil, err
	}
	p, err := r.Policy(ctx)
	if err != nil {
		return nil, err
	}
	now := r.now()
	if until, err := r.lockedUntil(ctx, memberID, p, now); err != nil {
		return nil, err
	} else if until != nil {
		return nil, &domainproductcode.LockedError{Until: *until}
	}
	used, err := r.usedToday(ctx, memberID, now)
	if err != nil {
		return nil, err
	}
	if used >= p.DailyQuota {
		return nil, domainproductcode.ErrQuotaReached
	}
	isTester, err := r.attempts.IsTestMember(ctx, memberID)
	if err != nil {
		return nil, err
	}

	res := &RedeemResult{}
	seen := map[string]bool{}
	locked := false
	for _, input := range codes {
		item := Item{Masked: mask(input)}
		code, err := domainproductcode.Normalize(input)
		switch {
		case locked:
			item.Status = domainproductcode.ItemLocked
		case err != nil:
			item.Status = domainproductcode.ItemInvalidFormat
			r.record(ctx, memberID, "", domainproductcode.AttemptInvalidFormat, nil, ip, now)
		case seen[code]:
			item.Status = domainproductcode.ItemDuplicate
		case used >= p.DailyQuota:
			item.Status = domainproductcode.ItemQuotaExceeded
		default:
			seen[code] = true
			outcome, err := r.redeemOne(ctx, memberID, code, isTester, ip, now)
			if err != nil {
				return nil, err
			}
			item.Status, item.Points, item.SKUName = outcome.status, outcome.points, outcome.skuName
			if outcome.upgrade != nil {
				res.TierUpgrade = outcome.upgrade
			}
			if item.Status == domainproductcode.ItemSuccess {
				used++
				res.TotalPoints += item.Points
			}
			if item.Status == domainproductcode.ItemNotFound {
				until, err := r.lockedUntil(ctx, memberID, p, now)
				if err != nil {
					return nil, err
				}
				locked = until != nil
			}
		}
		res.Items = append(res.Items, item)
	}

	until, err := r.lockedUntil(ctx, memberID, p, now)
	if err != nil {
		return nil, err
	}
	res.Quota = Quota{Limit: p.DailyQuota, Used: used, Remaining: max(0, p.DailyQuota-used), LockedUntil: until}
	r.notifyOutcome(ctx, memberID, res, now)
	return res, nil
}

// notifyOutcome queues the LINE messages for a redeem request (best effort).
func (r *redeemer) notifyOutcome(ctx context.Context, memberID uint, res *RedeemResult, now time.Time) {
	if r.notify == nil {
		return
	}
	if res.TotalPoints > 0 {
		balance := int64(0)
		if m, err := r.members.FindByID(ctx, memberID); err == nil {
			balance = m.PointBalance
		}
		ref := strconv.FormatUint(uint64(memberID), 10) + ":" + strconv.FormatInt(now.UnixNano(), 10)
		if err := r.notify.PointsEarned(ctx, memberID, res.TotalPoints, balance, ref); err != nil {
			log.Printf("[WARN] LINE notify points for member %d: %v", memberID, err)
		}
	}
	if res.TierUpgrade != nil {
		if err := r.notify.TierUpgraded(ctx, memberID, res.TierUpgrade.ID, res.TierUpgrade.Name); err != nil {
			log.Printf("[WARN] LINE notify tier for member %d: %v", memberID, err)
		}
	}
}

// record writes a failed attempt; a failure to log never blocks the member.
func (r *redeemer) record(ctx context.Context, memberID uint, suffix, result string, codeID *uint, ip string, now time.Time) {
	_ = r.attempts.CreateAttempt(ctx, &domainproductcode.RedemptionAttempt{
		MemberID: memberID, InputSuffix: suffix, Result: result, ProductCodeID: codeID, IP: truncateIP(ip), CreatedAt: now,
	})
}

func truncateIP(ip string) string {
	if len(ip) > 64 {
		return ip[:64]
	}
	return ip
}

// prepared is everything a redemption reads outside its transaction. The
// transaction itself only touches rows it locks, so it never waits on another
// connection while holding its locks.
type prepared struct {
	tiers     []domainloyalty.Tier
	calc      map[uint]usecasepoints.CalculateResult // by tier id
	expiresAt *time.Time
	bonus     int64
	bonusIDs  []uint
}

func (r *redeemer) prepare(ctx context.Context, skuCode string, base int64, now time.Time) (*prepared, error) {
	tiers, err := r.tiers.FindAll(ctx)
	if err != nil {
		return nil, err
	}
	p := &prepared{tiers: tiers, calc: map[uint]usecasepoints.CalculateResult{}}
	for _, t := range tiers {
		res, err := r.points.Calculate(ctx, usecasepoints.CalculateInput{TierID: t.ID, SKUID: skuCode, BasePoints: base, At: now})
		if err != nil {
			return nil, err
		}
		p.calc[t.ID] = res
	}
	if p.expiresAt, err = r.expiresAt(ctx, now); err != nil {
		return nil, err
	}
	if p.bonus, p.bonusIDs, err = r.firstCodeBonus(ctx, now); err != nil {
		return nil, err
	}
	return p, nil
}

type outcome struct {
	status  string
	points  int64
	skuName string
	upgrade *domainloyalty.Tier
}

// redeemOne redeems a single normalized code in its own transaction, so a
// wrong code never rolls back a right one.
func (r *redeemer) redeemOne(ctx context.Context, memberID uint, code string, isTester bool, ip string, now time.Time) (outcome, error) {
	hash := domainproductcode.Hash(r.secret, code)
	suffix := domainproductcode.Suffix(code)
	var out outcome
	var failed string
	var failedCodeID *uint

	batchInfo, err := r.attempts.FindBatchByCodeHash(ctx, hash)
	if errors.Is(err, domain.ErrNotFound) {
		r.record(ctx, memberID, suffix, domainproductcode.AttemptNotFound, nil, ip, now)
		return outcome{status: domainproductcode.AttemptNotFound}, nil
	}
	if err != nil {
		return outcome{}, err
	}
	prep, err := r.prepare(ctx, batchInfo.SKUCode, batchInfo.PointsPerCode, now)
	if err != nil {
		return outcome{}, err
	}

	err = r.uow.Do(ctx, func(ctx context.Context, tx domainproductcode.RedeemTx) error {
		c, batch, err := tx.LockCodeByHash(ctx, hash)
		if errors.Is(err, domain.ErrNotFound) {
			failed = domainproductcode.AttemptNotFound
			return nil
		}
		if err != nil {
			return err
		}
		id := c.ID
		failedCodeID = &id
		switch {
		// Test batches look like unknown codes to everyone but testers, and
		// batches not yet active look unknown too.
		case batch.IsTest && !isTester,
			batch.Status == domainproductcode.BatchDraft,
			batch.Status == domainproductcode.BatchExported:
			failed = domainproductcode.AttemptNotFound
			failedCodeID = nil
			return nil
		case c.Status == domainproductcode.CodeUsed:
			failed = domainproductcode.AttemptUsed
			return nil
		case c.Status == domainproductcode.CodeVoid || batch.Status == domainproductcode.BatchVoid:
			failed = domainproductcode.AttemptVoid
			return nil
		case batch.CodeExpiresAt != nil && !now.Before(*batch.CodeExpiresAt):
			failed = domainproductcode.AttemptExpired
			return nil
		}

		m, err := tx.LockMember(ctx, memberID)
		if err != nil {
			return err
		}
		calc, ok := prep.calc[m.TierID]
		if !ok || batch.PointsPerCode != batchInfo.PointsPerCode || batch.SKUCode != batchInfo.SKUCode {
			return errBatchChanged
		}
		expiresAt := prep.expiresAt
		ruleIDs := make([]uint, 0, len(calc.Rules))
		for _, rule := range calc.Rules {
			ruleIDs = append(ruleIDs, rule.ID)
		}
		prior, err := tx.CountUsedByMember(ctx, memberID)
		if err != nil {
			return err
		}
		entry, err := tx.Points().Earn(ctx, domainpoints.EarnCommand{
			MemberID: memberID, Points: calc.Points, SourceType: domainpoints.SourceProductCode,
			SourceID: strconv.FormatUint(uint64(c.ID), 10), Note: "กรอกรหัสสินค้า " + batch.SKUNameSnapshot,
			RuleIDs: ruleIDs, ExpiresAt: expiresAt,
		})
		if err != nil {
			return err
		}
		if err := tx.MarkCodeUsed(ctx, c.ID, memberID, entry.ID, now); err != nil {
			return err
		}
		out.points = calc.Points

		if prior == 0 {
			bonus, bonusRules := prep.bonus, prep.bonusIDs
			if bonus > 0 {
				if _, err := tx.Points().Earn(ctx, domainpoints.EarnCommand{
					MemberID: memberID, Points: bonus, SourceType: domainpoints.SourceBonus,
					SourceID: "first_code:" + strconv.FormatUint(uint64(memberID), 10), Note: "โบนัสกรอกรหัสครั้งแรก",
					RuleIDs: bonusRules, ExpiresAt: expiresAt,
				}); err != nil && !errors.Is(err, domain.ErrConflict) {
					return err
				} else if err == nil {
					out.points += bonus
				}
			}
		}

		lifetime := m.LifetimeSpend + batch.SpendValuePerCode
		tierID := m.TierID
		if next := upgradeTier(prep.tiers, m.TierID, lifetime); next != nil {
			tierID = next.ID
			from := m.TierID
			if err := tx.AddTierHistory(ctx, memberID, &from, next.ID, domainregistration.TierReasonUpgrade, now); err != nil {
				return err
			}
			out.upgrade = next
		}
		if err := tx.UpdateMemberSpendAndTier(ctx, memberID, lifetime, tierID); err != nil {
			return err
		}
		if err := tx.CreateAttempt(ctx, &domainproductcode.RedemptionAttempt{
			MemberID: memberID, InputSuffix: suffix, Result: domainproductcode.AttemptSuccess,
			ProductCodeID: &id, IP: truncateIP(ip), CreatedAt: now,
		}); err != nil {
			return err
		}
		out.status = domainproductcode.ItemSuccess
		out.skuName = batch.SKUNameSnapshot
		return nil
	})
	if err != nil {
		return outcome{}, err
	}
	if failed != "" {
		r.record(ctx, memberID, suffix, failed, failedCodeID, ip, now)
		return outcome{status: failed}, nil
	}
	return out, nil
}

func (r *redeemer) expiresAt(ctx context.Context, at time.Time) (*time.Time, error) {
	p, err := r.points.ExpiryPolicy(ctx)
	if err != nil {
		return nil, err
	}
	if p.Months <= 0 {
		return nil, nil
	}
	t := at.AddDate(0, p.Months, 0)
	return &t, nil
}

func (r *redeemer) firstCodeBonus(ctx context.Context, at time.Time) (int64, []uint, error) {
	if r.rules == nil {
		return 0, nil, nil
	}
	rules, err := r.rules.FindActive(ctx, at)
	if err != nil {
		return 0, nil, err
	}
	for _, rule := range rules {
		if rule.Type == domainearnrule.TypeBonusFixed && rule.Trigger == domainearnrule.TriggerFirstCode && rule.Points > 0 {
			return rule.Points, []uint{rule.ID}, nil
		}
	}
	return 0, nil, nil
}

// errBatchChanged means the batch was edited between the preparation and
// the transaction (should never happen: exported batches are immutable).
var errBatchChanged = errors.New("product code batch changed during redemption")

// upgradeTier returns the highest active tier the lifetime spend reaches, if
// it is above the current one. Members never move down here.
func upgradeTier(tiers []domainloyalty.Tier, currentID uint, lifetime float64) *domainloyalty.Tier {
	var current *domainloyalty.Tier
	for i := range tiers {
		if tiers[i].ID == currentID {
			current = &tiers[i]
		}
	}
	if current == nil {
		return nil
	}
	var best *domainloyalty.Tier
	for i := range tiers {
		t := tiers[i]
		if !t.IsActive || t.MinSpend <= current.MinSpend || t.MinSpend > lifetime {
			continue
		}
		if best == nil || t.MinSpend > best.MinSpend {
			best = &t
		}
	}
	return best
}

func (r *redeemer) History(ctx context.Context, memberID uint, page, limit int) ([]domainproductcode.HistoryItem, int64, error) {
	if _, err := r.activeMember(ctx, memberID); err != nil {
		return nil, 0, err
	}
	return r.attempts.History(ctx, memberID, page, limit)
}

func (r *redeemer) Flagged(ctx context.Context) ([]domainproductcode.FlaggedMember, error) {
	p, err := r.Policy(ctx)
	if err != nil {
		return nil, err
	}
	return r.attempts.Flagged(ctx, r.now().Add(-24*time.Hour), p.WrongPerDay)
}

func (r *redeemer) TestMembers(ctx context.Context) ([]domainproductcode.TestMemberView, error) {
	return r.attempts.ListTestMembers(ctx)
}

func (r *redeemer) AddTestMember(ctx context.Context, actor domain.Actor, memberID uint) error {
	if memberID == 0 {
		return domain.NewValidationError("memberId", "is required")
	}
	return r.attempts.AddTestMember(ctx, memberID, actor.ERPUserID, r.now())
}

func (r *redeemer) RemoveTestMember(ctx context.Context, memberID uint) error {
	return r.attempts.RemoveTestMember(ctx, memberID)
}
