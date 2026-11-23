package reward

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	"crm-core/domain"
	domainmember "crm-core/domain/member"
	domainpoints "crm-core/domain/points"
	domainreward "crm-core/domain/reward"
	"crm-core/timeutil"
	usecasepoints "crm-core/usecase/points"
)

// RedeemRequest is a member redeeming one reward (called from the LINE side).
type RedeemRequest struct {
	MemberID uint
	RewardID uint
	At       time.Time
	Address  Address
	// IdempotencyKey (optional, ≤64 of A-Z a-z 0-9 - _) makes retries safe:
	// the same member + key returns the first redemption.
	IdempotencyKey string
}

func validKey(k string) bool {
	if len(k) == 0 || len(k) > 64 {
		return false
	}
	for _, c := range k {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// Address is the shipping address snapshot.
type Address struct {
	Name     string
	Phone    string
	Address  string
	Province string
	Postcode string
	Note     string
}

func (a Address) validate() error {
	a.Name, a.Phone, a.Address = strings.TrimSpace(a.Name), strings.TrimSpace(a.Phone), strings.TrimSpace(a.Address)
	if a.Name == "" || a.Phone == "" || a.Address == "" {
		return domain.NewValidationError("address", "name, phone and address are required for shipping")
	}
	if utf8.RuneCountInString(a.Address) > 500 || utf8.RuneCountInString(a.Note) > 500 {
		return domain.NewValidationError("address", "address and note must be 500 characters or fewer")
	}
	return nil
}

func (u *usecase) open(r domainreward.Reward, at time.Time) error {
	if r.Status != domainreward.StatusActive {
		return domainreward.ErrNotAvailable
	}
	if r.AvailableFrom != nil && at.Before(*r.AvailableFrom) {
		return domainreward.ErrNotAvailable
	}
	if r.AvailableTo != nil && !at.Before(*r.AvailableTo) {
		return domainreward.ErrNotAvailable
	}
	return nil
}

func (u *usecase) checkTier(ctx context.Context, r domainreward.Reward, m domainmember.Member) error {
	if r.MinTierID == nil || *r.MinTierID == m.TierID || u.tiers == nil {
		return nil
	}
	min, err := u.tiers.FindByID(ctx, *r.MinTierID)
	if err != nil {
		return err
	}
	have, err := u.tiers.FindByID(ctx, m.TierID)
	if err != nil {
		return err
	}
	if have.SortOrder < min.SortOrder {
		return domainreward.ErrTierTooLow
	}
	return nil
}

// Redeem deducts the points and books one redemption. Like coupon claims, the
// checks that read through their own connection (tier, redemption guard) run
// before the transaction; inside it the reward row is locked, window, limit and
// remaining quota are re-checked, points are spent (the ledger re-reads the
// balance under the member row lock) and the count moves. Stock is not
// reserved in the ERP: it is deducted when the reward ships.
func (u *usecase) Redeem(ctx context.Context, req RedeemRequest) (*domainreward.Redemption, error) {
	if req.MemberID == 0 || req.RewardID == 0 {
		return nil, domain.NewValidationError("rewardId", "member and reward are required")
	}
	if err := req.Address.validate(); err != nil {
		return nil, err
	}
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	if req.IdempotencyKey != "" {
		if !validKey(req.IdempotencyKey) {
			return nil, domain.NewValidationError("idempotencyKey", "must be 1-64 letters, digits, - or _")
		}
		if prev, err := u.rewards.FindRedemptionByKey(ctx, req.MemberID, req.IdempotencyKey); err == nil {
			if prev.RewardID != req.RewardID {
				return nil, domain.NewValidationError("idempotencyKey", "was used for another reward")
			}
			return prev, nil
		} else if !errors.Is(err, domain.ErrNotFound) {
			return nil, err
		}
	}
	at := req.At
	if at.IsZero() {
		at = u.now()
	}

	pre, err := u.rewards.FindByID(ctx, req.RewardID)
	if err != nil {
		return nil, err
	}
	if err := u.open(*pre, at); err != nil {
		return nil, err
	}
	member, err := u.members.FindByID(ctx, req.MemberID)
	if err != nil {
		return nil, err
	}
	if member.Status != domainmember.StatusActive {
		return nil, domain.ErrMemberNotAllowed
	}
	if err := u.checkTier(ctx, *pre, *member); err != nil {
		return nil, err
	}
	if err := u.points.Check(ctx, member, pre.PointsCost, at); err != nil {
		return nil, err
	}

	var out *domainreward.Redemption
	err = u.uow.Do(ctx, func(ctx context.Context, r domainreward.TxRepos) error {
		rw, err := r.Rewards.LockByID(ctx, req.RewardID)
		if err != nil {
			return err
		}
		if err := u.open(*rw, at); err != nil {
			return err
		}
		cache, err := u.cacheFor(ctx, r.Rewards, *rw)
		if err != nil {
			return err
		}
		if RemainingQuota(*rw, cache) < 1 {
			return domainreward.ErrOutOfStock
		}
		if rw.LimitPerMember != nil {
			held, err := r.Rewards.CountActiveRedemptions(ctx, rw.ID, req.MemberID)
			if err != nil {
				return err
			}
			if held >= int64(*rw.LimitPerMember) {
				return domainreward.ErrLimitReached
			}
		}

		a := req.Address
		var key *string
		if req.IdempotencyKey != "" {
			k := req.IdempotencyKey
			key = &k
		}
		rd := &domainreward.Redemption{
			IdempotencyKey: key,
			// Placeholder until the id is known; replaced in this transaction.
			// Random, not time-based: the clock can repeat for two redemptions.
			RedemptionNo: placeholderNo(),
			MemberID:     req.MemberID, RewardID: rw.ID, Qty: rw.QtyPerRedemption, Points: rw.PointsCost,
			Status:   domainreward.RedemptionPending,
			ShipName: strings.TrimSpace(a.Name), ShipPhone: strings.TrimSpace(a.Phone),
			ShipAddress: strings.TrimSpace(a.Address), ShipProvince: strings.TrimSpace(a.Province),
			ShipPostcode: strings.TrimSpace(a.Postcode), MemberNote: strings.TrimSpace(a.Note),
			RewardName: rw.Name, SKUCode: rw.SKUCodeSnapshot, ERPSKUID: rw.ERPSKUID, WarehouseID: rw.ERPWarehouseID,
		}
		if err := r.Rewards.CreateRedemption(ctx, rd); err != nil {
			return err
		}
		rd.RedemptionNo = fmt.Sprintf("RD%s-%06d", at.In(timeutil.Bangkok()).Format("0601"), rd.ID)

		entry, err := u.points.SpendInTx(ctx, r.Points, usecasepoints.SpendRequest{
			MemberID: req.MemberID, Points: rw.PointsCost, SourceType: domainpoints.SourceRedemption,
			SourceID: rd.RedemptionNo, Note: "แลกของรางวัล " + rw.Name, At: at,
		})
		if err != nil {
			return err
		}
		rd.LedgerID = &entry.ID
		if err := r.Rewards.UpdateRedemption(ctx, rd); err != nil {
			return err
		}
		rw.RedeemedCount++
		if err := r.Rewards.Update(ctx, rw); err != nil {
			return err
		}
		out = rd
		return nil
	})
	if err != nil {
		// Two taps with the same key raced: the loser returns the winner's row.
		if req.IdempotencyKey != "" && errors.Is(err, domain.ErrConflict) {
			if prev, ferr := u.rewards.FindRedemptionByKey(ctx, req.MemberID, req.IdempotencyKey); ferr == nil {
				return prev, nil
			}
		}
		return nil, err
	}
	if u.notify != nil {
		if err := u.notify.RedemptionCreated(ctx, out.MemberID, out.RedemptionNo, out.RewardName, out.Points); err != nil {
			log.Printf("[WARN] LINE notify redemption %s: %v", out.RedemptionNo, err)
		}
	}
	return out, nil
}

func (u *usecase) Redemptions(ctx context.Context, q domainreward.RedemptionQuery) ([]domainreward.RedemptionRow, int64, error) {
	if q.Status != "" && !validRedemptionStatus(q.Status) {
		return nil, 0, domain.NewValidationError("status", "unknown redemption status")
	}
	if q.From != nil && q.To != nil && !q.From.Before(*q.To) {
		return nil, 0, domain.NewValidationError("dateFrom", "must be before dateTo")
	}
	q.Search = strings.TrimSpace(q.Search)
	if q.Page < 1 {
		q.Page = 1
	}
	if q.Limit == 0 {
		q.Limit = defaultLimit
	}
	if q.Limit > maxLimit && q.Limit != exportLimit {
		q.Limit = maxLimit
	}
	return u.rewards.FindRedemptions(ctx, q)
}

// exportLimit is the row cap of one redemption export (format=xlsx).
const exportLimit = 50000

// ExportLimit exposes the export cap to the delivery layer.
func ExportLimit() int { return exportLimit }

func validRedemptionStatus(s string) bool {
	switch s {
	case domainreward.RedemptionPending, domainreward.RedemptionApproved, domainreward.RedemptionPacking,
		domainreward.RedemptionShipped, domainreward.RedemptionDelivered, domainreward.RedemptionCancelled:
		return true
	}
	return false
}

func (u *usecase) Redemption(ctx context.Context, id uint) (*domainreward.Redemption, error) {
	return u.rewards.FindRedemption(ctx, id)
}

// move changes status from one of `from` to `to` under a row lock.
func (u *usecase) move(ctx context.Context, actor domain.Actor, id uint, to string, from []string,
	edit func(*domainreward.Redemption, time.Time)) (*domainreward.Redemption, error) {
	var out *domainreward.Redemption
	err := u.uow.Do(ctx, func(ctx context.Context, r domainreward.TxRepos) error {
		rd, err := r.Rewards.LockRedemption(ctx, id)
		if err != nil {
			return err
		}
		ok := false
		for _, s := range from {
			if rd.Status == s {
				ok = true
			}
		}
		if !ok {
			return domainreward.ErrInvalidStatus
		}
		now := u.now()
		rd.Status = to
		rd.LastActorID, rd.LastActorEmail = actor.ERPUserID, actor.Email
		if edit != nil {
			edit(rd, now)
		}
		if err := r.Rewards.UpdateRedemption(ctx, rd); err != nil {
			return err
		}
		out = rd
		return nil
	})
	return out, err
}

func (u *usecase) Approve(ctx context.Context, actor domain.Actor, id uint) (*domainreward.Redemption, error) {
	return u.move(ctx, actor, id, domainreward.RedemptionApproved, []string{domainreward.RedemptionPending},
		func(rd *domainreward.Redemption, now time.Time) { rd.ApprovedAt = &now })
}

func (u *usecase) Pack(ctx context.Context, actor domain.Actor, id uint) (*domainreward.Redemption, error) {
	return u.move(ctx, actor, id, domainreward.RedemptionPacking, []string{domainreward.RedemptionApproved},
		func(rd *domainreward.Redemption, now time.Time) { rd.PackedAt = &now })
}

func (u *usecase) Deliver(ctx context.Context, actor domain.Actor, id uint) (*domainreward.Redemption, error) {
	return u.move(ctx, actor, id, domainreward.RedemptionDelivered, []string{domainreward.RedemptionShipped},
		func(rd *domainreward.Redemption, now time.Time) { rd.DeliveredAt = &now })
}

// Ship deducts the stock in the ERP first, then marks the redemption shipped.
// There is no transaction across both systems: if the CRM update fails after
// the ERP deducted, a retry calls the ERP again with the same idempotency key
// (the redemption number) and gets the same reference without a second
// deduction. Short ERP stock keeps the redemption in packing.
func (u *usecase) Ship(ctx context.Context, actor domain.Actor, id uint, trackingNo string) (*domainreward.Redemption, error) {
	trackingNo = strings.TrimSpace(trackingNo)
	if trackingNo == "" || utf8.RuneCountInString(trackingNo) > 100 {
		return nil, domain.NewValidationError("trackingNo", "is required (100 characters max)")
	}
	rd, err := u.rewards.FindRedemption(ctx, id)
	if err != nil {
		return nil, err
	}
	if rd.Status != domainreward.RedemptionPacking {
		return nil, domainreward.ErrInvalidStatus
	}
	ref := rd.StockIssueRef
	if rd.ERPSKUID != nil && ref == "" {
		if u.erp == nil {
			return nil, domainreward.ErrERPUnavailable
		}
		ref, err = u.erp.IssueGoods(ctx, domainreward.GoodsIssue{
			IdempotencyKey: rd.RedemptionNo, WarehouseID: rd.WarehouseID, SKUID: *rd.ERPSKUID, Qty: rd.Qty,
			Reference: rd.RedemptionNo, Note: "CRM reward " + rd.RewardName,
		})
		if err != nil {
			return nil, err
		}
	}
	shipped, err := u.move(ctx, actor, id, domainreward.RedemptionShipped, []string{domainreward.RedemptionPacking},
		func(rd *domainreward.Redemption, now time.Time) {
			rd.ShippedAt, rd.TrackingNo, rd.StockIssueRef = &now, trackingNo, ref
		})
	if err == nil && u.notify != nil {
		if nerr := u.notify.RedemptionShipped(ctx, shipped.MemberID, shipped.RedemptionNo, shipped.RewardName, shipped.TrackingNo); nerr != nil {
			log.Printf("[WARN] LINE notify shipped %s: %v", shipped.RedemptionNo, nerr)
		}
	}
	return shipped, err
}

// Cancel refunds the points and frees the quota. After shipping a redemption
// cannot be cancelled (that is a product return).
func (u *usecase) Cancel(ctx context.Context, actor domain.Actor, id uint, reason string) (*domainreward.Redemption, error) {
	reason = strings.TrimSpace(reason)
	if n := utf8.RuneCountInString(reason); n < 3 || n > 500 {
		return nil, domain.NewValidationError("reason", "is required (3–500 characters)")
	}
	var out *domainreward.Redemption
	err := u.uow.Do(ctx, func(ctx context.Context, r domainreward.TxRepos) error {
		rd, err := r.Rewards.LockRedemption(ctx, id)
		if err != nil {
			return err
		}
		switch rd.Status {
		case domainreward.RedemptionPending, domainreward.RedemptionApproved, domainreward.RedemptionPacking:
		default:
			return domainreward.ErrInvalidStatus
		}
		now := u.now()
		rd.Status, rd.CancelledAt, rd.CancelReason = domainreward.RedemptionCancelled, &now, reason
		rd.LastActorID, rd.LastActorEmail = actor.ERPUserID, actor.Email
		if err := r.Rewards.UpdateRedemption(ctx, rd); err != nil {
			return err
		}
		rw, err := r.Rewards.LockByID(ctx, rd.RewardID)
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		if rw != nil && rw.RedeemedCount > 0 {
			rw.RedeemedCount--
			if err := r.Rewards.Update(ctx, rw); err != nil {
				return err
			}
		}
		if rd.Points > 0 {
			if _, err := u.points.RefundInTx(ctx, r.Points, usecasepoints.RefundRequest{
				MemberID: rd.MemberID, Points: rd.Points, SourceType: domainpoints.SourceRedemption,
				SourceID: rd.RedemptionNo, Note: "ยกเลิกการแลก: " + reason, At: now,
			}); err != nil {
				return err
			}
		}
		out = rd
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// placeholderNo is a unique temporary redemption_no (TMP + 16 hex chars).
func placeholderNo() string {
	var buf [8]byte
	_, _ = rand.Read(buf[:]) // crypto/rand never fails on supported platforms
	return "TMP" + hex.EncodeToString(buf[:])
}
