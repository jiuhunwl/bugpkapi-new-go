package user

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

var failingAdminWorkbenchDriverID atomic.Uint64

type failingAdminWorkbenchDriver struct{}

type failingAdminWorkbenchConn struct{}

func (failingAdminWorkbenchDriver) Open(string) (driver.Conn, error) {
	return failingAdminWorkbenchConn{}, nil
}

func (failingAdminWorkbenchConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not supported")
}

func (failingAdminWorkbenchConn) Close() error { return nil }

func (failingAdminWorkbenchConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not supported")
}

func (failingAdminWorkbenchConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return nil, errors.New("simulated analytics database outage")
}

type partialAdminWorkbenchDriver struct{}

type partialAdminWorkbenchConn struct{}

func (partialAdminWorkbenchDriver) Open(string) (driver.Conn, error) {
	return partialAdminWorkbenchConn{}, nil
}

func (partialAdminWorkbenchConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not supported")
}

func (partialAdminWorkbenchConn) Close() error { return nil }

func (partialAdminWorkbenchConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not supported")
}

func (partialAdminWorkbenchConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	lower := strings.ToLower(query)
	switch {
	case strings.Contains(lower, "from bp_payment_order"):
		return &adminWorkbenchFailureRows{
			columns: []string{"day", "count", "amount", "total_orders", "total_amount"},
		}, nil
	case strings.Contains(lower, "bp_call_api_metric_minute"):
		return nil, errors.New("simulated hot API projection failure")
	default:
		return nil, errors.New("unexpected partial workbench query")
	}
}

type adminWorkbenchFailureRows struct {
	columns []string
	values  [][]driver.Value
}

func (r adminWorkbenchFailureRows) Columns() []string { return r.columns }
func (r adminWorkbenchFailureRows) Close() error      { return nil }
func (r *adminWorkbenchFailureRows) Next(dest []driver.Value) error {
	if len(r.values) == 0 {
		return io.EOF
	}
	copy(dest, r.values[0])
	r.values = r.values[1:]
	return nil
}

// A database outage used to be swallowed by loadAdminWorkbenchStats, causing
// HTTP 200 with a complete-looking payload whose every metric was the Go zero
// value. The service must instead surface an explicit retryable server error
// when none of the requested data sections could be loaded.
func TestLoadAdminWorkbenchStatsDoesNotTurnCompleteDatabaseFailureIntoZeros(t *testing.T) {
	driverName := fmt.Sprintf("bugpk-admin-workbench-failing-%d", failingAdminWorkbenchDriverID.Add(1))
	sql.Register(driverName, failingAdminWorkbenchDriver{})
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	service := &Service{
		repo:             NewRepository(db),
		adminAccessCache: newAdminAccessResultCache(),
		now:              time.Now,
	}
	_, err = service.loadAdminWorkbenchStats(context.Background(), []AdminWorkbenchSection{
		AdminWorkbenchSectionOverview,
		AdminWorkbenchSectionCallTrend,
		AdminWorkbenchSectionRevenueTrend,
		AdminWorkbenchSectionHotAPIs,
		AdminWorkbenchSectionActiveUsers,
		AdminWorkbenchSectionAccess,
	}, "")
	if err == nil {
		t.Fatal("complete database failure was returned as a successful zero-valued workbench")
	}
	business, ok := apperrors.AsBusinessError(err)
	if !ok || business.StatusCode != consts.StatusServiceUnavailable {
		t.Fatalf("error = %T %v, want retryable HTTP 503 business error", err, err)
	}
}

func TestLoadAdminWorkbenchStatsMarksOnlyFailedSectionsAsDegraded(t *testing.T) {
	driverName := fmt.Sprintf("bugpk-admin-workbench-partial-%d", failingAdminWorkbenchDriverID.Add(1))
	sql.Register(driverName, partialAdminWorkbenchDriver{})
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	service := &Service{repo: NewRepository(db), now: time.Now}
	result, err := service.loadAdminWorkbenchStats(context.Background(), []AdminWorkbenchSection{
		AdminWorkbenchSectionRevenueTrend,
		AdminWorkbenchSectionHotAPIs,
	}, "")
	if err != nil {
		t.Fatalf("one failed section must not discard the successful section: %v", err)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		AnalyticsReady   *bool    `json:"analyticsReady"`
		DegradedSections []string `json:"degradedSections"`
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatal(err)
	}
	if contract.AnalyticsReady == nil || *contract.AnalyticsReady {
		t.Fatalf("partial response must explicitly report analyticsReady=false: %s", raw)
	}
	if want := []string{string(AdminWorkbenchSectionHotAPIs)}; !reflect.DeepEqual(contract.DegradedSections, want) {
		t.Fatalf("degradedSections = %v, want %v", contract.DegradedSections, want)
	}
}
