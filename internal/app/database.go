package app

import (
	"context"
	"database/sql"
	"fmt"

	"bugpk-api/internal/config"

	_ "github.com/go-sql-driver/mysql"
)

func openDatabase(ctx context.Context, cfg config.DatabaseConfig) (*sql.DB, error) {
	db, err := sql.Open("mysql", cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}

	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	db.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping mysql %s:%d/%s as %s: %w", cfg.Host, cfg.Port, cfg.Name, cfg.User, err)
	}

	if err := validateDatabaseTimePolicy(ctx, db, cfg.TimezoneDataConfirmation); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("mysql time policy: %w", err)
	}
	return db, nil
}

// OpenDatabase applies the same target-bound time policy to explicit maintenance
// commands as to server startup. Call it before any schema or business writes.
func OpenDatabase(ctx context.Context, cfg config.DatabaseConfig) (*sql.DB, error) {
	return openDatabase(ctx, cfg)
}
