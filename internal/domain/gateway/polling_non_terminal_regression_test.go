package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestGatewayPollsWhenPrimaryReturnsUnusableNonTerminalBody 锁定用户要求的语义：
// 只要上游返回的不是「终止轮询」响应（HTTP 404 / 业务码 404 / 配置的 stopCode），
// 且当前节点没有给出可用的成功数据，网关必须继续轮询下一个候选节点。
//
// 这里覆盖的是历史上会被误判为「通过」从而不再轮询的响应形态：
// 响应体里根本没有 code 字段、空响应体、被截断的 JSON、显式错误信号。
func TestGatewayPollsWhenPrimaryReturnsUnusableNonTerminalBody(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "empty-body", body: ""},
		{name: "truncated-json", body: `{"data":`},
		{name: "explicit-error-field", body: `{"error":"upstream rate limited"}`},
		{name: "explicit-errmsg-field", body: `{"errmsg":"节点繁忙"}`},
		{name: "success-false", body: `{"success":false,"data":null}`},
	}

	for _, mode := range []string{"PRIMARY", "ROUND_ROBIN", "FASTEST"} {
		for _, item := range cases {
			mode := mode
			item := item
			t.Run(fmt.Sprintf("%s/%s", mode, item.name), func(t *testing.T) {
				var calls []string
				service := &Service{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
					calls = append(calls, request.URL.String())
					body := item.body
					if strings.Contains(request.URL.Host, "backup") {
						body = `{"code":200,"data":{"ok":true}}`
					}
					return &http.Response{
						Body:       io.NopCloser(strings.NewReader(body)),
						Header:     http.Header{"Content-Type": []string{"application/json"}},
						Request:    request,
						StatusCode: http.StatusOK,
					}, nil
				})}}

				config := InterfaceConfig{
					ID:             9960,
					PollingEnabled: 1,
					PollingMode:    mode,
					RequestURL:     "https://primary.example/api",
				}
				// PRIMARY/FASTEST 会把 RequestURL 作为首个候选自动并入候选池；
				// ROUND_ROBIN 只使用 UpstreamConfigs。这样三种模式都得到两节点池。
				if mode == "ROUND_ROBIN" {
					config.UpstreamConfigs = []UpstreamConfig{
						{URL: "https://primary.example/api"},
						{URL: "https://backup.example/api"},
					}
				} else {
					config.UpstreamConfigs = []UpstreamConfig{{URL: "https://backup.example/api"}}
				}

				result, err := service.invokeUpstreams(context.Background(), config, http.MethodGet, OpenRequest{})
				if err != nil {
					t.Fatalf("backup node success returned an error: %v", err)
				}
				if len(calls) != 2 {
					t.Fatalf("unusable primary response did not advance to the backup node: calls=%v", calls)
				}
				if !result.Success || !strings.Contains(result.UpstreamURL, "backup") {
					t.Fatalf("polling did not return the backup node result: result=%+v calls=%v", result, calls)
				}
			})
		}
	}
}

// TestGatewayStopsOnTerminalResponseWithoutPolling 确认终止语义未被上面的收紧破坏：
// HTTP 404 与业务码 404 仍然在第一个节点立即终止，不再打备份节点。
func TestGatewayStopsOnTerminalResponseWithoutPolling(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		status int
	}{
		{name: "http-404", body: `{"message":"not found"}`, status: http.StatusNotFound},
		{name: "business-404", body: `{"code":404,"message":"not found"}`, status: http.StatusOK},
	}
	for _, item := range cases {
		item := item
		t.Run(item.name, func(t *testing.T) {
			var calls []string
			service := &Service{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls = append(calls, request.URL.String())
				return &http.Response{
					Body:       io.NopCloser(strings.NewReader(item.body)),
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Request:    request,
					StatusCode: item.status,
				}, nil
			})}}

			result, err := service.invokeUpstreams(context.Background(), InterfaceConfig{
				ID:             9961,
				PollingEnabled: 1,
				PollingMode:    "PRIMARY",
				RequestURL:     "https://primary.example/api",
				UpstreamConfigs: []UpstreamConfig{
					{URL: "https://backup.example/api"},
				},
			}, http.MethodGet, OpenRequest{})
			if err != nil {
				t.Fatalf("terminal response returned an error: %v", err)
			}
			if len(calls) != 1 || result.Success {
				t.Fatalf("terminal response did not stop on the first node: calls=%v result=%+v", calls, result)
			}
		})
	}
}

// TestGatewayReturnsLastResponseWhenEveryCandidateIsUnusable 所有节点都不可用时，
// 必须把最后一个真实 HTTP 响应透传给调用方，并且标记为不成功（不计费）。
func TestGatewayReturnsLastResponseWhenEveryCandidateIsUnusable(t *testing.T) {
	var calls []string
	service := &Service{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls = append(calls, request.URL.String())
		return &http.Response{
			Body:       io.NopCloser(strings.NewReader(`{"error":"all busy"}`)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Request:    request,
			StatusCode: http.StatusOK,
		}, nil
	})}}

	result, _ := service.invokeUpstreams(context.Background(), InterfaceConfig{
		ID:             9962,
		PollingEnabled: 1,
		PollingMode:    "ROUND_ROBIN",
		RequestURL:     "https://primary.example/api",
		UpstreamConfigs: []UpstreamConfig{
			{URL: "https://node-a.example/api"},
			{URL: "https://node-b.example/api"},
			{URL: "https://node-c.example/api"},
		},
	}, http.MethodGet, OpenRequest{})

	if len(calls) != 3 {
		t.Fatalf("exhausted polling did not try every candidate: calls=%v", calls)
	}
	if result.Success {
		t.Fatalf("exhausted polling reported success: result=%+v", result)
	}
	if len(result.TriedUpstreams) != 3 {
		t.Fatalf("tried upstream history = %v, want 3 entries", result.TriedUpstreams)
	}
}
