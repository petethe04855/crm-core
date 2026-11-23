package linemsg

import (
	"context"
	"errors"
	"time"

	"crm-core/domain"
	domainlinemsg "crm-core/domain/linemsg"
	domainmember "crm-core/domain/member"
)

// Event is the part of a LINE webhook event the CRM uses. Raw is the event's
// JSON as received, kept for audit.
type Event struct {
	WebhookEventID string
	Type           string // follow, unfollow, message, ...
	UserID         string // source.userId ("" for group/room sources)
	Timestamp      time.Time
	Raw            string
}

// Members finds whether a LINE user is a member.
type Members interface {
	FindByLineUserID(ctx context.Context, lineUserID string) (*domainmember.Member, *domainmember.MemberLineAccount, error)
}

// Webhook applies LINE webhook events (plan L6): records each one once,
// keeps is_friend in sync and re-links the member rich menu on follow.
type Webhook struct {
	events   domainlinemsg.WebhookRepository
	members  Members
	notifier *Notifier
	now      func() time.Time
}

// NewWebhook builds the webhook processor.
func NewWebhook(events domainlinemsg.WebhookRepository, members Members, notifier *Notifier) *Webhook {
	return &Webhook{events: events, members: members, notifier: notifier, now: time.Now}
}

// HandleResult counts what one webhook call did.
type HandleResult struct {
	Recorded  int
	Duplicate int
	Failed    int
}

// Handle records and applies events. LINE redelivers on non-2xx, so one bad
// event is recorded as failed instead of failing the whole call.
func (w *Webhook) Handle(ctx context.Context, events []Event) (HandleResult, error) {
	var res HandleResult
	for _, e := range events {
		if e.WebhookEventID == "" {
			continue
		}
		rec := &domainlinemsg.WebhookEvent{
			WebhookEventID: e.WebhookEventID, Type: e.Type, LineUserID: e.UserID, Payload: e.Raw,
			Status: domainlinemsg.EventReceived, ReceivedAt: w.now(),
		}
		fresh, err := w.events.Record(ctx, rec)
		if err != nil {
			return res, err
		}
		if !fresh {
			res.Duplicate++
			continue
		}
		res.Recorded++
		status, text := domainlinemsg.EventDone, ""
		if err := w.apply(ctx, e); err != nil {
			status, text = domainlinemsg.EventFailed, err.Error()
			res.Failed++
		}
		if err := w.events.Finish(ctx, rec.ID, status, text, w.now()); err != nil {
			return res, err
		}
	}
	return res, nil
}

func (w *Webhook) apply(ctx context.Context, e Event) error {
	if e.UserID == "" {
		return nil
	}
	switch e.Type {
	case "follow":
		if err := w.events.SetFriend(ctx, e.UserID, true); err != nil {
			return err
		}
		// A member who unblocked or re-added the OA gets the member menu back.
		m, _, err := w.members.FindByLineUserID(ctx, e.UserID)
		switch {
		case errors.Is(err, domain.ErrNotFound):
			return nil
		case err != nil:
			return err
		}
		if w.notifier == nil {
			return nil
		}
		mid := m.ID
		return w.notifier.enqueue(ctx, domainlinemsg.KindLinkMenu, domainlinemsg.TopicRichMenu, e.UserID, &mid, nil,
			"menu-follow:"+e.WebhookEventID)
	case "unfollow":
		return w.events.SetFriend(ctx, e.UserID, false)
	}
	return nil
}
