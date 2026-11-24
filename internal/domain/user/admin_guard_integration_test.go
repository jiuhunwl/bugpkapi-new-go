package user

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

func TestConcurrentAdminRemovalCannotRemoveLastActiveAdmin(t *testing.T) {
	dsn := os.Getenv("BUGPK_USER_INTEGRATION_DSN")
	if dsn == "" {
		t.Skip("BUGPK_USER_INTEGRATION_DSN is not configured")
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

	var adminRoleID, userRoleID int64
	if err := db.QueryRowContext(ctx, `select id from bp_role where role_key='admin'`).Scan(&adminRoleID); err != nil {
		t.Fatalf("query admin role: %v", err)
	}
	if err := db.QueryRowContext(ctx, `select id from bp_role where role_key='user'`).Scan(&userRoleID); err != nil {
		t.Fatalf("query user role: %v", err)
	}
	existingAdminStatus := map[int64]int{}
	rows, err := db.QueryContext(ctx, `
		select distinct u.id,u.status
		from bp_user u
		inner join bp_user_role ur on ur.user_id=u.id
		where ur.role_id=?
	`, adminRoleID)
	if err != nil {
		t.Fatalf("snapshot existing admins: %v", err)
	}
	for rows.Next() {
		var userID int64
		var status int
		if err := rows.Scan(&userID, &status); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		existingAdminStatus[userID] = status
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for userID, status := range existingAdminStatus {
			_, _ = db.Exec(`update bp_user set status=? where id=?`, status, userID)
		}
	})
	if _, err := db.ExecContext(ctx, `
		update bp_user u
		inner join bp_user_role ur on ur.user_id=u.id
		set u.status=0
		where ur.role_id=?
	`, adminRoleID); err != nil {
		t.Fatalf("disable existing admins: %v", err)
	}

	suffix := time.Now().UnixNano()
	createAdmin := func(label string) int64 {
		username := fmt.Sprintf("guard_%s_%d", label, suffix)
		result, err := db.ExecContext(ctx, `
			insert into bp_user(username,password_hash,display_name,home_path,balance,points,status)
			values(?, '$2a$10$integration.test.hash.placeholder', ?, '/console/workspace', 0.0000, 0, 1)
		`, username, username)
		if err != nil {
			t.Fatalf("insert %s admin: %v", label, err)
		}
		userID, err := result.LastInsertId()
		if err != nil {
			t.Fatalf("%s admin id: %v", label, err)
		}
		if _, err := db.ExecContext(ctx, `insert into bp_user_role(user_id,role_id) values(?,?)`, userID, adminRoleID); err != nil {
			t.Fatalf("grant %s admin role: %v", label, err)
		}
		return userID
	}
	firstID := createAdmin("first")
	secondID := createAdmin("second")
	t.Cleanup(func() {
		_, _ = db.Exec(`delete from bp_user_role where user_id in (?,?)`, firstID, secondID)
		_, _ = db.Exec(`delete from bp_user where id in (?,?)`, firstID, secondID)
	})

	repo := NewRepository(db)
	status := 1
	points := int64(0)
	updateRequest := SaveUserRequest{
		Balance:       "0.0000",
		PrimaryRoleID: userRoleID,
		HomePath:      "/console/workspace",
		Points:        &points,
		RealName:      fmt.Sprintf("guard_first_%d", suffix),
		RoleIDs:       []int64{userRoleID},
		Status:        &status,
		Username:      fmt.Sprintf("guard_first_%d", suffix),
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		results <- repo.UpdateUser(ctx, 1, firstID, updateRequest, nil, 1)
	}()
	go func() {
		defer wg.Done()
		<-start
		results <- repo.DeleteUser(ctx, secondID)
	}()
	close(start)
	wg.Wait()
	close(results)

	var successes, lastAdminRejected int
	for operationErr := range results {
		switch {
		case operationErr == nil:
			successes++
		case errors.Is(operationErr, ErrLastAdmin):
			lastAdminRejected++
		default:
			t.Fatalf("unexpected admin operation error: %v", operationErr)
		}
	}
	if successes != 1 || lastAdminRejected != 1 {
		t.Fatalf("admin operation results success=%d rejected=%d", successes, lastAdminRejected)
	}
	activeAdmins, err := repo.CountActiveAdmins(ctx)
	if err != nil {
		t.Fatalf("count active admins: %v", err)
	}
	if activeAdmins != 1 {
		t.Fatalf("active admins=%d, want 1", activeAdmins)
	}
}
