package auth_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"bugpk-api/internal/app"
	"bugpk-api/internal/config"
	"bugpk-api/internal/domain/auth"
	"bugpk-api/internal/pkg/testdb"

	_ "github.com/go-sql-driver/mysql"
)

func TestIntegrationRegistrationIgnoresClientRoleID(t *testing.T) {
	if os.Getenv("AUTH_REGISTRATION_ROLE_INTEGRATION") != "1" {
		t.Skip("set AUTH_REGISTRATION_ROLE_INTEGRATION=1 to run")
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

	ctx := context.Background()
	var userRoleID, adminRoleID int64
	if err := db.QueryRowContext(ctx, `select id from bp_role where role_key = 'user' limit 1`).Scan(&userRoleID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `select id from bp_role where role_key = 'admin' limit 1`).Scan(&adminRoleID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		update bp_role
		set status = 1, self_register_enabled = 1
		where id = ?
	`, userRoleID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		update bp_auth_register_config
		set default_role_id = ?
		where id = 1
	`, userRoleID); err != nil {
		t.Fatal(err)
	}

	customRoleKey := fmt.Sprintf("registration_forged_%d", time.Now().UnixNano())
	customRoleResult, err := db.ExecContext(ctx, `
		insert into bp_role(role_key, role_name, remark, qps_limit, self_register_enabled, status)
		values (?, 'Forged registration role', 'integration test only', 999, 1, 1)
	`, customRoleKey)
	if err != nil {
		t.Fatal(err)
	}
	customRoleID, err := customRoleResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `delete from bp_role where id = ?`, customRoleID)
	})

	repo := auth.NewRepository(db)
	for name, forgedRoleID := range map[string]int64{
		"administrator":            adminRoleID,
		"other self-register role": customRoleID,
	} {
		t.Run(name, func(t *testing.T) {
			username := fmt.Sprintf("role_guard_%d", time.Now().UnixNano())
			created, err := repo.CreateLocalUser(ctx, auth.RegisterRequest{
				Username: username,
				RoleID:   strconv.FormatInt(forgedRoleID, 10),
			}, "$2a$10$integration.placeholder.hash", 0, []byte(fmt.Sprintf("rg-ip-%d", time.Now().UnixNano())))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = db.ExecContext(context.Background(), `delete from bp_register_ip_used where user_id = ?`, created.ID)
				_, _ = db.ExecContext(context.Background(), `delete from bp_user_role where user_id = ?`, created.ID)
				_, _ = db.ExecContext(context.Background(), `delete from bp_user where id = ?`, created.ID)
			})

			var primaryRoleID int64
			if err := db.QueryRowContext(ctx, `select primary_role_id from bp_user where id = ?`, created.ID).Scan(&primaryRoleID); err != nil {
				t.Fatal(err)
			}
			if primaryRoleID != userRoleID {
				t.Fatalf("forged roleId %d assigned primary role %d, want server default %d", forgedRoleID, primaryRoleID, userRoleID)
			}
			var assignedRoleID int64
			if err := db.QueryRowContext(ctx, `select role_id from bp_user_role where user_id = ?`, created.ID).Scan(&assignedRoleID); err != nil {
				t.Fatal(err)
			}
			if assignedRoleID != userRoleID {
				t.Fatalf("forged roleId %d assigned role relation %d, want server default %d", forgedRoleID, assignedRoleID, userRoleID)
			}
		})
	}
}

func TestIntegrationRegistrationRejectsInvalidServerDefaultRole(t *testing.T) {
	if os.Getenv("AUTH_REGISTRATION_ROLE_INTEGRATION") != "1" {
		t.Skip("set AUTH_REGISTRATION_ROLE_INTEGRATION=1 to run")
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

	ctx := context.Background()
	var userRoleID, adminRoleID int64
	if err := db.QueryRowContext(ctx, `select id from bp_role where role_key = 'user' limit 1`).Scan(&userRoleID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `select id from bp_role where role_key = 'admin' limit 1`).Scan(&adminRoleID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `update bp_auth_register_config set default_role_id = ? where id = 1`, adminRoleID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `update bp_auth_register_config set default_role_id = ? where id = 1`, userRoleID)
	})

	repo := auth.NewRepository(db)
	username := fmt.Sprintf("invalid_default_guard_%d", time.Now().UnixNano())
	_, err = repo.CreateLocalUser(ctx, auth.RegisterRequest{
		Username: username,
		RoleID:   strconv.FormatInt(userRoleID, 10),
	}, "$2a$10$integration.placeholder.hash", 0, nil)
	if !errors.Is(err, auth.ErrRegistrationRoleInvalid) {
		t.Fatalf("invalid administrator default error = %v, want ErrRegistrationRoleInvalid", err)
	}
	var created int
	if err := db.QueryRowContext(ctx, `select exists(select 1 from bp_user where username = ?)`, username).Scan(&created); err != nil {
		t.Fatal(err)
	}
	if created != 0 {
		t.Fatal("registration transaction created a user with an invalid server default role")
	}
}
