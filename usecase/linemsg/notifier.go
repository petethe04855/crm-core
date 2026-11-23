// Package linemsg queues LINE messages (Notifier), sends them (Dispatch, run
// by backoffice-api's worker) and applies webhook events (Webhook).
//
// Notifications are best effort: they are queued after the business
// transaction committed, and a failure to queue never undoes the business
// change. Callers log the returned error.
package linemsg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"crm-core/domain"
	domainconsent "crm-core/domain/consent"
	domainlinemsg "crm-core/domain/linemsg"
	domainmember "crm-core/domain/member"
	domainprivacy "crm-core/domain/privacy"
	usecaseconsent "crm-core/usecase/consent"
)

// LineAccounts finds a member's active LINE link (member.Repository has it).
type LineAccounts interface {
	FindActiveLineAccount(ctx context.Context, memberID uint) (*domainmember.MemberLineAccount, error)
}

// Consents reads the LINE user's consent answers.
type Consents interface {
	Current(ctx context.Context, lineUserID string) ([]usecaseconsent.CurrentDocument, error)
}

// ErrNoMarketingConsent: the member did not accept LINE marketing messages.
var ErrNoMarketingConsent = errors.New("member has not accepted LINE marketing messages")

// Notifier queues messages for members.
type Notifier struct {
	outbox   domainlinemsg.OutboxRepository
	accounts LineAccounts
	consents Consents
	// liffURL is https://liff.line.me/<LIFF id>; links in messages open pages
	// of the member app under it.
	liffURL string
	prefs   PrefsReader
	now     func() time.Time
}

// PrefsReader reads a member's notification choices (privacy repository).
type PrefsReader interface {
	Prefs(ctx context.Context, memberID uint) (domainprivacy.NotificationPrefs, error)
}

// WithPrefs makes optional messages (points, tier, expiring points, birthday)
// respect the member's notification settings. Service messages (welcome,
// redemption status) are always sent.
func (n *Notifier) WithPrefs(p PrefsReader) *Notifier {
	n.prefs = p
	return n
}

// wants reports whether the member wants a message of this kind.
func (n *Notifier) wants(ctx context.Context, memberID uint, pick func(domainprivacy.NotificationPrefs) bool) (bool, error) {
	if n.prefs == nil {
		return true, nil
	}
	p, err := n.prefs.Prefs(ctx, memberID)
	if err != nil {
		return false, err
	}
	return pick(p), nil
}

// NewNotifier builds the notifier. consents may be nil when no marketing
// messages are sent.
func NewNotifier(outbox domainlinemsg.OutboxRepository, accounts LineAccounts, consents Consents, liffURL string) *Notifier {
	return &Notifier{outbox: outbox, accounts: accounts, consents: consents, liffURL: strings.TrimRight(liffURL, "/"), now: time.Now}
}

func (n *Notifier) link(path string) string {
	if n.liffURL == "" {
		return ""
	}
	return n.liffURL + path
}

// textMessage builds one LINE text message, with the link on its own line.
func (n *Notifier) textMessage(text, path string) []map[string]any {
	if link := n.link(path); link != "" {
		text += "\n\n" + link
	}
	return []map[string]any{{"type": "text", "text": text}}
}

func (n *Notifier) enqueue(ctx context.Context, kind, topic, lineUserID string, memberID *uint, payload any, dedupe string) error {
	if lineUserID == "" {
		return nil // not linked to LINE: nothing to send
	}
	raw := []byte("{}")
	if payload != nil {
		var err error
		if raw, err = json.Marshal(payload); err != nil {
			return err
		}
	}
	now := n.now()
	m := &domainlinemsg.Outbox{
		Kind: kind, Topic: topic, LineUserID: lineUserID, MemberID: memberID, Payload: string(raw),
		Status: domainlinemsg.StatusPending, NextAttemptAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if dedupe != "" {
		m.DedupeKey = &dedupe
	}
	return n.outbox.Enqueue(ctx, m)
}

// lineOf returns the member's LINE user id ("" when not linked).
func (n *Notifier) lineOf(ctx context.Context, memberID uint) (string, error) {
	acc, err := n.accounts.FindActiveLineAccount(ctx, memberID)
	if errors.Is(err, domain.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return acc.LineUserID, nil
}

func id(v uint) string { return strconv.FormatUint(uint64(v), 10) }

var num = func(v int64) string {
	s := strconv.FormatInt(v, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// Joined runs after register / link: switch to the member rich menu and say
// welcome (with the signup bonus, if any).
func (n *Notifier) Joined(ctx context.Context, lineUserID string, memberID uint, firstName, memberCode string, welcomePoints int64, linkedExisting bool) error {
	mid := memberID
	if err := n.enqueue(ctx, domainlinemsg.KindLinkMenu, domainlinemsg.TopicRichMenu, lineUserID, &mid, nil,
		"menu:"+id(memberID)+":"+lineUserID); err != nil {
		return err
	}
	text := fmt.Sprintf("ยินดีต้อนรับคุณ%s สู่สมาชิก 🎉\nรหัสสมาชิกของคุณคือ %s", firstName, memberCode)
	if linkedExisting {
		text = fmt.Sprintf("ผูกบัญชี LINE กับสมาชิก %s เรียบร้อยแล้ว\nแต้มและสิทธิ์เดิมของคุณยังอยู่ครบ", memberCode)
	} else if welcomePoints > 0 {
		text += fmt.Sprintf("\nรับแต้มต้อนรับ %s แต้ม", num(welcomePoints))
	}
	return n.enqueue(ctx, domainlinemsg.KindPush, domainlinemsg.TopicWelcome, lineUserID, &mid,
		n.textMessage(text, "/home"), "welcome:"+id(memberID)+":"+lineUserID)
}

// Unlinked runs when staff unlink a member's LINE account: back to the guest menu.
func (n *Notifier) Unlinked(ctx context.Context, lineUserID string, memberID uint) error {
	mid := memberID
	return n.enqueue(ctx, domainlinemsg.KindUnlinkMenu, domainlinemsg.TopicRichMenu, lineUserID, &mid, nil,
		"unmenu:"+id(memberID)+":"+lineUserID+":"+strconv.FormatInt(n.now().Unix(), 10))
}

// PointsEarned tells the member about points from product codes. ref makes
// it unique per redeem request.
func (n *Notifier) PointsEarned(ctx context.Context, memberID uint, points, balance int64, ref string) error {
	if points <= 0 {
		return nil
	}
	if ok, err := n.wants(ctx, memberID, func(p domainprivacy.NotificationPrefs) bool { return p.PointsUpdates }); err != nil || !ok {
		return err
	}
	line, err := n.lineOf(ctx, memberID)
	if err != nil {
		return err
	}
	mid := memberID
	text := fmt.Sprintf("คุณได้รับ %s แต้ม จากการกรอกรหัสสินค้า\nแต้มคงเหลือ %s แต้ม", num(points), num(balance))
	return n.enqueue(ctx, domainlinemsg.KindPush, domainlinemsg.TopicPointsEarned, line, &mid,
		n.textMessage(text, "/points"), "points:"+ref)
}

// TierUpgraded congratulates the member on a new tier.
func (n *Notifier) TierUpgraded(ctx context.Context, memberID, tierID uint, tierName string) error {
	if ok, err := n.wants(ctx, memberID, func(p domainprivacy.NotificationPrefs) bool { return p.PointsUpdates }); err != nil || !ok {
		return err
	}
	line, err := n.lineOf(ctx, memberID)
	if err != nil {
		return err
	}
	mid := memberID
	text := fmt.Sprintf("ยินดีด้วย! คุณเลื่อนเป็นสมาชิกระดับ %s แล้ว", tierName)
	return n.enqueue(ctx, domainlinemsg.KindPush, domainlinemsg.TopicTierUpgrade, line, &mid,
		n.textMessage(text, "/home"), "tier:"+id(memberID)+":"+id(tierID))
}

// RedemptionCreated confirms a reward redemption.
func (n *Notifier) RedemptionCreated(ctx context.Context, memberID uint, redemptionNo, rewardName string, points int64) error {
	line, err := n.lineOf(ctx, memberID)
	if err != nil {
		return err
	}
	mid := memberID
	text := fmt.Sprintf("แลก \"%s\" สำเร็จ (ใช้ %s แต้ม)\nเลขที่ %s — เราจะแจ้งอีกครั้งเมื่อจัดส่ง", rewardName, num(points), redemptionNo)
	return n.enqueue(ctx, domainlinemsg.KindPush, domainlinemsg.TopicRedemptionMade, line, &mid,
		n.textMessage(text, "/redemptions"), redeemedKey(redemptionNo))
}

// RedemptionShipped tells the member the reward is on its way.
func (n *Notifier) RedemptionShipped(ctx context.Context, memberID uint, redemptionNo, rewardName, trackingNo string) error {
	line, err := n.lineOf(ctx, memberID)
	if err != nil {
		return err
	}
	mid := memberID
	text := fmt.Sprintf("จัดส่ง \"%s\" แล้ว (เลขที่ %s)", rewardName, redemptionNo)
	if trackingNo != "" {
		text += "\nเลขพัสดุ " + trackingNo
	}
	return n.enqueue(ctx, domainlinemsg.KindPush, domainlinemsg.TopicRedemptionShip, line, &mid,
		n.textMessage(text, "/redemptions"), shippedKey(redemptionNo))
}

// Marketing queues a marketing message, only for members who accepted the
// current marketing_line consent. key dedupes one campaign per member.
func (n *Notifier) Marketing(ctx context.Context, memberID uint, text, path, key string) error {
	line, err := n.lineOf(ctx, memberID)
	if err != nil {
		return err
	}
	if line == "" {
		return nil
	}
	ok, err := n.MarketingAllowed(ctx, line)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNoMarketingConsent
	}
	mid := memberID
	return n.enqueue(ctx, domainlinemsg.KindPush, domainlinemsg.TopicMarketing, line, &mid,
		n.textMessage(text, path), "mkt:"+key+":"+id(memberID))
}

// MarketingAllowed reports whether the LINE user accepted the current
// marketing_line document.
func (n *Notifier) MarketingAllowed(ctx context.Context, lineUserID string) (bool, error) {
	if n.consents == nil {
		return false, nil
	}
	docs, err := n.consents.Current(ctx, lineUserID)
	if err != nil {
		return false, err
	}
	for _, d := range docs {
		if d.Type == domainconsent.TypeMarketingLine {
			return d.Accepted, nil
		}
	}
	return false, nil // no marketing document published: no consent
}

// ExpiringSoon reminds the member that points expire soon. One reminder per
// member per earliest expiry date.
func (n *Notifier) ExpiringSoon(ctx context.Context, memberID uint, points int64, earliest time.Time) error {
	if points <= 0 {
		return nil
	}
	if ok, err := n.wants(ctx, memberID, func(p domainprivacy.NotificationPrefs) bool { return p.ExpiringPoints }); err != nil || !ok {
		return err
	}
	line, err := n.lineOf(ctx, memberID)
	if err != nil {
		return err
	}
	mid := memberID
	day := earliest.In(bangkok).Format("2/1/2006")
	text := fmt.Sprintf("แต้มของคุณ %s แต้ม จะหมดอายุวันที่ %s\nใช้แลกของรางวัลหรือคูปองก่อนหมดอายุนะ", num(points), day)
	return n.enqueue(ctx, domainlinemsg.KindPush, domainlinemsg.TopicExpiring, line, &mid,
		n.textMessage(text, "/rewards"), "expiring:"+id(memberID)+":"+earliest.In(bangkok).Format("20060102"))
}

// Birthday greets the member (and announces the bonus, if any). One per year.
func (n *Notifier) Birthday(ctx context.Context, memberID uint, firstName string, bonus int64, year int) error {
	if ok, err := n.wants(ctx, memberID, func(p domainprivacy.NotificationPrefs) bool { return p.Birthday }); err != nil || !ok {
		return err
	}
	line, err := n.lineOf(ctx, memberID)
	if err != nil {
		return err
	}
	mid := memberID
	text := fmt.Sprintf("สุขสันต์วันเกิดคุณ%s 🎂", firstName)
	if bonus > 0 {
		text += fmt.Sprintf("\nรับของขวัญวันเกิด %s แต้ม", num(bonus))
	}
	return n.enqueue(ctx, domainlinemsg.KindPush, domainlinemsg.TopicBirthday, line, &mid,
		n.textMessage(text, "/home"), "birthday:"+id(memberID)+":"+strconv.Itoa(year))
}

var bangkok = time.FixedZone("Asia/Bangkok", 7*60*60)
