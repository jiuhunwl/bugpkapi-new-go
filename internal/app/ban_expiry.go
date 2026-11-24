package app

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"bugpk-api/internal/pkg/masking"

	"go.uber.org/zap"
)

const (
	banExpiryInterval   = time.Minute
	banExpiryBatchSize  = 500
	banExpiryMaxBatches = 20
	banExpiryTimeout    = 30 * time.Second
)

// banExpirySelectStatement 选出「临时封禁已到期」的账号。
//
// 背景：accountstatus.EffectiveStatusSQL 在 status=0 且 ban_until<=now() 时会把
// 有效状态当作 1，好让到期账号无需写库即可恢复。代价是 bp_user.status 仍是 0：
// 后台用户列表显示「已封禁」，网关与控制台却按「正常」放行。运营看到的就是
// 「账号已封禁，但 key 和其他功能还能用」——同一行数据有两个互相矛盾的真相。
//
// 这里每分钟把到期行真正解封，让 status 成为唯一真相：
//   - status=0 永远等于「当前不可用」，封禁立即生效、后台显示与实际一致；
//   - 临时封禁到期由本任务显式落库，不再依赖读路径的隐式判断；
//   - 条件里的 ban_permanent=0 保证永久封禁永远不会被这条语句选中。
const banExpirySelectStatement = `
	select id
	  from bp_user
	 where status = 0
	   and ban_permanent = 0
	   and ban_until is not null
	   and ban_until <= now()
	 order by ban_until asc
	 limit ?`

// banExpiryReleaseStatement 把到期账号写回启用态。
// 入选条件在写入时再判一次：从 select 到 update 之间账号可能被重新封禁
// （例如运营手动改成永久封禁），这里必须让那种情况落空。
const banExpiryReleaseStatement = `
	update bp_user
	   set status = 1,
	       ban_permanent = 0,
	       ban_until = null,
	       ban_reason = ''
	 where id in (%s)
	   and status = 0
	   and ban_permanent = 0
	   and ban_until is not null
	   and ban_until <= now()`

// banExpiryRestoreKeysStatement 恢复因封禁而暂停的接口 Key。
//
// join bp_user 且要求 u.status = 1 是必须的：解封同一批里可能混入刚刚
// 又被重新封禁的账号，只按标记恢复会把它们的 Key 放回线上。
// 条件覆盖全部封禁标记（> 0）而不只是 1：账号已经解封，凭证不该再带着
// 任何一种封禁留下的标记活着，否则会出现「账号正常、Key 永远停用」。
const banExpiryRestoreKeysStatement = `
	update bp_api_key k
	  inner join bp_user u on u.id = k.user_id
	   set k.status = 1,
	       k.suspended_by_ban = 0
	 where k.user_id in (%s)
	   and k.suspended_by_ban > 0
	   and u.status = 1`

// banExpiryReconcileKeysStatement 修复「账号已解封、Key 仍冻结」的孤儿行。
//
// 解封与恢复 Key 是两条独立语句。如果进程在两者之间退出，或恢复语句遇到
// 死锁、连接中断，账号此时已经是 status=1，不会再被到期查询选中，
// 它的 Key 就永久卡在封禁标记上：用户看到「账号正常，但名下所有
// Key 都是停用」，而系统里没有任何路径会自己好起来。
//
// 这条语句反过来按「账号已启用 + Key 仍标记封禁」找孤儿行，
// 每轮把上一轮的残留修回来；数据正常时它命中 0 行。
// 覆盖全部标记（> 0）：账号可用与凭证可用必须是同一件事。
const banExpiryReconcileKeysStatement = `
	update bp_api_key k
	  inner join bp_user u on u.id = k.user_id
	   set k.status = 1,
	       k.suspended_by_ban = 0
	 where k.suspended_by_ban > 0
	   and u.status = 1`

// banExpiryStore 抽成接口以便单测：生产用 sqlBanExpiryStore，测试用假实现。
type banExpiryStore interface {
	DueBanIDs(ctx context.Context, limit int) ([]int64, error)
	ReleaseBans(ctx context.Context, ids []int64) (int, error)
	RestoreKeys(ctx context.Context, ids []int64) error
	ReconcileSuspendedKeys(ctx context.Context) error
}

type sqlBanExpiryStore struct {
	db *sql.DB
}

func (s sqlBanExpiryStore) DueBanIDs(ctx context.Context, limit int) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, banExpirySelectStatement, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	ids := make([]int64, 0, limit)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s sqlBanExpiryStore) ReleaseBans(ctx context.Context, ids []int64) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	result, err := s.db.ExecContext(ctx, fmt.Sprintf(banExpiryReleaseStatement, placeholders(len(ids))), toAny(ids)...)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(affected), nil
}

func (s sqlBanExpiryStore) RestoreKeys(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(banExpiryRestoreKeysStatement, placeholders(len(ids))), toAny(ids)...)
	return err
}

func (s sqlBanExpiryStore) ReconcileSuspendedKeys(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, banExpiryReconcileKeysStatement)
	return err
}

// sweepBans 是单轮任务的完整语义：先修补上一轮遗留的孤儿 Key，再解封本轮到期的账号。
//
// 顺序刻意如此：孤儿修补不依赖本轮结果，先做可以把「上一轮崩溃」的影响限制在一分钟内。
// 两者互不阻断——解封是主任务，即使修补失败也要把到期的账号放出来，
// 因此先记录修补错误，再执行解封，最后才把修补错误交回调用方记日志。
func sweepBans(ctx context.Context, store banExpiryStore) (int, error) {
	if store == nil {
		return 0, nil
	}
	reconcileErr := store.ReconcileSuspendedKeys(ctx)
	released, releaseErr := expireDueBans(ctx, store, banExpiryBatchSize)
	if releaseErr != nil {
		return released, releaseErr
	}
	return released, reconcileErr
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimRight(strings.Repeat("?,", n), ",")
}

func toAny(ids []int64) []any {
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	return args
}

// expireDueBans 分批解封已到期的临时封禁，返回真正解封的账号数。
//
// 每一批都先取 ID 再按 ID 更新：这样才能把同一批账号的 Key 一起恢复。
// 取到的 ID 数量不足 batchSize 即说明已经取完，可以提前结束。
func expireDueBans(ctx context.Context, store banExpiryStore, batchSize int) (int, error) {
	if store == nil || batchSize <= 0 {
		return 0, nil
	}
	total := 0
	for batch := 0; batch < banExpiryMaxBatches; batch++ {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		ids, err := store.DueBanIDs(ctx, batchSize)
		if err != nil {
			return total, err
		}
		if len(ids) == 0 {
			break
		}
		released, err := store.ReleaseBans(ctx, ids)
		if err != nil {
			return total, err
		}
		total += released
		// 即使 released 为 0（账号在 select 与 update 之间被重新封禁），
		// 恢复语句仍然要跑：它自带 u.status = 1 的条件，不会误放行。
		if err := store.RestoreKeys(ctx, ids); err != nil {
			return total, err
		}
		if len(ids) < batchSize {
			break
		}
	}
	return total, nil
}

// startBanExpirySweeper 启动临时封禁到期清理任务。启动即执行一次，之后每分钟一次。
func startBanExpirySweeper(parent context.Context, db *sql.DB, log *zap.Logger) (context.CancelFunc, <-chan struct{}) {
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	if log == nil {
		log = zap.NewNop()
	}
	var store banExpiryStore
	if db != nil {
		store = sqlBanExpiryStore{db: db}
	}
	go func() {
		defer close(done)
		sweep := func() {
			sweepCtx, sweepCancel := context.WithTimeout(ctx, banExpiryTimeout)
			defer sweepCancel()
			released, err := sweepBans(sweepCtx, store)
			if err != nil {
				// 关闭期间取消导致的错误不是故障，避免停机日志噪声。
				if ctx.Err() == nil {
					log.Warn("temporary ban expiry sweep failed", zap.String("error", masking.Error(err)))
				}
			}
			if released > 0 {
				// 这里必须留痕：解封是一次真实的状态变更，运营需要能追溯。
				log.Info("temporary bans expired", zap.Int("accounts", released))
			}
		}
		sweep()
		ticker := time.NewTicker(banExpiryInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sweep()
			}
		}
	}()
	return cancel, done
}
