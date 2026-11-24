// Package promotion implements the promotion workflows: the monthly list, the
// draft/publish/archive lifecycle, duplication and the LINE app's active feed.
// All day boundaries are cut in Asia/Bangkok (crm-core/timeutil).
package promotion

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"crm-core/domain"
	domainpromotion "crm-core/domain/promotion"
	"crm-core/timeutil"

	"github.com/google/uuid"
)

const (
	defaultLimit = 20
	maxLimit     = 100
	// maxHomeBanners is the recommended number of simultaneous home banners.
	maxHomeBanners = 5
)

var monthPattern = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)

// ListQuery filters the list. Month is "YYYY-MM" in Thai time and selects every
// promotion whose display window overlaps that month.
type ListQuery struct {
	Month     string
	Status    string
	Placement string
	Search    string
	Page      int
	Limit     int
}

// Input is the editable form of a promotion. Dates are pointers because a
// draft may not have a window yet.
type Input struct {
	Title          string
	Description    string
	ImageURL       string
	Placement      string
	DisplayStartAt *time.Time
	DisplayEndAt   *time.Time
	CTAType        string
	CTATarget      string
	Audience       domainpromotion.Audience
	Priority       int
}

// CTATargetChecker verifies a reward/coupon target and whether it stays
// available for the whole display window.
//
// TODO(T4/T6): the coupon and reward modules implement this and inject it in
// cmd/api/wire.go. A nil checker skips the check, which is the current state
// because neither module exists yet.
type CTATargetChecker interface {
	// Validate returns a warning when the target exists but may not last
	// through [start, end), or an error when the target is invalid/closed.
	Validate(ctx context.Context, ctaType, target string, start, end time.Time) (warning string, err error)
}

// MaxPriority is the highest display order the back office accepts.
const MaxPriority = 9999

// Usecase is the promotion business logic.
type Usecase interface {
	List(ctx context.Context, q ListQuery) ([]domainpromotion.Promotion, int64, error)
	Get(ctx context.Context, id string) (*domainpromotion.Promotion, error)
	Create(ctx context.Context, actor domain.Actor, in Input) (*domainpromotion.Promotion, []string, error)
	Update(ctx context.Context, actor domain.Actor, id string, in Input) (*domainpromotion.Promotion, []string, error)
	Delete(ctx context.Context, actor domain.Actor, id string) error
	Publish(ctx context.Context, actor domain.Actor, id string) (*domainpromotion.Promotion, []string, error)
	// Unpublish hides a published promotion from the LINE app by moving it back
	// to draft (the UI calls that state "unpublic").
	Unpublish(ctx context.Context, actor domain.Actor, id string) (*domainpromotion.Promotion, error)
	Archive(ctx context.Context, actor domain.Actor, id string) (*domainpromotion.Promotion, error)
	Duplicate(ctx context.Context, actor domain.Actor, id string) (*domainpromotion.Promotion, error)
	// SetPriority changes only the LINE OA display position (1 first, 0 = not
	// set, shown last), so the list
	// screen can reorder rows without resending the whole form.
	SetPriority(ctx context.Context, actor domain.Actor, id string, priority int) (*domainpromotion.Promotion, error)
	// ActiveFor is the LINE feed: live promotions of one placement that match
	// the member's audience, in display order (1 first, 0 = not set last).
	ActiveFor(ctx context.Context, member domainpromotion.MemberFacts, placement string, now time.Time) ([]domainpromotion.Promotion, error)
}

type usecase struct {
	repo    domainpromotion.Repository
	checker CTATargetChecker
	now     func() time.Time
	newID   func() string
}

// New builds the promotion usecase. checker may be nil until T4/T6 land.
func New(repo domainpromotion.Repository, checker CTATargetChecker) Usecase {
	return &usecase{
		repo:    repo,
		checker: checker,
		now:     time.Now,
		newID:   newUUID,
	}
}

func (u *usecase) List(ctx context.Context, q ListQuery) ([]domainpromotion.Promotion, int64, error) {
	q = NormalizeListQuery(q)
	if !validListStatus(q.Status) {
		return nil, 0, domain.NewValidationError("status", "must be one of: public, unpublic, draft, published, archived")
	}
	if q.Placement != "" && !domainpromotion.ValidPlacement(q.Placement) {
		return nil, 0, domain.NewValidationError("placement", "unknown placement")
	}

	from, to, err := monthBounds(q.Month)
	if err != nil {
		return nil, 0, err
	}
	return u.repo.FindAll(ctx, domainpromotion.Query{
		Status:      q.Status,
		Placement:   q.Placement,
		Search:      q.Search,
		OverlapFrom: from,
		OverlapTo:   to,
		Page:        q.Page,
		Limit:       q.Limit,
	})
}

// validListStatus accepts the stored lifecycle statuses and the two values the
// promotions table offers: public (published) and unpublic (everything else).
func validListStatus(s string) bool {
	switch s {
	case "", "public", "unpublic":
		return true
	}
	return domainpromotion.ValidStatus(s)
}

// NormalizeListQuery trims the filters and clamps paging. List applies it too,
// so the API can echo the values it actually used in the response meta.
func NormalizeListQuery(q ListQuery) ListQuery {
	q.Status = strings.ToLower(strings.TrimSpace(q.Status))
	q.Placement = strings.ToLower(strings.TrimSpace(q.Placement))
	q.Search = strings.TrimSpace(q.Search)
	q.Month = strings.TrimSpace(q.Month)

	if q.Page < 1 {
		q.Page = 1
	}
	if q.Limit < 1 {
		q.Limit = defaultLimit
	}
	if q.Limit > maxLimit {
		q.Limit = maxLimit
	}
	return q
}

// monthBounds turns "YYYY-MM" into the Bangkok half-open range [first, next
// month). An empty month means no filter.
func monthBounds(month string) (*time.Time, *time.Time, error) {
	month = strings.TrimSpace(month)
	if month == "" {
		return nil, nil, nil
	}
	if !monthPattern.MatchString(month) {
		return nil, nil, domain.NewValidationError("month", "must be in YYYY-MM format")
	}
	start, err := time.ParseInLocation("2006-01", month, timeutil.Bangkok())
	if err != nil {
		return nil, nil, domain.NewValidationError("month", "must be in YYYY-MM format")
	}
	end := start.AddDate(0, 1, 0)
	return &start, &end, nil
}

func (u *usecase) Get(ctx context.Context, id string) (*domainpromotion.Promotion, error) {
	if strings.TrimSpace(id) == "" {
		return nil, domain.NewValidationError("id", "is required")
	}
	return u.repo.FindByID(ctx, id)
}

func (u *usecase) Create(ctx context.Context, actor domain.Actor, in Input) (*domainpromotion.Promotion, []string, error) {
	clean, err := normalizeInput(in)
	if err != nil {
		return nil, nil, err
	}
	if err := u.ensurePriorityFree(ctx, clean.Priority, ""); err != nil {
		return nil, nil, err
	}
	now := u.now()
	p := &domainpromotion.Promotion{
		ID:             u.newID(),
		Title:          clean.Title,
		Description:    clean.Description,
		ImageURL:       clean.ImageURL,
		Placement:      clean.Placement,
		DisplayStartAt: derefTime(clean.DisplayStartAt),
		DisplayEndAt:   derefTime(clean.DisplayEndAt),
		CTAType:        clean.CTAType,
		CTATarget:      clean.CTATarget,
		Audience:       clean.Audience,
		Priority:       clean.Priority,
		Status:         domainpromotion.StatusDraft,
		CreatedBy:      actor.ERPUserID,
		CreatedByEmail: actor.Email,
		UpdatedBy:      actor.ERPUserID,
		UpdatedByEmail: actor.Email,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := u.repo.Create(ctx, p); err != nil {
		return nil, nil, err
	}
	return p, nil, nil
}

func (u *usecase) Update(ctx context.Context, actor domain.Actor, id string, in Input) (*domainpromotion.Promotion, []string, error) {
	existing, err := u.Get(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if existing.Status == domainpromotion.StatusArchived {
		return nil, nil, domain.ErrConflict
	}
	clean, err := normalizeInput(in)
	if err != nil {
		return nil, nil, err
	}

	now := u.now()
	if existing.Status == domainpromotion.StatusPublished {
		// A published promotion always has a window; clearing it would make the
		// row invisible but still "published".
		if clean.DisplayStartAt == nil || clean.DisplayEndAt == nil {
			return nil, nil, domain.NewValidationError("displayStartAt", "is required for a published promotion")
		}
		// Editing a live promotion is allowed, but its start must not be pushed
		// into the past. Keeping the current (already past) start is fine.
		if domainpromotion.DisplayStatus(*existing, now) == domainpromotion.DisplayLive &&
			!clean.DisplayStartAt.Equal(existing.DisplayStartAt) &&
			clean.DisplayStartAt.Before(now) {
			return nil, nil, domain.NewValidationError("displayStartAt", "must not move into the past while the promotion is live")
		}
	}

	if clean.Priority != existing.Priority {
		if err := u.ensurePriorityFree(ctx, clean.Priority, existing.ID); err != nil {
			return nil, nil, err
		}
	}

	existing.Title = clean.Title
	existing.Description = clean.Description
	existing.ImageURL = clean.ImageURL
	existing.Placement = clean.Placement
	existing.DisplayStartAt = derefTime(clean.DisplayStartAt)
	existing.DisplayEndAt = derefTime(clean.DisplayEndAt)
	existing.CTAType = clean.CTAType
	existing.CTATarget = clean.CTATarget
	existing.Audience = clean.Audience
	existing.Priority = clean.Priority
	existing.UpdatedBy = actor.ERPUserID
	existing.UpdatedByEmail = actor.Email
	existing.UpdatedAt = now

	if err := u.repo.Update(ctx, existing); err != nil {
		return nil, nil, err
	}

	warnings, err := u.bannerWarnings(ctx, *existing)
	if err != nil {
		return nil, nil, err
	}
	return existing, warnings, nil
}

func (u *usecase) Delete(ctx context.Context, actor domain.Actor, id string) error {
	existing, err := u.Get(ctx, id)
	if err != nil {
		return err
	}
	if existing.Status != domainpromotion.StatusDraft {
		return domain.ErrConflict
	}
	return u.repo.Delete(ctx, id)
}

func (u *usecase) Publish(ctx context.Context, actor domain.Actor, id string) (*domainpromotion.Promotion, []string, error) {
	existing, err := u.Get(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	switch existing.Status {
	case domainpromotion.StatusArchived:
		return nil, nil, domain.ErrConflict
	case domainpromotion.StatusPublished:
		return nil, nil, domain.ErrConflict
	}

	now := u.now()
	if err := requirePublishable(*existing, now); err != nil {
		return nil, nil, err
	}

	warnings := []string{}
	if u.checker != nil && (existing.CTAType == domainpromotion.CTAReward || existing.CTAType == domainpromotion.CTACoupon) {
		warning, err := u.checker.Validate(ctx, existing.CTAType, existing.CTATarget, existing.DisplayStartAt, existing.DisplayEndAt)
		if err != nil {
			return nil, nil, err
		}
		if warning != "" {
			warnings = append(warnings, warning)
		}
	}

	existing.Status = domainpromotion.StatusPublished
	existing.UpdatedBy = actor.ERPUserID
	existing.UpdatedByEmail = actor.Email
	existing.UpdatedAt = now
	if err := u.repo.Update(ctx, existing); err != nil {
		return nil, nil, err
	}

	bannerWarnings, err := u.bannerWarnings(ctx, *existing)
	if err != nil {
		return nil, nil, err
	}
	return existing, append(warnings, bannerWarnings...), nil
}

// Unpublish moves a published promotion back to draft so it disappears from the
// LINE app. The UI shows draft as "unpublic".
func (u *usecase) Unpublish(ctx context.Context, actor domain.Actor, id string) (*domainpromotion.Promotion, error) {
	existing, err := u.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if existing.Status != domainpromotion.StatusPublished {
		return nil, domain.ErrConflict
	}
	existing.Status = domainpromotion.StatusDraft
	existing.UpdatedBy = actor.ERPUserID
	existing.UpdatedByEmail = actor.Email
	existing.UpdatedAt = u.now()
	if err := u.repo.Update(ctx, existing); err != nil {
		return nil, err
	}
	return existing, nil
}

func (u *usecase) Archive(ctx context.Context, actor domain.Actor, id string) (*domainpromotion.Promotion, error) {
	existing, err := u.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if existing.Status != domainpromotion.StatusPublished {
		return nil, domain.NewValidationError("status", "only a published promotion can be archived")
	}
	existing.Status = domainpromotion.StatusArchived
	// An archived promotion never shows in LINE OA; free its position.
	existing.Priority = 0
	existing.UpdatedBy = actor.ERPUserID
	existing.UpdatedByEmail = actor.Email
	existing.UpdatedAt = u.now()
	if err := u.repo.Update(ctx, existing); err != nil {
		return nil, err
	}
	return existing, nil
}

func (u *usecase) Duplicate(ctx context.Context, actor domain.Actor, id string) (*domainpromotion.Promotion, error) {
	source, err := u.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	now := u.now()
	copy := *source
	copy.ID = u.newID()
	copy.Title = duplicateTitle(source.Title)
	// Display positions are unique; the copy starts without one.
	copy.Priority = 0
	copy.Status = domainpromotion.StatusDraft
	copy.CreatedBy = actor.ERPUserID
	copy.CreatedByEmail = actor.Email
	copy.UpdatedBy = actor.ERPUserID
	copy.UpdatedByEmail = actor.Email
	copy.CreatedAt = now
	copy.UpdatedAt = now
	if err := u.repo.Create(ctx, &copy); err != nil {
		return nil, err
	}
	return &copy, nil
}

func (u *usecase) SetPriority(ctx context.Context, actor domain.Actor, id string, priority int) (*domainpromotion.Promotion, error) {
	if priority < 0 || priority > MaxPriority {
		return nil, domain.NewValidationError("priority", fmt.Sprintf("must be between 0 and %d", MaxPriority))
	}
	existing, err := u.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if existing.Status == domainpromotion.StatusArchived {
		return nil, domain.ErrConflict
	}
	if priority == existing.Priority {
		return existing, nil
	}
	if err := u.ensurePriorityFree(ctx, priority, existing.ID); err != nil {
		return nil, err
	}
	existing.Priority = priority
	existing.UpdatedBy = actor.ERPUserID
	existing.UpdatedByEmail = actor.Email
	existing.UpdatedAt = u.now()
	if err := u.repo.Update(ctx, existing); err != nil {
		return nil, err
	}
	return existing, nil
}

func (u *usecase) ActiveFor(ctx context.Context, member domainpromotion.MemberFacts, placement string, now time.Time) ([]domainpromotion.Promotion, error) {
	placement = strings.ToLower(strings.TrimSpace(placement))
	if placement == "" {
		return nil, domain.NewValidationError("placement", "is required")
	}
	if !domainpromotion.ValidPlacement(placement) {
		return nil, domain.NewValidationError("placement", "unknown placement")
	}
	live, err := u.repo.ListLive(ctx, placement, now)
	if err != nil {
		return nil, err
	}
	out := make([]domainpromotion.Promotion, 0, len(live))
	for _, p := range live {
		if p.Audience.Matches(member, now) {
			out = append(out, p)
		}
	}
	return out, nil
}

// ensurePriorityFree rejects a display position (1…MaxPriority) that another
// non-archived promotion already holds. 0 means "not set" and may repeat.
func (u *usecase) ensurePriorityFree(ctx context.Context, priority int, excludeID string) error {
	if priority == 0 {
		return nil
	}
	taken, err := u.repo.FindByPriority(ctx, priority, excludeID)
	if err != nil {
		return err
	}
	if taken != nil {
		return domain.NewValidationError("", fmt.Sprintf("ลำดับ %d ถูกใช้แล้วโดย \"%s\"", priority, taken.Title))
	}
	return nil
}

// bannerWarnings reports when more than maxHomeBanners home banners overlap.
// It never blocks the save.
func (u *usecase) bannerWarnings(ctx context.Context, p domainpromotion.Promotion) ([]string, error) {
	if p.Placement != domainpromotion.PlacementHomeBanner ||
		p.Status != domainpromotion.StatusPublished ||
		p.DisplayStartAt.IsZero() || p.DisplayEndAt.IsZero() {
		return nil, nil
	}
	count, err := u.repo.CountPublishedOverlapping(ctx, p.Placement, p.DisplayStartAt, p.DisplayEndAt, p.ID)
	if err != nil {
		return nil, err
	}
	total := count + 1
	if total <= maxHomeBanners {
		return nil, nil
	}
	return []string{fmt.Sprintf("มีแบนเนอร์หน้าแรกแสดงพร้อมกัน %d รายการ (แนะนำไม่เกิน %d)", total, maxHomeBanners)}, nil
}

// requirePublishable enforces plan §3.3: title, image and a complete future
// window are mandatory before going live.
func requirePublishable(p domainpromotion.Promotion, now time.Time) error {
	if strings.TrimSpace(p.Title) == "" {
		return domain.NewValidationError("title", "is required to publish")
	}
	if strings.TrimSpace(p.ImageURL) == "" {
		return domain.NewValidationError("imageUrl", "is required to publish")
	}
	if p.DisplayStartAt.IsZero() || p.DisplayEndAt.IsZero() {
		return domain.NewValidationError("displayStartAt", "display window is required to publish")
	}
	if !p.DisplayStartAt.Before(p.DisplayEndAt) {
		return domain.NewValidationError("displayEndAt", "must be after displayStartAt")
	}
	if !p.DisplayEndAt.After(now) {
		return domain.NewValidationError("displayEndAt", "must be in the future")
	}
	return nil
}

// normalizeInput trims and validates the form. Publish-only requirements
// (image, future end) are enforced by requirePublishable so drafts can be
// saved while incomplete.
func normalizeInput(in Input) (Input, error) {
	in.Title = strings.TrimSpace(in.Title)
	in.Description = strings.TrimSpace(in.Description)
	in.ImageURL = strings.TrimSpace(in.ImageURL)
	in.CTATarget = strings.TrimSpace(in.CTATarget)

	switch n := utf8.RuneCountInString(in.Title); {
	case n == 0:
		return in, domain.NewValidationError("title", "is required")
	case n > domainpromotion.MaxTitleRunes:
		return in, domain.NewValidationError("title", fmt.Sprintf("must be %d characters or fewer", domainpromotion.MaxTitleRunes))
	}
	if utf8.RuneCountInString(in.Description) > domainpromotion.MaxDescriptionRunes {
		return in, domain.NewValidationError("description", fmt.Sprintf("must be %d characters or fewer", domainpromotion.MaxDescriptionRunes))
	}

	in.Placement = strings.ToLower(strings.TrimSpace(in.Placement))
	if in.Placement == "" {
		in.Placement = domainpromotion.PlacementHomeBanner
	}
	if !domainpromotion.ValidPlacement(in.Placement) {
		return in, domain.NewValidationError("placement", "must be one of: home_banner, promotion_list, popup")
	}

	in.CTAType = strings.ToLower(strings.TrimSpace(in.CTAType))
	if in.CTAType == "" {
		in.CTAType = domainpromotion.CTANone
	}
	if !domainpromotion.ValidCTAType(in.CTAType) {
		return in, domain.NewValidationError("ctaType", "must be one of: none, url, reward, coupon")
	}
	switch in.CTAType {
	case domainpromotion.CTANone:
		in.CTATarget = ""
	case domainpromotion.CTAURL:
		if in.CTATarget == "" {
			return in, domain.NewValidationError("ctaTarget", "is required for a url CTA")
		}
		if !strings.HasPrefix(in.CTATarget, "https://") {
			return in, domain.NewValidationError("ctaTarget", "must start with https://")
		}
	case domainpromotion.CTAReward, domainpromotion.CTACoupon:
		if in.CTATarget == "" {
			return in, domain.NewValidationError("ctaTarget", "is required for a reward/coupon CTA")
		}
	}

	in.Audience = normalizeAudience(in.Audience)
	if err := validateAudience(in.Audience); err != nil {
		return in, err
	}

	if (in.DisplayStartAt == nil) != (in.DisplayEndAt == nil) {
		return in, domain.NewValidationError("displayStartAt", "both display dates are required together")
	}
	if in.DisplayStartAt != nil {
		start, end := in.DisplayStartAt.UTC(), in.DisplayEndAt.UTC()
		if !start.Before(end) {
			return in, domain.NewValidationError("displayEndAt", "must be after displayStartAt")
		}
		in.DisplayStartAt = &start
		in.DisplayEndAt = &end
	}
	return in, nil
}

func normalizeAudience(a domainpromotion.Audience) domainpromotion.Audience {
	seen := make(map[string]struct{}, len(a.Tiers))
	tiers := make([]string, 0, len(a.Tiers))
	for _, tier := range a.Tiers {
		tier = strings.ToLower(strings.TrimSpace(tier))
		if tier == "" {
			continue
		}
		if _, ok := seen[tier]; ok {
			continue
		}
		seen[tier] = struct{}{}
		tiers = append(tiers, tier)
	}
	a.Tiers = tiers
	return a
}

func validateAudience(a domainpromotion.Audience) error {
	if a.NewMemberDays != nil {
		if *a.NewMemberDays < 1 || *a.NewMemberDays > 3650 {
			return domain.NewValidationError("audience.newMemberDays", "must be between 1 and 3650")
		}
	}
	return nil
}

// duplicateTitle appends the Thai copy suffix without exceeding the title cap.
func duplicateTitle(title string) string {
	const suffix = " (สำเนา)"
	runes := []rune(title)
	limit := domainpromotion.MaxTitleRunes - utf8.RuneCountInString(suffix)
	if limit < 1 {
		limit = 1
	}
	if len(runes) > limit {
		runes = runes[:limit]
	}
	return string(runes) + suffix
}

func derefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

func newUUID() string { return uuid.NewString() }
