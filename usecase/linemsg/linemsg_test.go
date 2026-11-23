package linemsg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"crm-core/database"
	"crm-core/domain"
	domainconsent "crm-core/domain/consent"
	domainlinemsg "crm-core/domain/linemsg"
	domainloyalty "crm-core/domain/loyalty"
	domainmember "crm-core/domain/member"
	"crm-core/migrations"
	"crm-core/repository/postgres"
	usecaseconsent "crm-core/usecase/consent"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type env struct {
	db       *gorm.DB
	outbox   domainlinemsg.OutboxRepository
	notifier *Notifier
	consents usecaseconsent.Usecase
	member   domainmember.Member
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
	m := domainmember.Member{MemberCode: "M2611-000001", FirstName: "นก", LastName: "ท", Email: "n@x.com", TierID: tier.ID, Status: "active", Gender: "unspecified"}
	require.NoError(t, db.Create(&m).Error)
	require.NoError(t, db.Create(&domainmember.MemberLineAccount{MemberID: m.ID, LineUserID: "U1", LinkedAt: time.Now()}).Error)
	outbox := postgres.NewLineOutboxRepository(db)
	consents := usecaseconsent.New(postgres.NewConsentRepository(db))
	n := NewNotifier(outbox, postgres.NewMemberRepository(db), consents, "https://liff.line.me/123-abc")
	return env{db: db, outbox: outbox, notifier: n, consents: consents, member: m}
}

func (e env) rows(t *testing.T) []domainlinemsg.Outbox {
	t.Helper()
	var rows []domainlinemsg.Outbox
	require.NoError(t, e.db.Order("id").Find(&rows).Error)
	return rows
}

func TestJoinedQueuesMenuAndWelcomeOnce(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	require.NoError(t, e.notifier.Joined(ctx, "U1", e.member.ID, "นก", "M2611-000001", 50, false))
	require.NoError(t, e.notifier.Joined(ctx, "U1", e.member.ID, "นก", "M2611-000001", 50, false), "retry is deduped")

	rows := e.rows(t)
	require.Len(t, rows, 2)
	require.Equal(t, domainlinemsg.KindLinkMenu, rows[0].Kind)
	require.Equal(t, domainlinemsg.KindPush, rows[1].Kind)
	var msgs []map[string]string
	require.NoError(t, json.Unmarshal([]byte(rows[1].Payload), &msgs))
	require.Contains(t, msgs[0]["text"], "M2611-000001")
	require.Contains(t, msgs[0]["text"], "แต้มต้อนรับ 50 แต้ม")
	require.Contains(t, msgs[0]["text"], "https://liff.line.me/123-abc/home")
}

func TestEventMessagesUseTheMembersLine(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	require.NoError(t, e.notifier.PointsEarned(ctx, e.member.ID, 1200, 3450, "r1"))
	require.NoError(t, e.notifier.TierUpgraded(ctx, e.member.ID, 2, "Silver"))
	require.NoError(t, e.notifier.RedemptionCreated(ctx, e.member.ID, "RD2611-000001", "ขนมไก่", 40))
	require.NoError(t, e.notifier.RedemptionShipped(ctx, e.member.ID, "RD2611-000001", "ขนมไก่", "TH123"))
	require.NoError(t, e.notifier.RedemptionShipped(ctx, e.member.ID, "RD2611-000001", "ขนมไก่", "TH123"))
	rows := e.rows(t)
	require.Len(t, rows, 4)
	for _, r := range rows {
		require.Equal(t, "U1", r.LineUserID)
	}
	require.Contains(t, rows[0].Payload, "1,200")
	require.Contains(t, rows[3].Payload, "TH123")

	// A member without LINE gets nothing (and no error).
	var tier domainloyalty.Tier
	require.NoError(t, e.db.First(&tier).Error)
	other := domainmember.Member{MemberCode: "M2", FirstName: "a", LastName: "b", Email: "o@x.com", TierID: tier.ID, Status: "active", Gender: "unspecified"}
	require.NoError(t, e.db.Create(&other).Error)
	require.NoError(t, e.notifier.PointsEarned(ctx, other.ID, 10, 10, "r2"))
	require.Len(t, e.rows(t), 4)
}

func TestMarketingNeedsConsent(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	err := e.notifier.Marketing(ctx, e.member.ID, "โปรใหม่", "/coupons", "nov")
	require.ErrorIs(t, err, ErrNoMarketingConsent, "no marketing document yet")

	past := time.Now().Add(-time.Minute)
	doc, err := e.consents.CreateDocument(ctx, domain.Actor{ERPUserID: 1}, usecaseconsent.DocumentInput{
		Type: domainconsent.TypeMarketingLine, Title: "รับข่าวสาร", Content: "x", IsActive: true, EffectiveAt: &past,
	})
	require.NoError(t, err)
	require.ErrorIs(t, e.notifier.Marketing(ctx, e.member.ID, "โปรใหม่", "/coupons", "nov"), ErrNoMarketingConsent)

	require.NoError(t, e.consents.Accept(ctx, usecaseconsent.AcceptInput{LineUserID: "U1", DocumentIDs: []uint{doc.ID}}))
	require.NoError(t, e.notifier.Marketing(ctx, e.member.ID, "โปรใหม่", "/coupons", "nov"))
	require.Len(t, e.rows(t), 1)
	require.Equal(t, domainlinemsg.TopicMarketing, e.rows(t)[0].Topic)
}

type fakeSender struct {
	pushes  []string
	linked  []string
	failFor map[string]error
}

func (f *fakeSender) Push(_ context.Context, to string, msgs json.RawMessage) error {
	if err := f.failFor[to]; err != nil {
		return err
	}
	f.pushes = append(f.pushes, to+":"+string(msgs))
	return nil
}
func (f *fakeSender) LinkMemberMenu(_ context.Context, id string) error {
	if err := f.failFor[id]; err != nil {
		return err
	}
	f.linked = append(f.linked, id)
	return nil
}
func (f *fakeSender) UnlinkMenu(context.Context, string) error { return nil }

func TestDispatchRetriesThenDies(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	require.NoError(t, e.notifier.Joined(ctx, "U1", e.member.ID, "นก", "M1", 0, false))
	require.NoError(t, e.notifier.enqueue(ctx, domainlinemsg.KindPush, "t", "Ublocked", nil, []map[string]string{{"type": "text", "text": "x"}}, "b"))
	require.NoError(t, e.notifier.enqueue(ctx, domainlinemsg.KindPush, "t", "Uflaky", nil, []map[string]string{{"type": "text", "text": "x"}}, "f"))
	sender := &fakeSender{failFor: map[string]error{
		"Ublocked": fmt.Errorf("%w: 400 not a friend", domainlinemsg.ErrPermanent),
		"Uflaky":   errors.New("503 from LINE"),
	}}
	now := time.Now()

	res, err := Dispatch(ctx, e.outbox, sender, now, 10)
	require.NoError(t, err)
	require.Equal(t, DispatchResult{Sent: 2, Failed: 1, Dead: 1}, res)
	require.Equal(t, []string{"U1"}, sender.linked)

	res, err = Dispatch(ctx, e.outbox, sender, now.Add(30*time.Second), 10)
	require.NoError(t, err)
	require.Zero(t, res.Failed, "backoff: not due yet")

	at := now
	for i := 0; i < domainlinemsg.MaxAttempts; i++ {
		at = at.Add(13 * time.Hour)
		_, err = Dispatch(ctx, e.outbox, sender, at, 10)
		require.NoError(t, err)
	}
	var flaky domainlinemsg.Outbox
	require.NoError(t, e.db.Where("line_user_id = ?", "Uflaky").First(&flaky).Error)
	require.Equal(t, domainlinemsg.StatusDead, flaky.Status)
	require.Equal(t, domainlinemsg.MaxAttempts, flaky.Attempts)
	require.Contains(t, flaky.LastError, "503")

	sender.failFor = nil
	res, err = Dispatch(ctx, e.outbox, sender, at.Add(time.Hour), 10)
	require.NoError(t, err)
	require.Zero(t, res.Sent, "dead messages are not sent again")
}

func TestWebhookFollowUnfollowAndRedelivery(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	w := NewWebhook(postgres.NewLineWebhookRepository(e.db), postgres.NewMemberIdentityRepository(e.db), e.notifier)
	friend := func() bool {
		var acc domainmember.MemberLineAccount
		require.NoError(t, e.db.Where("line_user_id = ?", "U1").First(&acc).Error)
		return acc.IsFriend
	}

	res, err := w.Handle(ctx, []Event{
		{WebhookEventID: "ev1", Type: "follow", UserID: "U1"},
		{WebhookEventID: "ev2", Type: "follow", UserID: "Ustranger"},
		{WebhookEventID: "ev3", Type: "message", UserID: "U1"},
	})
	require.NoError(t, err)
	require.Equal(t, 3, res.Recorded)
	require.True(t, friend())
	rows := e.rows(t)
	require.Len(t, rows, 1, "the member gets the member rich menu back")
	require.Equal(t, domainlinemsg.KindLinkMenu, rows[0].Kind)

	res, err = w.Handle(ctx, []Event{{WebhookEventID: "ev1", Type: "follow", UserID: "U1"}})
	require.NoError(t, err)
	require.Equal(t, 1, res.Duplicate, "LINE redelivery is ignored")

	_, err = w.Handle(ctx, []Event{{WebhookEventID: "ev4", Type: "unfollow", UserID: "U1"}})
	require.NoError(t, err)
	require.False(t, friend())

	var events []domainlinemsg.WebhookEvent
	require.NoError(t, e.db.Find(&events).Error)
	require.Len(t, events, 4)
	for _, ev := range events {
		require.Equal(t, domainlinemsg.EventDone, ev.Status)
	}
}

func TestRedemptionDeliveries(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	require.NoError(t, e.notifier.RedemptionCreated(ctx, e.member.ID, "RD-1", "ขนมไก่", 40))
	require.NoError(t, e.notifier.RedemptionCreated(ctx, e.member.ID, "RD-2", "ขนมไก่", 40))
	require.NoError(t, e.notifier.RedemptionShipped(ctx, e.member.ID, "RD-1", "ขนมไก่", "TH1"))
	rows := e.rows(t)
	require.NoError(t, e.outbox.MarkSent(ctx, rows[0].ID, time.Now()))

	got, err := RedemptionDeliveries(ctx, e.outbox, []string{"RD-1", "RD-2", "RD-3"})
	require.NoError(t, err)
	require.Equal(t, domainlinemsg.StatusSent, got["RD-1"].Redeemed.Status)
	require.NotNil(t, got["RD-1"].Redeemed.SentAt)
	require.Equal(t, domainlinemsg.StatusPending, got["RD-1"].Shipped.Status)
	require.NotNil(t, got["RD-2"].Redeemed)
	require.Nil(t, got["RD-2"].Shipped, "not shipped yet")
	require.Nil(t, got["RD-3"].Redeemed, "never queued")

	empty, err := RedemptionDeliveries(ctx, nil, []string{"RD-1"})
	require.NoError(t, err)
	require.Empty(t, empty)
}
