package app

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"bugpk-api/internal/config"

	_ "github.com/go-sql-driver/mysql"
	"github.com/golang-migrate/migrate/v4"
)

func TestConcurrentRunMigrationsIntegration(t *testing.T) {
	if os.Getenv("MIGRATION_ROLLBACK_INTEGRATION") != "1" {
		t.Skip("set MIGRATION_ROLLBACK_INTEGRATION=1 to run")
	}
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Migration.Path = "../../migrations"
	db, err := sql.Open("mysql", cfg.Database.DSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(4)

	const instances = 2
	start := make(chan struct{})
	errorsCh := make(chan error, instances)
	var ready sync.WaitGroup
	var done sync.WaitGroup
	ready.Add(instances)
	done.Add(instances)
	for range instances {
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			errorsCh <- RunMigrations(db, cfg.Migration.Path)
		}()
	}
	ready.Wait()
	close(start)
	done.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatalf("concurrent migration instance failed: %v", err)
		}
	}
	latest := latestMigrationVersion(t, cfg.Migration.Path)
	assertMigrationState(t, db, latest, false)
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("database unavailable after concurrent migrations: %v", err)
	}
}

func TestAllMigrationsDownUpRoundTripIntegration(t *testing.T) {
	if os.Getenv("MIGRATION_ROLLBACK_INTEGRATION") != "1" {
		t.Skip("set MIGRATION_ROLLBACK_INTEGRATION=1 to run")
	}
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Migration.Path = "../../migrations"
	db, err := sql.Open("mysql", cfg.Database.DSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := RunMigrations(db, cfg.Migration.Path); err != nil {
		t.Fatal(err)
	}
	latest := latestMigrationVersion(t, cfg.Migration.Path)
	assertMigrationState(t, db, latest, false)

	migrator, err := newMigrator(db, cfg.Migration.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrator.Down(); err != nil {
		_, _ = migrator.Close()
		t.Fatal(err)
	}
	if version, dirty, err := migrator.Version(); !errors.Is(err, migrate.ErrNilVersion) {
		_, _ = migrator.Close()
		t.Fatalf("migration state after full down=%d:%t err=%v, want nil version", version, dirty, err)
	}
	closeMigratorForTest(t, migrator)
	assertNoBusinessTables(t, db)
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("application database was closed after full migration down: %v", err)
	}

	if err := RunMigrations(db, cfg.Migration.Path); err != nil {
		t.Fatal(err)
	}
	assertMigrationState(t, db, latest, false)
	assertRiskCoveringIndex(t, db, latest >= 25)
	assertWalletBalanceCapacity(t, db, latest >= 29)
	for _, table := range []string{"bp_user", "bp_menu", "bp_interface_api", "bp_payment_order", "bp_site_config"} {
		assertTableExists(t, db, table)
	}
}

func TestLatestMigrationDownUpRoundTripIntegration(t *testing.T) {
	if os.Getenv("MIGRATION_ROLLBACK_INTEGRATION") != "1" {
		t.Skip("set MIGRATION_ROLLBACK_INTEGRATION=1 to run")
	}
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Migration.Path = "../../migrations"
	db, err := sql.Open("mysql", cfg.Database.DSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := RunMigrations(db, cfg.Migration.Path); err != nil {
		t.Fatal(err)
	}
	latest := latestMigrationVersion(t, cfg.Migration.Path)
	assertMigrationState(t, db, latest, false)
	assertRiskCoveringIndex(t, db, latest >= 25)
	assertWalletBalanceCapacity(t, db, latest >= 29)
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("application database was closed after RunMigrations: %v", err)
	}

	migrator, err := newMigrator(db, cfg.Migration.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrator.Steps(-1); err != nil {
		_, _ = migrator.Close()
		t.Fatal(err)
	}
	closeMigratorForTest(t, migrator)
	previous := latest - 1
	assertMigrationState(t, db, previous, false)
	assertRiskCoveringIndex(t, db, previous >= 25)
	assertWalletBalanceCapacity(t, db, previous >= 29)
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("application database was closed after migration down: %v", err)
	}

	migrator, err = newMigrator(db, cfg.Migration.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrator.Steps(1); err != nil {
		_, _ = migrator.Close()
		t.Fatal(err)
	}
	closeMigratorForTest(t, migrator)
	assertMigrationState(t, db, latest, false)
	assertRiskCoveringIndex(t, db, latest >= 25)
	assertWalletBalanceCapacity(t, db, latest >= 29)
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("application database was closed after migration up: %v", err)
	}
}

func latestMigrationVersion(t *testing.T, path string) uint {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	var latest uint64
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		prefix, _, ok := strings.Cut(name, "_")
		if !ok {
			continue
		}
		version, parseErr := strconv.ParseUint(prefix, 10, 32)
		if parseErr != nil {
			t.Fatalf("invalid migration filename %q: %v", name, parseErr)
		}
		if version > latest {
			latest = version
		}
	}
	if latest == 0 {
		t.Fatalf("no up migrations found in %s", path)
	}
	return uint(latest)
}

func closeMigratorForTest(t *testing.T, migrator interface {
	Close() (error, error)
}) {
	t.Helper()
	sourceErr, databaseErr := migrator.Close()
	if err := errors.Join(sourceErr, databaseErr); err != nil {
		t.Fatalf("close migrator: %v", err)
	}
}

func assertMigrationState(t *testing.T, db *sql.DB, wantVersion uint, wantDirty bool) {
	t.Helper()
	var version uint
	var dirty bool
	if err := db.QueryRow("select version,dirty from schema_migrations").Scan(&version, &dirty); err != nil {
		t.Fatal(err)
	}
	if version != wantVersion || dirty != wantDirty {
		t.Fatalf("migration state=%d:%t, want %d:%t", version, dirty, wantVersion, wantDirty)
	}
}

func assertRiskCoveringIndex(t *testing.T, db *sql.DB, want bool) {
	t.Helper()
	var count int
	if err := db.QueryRow(`
		select count(distinct index_name)
		from information_schema.statistics
		where table_schema=database()
		  and table_name='bp_interface_call_log'
		  and index_name='idx_bp_call_log_risk_window'
	`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if (count == 1) != want {
		t.Fatalf("risk covering index exists=%t, want %t", count == 1, want)
	}
}

func assertWalletBalanceCapacity(t *testing.T, db *sql.DB, want bool) {
	t.Helper()
	for _, column := range []struct {
		table string
		name  string
	}{
		{table: "bp_user", name: "balance"},
		{table: "bp_wallet_transaction", name: "amount"},
		{table: "bp_wallet_transaction", name: "balance_after"},
	} {
		var precision, scale int
		if err := db.QueryRow(`
			select numeric_precision,numeric_scale
			from information_schema.columns
			where table_schema=database() and table_name=? and column_name=?
		`, column.table, column.name).Scan(&precision, &scale); err != nil {
			t.Fatal(err)
		}
		isExpanded := precision == 18 && scale == 4
		if isExpanded != want {
			t.Fatalf("%s.%s decimal capacity=%d,%d expanded=%t, want %t", column.table, column.name, precision, scale, isExpanded, want)
		}
	}
}

func assertNoBusinessTables(t *testing.T, db *sql.DB) {
	t.Helper()
	var count int
	if err := db.QueryRow(`
		select count(*)
		from information_schema.tables
		where table_schema=database()
		  and table_name <> 'schema_migrations'
	`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("business tables remaining after full down: %d", count)
	}
}

func assertTableExists(t *testing.T, db *sql.DB, table string) {
	t.Helper()
	var count int
	if err := db.QueryRow(`
		select count(*)
		from information_schema.tables
		where table_schema=database() and table_name=?
	`, table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("table %s was not restored after full up", table)
	}
}
