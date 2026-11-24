package analytics

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"bugpk-api/internal/pkg/testdb"
)

func TestRetentionBatchAllowsConcurrentBillingInsert(t *testing.T) {
	dsn := os.Getenv("BUGPK_ANALYTICS_ISOLATION_DSN")
	if dsn == "" {
		t.Skip("requires explicit disposable analytics MySQL")
	}
	db, _ := testdb.OpenIsolatedMySQL(t, dsn, os.Getenv("BUGPK_ANALYTICS_ISOLATION_DATADIR"), "bugpk_retention_lock")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, `create table bp_billing_record(
		id bigint primary key auto_increment,status varchar(32),created_at datetime,
		revenue_projected tinyint,key idx_status_time(status,created_at)
	) engine=InnoDB`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "insert into bp_billing_record(status,created_at,revenue_projected) values('CONFIRMED',now(),1)"); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(db)
	for _, legacy := range []bool{true, false} {
		t.Run(fmt.Sprintf("repeatable_read_control=%v", legacy), func(t *testing.T) {
			gate := fmt.Sprintf("retention-gate-%d", time.Now().UnixNano())
			seen := gate + "-seen"
			control, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer control.Close()
			if _, err := control.ExecContext(ctx, "do get_lock(?,0)", gate); err != nil {
				t.Fatal(err)
			}
			defer control.ExecContext(context.Background(), "do release_lock(?)", gate)
			// Pause after the real delete, while its transaction is still open.
			// Both keep the transaction open until the independent insert finishes.
			q := `delete from bp_billing_record where
				((status='CONFIRMED' and revenue_projected=1) or status='RELEASED')
				and created_at<date_sub(current_date(), interval 7 day) limit 5000;` +
				"do get_lock('" + seen + "',0); do get_lock('" + gate + "',10);" +
				"do release_lock('" + gate + "'); do release_lock('" + seen + "')"
			done := make(chan error, 1)
			go func() {
				if legacy {
					tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
					if err != nil {
						done <- err
						return
					}
					defer tx.Rollback()
					if _, err := tx.ExecContext(ctx, q); err != nil {
						done <- err
						return
					}
					done <- tx.Commit()
					return
				}
				_, err := repo.deleteRetentionBatch(ctx, q)
				done <- err
			}()
			deadline := time.Now().Add(5 * time.Second)
			for {
				var owner sql.NullInt64
				if err := db.QueryRowContext(ctx, "select is_used_lock(?)", seen).Scan(&owner); err != nil {
					t.Fatal(err)
				}
				if owner.Valid {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("retention did not reach the transaction barrier")
				}
				time.Sleep(5 * time.Millisecond)
			}
			writer, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
			if _, err := writer.ExecContext(ctx, "set session innodb_lock_wait_timeout=1"); err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			_, insertErr := writer.ExecContext(ctx, "insert into bp_billing_record(status,created_at,revenue_projected) values('RESERVED',now(),0)")
			t.Logf("insert elapsed=%v, error=%v", time.Since(started), insertErr)
			if legacy && insertErr == nil {
				t.Error("control did not reproduce the old range-lock conflict")
			}
			if !legacy && insertErr != nil {
				t.Errorf("retention blocked a new billing reservation: %v", insertErr)
			}
			if _, err := control.ExecContext(ctx, "do release_lock(?)", gate); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Run("settlement-predicate-is-preserved", func(t *testing.T) {
		if _, err := db.ExecContext(ctx, "delete from bp_billing_record"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `insert into bp_billing_record(id,status,created_at,revenue_projected) values
			(100,'RESERVED',date_sub(current_date(),interval 9 day),0),
			(101,'CONFIRMED',date_sub(current_date(),interval 9 day),0),
			(102,'CONFIRMED',date_sub(current_date(),interval 9 day),1),
			(103,'RELEASED',date_sub(current_date(),interval 9 day),0),
			(104,'CONFIRMED',now(),1)`); err != nil {
			t.Fatal(err)
		}
		affected, err := repo.pruneSweepTarget(ctx, datasetSweepTargets[len(datasetSweepTargets)-1])
		if err != nil || affected != 2 {
			t.Fatalf("settled retention: deleted=%d, err=%v", affected, err)
		}
		var ids string
		if err := db.QueryRowContext(ctx, "select group_concat(id order by id) from bp_billing_record").Scan(&ids); err != nil {
			t.Fatal(err)
		}
		if ids != "100,101,104" {
			t.Fatalf("reserved, unprojected or recent billing rows changed: %s", ids)
		}
	})
}
