package registration

import (
	"context"
	"regexp"
	"sync"
	"testing"
	"time"

	"crm-core/database"
	"crm-core/domain"
	domainearnrule "crm-core/domain/earnrule"
	domainloyalty "crm-core/domain/loyalty"
	domainmember "crm-core/domain/member"
	domainpoints "crm-core/domain/points"
	domainregistration "crm-core/domain/registration"
	"crm-core/migrations"
	"crm-core/repository/postgres"
	usecasemembercard "crm-core/usecase/membercard"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var now = time.Date(2026, 11, 21, 3, 0, 0, 0, time.UTC) // 10:00 Bangkok

func setup(t *testing.T) (Usecase, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), database.GormConfig("", logger.Silent))
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, migrations.Run(db, ""))
	uc := New(postgres.NewRegistrationUnitOfWork(db), postgres.NewTierRepository(db), postgres.NewEarnRuleRepository(db),
		func(context.Context) (int, error) { return 12, nil }, WithClock(func() time.Time { return now }))
	return uc, db
}

func signupRule(t *testing.T, db *gorm.DB, points int64) {
	t.Helper()
	require.NoError(t, db.Create(&domainearnrule.EarnRule{Type: domainearnrule.TypeBonusFixed, Trigger: domainearnrule.TriggerSignup, Points: points, Multiplier: 1, IsActive: true}).Error)
}

func input(line, email string) RegisterInput {
	return RegisterInput{
		Line:    LineInfo{UserID: line, DisplayName: "Nok", PictureURL: "https://profile.line-scdn.net/x"},
		Email:   email,
		Profile: Profile{FirstName: " นก ", LastName: "ทดสอบ", Phone: "081-234-5678"},
	}
}

func TestRegisterCreatesMemberLinkTierHistoryAndBonus(t *testing.T) {
	uc, db := setup(t)
	signupRule(t, db, 50)

	res, err := uc.Register(context.Background(), input("U1", "Nok@Example.com"))
	require.NoError(t, err)
	m := res.Member
	require.Regexp(t, regexp.MustCompile(`^M2611-\d{6}$`), m.MemberCode)
	require.Equal(t, "นก", m.FirstName)
	require.Equal(t, "nok@example.com", m.Email)
	require.NotNil(t, m.EmailVerifiedAt)
	require.Equal(t, "+66812345678", *m.PhoneE164)
	require.Equal(t, domainmember.GenderUnspecified, m.Gender)
	require.EqualValues(t, 50, res.WelcomePoints)

	var stored domainmember.Member
	require.NoError(t, db.First(&stored, m.ID).Error)
	require.EqualValues(t, 50, stored.PointBalance)
	require.Equal(t, m.MemberCode, stored.MemberCode)

	var tier domainloyalty.Tier
	require.NoError(t, db.First(&tier, stored.TierID).Error)
	require.Equal(t, domainloyalty.DefaultTierCode, tier.Code)

	var link domainmember.MemberLineAccount
	require.NoError(t, db.Where("line_user_id = ?", "U1").First(&link).Error)
	require.Equal(t, m.ID, link.MemberID)

	var hist []domainregistration.TierHistory
	require.NoError(t, db.Find(&hist).Error)
	require.Len(t, hist, 1)
	require.Nil(t, hist[0].FromTierID)
	require.Equal(t, domainregistration.TierReasonSignup, hist[0].Reason)

	var lot domainpoints.PointLot
	require.NoError(t, db.First(&lot).Error)
	require.Equal(t, now.AddDate(0, 12, 0).Unix(), lot.ExpiresAt.Unix())

	// The welcome bonus is unique per member (ledger source triple).
	_, err = postgres.NewPointsRepository(db).Earn(context.Background(), domainpoints.EarnCommand{
		MemberID: m.ID, Points: 50, SourceType: domainpoints.SourceBonus, SourceID: domainregistration.SignupSourcePrefix + "1",
	})
	require.ErrorIs(t, err, domain.ErrConflict)
}

func TestRegisterWithoutSignupRuleGivesNoPoints(t *testing.T) {
	uc, db := setup(t)
	res, err := uc.Register(context.Background(), input("U1", "a@example.com"))
	require.NoError(t, err)
	require.Zero(t, res.WelcomePoints)
	var count int64
	require.NoError(t, db.Model(&domainpoints.PointLedger{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestRegisterRejectsTakenEmailAndLinkedLine(t *testing.T) {
	uc, _ := setup(t)
	ctx := context.Background()
	_, err := uc.Register(ctx, input("U1", "a@example.com"))
	require.NoError(t, err)

	_, err = uc.Register(ctx, input("U2", "A@example.com"))
	require.ErrorIs(t, err, domainregistration.ErrEmailTaken)

	_, err = uc.Register(ctx, input("U1", "b@example.com"))
	require.ErrorIs(t, err, domainregistration.ErrLineAlreadyLinked, "one LINE account cannot hold two members")
}

func TestConcurrentRegisterSameEmailMakesOneMember(t *testing.T) {
	uc, db := setup(t)
	signupRule(t, db, 20)
	var wg sync.WaitGroup
	errs := make([]error, 5)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = uc.Register(context.Background(), input("U"+string(rune('a'+i)), "same@example.com"))
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
		} else {
			require.ErrorIs(t, err, domainregistration.ErrEmailTaken)
		}
	}
	require.Equal(t, 1, ok)
	var members, ledgers int64
	require.NoError(t, db.Model(&domainmember.Member{}).Count(&members).Error)
	require.NoError(t, db.Model(&domainpoints.PointLedger{}).Count(&ledgers).Error)
	require.EqualValues(t, 1, members)
	require.EqualValues(t, 1, ledgers)
}

func TestProfileValidation(t *testing.T) {
	uc, _ := setup(t)
	ctx := context.Background()
	cases := map[string]Profile{
		"no first name": {LastName: "x"},
		"bad phone":     {FirstName: "a", LastName: "b", Phone: "12345"},
		"bad gender":    {FirstName: "a", LastName: "b", Gender: "robot"},
		"future birth":  {FirstName: "a", LastName: "b", BirthDate: ptr(now.AddDate(0, 0, 2))},
		"under ten":     {FirstName: "a", LastName: "b", BirthDate: ptr(now.AddDate(-9, 0, 0))},
	}
	for name, p := range cases {
		_, err := uc.Register(ctx, RegisterInput{Line: LineInfo{UserID: "U-" + name}, Email: name + "@x.com", Profile: p})
		require.ErrorIs(t, err, domain.ErrValidation, name)
	}
	ten := now.AddDate(-10, 0, 0)
	_, err := uc.Register(ctx, RegisterInput{Line: LineInfo{UserID: "U10"}, Email: "ten@x.com",
		Profile: Profile{FirstName: "a", LastName: "b", BirthDate: &ten, Gender: "female"}})
	require.NoError(t, err, "exactly 10 years old is allowed")

	_, err = uc.Register(ctx, RegisterInput{Line: LineInfo{UserID: "U11"}, Profile: Profile{FirstName: "a", LastName: "b"}})
	require.ErrorIs(t, err, domain.ErrEmailNotVerified)
}

func TestLinkExistingMemberMovesOldLine(t *testing.T) {
	uc, db := setup(t)
	ctx := context.Background()
	reg, err := uc.Register(ctx, input("Uold", "m@example.com"))
	require.NoError(t, err)

	res, err := uc.LinkLine(ctx, LinkInput{Line: LineInfo{UserID: "Unew"}, Email: "M@example.com"})
	require.NoError(t, err)
	require.Equal(t, reg.Member.ID, res.Member.ID)
	require.True(t, res.MovedFromOtherLine)

	var old, cur domainmember.MemberLineAccount
	require.NoError(t, db.Where("line_user_id = ?", "Uold").First(&old).Error)
	require.NotNil(t, old.UnlinkedAt, "the old link is kept as history")
	require.NoError(t, db.Where("line_user_id = ?", "Unew").First(&cur).Error)
	require.Nil(t, cur.UnlinkedAt)

	// The old LINE account can register again as a new member later; its row
	// is reused because line_user_id is unique.
	again, err := uc.Register(ctx, input("Uold", "other@example.com"))
	require.NoError(t, err)
	var reused domainmember.MemberLineAccount
	require.NoError(t, db.Where("line_user_id = ?", "Uold").First(&reused).Error)
	require.Equal(t, again.Member.ID, reused.MemberID)
	require.Nil(t, reused.UnlinkedAt)

	// Linking again is a no-op.
	res, err = uc.LinkLine(ctx, LinkInput{Line: LineInfo{UserID: "Unew"}, Email: "m@example.com"})
	require.NoError(t, err)
	require.False(t, res.MovedFromOtherLine)
}

func TestLinkRejects(t *testing.T) {
	uc, db := setup(t)
	ctx := context.Background()
	_, err := uc.LinkLine(ctx, LinkInput{Line: LineInfo{UserID: "U1"}, Email: "nobody@example.com"})
	require.ErrorIs(t, err, domainregistration.ErrEmailNotMember)

	a, err := uc.Register(ctx, input("Ua", "a@example.com"))
	require.NoError(t, err)
	_, err = uc.Register(ctx, input("Ub", "b@example.com"))
	require.NoError(t, err)
	_, err = uc.LinkLine(ctx, LinkInput{Line: LineInfo{UserID: "Ub"}, Email: "a@example.com"})
	require.ErrorIs(t, err, domainregistration.ErrLineAlreadyLinked, "LINE b already belongs to member b")

	require.NoError(t, db.Model(&domainmember.Member{}).Where("id = ?", a.Member.ID).Update("status", domainmember.StatusSuspended).Error)
	_, err = uc.LinkLine(ctx, LinkInput{Line: LineInfo{UserID: "Uz"}, Email: "a@example.com"})
	require.ErrorIs(t, err, domain.ErrMemberNotAllowed)
}

func TestMemberCard(t *testing.T) {
	uc, db := setup(t)
	signupRule(t, db, 30)
	silver := domainloyalty.Tier{Code: "silver", Name: "Silver", MinSpend: 5000, EarnMultiplier: 1.2, SortOrder: 1, IsActive: true}
	gold := domainloyalty.Tier{Code: "gold", Name: "Gold", MinSpend: 20000, EarnMultiplier: 1.5, SortOrder: 2, IsActive: true}
	require.NoError(t, db.Create(&silver).Error)
	require.NoError(t, db.Create(&gold).Error)

	reg, err := uc.Register(context.Background(), input("U1", "a@example.com"))
	require.NoError(t, err)
	require.NoError(t, db.Model(&domainmember.Member{}).Where("id = ?", reg.Member.ID).Update("lifetime_spend", 1250).Error)
	// Make the welcome lot expire in 10 days.
	soon := time.Now().Add(10 * 24 * time.Hour)
	require.NoError(t, db.Model(&domainpoints.PointLot{}).Where("member_id = ?", reg.Member.ID).Update("expires_at", soon).Error)

	cards := usecasemembercard.New(postgres.NewMemberIdentityRepository(db), postgres.NewTierRepository(db), postgres.NewMemberCardRepository(db))
	card, err := cards.Card(context.Background(), reg.Member.ID)
	require.NoError(t, err)
	require.Equal(t, "bronze", card.Tier.Code)
	require.Equal(t, "silver", card.NextTier.Code)
	require.InDelta(t, 3750, card.SpendToNext, 0.001)
	require.InDelta(t, 0.25, card.Progress, 0.001)
	require.EqualValues(t, 30, card.ExpiringPoints)
	require.NotNil(t, card.ExpiringEarliest)
	require.EqualValues(t, 30, card.Member.PointBalance)
}

func ptr(t time.Time) *time.Time { return &t }
