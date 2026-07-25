package repository

import (
	"context"
	"fmt"
	"time"

	"paperMaster/internal/config"
	"paperMaster/internal/logger"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// NewDatabase selects and configures the application's GORM database from
// DB_DRIVER. Repositories receive the returned *gorm.DB through DI and own no
// connection setup logic.
func NewDatabase(cfg config.Config) (*gorm.DB, error) {
	switch cfg.DatabaseDriver {
	case "postgres", "postgresql":
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		db, err := gorm.Open(postgres.Open(cfg.DatabaseURL), &gorm.Config{
			NowFunc: func() time.Time { return time.Now().UTC() },
		})
		if err != nil {
			logger.Error(ctx, "open postgres database failed", "error", err)
			return nil, fmt.Errorf("open postgres: %w", err)
		}
		sqlDB, err := db.DB()
		if err != nil {
			logger.Error(ctx, "get postgres connection pool failed", "error", err)
			return nil, fmt.Errorf("get postgres connection pool: %w", err)
		}
		if err := sqlDB.PingContext(ctx); err != nil {
			_ = sqlDB.Close()
			logger.Error(ctx, "connect postgres database failed", "error", err)
			return nil, fmt.Errorf("connect postgres: %w", err)
		}
		sqlDB.SetMaxOpenConns(20)
		sqlDB.SetMaxIdleConns(10)
		sqlDB.SetConnMaxLifetime(10 * time.Minute)
		if cfg.AutoMigrate {
			if err := Migrate(ctx, db); err != nil {
				_ = sqlDB.Close()
				logger.Error(ctx, "apply document database migrations failed", "error", err)
				return nil, fmt.Errorf("migrate document tables: %w", err)
			}
		}
		return db, nil
	default:
		err := fmt.Errorf("unsupported DB_DRIVER %q", cfg.DatabaseDriver)
		logger.Error(context.Background(), "select database driver failed", "error", err)
		return nil, err
	}
}

// CloseDatabase releases the SQL connection pool owned by GORM.
func CloseDatabase(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	sqlDB, err := db.DB()
	if err != nil {
		logger.Error(context.Background(), "get database pool for close failed", "error", err)
		return err
	}
	if err := sqlDB.Close(); err != nil {
		logger.Error(context.Background(), "close database pool failed", "error", err)
		return err
	}
	return nil
}
