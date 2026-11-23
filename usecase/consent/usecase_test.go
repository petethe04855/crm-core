package consent

import (
	"context"
	"testing"
	"time"

	"crm-core/database"
	"crm-core/domain"
	domainconsent "crm-core/domain/consent"
	"crm-core/migrations"
	"crm-core/repository/postgres"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var owner = domain.Actor{ERPUserID: 1, Roles: []string{domain.ERPRoleOwner}}

func setup(t *testing.T) (Usecase, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), database.GormConfig("", logger.Silent))
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, migrations.Run(db, ""))
	return New(postgres.NewConsentRepository(db)), db
}

func create(t *testing.T, uc Usecase, docType string, required bool) *domainconsent.Document {
	t.Helper()
	past := time.Now().Add(-time.Minute)
	doc, err := uc.CreateDocument(context.Background(), owner, DocumentInput{
		Type: docType, Title: docType + " title", Content: "text", IsRequired: required, IsActive: true, EffectiveAt: &past,
	})
	require.NoError(t, err)
	return doc
}

func TestTableNames(t *testing.T) {
	_, db := setup(t)
	for _, name := range []string{"consent_documents", "consent_records", "otp_requests"} {
		require.True(t, db.Migrator().HasTable(name), name)
	}
}

func TestPendingUntilRequiredAccepted(t *testing.T) {
	uc, _ := setup(t)
	ctx := context.Background()

	pending, err := uc.Pending(ctx, "U1")
	require.NoError(t, err)
	require.False(t, pending, "nothing published yet")

	terms := create(t, uc, domainconsent.TypeTerms, true)
	pdpa := create(t, uc, domainconsent.TypePDPA, true)
	create(t, uc, domainconsent.TypeMarketingLine, false)

	pending, err = uc.Pending(ctx, "U1")
	require.NoError(t, err)
	require.True(t, pending)

	err = uc.Accept(ctx, AcceptInput{LineUserID: "U1", DocumentIDs: []uint{terms.ID}})
	require.ErrorIs(t, err, domain.ErrValidation, "PDPA is required too")

	require.NoError(t, uc.Accept(ctx, AcceptInput{LineUserID: "U1", DocumentIDs: []uint{terms.ID, pdpa.ID}}))
	pending, err = uc.Pending(ctx, "U1")
	require.NoError(t, err)
	require.False(t, pending)

	docs, err := uc.Current(ctx, "U1")
	require.NoError(t, err)
	require.Len(t, docs, 3)
	require.Equal(t, domainconsent.TypeTerms, docs[0].Type)
	require.True(t, docs[0].Accepted)
	require.False(t, docs[2].Accepted, "marketing was not ticked")
}

func TestNewRequiredVersionMustBeAcceptedAgain(t *testing.T) {
	uc, _ := setup(t)
	ctx := context.Background()
	v1 := create(t, uc, domainconsent.TypeTerms, true)
	require.NoError(t, uc.Accept(ctx, AcceptInput{LineUserID: "U1", DocumentIDs: []uint{v1.ID}}))

	v2 := create(t, uc, domainconsent.TypeTerms, true)
	require.Equal(t, 2, v2.Version)
	pending, err := uc.Pending(ctx, "U1")
	require.NoError(t, err)
	require.True(t, pending)

	err = uc.Accept(ctx, AcceptInput{LineUserID: "U1", DocumentIDs: []uint{v1.ID}})
	require.ErrorIs(t, err, domain.ErrValidation, "the old version is no longer current")
	require.NoError(t, uc.Accept(ctx, AcceptInput{LineUserID: "U1", DocumentIDs: []uint{v2.ID}}))
}

func TestFutureAndInactiveDocumentsAreNotCurrent(t *testing.T) {
	uc, _ := setup(t)
	ctx := context.Background()
	future := time.Now().Add(time.Hour)
	_, err := uc.CreateDocument(ctx, owner, DocumentInput{Type: domainconsent.TypeTerms, Title: "t", Content: "c", IsRequired: true, IsActive: true, EffectiveAt: &future})
	require.NoError(t, err)
	_, err = uc.CreateDocument(ctx, owner, DocumentInput{Type: domainconsent.TypePDPA, Title: "p", Content: "c", IsRequired: true, IsActive: false})
	require.NoError(t, err)
	docs, err := uc.Current(ctx, "U1")
	require.NoError(t, err)
	require.Empty(t, docs)
}

func TestMarketingWithdrawIsRecorded(t *testing.T) {
	uc, db := setup(t)
	ctx := context.Background()
	mkt := create(t, uc, domainconsent.TypeMarketingLine, false)
	require.NoError(t, uc.Accept(ctx, AcceptInput{LineUserID: "U1", DocumentIDs: []uint{mkt.ID}, IP: "1.1.1.1"}))
	require.NoError(t, uc.Accept(ctx, AcceptInput{LineUserID: "U1"}))

	var records []domainconsent.Record
	require.NoError(t, db.Order("id").Find(&records).Error)
	require.Len(t, records, 2)
	require.Equal(t, domainconsent.ActionAccept, records[0].Action)
	require.Equal(t, "1.1.1.1", records[0].IP)
	require.Equal(t, domainconsent.ActionWithdraw, records[1].Action)
}

func TestDocumentValidationAndEditLock(t *testing.T) {
	uc, _ := setup(t)
	ctx := context.Background()
	_, err := uc.CreateDocument(ctx, owner, DocumentInput{Type: "other", Title: "t", Content: "c"})
	require.ErrorIs(t, err, domain.ErrValidation)
	_, err = uc.CreateDocument(ctx, owner, DocumentInput{Type: domainconsent.TypeMarketingEmail, Title: "t", Content: "c", IsRequired: true})
	require.ErrorIs(t, err, domain.ErrValidation, "marketing cannot be required")
	_, err = uc.CreateDocument(ctx, owner, DocumentInput{Type: domainconsent.TypeTerms, Title: "t", Content: "c", PDFURL: "http://x"})
	require.ErrorIs(t, err, domain.ErrValidation)

	doc := create(t, uc, domainconsent.TypeTerms, true)
	edited, err := uc.UpdateDocument(ctx, doc.ID, DocumentInput{Title: "new title", Content: "new", IsRequired: true, IsActive: true})
	require.NoError(t, err)
	require.Equal(t, "new title", edited.Title)
	require.Equal(t, domainconsent.TypeTerms, edited.Type)

	require.NoError(t, uc.Accept(ctx, AcceptInput{LineUserID: "U1", DocumentIDs: []uint{doc.ID}}))
	_, err = uc.UpdateDocument(ctx, doc.ID, DocumentInput{Title: "again", Content: "x", IsRequired: true})
	require.ErrorIs(t, err, ErrDocumentInUse)

	off, err := uc.SetActive(ctx, doc.ID, false)
	require.NoError(t, err)
	require.False(t, off.IsActive)
}
