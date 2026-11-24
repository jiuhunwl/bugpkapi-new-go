package user

import "testing"

func TestBuildCallOutcomeStats(t *testing.T) {
	stats, ok := buildCallOutcomeStats(100, 80, 10, 5, 2, 3)
	if !ok {
		t.Fatal("complete buckets must be accepted")
	}
	if !stats.Available || stats.Calls != 100 || stats.SampleCalls != 90 {
		t.Fatalf("unexpected totals: %+v", stats)
	}
	if stats.SuccessRate != "88.89%" {
		t.Fatalf("success rate = %s, want 88.89%%", stats.SuccessRate)
	}
	if stats.ServiceFailureRate != "3.61%" {
		t.Fatalf("service failure rate = %s, want 3.61%%", stats.ServiceFailureRate)
	}
	if stats.ClientErrorRate != "5.56%" {
		t.Fatalf("client error rate = %s, want 5.56%%", stats.ClientErrorRate)
	}
	if stats.ThrottleRate != "2.22%" {
		t.Fatalf("throttle rate = %s, want 2.22%%", stats.ThrottleRate)
	}
	if stats.ExpectedEmptyRate != "10.00%" {
		t.Fatalf("expected empty rate = %s, want 10.00%%", stats.ExpectedEmptyRate)
	}
}

func TestBuildCallOutcomeStatsRejectsPartialBuckets(t *testing.T) {
	if _, ok := buildCallOutcomeStats(10, 9, 0, 0, 0, 0); ok {
		t.Fatal("a partially rebuilt bucket set must not be published")
	}
	if _, ok := buildCallOutcomeStats(0, 0, 0, 0, 0, 0); !ok {
		t.Fatal("an empty day is a valid zero sample")
	}
}

func TestBuildCallOutcomeStatsUsesDashWhenNoSample(t *testing.T) {
	stats, ok := buildCallOutcomeStats(5, 0, 5, 0, 0, 0)
	if !ok {
		t.Fatal("expected-empty-only traffic is a complete bucket set")
	}
	if stats.ServiceFailureRate != "—" || stats.SuccessRate != "—" {
		t.Fatalf("rates without an upstream sample must be dashes: %+v", stats)
	}
	if stats.ExpectedEmptyRate != "100.00%" {
		t.Fatalf("expected empty rate = %s, want 100.00%%", stats.ExpectedEmptyRate)
	}
}
