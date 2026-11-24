package riskcontrol_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/app"
	"bugpk-api/internal/config"
	"bugpk-api/internal/domain/analytics"
	"bugpk-api/internal/domain/rbac"
	"bugpk-api/internal/domain/riskcontrol"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/testdb"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
	_ "github.com/go-sql-driver/mysql"
)

func TestRiskControlPermissionsAreSeparatedIntegration(t *testing.T) {
	if os.Getenv("RISKCONTROL_INTEGRATION") != "1" {
		t.Skip("set RISKCONTROL_INTEGRATION=1 to run")
	}
	db := openRiskControlIntegrationDB(t)
	stamp := strconv.FormatInt(time.Now().UnixNano(), 36)

	roleResult, err := db.Exec(`
		insert into bp_role(role_name,role_key,status,remark)
		values(?,?,1,'risk permission integration')
	`, "Risk Viewer "+stamp, "risk-viewer-"+stamp)
	if err != nil {
		t.Fatal(err)
	}
	roleID, err := roleResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	userResult, err := db.Exec(`
		insert into bp_user(username,password_hash,display_name,status)
		values(?,?,?,1)
	`, "risk-viewer-"+stamp, "integration-test-hash", "Risk Viewer")
	if err != nil {
		t.Fatal(err)
	}
	userID, err := userResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("insert into bp_user_role(user_id,role_id) values(?,?)", userID, roleID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("insert into bp_role_menu(role_id,menu_id) values(?,170)", roleID); err != nil {
		t.Fatal(err)
	}

	apiCode := "risk-permission-" + stamp
	interfaceResult, err := db.Exec(`
		insert into bp_interface_api(api_code,name,request_url,request_method,remark)
		values(?, 'Risk Permission API', 'https://example.com/risk', 'GET', 'integration test')
	`, apiCode)
	if err != nil {
		t.Fatal(err)
	}
	interfaceID, err := interfaceResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec("delete from bp_interface_call_log where interface_id=?", interfaceID)
		_, _ = db.Exec("delete from bp_interface_api where id=?", interfaceID)
		_, _ = db.Exec("delete from bp_role_menu where role_id=?", roleID)
		_, _ = db.Exec("delete from bp_user_role where user_id=?", userID)
		_, _ = db.Exec("delete from bp_user where id=?", userID)
		_, _ = db.Exec("delete from bp_role where id=?", roleID)
	})
	fingerprint := strings.Repeat("e", 64)
	for range 8 {
		if _, err := db.Exec(`
			insert into bp_interface_call_log(
				interface_id,user_id,api_code,request_method,request_params,request_fingerprint,
				response_status,success,charge_type,client_ip,ip_country,ip_city,latitude,longitude
			) values(?,?,?,'GET','{"password":"private value","page":1}',?,200,1,'FREE',
				'203.0.113.88','CN','Shanghai',31.230400,121.473700)
		`, interfaceID, userID, apiCode, fingerprint); err != nil {
			t.Fatal(err)
		}
	}
	processRiskAnalytics(t, db)

	authz := rbac.NewService(rbac.NewRepository(db), nil)
	service := riskcontrol.NewService(riskcontrol.NewRepository(db), authz)
	principal := rbac.Principal{ID: userID, Roles: []string{"risk-viewer-" + stamp}}
	query := riskcontrol.RiskQuery{Limit: 20, WindowMinutes: 10}
	overview, err := service.Overview(context.Background(), principal, query)
	if err != nil {
		t.Fatal(err)
	}
	if len(overview.Events) != 0 || overview.TopAttackIP != "" || overview.TopAttackParam != "" {
		t.Fatalf("summary-only role received sensitive event data: %#v", overview)
	}
	assertForbidden(t, func() error {
		_, err := service.Events(context.Background(), principal, query)
		return err
	}())
	assertForbidden(t, func() error {
		_, err := service.Earth(context.Background(), principal, query)
		return err
	}())

	if _, err := db.Exec("insert into bp_role_menu(role_id,menu_id) values(?,172),(?,171)", roleID, roleID); err != nil {
		t.Fatal(err)
	}
	events, err := service.Events(context.Background(), principal, query)
	if err != nil || len(events) == 0 {
		t.Fatalf("event permission did not enable events: count=%d err=%v", len(events), err)
	}
	earth, err := service.Earth(context.Background(), principal, query)
	if err != nil || len(earth.Points) == 0 || len(earth.Events) == 0 {
		t.Fatalf("earth/event permissions did not enable full earth view: points=%d events=%d err=%v", len(earth.Points), len(earth.Events), err)
	}
}

func assertForbidden(t *testing.T, err error) {
	t.Helper()
	var business *apperrors.BusinessError
	if !errors.As(err, &business) || business.StatusCode != consts.StatusForbidden {
		t.Fatalf("error=%v, want 403 business error", err)
	}
}

func TestDistributedSameParamsAcrossUsersIntegration(t *testing.T) {
	if os.Getenv("RISKCONTROL_INTEGRATION") != "1" {
		t.Skip("set RISKCONTROL_INTEGRATION=1 to run")
	}
	db := openRiskControlIntegrationDB(t)

	stamp := strconv.FormatInt(time.Now().UnixNano(), 36)
	userIDs := []int64{1}
	for index := 0; index < 2; index++ {
		insert, err := db.Exec(`
			insert into bp_user(username,password_hash,display_name,status)
			values(?,?,?,1)
		`, fmt.Sprintf("risk-dist-%d-%s", index, stamp), "integration-test-hash", "Risk integration user")
		if err != nil {
			t.Fatal(err)
		}
		userID, err := insert.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		userIDs = append(userIDs, userID)
	}

	keyIDs := make([]int64, 0, len(userIDs))
	for index, userID := range userIDs {
		digest := sha256.Sum256([]byte(fmt.Sprintf("risk-key-%d-%s", index, stamp)))
		insert, err := db.Exec(`
			insert into bp_api_key(user_id,name,key_hash,key_prefix,key_suffix,key_type,status)
			values(?,?,?,'bp_test_','test','GLOBAL',1)
		`, userID, "Risk integration key", fmt.Sprintf("%x", digest[:]))
		if err != nil {
			t.Fatal(err)
		}
		keyID, err := insert.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		keyIDs = append(keyIDs, keyID)
	}

	apiCode := "risk-dist-" + stamp
	insert, err := db.Exec(`
		insert into bp_interface_api(api_code,name,request_url,request_method,remark)
		values(?, 'Distributed Risk API', 'https://example.com/risk', 'GET', 'integration test')
	`, apiCode)
	if err != nil {
		t.Fatal(err)
	}
	interfaceID, err := insert.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec("delete from bp_interface_call_log where api_code = ?", apiCode)
		_, _ = db.Exec("delete from bp_interface_api where id = ?", interfaceID)
		for _, keyID := range keyIDs {
			_, _ = db.Exec("delete from bp_api_key where id = ?", keyID)
		}
		for _, userID := range userIDs[1:] {
			_, _ = db.Exec("delete from bp_user where id = ?", userID)
		}
	})

	fingerprint := strings.Repeat("d", 64)
	for identityIndex, userID := range userIDs {
		for requestIndex := 0; requestIndex < 10; requestIndex++ {
			clientIP := fmt.Sprintf("203.0.113.%d", identityIndex*4+(requestIndex%4)+1)
			if _, err := db.Exec(`
				insert into bp_interface_call_log(
					interface_id,user_id,api_key_id,api_code,request_method,request_params,
					request_fingerprint,response_status,success,charge_type,client_ip
				) values(?,?,?,?, 'GET','{"page":1}',?,200,1,'FREE',?)
			`, interfaceID, userID, keyIDs[identityIndex], apiCode, fingerprint, clientIP); err != nil {
				t.Fatal(err)
			}
		}
	}
	processRiskAnalytics(t, db)

	events, err := riskcontrol.NewRepository(db).RiskEvents(context.Background(), riskcontrol.RiskQuery{
		Limit:         20,
		WindowMinutes: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.EventType != "DISTRIBUTED_SAME_PARAMS" || event.InterfaceID != strconv.FormatInt(interfaceID, 10) {
			continue
		}
		if event.UniqueIPCount < 12 || event.UniqueUserCount < 3 || event.UniqueKeyCount < 3 {
			t.Fatalf("distributed evidence is incomplete: %#v", event)
		}
		if len(event.Accounts) < 3 || len(event.RequestIPs) < 12 || len(event.APIKeys) < 3 {
			t.Fatalf("distributed identity evidence is incomplete: %#v", event)
		}
		if event.SuccessCount != event.RequestCount || event.FailedCount != 0 {
			t.Fatalf("distributed request outcomes are incomplete: %#v", event)
		}
		return
	}
	t.Fatalf("distributed risk event not found: %#v", events)
}

func TestProxyPoolSameParamsForOneUserIntegration(t *testing.T) {
	if os.Getenv("RISKCONTROL_INTEGRATION") != "1" {
		t.Skip("set RISKCONTROL_INTEGRATION=1 to run")
	}
	db := openRiskControlIntegrationDB(t)

	stamp := strconv.FormatInt(time.Now().UnixNano(), 36)
	userInsert, err := db.Exec(`
		insert into bp_user(username,password_hash,display_name,status)
		values(?,?,?,1)
	`, "risk-proxy-"+stamp, "integration-test-hash", "Risk proxy-pool user")
	if err != nil {
		t.Fatal(err)
	}
	userID, err := userInsert.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	digest := sha256.Sum256([]byte("risk-proxy-key-" + stamp))
	keyInsert, err := db.Exec(`
		insert into bp_api_key(user_id,name,key_hash,key_prefix,key_suffix,key_type,status)
		values(?,?,?,'bp_test_','test','GLOBAL',1)
	`, userID, "Risk proxy-pool key", fmt.Sprintf("%x", digest[:]))
	if err != nil {
		t.Fatal(err)
	}
	keyID, err := keyInsert.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	apiCode := "risk-proxy-" + stamp
	interfaceInsert, err := db.Exec(`
		insert into bp_interface_api(api_code,name,request_url,request_method,remark)
		values(?, 'Proxy Pool Risk API', 'https://example.com/risk', 'GET', 'integration test')
	`, apiCode)
	if err != nil {
		t.Fatal(err)
	}
	interfaceID, err := interfaceInsert.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec("delete from bp_interface_call_log where api_code = ?", apiCode)
		_, _ = db.Exec("delete from bp_interface_api where id = ?", interfaceID)
		_, _ = db.Exec("delete from bp_api_key where id = ?", keyID)
		_, _ = db.Exec("delete from bp_user where id = ?", userID)
	})

	fingerprint := strings.Repeat("p", 64)
	for ipIndex := 0; ipIndex < 5; ipIndex++ {
		clientIP := fmt.Sprintf("198.51.100.%d", ipIndex+10)
		for range 3 {
			if _, err := db.Exec(`
				insert into bp_interface_call_log(
					interface_id,user_id,api_key_id,api_code,request_method,request_params,
					request_fingerprint,response_status,success,charge_type,client_ip,created_at
				) values(?,?,?,?, 'GET','{"action":"lookup"}',?,200,1,'FREE',?,
					from_unixtime(floor(unix_timestamp(now()) / 300) * 300 + 1))
			`, interfaceID, userID, keyID, apiCode, fingerprint, clientIP); err != nil {
				t.Fatal(err)
			}
		}
	}
	processRiskAnalytics(t, db)

	events, err := riskcontrol.NewRepository(db).RiskEvents(context.Background(), riskcontrol.RiskQuery{
		Limit:         20,
		WindowMinutes: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.EventType != "PROXY_POOL_SAME_PARAMS" || event.InterfaceID != strconv.FormatInt(interfaceID, 10) {
			continue
		}
		if event.RequestCount != 15 || event.UniqueIPCount != 5 || event.UniqueUserCount != 1 || event.UniqueKeyCount != 1 {
			t.Fatalf("proxy-pool evidence is incomplete: %#v", event)
		}
		if len(event.Accounts) != 1 || event.Accounts[0].Username != "risk-proxy-"+stamp {
			t.Fatalf("proxy-pool account evidence is incomplete: %#v", event)
		}
		if len(event.RequestIPs) != 5 || len(event.APIKeys) != 1 || event.SuccessCount != 15 || event.FailedCount != 0 {
			t.Fatalf("proxy-pool request evidence is incomplete: %#v", event)
		}
		return
	}
	t.Fatalf("proxy-pool risk event not found: %#v", events)
}

func TestDeletedInterfaceLogsRemainVisibleToRiskControlIntegration(t *testing.T) {
	if os.Getenv("RISKCONTROL_INTEGRATION") != "1" {
		t.Skip("set RISKCONTROL_INTEGRATION=1 to run")
	}
	db := openRiskControlIntegrationDB(t)
	apiCode := "deleted-risk-" + strconv.FormatInt(time.Now().UnixNano(), 36)

	insert, err := db.Exec(`
		insert into bp_interface_api(api_code,name,request_url,request_method,remark)
		values(?,'Deleted Risk API','https://example.com/risk','GET','integration test')
	`, apiCode)
	if err != nil {
		t.Fatal(err)
	}
	interfaceID, err := insert.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec("delete from bp_interface_call_log where api_code = ?", apiCode)
		_, _ = db.Exec("delete from bp_interface_api where id = ?", interfaceID)
	})
	fingerprint := strings.Repeat("a", 64)
	for range 8 {
		if _, err := db.Exec(`
			insert into bp_interface_call_log(
				interface_id,user_id,api_code,request_method,request_params,
				request_fingerprint,response_status,success,charge_type,client_ip
			) values(?,1,?,'GET','{"page":1}',?,200,1,'FREE','203.0.113.10')
		`, interfaceID, apiCode, fingerprint); err != nil {
			t.Fatal(err)
		}
	}
	processRiskAnalytics(t, db)
	if _, err := db.Exec("delete from bp_interface_api where id = ?", interfaceID); err != nil {
		t.Fatal(err)
	}

	events, err := riskcontrol.NewRepository(db).RiskEvents(context.Background(), riskcontrol.RiskQuery{
		Limit:         20,
		WindowMinutes: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.EventType != "SINGLE_IP_CC" || event.InterfaceID != strconv.FormatInt(interfaceID, 10) {
			continue
		}
		if event.APICode != apiCode || event.InterfaceName != apiCode {
			t.Fatalf("deleted interface snapshot = name %q code %q", event.InterfaceName, event.APICode)
		}
		return
	}
	t.Fatalf("deleted-interface risk event not found: %#v", events)
}

func openRiskControlIntegrationDB(t *testing.T) *sql.DB {
	t.Helper()
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
	return db
}

func processRiskAnalytics(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := analytics.NewRepository(db).RunUntilCaughtUp(ctx, analytics.MaxBatchSize, 100)
	if err != nil {
		t.Fatalf("process analytics rollups before risk query: %v", err)
	}
	if result.HasMore {
		t.Fatalf("analytics rollups did not catch up after %d source rows", result.SourceRows)
	}
}
