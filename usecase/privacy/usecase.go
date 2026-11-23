// Package privacy is the member's PDPA self-service (plan L7): notification
// settings (including withdrawing marketing consent), a copy of their data,
// and account deletion requests that staff complete in the backoffice.
package privacy

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	"crm-core/domain"
	domainconsent "crm-core/domain/consent"
	domaincoupon "crm-core/domain/coupon"
	domainmember "crm-core/domain/member"
	domainmembercard "crm-core/domain/membercard"
	domainpoints "crm-core/domain/points"
	domainprivacy "crm-core/domain/privacy"
	domainproductcode "crm-core/domain/productcode"
	domainreward "crm-core/domain/reward"
	usecaseconsent "crm-core/usecase/consent"
)

// Settings is GET /me/notifications.
type Settings struct {
	Prefs domainprivacy.NotificationPrefs
	// Marketing consents; *Available is false when no such document is
	// published (the switch is hidden).
	MarketingLine           bool
	MarketingLineAvailable  bool
	MarketingEmail          bool
	MarketingEmailAvailable bool
}

// SettingsInput is PATCH /me/notifications; nil marketing fields are unchanged.
type SettingsInput struct {
	PointsUpdates  bool
	ExpiringPoints bool
	Birthday       bool
	MarketingLine  *bool
	MarketingEmail *bool
	IP             string
	UserAgent      string
}

// Export is the member's copy of their data (PDPA right of access).
type Export struct {
	GeneratedAt  time.Time                       `json:"generatedAt"`
	Member       ExportMember                    `json:"member"`
	LineAccounts []ExportLine                    `json:"lineAccounts"`
	Addresses    []domainmember.MemberAddress    `json:"addresses"`
	Consents     []domainconsent.Record          `json:"consents"`
	Points       []domainpoints.PointLedger      `json:"points"`
	Coupons      []ExportCoupon                  `json:"coupons"`
	Redemptions  []domainreward.RedemptionRow    `json:"redemptions"`
	ProductCodes []domainproductcode.HistoryItem `json:"productCodes"`
	TierHistory  []domainmembercard.TierChange   `json:"tierHistory"`
	Notification domainprivacy.NotificationPrefs `json:"notificationPreferences"`
}

// ExportMember is the member row without internal fields.
type ExportMember struct {
	MemberCode    string     `json:"memberCode"`
	FirstName     string     `json:"firstName"`
	LastName      string     `json:"lastName"`
	Email         string     `json:"email"`
	Phone         *string    `json:"phone"`
	BirthDate     *time.Time `json:"birthDate"`
	Gender        string     `json:"gender"`
	PointBalance  int64      `json:"pointBalance"`
	LifetimeSpend float64    `json:"lifetimeSpend"`
	Status        string     `json:"status"`
	RegisteredAt  time.Time  `json:"registeredAt"`
}

// ExportLine is a LINE link.
type ExportLine struct {
	LineUserID  string     `json:"lineUserId"`
	DisplayName string     `json:"displayName"`
	LinkedAt    time.Time  `json:"linkedAt"`
	UnlinkedAt  *time.Time `json:"unlinkedAt,omitempty"`
}

// ExportCoupon is a held coupon.
type ExportCoupon struct {
	Name      string     `json:"name"`
	Code      string     `json:"code"`
	Status    string     `json:"status"`
	ClaimedAt time.Time  `json:"claimedAt"`
	ExpiresAt time.Time  `json:"expiresAt"`
	UsedAt    *time.Time `json:"usedAt,omitempty"`
}

// Sources are the read models the export pulls from.
type Sources struct {
	Members     domainmember.Repository
	Profiles    domainmember.ProfileRepository
	Consents    usecaseconsent.Usecase
	Points      domainpoints.Repository
	Coupons     domaincoupon.Repository
	Rewards     domainreward.Repository
	Codes       domainproductcode.AttemptRepository
	MemberCards domainmembercard.Repository
}

// MenuReset resets the LINE rich menu after a deletion (linemsg.Notifier).
type MenuReset interface {
	Unlinked(ctx context.Context, lineUserID string, memberID uint) error
}

// Usecase is the PDPA self-service and the backoffice queue.
type Usecase interface {
	Settings(ctx context.Context, memberID uint, lineUserID string) (*Settings, error)
	UpdateSettings(ctx context.Context, memberID uint, lineUserID string, in SettingsInput) (*Settings, error)
	Export(ctx context.Context, memberID uint) (*Export, error)
	RequestDeletion(ctx context.Context, memberID uint, reason string) (*domainprivacy.DataRequest, error)
	PendingDeletion(ctx context.Context, memberID uint) (*domainprivacy.DataRequest, error)

	Requests(ctx context.Context, q domainprivacy.RequestQuery) ([]domainprivacy.RequestRow, int64, error)
	CompleteDeletion(ctx context.Context, actor domain.Actor, requestID uint, note string) error
	RejectDeletion(ctx context.Context, actor domain.Actor, requestID uint, note string) error
}

type usecase struct {
	repo  domainprivacy.Repository
	uow   domainprivacy.UnitOfWork
	src   Sources
	reset MenuReset
	now   func() time.Time
}

// Option customizes the usecase.
type Option func(*usecase)

// WithMenuReset resets the LINE rich menu after a deletion.
func WithMenuReset(r MenuReset) Option { return func(u *usecase) { u.reset = r } }

// WithClock replaces time.Now (tests).
func WithClock(now func() time.Time) Option { return func(u *usecase) { u.now = now } }

// New builds the privacy usecase.
func New(repo domainprivacy.Repository, uow domainprivacy.UnitOfWork, src Sources, opts ...Option) Usecase {
	u := &usecase{repo: repo, uow: uow, src: src, now: time.Now}
	for _, opt := range opts {
		opt(u)
	}
	return u
}

func (u *usecase) member(ctx context.Context, id uint) (*domainmember.Member, error) {
	m, err := u.src.Members.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if m.Status != domainmember.StatusActive || m.DeletedAt != nil {
		return nil, domain.ErrMemberNotAllowed
	}
	return m, nil
}

func (u *usecase) Settings(ctx context.Context, memberID uint, lineUserID string) (*Settings, error) {
	if _, err := u.member(ctx, memberID); err != nil {
		return nil, err
	}
	prefs, err := u.repo.Prefs(ctx, memberID)
	if err != nil {
		return nil, err
	}
	out := &Settings{Prefs: prefs}
	docs, err := u.src.Consents.Current(ctx, lineUserID)
	if err != nil {
		return nil, err
	}
	for _, d := range docs {
		switch d.Type {
		case domainconsent.TypeMarketingLine:
			out.MarketingLineAvailable, out.MarketingLine = true, d.Accepted
		case domainconsent.TypeMarketingEmail:
			out.MarketingEmailAvailable, out.MarketingEmail = true, d.Accepted
		}
	}
	return out, nil
}

func (u *usecase) UpdateSettings(ctx context.Context, memberID uint, lineUserID string, in SettingsInput) (*Settings, error) {
	if _, err := u.member(ctx, memberID); err != nil {
		return nil, err
	}
	if err := u.repo.SavePrefs(ctx, domainprivacy.NotificationPrefs{
		MemberID: memberID, PointsUpdates: in.PointsUpdates, ExpiringPoints: in.ExpiringPoints, Birthday: in.Birthday,
	}); err != nil {
		return nil, err
	}
	mid := memberID
	for typ, want := range map[string]*bool{
		domainconsent.TypeMarketingLine:  in.MarketingLine,
		domainconsent.TypeMarketingEmail: in.MarketingEmail,
	} {
		if want == nil {
			continue
		}
		err := u.src.Consents.SetOptional(ctx, usecaseconsent.OptionalInput{
			LineUserID: lineUserID, MemberID: &mid, Type: typ, Accept: *want, IP: in.IP, UserAgent: in.UserAgent,
		})
		if err != nil && !errors.Is(err, usecaseconsent.ErrNoDocument) {
			return nil, err
		}
	}
	return u.Settings(ctx, memberID, lineUserID)
}

func (u *usecase) Export(ctx context.Context, memberID uint) (*Export, error) {
	m, err := u.member(ctx, memberID)
	if err != nil {
		return nil, err
	}
	out := &Export{GeneratedAt: u.now(), Member: ExportMember{
		MemberCode: m.MemberCode, FirstName: m.FirstName, LastName: m.LastName, Email: m.Email, Phone: m.PhoneE164,
		BirthDate: m.BirthDate, Gender: m.Gender, PointBalance: m.PointBalance, LifetimeSpend: m.LifetimeSpend,
		Status: m.Status, RegisteredAt: m.CreatedAt,
	}}
	if acc, err := u.src.Members.FindActiveLineAccount(ctx, memberID); err == nil {
		out.LineAccounts = append(out.LineAccounts, ExportLine{LineUserID: acc.LineUserID, DisplayName: acc.DisplayName, LinkedAt: acc.LinkedAt})
		if out.Consents, err = u.src.Consents.Records(ctx, acc.LineUserID); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, domain.ErrNotFound) {
		return nil, err
	}
	if out.Addresses, err = u.src.Profiles.ListAddresses(ctx, memberID); err != nil {
		return nil, err
	}
	for page := 1; ; page++ {
		rows, total, err := u.src.Points.List(ctx, memberID, domainpoints.Query{Page: page, Limit: 500})
		if err != nil {
			return nil, err
		}
		out.Points = append(out.Points, rows...)
		if int64(len(out.Points)) >= total || len(rows) == 0 {
			break
		}
	}
	holders, _, err := u.src.Coupons.FindHolders(ctx, domaincoupon.HolderQuery{MemberID: memberID, Page: 1, Limit: 1000})
	if err != nil {
		return nil, err
	}
	for _, h := range holders {
		out.Coupons = append(out.Coupons, ExportCoupon{Name: h.CouponName, Code: h.Code, Status: h.Status, ClaimedAt: h.ClaimedAt, ExpiresAt: h.ExpiresAt, UsedAt: h.UsedAt})
	}
	if out.Redemptions, _, err = u.src.Rewards.FindRedemptions(ctx, domainreward.RedemptionQuery{MemberID: memberID, Page: 1, Limit: 1000}); err != nil {
		return nil, err
	}
	if out.ProductCodes, _, err = u.src.Codes.History(ctx, memberID, 1, 100); err != nil {
		return nil, err
	}
	if out.TierHistory, err = u.src.MemberCards.TierHistory(ctx, memberID); err != nil {
		return nil, err
	}
	if out.Notification, err = u.repo.Prefs(ctx, memberID); err != nil {
		return nil, err
	}
	return out, nil
}

func (u *usecase) RequestDeletion(ctx context.Context, memberID uint, reason string) (*domainprivacy.DataRequest, error) {
	m, err := u.member(ctx, memberID)
	if err != nil {
		return nil, err
	}
	reason = strings.TrimSpace(reason)
	if utf8.RuneCountInString(reason) > 500 {
		return nil, domain.NewValidationError("reason", "must be at most 500 characters")
	}
	if _, err := u.repo.PendingRequest(ctx, memberID, domainprivacy.RequestDelete); err == nil {
		return nil, domainprivacy.ErrRequestPending
	} else if !errors.Is(err, domain.ErrNotFound) {
		return nil, err
	}
	req := &domainprivacy.DataRequest{
		MemberID: memberID, Type: domainprivacy.RequestDelete, Status: domainprivacy.RequestPending,
		Reason: reason, MemberCode: m.MemberCode, CreatedAt: u.now(),
	}
	if err := u.repo.CreateRequest(ctx, req); err != nil {
		return nil, err
	}
	return req, nil
}

func (u *usecase) PendingDeletion(ctx context.Context, memberID uint) (*domainprivacy.DataRequest, error) {
	req, err := u.repo.PendingRequest(ctx, memberID, domainprivacy.RequestDelete)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, nil
	}
	return req, err
}

func (u *usecase) Requests(ctx context.Context, q domainprivacy.RequestQuery) ([]domainprivacy.RequestRow, int64, error) {
	switch q.Status {
	case "", domainprivacy.RequestPending, domainprivacy.RequestCompleted, domainprivacy.RequestRejected:
	default:
		return nil, 0, domain.NewValidationError("status", "unknown request status")
	}
	return u.repo.FindRequests(ctx, q)
}

func cleanNote(note string) (string, error) {
	note = strings.TrimSpace(note)
	if utf8.RuneCountInString(note) > 500 {
		return "", domain.NewValidationError("note", "must be at most 500 characters")
	}
	return note, nil
}

// CompleteDeletion anonymizes the member in one transaction. Redemptions not
// yet shipped or cancelled block it (staff finish or cancel them first, which
// refunds or delivers properly).
func (u *usecase) CompleteDeletion(ctx context.Context, actor domain.Actor, requestID uint, note string) error {
	note, err := cleanNote(note)
	if err != nil {
		return err
	}
	var lineUserID string
	var memberID uint
	err = u.uow.Do(ctx, func(ctx context.Context, tx domainprivacy.AnonymizeTx) error {
		req, err := tx.LockRequest(ctx, requestID)
		if err != nil {
			return err
		}
		if req.Status != domainprivacy.RequestPending {
			return domainprivacy.ErrRequestClosed
		}
		if _, err := tx.LockMember(ctx, req.MemberID); err != nil {
			return err
		}
		open, err := tx.CountOpenRedemptions(ctx, req.MemberID)
		if err != nil {
			return err
		}
		if open > 0 {
			return domainprivacy.ErrOpenRedemptions
		}
		if lineUserID, err = tx.ActiveLineUserID(ctx, req.MemberID); err != nil {
			return err
		}
		now := u.now()
		if err := tx.Anonymize(ctx, req.MemberID, now); err != nil {
			return err
		}
		memberID = req.MemberID
		return tx.CloseRequest(ctx, req.ID, domainprivacy.RequestCompleted, actor.ERPUserID, note, now)
	})
	if err != nil {
		return err
	}
	if lineUserID != "" && u.reset != nil {
		if err := u.reset.Unlinked(ctx, lineUserID, memberID); err != nil {
			log.Printf("[WARN] LINE rich menu reset after deletion of member %d: %v", memberID, err)
		}
	}
	return nil
}

func (u *usecase) RejectDeletion(ctx context.Context, actor domain.Actor, requestID uint, note string) error {
	note, err := cleanNote(note)
	if err != nil {
		return err
	}
	if note == "" {
		return domain.NewValidationError("note", "a reason for the member is required")
	}
	return u.uow.Do(ctx, func(ctx context.Context, tx domainprivacy.AnonymizeTx) error {
		req, err := tx.LockRequest(ctx, requestID)
		if err != nil {
			return err
		}
		if req.Status != domainprivacy.RequestPending {
			return domainprivacy.ErrRequestClosed
		}
		return tx.CloseRequest(ctx, req.ID, domainprivacy.RequestRejected, actor.ERPUserID, note, u.now())
	})
}
