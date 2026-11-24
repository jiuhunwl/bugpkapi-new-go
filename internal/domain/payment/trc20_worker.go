package payment

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Trc20Reconciler 负责拉取收款地址的链上转账并匹配充值订单。
// 匹配只认「金额完全一致 + 时间窗内 + 尚未绑定 txid」的未完成订单。
type Trc20Reconciler struct {
	service  *Service
	chain    Trc20ChainClient
	lookback trc20Lookback
}

// This cursor is deliberately separate from the durable forward cursor. A
// provider can index an older transfer after a newer one. Bounded historical
// pages must make progress without making the forward scan repeat its backlog.
// Restarting the worker safely repeats the recent window; receipts are durable
// and their existing transaction IDs keep settlement idempotent.
type trc20Lookback struct {
	address  string
	contract string
	cursor   int64
	upper    int64
}

func NewTrc20Reconciler(service *Service) *Trc20Reconciler {
	return &Trc20Reconciler{service: service, chain: newTrongridClient()}
}

func (r *Trc20Reconciler) ReconcileOnce(ctx context.Context) error {
	if r == nil || r.service == nil || r.service.repo == nil || r.chain == nil {
		return nil
	}
	stored, err := r.service.repo.Trc20Config(ctx)
	if err != nil {
		return err
	}
	address := strings.TrimSpace(stored.Address)
	if !stored.Enabled || address == "" {
		return nil
	}
	if stored.Confirmations != trc20DefaultConfirmations {
		return fmt.Errorf("TRC20 configuration requires an unsupported confirmation depth")
	}
	apiKey := ""
	if strings.TrimSpace(stored.APIKeyCipher) != "" {
		if r.service.cipher == nil {
			return ErrPaymentSecretMissing
		}
		apiKey, err = r.service.cipher.Decrypt(stored.APIKeyCipher)
		if err != nil {
			return err
		}
	}
	var failures []error
	retried := make(map[string]bool)
	// 重试队列不再按当前收款地址过滤：改过地址之后，旧地址上的悬空到账也必须
	// 能被重新尝试，否则只能人工改库。
	pending, err := r.service.repo.PendingTrc20Transfers(ctx, "", stored.RetryCursorID)
	if err != nil {
		return err
	}

	previousRetryID := stored.RetryCursorID
	for _, transfer := range pending {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		retried[transfer.TxID] = true
		attemptCtx, cancel := context.WithTimeout(ctx, time.Second)
		err := r.reconcileTransfer(attemptCtx, transfer)
		cancel()
		if err != nil {
			failures = append(failures, err)
		}
		// A cancelled/expired attempt still needs a bounded checkpoint. Without it,
		// one locked receipt can prevent every later receipt from ever being tried.
		checkpointCtx, checkpointCancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		saveErr := r.service.repo.UpdateTrc20RetryCursor(checkpointCtx, previousRetryID, transfer.ReceiptID)
		checkpointCancel()
		if saveErr != nil {
			return errors.Join(append(failures, saveErr)...)
		}
		previousRetryID = transfer.ReceiptID
	}
	if len(pending) == 0 {
		if err := r.service.repo.UpdateTrc20RetryCursor(ctx, previousRetryID, 0); err != nil {
			return errors.Join(append(failures, err)...)
		}
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(append(failures, err)...)
	}
	cursor := stored.CursorTimestampMS
	if cursor <= 0 {
		cursor = time.Now().Add(-trc20ScanWindow).UnixMilli()
	}
	transfers, err := r.chain.ListTransfers(ctx, address, stored.ContractAddress, apiKey, cursor)
	if err != nil {
		return errors.Join(append(failures, err)...)
	}
	nextCursor, _, err := r.recordTransfers(ctx, transfers, address, cursor, retried)
	if err != nil {
		failures = append(failures, err)
	}
	if err := r.service.repo.UpdateTrc20Cursor(ctx, nextCursor); err != nil {
		return errors.Join(append(failures, err)...)
	}
	// Forward scanning and its checkpoint take priority. The extra scan has its
	// own time budget and never rewinds the database's high-water mark.
	lookbackCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = r.reconcileLookback(lookbackCtx, address, stored.ContractAddress, apiKey, nextCursor, retried)
	cancel()
	if err != nil {
		failures = append(failures, err)
	}
	// 历史收款地址补扫：后台改过 address 之后，旧地址上的钱不会自己出现。
	historicalCtx, historicalCancel := context.WithTimeout(ctx, 10*time.Second)
	err = r.reconcileSnapshotAddresses(historicalCtx, address, stored.ContractAddress, apiKey, retried)
	historicalCancel()
	if err != nil {
		failures = append(failures, err)
	}
	// 悬空到账必须变成可见的告警，不能只躺在流水表里。
	if err := r.reportUnmatched(ctx); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

func (r *Trc20Reconciler) recordTransfers(ctx context.Context, transfers []Trc20Transfer, address string, cursor int64, attempted map[string]bool) (int64, bool, error) {
	sort.Slice(transfers, func(i, j int) bool { return transfers[i].BlockTimestamp < transfers[j].BlockTimestamp })
	nextCursor := cursor
	var failures []error
	for _, transfer := range transfers {
		if transfer.BlockTimestamp < cursor {
			continue
		}
		if transfer.TxID == "" || transfer.To != address || transfer.Confirmations != trc20DefaultConfirmations {
			return nextCursor, false, errors.Join(append(failures, fmt.Errorf("invalid confirmed TRC20 receipt"))...)
		}
		// Only a durable receipt permits advancing past a failed settlement.
		if err := r.service.repo.RecordTrc20Transfer(ctx, transfer); err != nil {
			return nextCursor, false, errors.Join(append(failures, err)...)
		}
		if !attempted[transfer.TxID] {
			attempted[transfer.TxID] = true
			if err := r.reconcileTransfer(ctx, transfer); err != nil {
				failures = append(failures, err)
			}
		}
		nextCursor = transfer.BlockTimestamp
	}
	return nextCursor, true, errors.Join(failures...)
}

func (r *Trc20Reconciler) reconcileLookback(ctx context.Context, address, contract, apiKey string, forwardCursor int64, attempted map[string]bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	start := time.Now().Add(-trc20ScanWindow).UnixMilli()
	if forwardCursor <= start {
		return nil
	}
	if r.lookback.address != address || r.lookback.contract != contract || r.lookback.cursor < start || r.lookback.upper == 0 {
		r.lookback = trc20Lookback{address: address, contract: contract, cursor: start, upper: forwardCursor}
	}
	lower := r.lookback.cursor
	transfers, err := r.chain.ListTransfers(ctx, address, contract, apiKey, lower)
	if err != nil {
		return err
	}
	next, complete, err := r.recordTransfers(ctx, transfers, address, lower, attempted)
	// Even when a later receipt cannot be stored, resume at the last durable
	// receipt. Failed settlements are already recoverable by the retry queue.
	r.lookback.cursor = next
	if complete && (next >= r.lookback.upper || next == lower) {
		// A full window with no newer receipt is also caught up. The provider
		// client never returns a partial empty window after reducing page bounds.
		r.lookback.upper = 0
	}
	return err
}

func (r *Trc20Reconciler) reconcileTransfer(ctx context.Context, transfer Trc20Transfer) error {
	settled, err := r.service.repo.Trc20TransferExists(ctx, transfer.TxID)
	if err != nil || settled {
		return err
	}
	orderNo, err := r.service.repo.MatchTrc20Order(ctx, transfer)
	if err != nil || orderNo == "" {
		return err
	}
	if err := r.service.repo.SettleTrc20Order(ctx, orderNo, transfer); err != nil {
		return err
	}
	r.service.notifyPaymentPaid(ctx, orderNo)
	return nil
}

// scanAddress 扫一个收款地址的已确认入账。current=false 表示这是历史快照地址，
// 扫到的新流水同样落库并尝试匹配，但不会推进主游标。
func (r *Trc20Reconciler) scanAddress(ctx context.Context, address, contract, apiKey string, cursor int64, retried map[string]bool) (int64, error) {
	transfers, err := r.chain.ListTransfers(ctx, address, contract, apiKey, cursor)
	if err != nil {
		return cursor, err
	}
	nextCursor, _, err := r.recordTransfers(ctx, transfers, address, cursor, retried)
	return nextCursor, err
}

// reconcileSnapshotAddresses 回头补扫「已不在配置里、但最近仍有订单快照」的地址。
//
// 后台改收款地址之后，旧地址上的入账再也进不了主扫描；而这些订单仍然带着旧
// 地址快照、仍然可以被合法结算。不补扫，用户转进旧地址的钱就永久悬空。
func (r *Trc20Reconciler) reconcileSnapshotAddresses(ctx context.Context, currentAddress, contract, apiKey string, retried map[string]bool) error {
	addresses, err := r.service.repo.Trc20SnapshotAddresses(ctx, currentAddress, trc20SnapshotAddressWindowHours, trc20SnapshotAddressLimit)
	if err != nil || len(addresses) == 0 {
		return err
	}
	var failures []error
	for _, address := range addresses {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		// 历史地址没有独立游标，固定回看匹配窗口即可：需要补的钱一定在这个窗口内
		// 才能被结算事务接受，更早的流水扫进来也不会被认领。
		since := time.Now().Add(-trc20MatchWindowHours * time.Hour).UnixMilli()
		if _, err := r.scanAddress(ctx, address, contract, apiKey, since, retried); err != nil {
			failures = append(failures, fmt.Errorf("snapshot address %s: %w", address, err))
		}
	}
	return errors.Join(failures...)
}

// reportUnmatched 把「已到账但没有订单认领」的流水升级为必须处理的告警。
//
// 没有这一步，钱到了、订单还停在 EXPIRED 的状态是完全静默的：用户以为要再转一次，
// 运营看不到任何信号。这里把每笔悬空入账的 txid/金额/收款地址直接抛给上层日志。
func (r *Trc20Reconciler) reportUnmatched(ctx context.Context) error {
	items, err := r.service.repo.UnmatchedTrc20Transfers(ctx, trc20UnmatchedWarnMinutes, 50)
	if err != nil || len(items) == 0 {
		return err
	}
	return fmt.Errorf("TRC20 receipts arrived without a matching order (manual reconciliation required): %s", describeTrc20Unmatched(items))
}

// Trc20ReconcileWorker 每分钟对账一次；配置变更无需重启，下一轮自动生效。
type Trc20ReconcileWorker struct {
	cancel context.CancelFunc
	done   <-chan struct{}
	once   sync.Once
}

func (s *Service) StartTrc20Reconciliation(ctx context.Context, interval time.Duration, onError func(error)) *Trc20ReconcileWorker {
	if ctx == nil {
		ctx = context.Background()
	}
	if interval <= 0 {
		interval = time.Minute
	}
	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	reconciler := NewTrc20Reconciler(s)
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-workerCtx.Done():
				return
			case <-ticker.C:
				runCtx, runCancel := context.WithTimeout(workerCtx, 30*time.Second)
				err := reconciler.ReconcileOnce(runCtx)
				runCancel()
				if err != nil && !errors.Is(err, context.Canceled) && onError != nil {
					onError(err)
				}
			}
		}
	}()
	return &Trc20ReconcileWorker{cancel: cancel, done: done}
}

func (w *Trc20ReconcileWorker) Close(ctx context.Context) error {
	if w == nil {
		return nil
	}
	w.once.Do(func() {
		if w.cancel != nil {
			w.cancel()
		}
	})
	if w.done == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
