package payment

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	apperrors "bugpk-api/internal/pkg/errors"
)

// This is maintenance/fault injection, not a claim that ordinary users can
// mutate payment channels. The waiter must decide from the row it locks.
func TestPaymentSettlementBindsChannelAfterLockWait(t *testing.T) {
	db, _ := paymentOutcomeDB(t)
	fixture := newChannelNotificationFixture(t, db)
	id := int64(997000)
	for _, source := range []string{ChannelAlipay, ChannelWechat} {
		for _, paid := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/paid=%v", source, paid), func(t *testing.T) {
				id++
				orderNo := seedChannelNotificationOrder(t, db, id, source)
				tradeNo := fmt.Sprintf("20261009220000000000%06d", id)
				if paid {
					request := fixture.request(t, source, orderNo, tradeNo, fmt.Sprintf("atomic-paid-%d", id), "paid")
					if code, body := fixture.deliver(t, source, request); !channelNotificationResponseMatches(source, code, body, true) {
						t.Fatalf("cannot establish same-channel PAID fixture: %d %s", code, body)
					}
				}
				want := readAtomicPaymentState(t, db, orderNo)
				target := ChannelWechat
				if source == ChannelWechat {
					target = ChannelHupijiaoWechat
				}
				want.Channel = target
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				locker, err := db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer locker.Rollback()
				var connectionID int64
				var currentChannel string
				if err := locker.QueryRowContext(ctx, "select connection_id()").Scan(&connectionID); err != nil {
					t.Fatal(err)
				}
				if err := locker.QueryRowContext(ctx, "select pay_channel from bp_payment_order where order_no=? for update", orderNo).Scan(&currentChannel); err != nil {
					t.Fatal(err)
				}
				result := make(chan error, 1)
				joined := false
				go func() { result <- settlePaymentOutcome(ctx, NewRepository(db), source, orderNo, tradeNo, "") }()
				defer func() {
					cancel()
					_ = locker.Rollback()
					if !joined {
						select {
						case <-result:
						case <-time.After(3 * time.Second):
							t.Error("settlement worker did not exit")
						}
					}
				}()
				if err := awaitOwnedPaymentLockWait(ctx, db, connectionID); err != nil {
					t.Fatal(err)
				}
				if _, err := locker.ExecContext(ctx, "update bp_payment_order set pay_channel=? where order_no=?", target, orderNo); err != nil {
					t.Fatal(err)
				}
				if err := locker.Commit(); err != nil {
					t.Fatal(err)
				}
				select {
				case err = <-result:
					joined = true
				case <-ctx.Done():
					t.Fatal("settlement did not finish after releasing the order lock")
				}
				if !errors.Is(err, ErrPaymentChannelMismatch) {
					t.Errorf("waiter accepted stale channel identity: %v", err)
				}
				if got := readAtomicPaymentState(t, db, orderNo); got != want {
					t.Errorf("channel mismatch changed durable state: got=%+v want=%+v", got, want)
				}
			})
		}
	}
}

func awaitOwnedPaymentLockWait(parent context.Context, db *sql.DB, connectionID int64) error {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var count int
		err := db.QueryRowContext(ctx, `select count(*) from performance_schema.data_lock_waits w
			inner join performance_schema.threads blocker on blocker.thread_id=w.blocking_thread_id
			where blocker.processlist_id=?`, connectionID).Scan(&count)
		if err != nil {
			return err
		}
		if count > 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("no waiter on the owned order lock: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

type atomicPaymentState struct {
	Status, Channel, Balance, AlipayTrade, OtherTrade, Buyer string
	Wallet, Points, Global, API                              int64
}

func readAtomicPaymentState(t *testing.T, db *sql.DB, orderNo string) atomicPaymentState {
	t.Helper()
	var out atomicPaymentState
	err := db.QueryRow(`select o.status,o.pay_channel,cast(u.balance as char),coalesce(o.alipay_trade_no,''),coalesce(o.channel_trade_no,''),coalesce(o.buyer_id,''),
		(select count(*) from bp_wallet_transaction w where w.user_id=u.id),u.points,
		(select count(*) from bp_user_package_global g where g.user_id=u.id),
		(select count(*) from bp_user_package_interface i where i.user_id=u.id)
		from bp_payment_order o inner join bp_user u on u.id=o.user_id where o.order_no=?`, orderNo).
		Scan(&out.Status, &out.Channel, &out.Balance, &out.AlipayTrade, &out.OtherTrade, &out.Buyer, &out.Wallet, &out.Points, &out.Global, &out.API)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPaymentSyncRejectsUnknownChannelsBeforePaidShortcut(t *testing.T) {
	db, _ := paymentOutcomeDB(t)
	svc := NewService(NewRepository(db), nil, NewConfigCipher("test-only-unknown-channel"))
	id := int64(998000)
	for _, channel := range []string{"", ChannelBalance, "UNKNOWN"} {
		for _, status := range []string{"CREATED", "PAID"} {
			t.Run(channel+"/"+status, func(t *testing.T) {
				id++
				orderNo := seedChannelNotificationOrder(t, db, id, channel)
				if _, err := db.Exec("update bp_payment_order set status=? where order_no=?", status, orderNo); err != nil {
					t.Fatal(err)
				}
				_, err := svc.SyncUserOrder(context.Background(), Principal{ID: id}, orderNo)
				business, ok := apperrors.AsBusinessError(err)
				if !ok || business.StatusCode != 400 {
					t.Errorf("unknown channel was accepted or routed to a provider: err=%v", err)
				}
			})
		}
	}
}

func TestPaymentCommitWitnessRejectsAnotherProvider(t *testing.T) {
	db, _ := paymentOutcomeDB(t)
	repo := NewRepository(db)
	orderNo := seedPaymentOutcome(t, db, 999001, "RECHARGE", ChannelHupijiaoWechat)
	if err := settlePaymentOutcome(context.Background(), repo, ChannelHupijiaoWechat, orderNo, "witness-provider-trade", "buyer"); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := db.QueryRow("select id from bp_payment_order where order_no=?", orderNo).Scan(&id); err != nil {
		t.Fatal(err)
	}
	// Sharing a trade-number column does not make a Hupijiao record a WECHAT witness.
	ok, err := repo.verifyPaidOrderCommit(context.Background(), id, "witness-provider-trade", "buyer", ChannelWechat)
	if ok || err == nil {
		t.Errorf("foreign provider was accepted as this operation's durable witness: ok=%v err=%v", ok, err)
	}
}

func TestWechatNotificationRequiresBoundedSignedIdentity(t *testing.T) {
	db, _ := paymentOutcomeDB(t)
	fixture := newChannelNotificationFixture(t, db)
	for index, id := range []string{"", " spaced-id ", strings.Repeat("i", 129)} {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			userID := int64(999100 + index)
			orderNo := seedChannelNotificationOrder(t, db, userID, ChannelWechat)
			tradeNo := fmt.Sprintf("20261009220000000000%06d", userID)
			request := fixture.request(t, ChannelWechat, orderNo, tradeNo, id, "paid")
			if code, body := fixture.deliver(t, ChannelWechat, request); !channelNotificationResponseMatches(ChannelWechat, code, body, false) {
				t.Errorf("invalid signed event identity was acknowledged: %d %s", code, body)
			}
			assertChannelPaymentState(t, db, orderNo, ChannelWechat, tradeNo, "CREATED")
		})
	}
}
