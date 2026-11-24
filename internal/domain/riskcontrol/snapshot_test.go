package riskcontrol

import "testing"

func TestOverviewSnapshotOnlyCoversTheExpensive24HourWindow(t *testing.T) {
	for _, test := range []struct {
		query RiskQuery
		want  bool
	}{
		{query: RiskQuery{Limit: 12, WindowMinutes: 10}, want: false},
		{query: RiskQuery{Limit: 12, WindowMinutes: 60}, want: false},
		{query: RiskQuery{Limit: 12, WindowMinutes: 1440}, want: true},
	} {
		if got := useOverviewSnapshot(test.query); got != test.want {
			t.Fatalf("useOverviewSnapshot(%+v)=%t, want %t", test.query, got, test.want)
		}
	}
}

func TestOverviewFromSnapshotAppliesLimitWithoutMutatingStoredPayload(t *testing.T) {
	stored := Overview{
		Events: []RiskEvent{
			{ParamSignature: "event-1"},
			{ParamSignature: "event-2"},
			{ParamSignature: "event-3"},
		},
		TopFailedAccounts: []FailedAccountRank{
			{UserID: "1"},
			{UserID: "2"},
			{UserID: "3"},
		},
		TopFailedIPs: []FailedIPRank{
			{ClientIP: "192.0.2.1"},
			{ClientIP: "192.0.2.2"},
			{ClientIP: "192.0.2.3"},
		},
		WindowMinutes: 1440,
	}

	result := overviewFromSnapshot(stored, RiskQuery{Limit: 2, WindowMinutes: 1440})
	if len(result.Events) != 2 || len(result.TopFailedAccounts) != 2 || len(result.TopFailedIPs) != 2 {
		t.Fatalf("snapshot limits were not applied: %+v", result)
	}
	if len(stored.Events) != 3 || len(stored.TopFailedAccounts) != 3 || len(stored.TopFailedIPs) != 3 {
		t.Fatalf("stored snapshot was mutated: %+v", stored)
	}

	result.Events[0].ParamSignature = "changed"
	result.TopFailedAccounts[0].UserID = "changed"
	result.TopFailedIPs[0].ClientIP = "changed"
	if stored.Events[0].ParamSignature != "event-1" || stored.TopFailedAccounts[0].UserID != "1" || stored.TopFailedIPs[0].ClientIP != "192.0.2.1" {
		t.Fatal("snapshot result still aliases the stored top-level slices")
	}
}
