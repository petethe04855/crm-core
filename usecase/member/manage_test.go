package member

import (
	"context"
	"strings"
	"testing"

	"crm-core/domain"
	domainmember "crm-core/domain/member"

	"github.com/stretchr/testify/require"
)

func newFakeMember(id uint) *fakeMembers {
	return &fakeMembers{
		byID: map[uint]domainmember.Member{id: {ID: id, Status: domainmember.StatusActive}},
	}
}

func TestSetStatus(t *testing.T) {
	ctx := context.Background()
	actor := domain.Actor{ERPUserID: 3, Email: "a@example.com"}

	members := newFakeMember(1)
	uc := New(members, &fakeTiers{})

	m, err := uc.SetStatus(ctx, actor, 1, " Suspended ")
	require.NoError(t, err)
	require.Equal(t, domainmember.StatusSuspended, m.Status)

	_, err = uc.SetStatus(ctx, actor, 1, "deleted")
	require.ErrorIs(t, err, domain.ErrValidation, "the API never soft-deletes members")

	_, err = uc.SetStatus(ctx, actor, 99, "active")
	require.ErrorIs(t, err, domain.ErrNotFound)
}

func TestUnlinkLine(t *testing.T) {
	ctx := context.Background()
	actor := domain.Actor{ERPUserID: 3}

	members := newFakeMember(1)
	members.line = &domainmember.MemberLineAccount{LineUserID: "U1"}
	uc := New(members, &fakeTiers{})

	require.NoError(t, uc.UnlinkLine(ctx, actor, 1))
	require.Nil(t, members.line)

	require.ErrorIs(t, uc.UnlinkLine(ctx, actor, 1), domain.ErrNotFound, "second unlink finds no active account")
}

func TestNormalizeTag(t *testing.T) {
	got, err := NormalizeTag("  VIP   ลูกค้า ")
	require.NoError(t, err)
	require.Equal(t, "vip ลูกค้า", got)

	_, err = NormalizeTag("   ")
	require.ErrorIs(t, err, domain.ErrValidation)

	_, err = NormalizeTag(strings.Repeat("ก", 41))
	require.ErrorIs(t, err, domain.ErrValidation)
}

func TestAddAndRemoveTag(t *testing.T) {
	ctx := context.Background()
	members := newFakeMember(1)
	uc := New(members, &fakeTiers{})
	actor := domain.Actor{ERPUserID: 9}

	tags, err := uc.AddTag(ctx, actor, 1, "VIP")
	require.NoError(t, err)
	require.Len(t, tags, 1)
	require.Equal(t, "vip", tags[0].Tag)
	require.EqualValues(t, 9, tags[0].CreatedBy)

	_, err = uc.AddTag(ctx, actor, 1, "vip")
	require.ErrorIs(t, err, domain.ErrConflict)

	tags, err = uc.RemoveTag(ctx, actor, 1, "vip")
	require.NoError(t, err)
	require.Empty(t, tags)

	_, err = uc.RemoveTag(ctx, actor, 1, "vip")
	require.ErrorIs(t, err, domain.ErrNotFound)
}

func TestAddNote(t *testing.T) {
	ctx := context.Background()
	members := newFakeMember(1)
	uc := New(members, &fakeTiers{})
	actor := domain.Actor{ERPUserID: 5, Email: "sales@example.com"}

	note, err := uc.AddNote(ctx, actor, 1, "  ลูกค้าโทรมาถามแต้ม  ")
	require.NoError(t, err)
	require.Equal(t, "ลูกค้าโทรมาถามแต้ม", note.Body)
	require.EqualValues(t, 5, note.CreatedBy)
	require.Equal(t, "sales@example.com", note.CreatedByEmail)

	_, err = uc.AddNote(ctx, actor, 1, "   ")
	require.ErrorIs(t, err, domain.ErrValidation)

	_, err = uc.AddNote(ctx, actor, 99, "ทดสอบ")
	require.ErrorIs(t, err, domain.ErrNotFound)

	notes, total, err := uc.Notes(ctx, 1, 0, 0)
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Len(t, notes, 1)
}
