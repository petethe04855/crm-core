package linemsg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	domainlinemsg "crm-core/domain/linemsg"
)

// Sender talks to the LINE Messaging API (backoffice-api/pkg/line).
// Errors wrapping domainlinemsg.ErrPermanent are not retried.
type Sender interface {
	Push(ctx context.Context, to string, messages json.RawMessage) error
	LinkMemberMenu(ctx context.Context, userID string) error
	UnlinkMenu(ctx context.Context, userID string) error
}

// DispatchResult counts one dispatch round.
type DispatchResult struct {
	Sent   int
	Failed int
	Dead   int
}

// Dispatch sends up to limit due messages. Each failure backs off
// (1m, 5m, 30m, 2h, 12h); a permanent error or the sixth failure is dead.
func Dispatch(ctx context.Context, outbox domainlinemsg.OutboxRepository, sender Sender, now time.Time, limit int) (DispatchResult, error) {
	var res DispatchResult
	rows, err := outbox.Due(ctx, now, limit)
	if err != nil {
		return res, err
	}
	for _, m := range rows {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		sendErr := send(ctx, sender, m)
		if sendErr == nil {
			if err := outbox.MarkSent(ctx, m.ID, now); err != nil {
				return res, err
			}
			res.Sent++
			continue
		}
		attempts := m.Attempts + 1
		dead := errors.Is(sendErr, domainlinemsg.ErrPermanent) || attempts >= domainlinemsg.MaxAttempts
		next := now
		if !dead {
			next = now.Add(domainlinemsg.Backoff[min(attempts-1, len(domainlinemsg.Backoff)-1)])
		}
		if err := outbox.MarkFailed(ctx, m.ID, attempts, next, sendErr.Error(), dead); err != nil {
			return res, err
		}
		if dead {
			res.Dead++
		} else {
			res.Failed++
		}
	}
	return res, nil
}

func send(ctx context.Context, sender Sender, m domainlinemsg.Outbox) error {
	switch m.Kind {
	case domainlinemsg.KindPush:
		if !json.Valid([]byte(m.Payload)) {
			return fmt.Errorf("%w: payload is not JSON", domainlinemsg.ErrPermanent)
		}
		return sender.Push(ctx, m.LineUserID, json.RawMessage(m.Payload))
	case domainlinemsg.KindLinkMenu:
		return sender.LinkMemberMenu(ctx, m.LineUserID)
	case domainlinemsg.KindUnlinkMenu:
		return sender.UnlinkMenu(ctx, m.LineUserID)
	default:
		return fmt.Errorf("%w: unknown outbox kind %q", domainlinemsg.ErrPermanent, m.Kind)
	}
}
