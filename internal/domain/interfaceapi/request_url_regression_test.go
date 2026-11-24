package interfaceapi

import (
	"strings"
	"testing"
)

func TestInvokeBusinessURLSurvivesBeforePersistence(t *testing.T) {
	got := requestParamsForPersistence(invokeRequestParamsForLog(map[string]any{"url": "https://video.example/watch/123?token=private-token&quality=hd"}, nil))
	if !strings.Contains(got, "video.example/watch/123") || !strings.Contains(got, "quality=hd") || strings.Contains(got, "private-token") {
		t.Fatalf("incorrect request params: %s", got)
	}
}
