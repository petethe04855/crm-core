// Package database provides the shared GORM configuration for CRM services.
// Every CRM table lives in its own Postgres schema (default "crm"); the schema
// prefix is applied through the naming strategy so entities stay portable
// (tests run on SQLite without a schema).
package database

import (
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

// DefaultSchema is the Postgres schema that holds all CRM tables.
const DefaultSchema = "crm"

// GormConfig returns a GORM config whose tables are prefixed with "<schema>.".
// Pass an empty schema to use unprefixed table names (e.g. SQLite tests).
func GormConfig(schemaName string, logLevel logger.LogLevel) *gorm.Config {
	prefix := ""
	if s := strings.TrimSpace(schemaName); s != "" {
		prefix = s + "."
	}
	return &gorm.Config{
		NamingStrategy: schema.NamingStrategy{TablePrefix: prefix},
		Logger:         logger.Default.LogMode(logLevel),
	}
}
