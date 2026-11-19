# crm-core

Go module กลางของ CRM: domain, usecase, repository และ migration
ใช้ร่วมกันโดย `backoffice-api` (และ `api-application` ในอนาคต) เพื่อไม่ให้กฎแต้ม/ledger ถูกเขียนซ้ำ 2 ที่

```text
domain/               entities + repository interfaces (member, loyalty, staffrole), errors, Actor
usecase/              member (list/get/export), staffrole, tier
repository/postgres/  GORM implementations (query แบบ portable เพื่อให้เทสบน SQLite ได้)
database/             GormConfig: prefix ตารางด้วย "<schema>."
migrations/           Run(): สร้าง schema + AutoMigrate + seed ระดับ bronze
```

ผู้ใช้ import ผ่าน `replace crm-core => ../crm-core` (ไม่ใช้ `go.work` ที่ root เพราะจะทำให้ `go build` ใน `erp-api-v2` / `erp-expense-api` ใช้ไม่ได้)

```bash
go test ./...
```

## แต้ม: วิธีเรียกใช้สำหรับคูปอง (T4) และของรางวัล (T6)

`usecase/points` เป็นที่เดียวที่เปลี่ยนแต้ม ห้ามเขียน `point_ledgers` / `point_lots` / `members.point_balance` เอง

```go
// สร้างครั้งเดียวตอน wire (ต้องมี WithMembers เพื่อให้ Check หา member ได้)
pointsUC := usecasepoints.New(
    postgres.NewPointsRepository(db), postgres.NewSettingsRepository(db),
    usecasepoints.WithEarnRules(postgres.NewEarnRuleRepository(db)),
    usecasepoints.WithMembers(postgres.NewMemberRepository(db)),
    usecasepoints.WithTiers(postgres.NewTierRepository(db)),
)

// ใน transaction ของโมดูลเอง (เช่น Claim คูปอง)
err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
    // 1) lock แถวของตัวเอง (คูปอง/รางวัล) แล้วตรวจโควตา เวลา ระดับ
    // 2) เงื่อนไขการแลกกลาง: สถานะ, ยืนยันอีเมล, อายุสมาชิก, ครั้งต่อวัน, แต้มคงเหลือ
    if err := pointsUC.Check(ctx, member, cost, now); err != nil {
        return err // domain.ErrMemberTooNew / ErrDailyLimitReached / ErrEmailNotVerified / ErrMemberNotAllowed / ErrInsufficientPoints
    }
    // 3) หักแต้มด้วย repository ที่ผูกกับ tx นี้ (ตัด lot ที่ใกล้หมดอายุก่อน)
    entry, err := pointsUC.SpendInTx(ctx, postgres.NewPointsRepository(tx), usecasepoints.SpendRequest{
        MemberID: member.ID, Points: cost, SourceType: "coupon", SourceID: memberCouponCode, At: now,
    })
    if err != nil {
        return err
    }
    // 4) บันทึกแถวของตัวเองพร้อม entry.ID (ledger id)
    return nil
})

// ยกเลิกแล้วคืนแต้ม (สร้าง lot ใหม่ อายุนับใหม่)
pointsUC.RefundInTx(ctx, postgres.NewPointsRepository(tx), usecasepoints.RefundRequest{ /* MemberID, Points, SourceType, SourceID, Note */ })
```

- `SpendInTx` ล็อกแถวสมาชิกและอ่านยอดใหม่เอง แลกพร้อมกันจึงได้ `ErrInsufficientPoints` แทนการติดลบ
- `(source_type, source_id, type)` เป็น unique: เรียกซ้ำด้วย source เดิมได้ `domain.ErrConflict` ไม่หักซ้ำ
- error ของ guard ถูก map เป็น HTTP 409 พร้อม code ใน `backoffice-api/internal/delivery/http/handler/errors.go` แล้ว
- แต้มหมดอายุตัดโดย job `points.expire` ทุกวัน 00:10 เวลาไทย (`backoffice-api/cmd/api/wire.go`)
