package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"bugpk-api/internal/pkg/businessclock"
)

var errDatabaseTimezoneDataUnconfirmed = errors.New("database civil-time baseline is not confirmed")

// This is a target/policy-bound operator acknowledgement, not a password,
// cryptographic audit proof, or an automatic conversion of historical data.
func databaseTimeConfirmation(serverUUID, databaseName string) string {
	digest := sha256.Sum256([]byte("bugpk/mysql-civil/v1\x00" + serverUUID + "\x00" + databaseName + "\x00" + businessclock.UTCOffset))
	return "mysql-civil-v1:" + hex.EncodeToString(digest[:])
}

// Validate before migrations, initial data writes, routes, and workers. A
// currently matching server clock cannot establish the origin of DATETIME
// values, so both existing and freshly provisioned targets require explicit
// acknowledgement. Never auto-fill this setting for an unreviewed database.
func validateDatabaseTimePolicy(parent context.Context, db *sql.DB, confirmation string) error {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	var serverUUID, databaseName, sessionZone string
	if err := db.QueryRowContext(ctx, "select @@server_uuid,database(),@@session.time_zone").Scan(&serverUUID, &databaseName, &sessionZone); err != nil {
		return err
	}
	if serverUUID == "" || databaseName == "" {
		return errors.New("database identity is unavailable for civil-time confirmation")
	}
	if sessionZone != businessclock.UTCOffset {
		return fmt.Errorf("MySQL session time_zone is %q, required %s", sessionZone, businessclock.UTCOffset)
	}
	expected := databaseTimeConfirmation(serverUUID, databaseName)
	if strings.TrimSpace(confirmation) != expected {
		return fmt.Errorf("%w: after auditing UTC+08:00 stored dates and stopping all legacy writers, set DB_TIMEZONE_DATA_CONFIRMATION=%s; this does not migrate any existing data", errDatabaseTimezoneDataUnconfirmed, expected)
	}
	return nil
}
