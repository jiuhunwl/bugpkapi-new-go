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

func TestPackageContractRoundTripIntegration(t *testing.T) {
	dsn := os.Getenv("BUGPK_PACKAGE_CONTRACT_INTEGRATION_DSN")
	if dsn == "" {
		t.Skip("BUGPK_PACKAGE_CONTRACT_INTEGRATION_DSN is not configured")
	}
	if err := testdb.ValidateIsolatedMySQLDSN(dsn); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}

	suffix := time.Now().UnixNano()
	apiCode := fmt.Sprintf("package-contract-%d", suffix)
	apiResult, err := db.ExecContext(ctx, `
		insert into bp_interface_api(api_code,name,avatar_url,request_url,request_method,remark,status)
		values(?,?,'https://example.com/avatar.png','https://example.com/upstream','POST','interface remark',1)
	`, apiCode, "Package Contract API")
	if err != nil {
		t.Fatal(err)
	}
	interfaceID, err := apiResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	repo := NewRepository(db)
	globalName := fmt.Sprintf("Global Contract %d", suffix)
	pointName := fmt.Sprintf("Point Contract %d", suffix)
	interfaceName := fmt.Sprintf("Interface Contract %d", suffix)

	if err := repo.SaveGlobal(ctx, 0, GlobalPackage{Name: globalName, Price: "1.0000", ValidDays: 30, Status: 1}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SavePoint(ctx, 0, PointPackage{Name: pointName, Price: "2.0000", Points: 200, Status: 1}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveInterface(ctx, 0, InterfacePackage{
		InterfaceID: fmt.Sprint(interfaceID),
		Name:        interfaceName,
		Remark:      "package remark",
		Status:      1,
		Specs: []InterfacePackageSpec{
			{SpecName: "premium", Price: "20.0000", ValidDays: 30, Status: 1, Remark: "second", SortNo: 20},
			{SpecName: "starter", Price: "9.9000", ValidDays: 7, Status: 1, Remark: "first", SortNo: 10},
		},
	}); err != nil {
		t.Fatal(err)
	}

	var globalID, pointID, interfacePackageID int64
	if err := db.QueryRowContext(ctx, `select id from bp_package_global where name=?`, globalName).Scan(&globalID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `select id from bp_package_point where name=?`, pointName).Scan(&pointID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `select id from bp_package_interface where name=?`, interfaceName).Scan(&interfacePackageID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec("delete from bp_package_interface_spec where package_id=?", interfacePackageID)
		_, _ = db.Exec("delete from bp_package_interface where id=?", interfacePackageID)
		_, _ = db.Exec("delete from bp_package_global where id=?", globalID)
		_, _ = db.Exec("delete from bp_package_point where id=?", pointID)
		_, _ = db.Exec("delete from bp_interface_api where id=?", interfaceID)
	})

	globals, err := repo.ListGlobal(ctx, ListParams{Keyword: globalName, Page: 1, PageSize: 10}, false, nil)
	if err != nil || len(globals.Items) != 1 {
		t.Fatalf("global list: %#v err=%v", globals, err)
	}
	if globals.Items[0].CreateTime == "" || globals.Items[0].UpdateTime == "" {
		t.Fatalf("global timestamps missing: %#v", globals.Items[0])
	}

	points, err := repo.ListPoint(ctx, ListParams{Keyword: pointName, Page: 1, PageSize: 10}, false, nil)
	if err != nil || len(points.Items) != 1 {
		t.Fatalf("point list: %#v err=%v", points, err)
	}
	if points.Items[0].CreateTime == "" || points.Items[0].UpdateTime == "" || points.Items[0].PointAmount != 200 {
		t.Fatalf("point contract mismatch: %#v", points.Items[0])
	}

	interfaces, err := repo.ListInterface(ctx, ListParams{Keyword: interfaceName, Page: 1, PageSize: 10}, false, nil)
	if err != nil || len(interfaces.Items) != 1 {
		t.Fatalf("interface list: %#v err=%v", interfaces, err)
	}
	item := interfaces.Items[0]
	if item.APICode != apiCode || item.AvatarURL == "" || item.RequestMethod != "POST" || item.InterfaceRemark != "interface remark" {
		t.Fatalf("interface metadata mismatch: %#v", item)
	}
	if item.CreateTime == "" || item.UpdateTime == "" || item.SpecCount != 2 || item.MinPrice != "9.9000" {
		t.Fatalf("interface aggregate contract mismatch: %#v", item)
	}
	if len(item.Specs) != 2 || item.Specs[0].SpecName != "starter" || item.Specs[0].Remark != "first" || item.Specs[0].SortNo != 10 {
		t.Fatalf("sorted spec contract mismatch: %#v", item.Specs)
	}
	if item.Specs[0].PackageID != fmt.Sprint(interfacePackageID) || item.Specs[0].CreateTime == "" || item.Specs[0].UpdateTime == "" {
		t.Fatalf("spec identity/timestamps missing: %#v", item.Specs[0])
	}

	detail, err := repo.InterfaceByID(ctx, interfacePackageID, true, nil)
	if err != nil {
		t.Fatalf("enabled interface package detail: %v", err)
	}
	if detail.ID != fmt.Sprint(interfacePackageID) || detail.APICode != apiCode || len(detail.Specs) != 2 || detail.MinPrice != "9.9000" {
		t.Fatalf("interface package detail contract mismatch: %#v", detail)
	}
	if _, err := db.ExecContext(ctx, `update bp_package_interface set status=0 where id=?`, interfacePackageID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.InterfaceByID(ctx, interfacePackageID, true, nil); err != sql.ErrNoRows {
		t.Fatalf("disabled mall package detail error = %v, want sql.ErrNoRows", err)
	}
}
