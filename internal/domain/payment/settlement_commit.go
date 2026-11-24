package payment

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"bugpk-api/internal/pkg/dbx"
)

// commitPaidOrder recovers a lost COMMIT acknowledgement from the immutable
// local order identity, not from the user's current balance or entitlements.
// expectedChannel preserves provider identity even when providers share a trade-number column.
func (r *Repository) commitPaidOrder(ctx context.Context, tx *sql.Tx, orderID int64, tradeNo, buyerID string, expectedChannel string) error {
	return dbx.CommitWithVerification(ctx, tx, func(checkCtx context.Context) (bool, error) {
		return r.verifyPaidOrderCommit(checkCtx, orderID, tradeNo, buyerID, expectedChannel)
	})
}

func (r *Repository) verifyPaidOrderCommit(ctx context.Context, orderID int64, tradeNo, buyerID string, expectedChannel string) (bool, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var status, alipayTrade, otherTrade, storedBuyer, storedChannel string
	err = tx.QueryRowContext(ctx, "select status,coalesce(alipay_trade_no,''),coalesce(channel_trade_no,''),coalesce(buyer_id,''),coalesce(pay_channel,'ALIPAY') from bp_payment_order where id=? for update", orderID).Scan(&status, &alipayTrade, &otherTrade, &storedBuyer, &storedChannel)
	if errors.Is(err, sql.ErrNoRows) {
		return false, errors.New("payment commit witness is missing")
	}
	if err != nil {
		return false, err
	}
	if err := requirePaymentChannel(storedChannel, expectedChannel); err != nil {
		// A foreign witness cannot establish this operation's commit outcome.
		return false, err
	}
	storedTrade := alipayTrade
	if !strings.EqualFold(expectedChannel, ChannelAlipay) {
		storedTrade = otherTrade
	}
	if status == "PAID" && strings.TrimSpace(storedTrade) == "" {
		return false, errors.New("paid order commit witness has no trade number")
	}
	// A concurrent redelivery with the same identity may have settled the order.
	// This is success at the business boundary; the PAID row and its grants are
	// written atomically, and the existing row lock prevents duplicate grants.
	return status == "PAID" && tradeNo != "" && storedTrade == tradeNo && (storedBuyer == "" || buyerID == "" || storedBuyer == buyerID), nil
}
