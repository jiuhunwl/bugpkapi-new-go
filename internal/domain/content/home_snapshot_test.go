package content

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestComposeHomeOverviewUsesSnapshotAnalytics(t *testing.T) {
	snapshot := HomeAnalyticsSnapshot{
		Stats:   PlatformStats{Calls24h: 613_515, PeakQPS24h: 21},
		HotAPIs: []HotAPIItem{{ID: "9", Name: "可靠接口", CallCount: 88}},
	}
	result := composeHomeOverview(
		SiteConfig{SiteName: "测试站点"},
		HomeTicker{Enabled: 1, Content: "公告", NoticeID: "7"},
		snapshot,
		AnalyticsHealth{Enabled: true, Ready: true},
	)
	if result.Stats.Calls24h != 613_515 || result.Stats.PeakQPS24h != 21 {
		t.Fatalf("snapshot stats were not composed: %+v", result.Stats)
	}
	if len(result.HotAPIs) != 1 || result.HotAPIs[0].Name != "可靠接口" {
		t.Fatalf("snapshot hot APIs were not composed: %+v", result.HotAPIs)
	}
	if result.SiteConfig.SiteName != "测试站点" || result.HomeNotice.Content != "公告" {
		t.Fatalf("live content was not composed: %+v", result)
	}
	if !result.Stats.AnalyticsReady || len(result.Stats.UnavailableFields) != 0 {
		t.Fatalf("healthy verdict was not stamped: %+v", result.Stats)
	}
}

func TestHomeAnalyticsSnapshotPayloadContainsNoMutableContent(t *testing.T) {
	payload, err := json.Marshal(HomeAnalyticsSnapshot{})
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	for _, forbidden := range []string{"siteConfig", "homeNotice", "analyticsCode"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("analytics snapshot leaked mutable content %q: %s", forbidden, text)
		}
	}
}

func TestInvalidateHomeOverviewQueuesOneCoalescedSnapshotRefresh(t *testing.T) {
	service := NewService(nil, nil, "")
	service.InvalidateHomeOverview(context.Background())
	service.InvalidateHomeOverview(context.Background())

	if got := len(service.homeSnapshotWake); got != 1 {
		t.Fatalf("queued snapshot refreshes = %d, want 1", got)
	}
}
