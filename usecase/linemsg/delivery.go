package linemsg

import (
	"context"
	"time"

	domainlinemsg "crm-core/domain/linemsg"
)

// Delivery is the state of one queued LINE message.
type Delivery struct {
	Status   string // pending | sent | dead
	QueuedAt time.Time
	SentAt   *time.Time
}

// RedemptionMessages are the LINE messages of one redemption (nil = never
// queued, e.g. the member has no LINE linked or LINE was not configured).
type RedemptionMessages struct {
	Redeemed *Delivery // "แลกสำเร็จ" on redeem
	Shipped  *Delivery // "จัดส่งแล้ว" on ship
}

// RedemptionDeliveries looks up the messages queued by RedemptionCreated and
// RedemptionShipped (dedupe keys "redeemed:<no>" and "shipped:<no>").
func RedemptionDeliveries(ctx context.Context, outbox domainlinemsg.OutboxRepository, redemptionNos []string) (map[string]RedemptionMessages, error) {
	out := make(map[string]RedemptionMessages, len(redemptionNos))
	if outbox == nil || len(redemptionNos) == 0 {
		return out, nil
	}
	keys := make([]string, 0, 2*len(redemptionNos))
	for _, no := range redemptionNos {
		keys = append(keys, redeemedKey(no), shippedKey(no))
	}
	rows, err := outbox.FindByDedupeKeys(ctx, keys)
	if err != nil {
		return nil, err
	}
	byKey := make(map[string]*Delivery, len(rows))
	for _, m := range rows {
		if m.DedupeKey == nil {
			continue
		}
		byKey[*m.DedupeKey] = &Delivery{Status: m.Status, QueuedAt: m.CreatedAt, SentAt: m.SentAt}
	}
	for _, no := range redemptionNos {
		out[no] = RedemptionMessages{Redeemed: byKey[redeemedKey(no)], Shipped: byKey[shippedKey(no)]}
	}
	return out, nil
}

func redeemedKey(no string) string { return "redeemed:" + no }
func shippedKey(no string) string  { return "shipped:" + no }
