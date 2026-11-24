package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratedatabase "github.com/golang-migrate/migrate/v4/database"
	"github.com/golang-migrate/migrate/v4/database/mysql"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

const (
	migrationLockTimeout       = 60 * time.Second
	migrationLockRetryInterval = 250 * time.Millisecond
)

type MigrationState struct {
	Version uint
	Dirty   bool
}

func RunMigrations(db *sql.DB, path string) error {
	deadline := time.Now().Add(migrationLockTimeout)
	for {
		migrator, err := newMigrator(db, path)
		if err != nil {
			return err
		}
		migrationErr := migrator.Up()
		sourceCloseErr, databaseCloseErr := migrator.Close()
		closeErr := errors.Join(sourceCloseErr, databaseCloseErr)

		if migrationErr == nil || errors.Is(migrationErr, migrate.ErrNoChange) {
			if closeErr != nil {
				return fmt.Errorf("close migration resources: %w", closeErr)
			}
			return nil
		}

		if isMigrationLockError(migrationErr) {
			if closeErr != nil {
				return fmt.Errorf("close migration resources: %w", closeErr)
			}
			if time.Now().Before(deadline) {
				remaining := time.Until(deadline)
				if remaining > migrationLockRetryInterval {
					remaining = migrationLockRetryInterval
				}
				time.Sleep(remaining)
				continue
			}
			return fmt.Errorf("apply migrations: database migration lock unavailable after %s: %w", migrationLockTimeout, migrationErr)
		}

		return fmt.Errorf("apply migrations: %w", migrationErr)
	}
}

func MigrationVersion(db *sql.DB, path string) (MigrationState, error) {
	migrator, err := newMigrator(db, path)
	if err != nil {
		return MigrationState{}, err
	}
	version, dirty, versionErr := migrator.Version()
	sourceCloseErr, databaseCloseErr := migrator.Close()
	if versionErr != nil {
		return MigrationState{}, errors.Join(versionErr, sourceCloseErr, databaseCloseErr)
	}
	if closeErr := errors.Join(sourceCloseErr, databaseCloseErr); closeErr != nil {
		return MigrationState{}, fmt.Errorf("close migration resources: %w", closeErr)
	}
	return MigrationState{Version: version, Dirty: dirty}, nil
}

func isMigrationLockError(err error) bool {
	return errors.Is(err, migrate.ErrLockTimeout) || errors.Is(err, migratedatabase.ErrLocked)
}

func newMigrator(db *sql.DB, path string) (*migrate.Migrate, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve migration path: %w", err)
	}

	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire migration database connection: %w", err)
	}
	driver, err := mysql.WithConnection(ctx, conn, &mysql.Config{})
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("create migration mysql driver: %w", err)
	}

	sourceURL := "file://" + filepath.ToSlash(absPath)
	migrator, err := migrate.NewWithDatabaseInstance(sourceURL, "mysql", driver)
	if err != nil {
		_ = driver.Close()
		return nil, fmt.Errorf("create migrator: %w", err)
	}
	migrator.LockTimeout = migrationLockTimeout
	return migrator, nil
}
