package packagepkg_test

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"bugpk-api/internal/app"
	"bugpk-api/internal/config"
	"bugpk-api/internal/domain/packagepkg"
	"bugpk-api/internal/domain/rbac"
	"bugpk-api/internal/pkg/testdb"

	_ "github.com/go-sql-driver/mysql"
)

func TestAdminUserBillingEndpointRemainsSelfScopedIntegration(t *testing.T) {
	if os.Getenv("PACKAGE_SCOPE_INTEGRATION") != "1" {
		t.Skip("set PACKAGE_SCOPE_INTEGRATION=1 to run")
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

	userResult, err := db.Exec(`
		insert into bp_user(username,password_hash,display_name,status)
		values('billing_scope_user','$2a$10$integration.placeholder.hash','Billing Scope User',1)
	`)
	if err != nil {
		t.Fatal(err)
	}
	otherUserID, err := userResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	apiResult, err := db.Exec(`
		insert into bp_interface_api(api_code,name,request_url,request_method,remark)
		values('billing-scope-api','Billing Scope API','https://example.com/billing','GET','integration test')
	`)
	if err != nil {
		t.Fatal(err)
	}
	interfaceID, err := apiResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	for _, userID := range []int64{1, otherUserID} {
		if _, err := db.Exec(`
			insert into bp_billing_record(user_id,interface_id,api_code,charge_type,status)
			values(?,?,'billing-scope-api','FREE','CONFIRMED')
		`, userID, interfaceID); err != nil {
			t.Fatal(err)
		}
	}

	authz := rbac.NewService(rbac.NewRepository(db), nil)
	service := packagepkg.NewService(packagepkg.NewRepository(db), authz)
	result, err := service.BillingRecords(context.Background(), packagepkg.Principal{
		ID:       1,
		Roles:    []string{"admin"},
		Username: "admin",
	}, packagepkg.ListParams{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || len(result.Items) != 1 || result.Items[0].UserID != "1" {
		t.Fatalf("admin self-scoped billing result = %#v", result)
	}
}
