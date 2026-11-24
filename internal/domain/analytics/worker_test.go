package analytics

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"
)

func TestIsTransientConnectionError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"driver bad conn", driver.ErrBadConn, true},
		{"mysql invalid connection", errors.New("select analytics source range: invalid connection"), true},
		{"mysql bad connection", errors.New("driver: bad connection"), true},
		{"connection reset", errors.New("read tcp: connection reset by peer"), true},
		{"broken pipe", errors.New("write tcp: broken pipe"), true},
		{"sql no rows", errors.New("sql: no rows in result set"), false},
		{"context canceled", context.Canceled, false},
		{"context deadline", context.DeadlineExceeded, false},
		{"lock wait timeout", errors.New("Error 1205: Lock wait timeout exceeded"), false},
		{"wrapped bad conn", errors.Join(errors.New("count analytics contributions"), driver.ErrBadConn), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTransientConnectionError(tc.err); got != tc.want {
				t.Fatalf("isTransientConnectionError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
