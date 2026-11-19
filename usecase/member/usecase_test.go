package member

import (
	"context"
	"testing"
	"time"

	"crm-core/domain"
	domainloyalty "crm-core/domain/loyalty"
	domainmember "crm-core/domain/member"

	"github.com/stretchr/testify/require"
)

type fakeMembers struct {
	list    []domainmember.Member
	byID    map[uint]domainmember.Member
	line    *domainmember.MemberLineAccount
	gotArgs domainmember.Query
	tags    map[uint][]domainmember.MemberTag
	notes   map[uint][]domainmember.MemberNote
}

func (f *fakeMembers) FindAll(_ context.Context, q domainmember.Query) ([]domainmember.Member, int64, error) {
	f.gotArgs = q
	return f.list, int64(len(f.list)), nil
}

func (f *fakeMembers) FindByID(_ context.Context, id uint) (*domainmember.Member, error) {
	m, ok := f.byID[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return &m, nil
}

func (f *fakeMembers) FindActiveLineAccount(context.Context, uint) (*domainmember.MemberLineAccount, error) {
	if f.line == nil {
		return nil, domain.ErrNotFound
	}
	return f.line, nil
}

func (f *fakeMembers) UpdateStatus(_ context.Context, id uint, status string) error {
	m, ok := f.byID[id]
	if !ok {
		return domain.ErrNotFound
	}
	m.Status = status
	f.byID[id] = m
	return nil
}

func (f *fakeMembers) UnlinkActiveLineAccount(_ context.Context, _ uint) error {
	if f.line == nil {
		return domain.ErrNotFound
	}
	f.line = nil
	return nil
}

func (f *fakeMembers) FindTags(_ context.Context, memberID uint) ([]domainmember.MemberTag, error) {
	return f.tags[memberID], nil
}

func (f *fakeMembers) AddTag(_ context.Context, tag *domainmember.MemberTag) error {
	for _, existing := range f.tags[tag.MemberID] {
		if existing.Tag == tag.Tag {
			return domain.ErrConflict
		}
	}
	if f.tags == nil {
		f.tags = map[uint][]domainmember.MemberTag{}
	}
	f.tags[tag.MemberID] = append(f.tags[tag.MemberID], *tag)
	return nil
}

func (f *fakeMembers) RemoveTag(_ context.Context, memberID uint, tag string) error {
	tags := f.tags[memberID]
	for i, existing := range tags {
		if existing.Tag == tag {
			f.tags[memberID] = append(tags[:i], tags[i+1:]...)
			return nil
		}
	}
	return domain.ErrNotFound
}

func (f *fakeMembers) FindNotes(_ context.Context, memberID uint, page, limit int) ([]domainmember.MemberNote, int64, error) {
	notes := f.notes[memberID]
	start := (page - 1) * limit
	if start >= len(notes) {
		return nil, int64(len(notes)), nil
	}
	end := start + limit
	if end > len(notes) {
		end = len(notes)
	}
	return notes[start:end], int64(len(notes)), nil
}

func (f *fakeMembers) AddNote(_ context.Context, note *domainmember.MemberNote) error {
	if f.notes == nil {
		f.notes = map[uint][]domainmember.MemberNote{}
	}
	// Newest first, matching the repository order.
	f.notes[note.MemberID] = append([]domainmember.MemberNote{*note}, f.notes[note.MemberID]...)
	return nil
}

type fakeTiers struct{ tiers []domainloyalty.Tier }

func (f *fakeTiers) FindAll(context.Context) ([]domainloyalty.Tier, error) { return f.tiers, nil }

func (f *fakeTiers) FindByID(_ context.Context, id uint) (*domainloyalty.Tier, error) {
	for _, t := range f.tiers {
		if t.ID == id {
			return &t, nil
		}
	}
	return nil, domain.ErrNotFound
}

func TestNormalizeQuery(t *testing.T) {
	q, err := NormalizeQuery(domainmember.Query{Search: "  nop ", Status: " Active ", Page: 0, Limit: 500})
	require.NoError(t, err)
	require.Equal(t, "nop", q.Search)
	require.Equal(t, "active", q.Status)
	require.Equal(t, 1, q.Page)
	require.Equal(t, maxLimit, q.Limit)

	q, err = NormalizeQuery(domainmember.Query{})
	require.NoError(t, err)
	require.Equal(t, defaultLimit, q.Limit)

	_, err = NormalizeQuery(domainmember.Query{Status: "vip"})
	require.ErrorIs(t, err, domain.ErrValidation)

	from := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	to := from.Add(-time.Hour)
	_, err = NormalizeQuery(domainmember.Query{RegisteredFrom: &from, RegisteredTo: &to})
	require.ErrorIs(t, err, domain.ErrValidation)
}

func TestListEnrichesTier(t *testing.T) {
	members := &fakeMembers{list: []domainmember.Member{
		{ID: 1, TierID: 10},
		{ID: 2, TierID: 99}, // tier missing: row still returned, tier empty
	}}
	tiers := &fakeTiers{tiers: []domainloyalty.Tier{{ID: 10, Code: "bronze", Name: "Bronze"}}}

	items, total, err := New(members, tiers).List(context.Background(), domainmember.Query{Limit: 5})
	require.NoError(t, err)
	require.EqualValues(t, 2, total)
	require.Equal(t, "Bronze", items[0].TierName)
	require.Equal(t, "bronze", items[0].TierCode)
	require.Empty(t, items[1].TierName)
	require.Equal(t, 1, members.gotArgs.Page, "usecase must pass normalized query")
	require.Equal(t, 5, members.gotArgs.Limit)
}

func TestListRejectsInvalidQuery(t *testing.T) {
	_, _, err := New(&fakeMembers{}, &fakeTiers{}).List(context.Background(), domainmember.Query{Status: "bad"})
	require.ErrorIs(t, err, domain.ErrValidation)
}

func TestGet(t *testing.T) {
	line := &domainmember.MemberLineAccount{LineUserID: "U1"}
	members := &fakeMembers{byID: map[uint]domainmember.Member{1: {ID: 1, TierID: 10}}, line: line}
	tiers := &fakeTiers{tiers: []domainloyalty.Tier{{ID: 10, Name: "Bronze"}}}
	uc := New(members, tiers)

	d, err := uc.Get(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, "Bronze", d.Tier.Name)
	require.Equal(t, "U1", d.Line.LineUserID)

	_, err = uc.Get(context.Background(), 2)
	require.ErrorIs(t, err, domain.ErrNotFound)

	_, err = uc.Get(context.Background(), 0)
	require.ErrorIs(t, err, domain.ErrValidation)

	members.line = nil
	d, err = uc.Get(context.Background(), 1)
	require.NoError(t, err)
	require.Nil(t, d.Line, "member without LINE account is still returned")
}
