package app

import (
	"os"
	"strings"
	"testing"
)

func TestHomeOverviewSnapshotIsPrimedAndRefreshedOutsideHTTPRequests(t *testing.T) {
	contents, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(contents)
	for _, required := range []string{
		"SetPublicCache(redisClient, 30*time.Second)",
		"RefreshHomeAnalyticsSnapshot",
		"StartHomeAnalyticsSnapshotWorker",
		"registerCloser(homeSnapshotWorker.Close)",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("homepage snapshot wiring is missing %q", required)
		}
	}
}
