package main

// bugpk-trc20ctl：TRC20 充值现场排查与补单工具。
//
// 用途（都在「钱已到链上、订单却还停在 EXPIRED」时使用）：
//
//	bugpk-trc20ctl -cmd diagnose -amount 50.41
//	bugpk-trc20ctl -cmd explain  -txid c58c...          # 按交易号查钱到底进了哪个地址
//	bugpk-trc20ctl -cmd trace    -txid c58c... -hours 720
//	bugpk-trc20ctl -cmd settle   -order BP2026... -txid c58c... -hours 720
//	bugpk-trc20ctl -cmd credit   -user 1234 -amount 50.41 -remark "manual fix BP2026..."
//
// 配置来源与主服务一致：-config 指向 config.yaml，环境变量优先级更高。
// 只读命令（diagnose/explain/trace）不会写库；settle/credit 会改余额，必须显式给 -confirm。
//
// 排查顺序：先用 explain 确认链上真实收款方，再判断是地址不符还是金额不符，
// 最后才决定走 settle（订单可正常结算）还是 credit（只能人工补余额）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"bugpk-api/internal/config"
	"bugpk-api/internal/domain/payment"
	"bugpk-api/internal/pkg/masking"

	_ "github.com/go-sql-driver/mysql"
)

func main() {
	var (
		configPath = flag.String("config", "", "path to config.yaml (env vars override)")
		command    = flag.String("cmd", "diagnose", "diagnose|explain|trace|settle|credit|markpaid")
		orderNo    = flag.String("order", "", "payment order number (settle)")
		txid       = flag.String("txid", "", "TRC20 transaction id")
		amount     = flag.String("amount", "", "USDT amount, e.g. 50.41 (diagnose filter / credit amount)")
		userID     = flag.Int64("user", 0, "user id (credit)")
		remark     = flag.String("remark", "", "wallet ledger remark (credit)")
		hours      = flag.Int("hours", 720, "how far back to search the chain (trace/settle)")
		confirm    = flag.Bool("confirm", false, "required for writes (settle/credit)")
	)
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fail(err)
	}
	db, err := sql.Open("mysql", cfg.Database.DSN())
	if err != nil {
		fail(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	repo := payment.NewRepository(db)
	service := payment.NewService(repo, nil, payment.NewConfigCipher(cfg.Payment.ConfigSecret))

	switch *command {
	case "diagnose":
		diag, err := repo.DiagnoseTrc20(ctx, *amount)
		if err != nil {
			fail(err)
		}
		printJSON(diag)
	case "trace":
		if *txid == "" {
			fail(fmt.Errorf("-txid is required"))
		}
		diag, err := repo.DiagnoseTrc20(ctx, "")
		if err != nil {
			fail(err)
		}
		target := diag.Address
		if target == "" {
			fail(fmt.Errorf("TRC20 receiving address is not configured"))
		}
		transfer, found, err := payment.FindConfirmedTrc20Transfer(ctx, target, diag.Contract, "", *txid, time.Now().Add(-time.Duration(*hours)*time.Hour))
		if err != nil {
			fail(err)
		}
		if !found {
			printJSON(map[string]any{"found": false, "txid": *txid, "address": target})
			return
		}
		exists, matched, err := repo.Trc20Receipt(ctx, transfer.TxID)
		if err != nil {
			fail(err)
		}
		printJSON(map[string]any{
			"found": true, "transfer": transfer, "recorded": exists, "matchedOrderNo": matched,
			"candidates": diag.Orders,
		})
	case "explain":
		if *txid == "" {
			fail(fmt.Errorf("-txid is required"))
		}
		// 不查账户流水，直接读链上回执：钱进了哪个地址、转了多少，一次说清。
		explanation, err := payment.ExplainTrc20Transaction(ctx, *txid, "")
		if err != nil {
			fail(err)
		}
		result := map[string]any{"transaction": explanation}
		if explanation.To != "" {
			diag, err := repo.DiagnoseTrc20(ctx, explanation.Amount)
			if err == nil {
				result["configuredAddress"] = diag.Address
				result["addressMatchesConfig"] = strings.EqualFold(diag.Address, explanation.To)
				result["candidateOrders"] = diag.Orders
				if !strings.EqualFold(diag.Address, explanation.To) {
					result["verdict"] = "链上收款地址与后台配置的 TRC20 收款地址不一致：自动对账永远扫不到这笔钱"
				}
			}
		}
		printJSON(result)
	case "settle":
		if *orderNo == "" || *txid == "" {
			fail(fmt.Errorf("-order and -txid are required"))
		}
		if !*confirm {
			fail(fmt.Errorf("settle writes balance; re-run with -confirm"))
		}
		order, err := service.ForceSettleTrc20(ctx, *orderNo, *txid, "", time.Now().Add(-time.Duration(*hours)*time.Hour))
		if err != nil {
			fail(err)
		}
		printJSON(order)
	case "markpaid":
		// 只改状态、不加余额：用于「管理员已经手工补过款」的订单。
		if *orderNo == "" {
			fail(fmt.Errorf("-order is required"))
		}
		// 先展示将要发生什么，再要求 -confirm，避免误操作。
		before, err := repo.OrderByNo(ctx, *orderNo, 0, true)
		if err != nil {
			fail(err)
		}
		userIDNum, convErr := strconv.ParseInt(before.UserID, 10, 64)
		if convErr != nil {
			fail(fmt.Errorf("订单用户号无法解析: %v", convErr))
		}
		credited, err := repo.WalletCreditedSince(ctx, userIDNum, time.Now().AddDate(0, 0, -7), before.PayAmount)
		if err != nil {
			fail(err)
		}
		printJSON(map[string]any{
			"orderNo":           before.OrderNo,
			"userId":            before.UserID,
			"status":            before.Status,
			"payAmount":         before.PayAmount,
			"balanceCredited":   credited,
			"willTouchBalance":  false,
			"willWriteStatusTo": "PAID",
		})
		if !*confirm {
			fail(fmt.Errorf("markpaid only rewrites status; re-run with -confirm to apply"))
		}
		updated, err := repo.MarkOrderPaidManually(ctx, *orderNo, *remark, "bugpk-trc20ctl")
		if err != nil {
			fail(err)
		}
		printJSON(updated)
	case "credit":
		if *userID <= 0 || *amount == "" {
			fail(fmt.Errorf("-user and -amount are required"))
		}
		if !*confirm {
			fail(fmt.Errorf("credit writes balance; re-run with -confirm"))
		}
		if err := repo.CreditWalletManually(ctx, *userID, *amount, *remark); err != nil {
			fail(err)
		}
		printJSON(map[string]any{"credited": true, "userId": *userID, "amount": *amount})
	default:
		fail(fmt.Errorf("unknown -cmd %q", *command))
	}
}

func printJSON(value any) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "ERROR "+masking.Error(err))
	os.Exit(1)
}
