package observability

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"bugpk-api/internal/domain/ipregion"
	"bugpk-api/internal/domain/rbac"
)

// stubRegionResolver 是确定性的地区解析器：单测不应依赖上游 HTTP。
type stubRegionResolver struct {
	mu      sync.Mutex
	calls   []string
	results map[string]ipregion.Region
	err     error
}

func (r *stubRegionResolver) Resolve(_ context.Context, ip string) (ipregion.Region, error) {
	r.mu.Lock()
	r.calls = append(r.calls, ip)
	r.mu.Unlock()
	if r.err != nil {
		return ipregion.Region{}, r.err
	}
	if region, ok := r.results[ip]; ok {
		return region, nil
	}
	return ipregion.Region{}, errors.New("no stub for " + ip)
}

func (r *stubRegionResolver) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func waitForUpdate(t *testing.T, state *auditCapture) []driver.NamedValue {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(state.updates) > 0 {
			return append([]driver.NamedValue(nil), state.updates...)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("region update never issued")
	return nil
}

func TestRecordOperationBackfillsRegionAsynchronously(t *testing.T) {
	repo, state := auditTestRepo(t)
	// 记录自增主键：异步补全必须能定位到这一行。
	state.lastInsertID = 77
	resolver := &stubRegionResolver{results: map[string]ipregion.Region{
		"8.8.8.8": {Country: "美国", Province: "加州", City: "山景城"},
	}}
	svc := NewService(repo, nil)
	svc.SetIPRegionResolver(resolver)
	defer func() { _ = svc.Close(context.Background()) }()

	svc.RecordOperation(context.Background(), OperationLogRecord{
		UserID: 1, Username: "admin", ClientIP: "8.8.8.8", Action: "create", Module: "system",
	})

	updates := waitForUpdate(t, state)
	// 参数顺序：country, province, city, id
	if updates[0].Value != "美国" || updates[1].Value != "加州" || updates[2].Value != "山景城" || updates[3].Value != int64(77) {
		t.Fatalf("wrong update args: %v", updates)
	}
	if resolver.callCount() != 1 {
		t.Fatalf("resolver called %d times, want 1", resolver.callCount())
	}
}

func TestRecordOperationSkipsPrivateAndPrefilledRegion(t *testing.T) {
	repo, state := auditTestRepo(t)
	state.lastInsertID = 5
	resolver := &stubRegionResolver{results: map[string]ipregion.Region{}}
	svc := NewService(repo, nil)
	svc.SetIPRegionResolver(resolver)
	defer func() { _ = svc.Close(context.Background()) }()

	// 内网 IP：不触发上游解析（内网地址解析无意义且可能泄露内网结构）。
	svc.RecordOperation(context.Background(), OperationLogRecord{ClientIP: "10.0.0.7", Action: "login"})
	// 调用方已自带地区：不重复解析，避免白白消耗上游额度。
	svc.RecordOperation(context.Background(), OperationLogRecord{ClientIP: "8.8.8.8", Country: "中国", Action: "login"})

	if resolver.callCount() != 0 {
		t.Fatalf("resolver called %d times, want 0", resolver.callCount())
	}
	if got := len(state.updates); got != 0 {
		t.Fatalf("unexpected region updates: %d", got)
	}
}

func TestRecordOperationWithoutResolverKeepsLegacyBehavior(t *testing.T) {
	repo, state := auditTestRepo(t)
	svc := NewService(repo, nil)
	// 未注入解析器：写入照旧成功，不应 panic，也不应发任何 update。
	svc.RecordOperation(context.Background(), OperationLogRecord{ClientIP: "8.8.8.8", Action: "create"})
	if state.writes != 1 {
		t.Fatalf("audit write lost: writes=%d", state.writes)
	}
	if len(state.updates) != 0 {
		t.Fatalf("unexpected region update without resolver")
	}
}

// 上游失败必须静默降级：地区列保持为空，但审计主体一行不少。
func TestRecordOperationSurvivesRegionResolverFailure(t *testing.T) {
	repo, state := auditTestRepo(t)
	state.lastInsertID = 9
	resolver := &stubRegionResolver{err: errors.New("upstream timeout")}
	svc := NewService(repo, nil)
	svc.SetIPRegionResolver(resolver)
	defer func() { _ = svc.Close(context.Background()) }()

	svc.RecordOperation(context.Background(), OperationLogRecord{ClientIP: "8.8.8.8", Action: "create"})
	if state.writes != 1 {
		t.Fatalf("audit write lost: writes=%d", state.writes)
	}
	if got := len(state.updates); got != 0 {
		t.Fatalf("a failed resolve must not write an empty region: %d updates", got)
	}
}

func TestResolveOperationLogRegionAdminOnly(t *testing.T) {
	repo, state := auditTestRepo(t)
	state.query = func(q string, _ []driver.NamedValue) (driver.Rows, error) {
		if strings.Contains(q, "from bp_operation_log") {
			return auditRows([]driver.Value{"8.8.8.8", "", "", ""}), nil
		}
		return nil, errors.New("unexpected query: " + q)
	}
	resolver := &stubRegionResolver{results: map[string]ipregion.Region{
		"8.8.8.8": {Country: "美国", Province: "加州", City: "山景城"},
	}}
	svc := NewService(repo, rbac.NewService(rbac.NewRepository(repo.db), nil))
	svc.SetIPRegionResolver(resolver)
	defer func() { _ = svc.Close(context.Background()) }()

	// 非管理员（无 system:operation-log:list）应被拒绝，且不调用上游。
	if _, err := svc.ResolveOperationLogRegion(context.Background(), rbac.Principal{ID: 9, Roles: []string{"user"}}, 1); err == nil {
		t.Fatal("ordinary user was allowed to resolve regions")
	}
	if resolver.callCount() != 0 {
		t.Fatal("resolver called for unauthorized request")
	}

	// admin 角色按既有约定绕过权限校验。
	got, err := svc.ResolveOperationLogRegion(context.Background(), rbac.Principal{ID: 1, Roles: []string{"admin"}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Country != "美国" || got.Province != "加州" || got.City != "山景城" {
		t.Fatalf("unexpected region: %+v", got)
	}
	// update 语句固定 4 个参数（country, province, city, id）。
	if len(state.updates) != 4 || state.updates[0].Value != "美国" || state.updates[3].Value != int64(1) {
		t.Fatalf("expected a persisted update, got %v", state.updates)
	}
	// 「三列皆空才写」是并发安全的前提：写入期补全、详情页解析、回填命令会
	// 争抢同一行，缺了这个条件就可能互相覆盖成空值。
	if !strings.Contains(state.execQueries[len(state.execQueries)-1], "coalesce(country,'') = ''") {
		t.Fatalf("update must be guarded by empty-region condition: %s", state.execQueries)
	}
}

func TestBackfillOperationLogRegionIsGuardedAndIdempotent(t *testing.T) {
	repo, state := auditTestRepo(t)
	if _, err := repo.BackfillOperationLogRegion(context.Background(), "8.8.8.8", "中国", "广东", "深圳"); err != nil {
		t.Fatal(err)
	}
	if len(state.execQueries) != 1 {
		t.Fatalf("want one update, got %d", len(state.execQueries))
	}
	q := state.execQueries[0]
	// 按 IP 批量更新时必须限定在待解析行上，否则会覆盖已解析的历史结论。
	for _, want := range []string{"client_ip = ?", "coalesce(country,'') = ''", "coalesce(province,'') = ''", "coalesce(city,'') = ''"} {
		if !strings.Contains(q, want) {
			t.Fatalf("backfill SQL missing %q: %s", want, q)
		}
	}
	// 空 IP 不应产生任何写操作。
	if _, err := repo.BackfillOperationLogRegion(context.Background(), "  ", "中国", "", ""); err != nil {
		t.Fatal(err)
	}
	if len(state.execQueries) != 1 {
		t.Fatal("blank IP must not issue an update")
	}
}

func TestPendingOperationLogRegionIPsDeduplicatesAndFiltersEmpty(t *testing.T) {
	repo, state := auditTestRepo(t)
	state.query = func(q string, _ []driver.NamedValue) (driver.Rows, error) {
		if !strings.Contains(q, "distinct client_ip") || !strings.Contains(q, "coalesce(country,'') = ''") {
			return nil, errors.New("unexpected query: " + q)
		}
		return auditRows([]driver.Value{"1.1.1.1"}, []driver.Value{"2.2.2.2"}), nil
	}
	ips, err := repo.PendingOperationLogRegionIPs(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ips) != 2 || ips[0] != "1.1.1.1" || ips[1] != "2.2.2.2" {
		t.Fatalf("unexpected ips: %v", ips)
	}
}
