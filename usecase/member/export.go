package member

import (
	"context"
	"fmt"
	"time"

	"crm-core/domain"
	domainloyalty "crm-core/domain/loyalty"
	domainmember "crm-core/domain/member"
)

// ExportMaxRows caps synchronous exports. Larger exports go through the worker
// (not built yet), so the caller must narrow the filter.
const ExportMaxRows = 50000

const exportPageSize = 1000

// ExportRow is one member row prepared for a spreadsheet export.
type ExportRow struct {
	MemberCode    string
	FirstName     string
	LastName      string
	Email         string
	Phone         string
	TierName      string
	Status        string
	PointBalance  int64
	LifetimeSpend float64
	RegisteredAt  time.Time
}

// Exporter exports members. Emails and phones are masked unless the actor is
// an owner (PDPA: only owners receive unmasked personal data in files).
type Exporter interface {
	Export(ctx context.Context, actor domain.Actor, q domainmember.Query) ([]ExportRow, error)
}

// NewExporter builds the member exporter.
func NewExporter(members domainmember.Repository, tiers domainloyalty.TierRepository) Exporter {
	return &usecase{members: members, tiers: tiers}
}

func (u *usecase) Export(ctx context.Context, actor domain.Actor, q domainmember.Query) ([]ExportRow, error) {
	q, err := NormalizeQuery(q)
	if err != nil {
		return nil, err
	}
	tiers, err := u.tiers.FindAll(ctx)
	if err != nil {
		return nil, err
	}
	tierNames := make(map[uint]string, len(tiers))
	for _, t := range tiers {
		tierNames[t.ID] = t.Name
	}

	unmasked := actor.IsOwner()
	rows := []ExportRow{}
	q.Limit = exportPageSize
	for page := 1; ; page++ {
		q.Page = page
		members, total, err := u.members.FindAll(ctx, q)
		if err != nil {
			return nil, err
		}
		if total > ExportMaxRows {
			return nil, domain.NewValidationError("filter",
				fmt.Sprintf("export has %d rows, more than the %d row limit; narrow the filter", total, ExportMaxRows))
		}
		for _, m := range members {
			rows = append(rows, toExportRow(m, tierNames[m.TierID], unmasked))
		}
		if len(members) < exportPageSize || int64(len(rows)) >= total {
			return rows, nil
		}
	}
}

func toExportRow(m domainmember.Member, tierName string, unmasked bool) ExportRow {
	email := m.Email
	phone := ""
	if m.PhoneE164 != nil {
		phone = *m.PhoneE164
	}
	if !unmasked {
		email = domainmember.MaskEmail(email)
		phone = domainmember.MaskPhone(phone)
	}
	return ExportRow{
		MemberCode:    m.MemberCode,
		FirstName:     m.FirstName,
		LastName:      m.LastName,
		Email:         email,
		Phone:         phone,
		TierName:      tierName,
		Status:        m.Status,
		PointBalance:  m.PointBalance,
		LifetimeSpend: m.LifetimeSpend,
		RegisteredAt:  m.CreatedAt,
	}
}
