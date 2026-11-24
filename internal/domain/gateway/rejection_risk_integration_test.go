package gateway_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/app"
	"bugpk-api/internal/config"
	"bugpk-api/internal/domain/gateway"
	"bugpk-api/internal/domain/ipregion"
	"bugpk-api/internal/domain/riskcontrol"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/testdb"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
	_ "github.com/go-sql-driver/mysql"
	"github.com/redis/go-redis/v9"
)

func TestRejectedOpenCallsReachRiskControlIntegration(t *testing.T) {
	if os.Getenv("GATEWAY_INTEGRATION") != "1" {
		t.Skip("set GATEWAY_INTEGRATION=1 to run")
	}
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if err := testdb.ValidateIsolatedMySQLDSN(cfg.Database.DSN()); err != nil {
		t.Fatal(err)
	}
	cfg.Migration.Path = "../../../migrations"
	db, err := sql.Open("mysql", cfg.Database.DSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := app.RunMigrations(db, cfg.Migration.Path); err != nil {
		t.Fatal(err)
	}
	redisClient := redis.NewClient(&redis.Options{
		Addr:     cfg.Redis.Addr(),
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.Database,
	})
	t.Cleanup(func() { _ = redisClient.Close() })
	pingCtx, cancelPing := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelPing()
	if err := redisClient.Ping(pingCtx).Err(); err != nil {
		t.Fatalf("Redis integration unavailable at %s: %v", cfg.Redis.Addr(), err)
	}
	apiCode := fmt.Sprintf("risk_rejected_%x", time.Now().UnixNano())
	insert, err := db.Exec(`
		insert into bp_interface_api(api_code,name,request_url,request_method,remark,status)
		values (?,'Risk rejected call','https://example.com/{phone}','POST','integration',1)
	`, apiCode)
	if err != nil {
		t.Fatal(err)
	}
	interfaceID, err := insert.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	secret := "bp_live_rejection_" + apiCode
	secretDigest := sha256.Sum256([]byte(secret))
	keyInsert, err := db.Exec(`
		insert into bp_api_key(user_id,name,key_hash,key_prefix,key_suffix,key_type,status)
		values (1,? ,?,'bp_live_test','test','GLOBAL',1)
	`, "risk-test-"+apiCode, hex.EncodeToString(secretDigest[:]))
	if err != nil {
		t.Fatal(err)
	}
	keyID, err := keyInsert.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`delete from bp_interface_call_log where interface_id=?`, interfaceID)
		_, _ = db.Exec(`delete from bp_api_key where id=?`, keyID)
		_, _ = db.Exec(`delete from bp_interface_api where id=?`, interfaceID)
	})
	service := gateway.NewService(gateway.NewRepository(db), nil, redisClient, "integration-risk-fingerprint-secret-with-32-bytes")
	_, openErr := service.Open(context.Background(), apiCode, gateway.OpenRequest{
		ClientIP: "203.0.113.25",
		Headers:  map[string]string{"X-API-Key": secret},
		Method:   "GET",
		Query:    map[string][]string{"phone": {"13800138000"}},
		RawQuery: "phone=13800138000",
	})
	var business *apperrors.BusinessError
	if !errors.As(openErr, &business) || business.StatusCode != consts.StatusMethodNotAllowed {
		t.Fatalf("method mismatch error = %v, want 405 business error", openErr)
	}
	var rejectionCount int
	if err := db.QueryRow(`
		select count(*) from bp_interface_call_log
		where interface_id=? and api_key_id=? and response_status=405 and success=0
		  and billable=0 and charge_type='NO_CHARGE' and request_fingerprint<>''
	`, interfaceID, keyID).Scan(&rejectionCount); err != nil {
		t.Fatal(err)
	}
	if rejectionCount != 0 {
		t.Fatalf("method rejection log count = %d, want 0", rejectionCount)
	}
	if _, err := db.Exec(`update bp_api_key set status=0 where id=?`, keyID); err != nil {
		t.Fatal(err)
	}
	_, disabledErr := service.Open(context.Background(), apiCode, gateway.OpenRequest{
		ClientIP: "203.0.113.25",
		Headers:  map[string]string{"X-API-Key": secret},
		Method:   "POST",
		Query:    map[string][]string{"phone": {"13800138000"}},
		RawQuery: "phone=13800138000",
	})
	if !errors.As(disabledErr, &business) || business.StatusCode != consts.StatusForbidden {
		t.Fatalf("disabled key error = %v, want 403 business error", disabledErr)
	}
	var disabledRejectionCount int
	if err := db.QueryRow(`
		select count(*) from bp_interface_call_log
		where interface_id=? and api_key_id=? and response_status=403 and success=0
		  and billable=0 and charge_type='NO_CHARGE' and error_message='API Key已禁用'
	`, interfaceID, keyID).Scan(&disabledRejectionCount); err != nil {
		t.Fatal(err)
	}
	if disabledRejectionCount != 0 {
		t.Fatalf("disabled-key rejection log count = %d, want 0", disabledRejectionCount)
	}
	if _, err := db.Exec(`update bp_api_key set status=1 where id=?`, keyID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`delete from bp_interface_call_log where interface_id=?`, interfaceID); err != nil {
		t.Fatal(err)
	}

	logs := gateway.NewRepository(db)
	for index := 0; index < 8; index++ {
		_, err := logs.CreateOpenLog(context.Background(), gateway.OpenResult{
			APIKeyID:           keyID,
			APIKeyPrefix:       "bp_live_test",
			APICode:            apiCode,
			BillingStatus:      "RELEASED",
			ChargeAmount:       "0.0000",
			ChargeType:         "NO_CHARGE",
			ClientIP:           "203.0.113.25",
			ErrorMessage:       "缺少必填参数: phone",
			InterfaceID:        interfaceID,
			RequestFingerprint: strings.Repeat("b", 64),
			RequestMethod:      "POST",
			RequestParams:      `{"action":"lookup","phone":"***"}`,
			RequestValue:       "203.0.113.25",
			ResponseStatus:     400,
			TraceID:            "gw_rejected_integration",
			UserID:             1,
			UserVisible:        true,
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	events, err := riskcontrol.NewRepository(db).RiskEvents(context.Background(), riskcontrol.RiskQuery{Limit: 10, WindowMinutes: 10})
	if err != nil {
		t.Fatal(err)
	}
	var rejectedEvent *riskcontrol.RiskEvent
	for index := range events {
		if events[index].EventType == "SINGLE_IP_CC" && events[index].InterfaceID == fmt.Sprint(interfaceID) {
			rejectedEvent = &events[index]
			break
		}
	}
	if rejectedEvent == nil {
		t.Fatalf("rejected calls did not produce a single-IP risk event: %+v", events)
	}
	if rejectedEvent.RequestSample != "fingerprint:"+strings.Repeat("b", 12) ||
		strings.Contains(rejectedEvent.RequestSample, "action") ||
		strings.Contains(rejectedEvent.RequestSample, "203.0.113.25") {
		t.Fatalf("risk sample did not use the privacy-safe fingerprint: %q", rejectedEvent.RequestSample)
	}
	if _, err := db.Exec(`delete from bp_interface_call_log where interface_id=?`, interfaceID); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 8; index++ {
		logID, err := logs.CreateOpenLog(context.Background(), gateway.OpenResult{
			APIKeyID:           keyID,
			APICode:            apiCode,
			BillingStatus:      "NO_CHARGE",
			ChargeAmount:       "0.0000",
			ChargeType:         "NO_CHARGE",
			ClientIP:           "203.0.113.26",
			ErrorMessage:       "缺少必填参数: phone",
			InterfaceID:        interfaceID,
			RequestFingerprint: strings.Repeat("c", 64),
			RequestMethod:      "POST",
			RequestParams:      `{"action":"lookup","phone":"***"}`,
			ResponseStatus:     400,
			TraceID:            "gw_low_rate_integration",
			UserID:             1,
			UserVisible:        true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`update bp_interface_call_log set created_at=date_sub(now(), interval ? minute) where id=?`, index*10, logID); err != nil {
			t.Fatal(err)
		}
	}
	events, err = riskcontrol.NewRepository(db).RiskEvents(context.Background(), riskcontrol.RiskQuery{Limit: 10, WindowMinutes: 120})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.InterfaceID != fmt.Sprint(interfaceID) {
			continue
		}
		t.Fatalf("low-rate calls across separate five-minute buckets were misclassified as CC: %+v", events)
	}
	if _, err := db.Exec(`delete from bp_interface_call_log where interface_id=?`, interfaceID); err != nil {
		t.Fatal(err)
	}

	overlapFingerprint := strings.Repeat("d", 64)
	for ipIndex := 1; ipIndex <= 5; ipIndex++ {
		for requestIndex := 0; requestIndex < 8; requestIndex++ {
			_, err := logs.CreateOpenLog(context.Background(), gateway.OpenResult{
				APIKeyID:           keyID,
				APICode:            apiCode,
				BillingStatus:      "NO_CHARGE",
				ChargeAmount:       "0.0000",
				ChargeType:         "NO_CHARGE",
				ClientIP:           fmt.Sprintf("203.0.113.%d", 100+ipIndex),
				ErrorMessage:       "缺少必填参数: phone",
				InterfaceID:        interfaceID,
				RequestFingerprint: overlapFingerprint,
				RequestMethod:      "POST",
				RequestParams:      `{"action":"lookup","phone":"***"}`,
				Region: ipregion.Region{
					City:      fmt.Sprintf("City-%d", ipIndex),
					Country:   "Riskland",
					ISP:       "Integration ISP",
					Latitude:  float64(ipIndex),
					Longitude: float64(100 + ipIndex),
					Provider:  "integration",
					Province:  fmt.Sprintf("Province-%d", ipIndex),
					Region:    fmt.Sprintf("Riskland Province-%d City-%d", ipIndex, ipIndex),
				},
				ResponseStatus: 400,
				TraceID:        "gw_overlap_integration",
				UserID:         1,
				UserVisible:    true,
			})
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	overview, err := riskcontrol.NewRepository(db).Overview(context.Background(), riskcontrol.RiskQuery{
		Limit:         1,
		WindowMinutes: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if overview.SingleIPEvents != 5 || overview.ProxyPoolEvents != 1 {
		t.Fatalf("overlapping event counts = single:%d proxy:%d, want 5 and 1", overview.SingleIPEvents, overview.ProxyPoolEvents)
	}
	if overview.RiskyRequests != 40 {
		t.Fatalf("deduplicated risky requests = %d, want 40", overview.RiskyRequests)
	}
	if len(overview.Events) != 1 {
		t.Fatalf("overview display events = %d, want query limit 1", len(overview.Events))
	}
	var trendRiskyRequests int64
	for _, point := range overview.Trend {
		trendRiskyRequests += point.RiskyEvents
	}
	if trendRiskyRequests != 40 {
		t.Fatalf("trend deduplicated risky requests = %d, want 40", trendRiskyRequests)
	}
	earth, err := riskcontrol.NewRepository(db).Earth(context.Background(), riskcontrol.RiskQuery{
		Limit:         1,
		WindowMinutes: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(earth.Points) != 1 {
		t.Fatalf("earth display points = %d, want query limit 1", len(earth.Points))
	}
	if earth.ActivePoints != 5 || earth.TotalCalls != 40 || earth.TotalRiskHits != 40 {
		t.Fatalf("earth totals = points:%d calls:%d risk:%d, want 5/40/40", earth.ActivePoints, earth.TotalCalls, earth.TotalRiskHits)
	}
	if earth.Points[0].RiskHits != 8 {
		t.Fatalf("earth first displayed point risk hits = %d, want 8", earth.Points[0].RiskHits)
	}
}
