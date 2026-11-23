package migrations

import (
	domainconsent "crm-core/domain/consent"
	domainmember "crm-core/domain/member"
	domainotp "crm-core/domain/otp"
	domainregistration "crm-core/domain/registration"
)

// consentModels lists the LINE onboarding tables: consent documents, the
// append-only consent records and email OTP requests (L1), and the member
// tier history (L2) and shipping addresses (L3).
func consentModels() []any {
	return []any{
		&domainconsent.Document{},
		&domainconsent.Record{},
		&domainotp.Request{},
		// L2: tier history written on signup (and later upgrades).
		&domainregistration.TierHistory{},
		// L3: shipping addresses.
		&domainmember.MemberAddress{},
	}
}
