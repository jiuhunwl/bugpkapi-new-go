package observability

import (
	"bugpk-api/internal/domain/auth"
	"bugpk-api/internal/domain/rbac"
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

type operationRows struct {
	data  [][]driver.Value
	width int
}

func (r *operationRows) Columns() []string {
	c := make([]string, r.width)
	for i := range c {
		c[i] = fmt.Sprint(i)
	}
	return c
}
func (r *operationRows) Close() error { return nil }
func (r *operationRows) Next(dest []driver.Value) error {
	if len(r.data) == 0 {
		return io.EOF
	}
	copy(dest, r.data[0])
	r.data = r.data[1:]
	return nil
}
func auditRows(rows ...[]driver.Value) driver.Rows {
	w := 1
	if len(rows) > 0 {
		w = len(rows[0])
	}
	return &operationRows{data: rows, width: w}
}
func TestOperationAdminScopeAppliesToRowsAndCount(t *testing.T) {
	repo, state := auditTestRepo(t)
	queries := 0
	state.query = func(q string, args []driver.NamedValue) (driver.Rows, error) {
		queries++
		if !strings.Contains(q, "l.actor_kind = 'admin'") || strings.Contains(q, "join bp_user") {
			t.Fatalf("admin scope missing or based on current roles: %s", q)
		}
		if strings.Contains(q, "count(*)") {
			return auditRows([]driver.Value{int64(1)}), nil
		}
		return &operationRows{width: 21}, nil
	}
	svc := NewService(repo, rbac.NewService(nil, nil))
	_, err := svc.AdminOperationLogs(context.Background(), rbac.Principal{ID: 1, Roles: []string{"admin"}}, OperationLogListParams{})
	if err != nil || queries != 2 {
		t.Fatalf("queries=%d err=%v", queries, err)
	}
}
func TestOperationMineAlwaysScopesCallerAndRejectsAnonymous(t *testing.T) {
	repo, state := auditTestRepo(t)
	queries := 0
	state.query = func(q string, args []driver.NamedValue) (driver.Rows, error) {
		queries++
		if !strings.Contains(q, "l.user_id = ?") || !strings.Contains(q, "l.actor_kind in ('admin','user')") || strings.Contains(q, "l.username like") {
			t.Fatalf("unsafe mine query: %s", q)
		}
		if len(args) == 0 || args[0].Value != int64(42) {
			t.Fatalf("caller filter spoofable: %v", args)
		}
		if strings.Contains(q, "count(*)") {
			return auditRows([]driver.Value{int64(1)}), nil
		}
		return &operationRows{width: 21}, nil
	}
	svc := NewService(repo, nil)
	page, err := svc.MyOperationLogs(context.Background(), rbac.Principal{ID: 42, Roles: []string{"user"}}, OperationLogListParams{UserID: 999, Username: "admin"})
	if err != nil || page.Total != 1 || queries != 2 {
		t.Fatalf("page=%+v queries=%d err=%v", page, queries, err)
	}
	if _, err = svc.MyOperationLogs(context.Background(), rbac.Principal{}, OperationLogListParams{}); err == nil || queries != 2 {
		t.Fatal("anonymous personal scope accepted")
	}
}
func TestOperationOverviewUsesSameAdminScopeAndPropagatesErrors(t *testing.T) {
	repo, state := auditTestRepo(t)
	queries := 0
	state.query = func(q string, args []driver.NamedValue) (driver.Rows, error) {
		queries++
		if !strings.Contains(q, "l.actor_kind = 'admin'") || !strings.Contains(q, "l.created_at >= ?") || strings.Contains(q, "bp_operation_metric_day") {
			t.Fatalf("unscoped overview: %s", q)
		}
		if strings.Contains(q, "count(distinct") {
			return auditRows([]driver.Value{int64(5), int64(2), int64(1), int64(3), int64(2)}), nil
		}
		return &operationRows{width: 3}, nil
	}
	stats, err := repo.OperationLogStats(context.Background())
	if err != nil || stats.TotalCount != 5 || stats.ActiveUsers != 2 || queries != 3 {
		t.Fatalf("stats=%+v queries=%d err=%v", stats, queries, err)
	}
	state.query = func(string, []driver.NamedValue) (driver.Rows, error) {
		return nil, errors.New("Error 1146: missing migration")
	}
	if _, err = repo.OperationLogStats(context.Background()); err == nil {
		t.Fatal("missing schema silently returned fake zero stats")
	}
}
func TestOperationActorClassificationSnapshot(t *testing.T) {
	for _, roles := range [][]string{{"admin"}, {"user"}, {"admin", "user"}} {
		repo, state := auditTestRepo(t)
		svc := NewService(repo, nil)
		// Event roles, not a DB join against today's membership.
		NewAuthAuditAdapter(svc).RecordAuth(context.Background(), auth.AuditEntry{UserID: 42, Username: "canonical", Roles: roles, Action: "login", Status: true})
		want := "user"
		for _, r := range roles {
			if r == "admin" {
				want = "admin"
			}
		}
		if state.writes != 1 || len(state.args) < 21 || state.args[20].Value != want {
			t.Fatalf("snapshot lost: %v", state.args)
		}
	}
}
