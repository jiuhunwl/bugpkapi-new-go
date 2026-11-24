package gateway_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	analyticsdomain "bugpk-api/internal/domain/analytics"
	contentdomain "bugpk-api/internal/domain/content"
	gatewaydomain "bugpk-api/internal/domain/gateway"
	interfaceapidomain "bugpk-api/internal/domain/interfaceapi"
	observabilitydomain "bugpk-api/internal/domain/observability"
	riskcontroldomain "bugpk-api/internal/domain/riskcontrol"
	userdomain "bugpk-api/internal/domain/user"
	"bugpk-api/internal/pkg/accountstatus"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/testdb"

	_ "github.com/go-sql-driver/mysql"
	"github.com/redis/go-redis/v9"
)

type activeTrafficSnapshot struct {
	adminAccess int64
	dashboard   int64
	home        int64
	market      int64
	risk        int64
	workbench   int64
}

func TestBannedAccountTrafficIsExcludedFromBusinessAnalytics(t *testing.T) {
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
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(2)
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	unique := fmt.Sprintf("active_traffic_%d", time.Now().UnixNano())
	apiCode := "ban_analytics_" + fmt.Sprint(time.Now().UnixNano())
	interfaceResult, err := db.ExecContext(ctx, `
		insert into bp_interface_api(api_code,name,request_url,request_method,status,remark)
		values(?,?,'https://127.0.0.1/never-called','GET',1,'account ban analytics integration test')
	`, apiCode, unique)
	if err != nil {
		t.Fatal(err)
	}
	interfaceID, err := interfaceResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec("delete from bp_interface_api where id=?", interfaceID)
	})

	result, err := db.ExecContext(ctx, `
		insert into bp_user(
			username,password_hash,display_name,home_path,balance,points,status,
			ban_permanent,ban_until,ban_reason,token_version
		)
		values(?,'not-used',?,'/console/workspace',0,0,1,0,null,'',0)
	`, unique, unique)
	if err != nil {
		t.Fatal(err)
	}
	userID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	secret := "bp_live_" + unique
	keyDigest := sha256.Sum256([]byte(secret))
	keyHash := hex.EncodeToString(keyDigest[:])
	keyResult, err := db.ExecContext(ctx, `
		insert into bp_api_key(user_id,name,key_hash,key_prefix,key_suffix,key_type,status)
		values(?,?,?,left(?,16),'test','GLOBAL',1)
	`, userID, unique, keyHash, unique)
	if err != nil {
		t.Fatal(err)
	}
	keyID, err := keyResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec("delete from bp_interface_call_log where user_id=?", userID)
		_, _ = db.Exec("delete from bp_api_key where id=?", keyID)
		_, _ = db.Exec("delete from bp_user where id=?", userID)
	})

	baseline := readActiveTrafficSnapshot(t, ctx, db, userID, interfaceID)
	traceID := "ban-analytics-" + unique
	if _, err := db.ExecContext(ctx, `
		insert into bp_interface_call_log(
			interface_id,user_id,api_key_id,api_key_prefix,api_code,request_method,
			request_params,request_fingerprint,response_status,success,billable,
			charge_type,billing_status,client_ip,client_region,ip_country,ip_province,
			ip_city,ip_isp,trace_id,user_visible,created_at
		)
		values(?,?,?,?,?,'GET','{}',?,200,1,0,'FREE','CONFIRMED',
		       '203.0.113.99','China Test Province Test City','China','Test Province',
		       'Test City','Test ISP',?,1,now())
	`, interfaceID, userID, keyID, unique[:min(len(unique), 16)], apiCode, keyHash, traceID); err != nil {
		t.Fatal(err)
	}

	active := readActiveTrafficSnapshot(t, ctx, db, userID, interfaceID)
	assertActiveTrafficDelta(t, baseline, active, 1)
	assertRawTrafficRetained(t, ctx, db, userID, 1)

	if _, err := db.ExecContext(ctx, `
		update bp_user
		set status=0,ban_permanent=1,ban_until=null,ban_reason='permanent analytics test',token_version=token_version+1
		where id=?
	`, userID); err != nil {
		t.Fatal(err)
	}
	permanentlyBanned := readActiveTrafficSnapshot(t, ctx, db, userID, interfaceID)
	assertActiveTrafficDelta(t, baseline, permanentlyBanned, 0)
	assertRawTrafficRetained(t, ctx, db, userID, 1)
	assertBannedGatewayAttemptIsNotLogged(t, ctx, db, userID, apiCode, secret)

	if _, err := db.ExecContext(ctx, `
		update bp_user
		set status=0,ban_permanent=0,ban_until=date_add(now(),interval 10 minute),ban_reason='temporary analytics test'
		where id=?
	`, userID); err != nil {
		t.Fatal(err)
	}
	temporarilyBanned := readActiveTrafficSnapshot(t, ctx, db, userID, interfaceID)
	assertActiveTrafficDelta(t, baseline, temporarilyBanned, 0)

	if _, err := db.ExecContext(ctx, `
		update bp_user
		set ban_until=date_sub(now(),interval 1 second)
		where id=?
	`, userID); err != nil {
		t.Fatal(err)
	}
	expired := readActiveTrafficSnapshot(t, ctx, db, userID, interfaceID)
	assertActiveTrafficDelta(t, baseline, expired, 1)

	if _, err := db.ExecContext(ctx, `
		update bp_user
		set status=1,ban_permanent=0,ban_until=null,ban_reason=''
		where id=?
	`, userID); err != nil {
		t.Fatal(err)
	}
	unbanned := readActiveTrafficSnapshot(t, ctx, db, userID, interfaceID)
	assertActiveTrafficDelta(t, baseline, unbanned, 1)
	assertRawTrafficRetained(t, ctx, db, userID, 1)
}

func readActiveTrafficSnapshot(t *testing.T, ctx context.Context, db *sql.DB, userID, interfaceID int64) activeTrafficSnapshot {
	t.Helper()
	// Statistics now read projections; run the same worker used in production
	// before asserting visibility after inserting raw traffic.
	if _, err := analyticsdomain.NewRepository(db).RunUntilCaughtUp(ctx, 1000, 100); err != nil {
		t.Fatalf("refresh analytics projections: %v", err)
	}
	contentRepo := contentdomain.NewRepository(db)
	stats, _, err := contentRepo.HomeOverview(ctx)
	if err != nil {
		t.Fatalf("home overview: %v", err)
	}
	dashboard, err := observabilitydomain.NewRepository(db).Overview(ctx)
	if err != nil {
		t.Fatalf("dashboard overview: %v", err)
	}
	risk, err := riskcontroldomain.NewRepository(db).Overview(ctx, riskcontroldomain.RiskQuery{Limit: 5, WindowMinutes: 60})
	if err != nil {
		t.Fatalf("risk overview: %v", err)
	}
	workbench, err := userdomain.NewRepository(db).UserCallSummary(ctx, userID)
	if err != nil {
		t.Fatalf("user workbench summary: %v", err)
	}
	trend, err := interfaceapidomain.NewRepository(db).InterfaceTrend7d(ctx, interfaceID)
	if err != nil {
		t.Fatalf("interface market trend: %v", err)
	}
	var marketCalls int64
	for _, point := range trend {
		value, ok := point["value"].(int64)
		if !ok {
			t.Fatalf("unexpected market trend value: %#v", point["value"])
		}
		marketCalls += value
	}
	access, err := userdomain.NewRepository(db).AdminAccessSummary(ctx)
	if err != nil {
		t.Fatalf("admin access summary: %v", err)
	}
	return activeTrafficSnapshot{
		adminAccess: access.TotalCalls,
		dashboard:   dashboard.TodayCalls,
		home:        stats.Calls24h,
		market:      marketCalls,
		risk:        risk.TotalRequests,
		workbench:   workbench.TotalCalls,
	}
}

func assertActiveTrafficDelta(t *testing.T, baseline, got activeTrafficSnapshot, want int64) {
	t.Helper()
	checks := map[string][2]int64{
		"admin access": {baseline.adminAccess, got.adminAccess},
		"dashboard":    {baseline.dashboard, got.dashboard},
		"homepage":     {baseline.home, got.home},
		"market":       {baseline.market, got.market},
		"risk":         {baseline.risk, got.risk},
		"workbench":    {baseline.workbench, got.workbench},
	}
	for name, values := range checks {
		if delta := values[1] - values[0]; delta != want {
			t.Fatalf("%s traffic delta = %d, want %d (baseline=%d current=%d)", name, delta, want, values[0], values[1])
		}
	}
}

func assertRawTrafficRetained(t *testing.T, ctx context.Context, db *sql.DB, userID, want int64) {
	t.Helper()
	count, err := userdomain.NewRepository(db).UserTotalRequests(ctx, userID)
	if err != nil {
		t.Fatalf("user management total requests: %v", err)
	}
	if count != want {
		t.Fatalf("user management total requests = %d, want %d", count, want)
	}
	var rawCount int64
	if err := db.QueryRowContext(ctx, "select count(*) from bp_interface_call_log where user_id=?", userID).Scan(&rawCount); err != nil {
		t.Fatalf("raw call log count: %v", err)
	}
	if rawCount != want {
		t.Fatalf("raw call log count = %d, want %d", rawCount, want)
	}
}

func assertBannedGatewayAttemptIsNotLogged(t *testing.T, ctx context.Context, db *sql.DB, userID int64, apiCode, secret string) {
	t.Helper()
	if os.Getenv("BUGPK_GATEWAY_REDIS_INTEGRATION") != "1" {
		t.Log("set BUGPK_GATEWAY_REDIS_INTEGRATION=1 to verify the guarded gateway path")
		return
	}
	redisAddress := os.Getenv("REDIS_ADDR")
	if redisAddress == "" {
		redisAddress = "127.0.0.1:16379"
	}
	redisClient := redis.NewClient(&redis.Options{Addr: redisAddress})
	t.Cleanup(func() { _ = redisClient.Close() })
	if err := redisClient.Ping(ctx).Err(); err != nil {
		t.Fatalf("Redis integration unavailable at %s: %v", redisAddress, err)
	}
	service := gatewaydomain.NewService(
		gatewaydomain.NewRepository(db),
		nil,
		redisClient,
		"active-account-analytics-integration-secret",
	)
	t.Cleanup(func() { _ = service.Close(context.Background()) })
	_, err := service.Open(ctx, apiCode, gatewaydomain.OpenRequest{
		ClientIP: "203.0.113.100",
		Headers:  map[string]string{"X-API-Key": secret},
		Method:   "GET",
	})
	var business *apperrors.BusinessError
	if !errors.As(err, &business) || business.StatusCode != 403 {
		t.Fatalf("banned gateway error = %v, want 403 business error", err)
	}
	details, ok := business.Data.(accountstatus.BanDetails)
	if !ok || details.Tag != accountstatus.BanTag || details.BanReason != "permanent analytics test" {
		t.Fatalf("banned gateway details = %#v", business.Data)
	}
	assertRawTrafficRetained(t, ctx, db, userID, 1)
}
