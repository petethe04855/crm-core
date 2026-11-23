package profile

import (
	"context"
	"testing"
	"time"

	"crm-core/database"
	"crm-core/domain"
	domainloyalty "crm-core/domain/loyalty"
	domainmember "crm-core/domain/member"
	domainotp "crm-core/domain/otp"
	"crm-core/migrations"
	"crm-core/repository/postgres"
	usecaseotp "crm-core/usecase/otp"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var now = time.Date(2026, 11, 22, 3, 0, 0, 0, time.UTC)

type mailbox struct{ code string }

func (m *mailbox) SendOTP(_ context.Context, _, code, _ string, _ time.Duration) error {
	m.code = code
	return nil
}

type env struct {
	uc   Usecase
	db   *gorm.DB
	mail *mailbox
	m    domainmember.Member
}

func setup(t *testing.T) env {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), database.GormConfig("", logger.Silent))
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, migrations.Run(db, ""))
	var tier domainloyalty.Tier
	require.NoError(t, db.First(&tier).Error)
	m := domainmember.Member{MemberCode: "M2611-000001", FirstName: "นก", LastName: "ทดสอบ", Email: "nok@example.com",
		TierID: tier.ID, Status: domainmember.StatusActive, Gender: domainmember.GenderUnspecified}
	require.NoError(t, db.Create(&m).Error)
	mail := &mailbox{}
	otps := usecaseotp.New(postgres.NewOTPRepository(db), mail, []byte("secret"), usecaseotp.WithClock(func() time.Time { return now }))
	uc := New(postgres.NewProfileRepository(db), otps, WithClock(func() time.Time { return now }))
	return env{uc: uc, db: db, mail: mail, m: m}
}

func date(y, m, d int) *time.Time {
	t := time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
	return &t
}

func TestUpdateProfileAndBirthDateOnce(t *testing.T) {
	e := setup(t)
	ctx := context.Background()

	m, err := e.uc.UpdateProfile(ctx, e.m.ID, Update{FirstName: "นกน้อย", LastName: "ทดสอบ", Phone: "0898765432", Gender: "female", BirthDate: date(1995, 5, 20)})
	require.NoError(t, err)
	require.Equal(t, "นกน้อย", m.FirstName)
	require.Equal(t, "+66898765432", *m.PhoneE164)
	require.Nil(t, m.BirthDateChangedAt, "setting the first birth date is not a correction")

	m, err = e.uc.UpdateProfile(ctx, e.m.ID, Update{FirstName: "นกน้อย", LastName: "ทดสอบ", BirthDate: date(1995, 5, 21)})
	require.NoError(t, err)
	require.NotNil(t, m.BirthDateChangedAt)
	require.Nil(t, m.PhoneE164, "an empty phone clears it")

	_, err = e.uc.UpdateProfile(ctx, e.m.ID, Update{FirstName: "a", LastName: "b", BirthDate: date(1995, 5, 22)})
	require.ErrorIs(t, err, domainmember.ErrBirthDateLocked)

	m, err = e.uc.UpdateProfile(ctx, e.m.ID, Update{FirstName: "ok", LastName: "b", BirthDate: date(1995, 5, 21)})
	require.NoError(t, err, "re-sending the same date is not a change")
	require.Equal(t, "ok", m.FirstName)

	_, err = e.uc.UpdateProfile(ctx, e.m.ID, Update{FirstName: "", LastName: "b"})
	require.ErrorIs(t, err, domain.ErrValidation)
}

func TestChangeEmailWithOTP(t *testing.T) {
	e := setup(t)
	ctx := context.Background()

	_, err := e.uc.RequestEmailChange(ctx, e.m.ID, "U1", "NOK@example.com", "")
	require.ErrorIs(t, err, domain.ErrValidation, "same as the current email")

	res, err := e.uc.RequestEmailChange(ctx, e.m.ID, "U1", "new@example.com", "")
	require.NoError(t, err)
	require.Equal(t, "ne*@example.com", res.MaskedEmail)

	_, err = e.uc.ConfirmEmailChange(ctx, e.m.ID, "U1", "new@example.com", "000000")
	if e.mail.code != "000000" {
		require.ErrorIs(t, err, domainotp.ErrInvalidCode)
	}
	m, err := e.uc.ConfirmEmailChange(ctx, e.m.ID, "U1", "new@example.com", e.mail.code)
	require.NoError(t, err)
	require.Equal(t, "new@example.com", m.Email)
	require.NotNil(t, m.EmailVerifiedAt)
}

func TestChangeEmailToTakenAddress(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	other := domainmember.Member{MemberCode: "M2611-000002", FirstName: "a", LastName: "b", Email: "taken@example.com",
		TierID: e.m.TierID, Status: domainmember.StatusActive, Gender: domainmember.GenderUnspecified}
	require.NoError(t, e.db.Create(&other).Error)

	_, err := e.uc.RequestEmailChange(ctx, e.m.ID, "U1", "taken@example.com", "")
	require.NoError(t, err, "the OTP is sent without revealing the address is taken")
	_, err = e.uc.ConfirmEmailChange(ctx, e.m.ID, "U1", "taken@example.com", e.mail.code)
	require.ErrorIs(t, err, domainmember.ErrEmailInUse)
}

func addr(name string, def bool) AddressInput {
	return AddressInput{Recipient: name, Phone: "081-234-5678", Address: "99/1 ถ.สุขุมวิท", Subdistrict: "คลองเตย",
		District: "คลองเตย", Province: "กรุงเทพมหานคร", Postcode: "10110", IsDefault: def}
}

func TestAddressesDefaultRules(t *testing.T) {
	e := setup(t)
	ctx := context.Background()

	first, err := e.uc.AddAddress(ctx, e.m.ID, addr("บ้าน", false))
	require.NoError(t, err)
	require.True(t, first.IsDefault, "the first address becomes the default")
	require.Equal(t, "+66812345678", first.Phone)

	second, err := e.uc.AddAddress(ctx, e.m.ID, addr("ที่ทำงาน", true))
	require.NoError(t, err)
	list, err := e.uc.Addresses(ctx, e.m.ID)
	require.NoError(t, err)
	require.Len(t, list, 2)
	require.Equal(t, second.ID, list[0].ID)
	require.True(t, list[0].IsDefault)
	require.False(t, list[1].IsDefault, "only one default")

	_, err = e.uc.UpdateAddress(ctx, e.m.ID, second.ID, addr("ที่ทำงานใหม่", false))
	require.NoError(t, err)
	list, err = e.uc.Addresses(ctx, e.m.ID)
	require.NoError(t, err)
	require.True(t, list[0].IsDefault, "un-ticking default on the default keeps one default")
	require.Equal(t, "ที่ทำงานใหม่", list[0].Recipient)

	require.NoError(t, e.uc.DeleteAddress(ctx, e.m.ID, second.ID))
	list, err = e.uc.Addresses(ctx, e.m.ID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.True(t, list[0].IsDefault, "deleting the default promotes another")
}

func TestAddressValidationOwnershipAndLimit(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	bad := addr("x", false)
	bad.Postcode = "1011"
	_, err := e.uc.AddAddress(ctx, e.m.ID, bad)
	require.ErrorIs(t, err, domain.ErrValidation)
	bad = addr("x", false)
	bad.Phone = ""
	_, err = e.uc.AddAddress(ctx, e.m.ID, bad)
	require.ErrorIs(t, err, domain.ErrValidation)

	mine, err := e.uc.AddAddress(ctx, e.m.ID, addr("บ้าน", false))
	require.NoError(t, err)
	other := domainmember.Member{MemberCode: "M2", FirstName: "a", LastName: "b", Email: "o@x.com", TierID: e.m.TierID, Status: "active", Gender: "unspecified"}
	require.NoError(t, e.db.Create(&other).Error)
	_, err = e.uc.UpdateAddress(ctx, other.ID, mine.ID, addr("hack", false))
	require.ErrorIs(t, err, domain.ErrNotFound, "another member's address is invisible")
	require.ErrorIs(t, e.uc.DeleteAddress(ctx, other.ID, mine.ID), domain.ErrNotFound)

	for i := 1; i < domainmember.MaxAddresses; i++ {
		_, err = e.uc.AddAddress(ctx, e.m.ID, addr("a", false))
		require.NoError(t, err)
	}
	_, err = e.uc.AddAddress(ctx, e.m.ID, addr("too many", false))
	require.ErrorIs(t, err, domainmember.ErrAddressLimit)
}

func TestSuspendedMemberCannotEdit(t *testing.T) {
	e := setup(t)
	require.NoError(t, e.db.Model(&domainmember.Member{}).Where("id = ?", e.m.ID).Update("status", domainmember.StatusSuspended).Error)
	_, err := e.uc.UpdateProfile(context.Background(), e.m.ID, Update{FirstName: "a", LastName: "b"})
	require.ErrorIs(t, err, domain.ErrMemberNotAllowed)
}
