package migrations

import domainlinemsg "crm-core/domain/linemsg"

// lineMessagingModels lists the LINE Messaging tables (L6): the outbox the
// backoffice worker sends and the webhook events line-oa-api receives.
func lineMessagingModels() []any {
	return []any{
		&domainlinemsg.Outbox{},
		&domainlinemsg.WebhookEvent{},
	}
}
