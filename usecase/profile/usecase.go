// Package profile is the member self-service in the LINE OA (L3): edit the
// profile, change the email with an OTP to the new address, and keep shipping
// addresses for rewards.
package profile

import (
	"context"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"crm-core/domain"
	domainmember "crm-core/domain/member"
	domainotp "crm-core/domain/otp"
	usecaseotp "crm-core/usecase/otp"
	usecaseregistration "crm-core/usecase/registration"
)

// Update is the edit form. BirthDate nil = keep as is (it cannot be cleared).
type Update struct {
	FirstName string
	LastName  string
	Phone     string
	Gender    string
	BirthDate *time.Time
}

// AddressInput is a shipping address as typed.
type AddressInput struct {
	Recipient   string
	Phone       string
	Address     string
	Subdistrict string
	District    string
	Province    string
	Postcode    string
	IsDefault   bool
}

// Usecase is the member self-service.
type Usecase interface {
	// Profile returns the member (active members only).
	Profile(ctx context.Context, memberID uint) (*domainmember.Member, error)
	UpdateProfile(ctx context.Context, memberID uint, in Update) (*domainmember.Member, error)
	// RequestEmailChange sends an OTP to the new address. It never says
	// whether another member uses it (that is checked after the OTP).
	RequestEmailChange(ctx context.Context, memberID uint, lineUserID, email, ip string) (*usecaseotp.RequestResult, error)
	ConfirmEmailChange(ctx context.Context, memberID uint, lineUserID, email, code string) (*domainmember.Member, error)

	Addresses(ctx context.Context, memberID uint) ([]domainmember.MemberAddress, error)
	AddAddress(ctx context.Context, memberID uint, in AddressInput) (*domainmember.MemberAddress, error)
	UpdateAddress(ctx context.Context, memberID, id uint, in AddressInput) (*domainmember.MemberAddress, error)
	DeleteAddress(ctx context.Context, memberID, id uint) error
}

type usecase struct {
	repo domainmember.ProfileRepository
	otps usecaseotp.Usecase
	now  func() time.Time
}

// Option customizes the usecase.
type Option func(*usecase)

// WithClock replaces time.Now (tests).
func WithClock(now func() time.Time) Option { return func(u *usecase) { u.now = now } }

// New builds the profile usecase.
func New(repo domainmember.ProfileRepository, otps usecaseotp.Usecase, opts ...Option) Usecase {
	u := &usecase{repo: repo, otps: otps, now: time.Now}
	for _, opt := range opts {
		opt(u)
	}
	return u
}

func (u *usecase) activeMember(ctx context.Context, id uint) (*domainmember.Member, error) {
	m, err := u.repo.FindMember(ctx, id)
	if err != nil {
		return nil, err
	}
	if m.Status != domainmember.StatusActive || m.DeletedAt != nil {
		return nil, domain.ErrMemberNotAllowed
	}
	return m, nil
}

func (u *usecase) Profile(ctx context.Context, memberID uint) (*domainmember.Member, error) {
	return u.activeMember(ctx, memberID)
}

func sameDay(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Format("2006-01-02") == b.Format("2006-01-02")
}

func (u *usecase) UpdateProfile(ctx context.Context, memberID uint, in Update) (*domainmember.Member, error) {
	m, err := u.activeMember(ctx, memberID)
	if err != nil {
		return nil, err
	}
	now := u.now()
	p, err := usecaseregistration.ValidateProfile(usecaseregistration.Profile{
		FirstName: in.FirstName, LastName: in.LastName, Phone: in.Phone, Gender: in.Gender, BirthDate: in.BirthDate,
	}, now)
	if err != nil {
		return nil, err
	}

	changes := domainmember.ProfileChanges{
		FirstName:          p.FirstName,
		LastName:           p.LastName,
		Gender:             p.Gender,
		BirthDate:          m.BirthDate,
		BirthDateChangedAt: m.BirthDateChangedAt,
	}
	if p.Phone != "" {
		phone := p.Phone
		changes.PhoneE164 = &phone
	}
	// Setting a birth date for the first time is free; correcting an existing
	// one is allowed once (birthday bonuses depend on it).
	if p.BirthDate != nil && !sameDay(p.BirthDate, m.BirthDate) {
		if m.BirthDate != nil {
			if m.BirthDateChangedAt != nil {
				return nil, domainmember.ErrBirthDateLocked
			}
			changes.BirthDateChangedAt = &now
		}
		changes.BirthDate = p.BirthDate
	}
	if err := u.repo.UpdateProfile(ctx, memberID, changes); err != nil {
		return nil, err
	}
	return u.repo.FindMember(ctx, memberID)
}

func (u *usecase) RequestEmailChange(ctx context.Context, memberID uint, lineUserID, email, ip string) (*usecaseotp.RequestResult, error) {
	m, err := u.activeMember(ctx, memberID)
	if err != nil {
		return nil, err
	}
	normalized, err := usecaseotp.NormalizeEmail(email)
	if err != nil {
		return nil, err
	}
	if normalized == m.Email {
		return nil, domain.NewValidationError("email", "is already your email")
	}
	return u.otps.Request(ctx, usecaseotp.RequestInput{
		Email: normalized, Purpose: domainotp.PurposeChangeEmail, LineUserID: lineUserID, IP: ip,
	})
}

func (u *usecase) ConfirmEmailChange(ctx context.Context, memberID uint, lineUserID, email, code string) (*domainmember.Member, error) {
	if _, err := u.activeMember(ctx, memberID); err != nil {
		return nil, err
	}
	verified, err := u.otps.Verify(ctx, usecaseotp.VerifyInput{
		Email: email, Code: code, Purpose: domainotp.PurposeChangeEmail, LineUserID: lineUserID,
	})
	if err != nil {
		return nil, err
	}
	if err := u.repo.UpdateEmail(ctx, memberID, verified, u.now()); err != nil {
		return nil, err
	}
	return u.repo.FindMember(ctx, memberID)
}

func (u *usecase) Addresses(ctx context.Context, memberID uint) ([]domainmember.MemberAddress, error) {
	if _, err := u.activeMember(ctx, memberID); err != nil {
		return nil, err
	}
	return u.repo.ListAddresses(ctx, memberID)
}

func (u *usecase) AddAddress(ctx context.Context, memberID uint, in AddressInput) (*domainmember.MemberAddress, error) {
	if _, err := u.activeMember(ctx, memberID); err != nil {
		return nil, err
	}
	a, err := validateAddress(in)
	if err != nil {
		return nil, err
	}
	count, err := u.repo.CountAddresses(ctx, memberID)
	if err != nil {
		return nil, err
	}
	if count >= domainmember.MaxAddresses {
		return nil, domainmember.ErrAddressLimit
	}
	a.MemberID = memberID
	if count == 0 {
		a.IsDefault = true // the first address is the default
	}
	if err := u.repo.SaveAddress(ctx, a); err != nil {
		return nil, err
	}
	return a, nil
}

func (u *usecase) UpdateAddress(ctx context.Context, memberID, id uint, in AddressInput) (*domainmember.MemberAddress, error) {
	if _, err := u.activeMember(ctx, memberID); err != nil {
		return nil, err
	}
	current, err := u.repo.FindAddress(ctx, memberID, id) // also proves ownership
	if err != nil {
		return nil, err
	}
	a, err := validateAddress(in)
	if err != nil {
		return nil, err
	}
	a.ID = current.ID
	a.MemberID = memberID
	a.CreatedAt = current.CreatedAt
	if current.IsDefault {
		a.IsDefault = true // un-defaulting happens by choosing another default
	}
	if err := u.repo.SaveAddress(ctx, a); err != nil {
		return nil, err
	}
	return a, nil
}

func (u *usecase) DeleteAddress(ctx context.Context, memberID, id uint) error {
	if _, err := u.activeMember(ctx, memberID); err != nil {
		return err
	}
	return u.repo.DeleteAddress(ctx, memberID, id)
}

var postcode = regexp.MustCompile(`^[1-9]\d{4}$`)

func clean(s string) string { return strings.Join(strings.Fields(s), " ") }

func validateAddress(in AddressInput) (*domainmember.MemberAddress, error) {
	a := &domainmember.MemberAddress{
		Recipient:   clean(in.Recipient),
		Address:     strings.TrimSpace(in.Address),
		Subdistrict: clean(in.Subdistrict),
		District:    clean(in.District),
		Province:    clean(in.Province),
		Postcode:    strings.TrimSpace(in.Postcode),
		IsDefault:   in.IsDefault,
	}
	required := []struct {
		field, value string
		max          int
	}{
		{"recipient", a.Recipient, 100},
		{"address", a.Address, 500},
		{"subdistrict", a.Subdistrict, 100},
		{"district", a.District, 100},
		{"province", a.Province, 100},
	}
	for _, r := range required {
		if r.value == "" {
			return nil, domain.NewValidationError(r.field, "is required")
		}
		if utf8.RuneCountInString(r.value) > r.max {
			return nil, domain.NewValidationError(r.field, "is too long")
		}
	}
	if !postcode.MatchString(a.Postcode) {
		return nil, domain.NewValidationError("postcode", "must be 5 digits")
	}
	phone, err := usecaseregistration.NormalizePhone(in.Phone)
	if err != nil {
		return nil, err
	}
	if phone == "" {
		return nil, domain.NewValidationError("phone", "is required for delivery")
	}
	a.Phone = phone
	return a, nil
}
