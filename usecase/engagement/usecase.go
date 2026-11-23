// Package engagement runs the daily member jobs of L7 (backoffice worker):
// reminders before points expire, and the birthday bonus + greeting.
package engagement

import (
	"context"
	"errors"
	"log"
	"strconv"
	"time"

	"crm-core/domain"
	domainearnrule "crm-core/domain/earnrule"
	domainmember "crm-core/domain/member"
	domainpoints "crm-core/domain/points"
	"crm-core/timeutil"
)

// ReminderWindow is how far ahead the expiry reminder looks.
const ReminderWindow = 7 * 24 * time.Hour

// Expiring is one member's points expiring inside the window.
type Expiring struct {
	MemberID uint
	Points   int64
	Earliest time.Time
}

// Repository is the read model of the jobs.
type Repository interface {
	ExpiringByMember(ctx context.Context, from, until time.Time) ([]Expiring, error)
	// BirthdayMembers lists active members born on month/day (and on Feb 29
	// when includeLeapDay, for 28 Feb in non-leap years).
	BirthdayMembers(ctx context.Context, month time.Month, day int, includeLeapDay bool) ([]domainmember.Member, error)
}

// Notifier is linemsg.Notifier's job-facing part.
type Notifier interface {
	ExpiringSoon(ctx context.Context, memberID uint, points int64, earliest time.Time) error
	Birthday(ctx context.Context, memberID uint, firstName string, bonus int64, year int) error
}

// ExpiryPolicy gives the point lifetime in months (0 = never expires).
type ExpiryPolicy func(ctx context.Context) (int, error)

// Jobs runs the engagement jobs.
type Jobs struct {
	repo     Repository
	ledger   domainpoints.Repository
	rules    domainearnrule.Repository
	expiry   ExpiryPolicy
	notifier Notifier
}

// New builds the jobs. notifier may be nil (bonus without message).
func New(repo Repository, ledger domainpoints.Repository, rules domainearnrule.Repository, expiry ExpiryPolicy, notifier Notifier) *Jobs {
	return &Jobs{repo: repo, ledger: ledger, rules: rules, expiry: expiry, notifier: notifier}
}

// RemindExpiring queues one reminder per member with points expiring within
// ReminderWindow. The notifier dedupes by earliest expiry date, so a daily run
// does not repeat itself. Returns how many members were reminded.
func (j *Jobs) RemindExpiring(ctx context.Context, now time.Time) (int, error) {
	if j.notifier == nil {
		return 0, nil
	}
	rows, err := j.repo.ExpiringByMember(ctx, now, now.Add(ReminderWindow))
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range rows {
		if err := j.notifier.ExpiringSoon(ctx, r.MemberID, r.Points, r.Earliest); err != nil {
			log.Printf("[WARN] expiry reminder for member %d: %v", r.MemberID, err)
			continue
		}
		n++
	}
	return n, nil
}

func isLeap(y int) bool { return y%4 == 0 && (y%100 != 0 || y%400 == 0) }

// Birthdays credits the active birthday rule (bonus_fixed, trigger birthday)
// once per member per year and greets the member. Members born on 29 Feb
// celebrate on 28 Feb in non-leap years. Returns how many were credited.
func (j *Jobs) Birthdays(ctx context.Context, now time.Time) (int, error) {
	today := now.In(timeutil.Bangkok())
	leapDay := today.Month() == time.February && today.Day() == 28 && !isLeap(today.Year())
	members, err := j.repo.BirthdayMembers(ctx, today.Month(), today.Day(), leapDay)
	if err != nil {
		return 0, err
	}
	bonus, ruleIDs, err := j.birthdayRule(ctx, now)
	if err != nil {
		return 0, err
	}
	var expiresAt *time.Time
	if bonus > 0 && j.expiry != nil {
		months, err := j.expiry(ctx)
		if err != nil {
			return 0, err
		}
		if months > 0 {
			t := now.AddDate(0, months, 0)
			expiresAt = &t
		}
	}
	credited := 0
	for _, m := range members {
		got := int64(0)
		if bonus > 0 {
			_, err := j.ledger.Earn(ctx, domainpoints.EarnCommand{
				MemberID: m.ID, Points: bonus, SourceType: domainpoints.SourceBonus,
				SourceID: "birthday:" + strconv.FormatUint(uint64(m.ID), 10) + ":" + strconv.Itoa(today.Year()),
				Note:     "ของขวัญวันเกิด", RuleIDs: ruleIDs, ExpiresAt: expiresAt,
			})
			switch {
			case errors.Is(err, domain.ErrConflict):
				continue // already credited this year (job ran twice)
			case err != nil:
				log.Printf("[WARN] birthday bonus for member %d: %v", m.ID, err)
				continue
			}
			got = bonus
			credited++
		}
		if j.notifier != nil {
			if err := j.notifier.Birthday(ctx, m.ID, m.FirstName, got, today.Year()); err != nil {
				log.Printf("[WARN] birthday message for member %d: %v", m.ID, err)
			}
		}
	}
	return credited, nil
}

func (j *Jobs) birthdayRule(ctx context.Context, at time.Time) (int64, []uint, error) {
	if j.rules == nil {
		return 0, nil, nil
	}
	rules, err := j.rules.FindActive(ctx, at)
	if err != nil {
		return 0, nil, err
	}
	for _, r := range rules {
		if r.Type == domainearnrule.TypeBonusFixed && r.Trigger == domainearnrule.TriggerBirthday && r.Points > 0 {
			return r.Points, []uint{r.ID}, nil
		}
	}
	return 0, nil, nil
}
