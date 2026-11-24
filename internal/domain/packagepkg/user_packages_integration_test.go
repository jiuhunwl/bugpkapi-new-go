package packagepkg

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"bugpk-api/internal/pkg/testdb"

	_ "github.com/go-sql-driver/mysql"
)

func TestUserPackagesReturnsScopeUniqueStableIDs(t *testing.T) {
	dsn := os.Getenv("BUGPK_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("BUGPK_MYSQL_TEST_DSN is not configured")
	}
	if err := testdb.ValidateIsolatedMySQLDSN(dsn); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	seed := time.Now().UnixNano()
	userID := seed
	var recordID int64
	if err := db.QueryRowContext(ctx, `
		select greatest(
			coalesce((select max(id) from bp_user_package_global),0),
			coalesce((select max(id) from bp_user_package_interface),0)
		) + 1
	`).Scan(&recordID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec("delete from bp_user_package_global where id=?", recordID)
		_, _ = db.Exec("delete from bp_user_package_interface where id=?", recordID)
	})

	if _, err := db.ExecContext(ctx, `
		insert into bp_user_package_global(id,user_id,package_id,package_name,daily_limit,qps_limit,start_at,end_at,status)
		values(?,?,1,?,100,5,now(),date_add(now(),interval 10 day),1)
	`, recordID, userID, fmt.Sprintf("global-%d", seed)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		insert into bp_user_package_interface(id,user_id,interface_id,package_id,spec_id,package_name,spec_name,daily_limit,qps_limit,start_at,end_at,status)
		values(?,?,1,1,1,?,?,50,3,now(),date_add(now(),interval 5 day),1)
	`, recordID, userID, fmt.Sprintf("interface-%d", seed), "spec"); err != nil {
		t.Fatal(err)
	}

	items, err := NewRepository(db).UserPackages(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("user packages = %d, want 2", len(items))
	}
	if items[0].Scope != "INTERFACE" || items[0].ID != fmt.Sprintf("INTERFACE:%d", recordID) {
		t.Fatalf("first package = %+v", items[0])
	}
	if items[1].Scope != "GLOBAL" || items[1].ID != fmt.Sprintf("GLOBAL:%d", recordID) {
		t.Fatalf("second package = %+v", items[1])
	}
	if items[0].ID == items[1].ID {
		t.Fatal("cross-scope package IDs collided")
	}
}
