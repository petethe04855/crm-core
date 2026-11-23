package privacy

import (
	"context"
	"testing"
	"time"

	"crm-core/database"
	"crm-core/domain"
	domainconsent "crm-core/domain/consent"
	domainlinemsg "crm-core/domain/linemsg"
	domainloyalty "crm-core/domain/loyalty"
	domainmember "crm-core/domain/member"
	domainpoints "crm-core/domain/points"
	domainprivacy "crm-core/domain/privacy"
	domainreward "crm-core/domain/reward"
	"crm-core/migrations"
	"crm-core/repository/postgres"
	usecaseconsent "crm-core/usecase/consent"
	usecaselinemsg "crm-core/usecase/linemsg"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type env struct {
	uc       Usecase
	db       *gorm.DB
	consents usecaseconsent.Usecase
	notifier *usecaselinemsg.Notifier
	m        domainmember.Member
}

var staff = domain.Actor{ERPUserID: 9, Roles: []string{domain.ERPRoleOwner}}

func setup(t *testing.T) env {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), database.GormConfig("", logger.Silent))
	require.NoError(t, err)
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, migrations.Run(db, ""))
	var tier domainloyalty.Tier
	require.NoError(t, db.First(&tier).Error)
	phone := "+66812345678"
	m := domainmember.Member{MemberCode: "M2611-000001", FirstName: "นก", LastName: "ทดสอบ", Email: "nok@example.com", PhoneE164: &phone,
		TierID: tier.ID, Status: domainmember.StatusActive, Gender: "female"}
	require.NoError(t, db.Create(&m).Error)
	require.NoError(t, db.Create(&domainmember.MemberLineAccount{MemberID: m.ID, LineUserID: "U1", DisplayName: "Nok", LinkedAt: time.Now()}).Error)
	require.NoError(t, db.Create(&domainmember.MemberAddress{MemberID: m.ID, Recipient: "นก", Phone: phone, Address: "99/1", Subdistrict: "a", District: "b", Province: "c", Postcode: "10110", IsDefault: true}).Error)
	_, err = postgres.NewPointsRepository(db).Earn(context.Background(), domainpoints.EarnCommand{MemberID: m.ID, Points: 50, SourceType: domainpoints.SourceOrder, SourceID: "o1"})
	require.NoError(t, err)

	consents := usecaseconsent.New(postgres.NewConsentRepository(db))
	privacyRepo := postgres.NewPrivacyRepository(db)
	notifier := usecaselinemsg.NewNotifier(postgres.NewLineOutboxRepository(db), postgres.NewMemberRepository(db), consents, "").WithPrefs(privacyRepo)
	uc := New(privacyRepo, postgres.NewPrivacyUnitOfWork(db), Sources{
		Members: postgres.NewMemberRepository(db), Profiles: postgres.NewProfileRepository(db), Consents: consents,
		Points: postgres.NewPointsRepository(db), Coupons: postgres.NewCouponRepository(db), Rewards: postgres.NewRewardRepository(db),
		Codes: postgres.NewProductCodeAttemptRepository(db), MemberCards: postgres.NewMemberCardRepository(db),
	}, WithMenuReset(notifier))
	return env{uc: uc, db: db, consents: consents, notifier: notifier, m: m}
}

func TestSettingsPrefsAndMarketingWithdrawal(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	s, err := e.uc.Settings(ctx, e.m.ID, "U1")
	require.NoError(t, err)
	require.True(t, s.Prefs.PointsUpdates, "defaults are on")
	require.False(t, s.MarketingLineAvailable)

	past := time.Now().Add(-time.Minute)
	doc, err := e.consents.CreateDocument(ctx, staff, usecaseconsent.DocumentInput{Type: domainconsent.TypeMarketingLine, Title: "ข่าวสาร", Content: "x", IsActive: true, EffectiveAt: &past})
	require.NoError(t, err)
	require.NoError(t, e.consents.Accept(ctx, usecaseconsent.AcceptInput{LineUserID: "U1", DocumentIDs: []uint{doc.ID}}))

	no := false
	s, err = e.uc.UpdateSettings(ctx, e.m.ID, "U1", SettingsInput{PointsUpdates: false, ExpiringPoints: true, Birthday: true, MarketingLine: &no})
	require.NoError(t, err)
	require.False(t, s.Prefs.PointsUpdates)
	require.True(t, s.MarketingLineAvailable)
	require.False(t, s.MarketingLine, "marketing consent withdrawn")

	var recs []domainconsent.Record
	require.NoError(t, e.db.Order("id").Find(&recs).Error)
	require.Equal(t, domainconsent.ActionWithdraw, recs[len(recs)-1].Action)

	// Prefs gate optional messages.
	require.NoError(t, e.notifier.PointsEarned(ctx, e.m.ID, 10, 60, "r"))
	var queued int64
	require.NoError(t, e.db.Model(&domainlinemsg.Outbox{}).Count(&queued).Error)
	require.Zero(t, queued, "points updates are off")
	require.ErrorIs(t, e.notifier.Marketing(ctx, e.m.ID, "โปร", "/", "k"), usecaselinemsg.ErrNoMarketingConsent)
}

func TestExportHoldsTheMembersData(t *testing.T) {
	e := setup(t)
	ex, err := e.uc.Export(context.Background(), e.m.ID)
	require.NoError(t, err)
	require.Equal(t, "nok@example.com", ex.Member.Email)
	require.Len(t, ex.LineAccounts, 1)
	require.Len(t, ex.Addresses, 1)
	require.Len(t, ex.Points, 1)
	require.EqualValues(t, 50, ex.Member.PointBalance)
}

func TestDeletionRequestAndAnonymize(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	req, err := e.uc.RequestDeletion(ctx, e.m.ID, "ไม่ใช้แล้ว")
	require.NoError(t, err)
	_, err = e.uc.RequestDeletion(ctx, e.m.ID, "")
	require.ErrorIs(t, err, domainprivacy.ErrRequestPending)
	pending, err := e.uc.PendingDeletion(ctx, e.m.ID)
	require.NoError(t, err)
	require.Equal(t, req.ID, pending.ID)

	// An unshipped redemption blocks the deletion.
	rd := domainreward.Redemption{RedemptionNo: "RD1", MemberID: e.m.ID, RewardID: 1, Qty: 1, Status: domainreward.RedemptionPending,
		ShipName: "นก", ShipAddress: "99/1", RewardName: "x", WarehouseID: 1}
	require.NoError(t, e.db.Create(&rd).Error)
	require.ErrorIs(t, e.uc.CompleteDeletion(ctx, staff, req.ID, ""), domainprivacy.ErrOpenRedemptions)
	require.NoError(t, e.db.Model(&rd).Update("status", domainreward.RedemptionDelivered).Error)

	require.NoError(t, e.uc.CompleteDeletion(ctx, staff, req.ID, "ตรวจแล้ว"))
	require.ErrorIs(t, e.uc.CompleteDeletion(ctx, staff, req.ID, ""), domainprivacy.ErrRequestClosed)

	var m domainmember.Member
	require.NoError(t, e.db.First(&m, e.m.ID).Error)
	require.Equal(t, domainmember.StatusDeleted, m.Status)
	require.NotContains(t, m.Email, "nok")
	require.Nil(t, m.PhoneE164)
	require.NotEqual(t, "นก", m.FirstName)
	require.EqualValues(t, 50, m.PointBalance, "the ledger and balance are kept")

	var acc domainmember.MemberLineAccount
	require.NoError(t, e.db.Where("member_id = ?", e.m.ID).First(&acc).Error)
	require.NotNil(t, acc.UnlinkedAt)
	require.Empty(t, acc.DisplayName)
	var addresses, ledgers int64
	require.NoError(t, e.db.Model(&domainmember.MemberAddress{}).Count(&addresses).Error)
	require.NoError(t, e.db.Model(&domainpoints.PointLedger{}).Count(&ledgers).Error)
	require.Zero(t, addresses)
	require.EqualValues(t, 1, ledgers)
	var shipped domainreward.Redemption
	require.NoError(t, e.db.First(&shipped, rd.ID).Error)
	require.Equal(t, "-", shipped.ShipAddress)

	var menu domainlinemsg.Outbox
	require.NoError(t, e.db.Where("kind = ?", domainlinemsg.KindUnlinkMenu).First(&menu).Error)
	require.Equal(t, "U1", menu.LineUserID)

	// The LINE user is free to join again later; the deleted member cannot act.
	_, err = e.uc.Settings(ctx, e.m.ID, "U1")
	require.ErrorIs(t, err, domain.ErrMemberNotAllowed)

	rows, total, err := e.uc.Requests(ctx, domainprivacy.RequestQuery{Status: domainprivacy.RequestCompleted})
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Equal(t, "M2611-000001", rows[0].MemberCode, "the code snapshot survives anonymizing")
}

func TestRejectNeedsReason(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	req, err := e.uc.RequestDeletion(ctx, e.m.ID, "")
	require.NoError(t, err)
	require.ErrorIs(t, e.uc.RejectDeletion(ctx, staff, req.ID, ""), domain.ErrValidation)
	require.NoError(t, e.uc.RejectDeletion(ctx, staff, req.ID, "มีคำสั่งซื้อค้าง"))
	pending, err := e.uc.PendingDeletion(ctx, e.m.ID)
	require.NoError(t, err)
	require.Nil(t, pending)
	_, err = e.uc.RequestDeletion(ctx, e.m.ID, "ขออีกครั้ง")
	require.NoError(t, err, "a new request after a rejection")
}
