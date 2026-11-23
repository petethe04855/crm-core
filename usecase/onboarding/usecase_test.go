package onboarding

import (
	"context"
	"testing"
	"time"

	"crm-core/database"
	"crm-core/domain"
	domainconsent "crm-core/domain/consent"
	domainloyalty "crm-core/domain/loyalty"
	domainmember "crm-core/domain/member"
	"crm-core/migrations"
	"crm-core/repository/postgres"
	usecaseconsent "crm-core/usecase/consent"
	usecaseotp "crm-core/usecase/otp"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type fakeVerifier map[string]string // idToken -> LINE user id

func (f fakeVerifier) Verify(_ context.Context, idToken string) (*LineIdentity, error) {
	id, ok := f[idToken]
	if !ok {
		return nil, ErrInvalidLineToken
	}
	return &LineIdentity{UserID: id, DisplayName: "Nok"}, nil
}

type lastCode struct{ code string }

func (m *lastCode) SendOTP(_ context.Context, _, code, _ string, _ time.Duration) error {
	m.code = code
	return nil
}

type env struct {
	uc       Usecase
	consents usecaseconsent.Usecase
	mail     *lastCode
	db       *gorm.DB
}

func setup(t *testing.T) env {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), database.GormConfig("", logger.Silent))
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, migrations.Run(db, ""))
	consents := usecaseconsent.New(postgres.NewConsentRepository(db))
	mail := &lastCode{}
	otps := usecaseotp.New(postgres.NewOTPRepository(db), mail, []byte("secret"))
	verifier := fakeVerifier{"tok-new": "Unew", "tok-member": "Umember", "tok-suspended": "Ususp"}
	uc := New(verifier, postgres.NewMemberIdentityRepository(db), consents, otps)
	return env{uc: uc, consents: consents, mail: mail, db: db}
}

func (e env) publishTerms(t *testing.T) *domainconsent.Document {
	t.Helper()
	past := time.Now().Add(-time.Minute)
	doc, err := e.consents.CreateDocument(context.Background(), domain.Actor{ERPUserID: 1}, usecaseconsent.DocumentInput{
		Type: domainconsent.TypeTerms, Title: "Terms", Content: "x", IsRequired: true, IsActive: true, EffectiveAt: &past,
	})
	require.NoError(t, err)
	return doc
}

func (e env) seedMember(t *testing.T, lineUserID, email, status string) {
	t.Helper()
	var tier domainloyalty.Tier
	require.NoError(t, e.db.First(&tier).Error)
	m := domainmember.Member{MemberCode: "M-" + lineUserID, FirstName: "A", LastName: "B", Email: email, TierID: tier.ID, Status: status, Gender: domainmember.GenderUnspecified}
	require.NoError(t, e.db.Create(&m).Error)
	if lineUserID != "" {
		require.NoError(t, e.db.Create(&domainmember.MemberLineAccount{MemberID: m.ID, LineUserID: lineUserID, LinkedAt: time.Now()}).Error)
	}
}

func TestInvalidTokenIsRejected(t *testing.T) {
	e := setup(t)
	_, err := e.uc.LoginWithLine(context.Background(), "forged")
	require.ErrorIs(t, err, ErrInvalidLineToken)
	_, err = e.uc.LoginWithLine(context.Background(), "")
	require.ErrorIs(t, err, domain.ErrValidation)
}

func TestNewUserGoesConsentThenEmail(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	terms := e.publishTerms(t)

	res, err := e.uc.LoginWithLine(ctx, "tok-new")
	require.NoError(t, err)
	require.Equal(t, "Unew", res.Line.UserID)
	require.Nil(t, res.Member)
	require.Equal(t, StepConsent, res.NextStep)

	_, err = e.uc.RequestEmailOTP(ctx, "Unew", "nok@example.com", "")
	require.ErrorIs(t, err, ErrConsentRequired, "no OTP before consent")

	require.NoError(t, e.consents.Accept(ctx, usecaseconsent.AcceptInput{LineUserID: "Unew", DocumentIDs: []uint{terms.ID}}))
	res, err = e.uc.Resume(ctx, "Unew")
	require.NoError(t, err)
	require.Equal(t, StepEmail, res.NextStep)

	_, err = e.uc.RequestEmailOTP(ctx, "Unew", "nok@example.com", "")
	require.NoError(t, err)
	out, err := e.uc.VerifyEmailOTP(ctx, "Unew", "nok@example.com", e.mail.code)
	require.NoError(t, err)
	require.False(t, out.ExistingMember)
	require.Equal(t, StepRegister, out.NextStep)
}

func TestVerifiedEmailOfExistingMemberLeadsToLink(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.seedMember(t, "", "old@example.com", domainmember.StatusActive)

	_, err := e.uc.RequestEmailOTP(ctx, "Unew", "old@example.com", "")
	require.NoError(t, err)
	out, err := e.uc.VerifyEmailOTP(ctx, "Unew", "OLD@example.com", e.mail.code)
	require.NoError(t, err)
	require.True(t, out.ExistingMember)
	require.Equal(t, StepLink, out.NextStep)
	require.Equal(t, "old@example.com", out.Email)
}

func TestLinkedMemberGoesHomeUnlessNewConsent(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.seedMember(t, "Umember", "m@example.com", domainmember.StatusActive)

	res, err := e.uc.LoginWithLine(ctx, "tok-member")
	require.NoError(t, err)
	require.NotNil(t, res.Member)
	require.Equal(t, StepHome, res.NextStep)

	e.publishTerms(t)
	res, err = e.uc.LoginWithLine(ctx, "tok-member")
	require.NoError(t, err)
	require.Equal(t, StepConsent, res.NextStep, "a new required version blocks members too")
}

func TestSuspendedMemberCannotLogIn(t *testing.T) {
	e := setup(t)
	e.seedMember(t, "Ususp", "s@example.com", domainmember.StatusSuspended)
	_, err := e.uc.LoginWithLine(context.Background(), "tok-suspended")
	require.ErrorIs(t, err, domain.ErrMemberNotAllowed)
}
