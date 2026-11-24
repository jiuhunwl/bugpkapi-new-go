package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// TestQPSRejectionMessageNamesScopeLimitAndAction 锁定限流响应文案契约：
// 必须说明是哪一层配额、上限是多少、以及调用方该怎么做。
func TestQPSRejectionMessageNamesScopeLimitAndAction(t *testing.T) {
	account := qpsRejectionMessage(qpsScopeAccount, 20)
	for _, fragment := range []string{"账号", "20 次/秒", "重试"} {
		if !strings.Contains(account, fragment) {
			t.Fatalf("account QPS message %q missing %q", account, fragment)
		}
	}
	iface := qpsRejectionMessage(qpsScopeInterface, 5)
	for _, fragment := range []string{"该接口", "5 次/秒", "重试"} {
		if !strings.Contains(iface, fragment) {
			t.Fatalf("interface QPS message %q missing %q", iface, fragment)
		}
	}
	if account == iface {
		t.Fatal("account and interface rate-limit messages must be distinguishable")
	}
}

// TestReserveQPSLimitFailsClosedWithActionableMessage 计数器不可用时必须 fail-closed，
// 并且返回可定位的 503 文案（而不是含糊的 "Redis service unavailable"）。
func TestReserveQPSLimitFailsClosedWithActionableMessage(t *testing.T) {
	_, err := (&Service{}).reserveQPSLimit(context.Background(), "k", 1, qpsScopeAccount)
	var business *apperrors.BusinessError
	if !errors.As(err, &business) || business.StatusCode != consts.StatusServiceUnavailable {
		t.Fatalf("missing limiter error = %v, want HTTP 503 business error", err)
	}
	if !strings.Contains(business.Message, "限流服务暂时不可用") {
		t.Fatalf("limiter outage message = %q, want a Chinese actionable message", business.Message)
	}
}

// TestInvokeUpstreamsReportsTimeoutBudgetToCaller 锁定超时响应契约：
// 请求预算耗尽时，调用方拿到的必须是「超时 + 预算值」而不是笼统的
// "服务暂时不可用"，且响应体经 applySafeUpstreamFailureResponse 透传该文案。
func TestInvokeUpstreamsReportsTimeoutBudgetToCaller(t *testing.T) {
	service := &Service{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}}

	result, err := service.invokeUpstreams(context.Background(), InterfaceConfig{
		ID:               994,
		APICode:          "slow-demo",
		RequestTimeoutMs: 120,
		PollingEnabled:   1,
		PollingMode:      "PRIMARY",
		RequestURL:       "https://primary.example/api",
		UpstreamConfigs:  []UpstreamConfig{{URL: "https://backup.example/api"}},
	}, http.MethodGet, OpenRequest{})

	if err == nil {
		t.Fatal("exhausted request budget must return an error")
	}
	for _, fragment := range []string{"上游响应超时", "120ms"} {
		if !strings.Contains(result.UpstreamBusinessMsg, fragment) {
			t.Fatalf("timeout message %q missing %q", result.UpstreamBusinessMsg, fragment)
		}
	}
	if result.Success {
		t.Fatalf("timed out call must not be reported as success: %+v", result)
	}
	applySafeUpstreamFailureResponse(&result)
	if !strings.Contains(result.Body, "上游响应超时") {
		t.Fatalf("public response body lost the timeout reason: %s", result.Body)
	}
}

// TestInvokeUpstreamsKeepsUpstreamBusinessMessageOnTimeout 上游最后一次响应自带
// 业务文案时，超时标注不得覆盖它——更精确的上游原因优先。
func TestInvokeUpstreamsKeepsUpstreamBusinessMessageOnTimeout(t *testing.T) {
	service := &Service{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if strings.Contains(request.URL.Host, "backup") {
			return &http.Response{
				Body:       io.NopCloser(strings.NewReader(`{"code":500,"message":"上游余额不足"}`)),
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Request:    request,
				StatusCode: http.StatusOK,
			}, nil
		}
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}}

	result, _ := service.invokeUpstreams(context.Background(), InterfaceConfig{
		ID:               995,
		RequestTimeoutMs: 200,
		PollingEnabled:   1,
		PollingMode:      "PRIMARY",
		RequestURL:       "https://primary.example/api",
		UpstreamConfigs:  []UpstreamConfig{{URL: "https://backup.example/api"}},
	}, http.MethodGet, OpenRequest{})

	if result.UpstreamBusinessMsg != "上游余额不足" {
		t.Fatalf("upstream business message was overwritten: %q", result.UpstreamBusinessMsg)
	}
}
