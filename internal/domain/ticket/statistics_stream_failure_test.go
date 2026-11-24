package ticket

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"bugpk-api/internal/pkg/testdb"
)

type failingStatusRows struct {
	driver.Rows
	terminal error
}

func (r *failingStatusRows) Next(dest []driver.Value) error {
	err := r.Rows.Next(dest)
	if errors.Is(err, io.EOF) {
		return r.terminal
	}
	return err
}

func TestTicketStatisticsDoesNotPublishOrCachePartialStatusStream(t *testing.T) {
	failure := errors.New("status stream interrupted")
	var fail atomic.Bool
	fail.Store(true)
	var statusReads atomic.Int32
	db := testdb.OpenStub(t, testdb.SQLStub{Query: func(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
		if strings.Contains(q, "select status,count(*)") {
			statusReads.Add(1)
			if fail.Load() {
				return &failingStatusRows{Rows: testdb.Rows([]driver.Value{"OPEN", int64(1)}), terminal: failure}, nil
			}
			return testdb.Rows([]driver.Value{"OPEN", int64(7)}), nil
		}
		if strings.Contains(q, "select count(*)") {
			return testdb.Rows([]driver.Value{int64(7), int64(7), int64(0), int64(0), int64(0), nil, nil}), nil
		}
		return testdb.Rows(), nil
	}})
	repo := NewRepository(db)
	if result, err := repo.Statistics(context.Background(), 7); !errors.Is(err, failure) {
		t.Fatalf("partial stream was reported as complete: result=%+v err=%v", result, err)
	}
	fail.Store(false)
	result, err := repo.Statistics(context.Background(), 7)
	if err != nil || result.Status["OPEN"] != 7 || statusReads.Load() != 2 {
		t.Fatalf("failed snapshot poisoned the cache: result=%+v reads=%d err=%v", result, statusReads.Load(), err)
	}
}
