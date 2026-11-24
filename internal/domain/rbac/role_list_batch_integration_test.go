package rbac

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

func TestListRolesLoadsPermissionsWithSingleDatabaseConnection(t *testing.T) {
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
	var menuIDs []int64
	menuRows, err := db.QueryContext(ctx, "select id from bp_menu order by id limit 2")
	if err != nil {
		t.Fatal(err)
	}
	for menuRows.Next() {
		var id int64
		if err := menuRows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		menuIDs = append(menuIDs, id)
	}
	if err := menuRows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(menuIDs) != 2 {
		t.Fatalf("default menu count = %d, want at least 2", len(menuIDs))
	}

	prefix := fmt.Sprintf("batch_role_%d", time.Now().UnixNano())
	var roleIDs []int64
	t.Cleanup(func() {
		if len(roleIDs) == 0 {
			return
		}
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(roleIDs)), ",")
		args := make([]any, len(roleIDs))
		for index, id := range roleIDs {
			args[index] = id
		}
		_, _ = db.Exec("delete from bp_role_menu where role_id in ("+placeholders+")", args...)
		_, _ = db.Exec("delete from bp_role where id in ("+placeholders+")", args...)
	})

	for index := range 3 {
		roleKey := fmt.Sprintf("%s_%d", prefix, index)
		result, err := db.ExecContext(ctx, `
			insert into bp_role(role_key,role_name,remark,status)
			values(?,?,?,1)
		`, roleKey, roleKey, "batch role list test")
		if err != nil {
			t.Fatal(err)
		}
		roleID, err := result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		roleIDs = append(roleIDs, roleID)
		for permissionIndex := 0; permissionIndex <= index%2; permissionIndex++ {
			if _, err := db.ExecContext(ctx, "insert into bp_role_menu(role_id,menu_id) values(?,?)", roleID, menuIDs[permissionIndex]); err != nil {
				t.Fatal(err)
			}
		}
	}

	repo := NewRepository(db)
	result, err := repo.ListRoles(ctx, RoleListParams{Name: prefix, Page: 1, PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	roles := result.Items
	if len(roles) != 3 {
		t.Fatalf("role count = %d, want 3", len(roles))
	}
	if result.Total != 3 {
		t.Fatalf("role total = %d, want 3", result.Total)
	}
	for _, role := range roles {
		if len(role.Permissions) < 1 || len(role.Permissions) > 2 {
			t.Fatalf("permissions not batch loaded for %s: %v", role.RoleKey, role.Permissions)
		}
		for index := 1; index < len(role.Permissions); index++ {
			if role.Permissions[index-1] >= role.Permissions[index] {
				t.Fatalf("permissions are not stably sorted for %s: %v", role.RoleKey, role.Permissions)
			}
		}
	}
}
