// Package migrations creates and updates the CRM schema. Only backoffice-api
// runs migrations (it connects with the crm_backoffice DB role that owns the
// schema); api-application connects with crm_member and never migrates.
package migrations

import (
	"errors"
	"fmt"
	"regexp"

	domainloyalty "crm-core/domain/loyalty"

	"gorm.io/gorm"
)

var schemaNamePattern = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// Models lists every CRM table in dependency order.
//
// New modules add their tables in their own models_<module>.go file and one
// append line here; nothing else in this package changes.
func Models() []any {
	var all []any
	all = append(all, memberModels()...)
	all = append(all, pointsModels()...)
	all = append(all, settingsModels()...)
	all = append(all, staffRoleModels()...)
	all = append(all, uploadModels()...)
	all = append(all, promotionModels()...)
	all = append(all, couponModels()...)
	all = append(all, earnRuleModels()...)
	all = append(all, rewardModels()...)
	return all
}

// Run creates the schema (Postgres only), migrates all tables and seeds the
// default entry tier.
func Run(db *gorm.DB, schemaName string) error {
	if db.Dialector.Name() == "postgres" && schemaName != "" {
		if !schemaNamePattern.MatchString(schemaName) {
			return fmt.Errorf("invalid schema name %q", schemaName)
		}
		if err := db.Exec("CREATE SCHEMA IF NOT EXISTS " + schemaName).Error; err != nil {
			return fmt.Errorf("create schema: %w", err)
		}
	}
	if err := db.AutoMigrate(Models()...); err != nil {
		return fmt.Errorf("auto migrate: %w", err)
	}
	return seedDefaultTier(db)
}

// seedDefaultTier makes sure the entry tier exists. Other tiers and their
// spend thresholds are configured by the owner in the backoffice.
func seedDefaultTier(db *gorm.DB) error {
	var tier domainloyalty.Tier
	err := db.Where("code = ?", domainloyalty.DefaultTierCode).First(&tier).Error
	if err == nil {
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("check default tier: %w", err)
	}
	tier = domainloyalty.Tier{
		Code:           domainloyalty.DefaultTierCode,
		Name:           "Bronze",
		MinSpend:       0,
		EarnMultiplier: 1,
		SortOrder:      0,
		CardTheme:      "bronze",
		IsActive:       true,
	}
	if err := db.Create(&tier).Error; err != nil {
		return fmt.Errorf("seed default tier: %w", err)
	}
	return nil
}
