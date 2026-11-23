package productcode

import (
	"context"
	"testing"
	"time"

	"crm-core/database"
	"crm-core/domain"
	domainproductcode "crm-core/domain/productcode"
	"crm-core/migrations"
	"crm-core/repository/postgres"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var owner = domain.Actor{ERPUserID: 1, Email: "owner@example.com", Roles: []string{domain.ERPRoleOwner}}
var secret = []byte("product-code-secret")

func setup(t *testing.T) (Usecase, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), database.GormConfig("", logger.Silent))
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, migrations.Run(db, ""))
	return New(postgres.NewProductCodeRepository(db), postgres.NewProductCodeUnitOfWork(db), secret), db
}

func input(qty int) BatchInput {
	exp := time.Now().AddDate(1, 0, 0)
	return BatchInput{SKUID: 101, SKUCode: "CHK-01", SKUName: "ขนมไก่", SKUPrice: 59, Quantity: qty, PointsPerCode: 10, LotNumber: "LOT-2611-A", CodeExpiresAt: &exp}
}

func TestCreateValidatesAndNumbers(t *testing.T) {
	uc, _ := setup(t)
	ctx := context.Background()
	b, err := uc.Create(ctx, owner, input(10))
	require.NoError(t, err)
	require.Regexp(t, `^PC\d{4}-0001$`, b.BatchNo)
	require.Equal(t, domainproductcode.BatchDraft, b.Status)
	require.InDelta(t, 59, b.SpendValuePerCode, 0.001, "spend value defaults to the SKU price")

	bad := input(0)
	_, err = uc.Create(ctx, owner, bad)
	require.ErrorIs(t, err, domain.ErrValidation)
	bad = input(domainproductcode.MaxBatchQuantity + 1)
	_, err = uc.Create(ctx, owner, bad)
	require.ErrorIs(t, err, domain.ErrValidation)
	bad = input(1)
	bad.SKUID = 0
	_, err = uc.Create(ctx, owner, bad)
	require.ErrorIs(t, err, domain.ErrValidation)
	bad = input(1)
	past := time.Now().Add(-time.Hour)
	bad.CodeExpiresAt = &past
	_, err = uc.Create(ctx, owner, bad)
	require.ErrorIs(t, err, domain.ErrValidation)

	upd := input(20)
	upd.PointsPerCode = 15
	b, err = uc.Update(ctx, b.ID, upd)
	require.NoError(t, err)
	require.Equal(t, 20, b.Quantity)
}

func TestExportOnceStoresOnlyHashes(t *testing.T) {
	uc, db := setup(t)
	ctx := context.Background()
	b, err := uc.Create(ctx, owner, input(50))
	require.NoError(t, err)

	exp, err := uc.Export(ctx, owner, b.ID)
	require.NoError(t, err)
	require.Len(t, exp.Codes, 50)
	require.Equal(t, domainproductcode.BatchExported, exp.Batch.Status)

	var rows []domainproductcode.Code
	require.NoError(t, db.Where("batch_id = ?", b.ID).Find(&rows).Error)
	require.Len(t, rows, 50)
	hashes := map[string]bool{}
	for _, r := range rows {
		hashes[r.CodeHash] = true
	}
	for _, printed := range exp.Codes {
		code, err := domainproductcode.Normalize(printed)
		require.NoError(t, err)
		require.True(t, hashes[domainproductcode.Hash(secret, code)], "every printed code is stored as a hash")
		for _, r := range rows {
			require.NotEqual(t, code, r.CodeHash)
		}
	}

	_, err = uc.Export(ctx, owner, b.ID)
	require.ErrorIs(t, err, domainproductcode.ErrAlreadyExported, "codes can be exported only once")
	_, err = uc.Update(ctx, b.ID, input(5))
	require.ErrorIs(t, err, domainproductcode.ErrNotDraft)
}

func TestLargeBatchHasNoDuplicates(t *testing.T) {
	if testing.Short() {
		t.Skip("100k codes")
	}
	uc, db := setup(t)
	ctx := context.Background()
	b, err := uc.Create(ctx, owner, input(domainproductcode.MaxBatchQuantity))
	require.NoError(t, err)
	exp, err := uc.Export(ctx, owner, b.ID)
	require.NoError(t, err)
	require.Len(t, exp.Codes, domainproductcode.MaxBatchQuantity)
	seen := make(map[string]bool, len(exp.Codes))
	for _, c := range exp.Codes {
		require.False(t, seen[c], "duplicate %s", c)
		seen[c] = true
	}
	var count int64
	require.NoError(t, db.Model(&domainproductcode.Code{}).Where("batch_id = ?", b.ID).Count(&count).Error)
	require.EqualValues(t, domainproductcode.MaxBatchQuantity, count)
}

// repeatReader returns the same bytes over and over, forcing hash collisions.
type repeatReader struct{ b byte }

func (r repeatReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = r.b
	}
	return len(p), nil
}

func TestExportGivesUpWhenItCannotMakeUniqueCodes(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), database.GormConfig("", logger.Silent))
	require.NoError(t, err)
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, migrations.Run(db, ""))
	uc := New(postgres.NewProductCodeRepository(db), postgres.NewProductCodeUnitOfWork(db), secret, WithRandom(repeatReader{7}))
	ctx := context.Background()
	b, err := uc.Create(ctx, owner, input(2))
	require.NoError(t, err)
	_, err = uc.Export(ctx, owner, b.ID)
	require.Error(t, err)
	got, err := uc.Get(ctx, b.ID)
	require.NoError(t, err)
	require.Equal(t, domainproductcode.BatchDraft, got.Status, "a failed export rolls back")
	require.Zero(t, got.Stats.Total)
}

func TestActivateAndVoid(t *testing.T) {
	uc, _ := setup(t)
	ctx := context.Background()

	noLot := input(3)
	noLot.LotNumber = ""
	b, err := uc.Create(ctx, owner, noLot)
	require.NoError(t, err)
	_, err = uc.Activate(ctx, owner, b.ID)
	require.ErrorIs(t, err, domainproductcode.ErrNotExported)
	_, err = uc.Export(ctx, owner, b.ID)
	require.NoError(t, err)
	_, err = uc.Activate(ctx, owner, b.ID)
	require.ErrorIs(t, err, domainproductcode.ErrLotRequired)

	test := noLot
	test.IsTest = true
	tb, err := uc.Create(ctx, owner, test)
	require.NoError(t, err)
	_, err = uc.Export(ctx, owner, tb.ID)
	require.NoError(t, err)
	active, err := uc.Activate(ctx, owner, tb.ID)
	require.NoError(t, err, "test batches need no lot")
	require.Equal(t, domainproductcode.BatchActive, active.Status)

	_, err = uc.Void(ctx, owner, tb.ID, "")
	require.ErrorIs(t, err, domain.ErrValidation, "a reason is required")
	voided, err := uc.Void(ctx, owner, tb.ID, "พิมพ์ผิด")
	require.NoError(t, err)
	require.Equal(t, domainproductcode.BatchVoid, voided.Status)
	got, err := uc.Get(ctx, tb.ID)
	require.NoError(t, err)
	require.EqualValues(t, 3, got.Stats.Void)
	require.Zero(t, got.Stats.Active)
	_, err = uc.Void(ctx, owner, tb.ID, "again")
	require.ErrorIs(t, err, domainproductcode.ErrAlreadyVoid)
	_, err = uc.Activate(ctx, owner, tb.ID)
	require.ErrorIs(t, err, domainproductcode.ErrAlreadyVoid)
}

func TestListSearchAndStats(t *testing.T) {
	uc, _ := setup(t)
	ctx := context.Background()
	a, err := uc.Create(ctx, owner, input(4))
	require.NoError(t, err)
	_, err = uc.Export(ctx, owner, a.ID)
	require.NoError(t, err)
	other := input(2)
	other.SKUCode = "FISH-02"
	other.SKUName = "ปลาเส้น"
	_, err = uc.Create(ctx, owner, other)
	require.NoError(t, err)

	list, total, err := uc.List(ctx, domainproductcode.Query{Search: "fish"})
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Equal(t, "FISH-02", list[0].SKUCode)

	list, _, err = uc.List(ctx, domainproductcode.Query{Status: domainproductcode.BatchExported})
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.EqualValues(t, 4, list[0].Stats.Total)
	require.EqualValues(t, 4, list[0].Stats.Active)
}

func TestExportNeedsSecret(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), database.GormConfig("", logger.Silent))
	require.NoError(t, err)
	require.NoError(t, migrations.Run(db, ""))
	uc := New(postgres.NewProductCodeRepository(db), postgres.NewProductCodeUnitOfWork(db), nil)
	b, err := uc.Create(context.Background(), owner, input(1))
	require.NoError(t, err)
	_, err = uc.Export(context.Background(), owner, b.ID)
	require.ErrorIs(t, err, domainproductcode.ErrNotConfigured)
}
