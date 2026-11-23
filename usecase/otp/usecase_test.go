package otp

import (
	"context"
	"errors"
	"testing"
	"time"

	"crm-core/database"
	"crm-core/domain"
	domainotp "crm-core/domain/otp"
	"crm-core/migrations"
	"crm-core/repository/postgres"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type fakeMailer struct {
	sent []string // codes, in order
	refs []string
	fail bool
}

func (m *fakeMailer) SendOTP(_ context.Context, _, code, ref string, _ time.Duration) error {
	if m.fail {
		return errors.New("smtp down")
	}
	m.sent = append(m.sent, code)
	m.refs = append(m.refs, ref)
	return nil
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func setup(t *testing.T) (Usecase, *fakeMailer, *clock, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), database.GormConfig("", logger.Silent))
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, migrations.Run(db, ""))
	mailer := &fakeMailer{}
	c := &clock{t: time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)}
	uc := New(postgres.NewOTPRepository(db), mailer, []byte("test-secret"), WithClock(c.now))
	return uc, mailer, c, db
}

func request(t *testing.T, uc Usecase, email, line, ip string) *RequestResult {
	t.Helper()
	res, err := uc.Request(context.Background(), RequestInput{Email: email, Purpose: domainotp.PurposeRegister, LineUserID: line, IP: ip})
	require.NoError(t, err)
	return res
}

func verify(uc Usecase, email, code, line string) (string, error) {
	return uc.Verify(context.Background(), VerifyInput{Email: email, Code: code, Purpose: domainotp.PurposeRegister, LineUserID: line})
}

func TestRequestAndVerify(t *testing.T) {
	uc, mailer, c, db := setup(t)
	res := request(t, uc, "  Nok@Example.COM ", "U1", "1.1.1.1")

	require.Len(t, mailer.sent, 1)
	require.Len(t, mailer.sent[0], 6)
	require.Len(t, res.RefCode, 4)
	require.Equal(t, mailer.refs[0], res.RefCode)
	require.Equal(t, "no*@example.com", res.MaskedEmail)
	require.Equal(t, c.t.Add(10*time.Minute), res.ExpiresAt)
	require.Equal(t, c.t.Add(60*time.Second), res.ResendAt)

	var stored domainotp.Request
	require.NoError(t, db.First(&stored).Error)
	require.NotContains(t, stored.CodeHash, mailer.sent[0], "only the HMAC is stored")
	require.Equal(t, "nok@example.com", stored.Destination)

	email, err := verify(uc, "nok@example.com", mailer.sent[0], "U1")
	require.NoError(t, err)
	require.Equal(t, "nok@example.com", email)

	_, err = verify(uc, "nok@example.com", mailer.sent[0], "U1")
	require.ErrorIs(t, err, domainotp.ErrNoActiveRequest, "a code works once")
}

func TestVerifyIsBoundToLineUser(t *testing.T) {
	uc, mailer, _, _ := setup(t)
	request(t, uc, "nok@example.com", "U1", "")
	_, err := verify(uc, "nok@example.com", mailer.sent[0], "U2")
	require.ErrorIs(t, err, domainotp.ErrNoActiveRequest)
}

func TestWrongCodeCountsAttempts(t *testing.T) {
	uc, mailer, _, _ := setup(t)
	request(t, uc, "nok@example.com", "U1", "")
	wrong := "000000"
	if mailer.sent[0] == wrong {
		wrong = "111111"
	}
	for i := 1; i <= 4; i++ {
		_, err := verify(uc, "nok@example.com", wrong, "U1")
		var ic *domainotp.InvalidCodeError
		require.ErrorAs(t, err, &ic)
		require.Equal(t, 5-i, ic.Remaining)
	}
	_, err := verify(uc, "nok@example.com", wrong, "U1")
	require.ErrorIs(t, err, domainotp.ErrTooManyAttempts)
	_, err = verify(uc, "nok@example.com", mailer.sent[0], "U1")
	require.ErrorIs(t, err, domainotp.ErrTooManyAttempts, "the right code no longer works after 5 misses")
}

func TestExpiredCode(t *testing.T) {
	uc, mailer, c, _ := setup(t)
	request(t, uc, "nok@example.com", "U1", "")
	c.advance(10 * time.Minute)
	_, err := verify(uc, "nok@example.com", mailer.sent[0], "U1")
	require.ErrorIs(t, err, domainotp.ErrExpired)
}

func TestResendCooldownAndOldCodeDies(t *testing.T) {
	uc, mailer, c, _ := setup(t)
	request(t, uc, "nok@example.com", "U1", "")

	_, err := uc.Request(context.Background(), RequestInput{Email: "nok@example.com", Purpose: domainotp.PurposeRegister, LineUserID: "U1"})
	var re *domainotp.ResendError
	require.ErrorAs(t, err, &re)
	require.Equal(t, c.t.Add(60*time.Second), re.RetryAt)

	c.advance(61 * time.Second)
	request(t, uc, "nok@example.com", "U1", "")
	require.Len(t, mailer.sent, 2)

	if mailer.sent[0] != mailer.sent[1] {
		_, err = verify(uc, "nok@example.com", mailer.sent[0], "U1")
		require.Error(t, err, "the first code is superseded")
	}
	_, err = verify(uc, "nok@example.com", mailer.sent[1], "U1")
	require.NoError(t, err)
}

func TestNewEmailSupersedesCodeForPreviousEmail(t *testing.T) {
	uc, mailer, _, _ := setup(t)
	request(t, uc, "first@example.com", "U1", "")
	request(t, uc, "second@example.com", "U1", "")
	_, err := verify(uc, "first@example.com", mailer.sent[0], "U1")
	require.ErrorIs(t, err, domainotp.ErrNoActiveRequest)
}

func TestRateLimitPerEmail(t *testing.T) {
	uc, _, c, _ := setup(t)
	for i := 0; i < 5; i++ {
		request(t, uc, "nok@example.com", "U1", "")
		c.advance(61 * time.Second)
	}
	_, err := uc.Request(context.Background(), RequestInput{Email: "nok@example.com", Purpose: domainotp.PurposeRegister, LineUserID: "U1"})
	require.ErrorIs(t, err, domainotp.ErrRateLimited)

	c.advance(time.Hour)
	request(t, uc, "nok@example.com", "U1", "")
}

func TestRateLimitPerLineUserAndIP(t *testing.T) {
	uc, _, c, _ := setup(t)
	for i := 0; i < 10; i++ {
		request(t, uc, "a"+string(rune('a'+i))+"@example.com", "U1", "")
		c.advance(time.Second)
	}
	_, err := uc.Request(context.Background(), RequestInput{Email: "z@example.com", Purpose: domainotp.PurposeRegister, LineUserID: "U1"})
	require.ErrorIs(t, err, domainotp.ErrRateLimited)

	for i := 0; i < 20; i++ {
		request(t, uc, "b"+string(rune('a'+i))+"@example.com", "IPUSER"+string(rune('a'+i)), "9.9.9.9")
	}
	_, err = uc.Request(context.Background(), RequestInput{Email: "y@example.com", Purpose: domainotp.PurposeRegister, LineUserID: "U9", IP: "9.9.9.9"})
	require.ErrorIs(t, err, domainotp.ErrRateLimited)
}

func TestSendFailureLetsMemberRetryAtOnce(t *testing.T) {
	uc, mailer, _, _ := setup(t)
	mailer.fail = true
	_, err := uc.Request(context.Background(), RequestInput{Email: "nok@example.com", Purpose: domainotp.PurposeRegister, LineUserID: "U1"})
	require.ErrorIs(t, err, domainotp.ErrSendFailed)
	mailer.fail = false
	request(t, uc, "nok@example.com", "U1", "")
}

func TestRejectsBadEmails(t *testing.T) {
	uc, _, _, _ := setup(t)
	for _, email := range []string{"", "nok", "nok@", "Nok <nok@example.com>", "nok@localhost", "a@b@c.com"} {
		_, err := uc.Request(context.Background(), RequestInput{Email: email, Purpose: domainotp.PurposeRegister, LineUserID: "U1"})
		require.ErrorIs(t, err, domain.ErrValidation, email)
	}
	_, err := uc.Request(context.Background(), RequestInput{Email: "x@mailinator.com", Purpose: domainotp.PurposeRegister, LineUserID: "U1"})
	require.ErrorIs(t, err, domainotp.ErrInvalidEmail)
}

func TestDomainCheckerRejectsUndeliverable(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), database.GormConfig("", logger.Silent))
	require.NoError(t, err)
	require.NoError(t, migrations.Run(db, ""))
	uc := New(postgres.NewOTPRepository(db), &fakeMailer{}, []byte("s"), WithDomainChecker(func(_ context.Context, host string) error {
		if host == "no-mx.example" {
			return errors.New("no mx")
		}
		return nil
	}))
	_, err = uc.Request(context.Background(), RequestInput{Email: "a@no-mx.example", Purpose: domainotp.PurposeRegister, LineUserID: "U1"})
	require.ErrorIs(t, err, domainotp.ErrInvalidEmail)
}

func TestVerifyValidatesCodeFormat(t *testing.T) {
	uc, _, _, _ := setup(t)
	_, err := verify(uc, "nok@example.com", "12ab56", "U1")
	require.ErrorIs(t, err, domain.ErrValidation)
}
