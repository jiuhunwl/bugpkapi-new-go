package user

import (
	"bugpk-api/internal/pkg/testdb"
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestUserSummaryAndTrendExcludeCurrentlyBannedAccounts(t *testing.T) {
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
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	userID := time.Now().UnixNano()
	name := fmt.Sprintf("summary-ban-%d", userID)
	if _, err = db.ExecContext(ctx, "insert into bp_user(id,username,password_hash,display_name,home_path,status) values(?,?,'not-used',?,'/console/workspace',1)", userID, name, name); err != nil {
		t.Fatal(err)
	}
	defer db.Exec("delete from bp_user where id=?", userID)
	if _, err = db.ExecContext(ctx, "insert into bp_call_user_metric_minute(bucket_start,user_id,call_count,success_count,failure_count,first_seen,last_seen) values(current_date(),?,1,1,0,now(),now())", userID); err != nil {
		t.Fatal(err)
	}
	defer db.Exec("delete from bp_call_user_metric_minute where user_id=?", userID)
	repo := NewRepository(db)
	for _, step := range []struct {
		name, update string
		want         int64
	}{
		{"active", "status=1,ban_permanent=0,ban_until=null", 1},
		{"permanent ban", "status=0,ban_permanent=1,ban_until=null", 0},
		{"temporary ban", "status=0,ban_permanent=0,ban_until=date_add(now(),interval 1 minute)", 0},
		{"expired ban", "status=0,ban_permanent=0,ban_until=date_sub(now(),interval 1 second)", 1},
	} {
		if _, err = db.ExecContext(ctx, "update bp_user set "+step.update+" where id=?", userID); err != nil {
			t.Fatal(err)
		}
		summary, err := repo.UserCallSummary(ctx, userID)
		if err != nil {
			t.Fatal(err)
		}
		if summary.TotalCalls != step.want {
			t.Errorf("%s summary=%d want %d", step.name, summary.TotalCalls, step.want)
		}
		trend, err := repo.UserDailyCallCounts(ctx, userID)
		if err != nil {
			t.Fatal(err)
		}
		if trend[time.Now().Format("2006-01-02")] != step.want {
			t.Errorf("%s trend=%v want %d", step.name, trend, step.want)
		}
	}
}
