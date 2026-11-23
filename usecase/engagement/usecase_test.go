package engagement_test

import (
	"context"
	"testing"
	"time"

	"crm-core/database"
	domainearnrule "crm-core/domain/earnrule"
	domainlinemsg "crm-core/domain/linemsg"
	domainloyalty "crm-core/domain/loyalty"
	domainmember "crm-core/domain/member"
	domainpoints "crm-core/domain/points"
	domainprivacy "crm-core/domain/privacy"
	"crm-core/migrations"
	"crm-core/repository/postgres"
	usecaseengagement "crm-core/usecase/engagement"
	usecaselinemsg "crm-core/usecase/linemsg"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type env struct {
	db   *gorm.DB
	jobs *usecaseengagement.Jobs
	tier uint
}

func setup(t *testing.T) env {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), database.GormConfig("", logger.Silent))
	require.NoError(t, err)
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, migrations.Run(db, ""))
	var tier domainloyalty.Tier
	require.NoError(t, db.First(&tier).Error)
	privacyRepo := postgres.NewPrivacyRepository(db)
	notifier := usecaselinemsg.NewNotifier(postgres.NewLineOutboxRepository(db), postgres.NewMemberRepository(db), nil, "").WithPrefs(privacyRepo)
	jobs := usecaseengagement.New(postgres.NewEngagementRepository(db), postgres.NewPointsRepository(db), postgres.NewEarnRuleRepository(db),
		func(context.Context) (int, error) { return 12, nil }, notifier)
	return env{db: db, jobs: jobs, tier: tier.ID}
}

func (e env) member(t *testing.T, n int, birth *time.Time) domainmember.Member {
	t.Helper()
	m := domainmember.Member{MemberCode: "M" + string(rune('0'+n)), FirstName: "คน", LastName: "x", Email: string(rune('a'+n)) + "@x.com",
		TierID: e.tier, Status: domainmember.StatusActive, Gender: "unspecified", BirthDate: birth}
	require.NoError(t, e.db.Create(&m).Error)
	require.NoError(t, e.db.Create(&domainmember.MemberLineAccount{MemberID: m.ID, LineUserID: "U" + string(rune('a'+n)), LinkedAt: time.Now()}).Error)
	return m
}

func (e env) pushes(t *testing.T, topic string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, e.db.Model(&domainlinemsg.Outbox{}).Where("topic = ?", topic).Count(&n).Error)
	return n
}

func TestExpiringReminderOncePerDate(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 22, 3, 0, 0, 0, time.UTC)
	m := e.member(t, 1, nil)
	optedOut := e.member(t, 2, nil)
	require.NoError(t, postgres.NewPrivacyRepository(e.db).SavePrefs(ctx, domainprivacy.NotificationPrefs{MemberID: optedOut.ID, PointsUpdates: true, Birthday: true}))
	ledger := postgres.NewPointsRepository(e.db)
	soon := now.Add(3 * 24 * time.Hour)
	later := now.Add(60 * 24 * time.Hour)
	for i, id := range []uint{m.ID, m.ID, optedOut.ID} {
		exp := soon
		if i == 1 {
			exp = later
		}
		_, err := ledger.Earn(ctx, domainpoints.EarnCommand{MemberID: id, Points: 100, SourceType: domainpoints.SourceOrder, SourceID: "o" + string(rune('0'+i)), ExpiresAt: &exp})
		require.NoError(t, err)
	}

	n, err := e.jobs.RemindExpiring(ctx, now)
	require.NoError(t, err)
	require.Equal(t, 2, n)
	require.EqualValues(t, 1, e.pushes(t, domainlinemsg.TopicExpiring), "the opted-out member gets nothing")
	var row domainlinemsg.Outbox
	require.NoError(t, e.db.Where("topic = ?", domainlinemsg.TopicExpiring).First(&row).Error)
	require.Contains(t, row.Payload, "100 แต้ม", "only points inside the 7-day window")

	_, err = e.jobs.RemindExpiring(ctx, now.Add(24*time.Hour))
	require.NoError(t, err)
	require.EqualValues(t, 1, e.pushes(t, domainlinemsg.TopicExpiring), "same expiry date: no second reminder")
}

func TestBirthdayBonusOncePerYear(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	require.NoError(t, e.db.Create(&domainearnrule.EarnRule{Type: domainearnrule.TypeBonusFixed, Trigger: domainearnrule.TriggerBirthday, Points: 200, Multiplier: 1, IsActive: true}).Error)
	bday := time.Date(1995, 11, 22, 0, 0, 0, 0, time.UTC)
	other := time.Date(1995, 11, 23, 0, 0, 0, 0, time.UTC)
	m := e.member(t, 1, &bday)
	e.member(t, 2, &other)
	e.member(t, 3, nil)
	now := time.Date(2026, 11, 22, 1, 0, 0, 0, time.UTC) // 08:00 Bangkok

	n, err := e.jobs.Birthdays(ctx, now)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	n, err = e.jobs.Birthdays(ctx, now.Add(time.Hour))
	require.NoError(t, err)
	require.Zero(t, n, "the job running twice credits once")

	var got domainmember.Member
	require.NoError(t, e.db.First(&got, m.ID).Error)
	require.EqualValues(t, 200, got.PointBalance)
	require.EqualValues(t, 1, e.pushes(t, domainlinemsg.TopicBirthday))
}

func TestLeapDayBirthdaysOnFeb28(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	require.NoError(t, e.db.Create(&domainearnrule.EarnRule{Type: domainearnrule.TypeBonusFixed, Trigger: domainearnrule.TriggerBirthday, Points: 10, Multiplier: 1, IsActive: true}).Error)
	leap := time.Date(2000, 2, 29, 0, 0, 0, 0, time.UTC)
	e.member(t, 1, &leap)
	n, err := e.jobs.Birthdays(ctx, time.Date(2027, 2, 28, 2, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, 1, n, "28 Feb in a non-leap year")
	n, err = e.jobs.Birthdays(ctx, time.Date(2028, 2, 28, 2, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Zero(t, n, "leap years celebrate on 29 Feb")
}
