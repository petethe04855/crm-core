// Package otp sends and verifies one-time codes by email (plan
// CRM_LINE_OA_MEMBERSHIP.md §5.4):
//
//   - 6 digits from crypto/rand; only an HMAC is stored; 4-character ref code
//   - valid 10 minutes, single use, 5 wrong tries, new code every 60 seconds
//   - a new code makes every earlier code of the LINE user unusable
//   - per email 5/hour, per LINE user 10/day, per IP 20/hour
//   - never says whether the email belongs to a member
package otp

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"math/big"
	"net/mail"
	"strings"
	"time"

	"crm-core/domain"
	domainmember "crm-core/domain/member"
	domainotp "crm-core/domain/otp"
)

// refAlphabet skips look-alike characters (0/O, 1/I/L).
const refAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

// RequestInput asks for a code.
type RequestInput struct {
	Email      string
	Purpose    string
	LineUserID string
	IP         string
}

// RequestResult is safe to show: it never reveals whether the email is known.
type RequestResult struct {
	RefCode     string
	MaskedEmail string
	ExpiresAt   time.Time
	ResendAt    time.Time
}

// VerifyInput checks a code.
type VerifyInput struct {
	Email      string
	Code       string
	Purpose    string
	LineUserID string
}

// EmailDomainChecker rejects domains that cannot receive mail (e.g. no MX
// record). Optional: nil skips the check (tests, offline development).
type EmailDomainChecker func(ctx context.Context, domain string) error

// Option customizes the usecase.
type Option func(*usecase)

// WithClock replaces time.Now (tests).
func WithClock(now func() time.Time) Option { return func(u *usecase) { u.now = now } }

// WithRandom replaces crypto/rand (tests).
func WithRandom(r io.Reader) Option { return func(u *usecase) { u.rand = r } }

// WithDomainChecker enables the MX / deliverability check.
func WithDomainChecker(check EmailDomainChecker) Option {
	return func(u *usecase) { u.checkDomain = check }
}

// Usecase sends and verifies email OTPs.
type Usecase interface {
	Request(ctx context.Context, in RequestInput) (*RequestResult, error)
	// Verify returns the normalized email once the code is correct.
	Verify(ctx context.Context, in VerifyInput) (string, error)
}

type usecase struct {
	repo        domainotp.Repository
	mailer      domainotp.Mailer
	secret      []byte
	now         func() time.Time
	rand        io.Reader
	checkDomain EmailDomainChecker
}

// New builds the OTP usecase. secret keys the code HMAC (OTP_HMAC_SECRET).
func New(repo domainotp.Repository, mailer domainotp.Mailer, secret []byte, opts ...Option) Usecase {
	u := &usecase{repo: repo, mailer: mailer, secret: secret, now: time.Now, rand: rand.Reader}
	for _, opt := range opts {
		opt(u)
	}
	return u
}

// NormalizeEmail trims and lowercases an email and checks its format.
func NormalizeEmail(raw string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(raw))
	if email == "" {
		return "", domain.NewValidationError("email", "is required")
	}
	if len(email) > 254 {
		return "", domain.NewValidationError("email", "is too long")
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email || addr.Name != "" {
		return "", domain.NewValidationError("email", "is not a valid email address")
	}
	at := strings.LastIndex(email, "@")
	host := email[at+1:]
	if !strings.Contains(host, ".") || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") {
		return "", domain.NewValidationError("email", "is not a valid email address")
	}
	return email, nil
}

func validPurpose(p string) bool {
	return p == domainotp.PurposeRegister || p == domainotp.PurposeChangeEmail
}

func (u *usecase) Request(ctx context.Context, in RequestInput) (*RequestResult, error) {
	email, err := NormalizeEmail(in.Email)
	if err != nil {
		return nil, err
	}
	if !validPurpose(in.Purpose) {
		return nil, domain.NewValidationError("purpose", "is not supported")
	}
	host := email[strings.LastIndex(email, "@")+1:]
	if disposableDomains[host] {
		return nil, domainotp.ErrInvalidEmail
	}
	if u.checkDomain != nil {
		if err := u.checkDomain(ctx, host); err != nil {
			return nil, domainotp.ErrInvalidEmail
		}
	}

	now := u.now()
	last, err := u.repo.Latest(ctx, email, in.Purpose, in.LineUserID)
	switch {
	case err == nil && last.VerifiedAt == nil && now.Before(last.ResendAvailableAt):
		return nil, &domainotp.ResendError{RetryAt: last.ResendAvailableAt}
	case err != nil && !errors.Is(err, domain.ErrNotFound):
		return nil, err
	}
	if err := u.checkRateLimits(ctx, email, in, now); err != nil {
		return nil, err
	}

	code, err := u.digits(domainotp.CodeLength)
	if err != nil {
		return nil, err
	}
	ref, err := u.ref(domainotp.RefLength)
	if err != nil {
		return nil, err
	}
	req := &domainotp.Request{
		Channel:           domainotp.ChannelEmail,
		Destination:       email,
		Purpose:           in.Purpose,
		CodeHash:          u.hash(in.Purpose, email, in.LineUserID, code),
		RefCode:           ref,
		ExpiresAt:         now.Add(domainotp.TTL),
		MaxAttempts:       domainotp.MaxAttempts,
		ResendAvailableAt: now.Add(domainotp.ResendCooldown),
		LineUserID:        in.LineUserID,
		IP:                in.IP,
		CreatedAt:         now,
	}
	if err := u.repo.Create(ctx, req); err != nil {
		return nil, err
	}
	if err := u.mailer.SendOTP(ctx, email, code, ref, domainotp.TTL); err != nil {
		// Tell the member at once (no silent retries until the code expires)
		// and let them ask again without waiting for the cooldown.
		_ = u.repo.Cancel(ctx, req.ID, now)
		return nil, domainotp.ErrSendFailed
	}
	return &RequestResult{
		RefCode:     ref,
		MaskedEmail: domainmember.MaskEmail(email),
		ExpiresAt:   req.ExpiresAt,
		ResendAt:    req.ResendAvailableAt,
	}, nil
}

func (u *usecase) checkRateLimits(ctx context.Context, email string, in RequestInput, now time.Time) error {
	n, err := u.repo.CountByDestinationSince(ctx, email, now.Add(-time.Hour))
	if err != nil {
		return err
	}
	if n >= domainotp.PerEmailPerHour {
		return domainotp.ErrRateLimited
	}
	if in.LineUserID != "" {
		n, err = u.repo.CountByLineUserSince(ctx, in.LineUserID, now.Add(-24*time.Hour))
		if err != nil {
			return err
		}
		if n >= domainotp.PerLineUserDaily {
			return domainotp.ErrRateLimited
		}
	}
	if in.IP != "" {
		n, err = u.repo.CountByIPSince(ctx, in.IP, now.Add(-time.Hour))
		if err != nil {
			return err
		}
		if n >= domainotp.PerIPPerHour {
			return domainotp.ErrRateLimited
		}
	}
	return nil
}

func (u *usecase) Verify(ctx context.Context, in VerifyInput) (string, error) {
	email, err := NormalizeEmail(in.Email)
	if err != nil {
		return "", err
	}
	code := strings.TrimSpace(in.Code)
	if len(code) != domainotp.CodeLength || strings.Trim(code, "0123456789") != "" {
		return "", domain.NewValidationError("code", "must be 6 digits")
	}

	req, err := u.repo.Latest(ctx, email, in.Purpose, in.LineUserID)
	if errors.Is(err, domain.ErrNotFound) {
		return "", domainotp.ErrNoActiveRequest
	}
	if err != nil {
		return "", err
	}
	now := u.now()
	switch {
	case req.VerifiedAt != nil:
		return "", domainotp.ErrNoActiveRequest // single use
	case req.AttemptCount >= req.MaxAttempts:
		return "", domainotp.ErrTooManyAttempts
	case !now.Before(req.ExpiresAt):
		return "", domainotp.ErrExpired
	}

	want := u.hash(in.Purpose, email, in.LineUserID, code)
	if !hmac.Equal([]byte(want), []byte(req.CodeHash)) {
		count, err := u.repo.IncrementAttempts(ctx, req.ID)
		if err != nil {
			return "", err
		}
		if count >= req.MaxAttempts {
			return "", domainotp.ErrTooManyAttempts
		}
		return "", &domainotp.InvalidCodeError{Remaining: req.MaxAttempts - count}
	}
	ok, err := u.repo.MarkVerified(ctx, req.ID, now)
	if err != nil {
		return "", err
	}
	if !ok { // verified or superseded by a concurrent request
		return "", domainotp.ErrNoActiveRequest
	}
	return email, nil
}

// hash binds the code to its purpose, email and LINE user, so a code is
// useless for any other combination even if two rows collide.
func (u *usecase) hash(purpose, email, lineUserID, code string) string {
	mac := hmac.New(sha256.New, u.secret)
	mac.Write([]byte(purpose + "\x00" + email + "\x00" + lineUserID + "\x00" + code))
	return hex.EncodeToString(mac.Sum(nil))
}

func (u *usecase) digits(n int) (string, error) {
	return u.pick("0123456789", n)
}

func (u *usecase) ref(n int) (string, error) {
	return u.pick(refAlphabet, n)
}

func (u *usecase) pick(alphabet string, n int) (string, error) {
	max := big.NewInt(int64(len(alphabet)))
	out := make([]byte, n)
	for i := range out {
		v, err := rand.Int(u.rand, max)
		if err != nil {
			return "", err
		}
		out[i] = alphabet[v.Int64()]
	}
	return string(out), nil
}

// disposableDomains is a short list of throwaway-mail providers. It is not
// exhaustive; the MX check and the per-LINE-user limit do the rest.
var disposableDomains = map[string]bool{
	"mailinator.com":    true,
	"guerrillamail.com": true,
	"10minutemail.com":  true,
	"tempmail.com":      true,
	"temp-mail.org":     true,
	"yopmail.com":       true,
	"trashmail.com":     true,
	"getnada.com":       true,
	"sharklasers.com":   true,
	"dispostable.com":   true,
	"maildrop.cc":       true,
	"throwawaymail.com": true,
}
