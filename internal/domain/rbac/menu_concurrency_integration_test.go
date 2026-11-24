package rbac

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"bugpk-api/internal/pkg/testdb"

	_ "github.com/go-sql-driver/mysql"
)

func TestConcurrentMenuUpdatesCannotCreateParentCycle(t *testing.T) {
	dsn := os.Getenv("BUGPK_RBAC_INTEGRATION_DSN")
	if dsn == "" {
		t.Skip("BUGPK_RBAC_INTEGRATION_DSN is not configured")
	}
	if err := testdb.ValidateIsolatedMySQLDSN(dsn); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open integration database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(8)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping integration database: %v", err)
	}

	repo := NewRepository(db)
	suffix := time.Now().UnixNano()
	firstName := fmt.Sprintf("CycleFirst%d", suffix)
	secondName := fmt.Sprintf("CycleSecond%d", suffix)
	firstPath := fmt.Sprintf("/cycle-first-%d", suffix)
	secondPath := fmt.Sprintf("/cycle-second-%d", suffix)
	baseMenu := func(name, path string) Menu {
		return Menu{Name: name, Path: path, Type: "menu", Meta: map[string]any{"title": name}, Status: 1}
	}
	if err := repo.CreateMenu(ctx, baseMenu(firstName, firstPath), nil); err != nil {
		t.Fatalf("create first menu: %v", err)
	}
	if err := repo.CreateMenu(ctx, baseMenu(secondName, secondPath), nil); err != nil {
		t.Fatalf("create second menu: %v", err)
	}
	var firstID, secondID int64
	if err := db.QueryRowContext(ctx, `select id from bp_menu where name=?`, firstName).Scan(&firstID); err != nil {
		t.Fatalf("query first menu: %v", err)
	}
	if err := db.QueryRowContext(ctx, `select id from bp_menu where name=?`, secondName).Scan(&secondID); err != nil {
		t.Fatalf("query second menu: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`delete from bp_role_menu where menu_id in (?,?)`, firstID, secondID)
		_, _ = db.Exec(`delete from bp_menu where id in (?,?)`, firstID, secondID)
	})

	first := baseMenu(firstName, firstPath)
	first.PID = secondID
	second := baseMenu(secondName, secondPath)
	second.PID = firstID
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		results <- repo.UpdateMenu(ctx, firstID, first, nil)
	}()
	go func() {
		defer wg.Done()
		<-start
		results <- repo.UpdateMenu(ctx, secondID, second, nil)
	}()
	close(start)
	wg.Wait()
	close(results)

	var successes, rejected int
	for updateErr := range results {
		if updateErr == nil {
			successes++
		} else {
			rejected++
		}
	}
	if successes != 1 || rejected != 1 {
		t.Fatalf("menu update results success=%d rejected=%d", successes, rejected)
	}
	var firstPID, secondPID int64
	if err := db.QueryRowContext(ctx, `select pid from bp_menu where id=?`, firstID).Scan(&firstPID); err != nil {
		t.Fatalf("query first parent: %v", err)
	}
	if err := db.QueryRowContext(ctx, `select pid from bp_menu where id=?`, secondID).Scan(&secondPID); err != nil {
		t.Fatalf("query second parent: %v", err)
	}
	if firstPID == secondID && secondPID == firstID {
		t.Fatal("concurrent updates created a parent cycle")
	}
}

func TestRepositoryProtectsAdminRoleFromDirectMutation(t *testing.T) {
	dsn := os.Getenv("BUGPK_RBAC_INTEGRATION_DSN")
	if dsn == "" {
		t.Skip("BUGPK_RBAC_INTEGRATION_DSN is not configured")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open integration database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	repo := NewRepository(db)
	var adminRoleID int64
	if err := db.QueryRowContext(ctx, `select id from bp_role where role_key='admin'`).Scan(&adminRoleID); err != nil {
		t.Fatalf("query admin role: %v", err)
	}
	if err := repo.UpdateRole(ctx, adminRoleID, Role{Name: "Admin", RoleKey: "admin", Status: 0}); !errors.Is(err, ErrProtectedAdminRole) {
		t.Fatalf("admin role disable error = %v", err)
	}
	if err := repo.DeleteRole(ctx, adminRoleID); !errors.Is(err, ErrProtectedAdminRole) {
		t.Fatalf("admin role delete error = %v", err)
	}
}
