package interfaceapi

import (
	"testing"
)

// TestPreserveMissingPollingCheckKeepsStoredResponseCheck 锁定「接口基础信息编辑
// 不得清空轮询失败校验配置」的契约。表单只提交了上游地址/轮询开关时，
// pollingCheckEnabled/field/expected/stopCode 必须沿用库中现值。
func TestPreserveMissingPollingCheckKeepsStoredResponseCheck(t *testing.T) {
	current := InterfaceItem{
		PollingCheckEnabled:  1,
		PollingCheckField:    "meta.status",
		PollingCheckExpected: "0",
		PollingStopCode:      "40401",
	}
	enabled := 1
	req := SaveInterfaceRequest{
		APICode:        "demo",
		Name:           "demo",
		RequestURL:     "https://primary.example/api",
		PollingEnabled: &enabled,
		UpstreamConfigs: []UpstreamConfig{
			{URL: "https://backup.example/api"},
		},
	}

	preserveMissingPollingCheck(&req, current)

	if req.PollingCheckEnabled == nil || *req.PollingCheckEnabled != 1 {
		t.Fatalf("pollingCheckEnabled = %v, want stored 1", req.PollingCheckEnabled)
	}
	if req.PollingCheckField != "meta.status" {
		t.Fatalf("pollingCheckField = %q, want stored meta.status", req.PollingCheckField)
	}
	if req.PollingCheckExpected != "0" {
		t.Fatalf("pollingCheckExpected = %q, want stored 0", req.PollingCheckExpected)
	}
	if req.PollingStopCode != "40401" {
		t.Fatalf("pollingStopCode = %q, want stored 40401", req.PollingStopCode)
	}
}

// TestPreserveMissingPollingCheckKeepsSubmittedValues 提交了新值时不得被库中现值覆盖。
func TestPreserveMissingPollingCheckKeepsSubmittedValues(t *testing.T) {
	current := InterfaceItem{
		PollingCheckEnabled:  1,
		PollingCheckField:    "meta.status",
		PollingCheckExpected: "0",
		PollingStopCode:      "40401",
	}
	disabled := 0
	req := SaveInterfaceRequest{
		PollingCheckEnabled:  &disabled,
		PollingCheckField:    "code",
		PollingCheckExpected: "200",
		PollingStopCode:      "404",
	}

	preserveMissingPollingCheck(&req, current)

	if req.PollingCheckEnabled == nil || *req.PollingCheckEnabled != 0 {
		t.Fatalf("explicit pollingCheckEnabled=0 was overwritten: %v", req.PollingCheckEnabled)
	}
	if req.PollingCheckField != "code" || req.PollingCheckExpected != "200" || req.PollingStopCode != "404" {
		t.Fatalf("submitted response check was overwritten: %+v", req)
	}
}

// TestOptionalPollingFieldAbsentRequiresUsableBody 锁定自动兼容模式的判定边界：
// 空响应体与损坏 JSON 必须继续轮询，纯文本/数组/无错误信号对象放行。
func TestOptionalPollingFieldAbsentRequiresUsableBody(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{name: "empty", body: "", want: false},
		{name: "whitespace", body: "   \n", want: false},
		{name: "truncated-object", body: `{"data":`, want: false},
		{name: "truncated-array", body: `[{"a":1}`, want: false},
		{name: "plain-text", body: "OK", want: true},
		{name: "html", body: "<html><body>hi</body></html>", want: true},
		{name: "json-array", body: `[{"id":1},{"id":2}]`, want: true},
		{name: "plain-object", body: `{"data":{"url":"https://cdn.example/a.mp4"}}`, want: true},
		{name: "empty-error-field", body: `{"error":"","data":{"ok":true}}`, want: true},
		{name: "null-error-field", body: `{"error":null,"data":{"ok":true}}`, want: true},
		{name: "explicit-error", body: `{"error":"rate limited"}`, want: false},
		{name: "explicit-errmsg", body: `{"errmsg":"upstream busy"}`, want: false},
		{name: "success-false-bool", body: `{"success":false,"data":null}`, want: false},
		{name: "success-false-string", body: `{"success":"false"}`, want: false},
		{name: "success-true", body: `{"success":true,"data":{"ok":1}}`, want: true},
	}
	for _, item := range cases {
		item := item
		t.Run(item.name, func(t *testing.T) {
			if got := optionalPollingFieldAbsent(item.body); got != item.want {
				t.Fatalf("optionalPollingFieldAbsent(%q) = %v, want %v", item.body, got, item.want)
			}
		})
	}
}
