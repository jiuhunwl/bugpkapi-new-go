package interfaceapi

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"bugpk-api/internal/domain/analytics"
	"bugpk-api/internal/pkg/testdb"

	_ "github.com/go-sql-driver/mysql"
)

func TestInterfaceListCountsOnlyRecentOpenTrafficForCurrentItems(t *testing.T) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	userID := time.Now().UnixNano()
	apiCode := fmt.Sprintf("count-%d", userID)
	if _, err := db.ExecContext(ctx, "insert into bp_user(id,username,password_hash,display_name,home_path,status) values(?,?,'test-only',?,'/console/workspace',1)", userID, apiCode, apiCode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec("delete from bp_user where id=?", userID) })
	result, err := db.ExecContext(ctx, `
		insert into bp_interface_api(api_code,name,request_url,request_method,price,point_price,is_top,is_featured,status,remark)
		values(?,?,'https://example.com/count','GET',0,0,0,0,1,'call count integration test')
	`, apiCode, apiCode)
	if err != nil {
		t.Fatal(err)
	}
	interfaceID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec("delete from bp_interface_call_log where interface_id=?", interfaceID)
		_, _ = db.Exec("delete from bp_interface_api where id=?", interfaceID)
	})

	if _, err := db.ExecContext(ctx, `
		insert into bp_interface_call_log(interface_id,user_id,request_method,response_status,success,charge_type,created_at)
		values
			(?,?,'GET',200,1,'FREE',now()),
			(?,?,'GET',200,1,'ADMIN',now()),
			(?,?,'GET',200,1,'FREE',date_sub(now(), interval 31 day))
	`, interfaceID, userID, interfaceID, userID, interfaceID, userID); err != nil {
		t.Fatal(err)
	}

	if _, err := analytics.NewRepository(db).RunUntilCaughtUp(ctx, 1000, 100); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(db)
	page, err := repo.List(ctx, ListParams{Keyword: apiCode, Page: 1, PageSize: 20}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != fmt.Sprintf("%d", interfaceID) {
		t.Fatalf("unexpected list result: %+v", page.Items)
	}
	if page.Items[0].CallCount != 1 {
		t.Fatalf("list call count = %d, want 1", page.Items[0].CallCount)
	}

	item, err := repo.ByID(ctx, interfaceID)
	if err != nil {
		t.Fatal(err)
	}
	if item.CallCount != 1 {
		t.Fatalf("detail call count = %d, want 1", item.CallCount)
	}
}
