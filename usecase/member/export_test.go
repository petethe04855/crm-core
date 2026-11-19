package member

import (
	"context"
	"testing"

	"crm-core/domain"
	domainloyalty "crm-core/domain/loyalty"
	domainmember "crm-core/domain/member"

	"github.com/stretchr/testify/require"
)

// pagedMembers serves a fixed member set page by page and can report a
// larger total to simulate an oversized export.
type pagedMembers struct {
	fakeMembers
	all       []domainmember.Member
	fakeTotal int64
	pages     int
}

func (p *pagedMembers) FindAll(_ context.Context, q domainmember.Query) ([]domainmember.Member, int64, error) {
	p.pages++
	total := int64(len(p.all))
	if p.fakeTotal > 0 {
		total = p.fakeTotal
	}
	start := (q.Page - 1) * q.Limit
	if start >= len(p.all) {
		return nil, total, nil
	}
	end := start + q.Limit
	if end > len(p.all) {
		end = len(p.all)
	}
	return p.all[start:end], total, nil
}

func makeMembers(n int) []domainmember.Member {
	phone := "+66971166657"
	out := make([]domainmember.Member, n)
	for i := range out {
		out[i] = domainmember.Member{ID: uint(i + 1), MemberCode: "M", Email: "nopharat@gmail.com", PhoneE164: &phone, TierID: 1}
	}
	return out
}

func TestExportMasksForNonOwner(t *testing.T) {
	repo := &pagedMembers{all: makeMembers(2)}
	tiers := &fakeTiers{tiers: []domainloyalty.Tier{{ID: 1, Name: "Bronze"}}}
	ex := NewExporter(repo, tiers)

	rows, err := ex.Export(context.Background(), domain.Actor{Roles: []string{"admin"}}, domainmember.Query{})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, "no******@gmail.com", rows[0].Email)
	require.Equal(t, "+66******657", rows[0].Phone)
	require.Equal(t, "Bronze", rows[0].TierName)

	rows, err = ex.Export(context.Background(), domain.Actor{Roles: []string{"owner"}}, domainmember.Query{})
	require.NoError(t, err)
	require.Equal(t, "nopharat@gmail.com", rows[0].Email, "owner gets unmasked data")
	require.Equal(t, "+66971166657", rows[0].Phone)
}

func TestExportReadsAllPages(t *testing.T) {
	repo := &pagedMembers{all: makeMembers(exportPageSize + 5)}
	rows, err := NewExporter(repo, &fakeTiers{}).Export(context.Background(), domain.Actor{}, domainmember.Query{Limit: 10})
	require.NoError(t, err)
	require.Len(t, rows, exportPageSize+5, "export ignores the UI page size")
	require.Equal(t, 2, repo.pages)
}

func TestExportRejectsOversizedResult(t *testing.T) {
	repo := &pagedMembers{all: makeMembers(1), fakeTotal: ExportMaxRows + 1}
	_, err := NewExporter(repo, &fakeTiers{}).Export(context.Background(), domain.Actor{}, domainmember.Query{})
	require.ErrorIs(t, err, domain.ErrValidation)
}

func TestMaskHelpers(t *testing.T) {
	cases := []struct{ name, got, want string }{
		{"email", domainmember.MaskEmail("nopharat@gmail.com"), "no******@gmail.com"},
		{"two-char local", domainmember.MaskEmail("ab@x.com"), "a*@x.com"},
		{"one-char local", domainmember.MaskEmail("a@x.com"), "*@x.com"},
		{"not an email", domainmember.MaskEmail("noemail"), "n******"},
		{"e164 phone", domainmember.MaskPhone("+66971166657"), "+66******657"},
		{"local phone", domainmember.MaskPhone("0812345678"), "*******678"},
		{"short phone", domainmember.MaskPhone("12"), "**"},
		{"empty phone", domainmember.MaskPhone(""), ""},
	}
	for _, c := range cases {
		require.Equal(t, c.want, c.got, c.name)
	}
}
