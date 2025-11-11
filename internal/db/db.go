// Package db provides database connection and migration functionality.
package db

import (
	"fmt"
	stdlog "log"
	"os"

	"consensus-monitoring/internal/config"
	"consensus-monitoring/internal/models"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Open opens a database connection using the provided configuration.
func Open(cfg config.Config) (*gorm.DB, error) {
	// Configure GORM logger (Silent to avoid cluttering output; only errors will be logged)
	newLogger := logger.New(
		stdlog.New(os.Stdout, "", stdlog.LstdFlags),
		logger.Config{
			SlowThreshold:             0,
			LogLevel:                  logger.Silent,
			IgnoreRecordNotFoundError: true,
			Colorful:                  false,
		},
	)

	if cfg.DBDialect == "" || cfg.DBDsn == "" {
		return nil, nil
	}

	switch cfg.DBDialect {
	case "postgres":
		return gorm.Open(postgres.Open(cfg.DBDsn), &gorm.Config{Logger: newLogger})
	default:
		return nil, fmt.Errorf("unsupported DB_DIALECT: %s", cfg.DBDialect)
	}
}

// AutoMigrate runs database migrations for all models.
func AutoMigrate(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	return db.AutoMigrate(
		&models.Block{},
		&models.RoundProposer{},
		&models.RoundVote{},
	)
}
