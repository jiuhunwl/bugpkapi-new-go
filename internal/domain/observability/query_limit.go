package observability

import (
	apperrors "bugpk-api/internal/pkg/errors"
	"context"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"time"
)

// Limit expensive log reads, not gateway writes. Waiting never consumes a SQL
// connection and is bounded separately from the query execution deadline.
func (r *Repository) acquireCallLogSlot(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	timer := time.NewTimer(200 * time.Millisecond)
	defer timer.Stop()
	select {
	case r.callLogSlots <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-r.callLogSlots
			return nil, err
		}
		return func() { <-r.callLogSlots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, apperrors.New(consts.StatusTooManyRequests, "Log queries are busy; narrow the time range or retry later")
	}
}
