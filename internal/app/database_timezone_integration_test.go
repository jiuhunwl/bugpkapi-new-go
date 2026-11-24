package app

import (
	"context"
	"database/sql"
	"errors"
	mysql "github.com/go-sql-driver/mysql"
	"go.uber.org/zap"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/config"
	"bugpk-api/internal/pkg/testdb"
)

func TestLegacyUTCDatabaseRequiresCivilTimeConfirmation(t *testing.T) {
	dsn := os.Getenv("BUGPK_TIMEZONE_DSN")
	if dsn == "" {
		t.Skip("requires explicit UTC disposable MySQL")
	}
	control, parsed := testdb.OpenIsolatedMySQL(t, dsn, os.Getenv("BUGPK_TIMEZONE_DATADIR"), "bugpk_timeguard")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var offset int
	if err := control.QueryRowContext(ctx, "select timestampdiff(second,utc_timestamp(),now())").Scan(&offset); err != nil || offset != 0 {
		t.Fatalf("fixture is not UTC: %d %v", offset, err)
	}
	if _, err := control.ExecContext(ctx, "create table legacy_entitlement(id int primary key, expires_at datetime not null); insert into legacy_entitlement values(1,date_add(now(),interval 1 hour))"); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := control.QueryRowContext(ctx, "select cast(expires_at as char) from legacy_entitlement where id=1").Scan(&before); err != nil {
		t.Fatal(err)
	}
	host, portText, err := net.SplitHostPort(parsed.Addr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.DatabaseConfig{Host: host, Port: port, Name: parsed.DBName, User: parsed.User, Password: parsed.Passwd, MaxOpenConns: 2, MaxIdleConns: 1}
	// Prove the guard runs before migration/bootstrap/worker construction.
	appCfg := config.Config{Database: cfg}
	appCfg.Migration.Enabled = true
	appCfg.Migration.Path = filepath.Join(t.TempDir(), "must-not-be-read")
	application, err := New(ctx, appCfg, zap.NewNop())
	var db *sql.DB
	if application != nil {
		db = application.DB
	}

	if err == nil && db != nil {
		var active int
		queryErr := db.QueryRowContext(ctx, "select expires_at>now() from legacy_entitlement where id=1").Scan(&active)
		db.Close()
		t.Fatalf("unconfirmed legacy UTC dates were exposed under a new clock: active=%d queryErr=%v", active, queryErr)
	}
	if !errors.Is(err, errDatabaseTimezoneDataUnconfirmed) || !strings.Contains(err.Error(), "DB_TIMEZONE_DATA_CONFIRMATION") {
		t.Fatalf("startup did not explain required data confirmation: %v", err)
	}
	var after string
	var active int
	if err := control.QueryRowContext(ctx, "select cast(expires_at as char),expires_at>now() from legacy_entitlement where id=1").Scan(&after, &active); err != nil {
		t.Fatal(err)
	}
	if before != after || active != 1 {
		t.Fatalf("guard mutated or invalidated legacy entitlement: %s -> %s active=%d", before, after, active)
	}
}

func TestConfirmedMixedOriginDatesOpenOnlyAfterTargetedRepair(t *testing.T) {
	dsn := os.Getenv("BUGPK_TIMEZONE_DSN")
	if dsn == "" {
		t.Skip("requires explicit UTC disposable MySQL")
	}
	control, parsed := testdb.OpenIsolatedMySQL(t, dsn, os.Getenv("BUGPK_TIMEZONE_DATADIR"), "bugpk_timerepair")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := control.ExecContext(ctx, "create table mixed_times(id int primary key, sql_until datetime(6) not null, parameter_until datetime(6) not null)"); err != nil {
		t.Fatal(err)
	}
	host, portText, err := net.SplitHostPort(parsed.Addr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.DatabaseConfig{Host: host, Port: port, Name: parsed.DBName, User: parsed.User, Password: parsed.Passwd, MaxOpenConns: 2, MaxIdleConns: 1}
	legacy, err := mysql.ParseDSN(cfg.DSN())
	if err != nil {
		t.Fatal(err)
	}
	delete(legacy.Params, "time_zone") // the exact previous connection policy
	oldDB, err := sql.Open("mysql", legacy.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer oldDB.Close()
	until := time.Now().Add(time.Hour)
	if _, err := oldDB.ExecContext(ctx, "insert into mixed_times values(1,date_add(now(6),interval 1 hour),?)", until); err != nil {
		t.Fatal(err)
	}
	var beforeParameter string
	var difference int64
	if err := control.QueryRowContext(ctx, "select cast(parameter_until as char),timestampdiff(second,sql_until,parameter_until) from mixed_times where id=1").Scan(&beforeParameter, &difference); err != nil {
		t.Fatal(err)
	}
	if difference < 28798 || difference > 28802 {
		t.Fatalf("fixture did not reproduce mixed SQL/driver origins: difference=%d", difference)
	}
	if db, err := openDatabase(ctx, cfg); err == nil {
		db.Close()
		t.Fatal("unknown mixed-origin target accepted")
	}
	// This repair is test-only and justified by the fixture's known origin.
	// Never generate this shift for unknown production values or whole tables.
	if _, err := control.ExecContext(ctx, "update mixed_times set sql_until=date_add(sql_until,interval 8 hour) where id=1"); err != nil {
		t.Fatal(err)
	}
	var uuid, name string
	if err := control.QueryRowContext(ctx, "select @@server_uuid,database()").Scan(&uuid, &name); err != nil {
		t.Fatal(err)
	}
	cfg.TimezoneDataConfirmation = databaseTimeConfirmation(uuid, name)
	db, err := openDatabase(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var sqlUntil, parameterUntil time.Time
	var active int
	if err := db.QueryRowContext(ctx, "select sql_until,parameter_until,sql_until>now() from mixed_times where id=1").Scan(&sqlUntil, &parameterUntil, &active); err != nil {
		t.Fatal(err)
	}
	if active != 1 || sqlUntil.Sub(until) > 2*time.Second || until.Sub(sqlUntil) > 2*time.Second || parameterUntil.Sub(until) > time.Second || until.Sub(parameterUntil) > time.Second {
		t.Fatalf("confirmed repaired dates changed entitlement lifetime: sql=%s param=%s want=%s active=%d", sqlUntil, parameterUntil, until, active)
	}
	var afterParameter, global string
	if err := control.QueryRowContext(ctx, "select cast(parameter_until as char),@@global.time_zone from mixed_times where id=1").Scan(&afterParameter, &global); err != nil {
		t.Fatal(err)
	}
	if afterParameter != beforeParameter || global != "+00:00" {
		t.Fatal("repair/guard altered the already-correct date or shared server zone")
	}
	cfg.TimezoneDataConfirmation = databaseTimeConfirmation(uuid, name+"_other")
	if other, err := openDatabase(ctx, cfg); err == nil {
		other.Close()
		t.Fatal("receipt for a different target accepted")
	}
}

// The SQL below deliberately replays the legacy daily-reset algorithm. It is
// evidence for the cutover precondition, not an assertion that mixed writers
// are supported or that this process can stop an already-running old binary.
func TestMixedLegacyClocksRequireCoordinatedCutover(t *testing.T) {
	dsn := os.Getenv("BUGPK_TIMEZONE_DSN")
	if dsn == "" {
		t.Skip("requires explicit disposable MySQL")
	}
	control, parsed := testdb.OpenIsolatedMySQL(t, dsn, os.Getenv("BUGPK_TIMEZONE_DATADIR"), "bugpk_mixedclock")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := control.ExecContext(ctx, "create table legacy_daily_quota(id int primary key, day_key date, day_used int not null)"); err != nil {
		t.Fatal(err)
	}
	oldConn, err := control.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer oldConn.Close()
	newConn, err := control.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer newConn.Close()
	fixed := time.Date(2026, 12, 31, 17, 30, 0, 0, time.UTC).Unix()
	for i, conn := range []*sql.Conn{oldConn, newConn} {
		zone := "+00:00"
		if i == 1 {
			zone = "+08:00"
		}
		if _, err := conn.ExecContext(ctx, "set session time_zone=?", zone); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.ExecContext(ctx, "set timestamp=?", fixed); err != nil {
			t.Fatal(err)
		}
	}
	var oldDay, newDay string
	if err := oldConn.QueryRowContext(ctx, "select cast(curdate() as char)").Scan(&oldDay); err != nil {
		t.Fatal(err)
	}
	if err := newConn.QueryRowContext(ctx, "select cast(curdate() as char)").Scan(&newDay); err != nil {
		t.Fatal(err)
	}
	if oldDay != "2026-12-31" || newDay != "2027-01-01" {
		t.Fatalf("fixed-clock setup failed: %s / %s", oldDay, newDay)
	}
	if _, err := oldConn.ExecContext(ctx, "insert into legacy_daily_quota values(1,curdate(),1)"); err != nil {
		t.Fatal(err)
	}
	var granted int64
	for _, conn := range []*sql.Conn{newConn, oldConn, newConn, oldConn} {
		if _, err := conn.ExecContext(ctx, "update legacy_daily_quota set day_key=curdate(),day_used=0 where id=1 and (day_key is null or day_key<>curdate())"); err != nil {
			t.Fatal(err)
		}
		result, err := conn.ExecContext(ctx, "update legacy_daily_quota set day_used=day_used+1 where id=1 and day_used<1")
		if err != nil {
			t.Fatal(err)
		}
		n, err := result.RowsAffected()
		if err != nil {
			t.Fatal(err)
		}
		granted += n
	}
	if granted != 4 {
		t.Fatalf("legacy mixed-clock hazard was not reproduced: grants=%d", granted)
	}
	host, portText, err := net.SplitHostPort(parsed.Addr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.DatabaseConfig{Host: host, Port: port, Name: parsed.DBName, User: parsed.User, Password: parsed.Passwd, MaxOpenConns: 2, MaxIdleConns: 1}
	db, guardErr := openDatabase(ctx, cfg)
	if db != nil {
		db.Close()
	}
	if !errors.Is(guardErr, errDatabaseTimezoneDataUnconfirmed) {
		t.Fatalf("expected explicit unconfirmed-target rejection, got %v", guardErr)
	}
	t.Logf("same instant: legacy day=%s new day=%s; alternating legacy resets granted %d extra uses; unconfirmed application startup refused", oldDay, newDay, granted)
}
