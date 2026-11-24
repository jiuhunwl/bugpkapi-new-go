package observability

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/csv"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"bugpk-api/internal/domain/rbac"
	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

func TestSafeCSVCellPreventsFormulaInjection(t *testing.T) {
	tests := map[string]string{
		"=HYPERLINK(\"https://example.com\")": "'=HYPERLINK(\"https://example.com\")",
		"+cmd":                                "'+cmd",
		"-1+1":                                "'-1+1",
		"@SUM(A1:A2)":                         "'@SUM(A1:A2)",
		"\t=cmd":                              "'\t=cmd",
		"\r\n+cmd":                            "'\r\n+cmd",
		" normal":                             " normal",
		"中文":                                  "中文",
	}
	for input, expected := range tests {
		if got := safeCSVCell(input); got != expected {
			t.Fatalf("safeCSVCell(%q) = %q, want %q", input, got, expected)
		}
	}
}

func TestWriteCallLogCSVRowIncludesBOMSafeHeaderAndCells(t *testing.T) {
	var buffer bytes.Buffer
	buffer.Write([]byte{0xEF, 0xBB, 0xBF})
	writer := csv.NewWriter(&buffer)
	if err := writer.Write(callLogExportHeader); err != nil {
		t.Fatalf("write header: %v", err)
	}
	writeCallLogCSVRow(writer, CallLogItem{
		ID: "1", CreateTime: "2026-08-23 10:00:00", InterfaceName: "if", APICode: "code",
		Username: "u", APIKeyPrefix: "pk-1", RequestMethod: "GET", ResponseStatus: 200,
		Success: true, Billable: true, ChargeType: "BALANCE", ChargeAmount: "0.0100",
		PointPrice: 0, ElapsedMs: 12, ClientRegion: "r", ClientIP: "1.2.3.4",
		TraceID: "t1", ErrorMessage: "=cmd", RequestParams: "p",
	})
	writer.Flush()
	content := buffer.String()
	if !bytes.HasPrefix(buffer.Bytes(), []byte{0xEF, 0xBB, 0xBF}) {
		t.Fatalf("missing BOM")
	}
	if !strings.Contains(content, "ID,Time,Interface") {
		t.Fatalf("missing header: %s", content)
	}
	if !strings.Contains(content, "'=cmd") {
		t.Fatalf("formula injection not neutralized: %s", content)
	}
}

func TestCallLogExportHeaderMatchesStreamColumns(t *testing.T) {
	if len(callLogExportHeader) != 19 {
		t.Fatalf("export header has %d columns, want 19", len(callLogExportHeader))
	}
}

func TestStreamExportCallLogsAppliesFuzzyRateLimit(t *testing.T) {
	svc := NewService(NewRepository(nil), rbac.NewService(nil, nil))
	principal := rbac.Principal{ID: 7, Roles: []string{"admin"}}
	for i := 0; i < defaultFuzzyBurst; i++ {
		if err := svc.allowFuzzySearch(principal, CallLogListParams{Keyword: "warmup"}); err != nil {
			t.Fatalf("warm-up fuzzy request %d should be allowed: %v", i, err)
		}
	}
	var out bytes.Buffer
	err := svc.StreamExportCallLogs(context.Background(), principal, CallLogListParams{Keyword: "overflow"}, &out)
	if err == nil {
		t.Fatal("export with an exhausted fuzzy budget must be rejected")
	}
	business, ok := apperrors.AsBusinessError(err)
	if !ok || business.StatusCode != consts.StatusTooManyRequests {
		t.Fatalf("export fuzzy limit error = %v, want 429 BusinessError", err)
	}
	if out.Len() != 0 {
		t.Fatalf("rate-limited export must not write output, wrote %d bytes", out.Len())
	}
}

const observabilityExportDeadlineDriverName = "bugpk-observability-export-deadline"

func init() {
	sql.Register(observabilityExportDeadlineDriverName, &exportCtxProbe)
}

// exportCtxProbe 捕获导出批次查询实际收到的 ctx deadline；block=true 时
// 阻塞到 ctx 结束，用于验证总上限过期后导出循环会提前终止。
var exportCtxProbe exportDeadlineDriver

type exportDeadlineDriver struct {
	mu          sync.Mutex
	deadline    time.Time
	hasDeadline bool
	block       bool
}

func (d *exportDeadlineDriver) reset(block bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.deadline = time.Time{}
	d.hasDeadline = false
	d.block = block
}

func (d *exportDeadlineDriver) record(ctx context.Context) {
	deadline, ok := ctx.Deadline()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.deadline, d.hasDeadline = deadline, ok
}

func (d *exportDeadlineDriver) lastDeadline() (time.Time, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.deadline, d.hasDeadline
}

func (d *exportDeadlineDriver) shouldBlock() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.block
}

func (d *exportDeadlineDriver) Open(string) (driver.Conn, error) {
	return exportDeadlineConn{}, nil
}

type exportDeadlineConn struct{}

func (exportDeadlineConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not supported")
}
func (exportDeadlineConn) Close() error { return nil }
func (exportDeadlineConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not supported")
}

func (exportDeadlineConn) QueryContext(ctx context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
	exportCtxProbe.record(ctx)
	if exportCtxProbe.shouldBlock() {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return nil, context.DeadlineExceeded
}

func newExportDeadlineService(t *testing.T) *Service {
	t.Helper()
	db, err := sql.Open(observabilityExportDeadlineDriverName, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := NewRepository(db)
	// 抬高单批超时到总上限之上，使捕获到的 deadline 只能来自导出级 cap。
	repo.SetQueryTimeout(45 * time.Minute)
	return NewService(repo, rbac.NewService(nil, nil))
}

func TestStreamExportCallLogsBoundsBatchesWithOverallDeadline(t *testing.T) {
	exportCtxProbe.reset(false)
	svc := newExportDeadlineService(t)
	principal := rbac.Principal{ID: 11, Roles: []string{"admin"}}
	start := time.Now()
	var out bytes.Buffer
	if err := svc.StreamExportCallLogs(context.Background(), principal, CallLogListParams{}, &out); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("export must surface the batch query deadline, got %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("failed first batch must not commit the CSV response: %q", out.String())
	}
	deadline, ok := exportCtxProbe.lastDeadline()
	if !ok {
		t.Fatal("export batch query must run under a deadline-bounded context")
	}
	if ceiling := start.Add(callLogExportMaxDuration); deadline.After(ceiling) {
		t.Fatalf("export deadline %v exceeds the overall cap %v", deadline, ceiling)
	}
	if floor := start.Add(9 * time.Minute); deadline.Before(floor) {
		t.Fatalf("export deadline %v does not reflect the %v overall cap", deadline, callLogExportMaxDuration)
	}
}

func TestStreamExportCallLogsStopsEarlyWhenDeadlineExpires(t *testing.T) {
	exportCtxProbe.reset(true)
	svc := newExportDeadlineService(t)
	principal := rbac.Principal{ID: 12, Roles: []string{"admin"}}
	parent, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- svc.StreamExportCallLogs(parent, principal, CallLogListParams{}, &out)
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("interrupted export must report the deadline, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("export kept running after the overall deadline expired")
	}
}
