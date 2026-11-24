package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

type fakeBanStore struct {
	dueIDs        [][]int64
	releaseFrom   []int64
	restored      [][]int64
	dueErr        error
	releaseErr    error
	restoreErr    error
	reconcileErr  error
	dueCalls      int
	releaseCall   int
	restoreCall   int
	reconcileCall int
	order         []string
	ctxErrs       []error
}

func (f *fakeBanStore) ReconcileSuspendedKeys(ctx context.Context) error {
	f.reconcileCall++
	f.order = append(f.order, "reconcile")
	return f.reconcileErr
}

func (f *fakeBanStore) DueBanIDs(ctx context.Context, limit int) ([]int64, error) {
	f.ctxErrs = append(f.ctxErrs, ctx.Err())
	f.dueCalls++
	f.order = append(f.order, "due")
	if f.dueErr != nil {
		return nil, f.dueErr
	}
	index := f.dueCalls - 1
	if index >= len(f.dueIDs) {
		return nil, nil
	}
	return f.dueIDs[index], nil
}

func (f *fakeBanStore) ReleaseBans(ctx context.Context, ids []int64) (int, error) {
	f.releaseCall++
	if f.releaseErr != nil {
		return 0, f.releaseErr
	}
	f.releaseFrom = append(f.releaseFrom, ids...)
	return len(ids), nil
}

func (f *fakeBanStore) RestoreKeys(ctx context.Context, ids []int64) error {
	f.restoreCall++
	f.order = append(f.order, "restore")
	if f.restoreErr != nil {
		return f.restoreErr
	}
	f.restored = append(f.restored, ids)
	return nil
}

func TestExpireDueBansStopsAfterShortBatch(t *testing.T) {
	store := &fakeBanStore{dueIDs: [][]int64{{3, 4, 5}}}
	released, err := expireDueBans(context.Background(), store, 500)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if released != 3 {
		t.Fatalf("released = %d, want 3", released)
	}
	if store.dueCalls != 1 {
		t.Fatalf("due calls = %d, want 1", store.dueCalls)
	}
	if store.restoreCall != 1 {
		t.Fatalf("restore calls = %d, want 1", store.restoreCall)
	}
}

func TestExpireDueBansDrainsFullBatches(t *testing.T) {
	first := make([]int64, 500)
	for i := range first {
		first[i] = int64(i + 1)
	}
	store := &fakeBanStore{dueIDs: [][]int64{first, {900, 901}}}
	released, err := expireDueBans(context.Background(), store, 500)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if released != 502 {
		t.Fatalf("released = %d, want 502", released)
	}
	if store.dueCalls != 2 {
		t.Fatalf("due calls = %d, want 2", store.dueCalls)
	}
	if store.restoreCall != 2 {
		t.Fatalf("restore calls = %d, want 2", store.restoreCall)
	}
}

func TestExpireDueBansCapsBatchesPerRun(t *testing.T) {
	store := &fakeBanStore{}
	for i := 0; i < banExpiryMaxBatches+5; i++ {
		full := make([]int64, 500)
		for j := range full {
			full[j] = int64(i*500 + j + 1)
		}
		store.dueIDs = append(store.dueIDs, full)
	}
	released, err := expireDueBans(context.Background(), store, 500)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store.dueCalls != banExpiryMaxBatches {
		t.Fatalf("due calls = %d, want %d", store.dueCalls, banExpiryMaxBatches)
	}
	if released != banExpiryMaxBatches*500 {
		t.Fatalf("released = %d, want %d", released, banExpiryMaxBatches*500)
	}
}

// Key 恢复语句必须无条件跟随解封执行：即使这批账号在 select 与 update 之间
// 被重新封禁（released 变成 0），也不能跳过——否则「账号恢复但 Key 仍冻结」
// 或者反面「账号仍封禁但 Key 被放回线上」都会出现。
func TestExpireDueBansAlwaysRestoresKeysForSelectedBatch(t *testing.T) {
	store := &fakeBanStore{dueIDs: [][]int64{{7, 8}}}
	store.releaseErr = nil
	if _, err := expireDueBans(context.Background(), store, 500); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store.restoreCall != 1 {
		t.Fatalf("restore calls = %d, want 1", store.restoreCall)
	}
	if got := store.restored[0]; len(got) != 2 || got[0] != 7 || got[1] != 8 {
		t.Fatalf("restored = %v, want [7 8]", got)
	}
}

func TestExpireDueBansPropagatesSelectError(t *testing.T) {
	sentinel := errors.New("db down")
	store := &fakeBanStore{dueErr: sentinel}
	released, err := expireDueBans(context.Background(), store, 500)
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want %v", err, sentinel)
	}
	if released != 0 {
		t.Fatalf("released = %d, want 0", released)
	}
	if store.restoreCall != 0 {
		t.Fatalf("restore calls = %d, want 0", store.restoreCall)
	}
}

func TestExpireDueBansPropagatesRestoreError(t *testing.T) {
	sentinel := errors.New("key table locked")
	store := &fakeBanStore{dueIDs: [][]int64{{11}}, restoreErr: sentinel}
	if _, err := expireDueBans(context.Background(), store, 500); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want %v", err, sentinel)
	}
}

func TestExpireDueBansStopsOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := &fakeBanStore{dueIDs: [][]int64{{1}}}
	if _, err := expireDueBans(ctx, store, 500); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if store.dueCalls != 0 {
		t.Fatalf("due calls = %d, want 0", store.dueCalls)
	}
}

func TestExpireDueBansIgnoresNilStore(t *testing.T) {
	released, err := expireDueBans(context.Background(), nil, 500)
	if err != nil || released != 0 {
		t.Fatalf("released = %d, err = %v, want 0/nil", released, err)
	}
}

// 防回归：这条语句一旦误伤永久封禁，等于后台任务替攻击者解封。
func TestBanExpirySelectOnlyTargetsTemporaryBans(t *testing.T) {
	for _, clause := range []string{
		"from bp_user",
		"status = 0",
		"ban_permanent = 0",
		"ban_until is not null",
		"ban_until <= now()",
		"limit ?",
	} {
		if !strings.Contains(banExpirySelectStatement, clause) {
			t.Fatalf("statement missing %q:\n%s", clause, banExpirySelectStatement)
		}
	}
	if strings.Contains(banExpirySelectStatement, "ban_permanent = 1") {
		t.Fatalf("statement must never target permanent bans:\n%s", banExpirySelectStatement)
	}
	if strings.Contains(strings.ToLower(banExpirySelectStatement), "delete") {
		t.Fatalf("statement must not delete rows:\n%s", banExpirySelectStatement)
	}
}

// 防回归：释放语句必须再判一次到期条件，避免把「select 之后才被封禁」的账号解锁。
func TestBanExpiryReleaseRechecksConditions(t *testing.T) {
	for _, clause := range []string{
		"set status = 1",
		"ban_permanent = 0",
		"ban_until is not null",
		"ban_until <= now()",
		"where id in (%s)",
	} {
		if !strings.Contains(banExpiryReleaseStatement, clause) {
			t.Fatalf("statement missing %q:\n%s", clause, banExpiryReleaseStatement)
		}
	}
}

// 孤儿修补必须先于解封执行：上一轮若在「解封账号」与「恢复 Key」之间崩溃，
// 留下的 suspended_by_ban=1 只有在下一轮开始前修掉，用户才不会长期停在
// 「账号正常，但名下所有 Key 都是停用」这个无法自愈的状态。
func TestSweepBansReconcilesBeforeReleasing(t *testing.T) {
	store := &fakeBanStore{dueIDs: [][]int64{{21}}}
	released, err := sweepBans(context.Background(), store)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if released != 1 {
		t.Fatalf("released = %d, want 1", released)
	}
	if store.reconcileCall != 1 {
		t.Fatalf("reconcile calls = %d, want 1", store.reconcileCall)
	}
	want := []string{"reconcile", "due", "restore"}
	if !slices.Equal(store.order, want) {
		t.Fatalf("order = %v, want %v", store.order, want)
	}
}

// 修补失败不能阻断解封：解封是主任务，修补只是自愈；错误仍要上报给日志。
func TestSweepBansReleasesEvenWhenReconcileFails(t *testing.T) {
	sentinel := errors.New("reconcile failed")
	store := &fakeBanStore{dueIDs: [][]int64{{31}}, reconcileErr: sentinel}
	released, err := sweepBans(context.Background(), store)
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want %v", err, sentinel)
	}
	if released != 1 {
		t.Fatalf("released = %d, want 1", released)
	}
	if store.restoreCall != 1 {
		t.Fatalf("restore calls = %d, want 1", store.restoreCall)
	}
}

func TestSweepBansIgnoresNilStore(t *testing.T) {
	released, err := sweepBans(context.Background(), nil)
	if err != nil || released != 0 {
		t.Fatalf("released = %d, err = %v, want 0/nil", released, err)
	}
}

// 防回归：孤儿修补按「账号已启用」反查所有带封禁标记的 Key，
// 不区分标记来源（1 临时 / 2 永久）——账号可用与凭证可用必须是同一件事。
// 它必须是全局反查而不是按 ID 列表操作，否则崩溃后那批 ID 再也取不到。
func TestBanExpiryReconcileRestoresEverySuspensionOfActiveAccounts(t *testing.T) {
	for _, clause := range []string{
		"inner join bp_user u on u.id = k.user_id",
		"k.suspended_by_ban > 0",
		"u.status = 1",
		"set k.status = 1",
		"k.suspended_by_ban = 0",
	} {
		if !strings.Contains(banExpiryReconcileKeysStatement, clause) {
			t.Fatalf("statement missing %q:\n%s", clause, banExpiryReconcileKeysStatement)
		}
	}
	// 只覆盖单一标记，会把另一类封禁留下的 Key 永久钉死在停用状态。
	for _, clause := range []string{"k.suspended_by_ban = 1", "k.suspended_by_ban = 2"} {
		if strings.Contains(banExpiryReconcileKeysStatement, clause) {
			t.Fatalf("reconcile must not be limited to %q:\n%s", clause, banExpiryReconcileKeysStatement)
		}
	}
	if strings.Contains(banExpiryReconcileKeysStatement, "where id in") {
		t.Fatalf("reconcile must scan globally, not by batch ids:\n%s", banExpiryReconcileKeysStatement)
	}
}

// 防回归：恢复 Key 必须 join bp_user 且要求账号已启用，
// 否则同一批里刚被重新封禁的账号会把 Key 放回线上。
func TestBanExpiryRestoreKeysRequiresActiveAccount(t *testing.T) {
	for _, clause := range []string{
		"inner join bp_user u on u.id = k.user_id",
		"k.suspended_by_ban > 0",
		"u.status = 1",
		"set k.status = 1",
	} {
		if !strings.Contains(banExpiryRestoreKeysStatement, clause) {
			t.Fatalf("statement missing %q:\n%s", clause, banExpiryRestoreKeysStatement)
		}
	}
	for _, clause := range []string{"k.suspended_by_ban = 1", "k.suspended_by_ban = 2"} {
		if strings.Contains(banExpiryRestoreKeysStatement, clause) {
			t.Fatalf("restore must not be limited to %q:\n%s", clause, banExpiryRestoreKeysStatement)
		}
	}
}
