package testdb

import (
	"fmt"
	"strings"

	"github.com/go-sql-driver/mysql"
)

func ValidateIsolatedMySQLDSN(dsn string) error {
	cfg, err := mysql.ParseDSN(strings.TrimSpace(dsn))
	if err != nil {
		return fmt.Errorf("parse integration MySQL DSN: %w", err)
	}
	databaseName := strings.ToLower(strings.TrimSpace(cfg.DBName))
	if databaseName == "" || (!strings.Contains(databaseName, "test") &&
		!strings.Contains(databaseName, "e2e") &&
		!strings.Contains(databaseName, "bench")) {
		return fmt.Errorf("integration MySQL DSN must target a dedicated test/e2e/bench database, got %q", cfg.DBName)
	}
	return nil
}
