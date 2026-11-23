// Package membercard builds the LINE member card (GET /me): who the member
// is, their tier, points, lifetime spend, how much more to spend for the next
// tier, and points expiring soon. Every number is computed here, never in the
// LIFF app.
package membercard

import (
	"context"
	"math"
	"time"

	"crm-core/domain"
	domainloyalty "crm-core/domain/loyalty"
	domainmember "crm-core/domain/member"
	domainmembercard "crm-core/domain/membercard"
)

// ExpiringWindow is how far ahead "points expiring soon" looks.
const ExpiringWindow = 30 * 24 * time.Hour

// Card is the member card.
type Card struct {
	Member   domainmember.Member
	Tier     domainloyalty.Tier
	NextTier *domainloyalty.Tier // nil at the top tier
	// SpendToNext is the baht left to reach NextTier (0 at the top tier).
	SpendToNext float64
	// Progress is 0..1 between the current tier threshold and the next one.
	Progress         float64
	ExpiringPoints   int64
	ExpiringEarliest *time.Time
}

// Usecase reads the member card.
type Usecase interface {
	Card(ctx context.Context, memberID uint) (*Card, error)
	TierHistory(ctx context.Context, memberID uint) ([]domainmembercard.TierChange, error)
}

type usecase struct {
	members domainmember.IdentityRepository
	tiers   domainloyalty.TierRepository
	read    domainmembercard.Repository
	now     func() time.Time
}

// New builds the member card usecase.
func New(members domainmember.IdentityRepository, tiers domainloyalty.TierRepository, read domainmembercard.Repository) Usecase {
	return &usecase{members: members, tiers: tiers, read: read, now: time.Now}
}

func (u *usecase) Card(ctx context.Context, memberID uint) (*Card, error) {
	m, err := u.members.FindByID(ctx, memberID)
	if err != nil {
		return nil, err
	}
	if m.Status != domainmember.StatusActive || m.DeletedAt != nil {
		return nil, domain.ErrMemberNotAllowed
	}
	tiers, err := u.tiers.FindAll(ctx)
	if err != nil {
		return nil, err
	}
	card := &Card{Member: *m}
	found := false
	for _, t := range tiers {
		if t.ID == m.TierID {
			card.Tier = t
			found = true
		}
	}
	if !found {
		return nil, domain.ErrNotFound
	}
	next := NextTier(tiers, card.Tier)
	if next != nil {
		card.NextTier = next
		card.SpendToNext = math.Max(0, round2(next.MinSpend-m.LifetimeSpend))
		span := next.MinSpend - card.Tier.MinSpend
		if span > 0 {
			card.Progress = math.Min(1, math.Max(0, (m.LifetimeSpend-card.Tier.MinSpend)/span))
		}
	} else {
		card.Progress = 1
	}

	now := u.now()
	exp, err := u.read.ExpiringPoints(ctx, m.ID, now, now.Add(ExpiringWindow))
	if err != nil {
		return nil, err
	}
	card.ExpiringPoints = exp.Points
	card.ExpiringEarliest = exp.EarliestAt
	return card, nil
}

// NextTier is the active tier with the smallest MinSpend above the current one.
func NextTier(tiers []domainloyalty.Tier, current domainloyalty.Tier) *domainloyalty.Tier {
	var next *domainloyalty.Tier
	for i := range tiers {
		t := tiers[i]
		if !t.IsActive || t.ID == current.ID || t.MinSpend <= current.MinSpend {
			continue
		}
		if next == nil || t.MinSpend < next.MinSpend {
			next = &t
		}
	}
	return next
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

func (u *usecase) TierHistory(ctx context.Context, memberID uint) ([]domainmembercard.TierChange, error) {
	m, err := u.members.FindByID(ctx, memberID)
	if err != nil {
		return nil, err
	}
	if m.Status != domainmember.StatusActive || m.DeletedAt != nil {
		return nil, domain.ErrMemberNotAllowed
	}
	return u.read.TierHistory(ctx, memberID)
}
