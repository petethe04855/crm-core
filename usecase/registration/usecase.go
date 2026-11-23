// Package registration joins a verified LINE user to the membership (plan
// CRM_LINE_OA_TASKS.md L2):
//
//   - Register: new member + default tier + LINE link + tier history + the
//     signup welcome bonus, all in one transaction.
//   - LinkLine: the verified email already belongs to a member, so link this
//     LINE account to it. If that member was linked to another LINE account,
//     the old link is ended (its row keeps the history) and the new one starts.
//
// The email always comes from a completed OTP (the API reads it from the
// onboarding token), never from the request body.
package registration

import (
	"context"
	"errors"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"crm-core/domain"
	domainearnrule "crm-core/domain/earnrule"
	domainloyalty "crm-core/domain/loyalty"
	domainmember "crm-core/domain/member"
	domainpoints "crm-core/domain/points"
	domainregistration "crm-core/domain/registration"
)

// MinAge is the youngest member the form accepts (plan §5.4).
const MinAge = 10

// Profile is what the member types on the register form.
type Profile struct {
	FirstName string
	LastName  string
	Phone     string     // optional, Thai mobile or E.164
	BirthDate *time.Time // optional, date only
	Gender    string     // optional, defaults to unspecified
}

// LineInfo is the verified LINE identity plus cosmetic profile data.
type LineInfo struct {
	UserID      string
	DisplayName string
	PictureURL  string
}

// RegisterInput registers a new member.
type RegisterInput struct {
	Line    LineInfo
	Email   string // verified by OTP
	Profile Profile
}

// LinkInput links LINE to the member that owns the verified email.
type LinkInput struct {
	Line  LineInfo
	Email string // verified by OTP
}

// Result is the member after registering or linking. WelcomePoints is the
// signup bonus credited (0 when no signup rule is active). MovedFromOtherLine
// is true when the member's previous LINE link was replaced.
type Result struct {
	Member             *domainmember.Member
	WelcomePoints      int64
	MovedFromOtherLine bool
}

// ExpiryPolicy gives the point lifetime in months (0 = never expires).
type ExpiryPolicy func(ctx context.Context) (months int, err error)

// Usecase registers members and links LINE accounts.
type Usecase interface {
	Register(ctx context.Context, in RegisterInput) (*Result, error)
	LinkLine(ctx context.Context, in LinkInput) (*Result, error)
}

type usecase struct {
	uow    domainregistration.UnitOfWork
	tiers  domainloyalty.TierRepository
	rules  domainearnrule.Repository
	expiry ExpiryPolicy
	notify JoinNotifier
	now    func() time.Time
}

// Option customizes the usecase.
type Option func(*usecase)

// WithClock replaces time.Now (tests).
func WithClock(now func() time.Time) Option { return func(u *usecase) { u.now = now } }

// JoinNotifier is told after a member joined from LINE (rich menu + welcome).
type JoinNotifier interface {
	Joined(ctx context.Context, lineUserID string, memberID uint, firstName, memberCode string, welcomePoints int64, linkedExisting bool) error
}

// WithNotifier sends the welcome / rich menu change after commit (best effort).
func WithNotifier(n JoinNotifier) Option { return func(u *usecase) { u.notify = n } }

func (u *usecase) joined(ctx context.Context, lineUserID string, res *Result, linked bool) {
	if u.notify == nil || res == nil || res.Member == nil {
		return
	}
	if err := u.notify.Joined(ctx, lineUserID, res.Member.ID, res.Member.FirstName, res.Member.MemberCode, res.WelcomePoints, linked); err != nil {
		log.Printf("[WARN] LINE notify join for member %d: %v", res.Member.ID, err)
	}
}

// New builds the registration usecase.
func New(uow domainregistration.UnitOfWork, tiers domainloyalty.TierRepository, rules domainearnrule.Repository, expiry ExpiryPolicy, opts ...Option) Usecase {
	u := &usecase{uow: uow, tiers: tiers, rules: rules, expiry: expiry, now: time.Now}
	for _, opt := range opts {
		opt(u)
	}
	return u
}

func normalizeEmail(email string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || !strings.Contains(email, "@") {
		// The API only passes an email proven by OTP; anything else is a bug.
		return "", domain.ErrEmailNotVerified
	}
	return email, nil
}

func validLine(l LineInfo) error {
	if strings.TrimSpace(l.UserID) == "" {
		return domain.ErrForbidden
	}
	return nil
}

func (u *usecase) Register(ctx context.Context, in RegisterInput) (*Result, error) {
	if err := validLine(in.Line); err != nil {
		return nil, err
	}
	email, err := normalizeEmail(in.Email)
	if err != nil {
		return nil, err
	}
	now := u.now()
	profile, err := ValidateProfile(in.Profile, now)
	if err != nil {
		return nil, err
	}
	tier, err := u.entryTier(ctx)
	if err != nil {
		return nil, err
	}
	bonus, ruleIDs, err := u.signupBonus(ctx, now)
	if err != nil {
		return nil, err
	}
	expiresAt, err := u.expiresAt(ctx, now)
	if err != nil {
		return nil, err
	}

	res := &Result{}
	err = u.uow.Do(ctx, func(ctx context.Context, tx domainregistration.Tx) error {
		if _, err := tx.FindActiveLink(ctx, in.Line.UserID); err == nil {
			return domainregistration.ErrLineAlreadyLinked
		} else if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		if _, err := tx.FindMemberByEmail(ctx, email); err == nil {
			return domainregistration.ErrEmailTaken
		} else if !errors.Is(err, domain.ErrNotFound) {
			return err
		}

		verified := now
		m := &domainmember.Member{
			FirstName:         profile.FirstName,
			LastName:          profile.LastName,
			Email:             email,
			EmailVerifiedAt:   &verified,
			PhoneE164:         nilIfEmpty(profile.Phone),
			BirthDate:         profile.BirthDate,
			Gender:            profile.Gender,
			TierID:            tier.ID,
			Status:            domainmember.StatusActive,
			RegisteredChannel: "line",
			LastActiveAt:      &verified,
			CreatedAt:         now,
			UpdatedAt:         now,
		}
		if err := tx.CreateMember(ctx, m); err != nil {
			return err
		}
		if err := tx.LinkLine(ctx, &domainmember.MemberLineAccount{
			MemberID:    m.ID,
			LineUserID:  in.Line.UserID,
			DisplayName: truncate(in.Line.DisplayName, 255),
			PictureURL:  safePicture(in.Line.PictureURL),
			LinkedAt:    now,
		}); err != nil {
			return err
		}
		if err := tx.AddTierHistory(ctx, &domainregistration.TierHistory{
			MemberID: m.ID, ToTierID: tier.ID, Reason: domainregistration.TierReasonSignup, CreatedAt: now,
		}); err != nil {
			return err
		}
		if bonus > 0 {
			entry, err := tx.Points().Earn(ctx, domainpoints.EarnCommand{
				MemberID:   m.ID,
				Points:     bonus,
				SourceType: domainpoints.SourceBonus,
				SourceID:   domainregistration.SignupSourcePrefix + strconv.FormatUint(uint64(m.ID), 10),
				Note:       "แต้มต้อนรับสมาชิกใหม่",
				RuleIDs:    ruleIDs,
				ExpiresAt:  expiresAt,
			})
			if err != nil {
				return err
			}
			m.PointBalance = entry.BalanceAfter
			res.WelcomePoints = bonus
		}
		res.Member = m
		return nil
	})
	if err != nil {
		return nil, err
	}
	u.joined(ctx, in.Line.UserID, res, false)
	return res, nil
}

func (u *usecase) LinkLine(ctx context.Context, in LinkInput) (*Result, error) {
	if err := validLine(in.Line); err != nil {
		return nil, err
	}
	email, err := normalizeEmail(in.Email)
	if err != nil {
		return nil, err
	}
	now := u.now()
	res := &Result{}
	err = u.uow.Do(ctx, func(ctx context.Context, tx domainregistration.Tx) error {
		found, err := tx.FindMemberByEmail(ctx, email)
		if errors.Is(err, domain.ErrNotFound) {
			return domainregistration.ErrEmailNotMember
		}
		if err != nil {
			return err
		}
		m, err := tx.LockMember(ctx, found.ID)
		if err != nil {
			return err
		}
		if m.Status != domainmember.StatusActive || m.DeletedAt != nil {
			return domain.ErrMemberNotAllowed
		}

		current, err := tx.FindActiveLink(ctx, in.Line.UserID)
		switch {
		case err == nil && current.MemberID == m.ID:
			res.Member = m // already linked to this member: nothing to do
			return nil
		case err == nil:
			return domainregistration.ErrLineAlreadyLinked
		case !errors.Is(err, domain.ErrNotFound):
			return err
		}

		// The member changed LINE account: end the old link, keep its row.
		old, err := tx.FindMemberActiveLink(ctx, m.ID)
		switch {
		case err == nil:
			if err := tx.UnlinkLine(ctx, old.ID, now); err != nil {
				return err
			}
			res.MovedFromOtherLine = true
		case !errors.Is(err, domain.ErrNotFound):
			return err
		}

		if err := tx.LinkLine(ctx, &domainmember.MemberLineAccount{
			MemberID:    m.ID,
			LineUserID:  in.Line.UserID,
			DisplayName: truncate(in.Line.DisplayName, 255),
			PictureURL:  safePicture(in.Line.PictureURL),
			LinkedAt:    now,
		}); err != nil {
			return err
		}
		if m.EmailVerifiedAt == nil {
			if err := tx.MarkEmailVerified(ctx, m.ID, now); err != nil {
				return err
			}
			m.EmailVerifiedAt = &now
		}
		res.Member = m
		return nil
	})
	if err != nil {
		return nil, err
	}
	u.joined(ctx, in.Line.UserID, res, true)
	return res, nil
}

// entryTier is the default tier (code bronze), else the lowest active tier.
func (u *usecase) entryTier(ctx context.Context) (*domainloyalty.Tier, error) {
	tiers, err := u.tiers.FindAll(ctx)
	if err != nil {
		return nil, err
	}
	var lowest *domainloyalty.Tier
	for i := range tiers {
		t := &tiers[i]
		if !t.IsActive {
			continue
		}
		if t.Code == domainloyalty.DefaultTierCode {
			return t, nil
		}
		if lowest == nil || t.MinSpend < lowest.MinSpend {
			lowest = t
		}
	}
	if lowest == nil {
		return nil, errors.New("no active tier: run migrations to seed the default tier")
	}
	return lowest, nil
}

// signupBonus uses the highest-priority active bonus_fixed rule with trigger
// signup (FindActive orders by priority). One rule only: bonuses never stack.
func (u *usecase) signupBonus(ctx context.Context, at time.Time) (int64, []uint, error) {
	if u.rules == nil {
		return 0, nil, nil
	}
	rules, err := u.rules.FindActive(ctx, at)
	if err != nil {
		return 0, nil, err
	}
	for _, r := range rules {
		if r.Type == domainearnrule.TypeBonusFixed && r.Trigger == domainearnrule.TriggerSignup && r.Points > 0 {
			return r.Points, []uint{r.ID}, nil
		}
	}
	return 0, nil, nil
}

func (u *usecase) expiresAt(ctx context.Context, at time.Time) (*time.Time, error) {
	if u.expiry == nil {
		return nil, nil
	}
	months, err := u.expiry(ctx)
	if err != nil {
		return nil, err
	}
	if months <= 0 {
		return nil, nil
	}
	t := at.AddDate(0, months, 0)
	return &t, nil
}

var thaiMobile = regexp.MustCompile(`^0[689]\d{8}$`)
var e164 = regexp.MustCompile(`^\+[1-9]\d{7,14}$`)

// NormalizePhone turns "081-234-5678" into "+66812345678". Empty stays empty.
func NormalizePhone(raw string) (string, error) {
	p := strings.NewReplacer(" ", "", "-", "", "(", "", ")", "", ".", "").Replace(strings.TrimSpace(raw))
	switch {
	case p == "":
		return "", nil
	case thaiMobile.MatchString(p):
		return "+66" + p[1:], nil
	case e164.MatchString(p):
		return p, nil
	}
	return "", domain.NewValidationError("phone", "must be a Thai mobile number (e.g. 0812345678)")
}

// ValidateProfile normalizes and checks the member-editable fields (also used
// by the profile edit in L3).
func ValidateProfile(p Profile, now time.Time) (Profile, error) {
	p.FirstName = strings.Join(strings.Fields(p.FirstName), " ")
	p.LastName = strings.Join(strings.Fields(p.LastName), " ")
	switch {
	case p.FirstName == "":
		return p, domain.NewValidationError("firstName", "is required")
	case utf8.RuneCountInString(p.FirstName) > 100:
		return p, domain.NewValidationError("firstName", "must be at most 100 characters")
	case p.LastName == "":
		return p, domain.NewValidationError("lastName", "is required")
	case utf8.RuneCountInString(p.LastName) > 100:
		return p, domain.NewValidationError("lastName", "must be at most 100 characters")
	}
	phone, err := NormalizePhone(p.Phone)
	if err != nil {
		return p, err
	}
	p.Phone = phone

	p.Gender = strings.ToLower(strings.TrimSpace(p.Gender))
	switch p.Gender {
	case "":
		p.Gender = domainmember.GenderUnspecified
	case domainmember.GenderMale, domainmember.GenderFemale, domainmember.GenderOther, domainmember.GenderUnspecified:
	default:
		return p, domain.NewValidationError("gender", "must be male, female, other or unspecified")
	}

	if p.BirthDate != nil {
		today := now.In(domainpoints.Bangkok)
		b := p.BirthDate.In(domainpoints.Bangkok)
		birth := time.Date(b.Year(), b.Month(), b.Day(), 0, 0, 0, 0, domainpoints.Bangkok)
		if birth.After(today) {
			return p, domain.NewValidationError("birthDate", "must not be in the future")
		}
		if birth.AddDate(MinAge, 0, 0).After(today) {
			return p, domain.NewValidationError("birthDate", "member must be at least 10 years old")
		}
		if birth.Year() < 1900 {
			return p, domain.NewValidationError("birthDate", "is not a valid date")
		}
		p.BirthDate = &birth
	}
	return p, nil
}

// safePicture keeps only LINE CDN https URLs (shown on the member card).
func safePicture(url string) string {
	url = strings.TrimSpace(url)
	if strings.HasPrefix(url, "https://") && len(url) <= 1000 {
		return url
	}
	return ""
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
