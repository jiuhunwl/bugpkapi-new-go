package content

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func analyticsTestNow() time.Time {
	return time.Date(2026, 10, 13, 12, 0, 0, 0, time.Local)
}

// healthyInput is a caught-up pipeline with a freshly written snapshot.
func healthyInput() analyticsHealthInput {
	now := analyticsTestNow()
	newest := now.Add(-30 * time.Second)
	return analyticsHealthInput{
		WorkerEnabled:           true,
		NewestLogID:             900_000,
		NewestLogCreatedAt:      newest,
		RollupLastLogID:         900_000,
		RollupLastLogCreatedAt:  newest,
		RollupLastCompletedAt:   now.Add(-5 * time.Second),
		SnapshotSourceUpdatedAt: now.Add(-40 * time.Second),
		SnapshotCoveredUpTo:     newest,
		Now:                     now,
	}
}

func TestAnalyticsHealthTruthDecisionTable(t *testing.T) {
	now := analyticsTestNow()

	cases := []struct {
		name         string
		mutate       func(*analyticsHealthInput)
		wantReady    bool
		wantSections []string
	}{
		{
			name:      "caught up pipeline certifies the numbers",
			mutate:    func(*analyticsHealthInput) {},
			wantReady: true,
		},
		{
			name:         "rollup worker disabled never certifies",
			mutate:       func(in *analyticsHealthInput) { in.WorkerEnabled = false },
			wantReady:    false,
			wantSections: []string{AnalyticsSectionDisabled},
		},
		{
			name:         "seeded snapshot row is not ready",
			mutate:       func(in *analyticsHealthInput) { in.SnapshotSourceUpdatedAt = time.Time{} },
			wantReady:    false,
			wantSections: []string{AnalyticsSectionSnapshot},
		},
		{
			name: "stalled snapshot refresh is not ready",
			mutate: func(in *analyticsHealthInput) {
				in.SnapshotSourceUpdatedAt = now.Add(-homeAnalyticsSnapshotStallAfter - time.Minute)
			},
			wantReady:    false,
			wantSections: []string{AnalyticsSectionSnapshot},
		},
		{
			name: "site with no traffic at all reports a real zero",
			mutate: func(in *analyticsHealthInput) {
				in.NewestLogID = 0
				in.NewestLogCreatedAt = time.Time{}
				in.RollupLastLogID = 0
				in.RollupLastLogCreatedAt = time.Time{}
				in.RollupLastCompletedAt = time.Time{}
				in.SnapshotCoveredUpTo = time.Time{}
			},
			wantReady: true,
		},
		{
			name: "traffic exists but rollup never produced a watermark",
			mutate: func(in *analyticsHealthInput) {
				in.RollupLastLogID = 0
				in.RollupLastLogCreatedAt = time.Time{}
				in.RollupLastCompletedAt = time.Time{}
			},
			wantReady:    false,
			wantSections: []string{AnalyticsSectionRollup},
		},
		{
			name: "stalled rollup behind a non-empty backlog",
			mutate: func(in *analyticsHealthInput) {
				in.RollupLastLogID = in.NewestLogID - 50_000
				in.RollupLastLogCreatedAt = in.NewestLogCreatedAt.Add(-20 * time.Minute)
				in.RollupLastCompletedAt = now.Add(-homeAnalyticsWorkerStallAfter - time.Minute)
			},
			wantReady:    false,
			wantSections: []string{AnalyticsSectionRollup},
		},
		{
			name: "rollup that never completed a batch is not certified",
			mutate: func(in *analyticsHealthInput) {
				in.RollupLastLogID = in.NewestLogID - 10
				in.RollupLastCompletedAt = time.Time{}
			},
			wantReady:    false,
			wantSections: []string{AnalyticsSectionRollup},
		},
		{
			name: "transient backlog with a live worker stays certified",
			mutate: func(in *analyticsHealthInput) {
				in.RollupLastLogID = in.NewestLogID - 4_000
				in.RollupLastLogCreatedAt = in.NewestLogCreatedAt.Add(-2 * time.Second)
				in.RollupLastCompletedAt = now.Add(-time.Second)
			},
			wantReady: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := healthyInput()
			tc.mutate(&in)
			health := evaluateAnalyticsHealth(in)
			if health.Ready != tc.wantReady {
				t.Fatalf("Ready = %v, want %v (sections=%v)", health.Ready, tc.wantReady, health.DegradedSections)
			}
			if len(health.DegradedSections) != len(tc.wantSections) {
				t.Fatalf("DegradedSections = %v, want %v", health.DegradedSections, tc.wantSections)
			}
			for _, want := range tc.wantSections {
				found := false
				for _, got := range health.DegradedSections {
					if got == want {
						found = true
					}
				}
				if !found {
					t.Fatalf("DegradedSections = %v, missing %q", health.DegradedSections, want)
				}
			}
			// A degraded verdict must always name the fields a client is
			// forbidden from rendering, and a certified one must never do so.
			if health.Ready && len(health.UnavailableFields) != 0 {
				t.Fatalf("ready verdict still marks fields unavailable: %v", health.UnavailableFields)
			}
			if !health.Ready && len(health.UnavailableFields) == 0 {
				t.Fatalf("degraded verdict names no unavailable fields")
			}
		})
	}
}

func TestAnalyticsHealthIdleSiteReportsZeroLagNotStaleData(t *testing.T) {
	now := analyticsTestNow()
	newest := now.Add(-6 * time.Hour) // last call happened hours ago
	health := evaluateAnalyticsHealth(analyticsHealthInput{
		WorkerEnabled:           true,
		NewestLogID:             12,
		NewestLogCreatedAt:      newest,
		RollupLastLogID:         12,
		RollupLastLogCreatedAt:  newest,
		RollupLastCompletedAt:   now.Add(-time.Second),
		SnapshotSourceUpdatedAt: now.Add(-30 * time.Second),
		SnapshotCoveredUpTo:     newest,
		Now:                     now,
	})
	if !health.Ready {
		t.Fatalf("idle but caught-up site was degraded: %v", health.DegradedSections)
	}
	if health.LagSeconds != 0 {
		t.Fatalf("caught-up site reported lag %ds, want 0", health.LagSeconds)
	}
}

func TestAnalyticsHealthReportsLagAgainstNewestCallLog(t *testing.T) {
	now := analyticsTestNow()
	health := evaluateAnalyticsHealth(analyticsHealthInput{
		WorkerEnabled:           true,
		NewestLogID:             500,
		NewestLogCreatedAt:      now.Add(-90 * time.Second),
		RollupLastLogID:         400,
		RollupLastLogCreatedAt:  now.Add(-150 * time.Second),
		RollupLastCompletedAt:   now.Add(-time.Second),
		SnapshotSourceUpdatedAt: now.Add(-10 * time.Second),
		SnapshotCoveredUpTo:     now.Add(-150 * time.Second),
		Now:                     now,
	})
	if !health.Ready {
		t.Fatalf("transient backlog was degraded: %v", health.DegradedSections)
	}
	// The published numbers reach back 150s, the newest call is 90s old, so the
	// homepage trails live traffic by 60s.
	if health.LagSeconds != 60 {
		t.Fatalf("LagSeconds = %d, want 60", health.LagSeconds)
	}
	if got := health.CoveredUpToText(); got != now.Add(-150*time.Second).Format(homeAnalyticsCoveredUpToLayout) {
		t.Fatalf("CoveredUpToText = %q", got)
	}
}

func TestUnavailableAnalyticsHealthFailsClosed(t *testing.T) {
	health := unavailableAnalyticsHealth(true)
	if health.Ready {
		t.Fatal("fail-closed verdict claimed readiness")
	}
	if len(health.DegradedSections) == 0 || len(health.UnavailableFields) == 0 {
		t.Fatalf("fail-closed verdict is not explicit: %+v", health)
	}
}

// apiTotal, enabledApiTotal and userTotal are counted from live tables rather
// than rollups, so they must never be reported as unavailable.
func TestAnalyticsUnavailableFieldsExcludeLiveCounters(t *testing.T) {
	required := []string{"calls24h", "calls24hDeltaPercent", "availabilityPercent", "activeRegions24h", "peakQps24h"}
	for _, field := range required {
		found := false
		for _, listed := range homeAnalyticsUnavailableFields {
			if listed == field {
				found = true
			}
		}
		if !found {
			t.Fatalf("rollup-derived field %q is missing from the unavailable list", field)
		}
	}
	for _, live := range []string{"apiTotal", "enabledApiTotal", "userTotal", "availabilitySource"} {
		for _, listed := range homeAnalyticsUnavailableFields {
			if listed == live {
				t.Fatalf("live counter %q must stay available when rollups degrade", live)
			}
		}
	}
}

func TestPlatformStatsSerializesTruthEnvelope(t *testing.T) {
	healthy, err := json.Marshal(applyAnalyticsHealth(PlatformStats{Calls24h: 12}, AnalyticsHealth{Enabled: true, Ready: true}))
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{`"analyticsReady":true`, `"degradedSections":[]`, `"unavailableFields":[]`} {
		if !strings.Contains(string(healthy), fragment) {
			t.Fatalf("healthy stats missing %s: %s", fragment, healthy)
		}
	}

	degraded, err := json.Marshal(applyAnalyticsHealth(PlatformStats{Calls24h: 0}, unavailableAnalyticsHealth(true)))
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{`"analyticsReady":false`, `"calls24h"`, `"rollup"`} {
		if !strings.Contains(string(degraded), fragment) {
			t.Fatalf("degraded stats missing %s: %s", fragment, degraded)
		}
	}
}

func TestStripAnalyticsHealthRemovesReadTimeVerdict(t *testing.T) {
	stripped := stripAnalyticsHealth(applyAnalyticsHealth(PlatformStats{Calls24h: 5}, unavailableAnalyticsHealth(true)))
	if stripped.AnalyticsReady || stripped.CoveredUpTo != "" || stripped.LagSeconds != 0 {
		t.Fatalf("read-time verdict survived the strip: %+v", stripped)
	}
	if stripped.DegradedSections != nil || stripped.UnavailableFields != nil {
		t.Fatalf("read-time sections survived the strip: %+v", stripped)
	}
	if stripped.Calls24h != 5 {
		t.Fatalf("strip dropped measured data: %+v", stripped)
	}

	// A stored payload may only ever carry the neutral verdict. If it could
	// store `true`, a snapshot written while the worker was healthy would keep
	// certifying itself after the worker died.
	payload, err := json.Marshal(HomeAnalyticsSnapshot{Stats: stripped})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), `"analyticsReady":true`) {
		t.Fatalf("persisted payload certifies itself: %s", payload)
	}
	if !strings.Contains(string(payload), `"analyticsReady":false`) {
		t.Fatalf("persisted payload is not explicitly neutral: %s", payload)
	}
}

// Even a tampered payload that claims readiness must not survive composition:
// the live probe always wins.
func TestStoredReadinessClaimIsOverriddenByTheLiveProbe(t *testing.T) {
	tampered := HomeAnalyticsSnapshot{Stats: PlatformStats{Calls24h: 7, AnalyticsReady: true}}
	composed := composeHomeOverview(
		SiteConfig{}, HomeTicker{}, tampered, unavailableAnalyticsHealth(true),
	)
	if composed.Stats.AnalyticsReady {
		t.Fatalf("stored readiness claim survived composition: %+v", composed.Stats)
	}
	if len(composed.Stats.UnavailableFields) == 0 {
		t.Fatalf("degraded composition named no unavailable fields: %+v", composed.Stats)
	}
	if composed.Stats.Calls24h != 7 {
		t.Fatalf("composition dropped the stored measurement: %+v", composed.Stats)
	}
}

func TestCoveredUpToTextRoundTrip(t *testing.T) {
	if got := formatCoveredUpTo(time.Time{}); got != "" {
		t.Fatalf("zero coverage formatted as %q, want empty", got)
	}
	value := time.Date(2026, 10, 13, 9, 8, 7, 0, time.Local)
	text := formatCoveredUpTo(value)
	if text != "2026-10-13 09:08:07" {
		t.Fatalf("coverage text = %q", text)
	}
	if parsed := parseCoveredUpTo(text); !parsed.Equal(value) {
		t.Fatalf("coverage round trip = %v, want %v", parsed, value)
	}
	if parsed := parseCoveredUpTo("not-a-timestamp"); !parsed.IsZero() {
		t.Fatalf("unparseable coverage must be treated as absent, got %v", parsed)
	}
}

// The homepage checkpoint key is a cross-domain contract with the analytics
// worker; a silent divergence would read a checkpoint that never advances and
// degrade every healthy deployment.
func TestHomeAnalyticsCheckpointKeyMatchesAnalyticsDomain(t *testing.T) {
	raw, err := os.ReadFile("../analytics/repository.go")
	if err != nil {
		t.Fatal(err)
	}
	want := "CheckpointKey                  = \"" + homeAnalyticsCheckpointKey + "\""
	normalized := strings.Join(strings.Fields(string(raw)), " ")
	if !strings.Contains(normalized, strings.Join(strings.Fields(want), " ")) {
		t.Fatalf("analytics CheckpointKey no longer matches %q", homeAnalyticsCheckpointKey)
	}
}

// Regression guard for the silent-zero defect: buildHomeOverview used to
// discard the snapshot readiness flag and compose an all-zero homepage instead
// of reporting that no measurement exists yet.
func TestHomepageAssemblyDoesNotDiscardSnapshotReadiness(t *testing.T) {
	raw, err := os.ReadFile("service.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	start := strings.Index(source, "func (s *Service) buildHomeOverview")
	end := strings.Index(source, "func (s *Service) ValidateNoticeUpload")
	if start < 0 || end <= start {
		t.Fatal("cannot isolate buildHomeOverview implementation")
	}
	function := source[start:end]

	call := strings.Index(function, "LoadHomeAnalyticsSnapshot")
	if call < 0 {
		t.Fatalf("buildHomeOverview no longer loads the persisted snapshot: %s", function)
	}
	head := function[:call]
	if tail := len(head) - 120; tail > 0 {
		head = head[tail:]
	}
	if strings.Contains(head, ", _,") {
		t.Fatalf("buildHomeOverview discards the snapshot readiness flag: %s", function)
	}
	if !strings.Contains(function, "snapshotReady") {
		t.Fatalf("buildHomeOverview does not act on snapshot readiness: %s", function)
	}
	if !strings.Contains(function, "applyAnalyticsHealth(") && !strings.Contains(function, "composeHomeOverview(") {
		t.Fatalf("buildHomeOverview does not stamp an analytics verdict: %s", function)
	}
}
