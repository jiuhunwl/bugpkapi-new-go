package masking

import (
	"encoding/json"
	"strings"
	"testing"
)

// 生产实测（2026-10-21）：24 小时内 2,196,186 条调用日志中有 1,903,115 条
// （86.6%）的 response_body 被 LogJSONText 抹成 [UPSTREAM_REDACTED]，响应里
// 解析出的业务直链全部不可读，调用日志彻底失去排查价值。响应体是调用方本来
// 就已经收到的数据，公开业务 URL 必须保留，只脱敏凭据与内网地址。
func TestResponseJSONTextKeepsBusinessLinks(t *testing.T) {
	input := `{"code":0,"data":{"video_url":"https://v11-vdl.doubao.com/abc/video.mp4?x-expires=1&sign=1","cover":"http://p3.example.com/cover.jpg","title":"样本"}}`
	got := ResponseJSONText(input)
	for _, want := range []string{"v11-vdl.doubao.com", "/abc/video.mp4", "x-expires=1", "p3.example.com/cover.jpg"} {
		if !strings.Contains(got, want) {
			t.Fatalf("business link lost %q: %s", want, got)
		}
	}
	if strings.Contains(got, redactedEndpointValue) {
		t.Fatalf("response body was endpoint-redacted: %s", got)
	}
	if !json.Valid([]byte(got)) {
		t.Fatalf("response body lost its JSON shape: %s", got)
	}
}

func TestResponseJSONTextMasksCredentialsAndQuerySecrets(t *testing.T) {
	input := `{"accessToken":"raw-token","password":"pw","email":"alice@example.com","mobile":"13800138000","url":"https://api.example.com/x?token=query-secret&id=7"}`
	got := ResponseJSONText(input)
	for _, secret := range []string{"raw-token", `"pw"`, "alice@example.com", "13800138000", "query-secret"} {
		if strings.Contains(got, secret) {
			t.Fatalf("response body leaked %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, "api.example.com/x") || !strings.Contains(got, "id=7") {
		t.Fatalf("harmless URL parts or business params lost: %s", got)
	}
}

func TestResponseJSONTextHidesInternalHosts(t *testing.T) {
	input := `{"callback":"http://10.0.0.8:8080/hook","next":"http://api.internal:9000/v1","public":"https://cdn.example.com/a"}`
	got := ResponseJSONText(input)
	for _, forbidden := range []string{"10.0.0.8", "8080", "api.internal", "9000"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("internal endpoint leaked %q: %s", forbidden, got)
		}
	}
	if !strings.Contains(got, "[INTERNAL_REDACTED]") || !strings.Contains(got, "cdn.example.com/a") {
		t.Fatalf("internal/public mix handled incorrectly: %s", got)
	}
}

func TestResponseTextKeepsPlainTextLinks(t *testing.T) {
	got := ResponseText("playback ready at https://cdn.example.com/v/1.mp4?quality=hd with token=plain-secret")
	if !strings.Contains(got, "cdn.example.com/v/1.mp4") || !strings.Contains(got, "quality=hd") {
		t.Fatalf("plain-text business link lost: %s", got)
	}
	if strings.Contains(got, "plain-secret") {
		t.Fatalf("plain-text credential leaked: %s", got)
	}
}

func TestResponseJSONTextIsIdempotent(t *testing.T) {
	input := `{"data":{"url":"https://cdn.example.com/a.mp4?token=s"},"accessToken":"t"}`
	once := ResponseJSONText(input)
	if twice := ResponseJSONText(once); twice != once {
		t.Fatalf("repeated redaction changed result:\n once=%s\ntwice=%s", once, twice)
	}
}

// 响应体里的历史标记不应被"发明"回原值，也不能因此拒绝输出。
func TestResponseJSONTextKeepsHistoricalMarker(t *testing.T) {
	input := `{"url":"[UPSTREAM_REDACTED]"}`
	if got := ResponseJSONText(input); got != input {
		t.Fatalf("historical marker altered: %s", got)
	}
}

// 抖音图片 CDN 路径里的 `~tplv-dy-aweme-images:300:300.jpeg` 会被
// internalIPv6Pattern 的 `(?:[0-9a-f]{0,4}:){2,}[0-9a-f]{0,4}` 分支命中，
// 而 isInternalHost 对任何不含点的字符串都返回 true ⇒ 公网 CDN 链接曾被改成
// `[INTERNAL_REDACTED]`。修好后 URL 必须逐字节保留。
func TestResponseJSONTextKeepsSignedCDNPaths(t *testing.T) {
	avatar := "https://p3-pc-sign.douyinpic.com/obj/abc~tplv-dy-aweme-images:300:300.jpeg?x-expires=1791876120&x-signature=XYZ"
	cover := "https://p3-sign.douyinpic.com/obj/cover~tplv-dy-aweme-images:1080:1920.jpeg?x-expires=1791876120"
	input := `{"code":200,"data":{"author":{"avatar":"` + avatar + `"},"cover":"` + cover + `"}}`
	got := ResponseJSONText(input)
	var decoded struct {
		Code int `json:"code"`
		Data struct {
			Author struct {
				Avatar string `json:"avatar"`
			} `json:"author"`
			Cover string `json:"cover"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatalf("response body is no longer valid JSON: %v: %s", err, got)
	}
	if decoded.Data.Author.Avatar != avatar {
		t.Fatalf("signed CDN avatar altered:\nwant %s\ngot  %s", avatar, decoded.Data.Author.Avatar)
	}
	if decoded.Data.Cover != cover {
		t.Fatalf("signed CDN cover altered:\nwant %s\ngot  %s", cover, decoded.Data.Cover)
	}
	if strings.Contains(got, "[INTERNAL_REDACTED]") {
		t.Fatalf("public CDN path was treated as an internal host: %s", got)
	}
}
