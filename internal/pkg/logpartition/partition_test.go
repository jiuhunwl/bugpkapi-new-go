package logpartition

import (
	"strings"
	"testing"
	"time"
)

func month(value string) time.Time {
	t, err := time.Parse("2006-01", value)
	if err != nil {
		panic(err)
	}
	return t
}

func TestInitialPlanPreservesHistoryAndFuture(t *testing.T) {
	p, err := buildPlan(month("2026-07"), month("2026-09"), 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !p.RequiresRebuild {
		t.Fatal("initial conversion must be marked as a rebuild")
	}
	for _, fragment := range []string{"DROP PRIMARY KEY, ADD PRIMARY KEY (id, created_at)", "PARTITION BY RANGE COLUMNS (created_at)", "p_before VALUES LESS THAN ('2026-07-01')", "p202607 VALUES LESS THAN ('2026-08-01')", "p202611 VALUES LESS THAN ('2026-12-01')", "p_future VALUES LESS THAN (MAXVALUE)"} {
		if !strings.Contains(p.SQL, fragment) {
			t.Fatalf("missing %q in %s", fragment, p.SQL)
		}
	}
	if strings.Contains(p.SQL, "DROP PARTITION") {
		t.Fatal("must never discard retained logs")
	}
}

func TestMaintenanceIsIdempotentAndHandlesYearBoundary(t *testing.T) {
	existing := []partition{{Name: "p_before", Upper: "'2026-11-01'"}, {Name: "p202611", Upper: "'2026-12-01'"}, {Name: "p_future", Upper: "MAXVALUE"}}
	p, err := buildPlan(time.Time{}, month("2026-12"), 1, existing)
	if err != nil {
		t.Fatal(err)
	}
	if p.RequiresRebuild || !strings.Contains(p.SQL, "REORGANIZE PARTITION p_future") || !strings.Contains(p.SQL, "p202701 VALUES LESS THAN ('2027-02-01')") {
		t.Fatalf("bad extension: %+v", p)
	}
	existing = append(existing[:2], partition{Name: "p202612", Upper: "'2027-01-01'"}, partition{Name: "p202701", Upper: "'2027-02-01'"}, partition{Name: "p_future", Upper: "MAXVALUE"})
	p, err = buildPlan(time.Time{}, month("2026-12"), 1, existing)
	if err != nil || p.SQL != "" {
		t.Fatalf("maintenance is not idempotent: %+v %v", p, err)
	}
}

func TestPlannerRejectsUnsafeOrExcessiveLayouts(t *testing.T) {
	for _, parts := range [][]partition{
		{{Name: "p_future", Upper: "MAXVALUE"}},
		{{Name: "p_before", Upper: "'2026-09-01'"}, {Name: "p202609", Upper: "'2026-11-01'"}, {Name: "p_future", Upper: "MAXVALUE"}},
		{{Name: "p_before", Upper: "'2026-09-01'"}, {Name: "p202609; DROP TABLE bp_user", Upper: "'2026-10-01'"}, {Name: "p_future", Upper: "MAXVALUE"}},
		{{Name: "p_before", Upper: "'2026-09-02'"}, {Name: "p202609", Upper: "'2026-10-01'"}, {Name: "p_future", Upper: "MAXVALUE"}},
	} {
		if _, err := buildPlan(time.Time{}, month("2026-09"), 2, parts); err == nil {
			t.Fatalf("accepted unsafe layout %+v", parts)
		}
	}
	for _, ahead := range []int{-1, 25} {
		if _, err := buildPlan(month("2026-09"), month("2026-09"), ahead, nil); err == nil {
			t.Fatalf("accepted monthsAhead=%d", ahead)
		}
	}
	if _, err := buildPlan(month("2000-01"), month("2026-09"), 2, nil); err == nil {
		t.Fatal("accepted unbounded historical partition count")
	}
}

func TestMonthCalculationUsesDatabaseCalendarNotDuration(t *testing.T) {
	now := time.Date(2026, 3, 31, 23, 59, 59, 0, time.FixedZone("local", 8*3600))
	p, err := buildPlan(month("2026-02"), now, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.SQL, "p202602 VALUES LESS THAN ('2026-03-01')") || !strings.Contains(p.SQL, "p202604 VALUES LESS THAN ('2026-05-01')") {
		t.Fatalf("bad month boundaries: %s", p.SQL)
	}
}

func TestMaintenanceNeverFallsBackToFullTableCopy(t *testing.T) {
	existing := []partition{{Name: "p_before", Upper: "'2026-09-01'"}, {Name: "p202609", Upper: "'2026-10-01'"}, {Name: "p_future", Upper: "MAXVALUE"}}
	plan, err := buildPlan(time.Time{}, month("2026-10"), 1, existing)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.SQL, "ALGORITHM=INPLACE") {
		t.Fatalf("maintenance must reject COPY fallback: %s", plan.SQL)
	}
}
