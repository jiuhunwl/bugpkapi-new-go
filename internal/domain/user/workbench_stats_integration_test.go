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

func TestAdminWorkbenchLoadsPaymentRevenueAndPendingCounts(t *testing.T) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	prefix := fmt.Sprintf("workbench-%d", time.Now().UnixNano())
	friendURL := "https://" + prefix + ".example.com"
	t.Cleanup(func() {
		_, _ = db.Exec("delete from bp_payment_order where order_no like ?", prefix+"%")
		_, _ = db.Exec("delete from bp_friend_link_application where normalized_site_url=?", friendURL)
	})

	repo := NewRepository(db)
	before, err := repo.AdminOverview(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := db.ExecContext(ctx, `
		insert into bp_payment_order(order_no,user_id,order_type,product,subject,pay_amount,status,expire_at,created_at)
		values(?,1,'RECHARGE','PAGE','pending workbench order',9.0000,'CREATED',date_add(now(),interval 1 hour),now())
	`, prefix+"-pending"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		insert into bp_friend_link_application(user_id,username,site_name,site_url,normalized_site_url,status)
		values(?,?,?,?,?,'PENDING')
	`, time.Now().UnixNano(), prefix, prefix, friendURL, friendURL); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		amount string
		days   int
		suffix string
	}{
		{amount: "12.3400", days: 0, suffix: "today"},
		{amount: "5.6600", days: 3, suffix: "recent"},
		{amount: "99.0000", days: 8, suffix: "old"},
	} {
		if _, err := db.ExecContext(ctx, `
			insert into bp_payment_order(order_no,user_id,order_type,product,subject,pay_amount,status,expire_at,paid_at,created_at)
			values(?,1,'RECHARGE','PAGE','paid workbench order',?,'PAID',date_add(now(),interval 1 hour),date_sub(now(),interval ? day),date_sub(now(),interval ? day))
		`, prefix+"-"+item.suffix, item.amount, item.days, item.days); err != nil {
			t.Fatal(err)
		}
	}

	after, err := repo.AdminOverview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.PendingOrders != before.PendingOrders+1 {
		t.Fatalf("pending orders = %d, want %d", after.PendingOrders, before.PendingOrders+1)
	}
	if after.PendingFriendLinks != before.PendingFriendLinks+1 {
		t.Fatalf("pending friend links = %d, want %d", after.PendingFriendLinks, before.PendingFriendLinks+1)
	}

	now := time.Now()
	labels := dayLabels(now, 7)
	trend, err := repo.AdminRevenueTrend(ctx, labels)
	if err != nil {
		t.Fatal(err)
	}
	if trend.TotalOrders != 2 || trend.TotalAmount != "18.0000" {
		t.Fatalf("revenue totals = orders %d amount %s", trend.TotalOrders, trend.TotalAmount)
	}
	index := make(map[string]int, len(labels))
	for i, label := range labels {
		index[label] = i
	}
	if i := index[now.Format("01-02")]; trend.OrderCounts[i] != 1 || trend.PaidAmounts[i] != "12.3400" {
		t.Fatalf("today revenue = count %d amount %s", trend.OrderCounts[i], trend.PaidAmounts[i])
	}
	recentLabel := now.AddDate(0, 0, -3).Format("01-02")
	if i := index[recentLabel]; trend.OrderCounts[i] != 1 || trend.PaidAmounts[i] != "5.6600" {
		t.Fatalf("recent revenue = count %d amount %s", trend.OrderCounts[i], trend.PaidAmounts[i])
	}
}

func TestUserDailyCallCountsUsesOneBoundedOpenTrafficQuery(t *testing.T) {
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
	userID := time.Now().UnixNano()
	t.Cleanup(func() {
		_, _ = db.Exec("delete from bp_call_user_metric_minute where user_id=?", userID)
		_, _ = db.Exec("delete from bp_call_api_metric_minute where user_id=?", userID)
		_, _ = db.Exec("delete from bp_interface_call_log where user_id=?", userID)
		_, _ = db.Exec("delete from bp_user where id=?", userID)
	})
	if _, err := db.ExecContext(ctx, `
		insert into bp_user(id,username,password_hash,display_name,home_path,balance,points,status)
		values(?,?,'not-used',?,'/console/workspace',0,0,1)
	`, userID, fmt.Sprintf("rollup-user-%d", userID), fmt.Sprintf("rollup-user-%d", userID)); err != nil {
		t.Fatal(err)
	}

	for _, item := range []struct {
		chargeType string
		count      int
		days       int
	}{
		{chargeType: "FREE", count: 1, days: 0},
		{chargeType: "POINTS", count: 2, days: 3},
		{chargeType: "BALANCE", count: 1, days: 40},
		{chargeType: "FREE", count: 1, days: 370},
		{chargeType: "ADMIN", count: 4, days: 0},
	} {
		for range item.count {
			if _, err := db.ExecContext(ctx, `
				insert into bp_interface_call_log(interface_id,user_id,request_method,response_status,success,billable,charge_type,created_at)
				values(1,?,'GET',200,1,0,?,date_sub(now(),interval ? day))
			`, userID, item.chargeType, item.days); err != nil {
				t.Fatal(err)
			}
		}
	}
	seedUserRollupsForTest(t, db, []int64{userID})

	counts, err := NewRepository(db).UserDailyCallCounts(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if got := counts[now.Format("2006-01-02")]; got != 1 {
		t.Fatalf("today count = %d, want 1", got)
	}
	if got := counts[now.AddDate(0, 0, -3).Format("2006-01-02")]; got != 2 {
		t.Fatalf("three-day count = %d, want 2", got)
	}
	if got := counts[now.AddDate(0, 0, -40).Format("2006-01-02")]; got != 1 {
		t.Fatalf("forty-day count = %d, want 1", got)
	}
	if _, ok := counts[now.AddDate(0, 0, -370).Format("2006-01-02")]; ok {
		t.Fatal("older-than-one-year call was included")
	}
}

func TestAdminAccessLocationsAggregatesAllRegionsAndExcludesAdminTraffic(t *testing.T) {
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
	prefix := fmt.Sprintf("atlas-%d", time.Now().UnixNano())
	atlasUserID := time.Now().UnixNano()
	t.Cleanup(func() {
		_, _ = db.Exec("delete from bp_call_region_hour where country like ?", prefix+"%")
		_, _ = db.Exec("delete from bp_call_user_metric_minute where user_id=?", atlasUserID)
		_, _ = db.Exec("delete from bp_call_api_metric_minute where user_id=?", atlasUserID)
		_, _ = db.Exec("delete from bp_interface_call_log where client_ip like ?", prefix+"%")
		_, _ = db.Exec("delete from bp_user where id=?", atlasUserID)
	})
	if _, err := db.ExecContext(ctx, `
		insert into bp_user(id,username,password_hash,display_name,home_path,balance,points,status)
		values(?,?,'not-used',?,'/console/workspace',0,0,1)
	`, atlasUserID, prefix, prefix); err != nil {
		t.Fatal(err)
	}

	insert := func(ip, chargeType, country, province, city, isp, region, latitude, longitude string, success int) {
		t.Helper()
		_, execErr := db.ExecContext(ctx, `
			insert into bp_interface_call_log(
				interface_id,user_id,request_method,response_status,success,billable,charge_type,
				client_ip,client_region,ip_country,ip_province,ip_city,ip_isp,latitude,longitude,elapsed_ms,created_at
			) values(1,?,'GET',200,?,0,?,?,?,?,?,?,?,?,?,25,now())
		`, atlasUserID, success, chargeType, ip, region, country, province, city, isp, latitude, longitude)
		if execErr != nil {
			t.Fatal(execErr)
		}
	}
	before, err := NewRepository(db).AdminAccessSummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	insert(prefix+"-1", "FREE", prefix+"-CN", "广东省", "深圳市", "ISP-A", prefix+" 广东省 深圳市", "22.543100", "114.057900", 1)
	insert(prefix+"-2", "FREE", prefix+"-CN", "广东省", "深圳市", "ISP-A", prefix+" 广东省 深圳市", "22.543100", "114.057900", 0)
	insert(prefix+"-3", "BALANCE", prefix+"-JP", "东京都", "东京", "ISP-B", prefix+" 东京都 东京", "35.676200", "139.650300", 1)
	insert(prefix+"-4", "ADMIN", prefix+"-US", "", "", "ISP-C", prefix+" 美国", "40.712800", "-74.006000", 1)

	seedRegionRollupsForTest(t, db, atlasUserID)
	repo := NewRepository(db)
	summary, err := repo.AdminAccessSummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if summary.TotalCalls != before.TotalCalls+3 || summary.SuccessCalls != before.SuccessCalls+2 || summary.FailedCalls != before.FailedCalls+1 {
		t.Fatalf("summary calls = %+v, want three non-admin calls added", summary)
	}
	if summary.UniqueIPs != before.UniqueIPs+3 || summary.ActiveCountries != before.ActiveCountries+2 || summary.ActiveLocations != before.ActiveLocations+2 {
		t.Fatalf("summary locations = %+v, want three IPs / two countries / two locations added", summary)
	}

	locations, err := repo.AdminAccessLocations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var atlasLocations []AdminAccessLocation
	for _, location := range locations {
		if strings.HasPrefix(location.Country, prefix) {
			atlasLocations = append(atlasLocations, location)
		}
	}
	if len(atlasLocations) != 2 {
		t.Fatalf("location rows = %d, want 2 rows for this test", len(atlasLocations))
	}
	if atlasLocations[0].CallCount != 2 || atlasLocations[0].FailedCalls != 1 || atlasLocations[0].UniqueIPs != 2 {
		t.Fatalf("first location = %+v, want aggregated first region row", atlasLocations[0])
	}
}

func seedRegionRollupsForTest(t *testing.T, db *sql.DB, userID int64) {
	t.Helper()
	if _, err := db.Exec(`
		insert into bp_call_region_hour(
			bucket_start,user_id,country,province,city,isp,latitude,longitude,
			call_count,success_count,failure_count,unique_ip_count,
			request_bytes,response_bytes,elapsed_sum,first_seen,last_seen
		)
		select from_unixtime(floor(unix_timestamp(created_at)/3600)*3600),user_id,
		       coalesce(ip_country,''),coalesce(ip_province,''),coalesce(ip_city,''),coalesce(ip_isp,''),
		       coalesce(latitude,0),coalesce(longitude,0),
		       count(*),sum(success=1),sum(success=0),count(distinct nullif(trim(coalesce(client_ip,'')),'')),
		       sum(request_size),sum(response_size),sum(elapsed_ms),
		       min(created_at),max(created_at)
		from bp_interface_call_log
		where user_id=? and charge_type<>'ADMIN'
		group by from_unixtime(floor(unix_timestamp(created_at)/3600)*3600),user_id,
		         coalesce(ip_country,''),coalesce(ip_province,''),coalesce(ip_city,''),coalesce(ip_isp,''),
		         coalesce(latitude,0),coalesce(longitude,0)
		on duplicate key update call_count=values(call_count),success_count=values(success_count),
		failure_count=values(failure_count),unique_ip_count=values(unique_ip_count),
		request_bytes=values(request_bytes),response_bytes=values(response_bytes),
		elapsed_sum=values(elapsed_sum),first_seen=values(first_seen),last_seen=values(last_seen)
	`, userID); err != nil {
		t.Fatal(err)
	}
}
