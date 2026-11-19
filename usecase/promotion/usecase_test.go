package promotion

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"crm-core/domain"
	domainpromotion "crm-core/domain/promotion"
	"crm-core/timeutil"

	"github.com/stretchr/testify/require"
)

// fakeRepo is an in-memory promotion.Repository that applies the same filters
// the SQL implementation does.
type fakeRepo struct {
	items     map[string]domainpromotion.Promotion
	lastQuery domainpromotion.Query
	nextSeq   int
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{items: map[string]domainpromotion.Promotion{}}
}

func (f *fakeRepo) FindAll(_ context.Context, q domainpromotion.Query) ([]domainpromotion.Promotion, int64, error) {
	f.lastQuery = q
	var out []domainpromotion.Promotion
	for _, p := range f.items {
		if q.Status != "" && p.Status != q.Status {
			continue
		}
		if q.Placement != "" && p.Placement != q.Placement {
			continue
		}
		if q.Search != "" && !strings.Contains(strings.ToLower(p.Title), strings.ToLower(q.Search)) {
			continue
		}
		if q.OverlapFrom != nil && q.OverlapTo != nil {
			if !p.DisplayStartAt.Before(*q.OverlapTo) || !p.DisplayEndAt.After(*q.OverlapFrom) {
				continue
			}
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority > out[j].Priority
		}
		if !out[i].DisplayStartAt.Equal(out[j].DisplayStartAt) {
			return out[i].DisplayStartAt.Before(out[j].DisplayStartAt)
		}
		return out[i].ID < out[j].ID
	})
	total := int64(len(out))
	from := (q.Page - 1) * q.Limit
	if from > len(out) {
		from = len(out)
	}
	to := from + q.Limit
	if to > len(out) {
		to = len(out)
	}
	return out[from:to], total, nil
}

func (f *fakeRepo) FindByID(_ context.Context, id string) (*domainpromotion.Promotion, error) {
	p, ok := f.items[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return &p, nil
}

func (f *fakeRepo) Create(_ context.Context, p *domainpromotion.Promotion) error {
	if _, exists := f.items[p.ID]; exists {
		return domain.ErrConflict
	}
	f.items[p.ID] = *p
	return nil
}

func (f *fakeRepo) Update(_ context.Context, p *domainpromotion.Promotion) error {
	if _, ok := f.items[p.ID]; !ok {
		return domain.ErrNotFound
	}
	f.items[p.ID] = *p
	return nil
}

func (f *fakeRepo) Delete(_ context.Context, id string) error {
	if _, ok := f.items[id]; !ok {
		return domain.ErrNotFound
	}
	delete(f.items, id)
	return nil
}

func (f *fakeRepo) CountPublishedOverlapping(_ context.Context, placement string, start, end time.Time, excludeID string) (int64, error) {
	var count int64
	for _, p := range f.items {
		if p.ID == excludeID || p.Status != domainpromotion.StatusPublished || p.Placement != placement {
			continue
		}
		if p.DisplayStartAt.Before(end) && p.DisplayEndAt.After(start) {
			count++
		}
	}
	return count, nil
}

func (f *fakeRepo) ListLive(_ context.Context, placement string, now time.Time) ([]domainpromotion.Promotion, error) {
	var out []domainpromotion.Promotion
	for _, p := range f.items {
		if p.Status == domainpromotion.StatusPublished && p.Placement == placement &&
			!now.Before(p.DisplayStartAt) && now.Before(p.DisplayEndAt) {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Priority > out[j].Priority })
	return out, nil
}

type fixedClock struct{ t time.Time }

func (c *fixedClock) now() time.Time { return c.t }

func newTestUsecase() (*usecase, *fakeRepo, *fixedClock) {
	repo := newFakeRepo()
	clock := &fixedClock{t: time.Date(2026, time.November, 18, 12, 0, 0, 0, timeutil.Bangkok())}
	uc := New(repo, nil).(*usecase)
	uc.now = clock.now
	seq := 0
	uc.newID = func() string {
		seq++
		return fmt.Sprintf("promo-%02d", seq)
	}
	return uc, repo, clock
}

var testActor = domain.Actor{ERPUserID: 7, Email: "owner@example.com", Roles: []string{"owner"}}

func window(clock *fixedClock) (*time.Time, *time.Time) {
	start := clock.t.Add(2 * time.Hour)
	end := start.Add(24 * time.Hour)
	return &start, &end
}

func validInput(clock *fixedClock) Input {
	start, end := window(clock)
	return Input{
		Title:          "โปรตุลาคม",
		ImageURL:       "/api/v1/files/banner",
		DisplayStartAt: start,
		DisplayEndAt:   end,
		Placement:      domainpromotion.PlacementHomeBanner,
	}
}

func TestListMonthUsesThaiMonthBoundaries(t *testing.T) {
	uc, repo, _ := newTestUsecase()
	// 31 Oct 23:30 Thai time (16:30 UTC) — the October filter must catch it,
	// November must not.
	repo.items["oct"] = domainpromotion.Promotion{
		ID: "oct", Title: "Halloween", Status: domainpromotion.StatusPublished,
		Placement:      domainpromotion.PlacementPromotionList,
		DisplayStartAt: time.Date(2026, time.October, 31, 23, 30, 0, 0, timeutil.Bangkok()),
		DisplayEndAt:   time.Date(2026, time.November, 15, 0, 0, 0, 0, timeutil.Bangkok()),
	}
	repo.items["dec"] = domainpromotion.Promotion{
		ID: "dec", Title: "December", Status: domainpromotion.StatusDraft,
		Placement:      domainpromotion.PlacementPopup,
		DisplayStartAt: time.Date(2026, time.December, 1, 0, 0, 0, 0, timeutil.Bangkok()),
		DisplayEndAt:   time.Date(2026, time.December, 31, 23, 59, 0, 0, timeutil.Bangkok()),
	}

	for month, want := range map[string]int{
		"2026-10": 1,
		"2026-11": 1,
		"2026-12": 1,
		"2027-01": 0,
		"2026-09": 0,
	} {
		got, total, err := uc.List(context.Background(), ListQuery{Month: month})
		require.NoError(t, err, month)
		require.EqualValues(t, want, total, month)
		require.Len(t, got, want, month)
	}

	// The window is half-open: a December promotion start is not in November.
	_, _, err := uc.List(context.Background(), ListQuery{Month: "2026-11"})
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, time.November, 1, 0, 0, 0, 0, timeutil.Bangkok()), *repo.lastQuery.OverlapFrom)
	require.Equal(t, time.Date(2026, time.December, 1, 0, 0, 0, 0, timeutil.Bangkok()), *repo.lastQuery.OverlapTo)
}

func TestListRejectsBadFilters(t *testing.T) {
	uc, _, _ := newTestUsecase()
	ctx := context.Background()

	for _, month := range []string{"2026-13", "11-2026", "2026-1", "November"} {
		_, _, err := uc.List(ctx, ListQuery{Month: month})
		require.Error(t, err, month)
		require.ErrorIs(t, err, domain.ErrValidation, month)
	}
	_, _, err := uc.List(ctx, ListQuery{Status: "live"})
	require.ErrorIs(t, err, domain.ErrValidation)
	_, _, err = uc.List(ctx, ListQuery{Placement: "email"})
	require.ErrorIs(t, err, domain.ErrValidation)
}

func TestListClampsPagingAndKeepsFilters(t *testing.T) {
	uc, repo, _ := newTestUsecase()
	repo.items["a"] = domainpromotion.Promotion{
		ID: "a", Title: "Summer", Status: domainpromotion.StatusDraft,
		Placement: domainpromotion.PlacementPopup,
	}
	_, total, err := uc.List(context.Background(), ListQuery{Search: "sum", Page: 0, Limit: 500})
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Equal(t, 1, repo.lastQuery.Page)
	require.Equal(t, maxLimit, repo.lastQuery.Limit)
}

func TestCreateKeepsDraftsIncomplete(t *testing.T) {
	uc, repo, _ := newTestUsecase()
	p, warnings, err := uc.Create(context.Background(), testActor, Input{Title: "  ร่างโปร  "})
	require.NoError(t, err)
	require.Empty(t, warnings)
	require.Equal(t, domainpromotion.StatusDraft, p.Status)
	require.Equal(t, "ร่างโปร", p.Title)
	require.Equal(t, domainpromotion.PlacementHomeBanner, p.Placement, "default placement")
	require.Equal(t, domainpromotion.CTANone, p.CTAType)
	require.True(t, p.DisplayStartAt.IsZero())
	require.Equal(t, testActor.ERPUserID, p.CreatedBy)
	require.Contains(t, repo.items, p.ID)
}

func TestCreateValidatesFields(t *testing.T) {
	uc, _, clock := newTestUsecase()
	ctx := context.Background()

	_, _, err := uc.Create(ctx, testActor, Input{Title: strings.Repeat("ก", domainpromotion.MaxTitleRunes+1)})
	requireValidationField(t, err, "title")

	_, _, err = uc.Create(ctx, testActor, Input{Title: "x", Description: strings.Repeat("a", 2001)})
	requireValidationField(t, err, "description")

	_, _, err = uc.Create(ctx, testActor, Input{Title: "x", Placement: "email"})
	requireValidationField(t, err, "placement")

	in := validInput(clock)
	in.CTAType = domainpromotion.CTAURL
	in.CTATarget = "http://example.com"
	_, _, err = uc.Create(ctx, testActor, in)
	requireValidationField(t, err, "ctaTarget")

	in.CTATarget = "https://example.com/sale"
	_, _, err = uc.Create(ctx, testActor, in)
	require.NoError(t, err, "an https url is accepted")

	in = validInput(clock)
	in.CTAType = domainpromotion.CTAReward
	_, _, err = uc.Create(ctx, testActor, in)
	requireValidationField(t, err, "ctaTarget")

	in = validInput(clock)
	in.CTAType = domainpromotion.CTANone
	in.CTATarget = "https://ignored.example.com"
	p, _, err := uc.Create(ctx, testActor, in)
	require.NoError(t, err)
	require.Empty(t, p.CTATarget, "a none CTA clears the target")

	in = validInput(clock)
	in.DisplayEndAt = in.DisplayStartAt
	_, _, err = uc.Create(ctx, testActor, in)
	requireValidationField(t, err, "displayEndAt")

	in = validInput(clock)
	in.DisplayEndAt = nil
	_, _, err = uc.Create(ctx, testActor, in)
	requireValidationField(t, err, "displayStartAt")

	days := 0
	in = validInput(clock)
	in.Audience = domainpromotion.Audience{NewMemberDays: &days}
	_, _, err = uc.Create(ctx, testActor, in)
	requireValidationField(t, err, "audience.newMemberDays")

	in = validInput(clock)
	in.Audience = domainpromotion.Audience{Tiers: []string{" Gold ", "gold", ""}}
	p, _, err = uc.Create(ctx, testActor, in)
	require.NoError(t, err)
	require.Equal(t, []string{"gold"}, p.Audience.Tiers, "tiers are normalized and deduplicated")
}

func TestPublishLifecycle(t *testing.T) {
	uc, _, clock := newTestUsecase()
	ctx := context.Background()

	draft, _, err := uc.Create(ctx, testActor, Input{Title: "ไม่มีรูป"})
	require.NoError(t, err)

	_, _, err = uc.Publish(ctx, testActor, draft.ID)
	requireValidationField(t, err, "imageUrl")

	in := validInput(clock)
	in.Title = "พร้อมเผยแพร่"
	ready, _, err := uc.Create(ctx, testActor, in)
	require.NoError(t, err)

	published, warnings, err := uc.Publish(ctx, testActor, ready.ID)
	require.NoError(t, err)
	require.Empty(t, warnings)
	require.Equal(t, domainpromotion.StatusPublished, published.Status)
	require.Equal(t, domainpromotion.DisplayScheduled, domainpromotion.DisplayStatus(*published, clock.t),
		"the window starts two hours from now")

	_, _, err = uc.Publish(ctx, testActor, ready.ID)
	require.ErrorIs(t, err, domain.ErrConflict, "publishing twice is a conflict")

	archived, err := uc.Archive(ctx, testActor, ready.ID)
	require.NoError(t, err)
	require.Equal(t, domainpromotion.StatusArchived, archived.Status)

	_, _, err = uc.Publish(ctx, testActor, ready.ID)
	require.ErrorIs(t, err, domain.ErrConflict)
}

func TestPublishRejectsAnEndInThePast(t *testing.T) {
	uc, _, clock := newTestUsecase()
	start := clock.t.Add(-48 * time.Hour)
	end := clock.t.Add(-24 * time.Hour)
	draft, _, err := uc.Create(context.Background(), testActor, Input{
		Title: "เก่า", ImageURL: "/x", DisplayStartAt: &start, DisplayEndAt: &end,
	})
	require.NoError(t, err)

	_, _, err = uc.Publish(context.Background(), testActor, draft.ID)
	requireValidationField(t, err, "displayEndAt")
}

func TestLivePromotionCannotMoveStartIntoThePast(t *testing.T) {
	uc, _, clock := newTestUsecase()
	ctx := context.Background()
	in := validInput(clock)
	in.DisplayStartAt = timePtr(clock.t.Add(-2 * time.Hour))
	in.DisplayEndAt = timePtr(clock.t.Add(2 * time.Hour))
	p, _, err := uc.Create(ctx, testActor, in)
	require.NoError(t, err)
	_, _, err = uc.Publish(ctx, testActor, p.ID)
	require.NoError(t, err, "a promotion may go live with a start already passed")

	past := clock.t.Add(-time.Hour)
	in.DisplayStartAt = &past
	_, _, err = uc.Update(ctx, testActor, p.ID, in)
	requireValidationField(t, err, "displayStartAt")

	// Keeping the current start (even though it is in the past) is allowed.
	in.DisplayStartAt = timePtr(clock.t.Add(-2 * time.Hour))
	in.Title = "แก้ชื่อระหว่างแสดง"
	updated, _, err := uc.Update(ctx, testActor, p.ID, in)
	require.NoError(t, err)
	require.Equal(t, "แก้ชื่อระหว่างแสดง", updated.Title)

	// Moving it forward is allowed.
	future := clock.t.Add(time.Hour)
	in.DisplayStartAt = &future
	in.DisplayEndAt = timePtr(clock.t.Add(3 * time.Hour))
	_, _, err = uc.Update(ctx, testActor, p.ID, in)
	require.NoError(t, err)
}

func TestDeleteOnlyDrafts(t *testing.T) {
	uc, _, clock := newTestUsecase()
	ctx := context.Background()

	in := validInput(clock)
	p, _, err := uc.Create(ctx, testActor, in)
	require.NoError(t, err)
	require.NoError(t, uc.Delete(ctx, testActor, p.ID))

	p, _, err = uc.Create(ctx, testActor, in)
	require.NoError(t, err)
	_, _, err = uc.Publish(ctx, testActor, p.ID)
	require.NoError(t, err)
	require.ErrorIs(t, uc.Delete(ctx, testActor, p.ID), domain.ErrConflict)
}

func TestArchiveRequiresPublished(t *testing.T) {
	uc, _, _ := newTestUsecase()
	draft, _, err := uc.Create(context.Background(), testActor, Input{Title: "ร่าง"})
	require.NoError(t, err)
	_, err = uc.Archive(context.Background(), testActor, draft.ID)
	requireValidationField(t, err, "status")
}

func TestDuplicateIsADraftCopyWithSuffix(t *testing.T) {
	uc, repo, clock := newTestUsecase()
	ctx := context.Background()

	source := validInput(clock)
	source.Title = strings.Repeat("ก", 120)
	p, _, err := uc.Create(ctx, testActor, source)
	require.NoError(t, err)

	copied, err := uc.Duplicate(ctx, testActor, p.ID)
	require.NoError(t, err)
	require.NotEqual(t, p.ID, copied.ID)
	require.Equal(t, domainpromotion.StatusDraft, copied.Status)
	require.LessOrEqual(t, len([]rune(copied.Title)), domainpromotion.MaxTitleRunes)
	require.Contains(t, copied.Title, "(สำเนา)")
	require.Equal(t, p.DisplayStartAt, copied.DisplayStartAt)
	require.Equal(t, p.Audience, copied.Audience)
	require.Len(t, repo.items, 2)

	_, err = uc.Duplicate(ctx, testActor, "missing")
	require.ErrorIs(t, err, domain.ErrNotFound)
}

func TestHomeBannerWarningsAfterFiveOverlaps(t *testing.T) {
	uc, repo, clock := newTestUsecase()
	ctx := context.Background()
	start, end := window(clock)

	for i := 1; i <= 4; i++ {
		repo.items[fmt.Sprintf("seed-%d", i)] = domainpromotion.Promotion{
			ID: fmt.Sprintf("seed-%d", i), Title: "seed", Status: domainpromotion.StatusPublished,
			Placement:      domainpromotion.PlacementHomeBanner,
			DisplayStartAt: *start, DisplayEndAt: *end,
		}
	}

	fifth, _, err := uc.Create(ctx, testActor, validInput(clock))
	require.NoError(t, err)
	_, warnings, err := uc.Publish(ctx, testActor, fifth.ID)
	require.NoError(t, err)
	require.Empty(t, warnings, "five banners is the recommended maximum")

	sixthIn := validInput(clock)
	sixthIn.Title = "ใบที่หก"
	sixth, _, err := uc.Create(ctx, testActor, sixthIn)
	require.NoError(t, err)
	_, warnings, err = uc.Publish(ctx, testActor, sixth.ID)
	require.NoError(t, err)
	require.Len(t, warnings, 1)
	require.Contains(t, warnings[0], "6")
}

func TestActiveForFiltersPlacementTimeAndAudience(t *testing.T) {
	uc, repo, clock := newTestUsecase()
	now := clock.t

	repo.items["all"] = domainpromotion.Promotion{
		ID: "all", Title: "ทุกคน", Status: domainpromotion.StatusPublished,
		Placement: domainpromotion.PlacementHomeBanner, Priority: 1,
		DisplayStartAt: now.Add(-time.Hour), DisplayEndAt: now.Add(time.Hour),
	}
	repo.items["gold"] = domainpromotion.Promotion{
		ID: "gold", Title: "Gold", Status: domainpromotion.StatusPublished,
		Placement: domainpromotion.PlacementHomeBanner, Priority: 10,
		Audience:       domainpromotion.Audience{Tiers: []string{"gold"}},
		DisplayStartAt: now.Add(-time.Hour), DisplayEndAt: now.Add(time.Hour),
	}
	newDays := 7
	repo.items["new"] = domainpromotion.Promotion{
		ID: "new", Title: "ใหม่", Status: domainpromotion.StatusPublished,
		Placement:      domainpromotion.PlacementHomeBanner,
		Audience:       domainpromotion.Audience{NewMemberDays: &newDays},
		DisplayStartAt: now.Add(-time.Hour), DisplayEndAt: now.Add(time.Hour),
	}
	repo.items["popup"] = domainpromotion.Promotion{
		ID: "popup", Title: "Popup", Status: domainpromotion.StatusPublished,
		Placement:      domainpromotion.PlacementPopup,
		DisplayStartAt: now.Add(-time.Hour), DisplayEndAt: now.Add(time.Hour),
	}
	repo.items["expired"] = domainpromotion.Promotion{
		ID: "expired", Title: "หมดแล้ว", Status: domainpromotion.StatusPublished,
		Placement:      domainpromotion.PlacementHomeBanner,
		DisplayStartAt: now.Add(-2 * time.Hour), DisplayEndAt: now,
	}
	repo.items["draft"] = domainpromotion.Promotion{
		ID: "draft", Title: "ร่าง", Status: domainpromotion.StatusDraft,
		Placement:      domainpromotion.PlacementHomeBanner,
		DisplayStartAt: now.Add(-time.Hour), DisplayEndAt: now.Add(time.Hour),
	}

	goldOld := domainpromotion.MemberFacts{TierCode: "gold", RegisteredAt: now.AddDate(0, 0, -100)}
	got, err := uc.ActiveFor(context.Background(), goldOld, domainpromotion.PlacementHomeBanner, now)
	require.NoError(t, err)
	require.Equal(t, []string{"gold", "all"}, promotionIDs(got), "priority order, audience matched")

	bronzeNew := domainpromotion.MemberFacts{TierCode: "bronze", RegisteredAt: now.AddDate(0, 0, -1)}
	got, err = uc.ActiveFor(context.Background(), bronzeNew, domainpromotion.PlacementHomeBanner, now)
	require.NoError(t, err)
	require.Equal(t, []string{"all", "new"}, promotionIDs(got))

	got, err = uc.ActiveFor(context.Background(), bronzeNew, domainpromotion.PlacementPopup, now)
	require.NoError(t, err)
	require.Equal(t, []string{"popup"}, promotionIDs(got))

	_, err = uc.ActiveFor(context.Background(), bronzeNew, "", now)
	requireValidationField(t, err, "placement")
	_, err = uc.ActiveFor(context.Background(), bronzeNew, "email", now)
	requireValidationField(t, err, "placement")
}

type fakeChecker struct {
	warning string
	err     error
	called  int
	target  string
	from    time.Time
	to      time.Time
}

func (f *fakeChecker) Validate(_ context.Context, _ string, target string, start, end time.Time) (string, error) {
	f.called++
	f.target = target
	f.from, f.to = start, end
	return f.warning, f.err
}

func TestPublishUsesTheCTATargetChecker(t *testing.T) {
	uc, _, clock := newTestUsecase()
	checker := &fakeChecker{warning: "คูปองหมดอายุก่อนช่วงแสดงผล"}
	uc.checker = checker

	in := validInput(clock)
	in.CTAType = domainpromotion.CTACoupon
	in.CTATarget = "coupon-123"
	p, _, err := uc.Create(context.Background(), testActor, in)
	require.NoError(t, err)

	_, warnings, err := uc.Publish(context.Background(), testActor, p.ID)
	require.NoError(t, err)
	require.Equal(t, 1, checker.called)
	require.Equal(t, "coupon-123", checker.target)
	require.Equal(t, p.DisplayStartAt, checker.from)
	require.Equal(t, p.DisplayEndAt, checker.to)
	require.Contains(t, warnings, "คูปองหมดอายุก่อนช่วงแสดงผล")

	checker.err = domain.NewValidationError("ctaTarget", "coupon is closed")
	second, _, err := uc.Create(context.Background(), testActor, in)
	require.NoError(t, err)
	_, _, err = uc.Publish(context.Background(), testActor, second.ID)
	requireValidationField(t, err, "ctaTarget")
}

func TestPublishSkipsTheCheckerForNonRewardCTAs(t *testing.T) {
	uc, _, clock := newTestUsecase()
	checker := &fakeChecker{}
	uc.checker = checker

	p, _, err := uc.Create(context.Background(), testActor, validInput(clock))
	require.NoError(t, err)
	_, _, err = uc.Publish(context.Background(), testActor, p.ID)
	require.NoError(t, err)
	require.Zero(t, checker.called)
}

func promotionIDs(list []domainpromotion.Promotion) []string {
	out := make([]string, 0, len(list))
	for _, p := range list {
		out = append(out, p.ID)
	}
	return out
}

func timePtr(t time.Time) *time.Time { return &t }

func requireValidationField(t *testing.T, err error, field string) {
	t.Helper()
	require.Error(t, err)
	var ve *domain.ValidationError
	require.True(t, errors.As(err, &ve), "expected a validation error, got %v", err)
	require.Equal(t, field, ve.Field)
}
