package postgres

import (
	"context"
	"testing"
	"time"

	"crm-core/domain"
	domainpromotion "crm-core/domain/promotion"

	"github.com/stretchr/testify/require"
)

func promo(id, title, status, placement string, start, end time.Time) *domainpromotion.Promotion {
	return &domainpromotion.Promotion{
		ID: id, Title: title, Status: status, Placement: placement,
		DisplayStartAt: start, DisplayEndAt: end,
	}
}

func TestPromotionRepositoryCRUD(t *testing.T) {
	db := newTestDB(t)
	repo := NewPromotionRepository(db)
	ctx := context.Background()
	start := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	days := 30

	p := promo("promo-1", "November Sale", domainpromotion.StatusDraft, domainpromotion.PlacementHomeBanner, start, end)
	p.Audience = domainpromotion.Audience{Tiers: []string{"gold"}, NewMemberDays: &days}
	p.CTAType = domainpromotion.CTACoupon
	p.CTATarget = "coupon-1"
	p.Priority = 5
	p.ImageURL = "/api/v1/files/banner"
	require.NoError(t, repo.Create(ctx, p))

	got, err := repo.FindByID(ctx, "promo-1")
	require.NoError(t, err)
	require.Equal(t, []string{"gold"}, got.Audience.Tiers)
	require.NotNil(t, got.Audience.NewMemberDays)
	require.Equal(t, 30, *got.Audience.NewMemberDays)
	require.Equal(t, "coupon-1", got.CTATarget)
	require.True(t, got.DisplayStartAt.Equal(start))

	got.Title = "November Sale v2"
	got.Priority = 0
	got.Description = ""
	got.CTAType = domainpromotion.CTANone
	got.CTATarget = ""
	require.NoError(t, repo.Update(ctx, got))

	reloaded, err := repo.FindByID(ctx, "promo-1")
	require.NoError(t, err)
	require.Equal(t, "November Sale v2", reloaded.Title)
	require.Equal(t, 0, reloaded.Priority, "zero values must persist")
	require.Empty(t, reloaded.CTATarget)
	require.Equal(t, domainpromotion.CTANone, reloaded.CTAType)

	require.NoError(t, repo.Delete(ctx, "promo-1"))
	_, err = repo.FindByID(ctx, "promo-1")
	require.ErrorIs(t, err, domain.ErrNotFound)
	require.ErrorIs(t, repo.Delete(ctx, "promo-1"), domain.ErrNotFound)
}

func TestPromotionRepositoryFindAllFiltersAndOrder(t *testing.T) {
	db := newTestDB(t)
	repo := NewPromotionRepository(db)
	ctx := context.Background()

	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	items := []*domainpromotion.Promotion{
		promo("a", "Summer Sale", domainpromotion.StatusPublished, domainpromotion.PlacementHomeBanner, base, base.AddDate(0, 0, 10)),
		promo("b", "winter sale", domainpromotion.StatusDraft, domainpromotion.PlacementPopup, base.AddDate(0, 0, 5), base.AddDate(0, 0, 15)),
		promo("c", "100%_off", domainpromotion.StatusPublished, domainpromotion.PlacementHomeBanner, base.AddDate(0, 0, 20), base.AddDate(0, 0, 25)),
		promo("d", "December", domainpromotion.StatusArchived, domainpromotion.PlacementPromotionList, base.AddDate(0, 1, 0), base.AddDate(0, 1, 10)),
	}
	items[0].Priority = 1
	items[1].Priority = 10
	items[2].Priority = 5
	for _, p := range items {
		require.NoError(t, repo.Create(ctx, p))
	}

	all, total, err := repo.FindAll(ctx, domainpromotion.Query{Page: 1, Limit: 20})
	require.NoError(t, err)
	require.EqualValues(t, 4, total)
	require.Equal(t, []string{"b", "c", "a", "d"}, []string{all[0].ID, all[1].ID, all[2].ID, all[3].ID},
		"priority desc, then start asc")

	published, total, err := repo.FindAll(ctx, domainpromotion.Query{Status: domainpromotion.StatusPublished, Page: 1, Limit: 20})
	require.NoError(t, err)
	require.EqualValues(t, 2, total)
	require.Len(t, published, 2)

	popups, total, err := repo.FindAll(ctx, domainpromotion.Query{Placement: domainpromotion.PlacementPopup, Page: 1, Limit: 20})
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Equal(t, "b", popups[0].ID)

	// Search is case-insensitive and LIKE wildcards are literal.
	found, total, err := repo.FindAll(ctx, domainpromotion.Query{Search: "WINTER", Page: 1, Limit: 20})
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Equal(t, "b", found[0].ID)

	found, total, err = repo.FindAll(ctx, domainpromotion.Query{Search: "%_", Page: 1, Limit: 20})
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Equal(t, "c", found[0].ID)

	// Month overlap: November only, half-open on both ends.
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	found, total, err = repo.FindAll(ctx, domainpromotion.Query{OverlapFrom: &from, OverlapTo: &to, Page: 1, Limit: 20})
	require.NoError(t, err)
	require.EqualValues(t, 3, total, "the December promotion is outside November")
	require.NotContains(t, []string{found[0].ID, found[1].ID, found[2].ID}, "d")

	// Paging keeps the total and returns the requested slice.
	page2, total, err := repo.FindAll(ctx, domainpromotion.Query{Page: 2, Limit: 2})
	require.NoError(t, err)
	require.EqualValues(t, 4, total)
	require.Len(t, page2, 2)
	require.Equal(t, "a", page2[0].ID)
}

func TestPromotionRepositoryCountOverlappingAndListLive(t *testing.T) {
	db := newTestDB(t)
	repo := NewPromotionRepository(db)
	ctx := context.Background()

	start := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	end := start.Add(4 * time.Hour)

	require.NoError(t, repo.Create(ctx, promo("live-1", "live 1", domainpromotion.StatusPublished, domainpromotion.PlacementHomeBanner, start, end)))
	require.NoError(t, repo.Create(ctx, promo("live-2", "live 2", domainpromotion.StatusPublished, domainpromotion.PlacementHomeBanner, start.Add(time.Hour), end.Add(time.Hour))))
	require.NoError(t, repo.Create(ctx, promo("later", "later", domainpromotion.StatusPublished, domainpromotion.PlacementHomeBanner, end, end.Add(time.Hour))))
	require.NoError(t, repo.Create(ctx, promo("draft", "draft", domainpromotion.StatusDraft, domainpromotion.PlacementHomeBanner, start, end)))
	require.NoError(t, repo.Create(ctx, promo("other", "other", domainpromotion.StatusPublished, domainpromotion.PlacementPopup, start, end)))

	count, err := repo.CountPublishedOverlapping(ctx, domainpromotion.PlacementHomeBanner, start, end, "")
	require.NoError(t, err)
	require.EqualValues(t, 2, count, "drafts, other placements and non-overlapping rows are excluded")

	count, err = repo.CountPublishedOverlapping(ctx, domainpromotion.PlacementHomeBanner, start, end, "live-1")
	require.NoError(t, err)
	require.EqualValues(t, 1, count, "the row under edit is excluded")

	live, err := repo.ListLive(ctx, domainpromotion.PlacementHomeBanner, start)
	require.NoError(t, err)
	require.Len(t, live, 1, "the start minute is live; the others have not started")
	require.Equal(t, "live-1", live[0].ID)

	live, err = repo.ListLive(ctx, domainpromotion.PlacementHomeBanner, end)
	require.NoError(t, err)
	require.Equal(t, []string{"live-2", "later"}, []string{live[0].ID, live[1].ID},
		"the first row ended at the boundary and 'later' started; live-2 still runs")
}
