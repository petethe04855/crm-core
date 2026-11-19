package migrations

import (
	domainearnrule "crm-core/domain/earnrule"
)

// earnRuleModels lists the point earning rules (plan §4.1).
func earnRuleModels() []any {
	return []any{
		&domainearnrule.EarnRule{},
	}
}
