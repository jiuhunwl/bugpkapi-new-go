package config_test

import (
	"context"
	"database/sql"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/config"
	"bugpk-api/internal/domain/gateway"
	"bugpk-api/internal/pkg/testdb"
	_ "github.com/go-sql-driver/mysql"
)

func TestApplicationConnectionsOnUTCMySQL(t *testing.T) {
	dsn := os.Getenv("BUGPK_TIMEZONE_DSN")
	if dsn == "" {
		t.Skip("requires explicit UTC disposable MySQL")
	}
	control, fixture := testdb.OpenIsolatedMySQL(t, dsn, os.Getenv("BUGPK_TIMEZONE_DATADIR"), "bugpk_timezone")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var global string
	if err := control.QueryRowContext(ctx, "select @@global.time_zone").Scan(&global); err != nil {
		t.Fatal(err)
	}
	if global != "+00:00" {
		t.Fatalf("test requires UTC server default, got %q", global)
	}
	paths, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	var schema strings.Builder
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		schema.Write(b)
		schema.WriteByte('\n')
	}
	if _, err := control.ExecContext(ctx, schema.String()); err != nil {
		t.Fatal(err)
	}
	host, portText, err := net.SplitHostPort(fixture.Addr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	appConfig := config.DatabaseConfig{Host: host, Port: port, Name: fixture.DBName, User: fixture.User, Password: fixture.Passwd, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second}
	db, err := sql.Open("mysql", appConfig.DSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(2)
	t.Run("new and recycled connections align session and decoder", func(t *testing.T) {
		var prior int64
		for i := 0; i < 3; i++ {
			conn, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var session, day string
			var id int64
			var instant time.Time
			before := time.Now().Add(-2 * time.Second)
			err = conn.QueryRowContext(ctx, "select connection_id(),@@session.time_zone,now(6),cast(date(convert_tz('2026-10-08 17:30:00','+00:00',@@session.time_zone)) as char)").Scan(&id, &session, &instant, &day)
			conn.Close()
			if err != nil {
				t.Fatal(err)
			}
			if session != "+08:00" || day != "2026-10-09" {
				t.Errorf("server civil time not aligned: session=%q businessDay=%q", session, day)
			}
			if instant.Before(before) || instant.After(time.Now().Add(2*time.Second)) {
				t.Errorf("NOW decoded as a different instant: age=%s", time.Since(instant))
			}
			if i > 0 && id == prior {
				t.Error("connection recreation was not exercised")
			}
			prior = id
			db.SetMaxIdleConns(0)
		}
	})
	t.Run("default and supplied dates round trip as the same instant", func(t *testing.T) {
		if _, err := db.ExecContext(ctx, "create table timezone_probe(id bigint primary key auto_increment, civil_at datetime(6) not null default current_timestamp(6), instant_at timestamp(6) not null default current_timestamp(6), supplied_at datetime(6) not null)"); err != nil {
			t.Fatal(err)
		}
		before := time.Now().UTC()
		result, err := db.ExecContext(ctx, "insert into timezone_probe(supplied_at) values(?)", before)
		if err != nil {
			t.Fatal(err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		var civil, instant, supplied time.Time
		if err := db.QueryRowContext(ctx, "select civil_at,instant_at,supplied_at from timezone_probe where id=?", id).Scan(&civil, &instant, &supplied); err != nil {
			t.Fatal(err)
		}
		for name, value := range map[string]time.Time{"default DATETIME": civil, "default TIMESTAMP": instant, "provided DATETIME": supplied} {
			if value.Before(before.Add(-2*time.Second)) || value.After(time.Now().Add(2*time.Second)) {
				t.Errorf("%s shifted across the connection: %s age=%s", name, value, time.Since(value))
			}
		}
	})
	t.Run("fresh reservation is not a stale refund candidate", func(t *testing.T) {
		if _, err := db.ExecContext(ctx, "insert into bp_user(id,username,password_hash,display_name,balance,primary_role_id) values(995010,'timezone-user','test-only','Timezone',10.0000,2)"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, "insert into bp_interface_api(id,api_code,name,request_url,request_method,price,status,remark) values(995010,'timezone-api','Timezone','https://example.invalid','GET',1.2500,1,'')"); err != nil {
			t.Fatal(err)
		}
		repo := gateway.NewRepository(db)
		res, err := repo.ReserveBilling(ctx, gateway.InterfaceConfig{ID: 995010, APICode: "timezone-api", Price: "1.2500", RequestMethod: "GET"}, 0, 995010)
		if err != nil {
			t.Fatal(err)
		}
		count, err := repo.ReconcileStaleBilling(ctx, time.Now().Add(-5*time.Minute), 100)
		if err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("newly created debit prematurely refunded: %d", count)
		}
		if _, err := db.ExecContext(ctx, "update bp_billing_record set created_at=date_sub(now(),interval 10 minute) where id=?", res.BillingRecordID); err != nil {
			t.Fatal(err)
		}
		for _, want := range []int{1, 0} {
			count, err = repo.ReconcileStaleBilling(ctx, time.Now().Add(-5*time.Minute), 100)
			if err != nil || count != want {
				t.Fatalf("stale reconciliation released=%d err=%v want=%d", count, err, want)
			}
		}
		var balance string
		if err := db.QueryRowContext(ctx, "select cast(balance as char) from bp_user where id=995010").Scan(&balance); err != nil {
			t.Fatal(err)
		}
		if balance != "10.0000" {
			t.Fatalf("refund altered original balance: %s", balance)
		}
	})
	t.Run("fresh checkpoint is not eight hours old", func(t *testing.T) {
		if _, err := db.ExecContext(ctx, "insert into bp_analytics_checkpoint(worker_key,last_completed_at) values('timezone_probe',now(6))"); err != nil {
			t.Fatal(err)
		}
		var completed time.Time
		if err := db.QueryRowContext(ctx, "select last_completed_at from bp_analytics_checkpoint where worker_key='timezone_probe'").Scan(&completed); err != nil {
			t.Fatal(err)
		}
		age := time.Since(completed)
		if age < -2*time.Second || age > 2*time.Second {
			t.Fatalf("fresh analytics checkpoint misclassified as stale: %s", age)
		}
	})
	if err := control.QueryRowContext(ctx, "select @@global.time_zone").Scan(&global); err != nil || global != "+00:00" {
		t.Fatalf("application changed shared server time zone: %q %v", global, err)
	}
}
