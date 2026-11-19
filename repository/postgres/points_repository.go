package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"crm-core/domain"
	domainmember "crm-core/domain/member"
	domainpoints "crm-core/domain/points"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type pointsRepository struct {
	db *gorm.DB
}

// NewPointsRepository builds the point ledger repository.
func NewPointsRepository(db *gorm.DB) domainpoints.Repository {
	return &pointsRepository{db: db}
}

// isDuplicate reports a unique constraint violation. GORM's translated error is
// unavailable here (the shared config does not set TranslateError), so the
// driver message is matched instead; SQLite tests and Postgres differ in text.
func isDuplicate(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint") ||
		strings.Contains(msg, "duplicate key") ||
		strings.Contains(msg, "unique constraint failed")
}

func (r *pointsRepository) Adjust(ctx context.Context, cmd domainpoints.AdjustCommand) (*domainpoints.PointLedger, error) {
	var entry *domainpoints.PointLedger
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		m, err := lockMember(tx, cmd.MemberID)
		if err != nil {
			return err
		}

		balance := m.PointBalance + cmd.Points
		if balance < 0 {
			return domain.ErrInsufficientPoints
		}

		row := domainpoints.PointLedger{
			MemberID:       m.ID,
			Type:           domainpoints.TypeAdjust,
			Points:         cmd.Points,
			BalanceAfter:   balance,
			SourceType:     cmd.SourceType,
			SourceID:       cmd.SourceID,
			Note:           cmd.Reason,
			CreatedBy:      cmd.ActorID,
			CreatedByEmail: cmd.ActorEmail,
		}
		if err := tx.Create(&row).Error; err != nil {
			if isDuplicate(err) {
				return domain.ErrConflict
			}
			return err
		}
		if err := tx.Model(&domainmember.Member{}).
			Where("id = ?", m.ID).
			Update("point_balance", balance).Error; err != nil {
			return err
		}
		entry = &row
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entry, nil
}

func (r *pointsRepository) List(ctx context.Context, memberID uint, q domainpoints.Query) ([]domainpoints.PointLedger, int64, error) {
	tx := r.db.WithContext(ctx).Model(&domainpoints.PointLedger{}).Where("member_id = ?", memberID)
	if q.Type != "" {
		tx = tx.Where("type = ?", q.Type)
	}

	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var rows []domainpoints.PointLedger
	err := tx.Order("created_at DESC").Order("id DESC").
		Offset((q.Page - 1) * q.Limit).Limit(q.Limit).
		Find(&rows).Error
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

func (r *pointsRepository) AdjustedAbsBetween(ctx context.Context, actorID uint, from, to time.Time) (int64, error) {
	var sum int64
	err := r.db.WithContext(ctx).
		Model(&domainpoints.PointLedger{}).
		Where("created_by = ? AND type = ? AND created_at >= ? AND created_at < ?",
			actorID, domainpoints.TypeAdjust, from, to).
		Select("COALESCE(SUM(ABS(points)), 0)").
		Scan(&sum).Error
	if err != nil {
		return 0, err
	}
	return sum, nil
}

// lockMember reads the member row for update. SQLite has no row locks; tests run
// single-connection, and Postgres is the production target, so the clause is
// only emitted there.
func lockMember(tx *gorm.DB, id uint) (*domainmember.Member, error) {
	read := tx
	if tx.Dialector.Name() == "postgres" {
		read = read.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var m domainmember.Member
	if err := read.First(&m, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	return &m, nil
}

// writeEntry appends the ledger row and moves the cached balance in one step.
// It reports a retried source as a conflict instead of double-crediting.
// writeEntry inserts row (filling row.ID, which callers use for the lot and the
// returned entry) and stores the new cached balance.
func writeEntry(tx *gorm.DB, member *domainmember.Member, row *domainpoints.PointLedger) error {
	if err := tx.Create(row).Error; err != nil {
		if isDuplicate(err) {
			return domain.ErrConflict
		}
		return err
	}
	return tx.Model(&domainmember.Member{}).
		Where("id = ?", member.ID).
		Update("point_balance", row.BalanceAfter).Error
}

func (r *pointsRepository) Earn(ctx context.Context, cmd domainpoints.EarnCommand) (*domainpoints.PointLedger, error) {
	var entry *domainpoints.PointLedger
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		m, err := lockMember(tx, cmd.MemberID)
		if err != nil {
			return err
		}
		balance := m.PointBalance + cmd.Points
		if balance < 0 {
			return domain.ErrInsufficientPoints
		}

		row := domainpoints.PointLedger{
			MemberID:       m.ID,
			Type:           domainpoints.TypeEarn,
			Points:         cmd.Points,
			BalanceAfter:   balance,
			SourceType:     cmd.SourceType,
			SourceID:       cmd.SourceID,
			RuleIDs:        cmd.RuleIDs,
			Note:           cmd.Note,
			CreatedBy:      cmd.CreatedBy,
			CreatedByEmail: cmd.CreatedByEmail,
		}
		if err := writeEntry(tx, m, &row); err != nil {
			return err
		}
		// A zero-point earn still records that the source was processed (the
		// unique source triple keeps retries idempotent) but opens no lot.
		if cmd.Points > 0 {
			lot := domainpoints.PointLot{
				MemberID:        m.ID,
				LedgerID:        row.ID,
				PointsEarned:    cmd.Points,
				PointsRemaining: cmd.Points,
				ExpiresAt:       cmd.ExpiresAt,
			}
			if err := tx.Create(&lot).Error; err != nil {
				return err
			}
		}
		entry = &row
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entry, nil
}

func (r *pointsRepository) Spend(ctx context.Context, cmd domainpoints.SpendCommand) (*domainpoints.PointLedger, error) {
	var entry *domainpoints.PointLedger
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		m, err := lockMember(tx, cmd.MemberID)
		if err != nil {
			return err
		}
		// Re-check under the row lock: the guard ran before we got here, so two
		// concurrent redemptions may both have passed it.
		if m.PointBalance < cmd.Points {
			return domain.ErrInsufficientPoints
		}
		balance := m.PointBalance - cmd.Points

		row := domainpoints.PointLedger{
			MemberID:       m.ID,
			Type:           domainpoints.TypeRedeem,
			Points:         -cmd.Points,
			BalanceAfter:   balance,
			SourceType:     cmd.SourceType,
			SourceID:       cmd.SourceID,
			Note:           cmd.Note,
			CreatedBy:      cmd.CreatedBy,
			CreatedByEmail: cmd.CreatedByEmail,
		}
		if err := writeEntry(tx, m, &row); err != nil {
			return err
		}
		if err := consumeLots(tx, m.ID, cmd.Points); err != nil {
			return err
		}
		entry = &row
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entry, nil
}

func (r *pointsRepository) Refund(ctx context.Context, cmd domainpoints.RefundCommand) (*domainpoints.PointLedger, error) {
	var entry *domainpoints.PointLedger
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		m, err := lockMember(tx, cmd.MemberID)
		if err != nil {
			return err
		}
		balance := m.PointBalance + cmd.Points
		if balance < 0 {
			return domain.ErrInsufficientPoints
		}

		row := domainpoints.PointLedger{
			MemberID:       m.ID,
			Type:           domainpoints.TypeRefund,
			Points:         cmd.Points,
			BalanceAfter:   balance,
			SourceType:     cmd.SourceType,
			SourceID:       cmd.SourceID,
			Note:           cmd.Note,
			CreatedBy:      cmd.CreatedBy,
			CreatedByEmail: cmd.CreatedByEmail,
		}
		if err := writeEntry(tx, m, &row); err != nil {
			return err
		}
		if cmd.Points > 0 {
			lot := domainpoints.PointLot{
				MemberID:        m.ID,
				LedgerID:        row.ID,
				PointsEarned:    cmd.Points,
				PointsRemaining: cmd.Points,
				ExpiresAt:       cmd.ExpiresAt,
			}
			if err := tx.Create(&lot).Error; err != nil {
				return err
			}
		}
		entry = &row
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entry, nil
}

// consumeLots takes points from the lots that expire soonest. Points credited
// without a lot (manual adjustments) never expire, so running out of lots is
// not an error.
func consumeLots(tx *gorm.DB, memberID uint, amount int64) error {
	remaining := amount
	for remaining > 0 {
		var lots []domainpoints.PointLot
		err := tx.Where("member_id = ? AND points_remaining > 0", memberID).
			Order("CASE WHEN expires_at IS NULL THEN 1 ELSE 0 END").
			Order("expires_at ASC").Order("id ASC").
			Limit(100).Find(&lots).Error
		if err != nil {
			return err
		}
		if len(lots) == 0 {
			return nil
		}
		taken := int64(0)
		for _, lot := range lots {
			if remaining == 0 {
				break
			}
			take := lot.PointsRemaining
			if take > remaining {
				take = remaining
			}
			if take <= 0 {
				continue
			}
			if err := tx.Model(&domainpoints.PointLot{}).
				Where("id = ?", lot.ID).
				Update("points_remaining", gorm.Expr("points_remaining - ?", take)).Error; err != nil {
				return err
			}
			remaining -= take
			taken += take
		}
		if taken == 0 {
			return nil
		}
	}
	return nil
}

func (r *pointsRepository) RedeemCountBetween(ctx context.Context, memberID uint, from, to time.Time) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&domainpoints.PointLedger{}).
		Where("member_id = ? AND type = ? AND created_at >= ? AND created_at < ?",
			memberID, domainpoints.TypeRedeem, from, to).
		Count(&count).Error
	return count, err
}

func (r *pointsRepository) ExpireDue(ctx context.Context, now time.Time) (int, error) {
	var memberIDs []uint
	if err := r.db.WithContext(ctx).
		Model(&domainpoints.PointLot{}).
		Where("points_remaining > 0 AND expires_at IS NOT NULL AND expires_at <= ?", now).
		Distinct().Pluck("member_id", &memberIDs).Error; err != nil {
		return 0, err
	}

	expired := 0
	for _, memberID := range memberIDs {
		err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			m, err := lockMember(tx, memberID)
			if err != nil {
				return err
			}
			var lots []domainpoints.PointLot
			if err := tx.Where(
				"member_id = ? AND points_remaining > 0 AND expires_at IS NOT NULL AND expires_at <= ?",
				memberID, now).
				Order("expires_at ASC").Order("id ASC").
				Find(&lots).Error; err != nil {
				return err
			}

			balance := m.PointBalance
			for _, lot := range lots {
				points := lot.PointsRemaining
				if points > balance {
					// Only reachable if the ledger and lots drifted apart; never
					// take the balance below zero.
					points = balance
				}
				if points <= 0 {
					break
				}
				row := domainpoints.PointLedger{
					MemberID:     m.ID,
					Type:         domainpoints.TypeExpire,
					Points:       -points,
					BalanceAfter: balance - points,
					SourceType:   domainpoints.SourceExpiry,
					SourceID:     fmt.Sprintf("lot:%d", lot.ID),
					Note:         "Points expired",
				}
				if err := tx.Create(&row).Error; err != nil {
					if isDuplicate(err) {
						// A previous run already wrote this lot's entry.
						continue
					}
					return err
				}
				if err := tx.Model(&domainpoints.PointLot{}).
					Where("id = ?", lot.ID).
					Update("points_remaining", lot.PointsRemaining-points).Error; err != nil {
					return err
				}
				balance -= points
				expired++
			}
			if balance != m.PointBalance {
				return tx.Model(&domainmember.Member{}).
					Where("id = ?", m.ID).
					Update("point_balance", balance).Error
			}
			return nil
		})
		if err != nil {
			return expired, err
		}
	}
	return expired, nil
}
