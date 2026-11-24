package gateway

import (
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"bugpk-api/internal/pkg/masking"
)

func TestInterfaceByCodeReleasesPrimaryQueryBeforeLoadingBillingRules(t *testing.T) {
	sourceBytes, err := os.ReadFile("repository.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(sourceBytes)
	start := strings.Index(source, "func (r *Repository) InterfaceByCode")
	end := strings.Index(source, "type upstreamConfigDocument")
	if start < 0 || end <= start {
		t.Fatal("cannot isolate InterfaceByCode implementation")
	}
	function := source[start:end]
	if !strings.Contains(function, "QueryRowContext(ctx") {
		t.Fatalf("InterfaceByCode must use QueryRowContext so the primary connection is released before BillingRules: %s", function)
	}
	if strings.Contains(function, "rows, err := r.db.QueryContext(ctx") {
		t.Fatalf("InterfaceByCode keeps a rows cursor open while loading BillingRules: %s", function)
	}
}

type gatewaySQLResult struct {
	affected int64
	err      error
}

func TestResponseBodyForLogUsesUTF8SafeLimits(t *testing.T) {
	if got := responseBodyForLog(string([]byte{0xff, 0xfe, 0xfd})); got != "[BINARY_RESPONSE_OMITTED]" {
		t.Fatalf("binary response log = %q", got)
	}
	value := strings.Repeat("你", 7000)
	got := responseBodyForLog(value)
	if len(got) > 20000 || !utf8.ValidString(got) {
		t.Fatalf("response log truncation is invalid: bytes=%d valid=%v", len(got), utf8.ValidString(got))
	}
	if got == value {
		t.Fatal("oversized response log was not truncated")
	}
}

func TestResponseBodyForLogMasksSecretsBeforePersistence(t *testing.T) {
	got := responseBodyForLog(`{"accessToken":"raw-token","password":"secret-password","email":"alice@example.com","mobile":"13800138000","data":"ok"}`)
	for _, secret := range []string{"raw-token", "secret-password", "alice@example.com", "13800138000"} {
		if strings.Contains(got, secret) {
			t.Fatalf("response log leaked %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, `"data":"ok"`) {
		t.Fatalf("non-sensitive response content was lost: %s", got)
	}

	plain := responseBodyForLog("Authorization: Bearer raw-token password=secret-password")
	if strings.Contains(plain, "raw-token") || strings.Contains(plain, "secret-password") {
		t.Fatalf("plain-text response log leaked credentials: %s", plain)
	}
}

// 响应体是调用方本来就收到的业务数据。链路层（网关 / 后台调试）落库时必须
// 保留解析出的业务直链，否则调用日志无法回答"用户拿到的是哪个地址、为什么
// 播放失败"。上游路由地址由 upstream_url 列单独保护。
func TestResponseBodyForLogKeepsBusinessLinks(t *testing.T) {
	got := responseBodyForLog(`{"code":0,"data":{"video_url":"https://v11-vdl.doubao.com/abc/video.mp4","cover":"https://p3.example.com/c.jpg"}}`)
	for _, want := range []string{"v11-vdl.doubao.com/abc/video.mp4", "p3.example.com/c.jpg"} {
		if !strings.Contains(got, want) {
			t.Fatalf("business link lost %q: %s", want, got)
		}
	}
	if strings.Contains(got, "[UPSTREAM_REDACTED]") {
		t.Fatalf("response body was endpoint-redacted: %s", got)
	}

	plain := responseBodyForLog("播放地址 https://cdn.example.com/v/1.mp4?quality=hd")
	if !strings.Contains(plain, "cdn.example.com/v/1.mp4") {
		t.Fatalf("plain-text business link lost: %s", plain)
	}
}

func TestResponseSizeForLogPreservesOriginalSize(t *testing.T) {
	result := OpenResult{Body: strings.Repeat("x", 20000), ResponseSize: 45000}
	if got := responseSizeForLog(result); got != 45000 {
		t.Fatalf("responseSizeForLog() = %d, want original size 45000", got)
	}
	if got := responseSizeForLog(OpenResult{Body: "fallback"}); got != 8 {
		t.Fatalf("responseSizeForLog() fallback = %d, want 8", got)
	}
}

func TestResponseWasTruncatedIgnoresMaskingLengthChanges(t *testing.T) {
	body := `{"accessToken":"raw-token","data":"ok"}`
	if responseWasTruncated(body, int64(len(body))) {
		t.Fatal("complete response was marked truncated before persistence masking")
	}
	if !responseWasTruncated(strings.Repeat("x", 20001), 20001) {
		t.Fatal("oversized response was not marked truncated")
	}
	if !responseWasTruncated("partial", 100) {
		t.Fatal("already-shortened response was not marked truncated")
	}
	if !responseWasTruncated(string([]byte{0xff}), 1) {
		t.Fatal("binary response omission was not marked truncated")
	}
}

func TestSafeDiagnosticTextMasksSecretsBeforePersistence(t *testing.T) {
	input := "upstream failed: https://api.example.com/path?token=raw-token password=secret-password email=alice@example.com"
	got := safeDiagnosticText(input, 500)
	for _, secret := range []string{"raw-token", "secret-password", "alice@example.com"} {
		if strings.Contains(got, secret) {
			t.Fatalf("diagnostic text leaked %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, "upstream failed") {
		t.Fatalf("diagnostic context was lost: %s", got)
	}
}

func TestSafeDiagnosticTextAppliesLengthLimitAfterMasking(t *testing.T) {
	got := safeDiagnosticText(strings.Repeat("失败", 400), 100)
	if len([]rune(got)) != 100 {
		t.Fatalf("diagnostic rune length = %d, want 100", len([]rune(got)))
	}
}

func TestUpstreamURLIsMaskedBeforePersistence(t *testing.T) {
	got := masking.URLString("https://user:password@upstream.example/path?apiKey=secret-key&city=shanghai")
	if strings.Contains(got, "password") || strings.Contains(got, "secret-key") {
		t.Fatalf("upstream URL leaked credentials: %s", got)
	}
	if !strings.Contains(got, "city=shanghai") {
		t.Fatalf("non-sensitive upstream URL query was lost: %s", got)
	}
}

func TestUpstreamURLsForPersistenceMasksAndKeepsEveryAttempt(t *testing.T) {
	longPath := strings.Repeat("x", 600)
	result := OpenResult{TriedUpstreams: []string{
		"https://primary.example/" + longPath + "?token=primary-secret&slot=1",
		"https://backup.example/" + longPath + "?apiKey=backup-secret&slot=2",
	}}
	got := upstreamURLsForPersistence(result)
	for _, expected := range []string{"node-1", "node-2"} {
		if !strings.Contains(got, expected) {
			t.Fatalf("aggregated upstream URLs lost %q: %s", expected, got)
		}
	}
	for _, forbidden := range []string{"primary.example", "backup.example", "slot=1", "slot=2", "primary-secret", "backup-secret"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("aggregated upstream URLs leaked %q: %s", forbidden, got)
		}
	}
	if len(got) >= 1024 {
		t.Fatalf("synthetic upstream references unexpectedly exceeded compact limit: %d", len(got))
	}
}

func TestUpstreamURLsForPersistenceNeverIncludesEndpointDetails(t *testing.T) {
	result := OpenResult{
		UpstreamURL: "https://primary.example/internal/parse?url=https%3A%2F%2Fprivate.example/item",
		TriedUpstreams: []string{
			"https://primary.example/internal/parse?slot=1",
			"https://backup.example/internal/parse?slot=2",
		},
	}
	got := upstreamURLsForPersistence(result)
	for _, forbidden := range []string{
		"primary.example", "backup.example", "private.example", "/internal/parse", "slot=1", "slot=2",
	} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("persisted upstream diagnostics leaked endpoint detail %q: %s", forbidden, got)
		}
	}
	if got != "node-1,node-2" {
		t.Fatalf("persisted upstream diagnostics = %q, want stable node references", got)
	}
}

func TestGatewayRequestLogFieldsAreMaskedAndBounded(t *testing.T) {
	params := requestParamsForPersistence(`{"token":"secret-token","data":"` + strings.Repeat("响", 8000) + `"}`)
	if len(params) > 20000 || !utf8.ValidString(params) {
		t.Fatalf("request params bytes=%d valid=%v", len(params), utf8.ValidString(params))
	}
	if strings.Contains(params, "secret-token") {
		t.Fatalf("request params leaked a token: %s", params)
	}

	// request_value 列保留业务 URL 主体（scheme/host/path/普通 query），
	// 仅对 URL 查询值里的敏感 key 做 key-based 脱敏。
	value := requestValueForPersistence("https://example.com/path?token=secret-token&data=" + strings.Repeat("x", 800))
	if len(value) > 500 || !utf8.ValidString(value) {
		t.Fatalf("request value bytes=%d valid=%v", len(value), utf8.ValidString(value))
	}
	if strings.Contains(value, "secret-token") {
		t.Fatalf("request value leaked a token: %s", value)
	}
	// 业务 URL 主体应保留可见，便于排查；只脱敏其中的 token 值。
	for _, keep := range []string{"example.com", "/path", "data="} {
		if !strings.Contains(value, keep) {
			t.Fatalf("request value lost business endpoint detail %q: %s", keep, value)
		}
	}

	// JSON 内的业务 URL 主体应保留，敏感 key 的值被脱敏。
	paramsWithURL := requestParamsForPersistence(`{"target":"https://upstream.example/internal?token=secret-token","safe":"ok"}`)
	for _, keep := range []string{"upstream.example", "/internal", "safe"} {
		if !strings.Contains(paramsWithURL, keep) {
			t.Fatalf("request params lost business endpoint detail %q: %s", keep, paramsWithURL)
		}
	}
	if strings.Contains(paramsWithURL, "secret-token") {
		t.Fatalf("request params leaked a token: %s", paramsWithURL)
	}
}

func TestTruncateRunesPreservesUnicode(t *testing.T) {
	if got := truncateRunes("风控补偿失败", 4); got != "风控补偿" {
		t.Fatalf("truncateRunes() = %q", got)
	}
}

func (result gatewaySQLResult) LastInsertId() (int64, error) { return 0, nil }
func (result gatewaySQLResult) RowsAffected() (int64, error) { return result.affected, result.err }

func TestRequireAffectedRejectsMissingRowsAndDriverErrors(t *testing.T) {
	if err := requireAffected(gatewaySQLResult{affected: 1}); err != nil {
		t.Fatalf("one affected row should pass: %v", err)
	}
	if err := requireAffected(gatewaySQLResult{}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("zero affected rows should return sql.ErrNoRows, got %v", err)
	}
	driverErr := errors.New("rows affected unavailable")
	if err := requireAffected(gatewaySQLResult{err: driverErr}); !errors.Is(err, driverErr) {
		t.Fatalf("RowsAffected error should propagate, got %v", err)
	}
}

func TestNormalizeRoleQPS(t *testing.T) {
	for _, test := range []struct {
		input int
		want  int
	}{
		{input: -1, want: 1},
		{input: 0, want: 1},
		{input: 1, want: 1},
		{input: 25, want: 25},
		{input: 1000, want: 1000},
		{input: 1_000_000, want: 1_000_000},
		{input: 1_000_001, want: 1_000_000},
	} {
		if got := normalizeRoleQPS(test.input); got != test.want {
			t.Fatalf("normalizeRoleQPS(%d) = %d, want %d", test.input, got, test.want)
		}
	}
}

func TestPrimaryRoleQPSQueryUsesUserPrimaryRole(t *testing.T) {
	for _, fragment := range []string{"bp_user", "primary_role_id", "primary_role.qps_limit"} {
		if !strings.Contains(primaryRoleQPSQuery, fragment) {
			t.Fatalf("primary role QPS query missing %q: %q", fragment, primaryRoleQPSQuery)
		}
	}
	if strings.Contains(primaryRoleQPSQuery, "default_user_qps") {
		t.Fatalf("primary role QPS query still treats legacy default_user_qps as authoritative: %q", primaryRoleQPSQuery)
	}
}

func TestEffectiveQPSLimitNeverLetsPackageDowngradeRole(t *testing.T) {
	tests := []struct {
		name       string
		role       int
		pkg        int
		hasPackage bool
		want       int
	}{
		{name: "role only", role: 25, want: 25},
		{name: "lower package", role: 25, pkg: 10, hasPackage: true, want: 25},
		{name: "higher package", role: 25, pkg: 80, hasPackage: true, want: 80},
		{name: "unlimited package", role: 25, pkg: 0, hasPackage: true, want: 0},
		{name: "invalid package", role: 25, pkg: -1, hasPackage: true, want: 25},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := effectiveQPSLimit(test.role, test.pkg, test.hasPackage); got != test.want {
				t.Fatalf("effectiveQPSLimit(%d, %d, %v) = %d, want %d", test.role, test.pkg, test.hasPackage, got, test.want)
			}
		})
	}
}
