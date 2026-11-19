package migrations

import (
	domainloyalty "crm-core/domain/loyalty"
	domainmember "crm-core/domain/member"
)

// memberModels lists the member tables. Order matters: tier → member → the
// tables that point at a member.
func memberModels() []any {
	return []any{
		&domainloyalty.Tier{},
		&domainmember.Member{},
		&domainmember.MemberLineAccount{},
		&domainmember.MemberTag{},
		&domainmember.MemberNote{},
	}
}
