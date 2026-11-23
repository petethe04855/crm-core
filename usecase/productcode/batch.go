// Package productcode manages packaging-code batches in the backoffice (L4B):
//
//	draft ──export──► exported ──activate──► active
//	  └────────────── void (any time) ─────────┘
//
// Export generates the codes and returns them once for the print file; only
// their HMAC and last four characters are stored, so a second export is
// impossible by design.
package productcode

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"crm-core/domain"
	domainproductcode "crm-core/domain/productcode"
	"crm-core/timeutil"
)

// BatchInput creates or edits a draft batch.
type BatchInput struct {
	SKUID             uint
	SKUCode           string
	SKUName           string
	SKUPrice          float64
	Quantity          int
	PointsPerCode     int64
	SpendValuePerCode *float64 // nil = the SKU price
	LotNumber         string
	LotExpiryDate     *time.Time
	CodeExpiresAt     *time.Time
	IsTest            bool
	Note              string
}

// Export is the one-time result of exporting a batch: the plain codes, in
// print order, formatted XXXX-XXXX-XXXX.
type Export struct {
	Batch domainproductcode.Batch
	Codes []string
}

// Usecase is the backoffice batch workflow.
type Usecase interface {
	List(ctx context.Context, q domainproductcode.Query) ([]domainproductcode.BatchWithStats, int64, error)
	Get(ctx context.Context, id uint) (*domainproductcode.BatchWithStats, error)
	Create(ctx context.Context, actor domain.Actor, in BatchInput) (*domainproductcode.Batch, error)
	Update(ctx context.Context, id uint, in BatchInput) (*domainproductcode.Batch, error)
	Export(ctx context.Context, actor domain.Actor, id uint) (*Export, error)
	Activate(ctx context.Context, actor domain.Actor, id uint) (*domainproductcode.Batch, error)
	Void(ctx context.Context, actor domain.Actor, id uint, reason string) (*domainproductcode.Batch, error)
}

type usecase struct {
	repo   domainproductcode.Repository
	uow    domainproductcode.UnitOfWork
	secret []byte
	rand   io.Reader
	now    func() time.Time
}

// Option customizes the usecase.
type Option func(*usecase)

// WithClock replaces time.Now (tests).
func WithClock(now func() time.Time) Option { return func(u *usecase) { u.now = now } }

// WithRandom replaces crypto/rand (tests).
func WithRandom(r io.Reader) Option { return func(u *usecase) { u.rand = r } }

// New builds the batch usecase. secret is PRODUCT_CODE_HMAC_SECRET and must be
// the same in line-oa-api, which hashes what members type.
func New(repo domainproductcode.Repository, uow domainproductcode.UnitOfWork, secret []byte, opts ...Option) Usecase {
	u := &usecase{repo: repo, uow: uow, secret: secret, rand: rand.Reader, now: time.Now}
	for _, opt := range opts {
		opt(u)
	}
	return u
}

func (u *usecase) List(ctx context.Context, q domainproductcode.Query) ([]domainproductcode.BatchWithStats, int64, error) {
	return u.repo.FindAll(ctx, q)
}

func (u *usecase) Get(ctx context.Context, id uint) (*domainproductcode.BatchWithStats, error) {
	return u.repo.FindByID(ctx, id)
}

func (u *usecase) validate(in BatchInput) (BatchInput, error) {
	in.SKUCode = strings.TrimSpace(in.SKUCode)
	in.SKUName = strings.TrimSpace(in.SKUName)
	in.LotNumber = strings.TrimSpace(in.LotNumber)
	in.Note = strings.TrimSpace(in.Note)
	switch {
	case in.SKUID == 0 || in.SKUCode == "" || in.SKUName == "":
		return in, domain.NewValidationError("erpSkuId", "choose a SKU from the ERP")
	case in.SKUPrice < 0:
		return in, domain.NewValidationError("skuPrice", "must not be negative")
	case in.Quantity < 1 || in.Quantity > domainproductcode.MaxBatchQuantity:
		return in, domain.NewValidationError("quantity", fmt.Sprintf("must be between 1 and %d", domainproductcode.MaxBatchQuantity))
	case in.PointsPerCode < 1 || in.PointsPerCode > domainproductcode.MaxPointsPerCode:
		return in, domain.NewValidationError("pointsPerCode", fmt.Sprintf("must be between 1 and %d", domainproductcode.MaxPointsPerCode))
	case in.SpendValuePerCode != nil && *in.SpendValuePerCode < 0:
		return in, domain.NewValidationError("spendValuePerCode", "must not be negative")
	case utf8.RuneCountInString(in.LotNumber) > 100:
		return in, domain.NewValidationError("lotNumber", "must be at most 100 characters")
	case utf8.RuneCountInString(in.Note) > 500:
		return in, domain.NewValidationError("note", "must be at most 500 characters")
	case in.CodeExpiresAt != nil && !in.CodeExpiresAt.After(u.now()):
		return in, domain.NewValidationError("codeExpiresAt", "must be in the future")
	}
	if in.LotExpiryDate != nil {
		d := in.LotExpiryDate.In(timeutil.Bangkok())
		date := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, timeutil.Bangkok())
		in.LotExpiryDate = &date
	}
	return in, nil
}

func apply(b *domainproductcode.Batch, in BatchInput) {
	b.SKUID = in.SKUID
	b.SKUCode = in.SKUCode
	b.SKUNameSnapshot = in.SKUName
	b.SKUPriceSnapshot = in.SKUPrice
	b.Quantity = in.Quantity
	b.PointsPerCode = in.PointsPerCode
	b.SpendValuePerCode = in.SKUPrice
	if in.SpendValuePerCode != nil {
		b.SpendValuePerCode = *in.SpendValuePerCode
	}
	b.LotNumber = in.LotNumber
	b.LotExpiryDate = in.LotExpiryDate
	b.CodeExpiresAt = in.CodeExpiresAt
	b.IsTest = in.IsTest
	b.Note = in.Note
}

func (u *usecase) Create(ctx context.Context, actor domain.Actor, in BatchInput) (*domainproductcode.Batch, error) {
	in, err := u.validate(in)
	if err != nil {
		return nil, err
	}
	now := u.now()
	b := &domainproductcode.Batch{
		Status:         domainproductcode.BatchDraft,
		CreatedBy:      actor.ERPUserID,
		CreatedByEmail: actor.Email,
	}
	// Random placeholder until the id is known (a clock value can repeat).
	var buf [8]byte
	if _, err := io.ReadFull(rand.Reader, buf[:]); err != nil {
		return nil, err
	}
	b.BatchNo = "TMP" + hex.EncodeToString(buf[:])
	apply(b, in)
	if err := u.repo.Create(ctx, b); err != nil {
		return nil, err
	}
	b.BatchNo = fmt.Sprintf("PC%s-%04d", now.In(timeutil.Bangkok()).Format("0601"), b.ID)
	if err := u.repo.Update(ctx, b); err != nil {
		return nil, err
	}
	return b, nil
}

func (u *usecase) Update(ctx context.Context, id uint, in BatchInput) (*domainproductcode.Batch, error) {
	current, err := u.repo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if current.Status != domainproductcode.BatchDraft {
		return nil, domainproductcode.ErrNotDraft
	}
	in, err = u.validate(in)
	if err != nil {
		return nil, err
	}
	b := current.Batch
	apply(&b, in)
	if err := u.repo.Update(ctx, &b); err != nil {
		return nil, err
	}
	return &b, nil
}

func (u *usecase) Export(ctx context.Context, actor domain.Actor, id uint) (*Export, error) {
	if len(u.secret) == 0 {
		return nil, domainproductcode.ErrNotConfigured
	}
	var out *Export
	err := u.uow.Do(ctx, func(ctx context.Context, tx domainproductcode.Tx) error {
		b, err := tx.LockBatch(ctx, id)
		if err != nil {
			return err
		}
		switch b.Status {
		case domainproductcode.BatchDraft:
		case domainproductcode.BatchVoid:
			return domainproductcode.ErrAlreadyVoid
		default:
			return domainproductcode.ErrAlreadyExported
		}

		now := u.now()
		codes, err := u.generate(ctx, tx, b.ID, b.Quantity, now)
		if err != nil {
			return err
		}
		b.Status = domainproductcode.BatchExported
		b.ExportedAt = &now
		b.ExportedBy = actor.ERPUserID
		if err := tx.UpdateBatch(ctx, b); err != nil {
			return err
		}
		out = &Export{Batch: *b, Codes: codes}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// maxGenerateRounds bounds top-up rounds after hash collisions (each round
// only regenerates the few codes that collided).
const maxGenerateRounds = 5

// generate makes n distinct codes, stores their hashes and returns the plain
// codes formatted for printing.
func (u *usecase) generate(ctx context.Context, tx domainproductcode.Tx, batchID uint, n int, now time.Time) ([]string, error) {
	printed := make([]string, 0, n)
	seen := make(map[string]bool, n)
	need := n
	for round := 0; need > 0; round++ {
		if round >= maxGenerateRounds {
			return nil, fmt.Errorf("could not generate %d unique codes", n)
		}
		plain := make(map[string]string, need) // hash -> code
		rows := make([]domainproductcode.Code, 0, need)
		order := make([]string, 0, need)
		// A broken random source would repeat codes forever: bound the draws.
		for draws := 0; len(rows) < need; draws++ {
			if draws > need*4+100 {
				return nil, fmt.Errorf("could not generate %d unique codes", n)
			}
			code, err := domainproductcode.Generate(u.rand)
			if err != nil {
				return nil, err
			}
			if seen[code] {
				continue
			}
			seen[code] = true
			h := domainproductcode.Hash(u.secret, code)
			plain[h] = code
			order = append(order, h)
			rows = append(rows, domainproductcode.Code{
				BatchID: batchID, CodeHash: h, CodeSuffix: domainproductcode.Suffix(code),
				Status: domainproductcode.CodeActive, CreatedAt: now,
			})
		}
		skipped, err := tx.InsertCodes(ctx, rows)
		if err != nil {
			return nil, err
		}
		for _, h := range order {
			if !skipped[h] {
				printed = append(printed, domainproductcode.Format(plain[h]))
			}
		}
		need = n - len(printed)
	}
	return printed, nil
}

func (u *usecase) Activate(ctx context.Context, actor domain.Actor, id uint) (*domainproductcode.Batch, error) {
	var out *domainproductcode.Batch
	err := u.uow.Do(ctx, func(ctx context.Context, tx domainproductcode.Tx) error {
		b, err := tx.LockBatch(ctx, id)
		if err != nil {
			return err
		}
		switch {
		case b.Status == domainproductcode.BatchVoid:
			return domainproductcode.ErrAlreadyVoid
		case b.Status == domainproductcode.BatchActive:
			out = b
			return nil
		case b.Status != domainproductcode.BatchExported:
			return domainproductcode.ErrNotExported
		case !b.IsTest && b.LotNumber == "" && b.ERPLotID == nil:
			return domainproductcode.ErrLotRequired
		case b.CodeExpiresAt != nil && !b.CodeExpiresAt.After(u.now()):
			return domainproductcode.ErrExpired
		}
		now := u.now()
		b.Status = domainproductcode.BatchActive
		b.ActivatedAt = &now
		b.ActivatedBy = actor.ERPUserID
		if err := tx.UpdateBatch(ctx, b); err != nil {
			return err
		}
		out = b
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (u *usecase) Void(ctx context.Context, actor domain.Actor, id uint, reason string) (*domainproductcode.Batch, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, domain.NewValidationError("reason", "is required")
	}
	if utf8.RuneCountInString(reason) > 500 {
		return nil, domain.NewValidationError("reason", "must be at most 500 characters")
	}
	var out *domainproductcode.Batch
	err := u.uow.Do(ctx, func(ctx context.Context, tx domainproductcode.Tx) error {
		b, err := tx.LockBatch(ctx, id)
		if err != nil {
			return err
		}
		if b.Status == domainproductcode.BatchVoid {
			return domainproductcode.ErrAlreadyVoid
		}
		// Used codes stay used (their points were earned); unused ones die.
		if _, err := tx.VoidActiveCodes(ctx, b.ID); err != nil {
			return err
		}
		now := u.now()
		b.Status = domainproductcode.BatchVoid
		b.VoidedAt = &now
		b.VoidedBy = actor.ERPUserID
		b.VoidReason = reason
		if err := tx.UpdateBatch(ctx, b); err != nil {
			return err
		}
		out = b
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
