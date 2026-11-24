package analytics

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/pkg/testdb"
)

// stubResult lets a stubbed Exec report how many rows it "deleted", which is
// what drives the prune loop.
type stubResult int64

func (r stubResult) LastInsertId() (int64, error) { return 0, nil }
func (r stubResult) RowsAffected() (int64, error) { return int64(r), nil }

type retentionTestTx struct{ committed *bool }

func (tx retentionTestTx) Commit() error   { *tx.committed = true; return nil }
func (tx retentionTestTx) Rollback() error { return nil }

func TestRetentionDeletesUseReadCommittedBatches(t *testing.T) {
	for _, risk := range []bool{false, true} {
		t.Run(fmt.Sprint("risk=", risk), func(t *testing.T) {
			inBatch, committed := false, false
			db := testdb.OpenStub(t, testdb.SQLStub{
				Begin: func(_ context.Context, options driver.TxOptions) (driver.Tx, error) {
					if options.ReadOnly || options.Isolation != driver.IsolationLevel(sql.LevelReadCommitted) {
						return nil, fmt.Errorf("retention must avoid repeatable-read gap locks: %+v", options)
					}
					inBatch = true
					return retentionTestTx{&committed}, nil
				},
				Exec: func(_ context.Context, _ string, _ []driver.NamedValue) (driver.Result, error) {
					if !inBatch {
						return nil, fmt.Errorf("delete used the default isolation and can block live inserts")
					}
					return stubResult(0), nil
				},
			})
			repo := NewRepository(db)
			var err error
			if risk {
				_, err = repo.PruneRiskFingerprint(context.Background())
			} else {
				_, err = repo.pruneSweepTarget(context.Background(), datasetSweepTargets[len(datasetSweepTargets)-1])
			}
			if err != nil || !committed {
				t.Fatalf("retention batch: committed=%v error=%v", committed, err)
			}
		})
	}
}

func pruneStubDB(t *testing.T, affected func(call int) (int64, error)) (*sql.DB, *[]string) {
	t.Helper()
	var statements []string
	calls := 0
	db := testdb.OpenStub(t, testdb.SQLStub{
		Begin: func(_ context.Context, options driver.TxOptions) (driver.Tx, error) {
			if options.ReadOnly || options.Isolation != driver.IsolationLevel(sql.LevelReadCommitted) {
				return nil, fmt.Errorf("expected read committed retention")
			}
			committed := false
			return retentionTestTx{&committed}, nil
		},
		Exec: func(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
			calls++
			statements = append(statements, query)
			deleted, err := affected(calls)
			if err != nil {
				return nil, err
			}
			return stubResult(deleted), nil
		},
	})
	return db, &statements
}

func TestPruneRiskFingerprintDeletesOnlyExpiredBucketsInBoundedBatches(t *testing.T) {
	db, statements := pruneStubDB(t, func(call int) (int64, error) {
		if call <= 2 {
			return riskFingerprintPruneBatch, nil
		}
		return 0, nil
	})
	repository := &Repository{db: db}

	deleted, err := repository.PruneRiskFingerprint(context.Background())
	if err != nil {
		t.Fatalf("PruneRiskFingerprint: %v", err)
	}
	if want := int64(2 * riskFingerprintPruneBatch); deleted != want {
		t.Fatalf("deleted = %d, want %d", deleted, want)
	}
	if len(*statements) != 3 {
		t.Fatalf("issued %d deletes, want 3 (stop as soon as a short batch proves the tail is clean)", len(*statements))
	}
	for _, statement := range *statements {
		normalized := strings.ToLower(statement)
		if !strings.Contains(normalized, "delete from bp_call_risk_fingerprint_5m") {
			t.Fatalf("prune touched the wrong table: %s", statement)
		}
		if !strings.Contains(normalized, "where bucket_start < "+strings.ToLower(riskFingerprintWindowSQL)) {
			t.Fatalf("prune must bound itself by the retention window, not by an arbitrary date: %s", statement)
		}
		// bucket_start is the leading primary-key column, so this range delete
		// never has to scan the live tail.
		if !strings.Contains(normalized, "limit ?") {
			t.Fatalf("prune must stay batched: %s", statement)
		}
	}
}

func TestPruneRiskFingerprintStopsAfterMaxBatches(t *testing.T) {
	db, statements := pruneStubDB(t, func(int) (int64, error) {
		return riskFingerprintPruneBatch, nil
	})
	repository := &Repository{db: db}

	deleted, err := repository.PruneRiskFingerprint(context.Background())
	if err != nil {
		t.Fatalf("PruneRiskFingerprint: %v", err)
	}
	if want := int64(riskFingerprintPruneMaxBatches * riskFingerprintPruneBatch); deleted != want {
		t.Fatalf("deleted = %d, want %d", deleted, want)
	}
	if len(*statements) != riskFingerprintPruneMaxBatches {
		t.Fatalf("issued %d deletes, want the %d-batch cap so a neglected table cannot hold the worker", len(*statements), riskFingerprintPruneMaxBatches)
	}
}

func TestPruneRiskFingerprintIsCheapWhenNothingHasExpired(t *testing.T) {
	db, statements := pruneStubDB(t, func(int) (int64, error) { return 0, nil })
	repository := &Repository{db: db}

	deleted, err := repository.PruneRiskFingerprint(context.Background())
	if err != nil {
		t.Fatalf("PruneRiskFingerprint: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("deleted = %d, want 0", deleted)
	}
	if len(*statements) != 1 {
		t.Fatalf("issued %d deletes for an already-pruned table, want 1", len(*statements))
	}
}

func TestPruneRiskFingerprintReportsDeleteFailures(t *testing.T) {
	boom := fmt.Errorf("delete rejected")
	db, _ := pruneStubDB(t, func(int) (int64, error) { return 0, boom })
	repository := &Repository{db: db}

	if _, err := repository.PruneRiskFingerprint(context.Background()); err == nil {
		t.Fatal("PruneRiskFingerprint hid a database error")
	}
}

func TestPruneRiskFingerprintRejectsUnusableRepository(t *testing.T) {
	if _, err := (*Repository)(nil).PruneRiskFingerprint(context.Background()); err == nil {
		t.Fatal("nil repository must not report a successful prune")
	}
	if _, err := (&Repository{}).PruneRiskFingerprint(context.Background()); err == nil {
		t.Fatal("repository without a database must not report a successful prune")
	}
}

func TestRiskFingerprintRetentionCoversTheDetectionWindow(t *testing.T) {
	if riskFingerprintRetention <= 24*time.Hour {
		t.Fatalf("retention %v must leave a full 24h detection window intact", riskFingerprintRetention)
	}
	// The sweep is what keeps the promise, so it has to run often enough that a
	// late or failed sweep cannot leave materially more than the retention
	// behind before the next one lands.
	if riskFingerprintPruneInterval >= riskFingerprintRetention/4 {
		t.Fatalf("prune interval %v is too coarse for a %v retention", riskFingerprintPruneInterval, riskFingerprintRetention)
	}
	if !strings.Contains(riskFingerprintWindowSQL, "interval 26 hour") {
		t.Fatalf("window expression = %q, want the retention expressed in hours", riskFingerprintWindowSQL)
	}
}

// The probe compares a projection against its source over one window. Retention
// and coverage must therefore be the same expression: if the risk table is
// pruned to 26 hours while the probe still counts a month of source rows, the
// projection is reported incomplete forever and rebuilt on every audit.
func TestRiskProjectionProbeUsesTheRetentionWindow(t *testing.T) {
	var riskDescriptor repairProjectionDescriptor
	for _, descriptor := range repairProjectionDescriptors {
		if descriptor.index == projectionRiskFingerprint {
			riskDescriptor = descriptor
		}
	}
	if riskDescriptor.name == "" {
		t.Fatal("risk projection descriptor is missing")
	}
	if riskDescriptor.coverageWindow != riskFingerprintWindowSQL {
		t.Fatalf("risk coverage window = %q, want %q", riskDescriptor.coverageWindow, riskFingerprintWindowSQL)
	}
	query, args := projectionCoverageQuery(riskDescriptor, sqlNullTime(time.Time{}))
	if len(args) != 1 {
		t.Fatalf("risk coverage probe must bind exactly the dataset floor argument")
	}
	query = strings.ToLower(query)
	if !strings.Contains(query, "greatest(") || !strings.Contains(query, "coalesce(?, '1970-01-01')") {
		t.Fatalf("risk coverage probe must floor its window at the dataset start: %s", query)
	}
	if !strings.Contains(query, strings.ToLower(riskFingerprintWindowSQL)) {
		t.Fatalf("risk coverage probe ignores the retention window: %s", query)
	}

	for _, descriptor := range repairProjectionDescriptors {
		if descriptor.index == projectionRiskFingerprint {
			continue
		}
		query, _ := projectionCoverageQuery(descriptor, sqlNullTime(time.Time{}))
		query = strings.ToLower(query)
		if strings.Contains(query, "interval 26 hour") {
			t.Fatalf("%s unexpectedly inherited the risk retention window: %s", descriptor.name, query)
		}
		if !strings.Contains(query, fmt.Sprintf("interval %d day", rollupRepairWindowDays)) {
			t.Fatalf("%s lost the %d-day repair window: %s", descriptor.name, rollupRepairWindowDays, query)
		}
	}

	source := strings.ToLower(recentSourceCoverageQuery)
	if !strings.Contains(source, strings.ToLower(riskFingerprintWindowSQL)) {
		t.Fatalf("the source-side risk count must use the same retention window: %s", source)
	}
	if !strings.Contains(source, fmt.Sprintf("interval %d day", rollupRepairWindowDays)) {
		t.Fatalf("the source coverage probe must stay bounded: %s", source)
	}
}

// The sweep replaces the retired nightly cleanup cron. Its delete order is not
// cosmetic: the log must be trimmed before the projections it feeds, because
// the coverage probe floors every comparison at the log's oldest surviving day.
// Projections pruned ahead of the log would carry fewer days than the floor and
// fabricate a drift signal on the very next audit.
func TestSweepDatasetRetentionPrunesTheLogFirstWithMatchedBounds(t *testing.T) {
	db, statements := pruneStubDB(t, func(call int) (int64, error) {
		if call%3 != 0 {
			return datasetSweepBatch, nil
		}
		return 0, nil
	})
	repository := &Repository{db: db}

	deleted, err := repository.SweepDatasetRetention(context.Background())
	if err != nil {
		t.Fatalf("SweepDatasetRetention: %v", err)
	}
	if want := int64(len(datasetSweepTargets) * 2 * datasetSweepBatch); deleted != want {
		t.Fatalf("deleted = %d, want %d", deleted, want)
	}
	if got := len(*statements); got != len(datasetSweepTargets)*3 {
		t.Fatalf("issued %d deletes, want %d (two full batches plus one short tail per table)", got, len(datasetSweepTargets)*3)
	}

	first := strings.ToLower((*statements)[0])
	if !strings.Contains(first, "delete from bp_interface_call_log") {
		t.Fatalf("the sweep must prune the call log first, got %s", first)
	}
	if !strings.Contains(first, "created_at < "+retentionDayBoundSQL(callLogRetentionDays)) {
		t.Fatalf("log prune lost its retention bound: %s", first)
	}

	last := strings.ToLower((*statements)[len(*statements)-1])
	if !strings.Contains(last, "delete from bp_billing_record") {
		t.Fatalf("billing must be swept last, got %s", last)
	}
	if !strings.Contains(last, "revenue_projected=1") || !strings.Contains(last, "released") {
		t.Fatalf("billing prune must only delete settled or released rows: %s", last)
	}
	if strings.Contains(strings.ToLower(strings.Join(*statements, "\n")), "reserved") {
		t.Fatal("the sweep must never delete RESERVED billing rows")
	}

	for _, statement := range *statements {
		normalized := strings.ToLower(statement)
		if !strings.Contains(normalized, "limit ?") {
			t.Fatalf("every sweep delete must stay batched: %s", statement)
		}
		switch {
		case strings.Contains(normalized, "bp_interface_call_log"),
			strings.Contains(normalized, "_minute"):
			if !strings.Contains(normalized, retentionDayBoundSQL(callLogRetentionDays)) {
				t.Fatalf("log and minute layers must share the %d-day bound: %s", callLogRetentionDays, statement)
			}
		case strings.Contains(normalized, "_hour"):
			if !strings.Contains(normalized, retentionDayBoundSQL(hourLayerRetentionDays)) {
				t.Fatalf("hour layers must use the %d-day bound: %s", hourLayerRetentionDays, statement)
			}
		case strings.Contains(normalized, "bp_billing_record"):
			if !strings.Contains(normalized, retentionDayBoundSQL(billingRecordRetentionDays)) {
				t.Fatalf("billing prune lost its bound: %s", statement)
			}
		default:
			t.Fatalf("sweep touched an unexpected table: %s", statement)
		}
	}
}

func TestSweepDatasetRetentionAbortsWhenTheLogPruneFails(t *testing.T) {
	db, statements := pruneStubDB(t, func(int) (int64, error) {
		return 0, fmt.Errorf("log delete rejected")
	})
	repository := &Repository{db: db}

	if _, err := repository.SweepDatasetRetention(context.Background()); err == nil {
		t.Fatal("a failed log prune must fail the sweep")
	}
	if len(*statements) != 1 {
		t.Fatalf("issued %d deletes after the log prune failed, want 1: pruning projections past an unpruned log would fabricate drift", len(*statements))
	}
}

func TestSweepDatasetRetentionContinuesPastProjectionFailures(t *testing.T) {
	db, statements := pruneStubDB(t, func(call int) (int64, error) {
		if call == 3 {
			return 0, fmt.Errorf("minute delete rejected")
		}
		return 0, nil
	})
	repository := &Repository{db: db}

	_, err := repository.SweepDatasetRetention(context.Background())
	if err == nil || !strings.Contains(err.Error(), "bp_call_metric_minute") {
		t.Fatalf("a projection failure must be reported, got %v", err)
	}
	// Extra surviving rows are always safe (they fall outside the floored
	// comparison window), so the remaining tables are still swept.
	if got := len(*statements); got != len(datasetSweepTargets) {
		t.Fatalf("issued %d deletes, want one attempt per table (%d)", got, len(datasetSweepTargets))
	}
}

func TestSweepDatasetRetentionCapsBatchesPerTable(t *testing.T) {
	db, statements := pruneStubDB(t, func(int) (int64, error) {
		return datasetSweepBatch, nil
	})
	repository := &Repository{db: db}

	deleted, err := repository.SweepDatasetRetention(context.Background())
	if err != nil {
		t.Fatalf("SweepDatasetRetention: %v", err)
	}
	if want := int64(len(datasetSweepTargets) * datasetSweepMaxBatches * datasetSweepBatch); deleted != want {
		t.Fatalf("deleted = %d, want %d", deleted, want)
	}
	if got := len(*statements); got != len(datasetSweepTargets)*datasetSweepMaxBatches {
		t.Fatalf("issued %d deletes, want the %d-batch cap per table", got, datasetSweepMaxBatches)
	}
}

func TestDatasetRetentionInvariants(t *testing.T) {
	if minuteLayerRetentionDays != callLogRetentionDays {
		t.Fatalf("minute layers (%d) must retain at least the log's floor window (%d) or every probe audit flags drift", minuteLayerRetentionDays, callLogRetentionDays)
	}
	// The day-layer rebuild window reaches back to yesterday 00:00, which at
	// 23:59 is nearly 48 hours of log.
	if callLogRetentionDays < 2 {
		t.Fatalf("log retention %d days cannot cover the day-layer rebuild window", callLogRetentionDays)
	}
	if hourLayerRetentionDays < 30 {
		t.Fatalf("hour retention %d days is below the widest 30-day reader window", hourLayerRetentionDays)
	}

	dayLayerTables := map[string]bool{
		"bp_call_metric_day":      true,
		"bp_call_user_metric_day": true,
		"bp_call_api_metric_day":  true,
	}
	swept := map[string]bool{}
	for _, target := range datasetSweepTargets {
		swept[target.name] = true
		if dayLayerTables[target.name] {
			t.Fatalf("%s is a day layer that outlives the log; it must never be swept", target.name)
		}
		if target.name == "bp_call_risk_fingerprint_5m" {
			t.Fatal("the risk fingerprint table is owned by PruneRiskFingerprint and must not be swept twice")
		}
		if target.name != "bp_billing_record" && target.extraPredicate != "" {
			t.Fatalf("%s carries an unexpected predicate: %s", target.name, target.extraPredicate)
		}
	}
	if !swept["bp_interface_call_log"] || !swept["bp_billing_record"] {
		t.Fatal("the sweep must own the call log and settled billing rows")
	}
}

// The partition drop is an optimization on top of the bounded delete, not a
// replacement for the log-first ordering invariant. A wired hook must run before
// the first row delete, and its failure must abort the log phase so projections
// are never pruned past an unpruned log.
func TestSweepDatasetRetentionDropsExpiredPartitionsBeforeRowDelete(t *testing.T) {
	var dropRetainDays int
	db, stmts := pruneStubDB(t, func(call int) (int64, error) { return 0, nil })
	repository := &Repository{db: db}
	repository.dropExpiredPartitions = func(ctx context.Context, retainDays int) (int64, error) {
		dropRetainDays = retainDays
		return 3, nil
	}
	if _, err := repository.SweepDatasetRetention(context.Background()); err != nil {
		t.Fatalf("SweepDatasetRetention: %v", err)
	}
	if dropRetainDays != callLogRetentionDays {
		t.Fatalf("drop hook got retainDays=%d, want %d", dropRetainDays, callLogRetentionDays)
	}
	// The first statement must be the log delete (the drop hook already ran).
	first := strings.ToLower((*stmts)[0])
	if !strings.Contains(first, "delete from bp_interface_call_log") {
		t.Fatalf("the log must still be deleted first after partition drop, got %s", first)
	}
}

func TestSweepDatasetRetentionAbortsWhenPartitionDropFails(t *testing.T) {
	db, stmts := pruneStubDB(t, func(int) (int64, error) { return 0, nil })
	repository := &Repository{db: db}
	repository.dropExpiredPartitions = func(ctx context.Context, retainDays int) (int64, error) {
		return 0, fmt.Errorf("partition drop rejected")
	}
	if _, err := repository.SweepDatasetRetention(context.Background()); err == nil {
		t.Fatal("a failed partition drop must fail the sweep")
	}
	if len(*stmts) != 0 {
		t.Fatalf("issued %d deletes after partition drop failed, want 0", len(*stmts))
	}
}

// A repository built without the drop hook (the &Repository{db:...} form used
// throughout these tests) must skip partition drops entirely and keep the
// bounded delete as its only cleanup, so unpartitioned installations are
// unaffected.
func TestPruneCallLogSkipsDropWhenHookIsNil(t *testing.T) {
	db, stmts := pruneStubDB(t, func(int) (int64, error) { return 0, nil })
	repository := &Repository{db: db}
	target := datasetSweepTargets[0]
	if _, err := repository.pruneCallLog(context.Background(), target); err != nil {
		t.Fatalf("pruneCallLog: %v", err)
	}
	for _, s := range *stmts {
		if strings.Contains(strings.ToLower(s), "drop partition") {
			t.Fatalf("nil drop hook must not issue a partition drop: %s", s)
		}
	}
}
