package observability

import (
	"bugpk-api/internal/domain/rbac"
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestCallLogBusinessURLSurvivesAdminAndUserReadPipelines(t *testing.T) {
	raw := CallLogItem{
		RequestParams: `{"url":["https://video.example/watch/123?quality=hd&token=private-token"],"count":2}`,
		RequestValue:  `{"url":["https://video.example/watch/123?quality=hd&token=private-token"]}`,
		UpstreamURL:   "https://provider.example/internal/parse?apiKey=provider-key",
		ErrorMessage:  "Get https://provider.example/internal/parse: timeout",
		ResponseBody:  `{"endpoint":"https://provider.example/internal/parse"}`,
	}
	for _, mode := range []string{"admin-default", "admin-unmasked", "export", "user"} {
		t.Run(mode, func(t *testing.T) {
			item := raw
			if mode == "admin-default" || mode == "admin-unmasked" {
				sanitizeCallLogEndpointFields(&item)
			}
			if mode == "admin-default" || mode == "export" {
				item = maskCallLogItem(item)
			}
			params, value := item.RequestParams, item.RequestValue
			if mode == "user" {
				params = toUserCallLogItem(item).RequestParams
				value = params
			}
			for _, v := range []string{params, value} {
				if !json.Valid([]byte(v)) {
					t.Fatalf("invalid JSON: %s", v)
				}
				if !strings.Contains(v, "video.example/watch/123") || !strings.Contains(v, "quality=hd") {
					t.Fatalf("business URL was erased: %s", v)
				}
				if strings.Contains(v, "private-token") {
					t.Fatalf("URL credential leaked: %s", v)
				}
			}
			if mode != "user" {
				// Upstream routing configuration and diagnostics keep the strict
				// endpoint redaction.
				for _, v := range []string{item.UpstreamURL, item.ErrorMessage} {
					if strings.Contains(v, "provider.example") || strings.Contains(v, "provider-key") {
						t.Fatalf("provider leaked: %s", v)
					}
				}
				// The response body keeps the business payload the caller already
				// received; only credentials must stay masked.
				if strings.Contains(item.ResponseBody, "provider-key") {
					t.Fatalf("response body credential leaked: %s", item.ResponseBody)
				}
			}
		})
	}
}
func TestCallLogOldRedactionMarkerIsNotInventedBack(t *testing.T) {
	item := CallLogItem{RequestParams: `{"url":["[UPSTREAM_REDACTED]"]}`}
	sanitizeCallLogEndpointFields(&item)
	item = maskCallLogItem(item)
	if item.RequestParams != `{"url":["[UPSTREAM_REDACTED]"]}` {
		t.Fatalf("historical marker altered: %s", item.RequestParams)
	}
}

type requestURLConnector struct{}

func (*requestURLConnector) Connect(context.Context) (driver.Conn, error) {
	return &requestURLConn{}, nil
}
func (*requestURLConnector) Driver() driver.Driver { return exportHTTPDriver{} }

type requestURLConn struct{}

func (*requestURLConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unsupported") }
func (*requestURLConn) Begin() (driver.Tx, error)           { return nil, errors.New("unsupported") }
func (*requestURLConn) Close() error                        { return nil }
func (*requestURLConn) QueryContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	if !strings.Contains(q, "from bp_interface_call_log l") {
		return nil, errors.New("unexpected SQL: " + q)
	}
	params := `{"url":["https://video.example/watch/123?token=private-token&quality=hd"]}`
	if strings.Contains(q, "l.id,date_format(") {
		return &exportHTTPRows{columns: 19, rows: [][]driver.Value{{int64(1), "2026-10-02 00:00:00", "API", "video", "tester", "pk", "GET", int64(200), int64(1), int64(0), "FREE", "0.0000", int64(0), int64(1), "", "203.0.113.1", "trace", "", params}}}, nil
	}
	row := make([]driver.Value, 42)
	for i := range row {
		row[i] = ""
	}
	for _, i := range []int{0, 1, 3, 6, 12, 13, 14, 15, 19, 21, 22, 25, 37, 38, 39, 40} {
		row[i] = int64(0)
	}
	row[0], row[1], row[3], row[14], row[40] = int64(1), int64(1), int64(1), int64(1), int64(1)
	row[2], row[4], row[8], row[9], row[10], row[11] = "API", "tester", "video", "GET", params, params
	row[13], row[16], row[20], row[24], row[41] = int64(200), "FREE", "0.0000", "https://provider.example/private", "2026-10-02 00:00:00"
	if strings.Contains(q, "coalesce(b.response_body") || strings.Contains(q, "coalesce(l.response_body") {
		row = append(row, `{"endpoint":"https://provider.example/private"}`)
	}
	return &exportHTTPRows{columns: len(row), rows: [][]driver.Value{row}}, nil
}
func TestCallLogBusinessURLThroughRepositoryAndService(t *testing.T) {
	db := sql.OpenDB(&requestURLConnector{})
	defer db.Close()
	svc := NewService(NewRepository(db), rbac.NewService(rbac.NewRepository(db), nil))
	principal := rbac.Principal{ID: 1, Roles: []string{"admin"}}
	check := func(value string) {
		t.Helper()
		// Business URLs stay readable; credentials never survive any view.
		if !strings.Contains(value, "video.example/watch/123") || strings.Contains(value, "private-token") || strings.Contains(value, "provider-key") {
			t.Fatalf("incorrect service payload: %s", value)
		}
	}
	for _, raw := range []bool{false, true} {
		page, err := svc.AdminCallLogs(context.Background(), principal, CallLogListParams{Page: 1, PageSize: 10, SkipTotal: true, IncludeSensitive: raw})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("unexpected rows: %d", len(page.Items))
		}
		b, _ := json.Marshal(page)
		check(string(b))
		detail, err := svc.AdminCallLogDetail(context.Background(), principal, 1, raw)
		if err != nil {
			t.Fatal(err)
		}
		b, _ = json.Marshal(detail)
		check(string(b))
	}
	var csv bytes.Buffer
	if err := svc.StreamExportCallLogs(context.Background(), principal, CallLogListParams{}, &csv); err != nil {
		t.Fatal(err)
	}
	check(csv.String())
}
