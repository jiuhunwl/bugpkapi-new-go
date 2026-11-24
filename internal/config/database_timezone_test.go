package config

import (
	mysql "github.com/go-sql-driver/mysql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDatabaseDSNAlignsSessionWithCivilTimeDecoding(t *testing.T) {
	parsed, err := mysql.ParseDSN((DatabaseConfig{Host: "127.0.0.1", Port: 3306, Name: "timezone_test", User: "tester", Password: "not-a-real-secret"}).DSN())
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Params["time_zone"] != "'+08:00'" {
		t.Fatalf("database NOW()/CURDATE() can disagree with decoded DATETIME: session=%q", parsed.Params["time_zone"])
	}
	_, offset := time.Date(2026, 10, 8, 0, 0, 0, 0, parsed.Loc).Zone()
	if offset != 8*60*60 {
		t.Fatalf("session and Go civil-time offset differ: %d", offset)
	}
	// The driver consumes charset into its private charset list on ParseDSN.
	if !strings.Contains(parsed.FormatDSN(), "charset=utf8mb4") {
		t.Fatal("session options lost charset")
	}
}

func TestDatabaseTimeConfirmationLoadsOnlyExplicitConfiguration(t *testing.T) {
	t.Setenv("DB_TIMEZONE_DATA_CONFIRMATION", "")
	c, err := Load("missing-config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if c.Database.TimezoneDataConfirmation != "" {
		t.Fatal("unknown database was automatically confirmed")
	}
	t.Setenv("DB_TIMEZONE_DATA_CONFIRMATION", "mysql-civil-v1:operator-reviewed-target")
	c, err = Load("missing-config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if c.Database.TimezoneDataConfirmation != "mysql-civil-v1:operator-reviewed-target" {
		t.Fatal("explicit target confirmation was lost")
	}
}

func TestDatabaseCivilTimeOffsetIsFixedForHistoricalValues(t *testing.T) {
	parsed, err := mysql.ParseDSN((DatabaseConfig{Host: "127.0.0.1", Port: 3306, Name: "timezone_test", User: "tester"}).DSN())
	if err != nil {
		t.Fatal(err)
	}
	for _, year := range []int{1988, 2026} {
		_, offset := time.Date(year, 7, 1, 12, 0, 0, 0, parsed.Loc).Zone()
		if offset != 8*60*60 {
			t.Fatalf("fixed +08:00 SQL policy decoded year %d with offset %d", year, offset)
		}
	}
}

func TestExplicitEmptyTimeConfirmationRevokesYAMLReceipt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("database:\n  timezoneDataConfirmation: mysql-civil-v1:saved-target\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APP_ENV", "test")
	t.Setenv("DB_TIMEZONE_DATA_CONFIRMATION", "")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.TimezoneDataConfirmation != "" {
		t.Fatalf("empty environment did not revoke stored acknowledgement: %q", cfg.Database.TimezoneDataConfirmation)
	}
	if err := os.Unsetenv("DB_TIMEZONE_DATA_CONFIRMATION"); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.TimezoneDataConfirmation != "mysql-civil-v1:saved-target" {
		t.Fatal("unset environment must preserve an explicit YAML setting")
	}
}
