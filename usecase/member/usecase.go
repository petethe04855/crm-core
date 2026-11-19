// Package member implements read and management workflows for CRM members
// used by the backoffice.
package member

import (
	"context"
	"errors"
	"strings"

	"crm-core/domain"
	domainloyalty "crm-core/domain/loyalty"
	domainmember "crm-core/domain/member"
)

const (
	defaultLimit = 20
	maxLimit     = 100
)

// ListItem is a member row enriched with its tier.
type ListItem struct {
	Member   domainmember.Member
	TierCode string
	TierName string
}

// Detail is the member detail view: member, tier and the linked LINE account.
type Detail struct {
	Member domainmember.Member
	Tier   *domainloyalty.Tier
	Line   *domainmember.MemberLineAccount
	Tags   []domainmember.MemberTag
}

// Usecase lists, reads and manages members.
type Usecase interface {
	List(ctx context.Context, q domainmember.Query) ([]ListItem, int64, error)
	Get(ctx context.Context, id uint) (*Detail, error)
	SetStatus(ctx context.Context, actor domain.Actor, id uint, status string) (*domainmember.Member, error)
	UnlinkLine(ctx context.Context, actor domain.Actor, id uint) error
	Tags(ctx context.Context, id uint) ([]domainmember.MemberTag, error)
	AddTag(ctx context.Context, actor domain.Actor, id uint, raw string) ([]domainmember.MemberTag, error)
	RemoveTag(ctx context.Context, actor domain.Actor, id uint, raw string) ([]domainmember.MemberTag, error)
	Notes(ctx context.Context, id uint, page, limit int) ([]domainmember.MemberNote, int64, error)
	AddNote(ctx context.Context, actor domain.Actor, id uint, body string) (*domainmember.MemberNote, error)
}

type usecase struct {
	members domainmember.Repository
	tiers   domainloyalty.TierRepository
}

// New builds the member usecase.
func New(members domainmember.Repository, tiers domainloyalty.TierRepository) Usecase {
	return &usecase{members: members, tiers: tiers}
}

var validStatuses = map[string]bool{
	domainmember.StatusActive:    true,
	domainmember.StatusSuspended: true,
	domainmember.StatusDeleted:   true,
}

// NormalizeQuery applies paging defaults and validates filters.
func NormalizeQuery(q domainmember.Query) (domainmember.Query, error) {
	q.Search = strings.TrimSpace(q.Search)
	q.Status = strings.ToLower(strings.TrimSpace(q.Status))
	if q.Page < 1 {
		q.Page = 1
	}
	if q.Limit < 1 {
		q.Limit = defaultLimit
	}
	if q.Limit > maxLimit {
		q.Limit = maxLimit
	}
	if q.Status != "" && !validStatuses[q.Status] {
		return q, domain.NewValidationError("status", "must be one of active, suspended, deleted")
	}
	if q.RegisteredFrom != nil && q.RegisteredTo != nil && !q.RegisteredFrom.Before(*q.RegisteredTo) {
		return q, domain.NewValidationError("registeredFrom", "must be before registeredTo")
	}
	return q, nil
}

func (u *usecase) List(ctx context.Context, q domainmember.Query) ([]ListItem, int64, error) {
	q, err := NormalizeQuery(q)
	if err != nil {
		return nil, 0, err
	}
	members, total, err := u.members.FindAll(ctx, q)
	if err != nil {
		return nil, 0, err
	}
	tiers, err := u.tiers.FindAll(ctx)
	if err != nil {
		return nil, 0, err
	}
	byID := make(map[uint]domainloyalty.Tier, len(tiers))
	for _, t := range tiers {
		byID[t.ID] = t
	}
	items := make([]ListItem, 0, len(members))
	for _, m := range members {
		item := ListItem{Member: m}
		if t, ok := byID[m.TierID]; ok {
			item.TierCode = t.Code
			item.TierName = t.Name
		}
		items = append(items, item)
	}
	return items, total, nil
}

func (u *usecase) Get(ctx context.Context, id uint) (*Detail, error) {
	if id == 0 {
		return nil, domain.NewValidationError("id", "is required")
	}
	m, err := u.members.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	detail := &Detail{Member: *m}

	tier, err := u.tiers.FindByID(ctx, m.TierID)
	switch {
	case err == nil:
		detail.Tier = tier
	case !errors.Is(err, domain.ErrNotFound):
		return nil, err
	}

	line, err := u.members.FindActiveLineAccount(ctx, m.ID)
	switch {
	case err == nil:
		detail.Line = line
	case !errors.Is(err, domain.ErrNotFound):
		return nil, err
	}

	if detail.Tags, err = u.members.FindTags(ctx, m.ID); err != nil {
		return nil, err
	}
	return detail, nil
}
