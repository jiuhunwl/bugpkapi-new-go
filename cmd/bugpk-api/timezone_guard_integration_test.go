package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"bugpk-api/internal/pkg/testdb"
)

func TestMigrationCommandsCannotBypassDatabaseTimeConfirmation(t *testing.T) {
	for _, apply := range []bool{true, false} {
		t.Run(strconv.FormatBool(apply), func(t *testing.T) {
			dsn := os.Getenv("BUGPK_TIMEZONE_DSN")
			if dsn == "" {
				t.Skip("requires explicit disposable MySQL")
			}
			control, parsed := testdb.OpenIsolatedMySQL(t, dsn, os.Getenv("BUGPK_TIMEZONE_DATADIR"), "bugpk_cli_time")
			host, portText, err := net.SplitHostPort(parsed.Addr)
			if err != nil {
				t.Fatal(err)
			}
			port, err := strconv.Atoi(portText)
			if err != nil {
				t.Fatal(err)
			}
			directory := t.TempDir()
			migrations := filepath.Join(directory, "migrations")
			if err := os.Mkdir(migrations, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(migrations, "000001_guard.up.sql"), []byte("create table should_not_be_created(id int primary key);"), 0600); err != nil {
				t.Fatal(err)
			}
			cfg := map[string]any{"app": map[string]any{"env": "test"}, "database": map[string]any{"host": host, "port": port, "name": parsed.DBName, "user": parsed.User, "password": parsed.Passwd, "timezoneDataConfirmation": ""}, "migration": map[string]any{"enabled": true, "path": migrations}}
			configBytes, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(directory, "config.yaml")
			if err := os.WriteFile(path, configBytes, 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("CONFIG_FILE", path)
			t.Setenv("APP_ENV", "test")
			t.Setenv("DB_HOST", host)
			t.Setenv("DB_PORT", portText)
			t.Setenv("DB_NAME", parsed.DBName)
			t.Setenv("DB_USER", parsed.User)
			t.Setenv("DB_PASSWORD", parsed.Passwd)
			t.Setenv("DB_TIMEZONE_DATA_CONFIRMATION", "")
			previous := os.Stderr
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			os.Stderr = writer
			code := runMigrationCommand(apply)
			writer.Close()
			os.Stderr = previous
			output, err := io.ReadAll(reader)
			reader.Close()
			if err != nil {
				t.Fatal(err)
			}
			var changed int
			if err := control.QueryRowContext(context.Background(), "select count(*) from information_schema.tables where table_schema=? and table_name in ('should_not_be_created','schema_migrations')", parsed.DBName).Scan(&changed); err != nil {
				t.Fatal(err)
			}
			if code == 0 || changed != 0 || !strings.Contains(string(output), "DB_TIMEZONE_DATA_CONFIRMATION") {
				t.Fatalf("migration bypassed acknowledgement: code=%d schemaWrites=%d stderr=%s", code, changed, output)
			}
			var partitionOutput bytes.Buffer
			if partitionCode := runCallLogPartitionCommand([]string{"--apply", "--timeout=10s"}, &partitionOutput); partitionCode == 0 || !strings.Contains(partitionOutput.String(), "DB_TIMEZONE_DATA_CONFIRMATION") {
				t.Fatalf("partition write bypassed policy: %d %s", partitionCode, partitionOutput.String())
			}
			// Preview must remain usable without confirmation and must not perform DDL.
			if _, err := control.ExecContext(context.Background(), "create table bp_interface_call_log(id bigint not null auto_increment primary key,created_at datetime not null) engine=InnoDB; insert into bp_interface_call_log(created_at) values(date_add(utc_timestamp(),interval 8 hour)),(date_sub(date_add(utc_timestamp(),interval 8 hour),interval 32 day))"); err != nil {
				t.Fatal(err)
			}
			partitionOutput.Reset()
			if code := runCallLogPartitionCommand([]string{"--timeout=10s"}, &partitionOutput); code != 0 || !strings.Contains(partitionOutput.String(), "Preview only") {
				t.Fatalf("unconfirmed read-only preview failed: %d %s", code, partitionOutput.String())
			}
			var primary string
			if err := control.QueryRowContext(context.Background(), "select group_concat(column_name order by seq_in_index) from information_schema.statistics where table_schema=database() and table_name='bp_interface_call_log' and index_name='PRIMARY'").Scan(&primary); err != nil || primary != "id" {
				t.Fatalf("preview changed primary key: %q %v", primary, err)
			}
			// The runbook's read-only query must produce the exact configured fingerprint.
			var fingerprint string
			if err := control.QueryRowContext(context.Background(), "select concat('mysql-civil-v1:',sha2(concat('bugpk/mysql-civil/v1',char(0),@@server_uuid,char(0),database(),char(0),'+08:00'),256))").Scan(&fingerprint); err != nil {
				t.Fatal(err)
			}
			t.Setenv("DB_TIMEZONE_DATA_CONFIRMATION", fingerprint) // this test target is known empty
			if code := runMigrationCommand(apply); code != 0 {
				t.Fatalf("confirmed maintenance failed: %d", code)
			}
			if err := control.QueryRowContext(context.Background(), "select count(*) from information_schema.tables where table_schema=? and table_name in ('should_not_be_created','schema_migrations')", parsed.DBName).Scan(&changed); err != nil {
				t.Fatal(err)
			}
			want := 1
			if apply {
				want = 2
			}
			if changed != want {
				t.Fatalf("confirmed command did not perform expected work: tables=%d want=%d", changed, want)
			}
			partitionOutput.Reset()
			if code := runCallLogPartitionCommand([]string{"--apply", "--allow-rebuild", "--timeout=10s"}, &partitionOutput); code != 0 {
				t.Fatalf("confirmed partition application failed: %d %s", code, partitionOutput.String())
			}
			if err := control.QueryRowContext(context.Background(), "select group_concat(column_name order by seq_in_index) from information_schema.statistics where table_schema=database() and table_name='bp_interface_call_log' and index_name='PRIMARY'").Scan(&primary); err != nil || primary != "id,created_at" {
				t.Fatalf("partition conversion did not preserve its key contract: %q %v", primary, err)
			}
			var ids string
			if err := control.QueryRowContext(context.Background(), "select group_concat(id order by id) from bp_interface_call_log").Scan(&ids); err != nil || ids != "1,2" {
				t.Fatalf("confirmed partition conversion lost rows: %q %v", ids, err)
			}

		})
	}
}
