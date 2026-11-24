package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const migrationPinDriverName = "bugpk-migration-pin"

var registerMigrationPinDriver sync.Once

// TestApplyConfiguredMigrationPinsConnectionForSessionVariables protects the
// migration runner from database/sql silently moving consecutive statements
// to different pooled connections. MySQL user variables and prepared
// statements are session-scoped, so a migration must execute as one pinned
// connection.
func TestApplyConfiguredMigrationPinsConnectionForSessionVariables(t *testing.T) {
	registerMigrationPinDriver.Do(func() {
		sql.Register(migrationPinDriverName, migrationPinDriver{})
	})

	db, err := sql.Open(migrationPinDriverName, "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(2)

	path := filepath.Join(t.TempDir(), "migration.sql")
	if err := os.WriteFile(path, []byte("SET @migration_pin = 1; ASSERT migration pin;"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := applyConfiguredMigration(context.Background(), db, path); err != nil {
		t.Fatalf("migration should retain session state across statements: %v", err)
	}
}

type migrationPinDriver struct{}

func (migrationPinDriver) Open(string) (driver.Conn, error) {
	return &migrationPinConn{}, nil
}

type migrationPinConn struct {
	mu         sync.Mutex
	sessionPin bool
	valid      bool
}

func (c *migrationPinConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("migration pin test driver does not prepare statements")
}

func (c *migrationPinConn) Close() error {
	c.mu.Lock()
	c.valid = false
	c.mu.Unlock()
	return nil
}

func (c *migrationPinConn) Begin() (driver.Tx, error) {
	return nil, errors.New("migration pin test driver does not support transactions")
}

func (c *migrationPinConn) ExecContext(ctx context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.valid {
		// The first statement marks this connection invalid once database/sql
		// returns it to the pool. A pinned sql.Conn is still allowed to use it.
		c.valid = true
	}
	switch {
	case strings.Contains(query, "SET @migration_pin"):
		c.sessionPin = true
		c.valid = false
		return migrationPinResult{}, nil
	case strings.Contains(query, "ASSERT migration pin") && !c.sessionPin:
		return nil, errors.New("session variable was lost on a new connection")
	default:
		return migrationPinResult{}, nil
	}
}

func (c *migrationPinConn) IsValid() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.valid
}

type migrationPinResult struct{}

func (migrationPinResult) LastInsertId() (int64, error) { return 0, nil }
func (migrationPinResult) RowsAffected() (int64, error) { return 0, nil }
