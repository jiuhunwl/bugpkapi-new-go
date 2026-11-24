package gateway

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/pkg/dbx"
	"bugpk-api/internal/pkg/testdb"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type reservationRecoveryTx struct{ commit func() error }

func (t reservationRecoveryTx) Commit() error { return t.commit() }
func (reservationRecoveryTx) Rollback() error { return nil }

func TestReservationRecoveryRetainsHandleAndUsesBoundedRefund(t *testing.T) {
	for _, mode := range []string{"repository", "service", "cancelled request", "persistent recovery failure"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			commitErr := errors.New("lost reservation COMMIT acknowledgement")
			probeErr := errors.New("reservation state probe unavailable")
			recoveryErr := errors.New("dial root:synthetic-db-password@tcp(db.invalid:3306/private_db) token=synthetic-token failed")
			var logs *observer.ObservedLogs
			begins, refunds, released := 0, 0, 0
			db := testdb.OpenStub(t, testdb.SQLStub{
				Begin: func(c context.Context, opts driver.TxOptions) (driver.Tx, error) {
					begins++
					if begins == 2 {
						return nil, probeErr
					}
					if begins > 2 {
						deadline, ok := c.Deadline()
						if c.Err() != nil || !ok || time.Until(deadline) > billingReleaseTimeout {
							t.Error("refund inherited cancellation or lacks bounded deadline")
						}
					}
					if begins > 2 && mode == "persistent recovery failure" {
						return nil, recoveryErr
					}
					if begins == 1 {
						return reservationRecoveryTx{func() error {
							if mode == "cancelled request" {
								cancel()
							}
							return commitErr
						}}, nil
					}
					return reservationRecoveryTx{func() error { return nil }}, nil
				},
				Query: func(c context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
					q = strings.Join(strings.Fields(q), " ")
					switch {
					case strings.Contains(q, "primary_role.qps_limit"):
						return testdb.Rows([]driver.Value{int64(1)}), nil
					// 主套餐探测：返回空串表示用户未指定，走默认「全站套餐优先」。
					case strings.Contains(q, "then 'GLOBAL'"):
						return testdb.Rows([]driver.Value{""}), nil
					case strings.HasPrefix(q, "select id,qps_limit from bp_user_package"):
						return testdb.Rows(), nil
					case strings.Contains(q, "from bp_billing_record"):
						if !strings.Contains(q, "for update") || len(args) != 1 || args[0].Value != int64(7) {
							t.Fatal("refund did not lock the original billing handle")
						}
						return testdb.Rows([]driver.Value{int64(99), "BALANCE", "1.2500", int64(0), nil, "RESERVED"}), nil
					default:
						return nil, fmt.Errorf("unexpected recovery query: %s", q)
					}
				},
				Exec: func(c context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
					q = strings.Join(strings.Fields(q), " ")
					switch {
					case strings.HasPrefix(q, "update bp_user_package"):
						return driver.RowsAffected(0), nil
					case strings.HasPrefix(q, "update bp_user set balance=balance-?"):
						return driver.RowsAffected(1), nil
					case strings.HasPrefix(q, "insert into bp_billing_record"):
						return outcomeLogResult{}, nil
					case strings.HasPrefix(q, "insert into bp_wallet_transaction"):
						return driver.RowsAffected(1), nil
					case strings.HasPrefix(q, "update bp_user set balance=balance+?"):
						refunds++
						if args[0].Value != "1.2500" || args[1].Value != int64(99) {
							t.Fatalf("refund used wrong amount or owner: %v", args)
						}
						return driver.RowsAffected(1), nil
					case strings.HasPrefix(q, "update bp_billing_record set status='RELEASED'"):
						released++
						if args[2].Value != int64(7) {
							t.Fatal("released wrong billing record")
						}
						return driver.RowsAffected(1), nil
					default:
						return nil, fmt.Errorf("unexpected recovery write: %s", q)
					}
				},
			})
			repo := NewRepository(db)
			api := InterfaceConfig{ID: 1, APICode: "recovery", Price: "1.2500"}
			var res BillingReservation
			var err error
			if mode == "repository" {
				res, err = repo.ReserveBilling(ctx, api, 0, 99)
			} else {
				core, observed := observer.New(zap.ErrorLevel)
				logs = observed
				svc := NewService(repo, nil, nil, "test-secret")
				svc.SetLogger(zap.New(core))
				defer svc.Close(context.Background())
				res, err = svc.reserveForOpen(ctx, api, 0, 99)
			}
			if !errors.Is(err, dbx.ErrCommitUncertain) || !errors.Is(err, commitErr) {
				t.Fatalf("reservation error identity lost: %v", err)
			}
			if mode == "persistent recovery failure" {
				if !errors.Is(err, probeErr) || !errors.Is(err, recoveryErr) {
					t.Fatal("original probe or recovery error was discarded")
				}
				if res.BillingRecordID != 0 || refunds != 0 || released != 0 {
					t.Fatal("failed recovery admitted request or fabricated refund")
				}
				entries := logs.FilterMessage("open gateway reservation recovery failed").All()
				if len(entries) != 1 || entries[0].ContextMap()["billing_record_id"] != int64(7) {
					t.Fatalf("missing diagnostic identity: %+v", entries)
				}
				message, _ := entries[0].ContextMap()["error"].(string)
				if message == "" {
					t.Fatal("recovery diagnostic is empty")
				}
				for _, secret := range []string{"synthetic-db-password", "synthetic-token", "db.invalid", "private_db"} {
					if strings.Contains(message, secret) {
						t.Fatalf("recovery diagnostic leaked %q", secret)
					}
				}
			} else if mode == "repository" {
				if res.BillingRecordID != 7 || !res.Reserved || refunds != 0 {
					t.Fatalf("missing internal compensation handle: %+v refunds=%d", res, refunds)
				}
			} else {
				if res.BillingRecordID != 0 || res.Reserved || refunds != 1 || released != 1 {
					t.Fatalf("failed pre-call debit not refunded/rejected: res=%+v refunds=%d released=%d", res, refunds, released)
				}
			}
		})
	}
}
