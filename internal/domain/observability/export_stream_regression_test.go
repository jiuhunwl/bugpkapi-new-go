package observability

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"bugpk-api/internal/domain/rbac"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/common/test/mock"
)

func TestExportWriterFlushesChunksWithoutBufferingWholeCSV(t *testing.T) {
	conn := mock.NewConn("")
	c := app.NewContext(0)
	c.SetConn(conn)
	writer := &streamingResponseWriter{c: c}
	payload := []byte("ID,Time\r\n1,2026-09-12\r\n")
	if n, err := writer.Write(payload); err != nil || n != len(payload) {
		t.Fatalf("Write() = %d, %v", n, err)
	}
	recorded := conn.WriterRecorder()
	wire, err := recorded.Peek(recorded.WroteLen())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(wire, payload) || !bytes.Contains(wire, []byte("Transfer-Encoding: chunked")) {
		t.Fatalf("export was not streamed to the connection: %q", wire)
	}
	if len(c.Response.Body()) != 0 {
		t.Fatalf("export retained %d bytes in response memory", len(c.Response.Body()))
	}
	if !strings.Contains(string(c.Response.Header.Peek("Content-Disposition")), ".csv") {
		t.Fatal("streamed export must remain a CSV attachment")
	}
}

func TestExportWriterPropagatesClientWriteFailure(t *testing.T) {
	c := app.NewContext(0)
	want := errors.New("client closed")
	c.Response.HijackWriter(&failingExportWriter{err: want})
	writer := &streamingResponseWriter{c: c}
	if _, err := writer.Write([]byte("row")); !errors.Is(err, want) {
		t.Fatalf("Write error = %v, want client error", err)
	}
}

type failingExportWriter struct{ err error }

func (w *failingExportWriter) Write([]byte) (int, error) { return 0, w.err }
func (w *failingExportWriter) Flush() error              { return nil }
func (w *failingExportWriter) Finalize() error           { return nil }

func TestExportFirstQueryFailureDoesNotCommitDownload(t *testing.T) {
	exportCtxProbe.reset(false)
	service := newExportDeadlineService(t)
	c := app.NewContext(0)
	c.SetConn(mock.NewConn(""))
	writer := &streamingResponseWriter{c: c}
	err := service.StreamExportCallLogs(context.Background(), rbac.Principal{ID: 11, Roles: []string{"admin"}}, CallLogListParams{}, writer)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("export error = %v", err)
	}
	if c.Response.GetHijackWriter() != nil || len(c.Response.Body()) != 0 {
		t.Fatal("failed first query committed a CSV download before reporting its error")
	}
}
