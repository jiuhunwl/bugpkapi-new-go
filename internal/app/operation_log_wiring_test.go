package app

import (
	"os"
	"strings"
	"testing"
)

func TestOperationAuditUsesSharedGroupBeforeBusinessRoutes(t *testing.T) {
	b, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if strings.Contains(s, "h.Group(cfg.API.SystemPrefix +") {
		t.Fatal("system routes bypass the shared audit group")
	}
	audit := strings.Index(s, "systemGroup := newSystemRouteGroup(h, cfg.API.SystemPrefix, observabilityService.OperationAuditMiddleware(cfg.API.SystemPrefix), log)")
	if audit < 0 {
		t.Fatal("all system routes must be composed from the shared audit group")
	}
	// 账号封禁是服务器级主网关：挂在某个路由组上会让新增的前缀绕过拦截。
	guard := strings.Index(s, "h.Use(newAccountStatusGuard(rbacService))")
	if guard < 0 {
		t.Fatal("account status guard must be registered on the server, not on a route group")
	}
	if guard > audit {
		t.Fatal("account status guard must be registered before any route group")
	}
	first := strings.Index(s, "authHandler.Register(")
	if audit > first {
		t.Fatal("audit middleware must be installed before business routes")
	}
	if !strings.Contains(s, `gatewayHandler.RegisterOpen(h.Group("/api"))`) {
		t.Fatal("open gateway must remain outside audit group")
	}
}

func TestOperationAuditMigrationPreservesHistoryAndIndexesScope(t *testing.T) {
	up, err := os.ReadFile("../../migrations/000097_operation_log_actor_scope.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile("../../migrations/000097_operation_log_actor_scope.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"ADD COLUMN actor_kind", "DEFAULT ''", "(actor_kind, created_at)", "(user_id, actor_kind, created_at)"} {
		if !strings.Contains(string(up), fragment) {
			t.Fatalf("missing %s", fragment)
		}
	}
	for _, b := range [][]byte{up, down} {
		for _, bad := range []string{"delete from", "truncate table", "update bp_operation_log", "join bp_user"} {
			if strings.Contains(strings.ToLower(string(b)), bad) {
				t.Fatalf("migration destroys or guesses history: %s", bad)
			}
		}
	}
	if !strings.Contains(string(down), "DROP COLUMN actor_kind") {
		t.Fatal("missing rollback")
	}
}
