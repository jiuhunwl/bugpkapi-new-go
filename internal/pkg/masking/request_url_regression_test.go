package masking

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func TestSensitiveJSONURLsMaskNestedURLCredentials(t *testing.T) {
	link := "https://video.example/watch?next=" + url.QueryEscape("https://media.example/item?token=nested-secret&quality=hd")
	b, _ := json.Marshal(map[string]any{"url": []string{link}})
	got := SensitiveJSONTextWithURLs(string(b))
	if strings.Contains(got, "nested-secret") || !strings.Contains(got, "video.example") || !strings.Contains(got, "media.example") {
		t.Fatalf("nested business URL not safely retained: %s", got)
	}
}

func TestSensitiveJSONURLsBoundNestedInputAndRemainIdempotent(t *testing.T) {
	link := "https://media.example/item?token=deep-secret"
	for i := 0; i < 12; i++ {
		link = "https://video.example/watch?next=" + url.QueryEscape(link)
	}
	b, _ := json.Marshal(map[string]string{"url": link})
	got := SensitiveJSONTextWithURLs(string(b))
	if strings.Contains(got, "deep-secret") || !json.Valid([]byte(got)) {
		t.Fatalf("invalid or unsafe bounded output: %s", got)
	}
	if again := SensitiveJSONTextWithURLs(got); again != got {
		t.Fatalf("repeated redaction changed result: %s", again)
	}
}
