package payment

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Bep20Reconciler 按区块游标拉取收款地址的 USDT-BEP20 转账并匹配充值订单。
// 与 TRC20 的区别：EVM 的 eth_getLogs 是严格按区块范围的完整视图，
// 因此不需要 TronGrid 那种「增量游标 + 回看窗口」的双扫描。
type Bep20Reconciler struct {
	service    *Service
	client     Bep20ChainClient
	rpcURL     string
	fromConfig bool
}

func NewBep20Reconciler(service *Service) *Bep20Reconciler {
	return &Bep20Reconciler{service: service, fromConfig: true}
}

func newBep20ReconcilerWithClient(service *Service, client Bep20ChainClient) *Bep20Reconciler {
	return &Bep20Reconciler{service: service, client: client}
}

var bep20SubmitGuard = newTrc20SubmitLimiter(trc20SubmitGuardCapacity, time.Duration(bep20SubmitCooldown)*time.Second)

func allowBep20Submit(orderNo string) bool { return bep20SubmitGuard.allow(orderNo, time.Now()) }

func (r *Bep20Reconciler) ReconcileOnce(ctx context.Context) error {
	if r == nil || r.service == nil || r.service.repo == nil {
		return nil
	}
	stored, err := r.service.repo.Bep20Config(ctx)
	if err != nil {
		return err
	}
	cfg := bep20PublicConfig(stored)
	address := strings.ToLower(strings.TrimSpace(cfg.Address))
	if !cfg.Enabled || address == "" {
		return nil
	}
	if !bep20AddressPattern.MatchString(address) {
		return fmt.Errorf("BEP20 receiving address is invalid")
	}
	if cfg.Confirmations < 1 || cfg.Confirmations > bep20MaxConfirmations {
		return fmt.Errorf("BEP20 confirmation policy is invalid")
	}
	// RPC 地址变更无需重启：下一轮自动换用新节点。
	if r.client == nil || (r.fromConfig && r.rpcURL != cfg.RPCURL) {
		r.client = newBep20RPCClient(cfg.RPCURL)
		r.rpcURL = cfg.RPCURL
	}
	latest, err := r.client.LatestBlock(ctx)
	if err != nil {
		return err
	}
	var failures []error
	if err := r.retryPending(ctx); err != nil {
		failures = append(failures, err)
	}
	if err := r.scanForward(ctx, stored, cfg, address, latest); err != nil {
		failures = append(failures, err)
	}
	if err := r.scanSnapshotAddresses(ctx, cfg, address, latest); err != nil {
		failures = append(failures, err)
	}
	if err := r.reportUnmatched(ctx); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

// retryPending 逐笔重试已落库但未认领的到账；游标用 CAS 推进，
// 一笔失败不会挡住后面的流水。
func (r *Bep20Reconciler) retryPending(ctx context.Context) error {
	stored, err := r.service.repo.Bep20Config(ctx)
	if err != nil {
		return err
	}
	pending, err := r.service.repo.PendingBep20Transfers(ctx, "", stored.RetryCursorID)
	if err != nil {
		return err
	}
	var failures []error
	previous := stored.RetryCursorID
	for _, transfer := range pending {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		attemptCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := r.reconcileTransfer(attemptCtx, transfer)
		cancel()
		if err != nil {
			failures = append(failures, err)
		}
		checkpointCtx, checkpointCancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		saveErr := r.service.repo.UpdateBep20RetryCursor(checkpointCtx, previous, transfer.ReceiptID)
		checkpointCancel()
		if saveErr != nil {
			return errors.Join(append(failures, saveErr)...)
		}
		previous = transfer.ReceiptID
	}
	if len(pending) == 0 {
		if err := r.service.repo.UpdateBep20RetryCursor(ctx, previous, 0); err != nil {
			return errors.Join(append(failures, err)...)
		}
	}
	return errors.Join(failures...)
}

func (r *Bep20Reconciler) scanForward(ctx context.Context, stored storedBep20Config, cfg Bep20Config, address string, latest int64) error {
	to := latest - int64(cfg.Confirmations) + 1
	if to <= 0 {
		return nil
	}
	cursor := stored.CursorBlock
	if cursor <= 0 {
		cursor = latest - bep20InitialBackfill
		if cursor < 1 {
			cursor = 1
		}
	}
	from := cursor + 1
	if from > to {
		return nil
	}
	transfers, err := r.client.ListTransfers(ctx, cfg.ContractAddress, address, from, to, latest)
	if err != nil {
		return err
	}
	// 先完整落库再结算：任何一条写失败都不推进游标，下一轮重扫是幂等的。
	sort.Slice(transfers, func(i, j int) bool {
		if transfers[i].BlockNumber == transfers[j].BlockNumber {
			return transfers[i].LogIndex < transfers[j].LogIndex
		}
		return transfers[i].BlockNumber < transfers[j].BlockNumber
	})
	for _, transfer := range transfers {
		if err := r.validateTransfer(transfer, address, from, to, cfg.Confirmations); err != nil {
			return err
		}
		if err := r.service.repo.RecordBep20Transfer(ctx, transfer); err != nil {
			return err
		}
	}
	var failures []error
	for _, transfer := range transfers {
		if err := r.reconcileTransfer(ctx, transfer); err != nil {
			failures = append(failures, err)
		}
	}
	if err := r.service.repo.UpdateBep20Cursor(ctx, to); err != nil {
		return errors.Join(append(failures, err)...)
	}
	return errors.Join(failures...)
}

// scanSnapshotAddresses 补扫「已不在配置里、但最近仍有订单快照」的历史收款地址。
// 只回看最近一小段区块：worker 每分钟运行一次，足以覆盖改地址后迟到的转账；
// 更早的漏扫仍可通过用户提交 txid 按订单快照地址精确补单。
func (r *Bep20Reconciler) scanSnapshotAddresses(ctx context.Context, cfg Bep20Config, currentAddress string, latest int64) error {
	addresses, err := r.service.repo.snapshotChainAddresses(ctx, ChannelBep20, currentAddress, trc20SnapshotAddressWindowHours, trc20SnapshotAddressLimit)
	if err != nil || len(addresses) == 0 {
		return err
	}
	to := latest - int64(cfg.Confirmations) + 1
	if to <= 0 {
		return nil
	}
	since := latest - bep20SnapshotWindowBlock
	if since < 1 {
		since = 1
	}
	var failures []error
	for _, address := range addresses {
		address = strings.ToLower(strings.TrimSpace(address))
		if !bep20AddressPattern.MatchString(address) || address == currentAddress {
			continue
		}
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		transfers, err := r.client.ListTransfers(ctx, cfg.ContractAddress, address, since, to, latest)
		if err != nil {
			failures = append(failures, fmt.Errorf("snapshot address %s: %w", address, err))
			continue
		}
		for _, transfer := range transfers {
			if err := r.validateTransfer(transfer, address, since, to, cfg.Confirmations); err != nil {
				failures = append(failures, err)
				continue
			}
			if err := r.service.repo.RecordBep20Transfer(ctx, transfer); err != nil {
				failures = append(failures, err)
				continue
			}
			if err := r.reconcileTransfer(ctx, transfer); err != nil {
				failures = append(failures, err)
			}
		}
	}
	return errors.Join(failures...)
}

func (r *Bep20Reconciler) validateTransfer(transfer Bep20Transfer, address string, minBlock, maxBlock int64, required int) error {
	if !bep20TxIDPattern.MatchString(transfer.TxHash) || transfer.LogIndex < 0 || transfer.BlockNumber <= 0 {
		return fmt.Errorf("invalid BEP20 receipt identity")
	}
	if transfer.BlockNumber < minBlock || transfer.BlockNumber > maxBlock {
		return fmt.Errorf("BEP20 receipt block is outside the scanned window")
	}
	if !strings.EqualFold(transfer.To, address) {
		return fmt.Errorf("BEP20 receipt recipient mismatch")
	}
	if transfer.Confirmations < 1 || transfer.Confirmations < required {
		return fmt.Errorf("BEP20 receipt is not confirmed deep enough")
	}
	return nil
}

func (r *Bep20Reconciler) reconcileTransfer(ctx context.Context, transfer Bep20Transfer) error {
	settled, err := r.service.repo.Bep20TransferExists(ctx, transfer)
	if err != nil || settled {
		return err
	}
	orderNo, err := r.service.repo.MatchBep20Order(ctx, transfer)
	if err != nil || orderNo == "" {
		return err
	}
	if err := r.service.repo.SettleBep20Order(ctx, orderNo, transfer); err != nil {
		return err
	}
	r.service.notifyPaymentPaid(ctx, orderNo)
	return nil
}

func (r *Bep20Reconciler) reportUnmatched(ctx context.Context) error {
	items, err := r.service.repo.UnmatchedBep20Transfers(ctx, trc20UnmatchedWarnMinutes, 50)
	if err != nil || len(items) == 0 {
		return err
	}
	return fmt.Errorf("BEP20 receipts arrived without a matching order (manual reconciliation required): %s", describeTrc20Unmatched(items))
}

// StartBep20Reconciliation 每分钟对账一次；配置变更无需重启。
func (s *Service) StartBep20Reconciliation(ctx context.Context, interval time.Duration, onError func(error)) *Trc20ReconcileWorker {
	if ctx == nil {
		ctx = context.Background()
	}
	if interval <= 0 {
		interval = time.Minute
	}
	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	reconciler := NewBep20Reconciler(s)
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-workerCtx.Done():
				return
			case <-ticker.C:
				runCtx, runCancel := context.WithTimeout(workerCtx, 45*time.Second)
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
