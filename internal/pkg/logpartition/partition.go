// Package logpartition manages optional, native MySQL monthly partitions of
// the existing call-log table. It never deletes logs or changes billing writes.
package logpartition

import (
	"fmt"
	"strings"
	"time"
)

const table = "bp_interface_call_log"
const maxMonthlyPartitions = 120

type Plan struct {
	SQL             string
	RequiresRebuild bool
}

type partition struct{ Name, Upper string }

func calendarMonth(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

func partitionBound(raw string) (time.Time, error) {
	raw = strings.Trim(raw, "'")
	for _, layout := range []string{"2006-01-02", "2006-01-02 15:04:05"} {
		if value, err := time.Parse(layout, raw); err == nil && value.Equal(calendarMonth(value)) {
			return value, nil
		}
	}
	return time.Time{}, fmt.Errorf("partition boundary is not a calendar month: %q", raw)
}

func buildPlan(first, now time.Time, ahead int, existing []partition) (Plan, error) {
	if ahead < 0 || ahead > 24 {
		return Plan{}, fmt.Errorf("months-ahead must be between 0 and 24")
	}
	if now.IsZero() {
		return Plan{}, fmt.Errorf("current time is required")
	}
	end := calendarMonth(now).AddDate(0, ahead+1, 0)
	start := calendarMonth(first)
	var definitions []string
	initial := len(existing) == 0
	if initial {
		if first.IsZero() {
			return Plan{}, fmt.Errorf("first log month is required")
		}
		if start.After(calendarMonth(now)) {
			start = calendarMonth(now)
		}
		definitions = append(definitions, fmt.Sprintf("PARTITION p_before VALUES LESS THAN ('%s')", start.Format("2006-01-02")))
	} else {
		if len(existing) < 3 || len(existing) > maxMonthlyPartitions+2 || existing[0].Name != "p_before" || existing[len(existing)-1] != (partition{Name: "p_future", Upper: "MAXVALUE"}) {
			return Plan{}, fmt.Errorf("unrecognized call-log partition layout; manual review required")
		}
		var err error
		start, err = partitionBound(existing[0].Upper)
		if err != nil {
			return Plan{}, err
		}
		for _, part := range existing[1 : len(existing)-1] {
			upper, err := partitionBound(part.Upper)
			if err != nil || part.Name != "p"+start.Format("200601") || !upper.Equal(start.AddDate(0, 1, 0)) {
				return Plan{}, fmt.Errorf("non-contiguous or unexpected monthly partition %q", part.Name)
			}
			start = upper
		}
		if !start.Before(end) {
			return Plan{}, nil
		}
	}
	added := 0
	for current := start; current.Before(end); current = current.AddDate(0, 1, 0) {
		added++
		existingMonths := 0
		if !initial {
			existingMonths = len(existing) - 2
		}
		if added+existingMonths > maxMonthlyPartitions {
			return Plan{}, fmt.Errorf("more than %d monthly partitions; review archival/retention before extending", maxMonthlyPartitions)
		}
		definitions = append(definitions, fmt.Sprintf("PARTITION p%s VALUES LESS THAN ('%s')", current.Format("200601"), current.AddDate(0, 1, 0).Format("2006-01-02")))
	}
	definitions = append(definitions, "PARTITION p_future VALUES LESS THAN (MAXVALUE)")
	prefix := "ALTER TABLE " + table + " ALGORITHM=INPLACE, REORGANIZE PARTITION p_future INTO (\n  "
	if initial {
		prefix = "ALTER TABLE " + table + " DROP PRIMARY KEY, ADD PRIMARY KEY (id, created_at)\nPARTITION BY RANGE COLUMNS (created_at) (\n  "
	}
	return Plan{SQL: prefix + strings.Join(definitions, ",\n  ") + "\n)", RequiresRebuild: initial}, nil
}
