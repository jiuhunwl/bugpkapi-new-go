package observability

import (
	"bugpk-api/internal/pkg/testdb"
	"context"
	"database/sql/driver"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
	_ "time/tzdata"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
)

const hostZoneTestEnv = "BUGPK_OBSERVABILITY_TEST_HOST_ZONE"

// Set the simulated host zone before tests start, only in an isolated child.
// Never mutate time.Local while unrelated tests run in the parent process.
func TestMain(m *testing.M) {
	if zone := os.Getenv(hostZoneTestEnv); zone != "" {
		location, err := time.LoadLocation(zone)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		time.Local = location
	}
	os.Exit(m.Run())
}

func TestLogBusinessTimeIsHostIndependent(t *testing.T) {
	runLogTimeHostChildren(t, "TestLogBusinessTimeHostChild")
}

func runLogTimeHostChildren(t *testing.T, child string) {
	t.Helper()
	for _, zone := range []string{"UTC", "Etc/GMT-9", "Etc/GMT+7", "America/New_York"} {
		t.Run(zone, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			pattern := "^" + child + "$"
			if child == "TestLogBusinessTimeHostChild" {
				pattern = "^(" + child + "|TestParseCleanupBefore.*|TestDefaultCleanupCutoff.*|TestCallLogParamsNormalizesValidFilters|TestNormalizeCallLogFilterWindow.*)$"
			}
			cmd := exec.CommandContext(ctx, exe, "-test.run="+pattern, "-test.v")
			cmd.Env = append(os.Environ(), hostZoneTestEnv+"="+zone)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("host %s: %v\n%s", zone, err, out)
			}
			if !strings.Contains(string(out), "BUSINESS_TIME_HOST="+zone) {
				t.Fatalf("host child did not exercise assertions: %s", out)
			}
		})
	}
}

func TestLogBusinessTimeHostChild(t *testing.T) {
	zone := os.Getenv(hostZoneTestEnv)
	if zone == "" {
		t.Skip("only run inside host-zone subprocess")
	}
	t.Log("BUSINESS_TIME_HOST=" + zone)
	now := time.Date(2026, 12, 31, 17, 30, 0, 0, time.UTC)
	if got := dayLabels(now, 2); !reflect.DeepEqual(got, []string{"2026-12-31", "2027-01-01"}) {
		t.Errorf("log trend day labels used input clock zone: %v", got)
	}
	db := testdb.OpenStub(t, testdb.SQLStub{Query: func(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
		if strings.Contains(strings.ToLower(q), "count(distinct") {
			dates := []time.Time{}
			for _, arg := range args {
				if text, ok := arg.Value.(string); ok && len(text) == 19 {
					parsed, err := time.ParseInLocation("2006-01-02 15:04:05", text, time.FixedZone("expected +08", 8*60*60))
					if err == nil {
						dates = append(dates, parsed)
					}
				}
			}
			if len(dates) != 2 {
				t.Fatalf("operation statistics window not observed: %v", args)
			}
			for i, want := range []time.Duration{7 * 24 * time.Hour, 30 * 24 * time.Hour} {
				delta := time.Since(dates[i]) - want
				if delta < -5*time.Second || delta > 5*time.Second {
					t.Errorf("operation window was formatted in host zone: age=%s expected=%s", time.Since(dates[i]), want)
				}
			}
			return testdb.Rows([]driver.Value{int64(0), int64(0), int64(0), int64(0), int64(0)}), nil
		}
		return testdb.Rows(), nil
	}})
	if _, err := NewRepository(db).OperationLogStats(context.Background()); err != nil {
		t.Fatalf("operation stats fixture failed: %v", err)
	}

	c := hertzapp.NewContext(0)
	c.Request.SetRequestURI("/?startTime=" + url.QueryEscape("2026-12-31T17:00:00Z"))
	if got, err := optionalLogTime(c, "startTime"); err != nil || got != "2027-01-01 01:00:00" {
		t.Errorf("RFC offset was not normalized to storage clock: %q %v", got, err)
	}
	c.Request.SetRequestURI("/?startTime=" + url.QueryEscape("2026-03-08 02:30:00"))
	if got, err := optionalLogTime(c, "startTime"); err != nil || got != "2026-03-08 02:30:00" {
		t.Errorf("host DST rewrote a valid business time: %q %v", got, err)
	}
	params := CallLogListParams{Keyword: "test-keyword"}
	if err := normalizeCallLogFilterWindow(&params, now); err != nil {
		t.Fatal(err)
	}
	if params.StartTime != "2026-12-02 01:30:00" || params.EndTime != "2027-01-01 01:30:00" {
		t.Errorf("default search window shifted: %+v", params)
	}
	for _, input := range []string{"2027-01-01 01:00:00", "2026-12-31T17:00:00Z"} {
		got, err := parseCleanupBefore(input, now)
		if err != nil || got != "2027-01-01 01:00:00" {
			t.Errorf("cleanup cutoff shifted: input=%q got=%q err=%v", input, got, err)
		}
	}
	if _, err := parseCleanupBefore("2027-01-01 02:00:00", now); err == nil {
		t.Error("future business cutoff accepted; could delete beyond requested time")
	}
	if got := defaultCleanupCutoff(now); got != "2026-12-31 23:59:59" {
		t.Errorf("previous business day cutoff shifted: %s", got)
	}
}

func TestLogCleanupBusinessCutoffAgainstMySQL(t *testing.T) {
	if os.Getenv("BUGPK_BUSINESS_DATE_DSN") == "" {
		t.Skip("requires explicit isolated business-date MySQL")
	}
	runLogTimeHostChildren(t, "TestLogCleanupSQLHostChild")
}

func TestLogCleanupSQLHostChild(t *testing.T) {
	zone := os.Getenv(hostZoneTestEnv)
	if zone == "" {
		t.Skip("only run inside host-zone subprocess")
	}
	t.Log("BUSINESS_TIME_HOST=" + zone)
	db, _ := testdb.OpenIsolatedMySQL(t, os.Getenv("BUGPK_BUSINESS_DATE_DSN"), os.Getenv("BUGPK_BUSINESS_DATE_DATADIR"), "bugpk_datecutoff")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, "create table bp_interface_call_log(id bigint primary key,created_at datetime not null); insert into bp_interface_call_log values(1,'2027-01-01 00:59:59'),(2,'2027-01-01 01:00:00'),(3,'2027-01-01 01:59:59')"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 12, 31, 17, 30, 0, 0, time.UTC)
	cutoff, err := parseCleanupBefore("2026-12-31T17:00:00Z", now)
	if err != nil {
		t.Fatal(err)
	}
	removed, more, err := NewRepository(db).CleanupLogs(ctx, cutoff)
	if err != nil || more || removed != 1 {
		t.Fatalf("wrong business cutoff deletion: removed=%d more=%v err=%v cutoff=%s", removed, more, err, cutoff)
	}
	var ids string
	if err := db.QueryRowContext(ctx, "select group_concat(id order by id) from bp_interface_call_log").Scan(&ids); err != nil {
		t.Fatal(err)
	}
	if ids != "2,3" {
		t.Fatalf("cleanup removed boundary or newer records: %s", ids)
	}
	if _, err := parseCleanupBefore("2027-01-01 02:00:00", now); err == nil {
		t.Fatal("future cutoff could reach the deletion repository")
	}
}
