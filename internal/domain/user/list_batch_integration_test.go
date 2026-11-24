package user

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/pkg/testdb"

	_ "github.com/go-sql-driver/mysql"
)

func TestListUsersLoadsPageRelationsWithSingleDatabaseConnection(t *testing.T) {
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
	prefix := fmt.Sprintf("batch_list_%d", time.Now().UnixNano())
	var ids []int64
	t.Cleanup(func() {
		if len(ids) == 0 {
			return
		}
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
		args := make([]any, len(ids))
		for index, id := range ids {
			args[index] = id
		}
		_, _ = db.Exec("delete from bp_call_user_metric_minute where user_id in ("+placeholders+")", args...)
		_, _ = db.Exec("delete from bp_interface_call_log where user_id in ("+placeholders+")", args...)
		_, _ = db.Exec("delete from bp_user_specified_response where user_id in ("+placeholders+")", args...)
		_, _ = db.Exec("delete from bp_user_role where user_id in ("+placeholders+")", args...)
		_, _ = db.Exec("delete from bp_user where id in ("+placeholders+")", args...)
	})

	for index := range 3 {
		username := fmt.Sprintf("%s_%d", prefix, index)
		result, err := db.ExecContext(ctx, `
			insert into bp_user(username,password_hash,display_name,home_path,balance,points,status)
			values(?,'not-used',?,'/console/workspace',0,0,1)
		`, username, username)
		if err != nil {
			t.Fatal(err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		if _, err := db.ExecContext(ctx, "insert into bp_user_role(user_id,role_id) values(?,2)", id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `
		insert into bp_user_specified_response(user_id,enabled,billable,body)
		values(?,1,1,'{"ok":true}')
	`, ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		insert into bp_interface_call_log(interface_id,user_id,request_method,response_status,success,charge_type)
		values
		  (1,?,'GET',200,1,'BALANCE'),
		  (1,?,'POST',200,1,'POINT'),
		  (1,?,'GET',200,1,'ADMIN'),
		  (1,?,'GET',500,0,'NONE')
	`, ids[0], ids[0], ids[0], ids[1]); err != nil {
		t.Fatal(err)
	}
	seedUserRollupsForTest(t, db, ids)

	repo := NewRepository(db)
	var expectedRoleName string
	if err := db.QueryRowContext(ctx, "select role_name from bp_role where id=2").Scan(&expectedRoleName); err != nil {
		t.Fatal(err)
	}
	result, err := repo.ListUsers(ctx, UserListParams{Keyword: prefix, Page: 1, PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 3 || result.Total != 3 {
		t.Fatalf("list result = total %d items %d", result.Total, len(result.Items))
	}
	var specifiedCount int
	requestCounts := make(map[string]int64, len(result.Items))
	for _, item := range result.Items {
		if len(item.RoleIDs) != 1 || item.RoleIDs[0] != 2 || len(item.Roles) != 1 || item.Roles[0] != "user" || len(item.RoleNames) != 1 || item.RoleNames[0] != expectedRoleName {
			t.Fatalf("roles not batch loaded for %s: ids=%v keys=%v names=%v", item.Username, item.RoleIDs, item.Roles, item.RoleNames)
		}
		if item.SpecifiedResponseEnabled == 1 && item.SpecifiedResponseBillable == 1 {
			specifiedCount++
		}
		requestCounts[item.Username] = item.TotalRequests
	}
	if specifiedCount != 1 {
		t.Fatalf("specified-response flags loaded for %d users, want 1", specifiedCount)
	}
	if got := requestCounts[prefix+"_0"]; got != 2 {
		t.Fatalf("first user's total requests = %d, want 2 non-admin calls", got)
	}
	if got := requestCounts[prefix+"_1"]; got != 1 {
		t.Fatalf("second user's total requests = %d, want 1", got)
	}
	if got := requestCounts[prefix+"_2"]; got != 0 {
		t.Fatalf("third user's total requests = %d, want 0", got)
	}
}

func seedUserRollupsForTest(t *testing.T, db *sql.DB, userIDs []int64) {
	t.Helper()
	if len(userIDs) == 0 {
		return
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(userIDs)), ",")
	args := make([]any, len(userIDs))
	for index, id := range userIDs {
		args[index] = id
	}
	query := `
		insert into bp_call_user_metric_minute(
			bucket_start,user_id,call_count,success_count,failure_count,billable_count,
			charge_amount,point_amount,request_bytes,response_bytes,elapsed_sum,
			elapsed_min,elapsed_max,first_seen,last_seen
		)
		select from_unixtime(floor(unix_timestamp(created_at)/60)*60),user_id,
		       count(*),sum(success=1),sum(success=0),sum(billable=1),
		       sum(charge_amount),sum(point_price),sum(request_size),sum(response_size),sum(elapsed_ms),
		       min(elapsed_ms),max(elapsed_ms),min(created_at),max(created_at)
		from bp_interface_call_log
		where user_id in (` + placeholders + `) and charge_type<>'ADMIN'
		group by from_unixtime(floor(unix_timestamp(created_at)/60)*60),user_id
		on duplicate key update call_count=values(call_count),success_count=values(success_count),
		failure_count=values(failure_count),billable_count=values(billable_count),charge_amount=values(charge_amount),
		point_amount=values(point_amount),request_bytes=values(request_bytes),response_bytes=values(response_bytes),
		elapsed_sum=values(elapsed_sum),elapsed_min=values(elapsed_min),elapsed_max=values(elapsed_max),
		first_seen=values(first_seen),last_seen=values(last_seen)
	`
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}
