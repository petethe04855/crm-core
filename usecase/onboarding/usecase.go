// Package onboarding is the LINE OA entry flow (plan CRM_LINE_OA_TASKS.md §4):
//
//	LINE ID token ─► linked member ─────────────► home
//	             └► not linked ─► consent ─► email OTP ─► register / link (L2)
//
// The LINE user id always comes from a verified ID token, never from the
// client. Tokens are issued by the API project; this package only decides who
// the user is and which step comes next.
package onboarding

import (
	"context"
	"errors"
	"strings"

	"crm-core/domain"
	domainmember "crm-core/domain/member"
	domainotp "crm-core/domain/otp"
	usecaseconsent "crm-core/usecase/consent"
	usecaseotp "crm-core/usecase/otp"
)

// Next steps returned to the LIFF app.
const (
	StepHome     = "home"     // linked member, nothing pending
	StepConsent  = "consent"  // a required consent document is not accepted
	StepEmail    = "email"    // prove an email with OTP
	StepRegister = "register" // email verified, no member yet (L2)
	StepLink     = "link"     // email verified and belongs to a member (L2)
)

// Errors.
var (
	// ErrInvalidLineToken means LINE rejected the ID token (forged, expired or
	// issued for another channel).
	ErrInvalidLineToken = errors.New("invalid LINE ID token")
	// ErrConsentRequired means a required consent is missing for this step.
	ErrConsentRequired = errors.New("consent required")
)

// LineIdentity is what a verified LINE ID token says about the user.
type LineIdentity struct {
	UserID      string
	DisplayName string
	PictureURL  string
}

// LineVerifier checks a LIFF ID token with LINE. Returns ErrInvalidLineToken
// when LINE rejects it.
type LineVerifier interface {
	Verify(ctx context.Context, idToken string) (*LineIdentity, error)
}

// LoginResult says who the LINE user is and where to go next.
type LoginResult struct {
	Line     LineIdentity
	Member   *domainmember.Member // nil until the LINE user is linked
	NextStep string
}

// EmailResult is returned after the OTP is correct. ExistingMember may be
// shown now: the user proved they own the address.
type EmailResult struct {
	Email          string
	ExistingMember bool
	NextStep       string
}

// Usecase is the onboarding flow.
type Usecase interface {
	LoginWithLine(ctx context.Context, idToken string) (*LoginResult, error)
	// Resume recomputes the next step for a LINE user that already holds a
	// token (after consent, after refresh).
	Resume(ctx context.Context, lineUserID string) (*LoginResult, error)
	RequestEmailOTP(ctx context.Context, lineUserID, email, ip string) (*usecaseotp.RequestResult, error)
	VerifyEmailOTP(ctx context.Context, lineUserID, email, code string) (*EmailResult, error)
	// RequireConsent returns ErrConsentRequired while a required consent is
	// missing (register / link check it too).
	RequireConsent(ctx context.Context, lineUserID string) error
}

type usecase struct {
	verifier LineVerifier
	members  domainmember.IdentityRepository
	consents usecaseconsent.Usecase
	otps     usecaseotp.Usecase
}

// New builds the onboarding usecase.
func New(verifier LineVerifier, members domainmember.IdentityRepository, consents usecaseconsent.Usecase, otps usecaseotp.Usecase) Usecase {
	return &usecase{verifier: verifier, members: members, consents: consents, otps: otps}
}

func (u *usecase) LoginWithLine(ctx context.Context, idToken string) (*LoginResult, error) {
	if strings.TrimSpace(idToken) == "" {
		return nil, domain.NewValidationError("idToken", "is required")
	}
	identity, err := u.verifier.Verify(ctx, idToken)
	if err != nil {
		return nil, err
	}
	if identity == nil || identity.UserID == "" {
		return nil, ErrInvalidLineToken
	}
	res, err := u.Resume(ctx, identity.UserID)
	if err != nil {
		return nil, err
	}
	res.Line = *identity
	return res, nil
}

func (u *usecase) Resume(ctx context.Context, lineUserID string) (*LoginResult, error) {
	res := &LoginResult{Line: LineIdentity{UserID: lineUserID}}
	member, _, err := u.members.FindByLineUserID(ctx, lineUserID)
	switch {
	case err == nil:
		if member.Status != domainmember.StatusActive || member.DeletedAt != nil {
			return nil, domain.ErrMemberNotAllowed
		}
		res.Member = member
	case !errors.Is(err, domain.ErrNotFound):
		return nil, err
	}

	pending, err := u.consents.Pending(ctx, lineUserID)
	if err != nil {
		return nil, err
	}
	switch {
	case pending:
		// Members too: a new required version must be accepted before use.
		res.NextStep = StepConsent
	case res.Member != nil:
		res.NextStep = StepHome
	default:
		res.NextStep = StepEmail
	}
	return res, nil
}

func (u *usecase) RequestEmailOTP(ctx context.Context, lineUserID, email, ip string) (*usecaseotp.RequestResult, error) {
	if err := u.RequireConsent(ctx, lineUserID); err != nil {
		return nil, err
	}
	return u.otps.Request(ctx, usecaseotp.RequestInput{
		Email:      email,
		Purpose:    domainotp.PurposeRegister,
		LineUserID: lineUserID,
		IP:         ip,
	})
}

func (u *usecase) VerifyEmailOTP(ctx context.Context, lineUserID, email, code string) (*EmailResult, error) {
	if err := u.RequireConsent(ctx, lineUserID); err != nil {
		return nil, err
	}
	verified, err := u.otps.Verify(ctx, usecaseotp.VerifyInput{
		Email:      email,
		Code:       code,
		Purpose:    domainotp.PurposeRegister,
		LineUserID: lineUserID,
	})
	if err != nil {
		return nil, err
	}
	res := &EmailResult{Email: verified, NextStep: StepRegister}
	_, err = u.members.FindByEmail(ctx, verified)
	switch {
	case err == nil:
		res.ExistingMember = true
		res.NextStep = StepLink
	case !errors.Is(err, domain.ErrNotFound):
		return nil, err
	}
	return res, nil
}

func (u *usecase) RequireConsent(ctx context.Context, lineUserID string) error {
	if strings.TrimSpace(lineUserID) == "" {
		return domain.ErrForbidden
	}
	pending, err := u.consents.Pending(ctx, lineUserID)
	if err != nil {
		return err
	}
	if pending {
		return ErrConsentRequired
	}
	return nil
}
