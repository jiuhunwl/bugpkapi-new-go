package payment

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestPaymentCloseKeepsChannelAndTerminalState(t *testing.T) {
	db, _ := paymentOutcomeDB(t)
	repo := NewRepository(db)
	fixture := newChannelNotificationFixture(t, db)
	id := int64(999300)
	for _, channel := range []string{ChannelAlipay, ChannelWechat, ChannelHupijiaoAlipay, ChannelHupijiaoWechat} {
		for _, initial := range []string{"CREATED", "PAYING", "PAID", "CLOSED"} {
			t.Run(channel+"/"+initial, func(t *testing.T) {
				id++
				orderNo := seedChannelNotificationOrder(t, db, id, channel)
				if initial == "PAID" {
					request := fixture.request(t, channel, orderNo, fmt.Sprintf("20261009220000000000%06d", id), fmt.Sprintf("close-notify-%d", id), "paid")
					if code, body := fixture.deliver(t, channel, request); !channelNotificationResponseMatches(channel, code, body, true) {
						t.Fatalf("PAID setup failed: %d %s", code, body)
					}
				} else if _, err := db.Exec("update bp_payment_order set status=? where order_no=?", initial, orderNo); err != nil {
					t.Fatal(err)
				}
				before := readAtomicPaymentState(t, db, orderNo)
				foreign := ChannelAlipay
				if channel == ChannelAlipay {
					foreign = ChannelWechat
				}
				if err := repo.MarkOrderClosed(context.Background(), orderNo, foreign); !errors.Is(err, ErrPaymentChannelMismatch) {
					t.Errorf("foreign close accepted: %v", err)
				}
				if got := readAtomicPaymentState(t, db, orderNo); got != before {
					t.Errorf("foreign close mutated state: %+v vs %+v", got, before)
				}
				want := before
				if initial != "PAID" {
					want.Status = "CLOSED"
				}
				for repeat := 0; repeat < 2; repeat++ {
					if err := repo.MarkOrderClosed(context.Background(), orderNo, channel); err != nil {
						t.Fatal(err)
					}
					if got := readAtomicPaymentState(t, db, orderNo); got != want {
						t.Errorf("same-channel close/terminal replay changed assets: %+v vs %+v", got, want)
					}
				}
			})
		}
	}
}

func TestPaymentCloseBindsChannelAfterLockWait(t *testing.T) {
	db, _ := paymentOutcomeDB(t)
	repo := NewRepository(db)
	for index, source := range []string{ChannelAlipay, ChannelHupijiaoWechat} {
		t.Run(source, func(t *testing.T) {
			orderNo := seedChannelNotificationOrder(t, db, int64(999400+index), source)
			want := readAtomicPaymentState(t, db, orderNo)
			want.Channel = ChannelWechat
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			locker, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer locker.Rollback()
			var connectionID int64
			var channel string
			if err := locker.QueryRowContext(ctx, "select connection_id()").Scan(&connectionID); err != nil {
				t.Fatal(err)
			}
			if err := locker.QueryRowContext(ctx, "select pay_channel from bp_payment_order where order_no=? for update", orderNo).Scan(&channel); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			joined := false
			go func() { done <- repo.MarkOrderClosed(ctx, orderNo, source) }()
			defer func() {
				cancel()
				_ = locker.Rollback()
				if !joined {
					select {
					case <-done:
					case <-time.After(3 * time.Second):
						t.Error("close worker did not exit")
					}
				}
			}()
			if err := awaitOwnedPaymentLockWait(ctx, db, connectionID); err != nil {
				t.Fatal(err)
			}
			if _, err := locker.ExecContext(ctx, "update bp_payment_order set pay_channel=? where order_no=?", want.Channel, orderNo); err != nil {
				t.Fatal(err)
			}
			if err := locker.Commit(); err != nil {
				t.Fatal(err)
			}
			select {
			case err = <-done:
				joined = true
			case <-ctx.Done():
				t.Fatal("close did not finish after unlock")
			}
			if !errors.Is(err, ErrPaymentChannelMismatch) {
				t.Errorf("close used stale provider identity: %v", err)
			}
			if got := readAtomicPaymentState(t, db, orderNo); got != want {
				t.Errorf("foreign close changed state after lock wait: %+v vs %+v", got, want)
			}
		})
	}
}
