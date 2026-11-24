package user

import (
	"context"
	"fmt"
)

// adminTodayOutcomes reads today's platform-wide outcome composition from the
// minute projection. It is deliberately separate from the certified admin call
// snapshot: a damaged or still-upgrading outcome layer must never blank the
// established call/revenue cards.
func (r *Repository) adminTodayOutcomes(ctx context.Context, suppressed bool) (CallOutcomeStats, error) {
	if suppressed {
		return CallOutcomeStats{}, nil
	}
	stats, rows, err := r.readTodayOutcomes(ctx, `
		select count(*),
		       coalesce(sum(call_count),0),coalesce(sum(success_count),0),
		       coalesce(sum(expected_empty_count),0),coalesce(sum(client_error_count),0),
		       coalesce(sum(throttle_count),0),coalesce(sum(upstream_error_count),0)
		from bp_call_metric_minute
		where bucket_start>=current_date() and bucket_start<date_add(current_date(), interval 1 day)
	`)
	if err != nil {
		return CallOutcomeStats{}, err
	}
	if rows == 0 {
		var source int
		if err := r.db.QueryRowContext(ctx, `
			select exists(select 1 from bp_call_metric_minute where bucket_start>=current_date() limit 1)
		`).Scan(&source); err != nil {
			return CallOutcomeStats{}, err
		}
		if source != 0 {
			return CallOutcomeStats{}, nil
		}
	}
	return stats, nil
}

// userTodayOutcomes reads one account's outcome composition for today.
func (r *Repository) userTodayOutcomes(ctx context.Context, userID int64) (CallOutcomeStats, error) {
	stats, rows, err := r.readTodayOutcomes(ctx, `
		select count(*),
		       coalesce(sum(call_count),0),coalesce(sum(success_count),0),
		       coalesce(sum(expected_empty_count),0),coalesce(sum(client_error_count),0),
		       coalesce(sum(throttle_count),0),coalesce(sum(upstream_error_count),0)
		from bp_call_user_metric_minute
		where user_id=? and bucket_start>=current_date() and bucket_start<date_add(current_date(), interval 1 day)
	`, userID)
	if err != nil {
		return CallOutcomeStats{}, err
	}
	if rows == 0 {
		var source int
		if err := r.db.QueryRowContext(ctx, `
			select exists(select 1 from bp_call_user_metric_minute
				where user_id=? and bucket_start>=current_date() limit 1)
		`, userID).Scan(&source); err != nil {
			return CallOutcomeStats{}, err
		}
		if source != 0 {
			return CallOutcomeStats{}, nil
		}
	}
	return stats, nil
}

func (r *Repository) readTodayOutcomes(ctx context.Context, query string, args ...any) (CallOutcomeStats, int64, error) {
	var (
		rows                                                       int64
		calls, success, expected, client, throttle, upstreamErrors int64
	)
	if err := r.db.QueryRowContext(ctx, query, args...).Scan(
		&rows, &calls, &success, &expected, &client, &throttle, &upstreamErrors,
	); err != nil {
		if isMissingTableError(err) ||
			isMissingAdminAnalyticsColumnError(err,
				"expected_empty_count", "client_error_count", "throttle_count", "upstream_error_count") {
			return CallOutcomeStats{}, 0, nil
		}
		return CallOutcomeStats{}, 0, err
	}
	stats, ok := buildCallOutcomeStats(calls, success, expected, client, throttle, upstreamErrors)
	if !ok {
		return CallOutcomeStats{}, rows, nil
	}
	return stats, rows, nil
}

// buildCallOutcomeStats validates the bucket invariant and computes the rate
// denominators. A projection row is only trusted when every call lands in one
// of the five buckets; a partial rebuild therefore reports unavailable instead
// of publishing a smaller denominator as the whole truth.
func buildCallOutcomeStats(calls, success, expected, client, throttle, upstream int64) (CallOutcomeStats, bool) {
	if calls < 0 || success < 0 || expected < 0 || client < 0 || throttle < 0 || upstream < 0 {
		return CallOutcomeStats{}, false
	}
	classified := success + expected + client + throttle + upstream
	if classified != calls {
		return CallOutcomeStats{}, false
	}
	valid := success + client + throttle + upstream
	upstreamAttributable := success + upstream
	return CallOutcomeStats{
		Available:            true,
		Calls:                calls,
		SuccessCalls:         success,
		ExpectedEmptyCalls:   expected,
		ClientErrorCalls:     client,
		ThrottledCalls:       throttle,
		UpstreamFailureCalls: upstream,
		SampleCalls:          valid,
		SuccessRate:          outcomeRate(success, valid),
		ServiceFailureRate:   outcomeRate(upstream, upstreamAttributable),
		ClientErrorRate:      outcomeRate(client, valid),
		ThrottleRate:         outcomeRate(throttle, valid),
		ExpectedEmptyRate:    outcomeRate(expected, calls),
	}, true
}

// outcomeRate returns a dash when the denominator has no sample, so a period
// with only expected-empty traffic never displays "0% service failure" as if
// upstream availability had been measured.
func outcomeRate(value, total int64) string {
	if total <= 0 {
		return "—"
	}
	return fmt.Sprintf("%.2f%%", float64(value)*100/float64(total))
}
