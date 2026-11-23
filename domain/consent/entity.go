// Package consent stores the PDPA consent documents members accept in the LINE
// OA (terms, privacy notice, marketing) and an append-only record of every
// accept / withdraw.
package consent

import (
	"context"
	"time"

	"gorm.io/gorm/schema"
)

// Document types. Terms and PDPA are normally required; marketing consents are
// optional and can be withdrawn at any time.
const (
	TypeTerms          = "terms"
	TypePDPA           = "pdpa"
	TypeMarketingLine  = "marketing_line"
	TypeMarketingEmail = "marketing_email"
)

// Types lists every document type in display order.
var Types = []string{TypeTerms, TypePDPA, TypeMarketingLine, TypeMarketingEmail}

// Record actions.
const (
	ActionAccept   = "accept"
	ActionWithdraw = "withdraw"
)

// Document is one version of a consent document. A new version of the same
// type is a new row; the current document of a type is the active row with
// the highest version whose EffectiveAt has passed. Content of a version is
// never edited after members accepted it.
type Document struct {
	ID      uint   `gorm:"primaryKey"`
	Type    string `gorm:"size:30;not null;uniqueIndex:ux_consent_documents_type_version,priority:1"`
	Version int    `gorm:"not null;uniqueIndex:ux_consent_documents_type_version,priority:2"`
	Title   string `gorm:"size:255;not null"`
	// Content is plain text (paragraphs separated by blank lines). It is shown
	// as text in LIFF, never as HTML, so a document cannot inject markup.
	Content     string    `gorm:"type:text;not null"`
	PDFURL      string    `gorm:"column:pdf_url;size:1000"`
	IsRequired  bool      `gorm:"not null;default:false"`
	EffectiveAt time.Time `gorm:"not null;index"`
	IsActive    bool      `gorm:"not null;default:false;index"`
	CreatedBy   uint      `gorm:"not null;default:0"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// TableName names the table consent_documents and keeps the schema prefix
// (a plain TableName() string would bypass the naming strategy).
func (Document) TableName(namer schema.Namer) string { return namer.TableName("ConsentDocument") }

// Record is one accept / withdraw event. Append-only: rows are never updated.
// LineUserID is always set (consent is given before registration); MemberID is
// filled once the LINE user is a member.
type Record struct {
	ID              uint   `gorm:"primaryKey"`
	LineUserID      string `gorm:"size:64;not null;index"`
	MemberID        *uint  `gorm:"index"`
	DocumentID      uint   `gorm:"not null;index"`
	DocumentType    string `gorm:"size:30;not null"`
	DocumentVersion int    `gorm:"not null"`
	Action          string `gorm:"size:20;not null"`
	IP              string `gorm:"size:64"`
	UserAgent       string `gorm:"size:500"`
	CreatedAt       time.Time
}

// TableName names the table consent_records and keeps the schema prefix.
func (Record) TableName(namer schema.Namer) string { return namer.TableName("ConsentRecord") }

// Repository is the persistence contract for consent documents and records.
type Repository interface {
	// ListDocuments returns every version, newest first (backoffice).
	ListDocuments(ctx context.Context) ([]Document, error)
	FindDocument(ctx context.Context, id uint) (*Document, error)
	// CurrentDocuments returns, per type, the active document with the highest
	// version effective at now.
	CurrentDocuments(ctx context.Context, now time.Time) ([]Document, error)
	MaxVersion(ctx context.Context, docType string) (int, error)
	CreateDocument(ctx context.Context, doc *Document) error
	UpdateDocument(ctx context.Context, doc *Document) error
	// HasRecords reports whether anyone accepted or withdrew the document.
	HasRecords(ctx context.Context, documentID uint) (bool, error)
	// LatestActions returns the latest action per document id for the LINE user.
	LatestActions(ctx context.Context, lineUserID string, documentIDs []uint) (map[uint]string, error)
	CreateRecords(ctx context.Context, records []Record) error
	// Records lists every answer of a LINE user, oldest first (PDPA export).
	Records(ctx context.Context, lineUserID string) ([]Record, error)
}
