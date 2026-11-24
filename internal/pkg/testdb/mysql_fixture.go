package testdb

import (
	"context"
	"database/sql"
	"fmt"
	mysql "github.com/go-sql-driver/mysql"
	"net"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

var mysqlFixtureSequence atomic.Uint64
var fixturePrefix = regexp.MustCompile("^[a-z0-9_]+$")

// OpenIsolatedMySQL requires an explicit loopback DSN and exact disposable
// datadir. It never reads application configuration or drops caller databases.
func OpenIsolatedMySQL(t stubTest, dsn, expectedDir, prefix string) (*sql.DB, *mysql.Config) {
	t.Helper()
	if err := ValidateIsolatedMySQLDSN(dsn); err != nil {
		t.Fatalf("unsafe MySQL fixture: %v", err)
		return nil, nil
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("parse test DSN: %v", err)
		return nil, nil
	}
	host, _, err := net.SplitHostPort(cfg.Addr)
	if err != nil || cfg.Net != "tcp" || host != "127.0.0.1" || expectedDir == "" || !fixturePrefix.MatchString(prefix) {
		t.Fatalf("MySQL fixture requires loopback, a datadir and a safe prefix")
		return nil, nil
	}
	admin, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open test MySQL: %v", err)
		return nil, nil
	}
	t.Cleanup(func() { admin.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var actual string
	if err := admin.QueryRowContext(ctx, "select @@datadir").Scan(&actual); err != nil {
		t.Fatalf("verify test datadir: %v", err)
		return nil, nil
	}
	norm := func(s string) string { return strings.TrimRight(strings.ReplaceAll(filepath.Clean(s), "\\", "/"), "/") }
	if !strings.EqualFold(norm(actual), norm(expectedDir)) {
		t.Fatalf("unexpected database datadir: %s", actual)
		return nil, nil
	}
	name := fmt.Sprintf("%s_%d_%d_test", prefix, time.Now().UnixNano(), mysqlFixtureSequence.Add(1))
	if _, err := admin.ExecContext(ctx, "create database "+name+" character set utf8mb4 collate utf8mb4_unicode_ci"); err != nil {
		t.Fatalf("create isolated database: %v", err)
		return nil, nil
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, "drop database "+name); err != nil {
			t.Fatalf("drop isolated database: %v", err)
		}
	})
	cfg.DBName = name
	cfg.MultiStatements = true
	cfg.ParseTime = true
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatalf("open isolated database: %v", err)
		return nil, nil
	}
	t.Cleanup(func() { db.Close() })
	return db, cfg
}
