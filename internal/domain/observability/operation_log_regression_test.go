package observability

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"bugpk-api/internal/domain/auth"
	"bugpk-api/internal/domain/rbac"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
)

type auditCapture struct {
	args       []driver.NamedValue
	writes     int
	deadline   time.Time
	contextErr error
	fail       bool
	block      bool
	query      func(string, []driver.NamedValue) (driver.Rows, error)
	// lastInsertID 模拟自增主键；区域异步补全依赖 InsertOperationLog 回传 ID。
	lastInsertID int64
	// updates 捕获 update bp_operation_log 的参数，供区域回填断言。
	updates []driver.NamedValue
	// execQueries 记录所有 ExecContext 的 SQL 文本（含 update），供 SQL 契约断言。
	execQueries []string
}
type auditDriver struct{ state *auditCapture }
type auditConn struct{ state *auditCapture }

// auditResult 让测试驱动能返回自增主键（driver.RowsAffected 恒返回 error）。
type auditResult struct {
	affected int64
	lastID   int64
}

func (r auditResult) LastInsertId() (int64, error) { return r.lastID, nil }
func (r auditResult) RowsAffected() (int64, error) { return r.affected, nil }

var auditDriverID atomic.Uint64

func (d auditDriver) Open(string) (driver.Conn, error)   { return &auditConn{d.state}, nil }
func (c *auditConn) Close() error                        { return nil }
func (c *auditConn) Begin() (driver.Tx, error)           { return nil, errors.New("unsupported") }
func (c *auditConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unsupported") }
func (c *auditConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.state.execQueries = append(c.state.execQueries, query)
	if strings.Contains(query, "update bp_operation_log") {
		c.state.updates = append([]driver.NamedValue(nil), args...)
		return auditResult{affected: 1}, nil
	}
	if !strings.Contains(query, "insert into bp_operation_log") {
		return driver.RowsAffected(1), nil
	}
	c.state.deadline, _ = ctx.Deadline()
	c.state.contextErr = ctx.Err()
	if c.state.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if c.state.fail {
		return nil, errors.New("audit database unavailable")
	}
	c.state.args = append([]driver.NamedValue(nil), args...)
	detail := args[19].Value
	if detail != nil {
		s, ok := detail.(string)
		if !ok || !json.Valid([]byte(s)) {
			return nil, errors.New("invalid JSON in detail column")
		}
	}
	c.state.writes++
	return auditResult{affected: 1, lastID: c.state.lastInsertID}, nil
}
func (c *auditConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if c.state.query == nil {
		return nil, fmt.Errorf("unexpected query: %s", q)
	}
	return c.state.query(q, args)
}
func auditTestRepo(t *testing.T) (*Repository, *auditCapture) {
	t.Helper()
	state := &auditCapture{}
	name := fmt.Sprintf("operation-audit-%d", auditDriverID.Add(1))
	sql.Register(name, auditDriver{state})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return NewRepository(db), state
}
func TestOperationLogDetailAlwaysValidJSONOrNull(t *testing.T) {
	for _, detail := range []string{"", "  ", "{bad", `{"hello":"世界"}`, `{"text":"` + strings.Repeat("中", 4000) + `"}`} {
		t.Run(fmt.Sprintf("bytes-%d", len(detail)), func(t *testing.T) {
			repo, state := auditTestRepo(t)
			if _, err := repo.InsertOperationLog(context.Background(), OperationLogRecord{Detail: detail}); err != nil {
				t.Fatal(err)
			}
			if state.writes != 1 {
				t.Fatal("audit entry was lost")
			}
			if json.Valid([]byte(detail)) && len(detail) <= maxOperationLogDetailLen && state.args[19].Value != detail {
				t.Fatal("valid detail changed")
			}
		})
	}
}
func TestOperationAuditWritesAndSkipsReadRequests(t *testing.T) {
	for _, tc := range []struct {
		method, path   string
		status, writes int
	}{
		{"POST", "/system-api/user", 200, 1}, {"DELETE", "/system-api/user/1", 403, 1},
		{"GET", "/system-api/user", 200, 0}, {"POST", "/system-api/system/operation-log/list", 200, 0},
		{"POST", "/api/demo", 200, 0},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			repo, state := auditTestRepo(t)
			svc := NewService(repo, nil)
			h := server.New()
			group := h.Group("/system-api")
			group.Use(auditActor(rbac.Principal{ID: 42, Username: "tester", Roles: []string{"admin"}}), svc.OperationAuditMiddleware())
			handler := func(_ context.Context, c *hertzapp.RequestContext) {
				c.JSON(tc.status, map[string]string{"token": "do-not-persist-response"})
			}
			if strings.HasPrefix(tc.path, "/system-api") {
				group.Handle(tc.method, strings.TrimPrefix(tc.path, "/system-api"), handler)
			} else {
				h.Handle(tc.method, tc.path, handler)
			}
			resp := ut.PerformRequest(h.Engine, tc.method, tc.path, nil)
			if resp.Code != tc.status {
				t.Fatalf("response changed: %d", resp.Code)
			}
			if state.writes != tc.writes {
				t.Fatalf("writes=%d want=%d", state.writes, tc.writes)
			}
			if tc.status == 200 && state.writes > 0 && state.args[10].Value != "" {
				t.Fatal("successful response leaked into audit error_message")
			}
		})
	}
}

func TestOperationAuditErrorTruncationPreservesUTF8(t *testing.T) {
	msg := truncateMessage(strings.Repeat("错", 300))
	if len(msg) > maxAuditMessageLen || !utf8.ValidString(msg) {
		t.Fatal("audit error is invalid UTF-8 or oversized")
	}
}
func TestOperationAuthAdapterPersistsWithoutDetail(t *testing.T) {
	repo, state := auditTestRepo(t)
	NewAuthAuditAdapter(NewService(repo, nil)).RecordAuth(context.Background(), auth.AuditEntry{UserID: 42, Username: "tester", Action: "login", Status: true})
	if state.writes != 1 || state.args[0].Value != int64(42) || state.args[3].Value != "login" {
		t.Fatal("auth audit entry was lost or changed")
	}
}

func TestOperationAuditMergesAuthEventWithHTTPMetadata(t *testing.T) {
	repo, state := auditTestRepo(t)
	svc := NewService(repo, nil)
	adapter := NewAuthAuditAdapter(svc)
	h := server.New()
	group := h.Group("/system-api")
	group.Use(svc.OperationAuditMiddleware())
	group.POST("/auth/login", func(ctx context.Context, c *hertzapp.RequestContext) {
		adapter.RecordAuth(ctx, auth.AuditEntry{UserID: 42, Username: "tester", Action: "login", Summary: "登录成功", Status: true})
		c.JSON(200, map[string]string{"accessToken": "secret"})
	})
	ut.PerformRequest(h.Engine, "POST", "/system-api/auth/login", nil)
	if state.writes != 1 {
		t.Fatalf("one HTTP action produced %d rows", state.writes)
	}
	if state.args[0].Value != int64(42) || state.args[3].Value != "login" || state.args[6].Value != "/system-api/auth/login" || state.args[10].Value != "" {
		t.Fatalf("lost auth identity or HTTP metadata: %#v", state.args)
	}
}
func TestOperationAuditSkipsRejectedLoginWithoutDomainEvent(t *testing.T) {
	repo, state := auditTestRepo(t)
	svc := NewService(repo, nil)
	h := server.New()
	g := h.Group("/system-api")
	g.Use(auditActor(rbac.Principal{ID: 42, Username: "tester", Roles: []string{"admin"}}), svc.OperationAuditMiddleware())
	g.POST("/auth/login", func(_ context.Context, c *hertzapp.RequestContext) {
		c.JSON(400, map[string]string{"message": "invalid request"})
	})
	ut.PerformRequest(h.Engine, "POST", "/system-api/auth/login", nil)
	if state.writes != 0 {
		t.Fatal("unattributed login must not enter operation logs")
	}
}
func TestOperationAuditWriteHasIndependentBoundedDeadline(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelled), func(t *testing.T) {
			repo, state := auditTestRepo(t)
			svc := NewService(repo, nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelled {
				cancel()
			}
			before := time.Now()
			svc.RecordOperation(ctx, OperationLogRecord{Action: "update"})
			if state.writes != 1 {
				t.Fatal("request cancellation discarded audit entry")
			}
			if state.contextErr != nil || state.deadline.IsZero() || state.deadline.Sub(before) > 2100*time.Millisecond {
				t.Fatalf("unbounded or cancelled write context: %v %v", state.deadline, state.contextErr)
			}
		})
	}
}
func TestOperationAuditModuleBoundaries(t *testing.T) {
	for _, tc := range []struct{ path, module string }{
		{"/system-api/user/42", "user"}, {"/system-api/userland/42", "system"},
		{"/system-api/notice/42", "content"}, {"/system-api/friend-links/42", "content"},
		{"/system-api/menu/42", "permission"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			module, _ := classifyOperation("PUT", tc.path)
			if module != tc.module {
				t.Fatalf("module=%s want=%s", module, tc.module)
			}
		})
	}
}

func TestOperationAuditDatabaseFailureDoesNotChangeHTTPResponse(t *testing.T) {
	for _, block := range []bool{false, true} {
		t.Run(fmt.Sprint(block), func(t *testing.T) {
			repo, state := auditTestRepo(t)
			state.fail = true
			state.block = block
			svc := NewService(repo, nil)
			h := server.New()
			g := h.Group("/system-api")
			g.Use(auditActor(rbac.Principal{ID: 42, Username: "tester", Roles: []string{"admin"}}), svc.OperationAuditMiddleware())
			g.PUT("/user/42", func(_ context.Context, c *hertzapp.RequestContext) { c.SetStatusCode(204) })
			start := time.Now()
			resp := ut.PerformRequest(h.Engine, "PUT", "/system-api/user/42", nil)
			if resp.Code != 204 {
				t.Fatalf("audit failure changed business status: %d", resp.Code)
			}
			if state.deadline.IsZero() {
				t.Fatal("test never attempted audit persistence")
			}
			if state.writes != 0 {
				t.Fatal("failed write counted as persisted")
			}
			if block && time.Since(start) > 3*time.Second {
				t.Fatal("audit write exceeded bounded timeout")
			}
		})
	}
}
