package interfaceapi_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	"bugpk-api/internal/app"
	"bugpk-api/internal/config"
	"bugpk-api/internal/domain/gateway"
	"bugpk-api/internal/domain/interfaceapi"
	"bugpk-api/internal/domain/observability"
	"bugpk-api/internal/domain/payment"
	"bugpk-api/internal/pkg/testdb"

	_ "github.com/go-sql-driver/mysql"
)

func TestInterfaceDeleteProtectsSoldRightsAndCleansDependenciesIntegration(t *testing.T) {
	db := openInterfaceDeleteIntegrationDB(t)
	ctx := context.Background()
	const apiCode = "delete-guard-api"
	interfaceID, packageID, specID := insertInterfacePackageFixture(t, db, apiCode)

	keyResult, err := db.Exec(`
		insert into bp_api_key(user_id,name,key_hash,key_prefix,key_suffix,key_type,status)
		values(1,'Delete Guard Key',?,'bp_live_guard','guard','INTERFACE',1)
	`, strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	keyID, err := keyResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("insert into bp_api_key_interface(key_id,interface_id) values(?,?)", keyID, interfaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		insert into bp_interface_billing_rule(interface_id,field_name,operator,expected_value,sort_no)
		values(?,'code','EQ','0',1)
	`, interfaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		insert into bp_interface_call_log(interface_id,user_id,api_code,request_method,response_status,success,charge_type)
		values(?,1,?,'GET',200,1,'FREE')
	`, interfaceID, apiCode); err != nil {
		t.Fatal(err)
	}
	if _, err := gateway.NewRepository(db).CreateOpenLog(ctx, gateway.OpenResult{
		APICode:        apiCode,
		Body:           `{"accessToken":"raw-token","data":"ok"}`,
		BillingStatus:  "CONFIRMED",
		ChargeAmount:   "0.0000",
		ChargeType:     "FREE",
		InterfaceID:    interfaceID,
		RequestMethod:  "GET",
		ResponseSize:   int64(len(`{"accessToken":"raw-token","data":"ok"}`)),
		ResponseStatus: 200,
		Success:        true,
		UserID:         1,
		UserVisible:    true,
	}); err != nil {
		t.Fatal(err)
	}
	managementBody := strings.Repeat("x", 20001)
	if err := interfaceapi.NewRepository(db).CreateCallLog(ctx,
		interfaceapi.InterfaceItem{ID: strconv.FormatInt(interfaceID, 10), APICode: apiCode},
		interfaceapi.Principal{ID: 1},
		0,
		"",
		interfaceapi.InvokeResult{
			Body:          managementBody,
			RequestMethod: "GET",
			ResponseSize:  int64(len(managementBody)),
			StatusCode:    200,
			Success:       true,
		}, "", "", "", "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	entitlement, err := db.Exec(`
		insert into bp_user_package_interface(
			user_id,interface_id,package_id,spec_id,package_name,spec_name,
			daily_limit,qps_limit,start_at,end_at,status
		) values(1,?,?,?,'Guard Package','Guard Spec',100,10,now(),date_add(now(),interval 1 day),1)
	`, interfaceID, packageID, specID)
	if err != nil {
		t.Fatal(err)
	}
	entitlementID, err := entitlement.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	repo := interfaceapi.NewRepository(db)
	if err := repo.Delete(ctx, interfaceID); !errors.Is(err, interfaceapi.ErrInterfaceHasActiveEntitlements) {
		t.Fatalf("delete with active entitlement error = %v", err)
	}
	assertCount(t, db, "select count(*) from bp_interface_api where id=?", 1, interfaceID)

	if _, err := db.Exec("update bp_user_package_interface set status=0,end_at=date_sub(now(),interval 1 minute) where id=?", entitlementID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		insert into bp_payment_order(
			order_no,user_id,order_type,product,subject,target_id,pay_amount,status,expire_at,snapshot
		) values('BP_DELETE_GUARD',1,'INTERFACE_PACKAGE','PAGE','Guard pending order',?,10.0000,'PAYING',date_add(now(),interval 30 minute),json_object())
	`, specID); err != nil {
		t.Fatal(err)
	}
	if err := repo.Delete(ctx, interfaceID); !errors.Is(err, interfaceapi.ErrInterfaceHasPendingPayments) {
		t.Fatalf("delete with pending payment error = %v", err)
	}
	assertCount(t, db, "select count(*) from bp_interface_api where id=?", 1, interfaceID)

	if _, err := db.Exec("update bp_payment_order set status='CANCELLED' where order_no='BP_DELETE_GUARD'"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Delete(ctx, interfaceID); err != nil {
		t.Fatal(err)
	}
	assertCount(t, db, "select count(*) from bp_interface_api where id=?", 0, interfaceID)
	assertCount(t, db, "select count(*) from bp_api_key_interface where interface_id=?", 0, interfaceID)
	assertCount(t, db, "select count(*) from bp_package_interface where id=?", 0, packageID)
	assertCount(t, db, "select count(*) from bp_package_interface_spec where id=?", 0, specID)
	assertCount(t, db, "select count(*) from bp_interface_billing_rule where interface_id=?", 0, interfaceID)
	assertCount(t, db, "select count(*) from bp_interface_call_log where interface_id=?", 3, interfaceID)
	assertCount(t, db, "select count(*) from bp_interface_call_log where interface_id=? and response_truncated=1", 1, interfaceID)
	assertCount(t, db, "select count(*) from bp_interface_call_log_body b join bp_interface_call_log l on l.id=b.log_id where l.interface_id=? and b.response_body like '%raw-token%'", 0, interfaceID)
	assertCount(t, db, "select count(*) from bp_user_package_interface where id=?", 1, entitlementID)

	logs, err := observability.NewRepository(db).ListCallLogs(ctx, observability.CallLogListParams{
		InterfaceID: interfaceID,
		Page:        1,
		PageSize:    10,
	}, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if logs.Total != 3 || len(logs.Items) != 3 {
		t.Fatalf("deleted interface history disappeared from observability: %+v", logs)
	}
	var foundMasked, foundTruncated bool
	for _, item := range logs.Items {
		if item.APICode != apiCode {
			t.Fatalf("deleted interface log lost apiCode snapshot: %+v", item)
		}
		switch {
		case item.ResponseSize == int64(len(managementBody)):
			foundTruncated = item.ResponseSaved == 20000 && item.ResponseTruncated
		case item.ResponseSize == int64(len(`{"accessToken":"raw-token","data":"ok"}`)):
			foundMasked = item.ResponseSaved > 0 && item.ResponseSaved < item.ResponseSize && !item.ResponseTruncated
		}
	}
	if !foundMasked || !foundTruncated {
		t.Fatalf("response size metadata is inaccurate after masking/truncation: %+v", logs.Items)
	}
}

func TestStaleInterfacePackageSeedCannotCreatePaymentOrderIntegration(t *testing.T) {
	db := openInterfaceDeleteIntegrationDB(t)
	ctx := context.Background()
	interfaceID, _, specID := insertInterfacePackageFixture(t, db, "stale-payment-api")
	paymentRepo := payment.NewRepository(db)
	seed, err := paymentRepo.BuildInterfacePackageOrder(ctx, 1, specID, "BP_STALE_INTERFACE", "PAGE")
	if err != nil {
		t.Fatal(err)
	}
	if err := interfaceapi.NewRepository(db).Delete(ctx, interfaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := paymentRepo.CreateOrder(ctx, seed); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("create order from stale interface-package seed error = %v", err)
	}
	assertCount(t, db, "select count(*) from bp_payment_order where order_no='BP_STALE_INTERFACE'", 0)
}

func openInterfaceDeleteIntegrationDB(t *testing.T) *sql.DB {
	t.Helper()
	if os.Getenv("INTERFACE_DELETE_INTEGRATION") != "1" {
		t.Skip("set INTERFACE_DELETE_INTEGRATION=1 to run")
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
	return db
}

func insertInterfacePackageFixture(t *testing.T, db *sql.DB, apiCode string) (int64, int64, int64) {
	t.Helper()
	result, err := db.Exec(`
		insert into bp_interface_api(api_code,name,request_url,request_method,remark)
		values(?,?,'https://example.com/delete','GET','delete lifecycle integration')
	`, apiCode, apiCode)
	if err != nil {
		t.Fatal(err)
	}
	interfaceID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	result, err = db.Exec("insert into bp_package_interface(interface_id,name,status) values(?,'Guard Package',1)", interfaceID)
	if err != nil {
		t.Fatal(err)
	}
	packageID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	result, err = db.Exec(`
		insert into bp_package_interface_spec(package_id,name,price,valid_days,daily_limit,qps_limit,status)
		values(?,'Guard Spec',10.0000,30,100,10,1)
	`, packageID)
	if err != nil {
		t.Fatal(err)
	}
	specID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return interfaceID, packageID, specID
}

func assertCount(t *testing.T, db *sql.DB, query string, want int64, args ...any) {
	t.Helper()
	var got int64
	if err := db.QueryRow(query, args...).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("count for %q = %d, want %d", query, got, want)
	}
}
