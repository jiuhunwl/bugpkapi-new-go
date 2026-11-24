package analytics

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/pkg/testdb"
)

func TestSourceAuditUsesReadOnlySnapshotAndBoundedPrimaryChunks(t *testing.T) {
	calls := 0
	db := testdb.OpenStub(t, testdb.SQLStub{Begin: func(_ context.Context, o driver.TxOptions) (driver.Tx, error) {
		if !o.ReadOnly || sql.IsolationLevel(o.Isolation) != sql.LevelRepeatableRead {
			t.Fatalf("unsafe audit transaction: %+v", o)
		}
		return auditTestTx{}, nil
	}, Query: func(_ context.Context, q string, a []driver.NamedValue) (driver.Rows, error) {
		calls++
		if !strings.Contains(q, "force index(primary)") || !strings.Contains(q, "order by id limit ?") {
			t.Fatalf("unbounded audit query: %s", q)
		}
		if len(a) != 7 || a[6].Value != int64(sourceAuditChunkSize) {
			t.Fatalf("audit chunk lost fixed bound: %v", a)
		}
		if calls == 1 {
			return testdb.Rows([]driver.Value{int64(2000), int64(2000), int64(1700), int64(100), int64(90), int64(300), time.Now(), time.Now(), time.Now(), time.Now()}), nil
		}
		return testdb.Rows([]driver.Value{int64(0), int64(0), int64(0), int64(0), int64(0), int64(0), nil, nil, nil, nil}), nil
	}})
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	c, err := auditSourceChunks(context.Background(), tx, 2000, time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if c.sourceCallCount != 1700 || c.sourceFailureCount != 100 || c.sourceFailureIPCount != 90 || c.sourceRiskCallCount != 300 || !c.sourceCoverageReady || calls != 2 {
		t.Fatalf("wrong audited totals: %+v calls=%d", c, calls)
	}
}

type auditTestTx struct{}

func (auditTestTx) Commit() error   { return nil }
func (auditTestTx) Rollback() error { return nil }

// The stubbed tests above never touch a real schema, so a derived table that
// forgets to project a column its own predicate needs fails only in production.
func TestBoundedSourceQueriesProjectEveryOutcomeColumn(t *testing.T) {
	for name, query := range map[string]string{"audit chunk": sourceAuditChunkQuery, "watermark probe": recentSourceWatermarksQuery} {
		start := strings.Index(strings.ToLower(query), "from (select ")
		if start < 0 {
			t.Fatalf("%s no longer uses a bounded derived table", name)
		}
		projection := query[start+len("from (select "):]
		projection = projection[:strings.Index(strings.ToLower(projection), " from bp_interface_call_log")]
		for _, column := range []string{"success", "response_status", "outcome_class"} {
			if !strings.Contains(projection, column) {
				t.Fatalf("%s derives outcome buckets but does not project %s: %s", name, column, projection)
			}
		}
	}
}
func TestSourceAuditDeadlineReturnsWithoutWritingCheckpoint(t *testing.T) {
	writes := 0
	db := testdb.OpenStub(t, testdb.SQLStub{Begin: testdb.ReadOnlyTransaction, Query: func(ctx context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
		if strings.Contains(q, "select last_log_id,") {
			return testdb.Rows([]driver.Value{int64(25), "NONE", "IDLE", time.Now().Add(-24 * time.Hour), time.Now().Add(-time.Hour)}), nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}, Exec: func(context.Context, string, []driver.NamedValue) (driver.Result, error) {
		writes++
		return driver.RowsAffected(1), nil
	}})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := NewRepository(db).AuditSourceCoverage(ctx); err == nil {
		t.Fatal("timed out audit must not report complete coverage")
	}
	if writes != 0 || time.Since(start) > time.Second {
		t.Fatalf("audit blocked/wrote ingestion state: writes=%d elapsed=%s", writes, time.Since(start))
	}
}
func TestSourceWatermarkProbeNeverScansAbsentRiskAcrossWholeLog(t *testing.T) {
	queries := 0
	db := testdb.OpenStub(t, testdb.SQLStub{Begin: testdb.ReadOnlyTransaction, Query: func(_ context.Context, q string, a []driver.NamedValue) (driver.Rows, error) {
		queries++
		if !strings.Contains(q, "force index(primary)") || !strings.Contains(q, "order by id desc limit ?") {
			t.Fatalf("watermark probe can scan millions when no risk rows exist: %s", q)
		}
		if len(a) != 2 || a[1].Value != int64(sourceAuditChunkSize) {
			t.Fatalf("unbounded watermark probe: %v", a)
		}
		return testdb.Rows([]driver.Value{time.Now(), nil, nil, nil}), nil
	}})
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	c, err := probeRecentSourceWatermarks(context.Background(), tx, 1000000)
	if err != nil {
		t.Fatal(err)
	}
	if queries != 1 || !c.sourceLatest.Valid || c.riskLatest.Valid {
		t.Fatalf("bounded no-risk watermark wrong: %+v", c)
	}
}
func TestSourceAuditRetriesWhenConcurrentIngestAdvancesCheckpoint(t *testing.T) {
	at := time.Now()
	reads := 0
	db := testdb.OpenStub(t, testdb.SQLStub{Begin: func(context.Context, driver.TxOptions) (driver.Tx, error) { return auditTestTx{}, nil }, Query: func(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
		switch {
		case strings.Contains(q, "for update"):
			return testdb.Rows([]driver.Value{int64(26), "NONE", "IDLE"}), nil
		case strings.Contains(q, "select last_log_id,"):
			return testdb.Rows([]driver.Value{int64(25), "NONE", "IDLE", at.Add(-24 * time.Hour), at.Add(-time.Hour)}), nil
		case strings.Contains(q, "order by id limit ?"):
			reads++
			if reads == 1 {
				return testdb.Rows([]driver.Value{int64(25), int64(25), int64(25), int64(0), int64(0), int64(0), at, nil, nil, nil}), nil
			}
			return testdb.Rows([]driver.Value{int64(0), int64(0), int64(0), int64(0), int64(0), int64(0), nil, nil, nil, nil}), nil
		case strings.Contains(q, "cast(min(created_at) as date)"):
			return testdb.Rows([]driver.Value{at.Add(-time.Hour)}), nil
		case strings.Contains(q, "select min(created_at)"):
			return testdb.Rows([]driver.Value{at.Add(-time.Hour)}), nil
		case strings.Contains(q, "coalesce(sum("):
			return testdb.Rows([]driver.Value{int64(0), int64(0), nil}), nil
		default:
			t.Fatalf("unexpected audit query %s", q)
			return nil, nil
		}
	}})
	scheduled, err := NewRepository(db).AuditSourceCoverage(context.Background())
	if scheduled || err == nil {
		t.Fatalf("stale audit must retry promptly, not wait another 24h: scheduled=%v err=%v", scheduled, err)
	}
}
