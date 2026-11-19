package staffrole

import (
	"context"
	"testing"
	"time"

	"crm-core/domain"
	domainstaffrole "crm-core/domain/staffrole"

	"github.com/stretchr/testify/require"
)

type fakeRepo struct {
	byUser map[uint]domainstaffrole.StaffRole
}

func newFakeRepo() *fakeRepo { return &fakeRepo{byUser: map[uint]domainstaffrole.StaffRole{}} }

func (f *fakeRepo) FindAll(context.Context) ([]domainstaffrole.StaffRole, error) {
	out := []domainstaffrole.StaffRole{}
	for _, r := range f.byUser {
		out = append(out, r)
	}
	return out, nil
}

func (f *fakeRepo) FindByERPUserID(_ context.Context, id uint) (*domainstaffrole.StaffRole, error) {
	r, ok := f.byUser[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return &r, nil
}

func (f *fakeRepo) Upsert(_ context.Context, r *domainstaffrole.StaffRole) error {
	f.byUser[r.ERPUserID] = *r
	return nil
}

func (f *fakeRepo) DeleteByERPUserID(_ context.Context, id uint) error {
	if _, ok := f.byUser[id]; !ok {
		return domain.ErrNotFound
	}
	delete(f.byUser, id)
	return nil
}

var (
	owner = domain.Actor{ERPUserID: 1, Roles: []string{"owner"}}
	admin = domain.Actor{ERPUserID: 2, Roles: []string{"sales", "admin"}}
)

func TestGrantRequiresOwner(t *testing.T) {
	uc := New(newFakeRepo())
	_, err := uc.Grant(context.Background(), admin, 5, "x@example.com", "admin")
	require.ErrorIs(t, err, domain.ErrForbidden, "a CRM admin cannot grant roles")
}

func TestGrantValidates(t *testing.T) {
	uc := New(newFakeRepo())
	_, err := uc.Grant(context.Background(), owner, 0, "", "admin")
	require.ErrorIs(t, err, domain.ErrValidation)
	_, err = uc.Grant(context.Background(), owner, 5, "", "owner")
	require.ErrorIs(t, err, domain.ErrValidation, "only CRM roles are grantable")
}

func TestGrantStoresNormalizedGrant(t *testing.T) {
	repo := newFakeRepo()
	u := New(repo).(*usecase)
	fixed := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	u.now = func() time.Time { return fixed }

	g, err := u.Grant(context.Background(), owner, 5, "  Staff@Example.com ", " ADMIN ")
	require.NoError(t, err)
	require.Equal(t, "admin", g.Role)
	require.Equal(t, "staff@example.com", g.Email)
	require.EqualValues(t, 1, g.GrantedBy)
	require.Equal(t, fixed, g.GrantedAt)
	require.Contains(t, repo.byUser, uint(5))
}

func TestRevoke(t *testing.T) {
	repo := newFakeRepo()
	uc := New(repo)
	_, err := uc.Grant(context.Background(), owner, 5, "", "admin")
	require.NoError(t, err)

	require.ErrorIs(t, uc.Revoke(context.Background(), admin, 5), domain.ErrForbidden)
	require.NoError(t, uc.Revoke(context.Background(), owner, 5))
	require.ErrorIs(t, uc.Revoke(context.Background(), owner, 5), domain.ErrNotFound)
}

func TestEffectiveRoles(t *testing.T) {
	repo := newFakeRepo()
	uc := New(repo)
	ctx := context.Background()

	roles, err := uc.EffectiveRoles(ctx, 5, "Sales")
	require.NoError(t, err)
	require.Equal(t, []string{"sales"}, roles)

	_, err = uc.Grant(ctx, owner, 5, "", "admin")
	require.NoError(t, err)
	roles, err = uc.EffectiveRoles(ctx, 5, "sales")
	require.NoError(t, err)
	require.Equal(t, []string{"sales", "admin"}, roles)

	roles, err = uc.EffectiveRoles(ctx, 9, "")
	require.NoError(t, err)
	require.Empty(t, roles)
}
