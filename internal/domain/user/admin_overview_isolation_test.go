package user

import (
	apperrors "bugpk-api/internal/pkg/errors"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type overviewIsolationProbe struct {
	repair             string
	repairError        bool
	stale              bool
	callsError         bool
	revenueError       bool
	paidRevenueError   bool
	allError           bool
	zeroValues         bool
	missingProjections bool
	allowRaw           bool
	partialCalls       bool
	partialGroup       string
	raw                atomic.Int32
}
type overviewIsolationConnector struct{ p *overviewIsolationProbe }

func (c overviewIsolationConnector) Connect(context.Context) (driver.Conn, error) {
	return &overviewIsolationConn{p: c.p}, nil
}
func (c overviewIsolationConnector) Driver() driver.Driver { return overviewIsolationDriver{p: c.p} }

type overviewIsolationDriver struct{ p *overviewIsolationProbe }

func (d overviewIsolationDriver) Open(string) (driver.Conn, error) {
	return &overviewIsolationConn{p: d.p}, nil
}

type overviewIsolationConn struct{ p *overviewIsolationProbe }

func (*overviewIsolationConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unsupported prepare")
}
func (*overviewIsolationConn) Close() error              { return nil }
func (*overviewIsolationConn) Begin() (driver.Tx, error) { return overviewIsolationTx{}, nil }
func (*overviewIsolationConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return overviewIsolationTx{}, nil
}

type overviewIsolationTx struct{}

func (overviewIsolationTx) Commit() error   { return nil }
func (overviewIsolationTx) Rollback() error { return nil }
func overviewRow(values ...driver.Value) driver.Rows {
	columns := make([]string, len(values))
	for i := range columns {
		columns[i] = fmt.Sprint(i)
	}
	return &adminWorkbenchFailureRows{columns: columns, values: [][]driver.Value{values}}
}
func (c *overviewIsolationConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	q := strings.ToLower(query)
	if c.p.missingProjections && (strings.Contains(q, "from bp_call_metric_minute") || strings.Contains(q, "from bp_call_user_metric_minute")) {
		return nil, errors.New("Error 1146 (42S02): projection table doesn't exist")
	}
	if c.p.allError {
		return nil, errors.New("database unavailable")
	}
	switch {
	case strings.Contains(q, "c.last_log_id,c.last_completed_at"):
		if c.p.repairError {
			return nil, errors.New("repair health unavailable")
		}
		phase := "IDLE"
		if c.p.repair == "RUNNING" {
			phase = "CLEAR"
		}
		at := time.Now()
		if c.p.stale {
			at = at.Add(-time.Hour)
		}
		count := int64(25)
		if c.p.zeroValues {
			count = 0
		}
		return overviewRow(count, at, "", c.p.repair, phase, int64(adminProjectionMetricMinute), count, time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)), nil
	case strings.Contains(q, "repair_status"):
		if c.p.repairError {
			return nil, errors.New("repair health unavailable")
		}
		phase := "IDLE"
		if c.p.repair == "RUNNING" {
			phase = "CLEAR"
		}
		return overviewRow(c.p.repair, int64(adminProjectionMetricMinute), phase, time.Now(), ""), nil
	case strings.Contains(q, "last_completed_at"):
		at := time.Now()
		if c.p.stale {
			at = at.Add(-time.Hour)
		}
		return overviewRow(at), nil
	case strings.Contains(q, "from bp_billing_revenue_projection"):
		if c.p.revenueError {
			return nil, errors.New("revenue read timed out")
		}
		if c.p.zeroValues {
			return overviewRow("0.0000"), nil
		}
		return overviewRow("138.2500"), nil
	case strings.Contains(q, "from bp_payment_order") && strings.Contains(q, "status='paid'"):
		// 「今日/累计收入」新口径：PAID 充值订单实付金额（与收入趋势同源）。
		if c.p.paidRevenueError {
			return nil, errors.New("paid revenue unavailable")
		}
		if c.p.zeroValues {
			return overviewRow("0.0000"), nil
		}
		return overviewRow("88.0000"), nil
	case strings.Contains(q, "select exists") && strings.Contains(q, "from bp_call_metric_minute"):
		if c.p.zeroValues {
			return overviewRow(int64(0)), nil
		}
		return overviewRow(int64(1)), nil
	case strings.Contains(q, "from bp_call_api_metric_day"):
		if c.p.zeroValues {
			return overviewRow("10-03", int64(0)), nil
		}
		return overviewRow("10-03", int64(25)), nil
	case strings.Contains(q, "from bp_call_metric_minute"):
		if c.p.callsError {
			return nil, errors.New("call projection unavailable")
		}
		values := []driver.Value{int64(25), int64(24), int64(1), int64(2), "3.5000"}
		if c.p.partialCalls || c.p.partialGroup == "calls" {
			values[1] = "invalid-count"
		}
		if c.p.zeroValues {
			values = []driver.Value{int64(0), int64(0), int64(0), int64(0), "0.0000"}
		}
		if strings.Contains(q, "group by") {
			values = append(append([]driver.Value{"10-03"}, values...), values[0])
		}
		return overviewRow(values...), nil
	case strings.Contains(q, "group by date_format(l.created_at"):
		// The source oracle for the tolerance gate. The empty variant must be a
		// genuine empty row set, not a single zero-valued row.
		if c.p.zeroValues {
			return &adminWorkbenchFailureRows{columns: []string{"day", "calls"}}, nil
		}
		return overviewRow("10-03", int64(25)), nil
	case strings.Contains(q, "from bp_interface_call_log"):
		if strings.Contains(q, "select exists") {
			if strings.Contains(q, "l.created_at>=?") {
				return overviewRow(int64(0)), nil
			}
			if c.p.zeroValues {
				return overviewRow(int64(0)), nil
			}
			return overviewRow(int64(1)), nil
		}
		c.p.raw.Add(1)
		if c.p.allowRaw {
			return overviewRow(int64(0), int64(0), int64(0), int64(0), "0.0000"), nil
		}
		return nil, errors.New("raw aggregation must not run")
	case strings.Contains(q, "from bp_user u"):
		if c.p.partialGroup == "users" {
			return overviewRow(int64(42), "invalid-count"), nil
		}
		if c.p.zeroValues {
			return overviewRow(int64(0), int64(0)), nil
		}
		return overviewRow(int64(42), int64(2)), nil
	case strings.Contains(q, "from bp_interface_api"):
		if c.p.partialGroup == "apis" {
			return overviewRow(int64(8), "invalid-count"), nil
		}
		if c.p.zeroValues {
			return overviewRow(int64(0), int64(0)), nil
		}
		return overviewRow(int64(8), int64(6)), nil
	case strings.Contains(q, "from bp_api_key"):
		if c.p.partialGroup == "keys" {
			return overviewRow(int64(12), "invalid-count"), nil
		}
		if c.p.zeroValues {
			return overviewRow(int64(0), int64(0)), nil
		}
		return overviewRow(int64(12), int64(9)), nil
	case strings.Contains(q, "from bp_friend_link_application"):
		if c.p.partialGroup == "pending" {
			return overviewRow(int64(3), "invalid-count"), nil
		}
		if c.p.zeroValues {
			return overviewRow(int64(0), int64(0)), nil
		}
		return overviewRow(int64(3), int64(4)), nil
	default:
		return nil, fmt.Errorf("unexpected query %s", q)
	}
}
func TestAdminOverviewIsolatesUnavailableMetrics(t *testing.T) {
	for _, tc := range []struct {
		name    string
		p       *overviewIsolationProbe
		missing string
	}{
		{name: "active repair", p: &overviewIsolationProbe{repair: "RUNNING"}, missing: "todayCalls"},
		{name: "unknown repair health", p: &overviewIsolationProbe{repairError: true}, missing: "todayCalls"},
		{name: "stale projection", p: &overviewIsolationProbe{repair: "NONE", stale: true}, missing: "todayCalls"},
		{name: "failed call query", p: &overviewIsolationProbe{repair: "NONE", callsError: true}, missing: "todayCalls"},
		{name: "failed revenue query", p: &overviewIsolationProbe{repair: "NONE", revenueError: true, paidRevenueError: true}, missing: "totalRevenue"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := sql.OpenDB(overviewIsolationConnector{p: tc.p})
			defer db.Close()
			service := &Service{repo: NewRepository(db), now: time.Now}
			result, err := service.loadAdminWorkbenchStats(context.Background(), []AdminWorkbenchSection{AdminWorkbenchSectionOverview}, "")
			if err != nil {
				t.Fatalf("independent metrics were discarded: %v", err)
			}
			if result.Overview.TotalUsers != 42 || result.Overview.APITotal != 8 || result.Overview.KeyTotal != 12 || result.Overview.PendingOrders != 3 || result.Overview.PendingFriendLinks != 4 {
				t.Fatalf("healthy metrics lost: %+v", result.Overview)
			}
			data, _ := json.Marshal(result.Overview)
			var payload map[string]any
			_ = json.Unmarshal(data, &payload)
			missing, ok := payload["unavailableFields"].([]any)
			if !ok {
				t.Fatalf("missing field-level availability: %s", data)
			}
			found := false
			for _, field := range missing {
				if field == tc.missing {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing availability for %s: %s", tc.missing, data)
			}
			if result.AnalyticsReady || len(result.DegradedSections) != 1 || result.DegradedSections[0] != AdminWorkbenchSectionOverview {
				t.Fatalf("partial data marked fully ready: %+v", result)
			}
			if tc.p.raw.Load() != 0 {
				t.Fatal("HTTP executed raw log aggregation")
			}
			if tc.missing == "todayCalls" && result.Overview.TotalRevenue != "88.0000" {
				t.Fatal("independent revenue lost")
			}
			if tc.missing == "totalRevenue" && result.Overview.TodayCalls != 25 {
				t.Fatal("healthy calls lost")
			}
		})
	}
}
func TestAdminOverviewHealthyIncludesEmptyUnavailableFields(t *testing.T) {
	p := &overviewIsolationProbe{repair: "NONE"}
	db := sql.OpenDB(overviewIsolationConnector{p: p})
	defer db.Close()
	service := &Service{repo: NewRepository(db), now: time.Now}
	result, err := service.loadAdminWorkbenchStats(context.Background(), []AdminWorkbenchSection{AdminWorkbenchSectionOverview}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !result.AnalyticsReady || result.Overview.TodayCalls != 25 || result.Overview.TodayRevenue != "88.0000" {
		t.Fatalf("unexpected %+v", result)
	}
	data, _ := json.Marshal(result.Overview)
	if !strings.Contains(string(data), "\"unavailableFields\":[]") {
		t.Fatalf("missing explicit health: %s", data)
	}
}

func TestAdminOverviewCompleteOutageRemainsRetryable(t *testing.T) {
	for _, sections := range [][]AdminWorkbenchSection{
		{AdminWorkbenchSectionOverview},
		{AdminWorkbenchSectionAlerts},
		{AdminWorkbenchSectionOverview, AdminWorkbenchSectionAlerts},
		allAdminWorkbenchSections,
	} {
		t.Run(fmt.Sprint(sections), func(t *testing.T) {
			p := &overviewIsolationProbe{allError: true}
			db := sql.OpenDB(overviewIsolationConnector{p: p})
			defer db.Close()
			service := &Service{repo: NewRepository(db), now: time.Now}
			_, err := service.loadAdminWorkbenchStats(context.Background(), sections, "")
			var business *apperrors.BusinessError
			if !errors.As(err, &business) || business.StatusCode != consts.StatusServiceUnavailable {
				t.Fatalf("total outage was disguised as usable statistics: %v", err)
			}
		})
	}
}

func TestAdminOverviewDoesNotPublishPartiallyScannedCallCounts(t *testing.T) {
	p := &overviewIsolationProbe{repair: "NONE", partialCalls: true}
	db := sql.OpenDB(overviewIsolationConnector{p: p})
	defer db.Close()
	service := &Service{repo: NewRepository(db), now: time.Now}
	got, err := service.loadAdminWorkbenchStats(context.Background(), []AdminWorkbenchSection{AdminWorkbenchSectionOverview}, "")
	if err != nil {
		t.Fatalf("healthy independent metrics lost: %v", err)
	}
	if got.Overview.TodayCalls != 0 || got.Overview.TodaySuccessRate != "" || got.Overview.TodayRevenue != "" {
		t.Fatalf("partially scanned metrics escaped: %+v", got.Overview)
	}
	if got.Overview.TotalUsers != 42 || got.Overview.TotalRevenue != "88.0000" {
		t.Fatal("independent successful reads were discarded")
	}
	if got.AnalyticsReady {
		t.Fatal("failed call scan was marked ready")
	}
}

func TestAdminOverviewMetricGroupsPublishAtomically(t *testing.T) {
	for _, tc := range []struct{ group, first, second string }{
		{"users", "totalUsers", "todayNewUsers"},
		{"apis", "apiTotal", "apiEnabled"},
		{"keys", "keyTotal", "keyEnabled"},
		{"pending", "pendingOrders", "pendingFriendLinks"},
		{"calls", "todayCalls", "todaySuccessCalls"},
	} {
		t.Run(tc.group, func(t *testing.T) {
			p := &overviewIsolationProbe{repair: "NONE", partialGroup: tc.group}
			db := sql.OpenDB(overviewIsolationConnector{p: p})
			defer db.Close()
			svc := &Service{repo: NewRepository(db), now: time.Now}
			got, err := svc.loadAdminWorkbenchStats(context.Background(), []AdminWorkbenchSection{AdminWorkbenchSectionOverview}, "")
			if err != nil {
				t.Fatalf("healthy metric groups discarded: %v", err)
			}
			raw, err := json.Marshal(got.Overview)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]any
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			if fields[tc.first] != float64(0) || fields[tc.second] != float64(0) {
				t.Fatalf("failed scan published half a metric group: %s", raw)
			}
			unavailable, ok := fields["unavailableFields"].([]any)
			if !ok {
				t.Fatalf("missing field availability: %s", raw)
			}
			for _, field := range []string{tc.first, tc.second} {
				found := false
				for _, name := range unavailable {
					if name == field {
						found = true
					}
				}
				if !found {
					t.Fatalf("failed field %s not marked unavailable", field)
				}
			}
			if got.AnalyticsReady || got.Overview.TotalRevenue != "88.0000" {
				t.Fatalf("readiness or independent revenue lost: %+v", got)
			}
		})
	}
}

func TestAdminOverviewSuccessfulZeroCountsRemainAvailable(t *testing.T) {
	p := &overviewIsolationProbe{repair: "NONE", zeroValues: true}
	db := sql.OpenDB(overviewIsolationConnector{p: p})
	defer db.Close()
	svc := &Service{repo: NewRepository(db), now: time.Now}
	got, err := svc.loadAdminWorkbenchStats(context.Background(), []AdminWorkbenchSection{AdminWorkbenchSectionOverview}, "")
	if err != nil {
		t.Fatalf("real zero counters were treated as outage: %v", err)
	}
	if !got.AnalyticsReady || len(got.DegradedSections) != 0 || len(got.Overview.UnavailableFields) != 0 {
		t.Fatalf("successful zero queries were marked unavailable: %+v", got)
	}
	if got.Overview.TodayCalls != 0 || got.Overview.TotalUsers != 0 || got.Overview.TotalRevenue != "0.0000" {
		t.Fatalf("unexpected empty-system counts: %+v", got.Overview)
	}
	if p.raw.Load() != 0 {
		t.Fatal("empty system triggered raw-log aggregation")
	}
}

func TestAdminOverviewMissingProjectionsCannotCertifyEmptyMoney(t *testing.T) {
	p := &overviewIsolationProbe{repair: "NONE", zeroValues: true, missingProjections: true}
	db := sql.OpenDB(overviewIsolationConnector{p: p})
	defer db.Close()
	svc := &Service{repo: NewRepository(db), now: time.Now}
	got, err := svc.loadAdminWorkbenchStats(context.Background(), []AdminWorkbenchSection{AdminWorkbenchSectionOverview}, "")
	if err != nil {
		t.Fatalf("independent real zero counters discarded: %v", err)
	}
	if got.AnalyticsReady {
		t.Fatalf("missing call projections certified empty amounts: %+v", got.Overview)
	}
	missing := map[string]bool{}
	for _, field := range got.Overview.UnavailableFields {
		missing[field] = true
	}
	for _, field := range []string{"todayCalls", "todayChargeAmount", "todayRevenue"} {
		if !missing[field] {
			t.Fatalf("missing projection field %s marked available", field)
		}
	}
	if p.raw.Load() != 0 {
		t.Fatal("HTTP missing-projection path scanned raw logs")
	}
}

func TestAdminOverviewNonHTTPFallbackReturnsCompleteZeroMetrics(t *testing.T) {
	p := &overviewIsolationProbe{repair: "NONE", zeroValues: true, missingProjections: true, allowRaw: true}
	db := sql.OpenDB(overviewIsolationConnector{p: p})
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := NewRepository(db).AdminOverview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.TodayCalls != 0 || got.TodayChargeAmount != "0.0000" || got.TodayRevenue != "0.0000" || len(got.UnavailableFields) != 0 {
		t.Fatalf("explicit source fallback did not return valid zero metrics: %+v", got)
	}
	if p.raw.Load() != 1 {
		t.Fatal("expected one explicit non-HTTP source read")
	}
}

// 收入口径回归：今日/累计收入与收入趋势同源（PAID 充值订单 pay_amount），
// 不再依赖接口计费投影；PAID 查询不可用时降级回原口径而非整卡不可用。
func TestAdminOverviewRevenueUsesPaidOrderSource(t *testing.T) {
	t.Run("paid orders preferred", func(t *testing.T) {
		p := &overviewIsolationProbe{repair: "NONE"}
		db := sql.OpenDB(overviewIsolationConnector{p: p})
		defer db.Close()
		service := &Service{repo: NewRepository(db), now: time.Now}
		result, err := service.loadAdminWorkbenchStats(context.Background(), []AdminWorkbenchSection{AdminWorkbenchSectionOverview}, "")
		if err != nil {
			t.Fatal(err)
		}
		// 今日 3.5000 是计费金额；88.0000 才是 PAID 订单实付口径。
		if result.Overview.TodayRevenue != "88.0000" || result.Overview.TotalRevenue != "88.0000" {
			t.Fatalf("revenue cards not aligned with paid orders: %+v", result.Overview)
		}
	})
	t.Run("paid query failure degrades to legacy source", func(t *testing.T) {
		p := &overviewIsolationProbe{repair: "NONE", paidRevenueError: true}
		db := sql.OpenDB(overviewIsolationConnector{p: p})
		defer db.Close()
		service := &Service{repo: NewRepository(db), now: time.Now}
		result, err := service.loadAdminWorkbenchStats(context.Background(), []AdminWorkbenchSection{AdminWorkbenchSectionOverview}, "")
		if err != nil {
			t.Fatal(err)
		}
		if result.Overview.TodayRevenue != "3.5000" {
			t.Fatalf("today revenue fallback lost charge amount: %+v", result.Overview)
		}
		if result.Overview.TotalRevenue != "138.2500" {
			t.Fatalf("total revenue fallback lost projection: %+v", result.Overview)
		}
		if slices.Contains(result.Overview.UnavailableFields, "totalRevenue") {
			t.Fatalf("degraded revenue marked unavailable: %+v", result.Overview.UnavailableFields)
		}
	})
}
