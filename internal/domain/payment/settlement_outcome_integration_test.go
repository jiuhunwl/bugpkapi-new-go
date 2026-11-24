package payment

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"bugpk-api/internal/pkg/dbx"
	"bugpk-api/internal/pkg/testdb"
	hertzapp "github.com/cloudwego/hertz/pkg/app"
	mysql "github.com/go-sql-driver/mysql"
)

var paymentFaultSequence atomic.Uint64

func paymentOutcomeDB(t *testing.T) (*sql.DB, *mysql.Config) {
	t.Helper()
	dsn := os.Getenv("BUGPK_PAYMENT_OUTCOME_DSN")
	if dsn == "" {
		t.Skip("requires explicit isolated payment outcome MySQL")
	}
	db, cfg := testdb.OpenIsolatedMySQL(t, dsn, os.Getenv("BUGPK_PAYMENT_OUTCOME_DATADIR"), "bugpk_pay")
	paths, err := filepath.Glob(filepath.Join("..", "..", "..", "migrations", "*.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	var schema strings.Builder
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		schema.Write(b)
		schema.WriteByte('\n')
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, schema.String()); err != nil {
		t.Fatal(err)
	}
	return db, cfg
}
func paymentFaultDB(t *testing.T, cfg *mysql.Config, before bool) (*sql.DB, *testdb.CommitFault) {
	t.Helper()
	fault := testdb.NewCommitFault(before)
	name := fmt.Sprintf("payment-fault-%d", paymentFaultSequence.Add(1))
	mysql.RegisterDialContext(name, func(ctx context.Context, address string) (net.Conn, error) {
		c, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", address)
		if err != nil {
			return nil, err
		}
		return fault.Wrap(c), nil
	})
	t.Cleanup(func() { mysql.DeregisterDialContext(name) })
	copied := *cfg
	copied.Net = name
	db, err := sql.Open("mysql", copied.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, fault
}
func seedPaymentOutcome(t *testing.T, db *sql.DB, id int64, kind, channel string) string {
	t.Helper()
	orderNo := fmt.Sprintf("BP_OUTCOME_%d", id)
	if _, err := db.Exec("insert into bp_user(id,username,password_hash,display_name,balance,points,primary_role_id) values(?,?,'test-only','Outcome',0.0000,0,2)", id, orderNo); err != nil {
		t.Fatal(err)
	}
	snapshot := `{"name":"Global","packageName":"Interface","specName":"Monthly","packageId":700,"interfaceId":800,"validDays":30,"dailyLimit":25,"qpsLimit":4,"points":50}`
	if _, err := db.Exec(`insert into bp_payment_order(order_no,user_id,order_type,product,pay_channel,subject,target_id,pay_amount,gift_amount,grant_points,status,expire_at,snapshot) values(?,?,?,'PAGE',?,'Outcome',700,10.0000,2.0000,50,'CREATED',date_add(now(),interval 30 minute),?)`, orderNo, id, kind, channel, snapshot); err != nil {
		t.Fatal(err)
	}
	return orderNo
}
func settlePaymentOutcome(ctx context.Context, repo *Repository, channel, orderNo, tradeNo, buyer string) error {
	if channel == ChannelAlipay {
		return repo.SettlePaidOrder(ctx, orderNo, AlipayTradeStatus{TradeNo: tradeNo, BuyerID: buyer, Success: true})
	}
	status := PayTradeStatus{OutTradeNo: orderNo, TradeNo: tradeNo, BuyerID: buyer, Total: "10.0000", Success: true}
	if channel == ChannelWechat {
		return repo.SettleWechatPaidOrder(ctx, orderNo, status)
	}
	return repo.SettleHupijiaoPaidOrder(ctx, channel, orderNo, status)
}
func assertPaymentOutcome(t *testing.T, db *sql.DB, id int64, kind string, paid bool) {
	t.Helper()
	var status, balance string
	var points int64
	var wallet, global, api int
	queries := []struct {
		q    string
		dest []any
	}{
		{"select status from bp_payment_order where user_id=?", []any{&status}},
		{"select cast(balance as char),points from bp_user where id=?", []any{&balance, &points}},
		{"select count(*) from bp_wallet_transaction where user_id=?", []any{&wallet}},
		{"select count(*) from bp_user_package_global where user_id=?", []any{&global}},
		{"select count(*) from bp_user_package_interface where user_id=?", []any{&api}},
	}
	for _, q := range queries {
		if err := db.QueryRow(q.q, id).Scan(q.dest...); err != nil {
			t.Fatal(err)
		}
	}
	wantStatus, wantBalance := "CREATED", "0.0000"
	var wantPoints int64
	var wantWallet, wantGlobal, wantAPI int
	if paid {
		wantStatus = "PAID"
		switch kind {
		case "RECHARGE":
			wantBalance = "12.0000"
			wantWallet = 1
		case "POINT_PACKAGE":
			wantPoints = 50
			wantWallet = 1
		case "GLOBAL_PACKAGE":
			wantGlobal = 1
		case "INTERFACE_PACKAGE":
			wantAPI = 1
		}
	}
	if status != wantStatus || balance != wantBalance || points != wantPoints || wallet != wantWallet || global != wantGlobal || api != wantAPI {
		t.Fatalf("settlement not atomic/exactly once: status=%s balance=%s points=%d wallet=%d global=%d api=%d; expected paid=%v kind=%s", status, balance, points, wallet, global, api, paid, kind)
	}
}

func TestPaymentSettlementCommitOutcomes(t *testing.T) {
	db, cfg := paymentOutcomeDB(t)
	id := int64(990100)
	for _, channel := range []string{ChannelAlipay, ChannelWechat, ChannelHupijiaoAlipay, ChannelHupijiaoWechat} {
		for _, kind := range []string{"RECHARGE", "POINT_PACKAGE", "GLOBAL_PACKAGE", "INTERFACE_PACKAGE"} {
			for _, before := range []bool{false, true} {
				id++
				t.Run(fmt.Sprintf("%s/%s/before=%v", channel, kind, before), func(t *testing.T) {
					orderNo := seedPaymentOutcome(t, db, id, kind, channel)
					tradeNo := "TRADE_" + orderNo
					faultDB, fault := paymentFaultDB(t, cfg, before)
					err := settlePaymentOutcome(context.Background(), NewRepository(faultDB), channel, orderNo, tradeNo, "buyer")
					if !fault.Triggered() {
						t.Fatal("COMMIT fault not triggered")
					}
					if before {
						if err == nil || errors.Is(err, dbx.ErrCommitUncertain) {
							t.Fatalf("durably uncommitted settlement misclassified: %v", err)
						}
					} else if err != nil {
						t.Fatalf("committed entitlement reported failure: %v", err)
					}
					assertPaymentOutcome(t, db, id, kind, !before)
					// Provider redelivery after a failed commit must grant once; an already-paid
					// callback is a read-only result and must not require another COMMIT ACK.
					if err := settlePaymentOutcome(context.Background(), NewRepository(db), channel, orderNo, tradeNo, "buyer"); err != nil {
						t.Fatal(err)
					}
					duplicateDB, duplicateFault := paymentFaultDB(t, cfg, false)
					if err := settlePaymentOutcome(context.Background(), NewRepository(duplicateDB), channel, orderNo, tradeNo, "buyer"); err != nil {
						t.Fatalf("paid duplicate needlessly failed on COMMIT: %v", err)
					}
					if duplicateFault.Triggered() {
						t.Fatal("read-only paid duplicate issued redundant COMMIT")
					}
					for _, identity := range [][2]string{{tradeNo + "_OTHER", "buyer"}, {tradeNo, "other-buyer"}} {
						if err := settlePaymentOutcome(context.Background(), NewRepository(db), channel, orderNo, identity[0], identity[1]); err == nil {
							t.Fatal("paid order accepted different payment identity")
						}
					}
					assertPaymentOutcome(t, db, id, kind, true)
				})
			}
		}
	}
}

func TestPaymentConcurrentRedeliveryWithLostCommit(t *testing.T) {
	db, cfg := paymentOutcomeDB(t)
	id := int64(990300)
	for _, channel := range []string{ChannelAlipay, ChannelWechat, ChannelHupijiaoAlipay, ChannelHupijiaoWechat} {
		for _, kind := range []string{"RECHARGE", "POINT_PACKAGE", "GLOBAL_PACKAGE", "INTERFACE_PACKAGE"} {
			id++
			t.Run(channel+"/"+kind, func(t *testing.T) {
				orderNo := seedPaymentOutcome(t, db, id, kind, channel)
				faultDB, fault := paymentFaultDB(t, cfg, false)
				repo := NewRepository(faultDB)
				start := make(chan struct{})
				results := make(chan error, 8)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				for i := 0; i < 8; i++ {
					go func() {
						<-start
						results <- settlePaymentOutcome(ctx, repo, channel, orderNo, "TRADE_"+orderNo, "buyer")
					}()
				}
				close(start)
				for i := 0; i < 8; i++ {
					if err := <-results; err != nil {
						t.Errorf("redelivery failed: %v", err)
					}
				}
				if !fault.Triggered() {
					t.Fatal("fault not triggered")
				}
				assertPaymentOutcome(t, db, id, kind, true)
			})
		}
	}
}

func TestPaymentCommitProbeWaitsForTransactionDecision(t *testing.T) {
	db, cfg := paymentOutcomeDB(t)
	for index, commit := range []bool{false, true} {
		t.Run(fmt.Sprint(commit), func(t *testing.T) {
			orderNo := seedPaymentOutcome(t, db, 990500+int64(index), "RECHARGE", ChannelAlipay)
			var orderID int64
			if err := db.QueryRow("select id from bp_payment_order where order_no=?", orderNo).Scan(&orderID); err != nil {
				t.Fatal(err)
			}
			tx, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if err := NewRepository(db).applySettlement(context.Background(), tx, orderID, 990500+int64(index), 700, "RECHARGE", "10.0000", "2.0000", 0, "{}", "CNY", ""); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec("update bp_payment_order set status='PAID',alipay_trade_no='probe-trade',buyer_id='buyer',settled_at=now() where id=?", orderID); err != nil {
				t.Fatal(err)
			}
			probeDB, err := sql.Open("mysql", cfg.FormatDSN())
			if err != nil {
				t.Fatal(err)
			}
			probeDB.SetMaxOpenConns(1)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer func() { tx.Rollback(); cancel(); probeDB.Close() }()
			var probeID int64
			if err := probeDB.QueryRowContext(ctx, "select connection_id()").Scan(&probeID); err != nil {
				t.Fatal(err)
			}
			type outcome struct {
				ok  bool
				err error
			}
			done := make(chan outcome, 1)
			go func() {
				ok, err := NewRepository(probeDB).verifyPaidOrderCommit(ctx, orderID, "probe-trade", "buyer", ChannelAlipay)
				done <- outcome{ok, err}
			}()
			waiting := false
			for until := time.Now().Add(2 * time.Second); time.Now().Before(until); {
				var count int
				if err := db.QueryRowContext(ctx, "select count(*) from performance_schema.data_lock_waits w join performance_schema.threads t on t.thread_id=w.requesting_thread_id where t.processlist_id=?", probeID).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count > 0 {
					waiting = true
					break
				}
				select {
				case result := <-done:
					t.Fatalf("probe guessed outcome before transaction completed: %+v", result)
				default:
				}
				time.Sleep(5 * time.Millisecond)
			}
			if !waiting {
				t.Fatal("probe did not wait for original order lock")
			}
			if commit {
				err = tx.Commit()
			} else {
				err = tx.Rollback()
			}
			if err != nil {
				t.Fatal(err)
			}
			result := <-done
			if result.err != nil || result.ok != commit {
				t.Fatalf("wrong durable outcome: %+v want=%v", result, commit)
			}
			assertPaymentOutcome(t, db, 990500+int64(index), "RECHARGE", commit)
		})
	}
}

func TestSignedAlipayNotifyAcknowledgesDurableSettlement(t *testing.T) {
	db, cfg := paymentOutcomeDB(t)
	signer := newTestAlipayClient(t)
	cipher := NewConfigCipher("test-payment-outcome-secret")
	encrypted, err := cipher.Encrypt(signer.cfg.AppPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	config := signer.cfg
	config.AppID = "2026000000000000"
	config.SellerID = "2088000000000000"
	if err := NewRepository(db).SaveAlipayConfig(context.Background(), config, encrypted, true); err != nil {
		t.Fatal(err)
	}
	for index, before := range []bool{false, true} {
		t.Run(fmt.Sprint(before), func(t *testing.T) {
			id := int64(990700 + index)
			seeded := seedPaymentOutcome(t, db, id, "RECHARGE", ChannelAlipay)
			values := validNotifyEnvelope()
			orderNo := fmt.Sprintf("BP20261006123456%012X", id)
			if _, err := db.Exec("update bp_payment_order set order_no=? where order_no=?", orderNo, seeded); err != nil {
				t.Fatal(err)
			}
			values.Set("out_trade_no", orderNo)
			values.Set("trade_no", fmt.Sprintf("20261006220000000000%06d", id))
			values.Set("notify_id", fmt.Sprintf("outcome-notify-%d", id))
			values.Set("buyer_id", "2088000000000001")
			values.Set("sign", mustSignParams(t, signer, values))
			faultDB, fault := paymentFaultDB(t, cfg, before)
			handler := NewHandler(NewService(NewRepository(faultDB), nil, cipher))
			invoke := func() (int, string) {
				c := hertzapp.NewContext(0)
				c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				c.Request.SetBodyString(values.Encode())
				handler.alipayNotify(context.Background(), c)
				return c.Response.StatusCode(), string(c.Response.Body())
			}
			code, body := invoke()
			if !fault.Triggered() {
				t.Fatal("HTTP callback never reached settlement COMMIT")
			}
			if before {
				if code != 400 || body != "failure" {
					t.Fatalf("uncommitted payment acknowledged: %d %s", code, body)
				}
			} else if code != 200 || body != "success" {
				t.Fatalf("committed payment misreported to provider: %d %s", code, body)
			}
			assertPaymentOutcome(t, db, id, "RECHARGE", !before)
			for i := 0; i < 2; i++ {
				if code, body := invoke(); code != 200 || body != "success" {
					t.Fatalf("valid redelivery failed: %d %s", code, body)
				}
			}
			assertPaymentOutcome(t, db, id, "RECHARGE", true)
			// Recovered commits must not weaken signature verification or trusted audit.
			values.Set("total_amount", "100.00")
			if code, body := invoke(); code != 400 || body != "failure" {
				t.Fatalf("tampered notification accepted: %d %s", code, body)
			}
			assertPaymentOutcome(t, db, id, "RECHARGE", true)
			var verified, processed, attempts int
			if err := db.QueryRow("select verified,processed,attempt_count from bp_payment_notify_log where notify_id=? and event_type='ALIPAY_NOTIFY'", values.Get("notify_id")).Scan(&verified, &processed, &attempts); err != nil {
				t.Fatal(err)
			}
			if verified != 1 || processed != 1 || attempts != 4 {
				t.Fatalf("audit state not durable/monotonic: verified=%d processed=%d attempts=%d", verified, processed, attempts)
			}
		})
	}
}

// These barriers model infrastructure failures after a real COMMIT OK. Direct
// witness deletion/corruption is fault injection, not a normal application API.
func TestPaymentLostAckProbeFailureBoundaries(t *testing.T) {
	db, cfg := paymentOutcomeDB(t)
	id := int64(991000)
	for _, channel := range []string{ChannelAlipay, ChannelWechat, ChannelHupijiaoAlipay, ChannelHupijiaoWechat} {
		for _, mode := range []string{"deleted order", "missing trade", "probe unavailable", "probe locked", "cancel original"} {
			id++
			t.Run(channel+"/"+mode, func(t *testing.T) {
				orderNo := seedPaymentOutcome(t, db, id, "RECHARGE", channel)
				faultDB, fault := paymentFaultDB(t, cfg, false)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var held *sql.Tx
				defer func() {
					if held != nil {
						held.Rollback()
					}
				}()
				var hookErr error
				fault.AfterCommit = func() error {
					switch mode {
					case "deleted order":
						_, hookErr = db.Exec("delete from bp_payment_order where order_no=?", orderNo)
					case "missing trade":
						query := "update bp_payment_order set channel_trade_no=null where order_no=?"
						if channel == ChannelAlipay {
							query = "update bp_payment_order set alipay_trade_no=null where order_no=?"
						}
						_, hookErr = db.Exec(query, orderNo)
					case "probe unavailable":
						hookErr = faultDB.Close()
					case "probe locked":
						held, hookErr = db.BeginTx(context.Background(), nil)
						if hookErr == nil {
							var rowID int64
							hookErr = held.QueryRow("select id from bp_payment_order where order_no=? for update", orderNo).Scan(&rowID)
						}
					case "cancel original":
						cancel()
					}
					return hookErr
				}
				started := time.Now()
				err := settlePaymentOutcome(ctx, NewRepository(faultDB), channel, orderNo, "TRADE_"+orderNo, "buyer")
				if !fault.Triggered() || hookErr != nil {
					t.Fatalf("post-COMMIT barrier failed: triggered=%v err=%v", fault.Triggered(), hookErr)
				}
				if mode == "cancel original" {
					if ctx.Err() == nil || err != nil {
						t.Fatalf("durable payment could not recover after originating context cancellation: %v", err)
					}
				} else if !errors.Is(err, dbx.ErrCommitUncertain) {
					t.Fatalf("unverifiable commit outcome must remain uncertain: %v", err)
				}
				if mode == "probe locked" {
					if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 5*time.Second {
						t.Fatalf("probe did not respect bounded waiting: elapsed=%s err=%v", time.Since(started), err)
					}
				}
				var balance string
				var wallet int
				if err := db.QueryRow("select cast(balance as char) from bp_user where id=?", id).Scan(&balance); err != nil {
					t.Fatal(err)
				}
				if err := db.QueryRow("select count(*) from bp_wallet_transaction where user_id=?", id).Scan(&wallet); err != nil {
					t.Fatal(err)
				}
				if balance != "12.0000" || wallet != 1 {
					t.Fatalf("probe mutated committed funds: balance=%s wallet=%d", balance, wallet)
				}
				if mode != "deleted order" {
					assertPaymentOutcome(t, db, id, "RECHARGE", true)
				}
			})
		}
	}
}
