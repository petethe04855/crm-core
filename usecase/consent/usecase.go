// Package consent serves the PDPA consent documents: the LINE OA shows the
// current versions and records what the member accepts; the backoffice
// publishes new versions.
package consent

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"crm-core/domain"
	domainconsent "crm-core/domain/consent"
)

// CurrentDocument is a current document with the LINE user's latest answer.
type CurrentDocument struct {
	domainconsent.Document
	Accepted bool
}

// AcceptInput is what the member submitted on the consent screen.
type AcceptInput struct {
	LineUserID string
	MemberID   *uint
	// DocumentIDs are the documents the member ticked. Current optional
	// documents left out are withdrawn if they had been accepted before.
	DocumentIDs []uint
	IP          string
	UserAgent   string
}

// OptionalInput changes one optional consent.
type OptionalInput struct {
	LineUserID string
	MemberID   *uint
	Type       string
	Accept     bool
	IP         string
	UserAgent  string
}

// ErrNoDocument: no current document of that type is published.
var ErrNoDocument = errors.New("no current consent document of this type")

// DocumentInput creates or edits a document version in the backoffice.
type DocumentInput struct {
	Type        string
	Title       string
	Content     string
	PDFURL      string
	IsRequired  bool
	EffectiveAt *time.Time // nil = now
	IsActive    bool
}

// ErrDocumentInUse means members already answered this version: publish a new
// version instead of editing it.
var ErrDocumentInUse = errors.New("consent document already has answers")

// Usecase is the consent workflow.
type Usecase interface {
	Current(ctx context.Context, lineUserID string) ([]CurrentDocument, error)
	// Pending reports whether a current required document is not accepted.
	Pending(ctx context.Context, lineUserID string) (bool, error)
	Accept(ctx context.Context, in AcceptInput) error
	// SetOptional accepts or withdraws the current optional document of a
	// type (marketing consents) without touching the others.
	SetOptional(ctx context.Context, in OptionalInput) error
	// Records lists the LINE user's answers (PDPA export).
	Records(ctx context.Context, lineUserID string) ([]domainconsent.Record, error)

	ListDocuments(ctx context.Context) ([]domainconsent.Document, error)
	CreateDocument(ctx context.Context, actor domain.Actor, in DocumentInput) (*domainconsent.Document, error)
	UpdateDocument(ctx context.Context, id uint, in DocumentInput) (*domainconsent.Document, error)
	SetActive(ctx context.Context, id uint, active bool) (*domainconsent.Document, error)
}

type usecase struct {
	repo domainconsent.Repository
	now  func() time.Time
}

// New builds the consent usecase.
func New(repo domainconsent.Repository) Usecase {
	return &usecase{repo: repo, now: time.Now}
}

func (u *usecase) Current(ctx context.Context, lineUserID string) ([]CurrentDocument, error) {
	docs, err := u.repo.CurrentDocuments(ctx, u.now())
	if err != nil {
		return nil, err
	}
	ids := make([]uint, 0, len(docs))
	for _, d := range docs {
		ids = append(ids, d.ID)
	}
	actions, err := u.repo.LatestActions(ctx, lineUserID, ids)
	if err != nil {
		return nil, err
	}
	out := make([]CurrentDocument, 0, len(docs))
	for _, t := range domainconsent.Types { // stable display order
		for _, d := range docs {
			if d.Type == t {
				out = append(out, CurrentDocument{Document: d, Accepted: actions[d.ID] == domainconsent.ActionAccept})
			}
		}
	}
	return out, nil
}

func (u *usecase) Pending(ctx context.Context, lineUserID string) (bool, error) {
	docs, err := u.Current(ctx, lineUserID)
	if err != nil {
		return false, err
	}
	for _, d := range docs {
		if d.IsRequired && !d.Accepted {
			return true, nil
		}
	}
	return false, nil
}

func (u *usecase) Accept(ctx context.Context, in AcceptInput) error {
	if strings.TrimSpace(in.LineUserID) == "" {
		return domain.NewValidationError("lineUserId", "is required")
	}
	docs, err := u.Current(ctx, in.LineUserID)
	if err != nil {
		return err
	}
	ticked := map[uint]bool{}
	for _, id := range in.DocumentIDs {
		ticked[id] = true
	}
	current := map[uint]bool{}
	for _, d := range docs {
		current[d.ID] = true
	}
	for id := range ticked {
		if !current[id] {
			// An old version or an unknown id: the member must reload the screen.
			return domain.NewValidationError("documentIds", "contains a document that is not current")
		}
	}

	now := u.now()
	var records []domainconsent.Record
	for _, d := range docs {
		action := ""
		switch {
		case ticked[d.ID] && !d.Accepted:
			action = domainconsent.ActionAccept
		case !ticked[d.ID] && d.IsRequired && !d.Accepted:
			return domain.NewValidationError("documentIds", "must accept "+d.Title)
		case !ticked[d.ID] && !d.IsRequired && d.Accepted:
			action = domainconsent.ActionWithdraw
		}
		if action == "" {
			continue
		}
		records = append(records, domainconsent.Record{
			LineUserID:      in.LineUserID,
			MemberID:        in.MemberID,
			DocumentID:      d.ID,
			DocumentType:    d.Type,
			DocumentVersion: d.Version,
			Action:          action,
			IP:              truncate(in.IP, 64),
			UserAgent:       truncate(in.UserAgent, 500),
			CreatedAt:       now,
		})
	}
	return u.repo.CreateRecords(ctx, records)
}

func (u *usecase) ListDocuments(ctx context.Context) ([]domainconsent.Document, error) {
	return u.repo.ListDocuments(ctx)
}

func (u *usecase) CreateDocument(ctx context.Context, actor domain.Actor, in DocumentInput) (*domainconsent.Document, error) {
	if err := validate(&in); err != nil {
		return nil, err
	}
	max, err := u.repo.MaxVersion(ctx, in.Type)
	if err != nil {
		return nil, err
	}
	doc := &domainconsent.Document{
		Type:      in.Type,
		Version:   max + 1,
		CreatedBy: actor.ERPUserID,
	}
	apply(doc, in, u.now())
	if err := u.repo.CreateDocument(ctx, doc); err != nil {
		return nil, err
	}
	return doc, nil
}

func (u *usecase) UpdateDocument(ctx context.Context, id uint, in DocumentInput) (*domainconsent.Document, error) {
	doc, err := u.repo.FindDocument(ctx, id)
	if err != nil {
		return nil, err
	}
	in.Type = doc.Type // a version never changes type
	if err := validate(&in); err != nil {
		return nil, err
	}
	used, err := u.repo.HasRecords(ctx, id)
	if err != nil {
		return nil, err
	}
	if used {
		return nil, ErrDocumentInUse
	}
	apply(doc, in, u.now())
	if err := u.repo.UpdateDocument(ctx, doc); err != nil {
		return nil, err
	}
	return doc, nil
}

func (u *usecase) SetActive(ctx context.Context, id uint, active bool) (*domainconsent.Document, error) {
	doc, err := u.repo.FindDocument(ctx, id)
	if err != nil {
		return nil, err
	}
	doc.IsActive = active
	if err := u.repo.UpdateDocument(ctx, doc); err != nil {
		return nil, err
	}
	return doc, nil
}

func validate(in *DocumentInput) error {
	in.Type = strings.TrimSpace(in.Type)
	in.Title = strings.TrimSpace(in.Title)
	in.Content = strings.TrimSpace(in.Content)
	in.PDFURL = strings.TrimSpace(in.PDFURL)
	known := false
	for _, t := range domainconsent.Types {
		if t == in.Type {
			known = true
		}
	}
	switch {
	case !known:
		return domain.NewValidationError("type", "must be one of: "+strings.Join(domainconsent.Types, ", "))
	case in.Title == "":
		return domain.NewValidationError("title", "is required")
	case utf8.RuneCountInString(in.Title) > 255:
		return domain.NewValidationError("title", "must be at most 255 characters")
	case in.Content == "":
		return domain.NewValidationError("content", "is required")
	case utf8.RuneCountInString(in.Content) > 100000:
		return domain.NewValidationError("content", "is too long")
	case in.PDFURL != "" && !strings.HasPrefix(in.PDFURL, "https://"):
		return domain.NewValidationError("pdfUrl", "must start with https://")
	case in.IsRequired && strings.HasPrefix(in.Type, "marketing_"):
		// PDPA: marketing consent must be a free choice.
		return domain.NewValidationError("isRequired", "marketing consent cannot be required")
	}
	return nil
}

func apply(doc *domainconsent.Document, in DocumentInput, now time.Time) {
	doc.Title = in.Title
	doc.Content = in.Content
	doc.PDFURL = in.PDFURL
	doc.IsRequired = in.IsRequired
	doc.IsActive = in.IsActive
	doc.EffectiveAt = now
	if in.EffectiveAt != nil {
		doc.EffectiveAt = *in.EffectiveAt
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func (u *usecase) SetOptional(ctx context.Context, in OptionalInput) error {
	if strings.TrimSpace(in.LineUserID) == "" {
		return domain.NewValidationError("lineUserId", "is required")
	}
	docs, err := u.Current(ctx, in.LineUserID)
	if err != nil {
		return err
	}
	for _, d := range docs {
		if d.Type != in.Type {
			continue
		}
		if d.IsRequired {
			return domain.NewValidationError("type", "required consents cannot be withdrawn here (delete the account instead)")
		}
		if d.Accepted == in.Accept {
			return nil
		}
		action := ActionFor(in.Accept)
		return u.repo.CreateRecords(ctx, []domainconsent.Record{{
			LineUserID: in.LineUserID, MemberID: in.MemberID, DocumentID: d.ID, DocumentType: d.Type,
			DocumentVersion: d.Version, Action: action, IP: truncate(in.IP, 64), UserAgent: truncate(in.UserAgent, 500),
			CreatedAt: u.now(),
		}})
	}
	return ErrNoDocument
}

// ActionFor maps accept/withdraw.
func ActionFor(accept bool) string {
	if accept {
		return domainconsent.ActionAccept
	}
	return domainconsent.ActionWithdraw
}

func (u *usecase) Records(ctx context.Context, lineUserID string) ([]domainconsent.Record, error) {
	return u.repo.Records(ctx, lineUserID)
}
