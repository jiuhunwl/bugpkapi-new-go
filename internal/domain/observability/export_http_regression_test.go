package observability

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/config"
	"bugpk-api/internal/domain/auth"
	"bugpk-api/internal/domain/rbac"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/network/standard"
)

// Exercises the actual handler and Hertz finalization over a real loopback
// connection. Only SQL rows are replaced; no live database is used.
func TestCallLogExportHTTPResponseBoundaries(t *testing.T) {
	for _, mode := range []string{"empty", "first-query-failure", "later-query-failure", "forbidden"} {
		t.Run(mode, func(t *testing.T) {
			db := sql.OpenDB(&exportHTTPConnector{mode: mode})
			defer db.Close()
			jwt, err := auth.NewJWTService(config.AuthConfig{Secret: strings.Repeat("a", 48), Issuer: "export-test", AccessTokenTTL: time.Minute})
			if err != nil {
				t.Fatal(err)
			}
			token, err := jwt.CreateAccessToken(auth.User{ID: 1, Username: "tester"}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			handler := NewHandler(NewService(NewRepository(db), rbac.NewService(rbac.NewRepository(db), jwt)))
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			h := server.New(server.WithListener(listener), server.WithTransport(standard.NewTransporter))
			h.GET("/export", handler.export)
			stopped := make(chan error, 1)
			go func() { stopped <- h.Run() }()
			defer func() {
				_ = h.Close()
				select {
				case <-stopped:
				case <-time.After(3 * time.Second):
					t.Error("export test server did not stop")
				}
			}()
			client := &http.Client{Timeout: 3 * time.Second}
			defer client.CloseIdleConnections()
			req, err := http.NewRequest(http.MethodGet, "http://"+listener.Addr().String()+"/export", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+token)
			response, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, readErr := io.ReadAll(response.Body)
			switch mode {
			case "empty":
				if readErr != nil || response.StatusCode != http.StatusOK || !strings.HasPrefix(string(body), "\xef\xbb\xbfID,Time,Interface") {
					t.Fatalf("empty CSV response: status=%d body=%q err=%v", response.StatusCode, body, readErr)
				}
				if !strings.Contains(response.Header.Get("Content-Disposition"), ".csv") {
					t.Fatal("missing CSV attachment")
				}
			case "later-query-failure":
				if !errors.Is(readErr, io.ErrUnexpectedEOF) {
					t.Fatalf("interrupted CSV must fail client read, got %v", readErr)
				}
				if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "ID,Time,Interface") || strings.Contains(string(body), "error_id") {
					t.Fatalf("interrupted CSV is not a pure partial CSV: status=%d body=%q", response.StatusCode, body)
				}
			default:
				expectedStatus := http.StatusInternalServerError
				if mode == "forbidden" {
					expectedStatus = http.StatusForbidden
				}
				if readErr != nil || response.StatusCode != expectedStatus || !strings.HasPrefix(string(body), "{") {
					t.Fatalf("error response: status=%d body=%q err=%v", response.StatusCode, body, readErr)
				}
				if response.Header.Get("Content-Disposition") != "" {
					t.Fatal("error JSON must not be a CSV attachment")
				}
			}
		})
	}
}

type exportHTTPConnector struct {
	mode    string
	batches int
}

func (c *exportHTTPConnector) Connect(context.Context) (driver.Conn, error) {
	return &exportHTTPConn{state: c}, nil
}
func (c *exportHTTPConnector) Driver() driver.Driver { return exportHTTPDriver{} }

type exportHTTPDriver struct{}

func (exportHTTPDriver) Open(string) (driver.Conn, error) { return nil, errors.New("use connector") }

type exportHTTPConn struct{ state *exportHTTPConnector }

func (*exportHTTPConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare unsupported")
}
func (*exportHTTPConn) Close() error              { return nil }
func (*exportHTTPConn) Begin() (driver.Tx, error) { return nil, errors.New("transaction unsupported") }
func (c *exportHTTPConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	switch {
	case strings.Contains(query, "from bp_user\n"):
		return &exportHTTPRows{columns: 7, rows: [][]driver.Value{{int64(1), "tester", int64(1), int64(0), "", "", int64(0)}}}, nil
	case strings.Contains(query, "select r.role_key"):
		role := "admin"
		if c.state.mode == "forbidden" {
			role = "user"
		}
		return &exportHTTPRows{columns: 1, rows: [][]driver.Value{{role}}}, nil
	case strings.Contains(query, "from bp_interface_call_log l"):
		c.state.batches++
		if c.state.mode == "first-query-failure" || c.state.batches > 1 {
			return nil, errors.New("test export query failed")
		}
		rows := &exportHTTPRows{columns: 19}
		if c.state.mode == "later-query-failure" {
			for id := callLogExportBatchSize + 1; id > 0; id-- {
				rows.rows = append(rows.rows, []driver.Value{int64(id), "2026-09-12 12:00:00", "Example API", "example", "tester", "key-prefix", "GET", int64(200), int64(1), int64(0), "FREE", "0.0000", int64(0), int64(1), "", "203.0.113.1", "trace", "", "{}"})
			}
		}
		return rows, nil
	case strings.Contains(query, "auth_code"):
		return &exportHTTPRows{columns: 1}, nil
	default:
		return nil, errors.New("unexpected SQL query in export test: " + query)
	}
}

type exportHTTPRows struct {
	columns int
	rows    [][]driver.Value
	offset  int
}

func (r *exportHTTPRows) Columns() []string {
	names := make([]string, r.columns)
	for i := range names {
		names[i] = "value"
	}
	return names
}
func (*exportHTTPRows) Close() error { return nil }
func (r *exportHTTPRows) Next(dest []driver.Value) error {
	if r.offset >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.offset])
	r.offset++
	return nil
}
