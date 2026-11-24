package riskcontrol

import (
	"fmt"
	"testing"
	"time"
)

func TestInvalidKeyMetricKeysUseExactMinuteWindow(t *testing.T) {
	now := time.Unix(1_900_000_045, 0)
	keys := invalidKeyMetricKeys(now, 10)
	if len(keys) != 20 {
		t.Fatalf("metric key count = %d, want 20", len(keys))
	}

	currentMinute := now.Unix() / 60
	for offset := 0; offset < 10; offset++ {
		attemptKey := fmt.Sprintf("gateway:risk:invalid-key:metric:attempts:%d", currentMinute-int64(offset))
		blockedKey := fmt.Sprintf("gateway:risk:invalid-key:metric:blocked:%d", currentMinute-int64(offset))
		if keys[offset*2] != attemptKey || keys[offset*2+1] != blockedKey {
			t.Fatalf("metric keys at offset %d = %q/%q, want %q/%q", offset, keys[offset*2], keys[offset*2+1], attemptKey, blockedKey)
		}
	}

	excludedMinute := currentMinute - 10
	for _, key := range keys {
		if key == fmt.Sprintf("gateway:risk:invalid-key:metric:attempts:%d", excludedMinute) ||
			key == fmt.Sprintf("gateway:risk:invalid-key:metric:blocked:%d", excludedMinute) {
			t.Fatalf("metric window included an eleventh minute bucket: %q", key)
		}
	}
}

func TestInvalidKeyMetricKeysRejectNonPositiveWindow(t *testing.T) {
	if keys := invalidKeyMetricKeys(time.Now(), 0); len(keys) != 0 {
		t.Fatalf("zero-minute window returned %d keys", len(keys))
	}
	if keys := invalidKeyMetricKeys(time.Now(), -1); len(keys) != 0 {
		t.Fatalf("negative-minute window returned %d keys", len(keys))
	}
}
