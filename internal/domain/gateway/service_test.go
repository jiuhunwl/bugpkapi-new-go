package gateway

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	stderrors "errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"bugpk-api/internal/domain/rbac"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/httpclient"
	"bugpk-api/internal/pkg/requestvalidation"
	"bugpk-api/internal/pkg/upstreamhealth"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
	_ "github.com/go-sql-driver/mysql"
	"github.com/redis/go-redis/v9"
)

type gatewayFailingReadCloser struct{}

func TestApplySafeUpstreamFailureResponseHidesInternalDetails(t *testing.T) {
	result := OpenResult{
		Body:           `dial tcp 10.20.30.40:8443: connectex: connection refused`,
		ContentType:    "text/plain",
		ErrorMessage:   `Get "https://internal.example.local/private": tls handshake timeout`,
		ResponseStatus: http.StatusOK,
		UpstreamURL:    "https://internal.example.local/private",
	}

	applySafeUpstreamFailureResponse(&result)

	for _, secret := range []string{"10.20.30.40", "internal.example.local", "connectex", "tls handshake"} {
		if strings.Contains(strings.ToLower(result.Body), strings.ToLower(secret)) {
			t.Fatalf("open response leaked internal detail %q: %s", secret, result.Body)
		}
	}
	if !strings.Contains(result.Body, safeUpstreamFailureMessage) {
		t.Fatalf("safe failure message missing from response: %s", result.Body)
	}
	if safeUpstreamFailureMessage != "服务暂时不可用" {
		t.Fatalf("safe failure message drifted from P0 convention: %q", safeUpstreamFailureMessage)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(result.Body), &body); err != nil {
		t.Fatalf("safe failure response is not valid JSON: %v", err)
	}
	if body["message"] != "服务暂时不可用" || body["error"] != "服务暂时不可用" {
		t.Fatalf("safe failure response message/error mismatch: %+v", body)
	}
	if body["code"] != float64(-1) {
		t.Fatalf("safe failure response code = %v, want -1 (frontend compatibility)", body["code"])
	}
	if _, ok := body["data"]; !ok || body["data"] != nil {
		t.Fatalf("safe failure response data must stay null: %+v", body)
	}
	if result.ContentType != "application/json; charset=utf-8" {
		t.Fatalf("content type = %q", result.ContentType)
	}
	if result.ResponseStatus != http.StatusBadGateway {
		t.Fatalf("response status = %d, want %d", result.ResponseStatus, http.StatusBadGateway)
	}
	if result.ResponseSize != int64(len(result.Body)) {
		t.Fatalf("response size = %d, want %d", result.ResponseSize, len(result.Body))
	}
	if strings.Contains(result.ErrorMessage, "internal.example.local") || strings.Contains(result.ErrorMessage, "/private") {
		t.Fatalf("open result retained an upstream endpoint: %s", result.ErrorMessage)
	}
	if !strings.Contains(result.ErrorMessage, "[UPSTREAM_REDACTED]") || !strings.Contains(result.ErrorMessage, "tls handshake timeout") {
		t.Fatalf("open result lost safe diagnostic context: %s", result.ErrorMessage)
	}
}

func TestUpstreamFailureReasonRedactsEndpointDetails(t *testing.T) {
	tests := []struct {
		name        string
		invokeErr   error
		cleaningErr error
	}{
		{
			name:      "request error",
			invokeErr: stderrors.New(`Get "https://parse.upstream.example.com/api/parse?url=https%3A%2F%2Fv.kuaishou.com%2FAbCdEf12": net/http: timeout awaiting response headers`),
		},
		{
			name:        "response cleaning error",
			cleaningErr: stderrors.New(`decode https://parse.upstream.example.com/private failed: unexpected EOF`),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := upstreamFailureReason(OpenResult{}, tc.invokeErr, tc.cleaningErr, false)
			for _, forbidden := range []string{"parse.upstream.example.com", "v.kuaishou.com", "/api/parse", "/private", "AbCdEf12"} {
				if strings.Contains(got, forbidden) {
					t.Fatalf("failure reason leaked %q: %s", forbidden, got)
				}
			}
			if !strings.Contains(got, "[UPSTREAM_REDACTED]") {
				t.Fatalf("failure reason has no redaction marker: %s", got)
			}
		})
	}
}

func TestApplySafeUpstreamFailureResponseRedactsUpstreamURLInBusinessMessage(t *testing.T) {
	result := OpenResult{
		UpstreamBusinessMsg: `failed: https://parse.upstream.example.com/api/parse?url=https%3A%2F%2Fv.kuaishou.com%2FAbCdEf12`,
	}

	applySafeUpstreamFailureResponse(&result)
	for _, forbidden := range []string{"parse.upstream.example.com", "v.kuaishou.com", "AbCdEf12", "/api/parse"} {
		if strings.Contains(result.Body, forbidden) {
			t.Fatalf("business message leaked upstream detail %q: %s", forbidden, result.Body)
		}
	}
}

func TestOpenDoesNotMisreportDatabaseFailureAsInvalidAPIKey(t *testing.T) {
	db, err := sql.Open("mysql", "root:root@tcp(127.0.0.1:3306)/closed_gateway_test")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	service := NewService(NewRepository(db), nil, nil, "gateway-risk-key-with-at-least-thirty-two-bytes")
	_, err = service.Open(context.Background(), "phone_lookup", OpenRequest{
		Headers: map[string]string{"X-API-Key": "bp_live_database_failure"},
		Method:  "GET",
	})
	if err == nil {
		t.Fatal("closed database unexpectedly authenticated an API key")
	}
	var business *apperrors.BusinessError
	if stderrors.As(err, &business) && business.StatusCode == consts.StatusForbidden {
		t.Fatalf("database failure was misreported as invalid API key: %v", err)
	}
}

func TestQPSRejectionResultMarksReservedBillingAsReleased(t *testing.T) {
	result := qpsRejectionResult(OpenResult{
		Billable:      true,
		BillingStatus: "CONFIRMED",
		ChargeAmount:  "9.9900",
		ChargeType:    "BALANCE",
		PointPrice:    5,
	}, BillingReservation{
		BillingRecordID: 41,
		ChargeAmount:    "9.9900",
		ChargeType:      "BALANCE",
		PointAmount:     5,
		Reserved:        true,
		Snapshot:        `{"source":"balance"}`,
	})
	if result.Billable || result.BillingStatus != "RELEASED" || result.ChargeType != "NO_CHARGE" {
		t.Fatalf("QPS rejection retained a charge: %+v", result)
	}
	if result.BillingRecordID != 41 || result.ChargeAmount != "0.0000" || result.PointPrice != 0 {
		t.Fatalf("QPS rejection release snapshot is inconsistent: %+v", result)
	}
	if result.CallLogEligible || shouldRecordCallLog(result) {
		t.Fatalf("QPS rejection must not enter call logs or success-rate statistics: %+v", result)
	}
}

func TestQPSRejectionResultKeepsNonBillableRequestUnreserved(t *testing.T) {
	result := qpsRejectionResult(OpenResult{
		BillingStatus: "NO_CHARGE",
		ChargeAmount:  "0.0000",
		ChargeType:    "NO_CHARGE",
	}, BillingReservation{})
	if result.BillingStatus != "NO_CHARGE" || result.BillingRecordID != 0 {
		t.Fatalf("non-billable QPS rejection invented a reservation: %+v", result)
	}
}

func TestDomainRouteRejectionIsNeverCallLogEligible(t *testing.T) {
	result := OpenResult{
		CallLogEligible: true,
		CacheHit:        true,
		RejectionKind:   OpenRejectionDomainRouteMismatch,
	}
	if shouldRecordCallLog(result) {
		t.Fatalf("domain route mismatch must not enter call logs: %+v", result)
	}
}

func TestOpenResolvesDomainRouteBeforeSpecifiedResponse(t *testing.T) {
	sourceBytes, err := os.ReadFile("service.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(sourceBytes)
	routeIndex := strings.Index(source, "resolveDomainRoute(api, req)")
	specifiedIndex := strings.Index(source, "if key.SpecifiedResponseEnabled == 1")
	if routeIndex < 0 || specifiedIndex < 0 {
		t.Fatal("cannot locate domain route and specified-response admission points")
	}
	if routeIndex > specifiedIndex {
		t.Fatalf("domain route resolution must precede specified response: route=%d specified=%d", routeIndex, specifiedIndex)
	}
}

func (gatewayFailingReadCloser) Read([]byte) (int, error) {
	return 0, stderrors.New("upstream body read failed")
}

func (gatewayFailingReadCloser) Close() error { return nil }

func TestInvokeOneTreatsResponseReadFailureAsBadGateway(t *testing.T) {
	service := &Service{httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: gatewayFailingReadCloser{}}, nil
	})}}
	result, err := service.invokeOne(context.Background(), http.MethodGet, UpstreamConfig{URL: "https://upstream.example/test"}, OpenRequest{})
	if err == nil || !strings.Contains(err.Error(), "read upstream response") {
		t.Fatalf("expected response read error, got result=%+v err=%v", result, err)
	}
	if result.ResponseStatus != consts.StatusBadGateway || result.Success {
		t.Fatalf("read failure must be a failed gateway result: %+v", result)
	}
	if !result.CallLogEligible || !shouldRecordCallLog(result) {
		t.Fatalf("response read failure happened after a real upstream request and must be logged: %+v", result)
	}
}

func TestInvokeOneRejectsOversizedUpstreamResponse(t *testing.T) {
	body := strings.Repeat("x", int(httpclient.MaxUpstreamResponseBytes)+1)
	service := &Service{httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}}
	result, err := service.invokeOne(context.Background(), http.MethodGet, UpstreamConfig{URL: "https://upstream.example/test"}, OpenRequest{})
	if !stderrors.Is(err, httpclient.ErrResponseTooLarge) {
		t.Fatalf("oversized response error = %v, want ErrResponseTooLarge", err)
	}
	if result.ResponseStatus != consts.StatusBadGateway || result.Success {
		t.Fatalf("oversized response must be a failed gateway result: %+v", result)
	}
	if !result.CallLogEligible || !shouldRecordCallLog(result) {
		t.Fatalf("oversized upstream response must remain part of API reliability statistics: %+v", result)
	}
}

func TestInvokeOneMarksRealHTTPResponseEligibleForCallLogs(t *testing.T) {
	service := &Service{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			Body:       io.NopCloser(strings.NewReader(`{"error":"upstream failed"}`)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Request:    request,
			StatusCode: http.StatusInternalServerError,
		}, nil
	})}}
	result, err := service.invokeOne(context.Background(), http.MethodGet, UpstreamConfig{URL: "https://upstream.example/test"}, OpenRequest{})
	if err != nil {
		t.Fatalf("real HTTP response returned transport error: %v", err)
	}
	if result.ResponseStatus != http.StatusInternalServerError || result.Success {
		t.Fatalf("unexpected upstream result: %+v", result)
	}
	if !result.CallLogEligible || !shouldRecordCallLog(result) {
		t.Fatalf("real upstream 5xx response must be logged: %+v", result)
	}
}

func TestInvokeOneDistinguishesNetworkFailureFromClientCancellation(t *testing.T) {
	networkService := &Service{httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, stderrors.New("dial tcp: connection refused")
	})}}
	networkResult, networkErr := networkService.invokeOne(context.Background(), http.MethodGet, UpstreamConfig{URL: "https://upstream.example/test"}, OpenRequest{})
	if networkErr == nil || !networkResult.CallLogEligible || !shouldRecordCallLog(networkResult) {
		t.Fatalf("real network failure must be logged: result=%+v err=%v", networkResult, networkErr)
	}

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelService := &Service{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return nil, request.Context().Err()
	})}}
	cancelledResult, cancelledErr := cancelService.invokeOne(cancelledCtx, http.MethodGet, UpstreamConfig{URL: "https://upstream.example/test"}, OpenRequest{})
	if !stderrors.Is(cancelledErr, context.Canceled) {
		t.Fatalf("cancelled request error = %v, want context canceled", cancelledErr)
	}
	if cancelledResult.CallLogEligible || shouldRecordCallLog(cancelledResult) {
		t.Fatalf("client cancellation must not enter call logs: %+v", cancelledResult)
	}
}

func TestValidateInterfaceBindingForKey(t *testing.T) {
	if err := validateInterfaceBindingForKey("INTERFACE", nil); err == nil {
		t.Fatal("expected INTERFACE key without bindings to be rejected")
	}
	if err := validateInterfaceBindingForKey("INTERFACE", []int64{1}); err != nil {
		t.Fatalf("expected INTERFACE key with bindings to pass, got %v", err)
	}
	if err := validateInterfaceBindingForKey("GLOBAL", nil); err != nil {
		t.Fatalf("expected GLOBAL key without bindings to pass, got %v", err)
	}
	if err := validateInterfaceBindingForKey("GLOBAL", []int64{1}); err == nil {
		t.Fatal("expected GLOBAL key with interface bindings to be rejected")
	}
	if err := validateInterfaceBindingForKey("UNKNOWN", nil); err == nil {
		t.Fatal("expected unknown key type to be rejected")
	}
}

func TestNormalizeGlobalKeyClearsInterfaceBindings(t *testing.T) {
	now := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.Local)
	service := &Service{
		now:  func() time.Time { return now },
		repo: &Repository{},
	}
	item, expiresAt, interfaceIDs, ipRules, err := service.normalizeKey(context.Background(), rbac.Principal{ID: 7}, SaveKeyRequest{
		InterfaceIDs: []int64{3, 2, 3},
		KeyType:      "GLOBAL",
		Name:         "global-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	if item.KeyType != "GLOBAL" || item.UserID != "7" {
		t.Fatalf("normalized key = %+v", item)
	}
	if expiresAt != nil || len(interfaceIDs) != 0 || len(ipRules) != 0 {
		t.Fatalf("global key retained scoped settings: expires=%v interfaces=%v ipRules=%v", expiresAt, interfaceIDs, ipRules)
	}
}

func TestPreserveOmittedKeyExpirationDistinguishesClearFromOmit(t *testing.T) {
	current := "2026-08-01 12:00:00"
	omitted := SaveKeyRequest{}
	preserveOmittedKeyExpiration(&omitted, current)
	if omitted.ExpiresAt == nil || *omitted.ExpiresAt != current {
		t.Fatalf("omitted expiration was not preserved: %#v", omitted.ExpiresAt)
	}

	empty := ""
	clear := SaveKeyRequest{ExpiresAt: &empty}
	preserveOmittedKeyExpiration(&clear, current)
	if clear.ExpiresAt == nil || *clear.ExpiresAt != "" {
		t.Fatalf("explicit empty expiration was not preserved as a clear request: %#v", clear.ExpiresAt)
	}
}

func TestQPSKeysUseUserInterfaceAndServiceClock(t *testing.T) {
	svc := &Service{now: func() time.Time {
		return time.Unix(12345, 0)
	}}
	if got, want := svc.accountQPSKey(1), "openapi:quota:qps:v3:account:1:12345"; got != want {
		t.Fatalf("accountQPSKey() = %q, want %q", got, want)
	}
	if got, want := svc.interfaceUserQPSKey(1, 77), "openapi:quota:qps:v3:interface-user:1:77:12345"; got != want {
		t.Fatalf("interfaceUserQPSKey() = %q, want %q", got, want)
	}
	if got, want := svc.interfaceSharedQPSKey(77), "openapi:quota:qps:v3:interface-shared:77:12345"; got != want {
		t.Fatalf("interfaceSharedQPSKey() = %q, want %q", got, want)
	}
}

func TestReserveQPSAllowsUnlimitedQuotaWithoutRedis(t *testing.T) {
	svc := &Service{}
	reservation, err := svc.reserveQPS(context.Background(), 1, BillingReservation{QPSLimit: 0})
	if err != nil {
		t.Fatalf("reserveQPS() unlimited error = %v", err)
	}
	if reservation.Reserved || reservation.Counted || len(reservation.Keys) > 0 {
		t.Fatalf("reserveQPS() unlimited reservation = %+v, want empty", reservation)
	}
}

func TestRequestQPSMinusOneBypassesRedis(t *testing.T) {
	reservation, err := (&Service{}).reserveRequestQPS(context.Background(), 1, InterfaceConfig{ID: 1, UserQPSLimit: -1}, BillingReservation{QPSLimit: 10, Reserved: true})
	if err != nil {
		t.Fatalf("QPS -1 returned error: %v", err)
	}
	if reservation.Reserved || reservation.Counted || len(reservation.Keys) > 0 {
		t.Fatalf("QPS -1 reservation = %+v, want empty", reservation)
	}
}

func TestCustomInterfaceQPSFailsClosedWithoutRedis(t *testing.T) {
	_, err := (&Service{}).reserveRequestQPS(context.Background(), 1, InterfaceConfig{ID: 1, UserQPSLimit: 1}, BillingReservation{QPSLimit: 100, Reserved: true})
	var business *apperrors.BusinessError
	if !stderrors.As(err, &business) || business.StatusCode != consts.StatusServiceUnavailable {
		t.Fatalf("reserveRequestQPS() error = %v, want service unavailable", err)
	}
}

func TestReserveQPSFailsClosedWithoutRedis(t *testing.T) {
	svc := &Service{}
	_, err := svc.reserveQPS(context.Background(), 1, BillingReservation{QPSLimit: 1})
	var business *apperrors.BusinessError
	if !stderrors.As(err, &business) || business.StatusCode != consts.StatusServiceUnavailable {
		t.Fatalf("reserveQPS() error = %v, want service unavailable", err)
	}
}

func TestReleaseReservedQPSNoopsWhenNotReserved(t *testing.T) {
	svc := &Service{}
	svc.releaseReservedQPS(context.Background(), qpsReservation{})
	svc.releaseReservedQPS(context.Background(), qpsReservation{Keys: []string{"qps"}, Counted: true})
}

func TestMarketTestKeyQueryRequiresEnabledInterface(t *testing.T) {
	query := marketTestKeyQuery()
	required := []string{
		"inner join bp_interface_api a on a.id = ? and a.status = 1",
		"k.status = 1",
		"k.key_type = 'GLOBAL' or exists",
	}
	for _, fragment := range required {
		if !strings.Contains(query, fragment) {
			t.Fatalf("marketTestKeyQuery() missing %q in %s", fragment, query)
		}
	}
}

func TestInvokeKeysQueryIsOwnerScopedAndExcludesExpiredKeys(t *testing.T) {
	query := invokeKeysQuery(false)
	for _, fragment := range []string{
		"k.user_id = ?",
		"k.status = 1",
		"k.expires_at is null or k.expires_at > now()",
		"k.key_type = 'GLOBAL' or exists",
		"bi.interface_id = ?",
		"limit ?",
	} {
		if !strings.Contains(strings.ToLower(query), strings.ToLower(fragment)) {
			t.Fatalf("invokeKeysQuery() missing %q in %s", fragment, query)
		}
	}
	if strings.Contains(strings.ToLower(query), "admin") {
		t.Fatal("invoke key query must not include an admin ownership bypass")
	}
	withKeyword := invokeKeysQuery(true)
	if !strings.Contains(strings.ToLower(withKeyword), "k.name like ?") {
		t.Fatalf("keyword query missing name filter: %s", withKeyword)
	}
	if got := escapeLikeKeyword(`a%b_c\d`); got != `a\%b\_c\\d` {
		t.Fatalf("escapeLikeKeyword() = %q", got)
	}
}

func TestFastestCandidatesIncludePrimaryURL(t *testing.T) {
	api := InterfaceConfig{
		PollingEnabled:  1,
		PollingMode:     "FASTEST",
		RequestURL:      "https://primary.example.com/api",
		UpstreamConfigs: []UpstreamConfig{{URL: "https://backup.example.com/api"}},
	}
	candidates := upstreamCandidates(api)
	if len(candidates) != 2 {
		t.Fatalf("len(candidates) = %d, want 2", len(candidates))
	}
	if candidates[0].URL != api.RequestURL {
		t.Fatalf("first candidate = %q, want primary %q", candidates[0].URL, api.RequestURL)
	}
}

func TestUpstreamCandidatesNormalizeHistoricalPollingMode(t *testing.T) {
	backup := UpstreamConfig{URL: "https://backup.example.com/api"}

	primary := upstreamCandidates(InterfaceConfig{
		PollingEnabled:  1,
		PollingMode:     "  primary  ",
		RequestURL:      "https://primary.example.com/api",
		UpstreamConfigs: []UpstreamConfig{backup},
	})
	if len(primary) != 2 || primary[0].URL != "https://primary.example.com/api" || primary[1].URL != backup.URL {
		t.Fatalf("trimmed/lowercase PRIMARY candidates = %+v", primary)
	}

	legacyEmpty := upstreamCandidates(InterfaceConfig{
		PollingEnabled:  1,
		PollingMode:     "",
		RequestURL:      "https://primary.example.com/api",
		UpstreamConfigs: []UpstreamConfig{backup},
	})
	if len(legacyEmpty) != 1 || legacyEmpty[0].URL != backup.URL {
		t.Fatalf("empty enabled mode must use ROUND_ROBIN semantics, candidates = %+v", legacyEmpty)
	}
}

func TestInvokeUpstreamsReportsNormalizedRuntimePollingMode(t *testing.T) {
	service := &Service{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			Body:       io.NopCloser(strings.NewReader(`{"code":200}`)),
			Header:     make(http.Header),
			Request:    request,
			StatusCode: http.StatusOK,
		}, nil
	})}}

	result, err := service.invokeUpstreams(context.Background(), InterfaceConfig{
		PollingEnabled:  1,
		PollingMode:     "  round_robin  ",
		UpstreamConfigs: []UpstreamConfig{{URL: "https://first.example/api"}},
	}, http.MethodGet, OpenRequest{})
	if err != nil {
		t.Fatalf("normalized runtime mode returned error: %v", err)
	}
	if result.PollingMode != "ROUND_ROBIN" {
		t.Fatalf("result polling mode = %q, want ROUND_ROBIN", result.PollingMode)
	}
}

func TestApplyTemplateMissingParameterReturnsBadRequest(t *testing.T) {
	_, _, err := applyTemplate("https://example.com/weather/{city}", map[string]string{})
	if err == nil {
		t.Fatal("expected missing parameter to return an error")
	}
	var business *apperrors.BusinessError
	if !stderrors.As(err, &business) {
		t.Fatalf("expected BusinessError, got %T", err)
	}
	if business.StatusCode != consts.StatusBadRequest {
		t.Fatalf("status = %d, want %d", business.StatusCode, consts.StatusBadRequest)
	}
	if !strings.Contains(business.Message, "city") {
		t.Fatalf("message %q does not mention missing parameter", business.Message)
	}
}

func TestInvokeOneMissingTemplateParameterDoesNotCallUpstream(t *testing.T) {
	called := false
	svc := &Service{
		httpClient: &http.Client{
			Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				called = true
				t.Fatal("upstream should not be called when required template parameter is missing")
				return nil, nil
			}),
		},
	}

	result, err := svc.invokeOne(
		context.Background(),
		http.MethodGet,
		UpstreamConfig{URL: "https://example.com/weather/{city}"},
		OpenRequest{Query: map[string][]string{}},
	)
	if err == nil {
		t.Fatal("expected missing template parameter to return an error")
	}
	if called {
		t.Fatal("upstream was called despite missing template parameter")
	}
	var business *apperrors.BusinessError
	if !stderrors.As(err, &business) {
		t.Fatalf("expected BusinessError, got %T", err)
	}
	if business.StatusCode != consts.StatusBadRequest {
		t.Fatalf("business status = %d, want %d", business.StatusCode, consts.StatusBadRequest)
	}
	if result.ResponseStatus != consts.StatusBadRequest {
		t.Fatalf("response status = %d, want %d", result.ResponseStatus, consts.StatusBadRequest)
	}
	if result.CallLogEligible || shouldRecordCallLog(result) {
		t.Fatalf("local template validation must not enter call logs: %+v", result)
	}
	if !strings.Contains(business.Message, "city") {
		t.Fatalf("message %q does not mention missing parameter", business.Message)
	}
}

func TestInvokeUpstreamsReturnsBusinessErrorForMissingTemplateParameter(t *testing.T) {
	svc := &Service{
		httpClient: &http.Client{
			Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Fatal("upstream should not be called when required template parameter is missing")
				return nil, nil
			}),
		},
	}

	result, err := svc.invokeUpstreams(
		context.Background(),
		InterfaceConfig{
			RequestURL:     "https://example.com/weather/{city}",
			RequestMethod:  http.MethodGet,
			PollingEnabled: 0,
		},
		http.MethodGet,
		OpenRequest{Query: map[string][]string{}},
	)
	if err == nil {
		t.Fatal("expected missing template parameter to return an error")
	}
	var business *apperrors.BusinessError
	if !stderrors.As(err, &business) {
		t.Fatalf("expected BusinessError, got %T", err)
	}
	if business.StatusCode != consts.StatusBadRequest {
		t.Fatalf("business status = %d, want %d", business.StatusCode, consts.StatusBadRequest)
	}
	if result.ResponseStatus != consts.StatusBadRequest {
		t.Fatalf("response status = %d, want %d", result.ResponseStatus, consts.StatusBadRequest)
	}
}

func TestInvokeUpstreamsStopsFallbackWhenCallerContextIsCanceled(t *testing.T) {
	service := &Service{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return nil, request.Context().Err()
	})}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	primary := "https://primary.example.com/api"
	result, err := service.invokeUpstreams(ctx, InterfaceConfig{
		ID:              1,
		PollingEnabled:  1,
		PollingMode:     "PRIMARY",
		RequestMethod:   http.MethodGet,
		RequestURL:      primary,
		UpstreamConfigs: []UpstreamConfig{{URL: "https://backup.example.com/api"}},
	}, http.MethodGet, OpenRequest{})
	if !stderrors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
	if result.UpstreamURL != primary || result.UpstreamSwitched {
		t.Fatalf("canceled request incorrectly advanced to fallback: %+v", result)
	}
}

func TestInvokeUpstreamsRoundRobinFallsBackToStableOrderWithoutRedis(t *testing.T) {
	var calls []string
	service := &Service{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls = append(calls, request.URL.String())
		return &http.Response{
			Body:       io.NopCloser(strings.NewReader(`{"code":200}`)),
			Header:     make(http.Header),
			Request:    request,
			StatusCode: http.StatusOK,
		}, nil
	})}}
	result, err := service.invokeUpstreams(context.Background(), InterfaceConfig{
		ID:             71,
		PollingEnabled: 1,
		PollingMode:    "ROUND_ROBIN",
		UpstreamConfigs: []UpstreamConfig{
			{URL: "https://first.example/api"},
			{URL: "https://second.example/api"},
		},
	}, http.MethodGet, OpenRequest{})
	if err != nil {
		t.Fatalf("ROUND_ROBIN without Redis returned error: %v", err)
	}
	if len(calls) != 1 || !strings.Contains(calls[0], "first.example") {
		t.Fatalf("stable fallback calls = %v", calls)
	}
	if !result.Success || result.UpstreamSwitched {
		t.Fatalf("unexpected fallback result: %+v", result)
	}
}

func TestRequestValidationRejectionsAreExplicitlyExcludedFromLogs(t *testing.T) {
	for _, result := range []requestvalidation.Result{
		requestvalidation.ResultInvalidParams,
		requestvalidation.ResultPlatformMismatch,
	} {
		decision := requestvalidation.Decision{Result: result}
		if requestValidationRejectionShouldLog(decision) {
			t.Fatalf("validation result %s was marked loggable", result)
		}
		kind := requestValidationRejectionKind(decision)
		if kind == OpenRejectionNone {
			t.Fatalf("validation result %s did not receive an explicit rejection kind", result)
		}
		if shouldRecordCallLog(OpenResult{CallLogEligible: true, RejectionKind: kind}) {
			t.Fatalf("explicit validation rejection %s entered call-log policy", kind)
		}
	}
}

func TestRequestValidationPassDoesNotCreateRejectionKind(t *testing.T) {
	decision := requestvalidation.Decision{Result: requestvalidation.ResultPass}
	if got := requestValidationRejectionKind(decision); got != OpenRejectionNone {
		t.Fatalf("pass decision rejection kind = %q", got)
	}
}

func TestInvokeUpstreamsRetriesBackupOnNonNotFound4xx(t *testing.T) {
	var calls []string
	service := &Service{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls = append(calls, request.URL.String())
		return &http.Response{
			Body:       io.NopCloser(strings.NewReader(`{"message":"invalid business input"}`)),
			Header:     make(http.Header),
			Request:    request,
			StatusCode: http.StatusBadRequest,
		}, nil
	})}}
	result, err := service.invokeUpstreams(context.Background(), InterfaceConfig{
		ID:              72,
		PollingEnabled:  1,
		PollingMode:     "PRIMARY",
		RequestURL:      "https://primary.example/api",
		UpstreamConfigs: []UpstreamConfig{{URL: "https://backup.example/api"}},
	}, http.MethodGet, OpenRequest{})
	if err == nil {
		t.Fatalf("expected all-nodes 4xx to surface upstream error, got nil")
	}
	if len(calls) != 2 {
		t.Fatalf("non-404 4xx should retry all nodes, calls = %v", calls)
	}
	if result.ResponseStatus != http.StatusBadRequest {
		t.Fatalf("response status = %d, want 400 (last node preserved)", result.ResponseStatus)
	}
	if len(result.TriedUpstreams) != 2 {
		t.Fatalf("TriedUpstreams = %v, want both nodes recorded", result.TriedUpstreams)
	}
}

func TestInvokeUpstreamsAppliesGlobalCheckToBackupWithoutNodeOverride(t *testing.T) {
	var calls []string
	service := &Service{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls = append(calls, request.URL.String())
		return &http.Response{
			Body:       io.NopCloser(strings.NewReader(`{"code":500,"message":"temporary failure"}`)),
			Header:     make(http.Header),
			Request:    request,
			StatusCode: http.StatusOK,
		}, nil
	})}}

	result, err := service.invokeUpstreams(context.Background(), InterfaceConfig{
		ID:                   721,
		PollingEnabled:       1,
		PollingMode:          "PRIMARY",
		RequestURL:           "https://primary.example/api",
		PollingCheckEnabled:  1,
		PollingCheckField:    "code",
		PollingCheckExpected: "200",
		// No stop code is configured: the global response check still applies
		// to every backup node. A zero-valued node check means inherit.
		UpstreamConfigs: []UpstreamConfig{{URL: "https://backup.example/api"}},
	}, http.MethodGet, OpenRequest{})
	if err == nil {
		t.Fatalf("non-success business response was accepted by an unconfigured backup: result=%+v calls=%v", result, calls)
	}
	if len(calls) != 2 {
		t.Fatalf("global response check must be applied to both nodes, calls=%v", calls)
	}
	if result.ResponseStatus != http.StatusOK || len(result.TriedUpstreams) != 2 {
		t.Fatalf("unexpected exhausted polling result: %+v", result)
	}
}

// Polling-enabled interfaces must treat an explicit business `code` field as
// a response contract even when legacy rows have both the global check toggle
// and stop-code empty.  A non-success, non-404 business code must not be
// accepted from the first node and must advance to the next candidate.
func TestInvokeUpstreamsRetriesNon404BusinessCodeWithoutExplicitCheck(t *testing.T) {
	var calls []string
	service := &Service{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls = append(calls, request.URL.String())
		body := `{"code":500,"message":"temporary"}`
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

	result, err := service.invokeUpstreams(context.Background(), InterfaceConfig{
		ID:              722,
		PollingEnabled:  1,
		PollingMode:     "PRIMARY",
		RequestURL:      "https://primary.example/api",
		UpstreamConfigs: []UpstreamConfig{{URL: "https://backup.example/api"}},
	}, http.MethodGet, OpenRequest{})
	if err != nil {
		t.Fatalf("backup success should be returned after non-404 business code: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("non-404 business code must trigger fallback even without explicit check, calls=%v", calls)
	}
	if !result.Success || !result.UpstreamSwitched || !strings.Contains(result.UpstreamURL, "backup.example") {
		t.Fatalf("unexpected fallback result: %+v", result)
	}
}

func TestInvokeUpstreamsDoesNotLetSlowHealthWriteBlockFallback(t *testing.T) {
	var calls []string
	service := &Service{
		httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			calls = append(calls, request.URL.String())
			body := `{"code":500,"message":"temporary"}`
			if strings.Contains(request.URL.Host, "backup") {
				body = `{"code":200}`
			}
			return &http.Response{
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Request:    request,
				StatusCode: http.StatusOK,
			}, nil
		})},
		healthRecordFn: func(ctx context.Context, _ int64, _ string, _ bool, _ int64) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	result, err := service.invokeUpstreams(ctx, InterfaceConfig{
		ID:               723,
		PollingEnabled:   1,
		PollingMode:      "PRIMARY",
		RequestTimeoutMs: 250,
		RequestURL:       "https://primary.example/api",
		UpstreamConfigs:  []UpstreamConfig{{URL: "https://backup.example/api"}},
	}, http.MethodGet, OpenRequest{})
	if err != nil {
		t.Fatalf("slow health metric write must not prevent backup success: %v", err)
	}
	if len(calls) != 2 || !result.Success || !result.UpstreamSwitched {
		t.Fatalf("fallback was blocked by health metric write: calls=%v result=%+v", calls, result)
	}
}

func TestInvokeUpstreamsStopsPollingOn404(t *testing.T) {
	var calls []string
	service := &Service{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls = append(calls, request.URL.String())
		return &http.Response{
			Body:       io.NopCloser(strings.NewReader(`{"message":"not found"}`)),
			Header:     make(http.Header),
			Request:    request,
			StatusCode: http.StatusNotFound,
		}, nil
	})}}
	result, err := service.invokeUpstreams(context.Background(), InterfaceConfig{
		ID:              73,
		PollingEnabled:  1,
		PollingMode:     "PRIMARY",
		RequestURL:      "https://primary.example/api",
		UpstreamConfigs: []UpstreamConfig{{URL: "https://backup.example/api"}},
	}, http.MethodGet, OpenRequest{})
	if err != nil {
		t.Fatalf("404 should terminate polling without gateway error: %v", err)
	}
	if len(calls) != 1 || !strings.Contains(calls[0], "primary.example") {
		t.Fatalf("404 must not retry another node: %v", calls)
	}
	if result.ResponseStatus != http.StatusNotFound || result.UpstreamSwitched {
		t.Fatalf("unexpected 404 result: %+v", result)
	}
}

func TestInvokeUpstreamsStopsPollingOnDefaultBusiness404Code(t *testing.T) {
	var calls []string
	service := &Service{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls = append(calls, request.URL.String())
		return &http.Response{
			Body:       io.NopCloser(strings.NewReader(`{"code":404,"message":"not found"}`)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Request:    request,
			StatusCode: http.StatusOK,
		}, nil
	})}}
	result, err := service.invokeUpstreams(context.Background(), InterfaceConfig{
		ID:              731,
		PollingEnabled:  1,
		PollingMode:     "PRIMARY",
		RequestURL:      "https://primary.example/api",
		UpstreamConfigs: []UpstreamConfig{{URL: "https://backup.example/api"}},
	}, http.MethodGet, OpenRequest{})
	if err != nil {
		t.Fatalf("business 404 should terminate polling without an error: %v", err)
	}
	if len(calls) != 1 || result.ResponseStatus != http.StatusOK || result.Success {
		t.Fatalf("unexpected business 404 result: calls=%v result=%+v", calls, result)
	}
}

func TestInvokeUpstreamsStopsPollingOnConfiguredBusinessCode(t *testing.T) {
	var calls []string
	service := &Service{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls = append(calls, request.URL.String())
		return &http.Response{
			Body:       io.NopCloser(strings.NewReader(`{"code":40401,"msg":"account not found"}`)),
			Header:     make(http.Header),
			Request:    request,
			StatusCode: http.StatusOK,
		}, nil
	})}}
	result, err := service.invokeUpstreams(context.Background(), InterfaceConfig{
		ID:                74,
		PollingEnabled:    1,
		PollingMode:       "PRIMARY",
		RequestURL:        "https://primary.example/api",
		PollingStopCode:   "40401",
		PollingCheckField: "code",
		UpstreamConfigs:   []UpstreamConfig{{URL: "https://backup.example/api"}},
	}, http.MethodGet, OpenRequest{})
	if err != nil {
		t.Fatalf("configured stop code should terminate polling without gateway error: %v", err)
	}
	if len(calls) != 1 || !strings.Contains(calls[0], "primary.example") {
		t.Fatalf("stop code must not retry another node: %v", calls)
	}
	if result.ResponseStatus != http.StatusOK || result.UpstreamSwitched || result.Success {
		t.Fatalf("unexpected stop-code result: %+v", result)
	}
}

func TestInvokeUpstreamsChecksConfiguredStopCodeBeforeResponseCleaning(t *testing.T) {
	var calls []string
	service := &Service{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls = append(calls, request.URL.String())
		return &http.Response{
			Body:       io.NopCloser(strings.NewReader(`{"code":0,"message":"terminal"}`)),
			Header:     make(http.Header),
			Request:    request,
			StatusCode: http.StatusOK,
		}, nil
	})}}

	result, err := service.invokeUpstreams(context.Background(), InterfaceConfig{
		ID:                      740,
		PollingEnabled:          1,
		PollingMode:             "PRIMARY",
		RequestURL:              "https://primary.example/api",
		PollingCheckEnabled:     1,
		PollingCheckField:       "code",
		PollingCheckExpected:    "200",
		PollingStopCode:         "0",
		ResponseCleaningEnabled: 1,
		UpstreamConfigs:         []UpstreamConfig{{URL: "https://backup.example/api"}},
	}, http.MethodGet, OpenRequest{})

	if err != nil {
		t.Fatalf("configured stop code should terminate before cleaning: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("terminal raw code must not advance to backup, calls=%v", calls)
	}
	if result.Success || result.ResponseStatus != http.StatusOK || !strings.Contains(result.Body, `"code":0`) {
		t.Fatalf("raw terminal response was not preserved: %+v", result)
	}
}

func TestInvokeUpstreamsRetriesWhenBodyCodeDiffersFromStopCode(t *testing.T) {
	var calls []string
	service := &Service{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls = append(calls, request.URL.String())
		status := http.StatusOK
		body := `{"code":40001,"msg":"rate limit"}`
		if strings.Contains(request.URL.Host, "backup") {
			body = `{"code":200}`
		}
		return &http.Response{
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
			Request:    request,
			StatusCode: status,
		}, nil
	})}}
	result, err := service.invokeUpstreams(context.Background(), InterfaceConfig{
		ID:                   75,
		PollingEnabled:       1,
		PollingMode:          "PRIMARY",
		RequestURL:           "https://primary.example/api",
		PollingStopCode:      "40401",
		PollingCheckField:    "code",
		PollingCheckExpected: "200",
		PollingCheckEnabled:  1,
		UpstreamConfigs:      []UpstreamConfig{{URL: "https://backup.example/api"}},
	}, http.MethodGet, OpenRequest{})
	if err != nil {
		t.Fatalf("body code != stop code should continue polling: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("expected fallback to backup node, calls = %v", calls)
	}
	if !result.Success || !result.UpstreamSwitched || !strings.Contains(result.UpstreamURL, "backup.example") {
		t.Fatalf("unexpected fallback result: %+v", result)
	}
}

func TestInvokeUpstreamsConfiguredStopCodeValidatesBusinessCodeWithoutPerNodeToggle(t *testing.T) {
	var calls []string
	service := &Service{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls = append(calls, request.URL.String())
		body := `{"code":500,"msg":"temporary business failure"}`
		if strings.Contains(request.URL.Host, "backup") {
			body = `{"code":200,"data":{"id":1}}`
		}
		return &http.Response{
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
			Request:    request,
			StatusCode: http.StatusOK,
		}, nil
	})}}

	result, err := service.invokeUpstreams(context.Background(), InterfaceConfig{
		ID:                   78,
		PollingEnabled:       1,
		PollingMode:          "PRIMARY",
		PollingStopCode:      "404",
		PollingCheckField:    "code",
		PollingCheckExpected: "200",
		RequestURL:           "https://primary.example/api",
		UpstreamConfigs:      []UpstreamConfig{{URL: "https://backup.example/api"}},
	}, http.MethodGet, OpenRequest{})
	if err != nil {
		t.Fatalf("configured code-aware polling returned error: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("non-success business code must retry the backup node, calls = %v", calls)
	}
	if !result.Success || !result.UpstreamSwitched || !strings.Contains(result.UpstreamURL, "backup.example") {
		t.Fatalf("unexpected configured code-aware polling result: %+v", result)
	}
}

func TestInvokeUpstreamsConfiguredStopCodeExhaustsAllNonSuccessBusinessCodes(t *testing.T) {
	var calls []string
	service := &Service{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls = append(calls, request.URL.String())
		return &http.Response{
			Body:       io.NopCloser(strings.NewReader(`{"code":500,"msg":"temporary business failure"}`)),
			Header:     make(http.Header),
			Request:    request,
			StatusCode: http.StatusOK,
		}, nil
	})}}

	result, err := service.invokeUpstreams(context.Background(), InterfaceConfig{
		ID:                   79,
		PollingEnabled:       1,
		PollingMode:          "PRIMARY",
		PollingStopCode:      "404",
		PollingCheckField:    "code",
		PollingCheckExpected: "200",
		RequestURL:           "https://primary.example/api",
		UpstreamConfigs: []UpstreamConfig{
			{URL: "https://backup1.example/api"},
			{URL: "https://backup2.example/api"},
		},
	}, http.MethodGet, OpenRequest{})
	if err == nil {
		t.Fatal("all non-success business codes must surface a polling failure")
	}
	if len(calls) != 3 || len(result.TriedUpstreams) != 3 {
		t.Fatalf("all candidates must be exhausted once, calls=%v tried=%v", calls, result.TriedUpstreams)
	}
}

func TestInvokeUpstreamsStopCodeResultKeepsEveryAttemptForSingleFailureLog(t *testing.T) {
	var calls []string
	service := &Service{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls = append(calls, request.URL.String())
		body := `{"code":500,"msg":"temporary business failure"}`
		if strings.Contains(request.URL.Host, "backup") {
			body = `{"code":404,"msg":"not found"}`
		}
		return &http.Response{
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
			Request:    request,
			StatusCode: http.StatusOK,
		}, nil
	})}}

	result, err := service.invokeUpstreams(context.Background(), InterfaceConfig{
		ID:                   80,
		PollingEnabled:       1,
		PollingMode:          "PRIMARY",
		PollingStopCode:      "404",
		PollingCheckField:    "code",
		PollingCheckExpected: "200",
		RequestURL:           "https://primary.example/api",
		UpstreamConfigs:      []UpstreamConfig{{URL: "https://backup.example/api"}},
	}, http.MethodGet, OpenRequest{})
	if err != nil {
		t.Fatalf("configured stop code should be returned as a business result: %v", err)
	}
	if len(calls) != 2 || len(result.TriedUpstreams) != 2 {
		t.Fatalf("failure log metadata must retain both attempts, calls=%v tried=%v", calls, result.TriedUpstreams)
	}
	if result.Success || !result.UpstreamSwitched {
		t.Fatalf("unexpected configured stop-code result: %+v", result)
	}
	if result.ErrorMessage != "not found" {
		t.Fatalf("stop-code error message = %q, want upstream business message", result.ErrorMessage)
	}
}

func TestShouldStopPollingDefaultsToHTTP404(t *testing.T) {
	api := InterfaceConfig{ID: 76}
	if shouldStopPolling(api, &OpenResult{ResponseStatus: http.StatusNotFound}) != true {
		t.Fatalf("default 404 must stop polling")
	}
	if shouldStopPolling(api, &OpenResult{ResponseStatus: http.StatusBadRequest}) != false {
		t.Fatalf("default non-404 must not stop polling")
	}
}

func TestShouldStopPollingConfiguredMatchesBodyCode(t *testing.T) {
	api := InterfaceConfig{ID: 77, PollingStopCode: "40401", PollingCheckField: "code"}
	if shouldStopPolling(api, &OpenResult{Body: `{"code":40401}`, ResponseStatus: http.StatusOK}) != true {
		t.Fatalf("matching body code must stop polling")
	}
	if shouldStopPolling(api, &OpenResult{Body: `{"code":200}`, ResponseStatus: http.StatusOK}) != false {
		t.Fatalf("non-matching body code must not stop polling")
	}
	// stop 配置后不再特判 HTTP 404
	if shouldStopPolling(api, &OpenResult{Body: `{}`, ResponseStatus: http.StatusNotFound}) != false {
		t.Fatalf("configured mode must not treat HTTP 404 specially")
	}
}

func TestEffectivePollingCheckInheritsGlobalRuleWhenNodeOverrideDisabled(t *testing.T) {
	api := InterfaceConfig{
		PollingCheckExpected: "0",
		PollingCheckField:    "meta.status",
		PollingStopCode:      "40401",
	}
	cfg := effectivePollingCheck(api, UpstreamConfig{
		PollingCheckEnabled:  0,
		PollingCheckExpected: "200",
		PollingCheckField:    "code",
	})
	if cfg.PollingCheckEnabled != 1 || cfg.PollingCheckField != "meta.status" || cfg.PollingCheckExpected != "0" {
		t.Fatalf("disabled node check did not inherit global rule: %+v", cfg)
	}

	override := effectivePollingCheck(api, UpstreamConfig{
		PollingCheckEnabled:  1,
		PollingCheckExpected: "OK",
		PollingCheckField:    "result",
	})
	if override.PollingCheckField != "result" || override.PollingCheckExpected != "OK" {
		t.Fatalf("enabled node override was not preserved: %+v", override)
	}
}

func TestInvokeUpstreamsRetriesBackupAfterUpstreamHTTP5xx(t *testing.T) {
	var calls []string
	service := &Service{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls = append(calls, request.URL.String())
		status := http.StatusInternalServerError
		body := `{"message":"temporary upstream failure"}`
		if strings.Contains(request.URL.Host, "backup") {
			status = http.StatusOK
			body = `{"code":200}`
		}
		return &http.Response{
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
			Request:    request,
			StatusCode: status,
		}, nil
	})}}
	result, err := service.invokeUpstreams(context.Background(), InterfaceConfig{
		ID:              73,
		PollingEnabled:  1,
		PollingMode:     "PRIMARY",
		RequestURL:      "https://primary.example/api",
		UpstreamConfigs: []UpstreamConfig{{URL: "https://backup.example/api"}},
	}, http.MethodGet, OpenRequest{})
	if err != nil {
		t.Fatalf("upstream 5xx fallback returned error: %v", err)
	}
	if len(calls) != 2 || !strings.Contains(calls[0], "primary.example") || !strings.Contains(calls[1], "backup.example") {
		t.Fatalf("upstream 5xx fallback calls = %v", calls)
	}
	if !result.Success || !result.UpstreamSwitched || !strings.Contains(result.UpstreamURL, "backup.example") {
		t.Fatalf("unexpected upstream 5xx fallback result: %+v", result)
	}
}

func TestOrderRoundRobinHealthGroupsRotatesOnlyWithinEachRank(t *testing.T) {
	now := time.Date(2026, time.August, 20, 10, 0, 0, 0, time.UTC)
	configs := []UpstreamConfig{
		{URL: "https://healthy-a.example/api"},
		{URL: "https://healthy-b.example/api"},
		{URL: "https://unknown.example/api"},
		{URL: "https://cooling.example/api"},
	}
	metrics := []upstreamhealth.ReadResult{
		{Found: true, Metric: upstreamhealth.Metric{Status: upstreamhealth.StatusHealthy}},
		{Found: true, Metric: upstreamhealth.Metric{Status: upstreamhealth.StatusHealthy}},
		{},
		{Found: true, Metric: upstreamhealth.Metric{
			CooldownUntil: strconv.FormatInt(now.Add(time.Minute).Unix(), 10),
			FailureCount:  2,
			Status:        upstreamhealth.StatusUnhealthy,
		}},
	}
	ordered := orderRoundRobinHealthGroups(configs, metrics, now, func(rank, size int) int {
		if rank == 0 && size == 2 {
			return 1
		}
		return 0
	})
	want := []string{
		"https://healthy-b.example/api",
		"https://healthy-a.example/api",
		"https://unknown.example/api",
		"https://cooling.example/api",
	}
	if len(ordered) != len(want) {
		t.Fatalf("ordered candidate count = %d, want %d", len(ordered), len(want))
	}
	for index, expected := range want {
		if ordered[index].URL != expected {
			t.Fatalf("ordered[%d] = %q, want %q", index, ordered[index].URL, expected)
		}
	}
}

func TestOrderRoundRobinHealthCandidatesPreservesOriginalNodeNumbers(t *testing.T) {
	now := time.Date(2026, time.August, 20, 10, 0, 0, 0, time.UTC)
	candidates := numberUpstreamCandidates([]UpstreamConfig{
		{URL: "https://healthy-a.example/api"},
		{URL: "https://healthy-b.example/api"},
		{URL: "https://cooling.example/api"},
	})
	metrics := []upstreamhealth.ReadResult{
		{Found: true, Metric: upstreamhealth.Metric{Status: upstreamhealth.StatusHealthy}},
		{Found: true, Metric: upstreamhealth.Metric{Status: upstreamhealth.StatusHealthy}},
		{Found: true, Metric: upstreamhealth.Metric{
			CooldownUntil: strconv.FormatInt(now.Add(time.Minute).Unix(), 10),
			FailureCount:  2,
			Status:        upstreamhealth.StatusUnhealthy,
		}},
	}
	ordered := orderRoundRobinHealthCandidates(candidates, metrics, now, func(_ int, size int) int {
		if size == 2 {
			return 1
		}
		return 0
	})
	wantURLs := []string{
		"https://healthy-b.example/api",
		"https://healthy-a.example/api",
		"https://cooling.example/api",
	}
	wantNodes := []int{2, 1, 3}
	for index := range wantURLs {
		if ordered[index].Config.URL != wantURLs[index] || ordered[index].Node != wantNodes[index] {
			t.Fatalf("ordered[%d] = %+v, want url=%q node=%d", index, ordered[index], wantURLs[index], wantNodes[index])
		}
	}
}

func TestClassifyUpstreamHealthRecordsNonStopResponseAndTransportFailures(t *testing.T) {
	tests := []struct {
		name       string
		result     OpenResult
		invokeErr  error
		cleanErr   error
		passed     bool
		wantRecord bool
		wantHealth bool
	}{
		{name: "local cleaning", result: OpenResult{ResponseStatus: http.StatusOK}, cleanErr: stderrors.New("mapping failed")},
		{name: "client response", result: OpenResult{ResponseStatus: http.StatusBadRequest}, wantRecord: true},
		{name: "non-stop not found response", result: OpenResult{ResponseStatus: http.StatusNotFound}, wantRecord: true},
		{name: "network error", invokeErr: stderrors.New("dial failed"), wantRecord: true},
		{name: "upstream timeout", invokeErr: context.DeadlineExceeded, wantRecord: true},
		{name: "server response", result: OpenResult{ResponseStatus: http.StatusInternalServerError}, wantRecord: true},
		{name: "validated success", result: OpenResult{ResponseStatus: http.StatusOK}, passed: true, wantRecord: true, wantHealth: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record, healthy := classifyUpstreamHealth(test.result, test.invokeErr, test.cleanErr, test.passed)
			if record != test.wantRecord || healthy != test.wantHealth {
				t.Fatalf("classifyUpstreamHealth() = (%v,%v), want (%v,%v)", record, healthy, test.wantRecord, test.wantHealth)
			}
		})
	}
}

func TestInvokeUpstreamsContinuesAfterMalformedCleanedJSON(t *testing.T) {
	var calls []string
	service := &Service{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls = append(calls, request.URL.String())
		body := `{"data":`
		if strings.Contains(request.URL.Host, "backup") {
			body = `{"result":{"videoUrl":"https://cdn.example/video.mp4"}}`
		}
		return &http.Response{
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Request:    request,
			StatusCode: http.StatusOK,
		}, nil
	})}}

	result, err := service.invokeUpstreams(context.Background(), InterfaceConfig{
		ID:                      42,
		PollingEnabled:          1,
		PollingMode:             "PRIMARY",
		RequestMethod:           http.MethodGet,
		RequestURL:              "https://primary.example/api",
		ResponseCleaningEnabled: 1,
		UpstreamConfigs:         []UpstreamConfig{{URL: "https://backup.example/api"}},
	}, http.MethodGet, OpenRequest{})
	if err != nil {
		t.Fatalf("invokeUpstreams returned error: %v", err)
	}
	if len(calls) != 2 || !strings.Contains(calls[0], "primary.example") || !strings.Contains(calls[1], "backup.example") {
		t.Fatalf("unexpected upstream call sequence: %v", calls)
	}
	if !result.UpstreamSwitched || !strings.Contains(result.UpstreamURL, "backup.example") {
		t.Fatalf("fallback metadata missing: %+v", result)
	}
	if !strings.Contains(result.Body, `"code":200`) || !strings.Contains(result.Body, `"type":"video"`) {
		t.Fatalf("fallback response was not normalized: %s", result.Body)
	}
}

func TestResponsePassesUsesExactArrayPathValues(t *testing.T) {
	cfg := UpstreamConfig{
		PollingCheckEnabled:  1,
		PollingCheckField:    "data.items.0.id",
		PollingCheckExpected: "9007199254740993",
	}
	if !responsePasses(cfg, http.StatusOK, `{"data":{"items":[{"id":9007199254740993}]}}`) {
		t.Fatal("exact large integer array path did not pass")
	}
	if responsePasses(cfg, http.StatusOK, `{"data":{"items":[{"id":9007199254740992}]}}`) {
		t.Fatal("different large integer passed after precision loss")
	}
	if responsePasses(UpstreamConfig{PollingCheckEnabled: 1, PollingCheckField: "missing", PollingCheckExpected: ""}, http.StatusOK, `{}`) {
		t.Fatal("missing response field matched an empty expected value")
	}
}

func TestBillingRulesCompareLargeAndNegativeNumbersExactly(t *testing.T) {
	if !billingRulesMatch([]BillingRule{{FieldName: "value", Operator: "GT", ExpectedValue: "9007199254740992"}}, `{"value":9007199254740993}`) {
		t.Fatal("large integer GT comparison lost precision")
	}
	if !billingRulesMatch([]BillingRule{{FieldName: "value", Operator: "LT", ExpectedValue: "-9007199254740992"}}, `{"value":-9007199254740993}`) {
		t.Fatal("negative integer LT comparison lost precision")
	}
	if billingRulesMatch([]BillingRule{{FieldName: "missing", Operator: "EQ", ExpectedValue: ""}}, `{}`) {
		t.Fatal("missing billing field matched an empty expected value")
	}
}

func TestAPIKeyQueryIsCaseInsensitiveAndNeverForwarded(t *testing.T) {
	query := map[string][]string{
		"Key":    {"bp_live_secret"},
		"normal": {"value"},
	}
	secret, err := apiKeyFromQuery(query)
	if err != nil || secret != "bp_live_secret" {
		t.Fatalf("apiKeyFromQuery() = %q, %v", secret, err)
	}
	if values := templateValues(OpenRequest{Query: query}); values["Key"] != "" {
		t.Fatalf("template values leaked API key: %#v", values)
	}
	target, err := appendQuery("https://upstream.example/path", query, nil)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	for name := range parsed.Query() {
		if strings.EqualFold(name, "key") {
			t.Fatalf("forwarded target leaked API key: %s", target)
		}
	}
	if parsed.Query().Get("normal") != "value" {
		t.Fatalf("safe query parameter was lost: %s", target)
	}
}

func TestAPIKeyQueryRejectsConflictingCaseVariants(t *testing.T) {
	_, err := apiKeyFromQuery(map[string][]string{
		"key": {"first"},
		"KEY": {"second"},
	})
	var business *apperrors.BusinessError
	if !stderrors.As(err, &business) || business.StatusCode != consts.StatusBadRequest {
		t.Fatalf("conflicting keys error = %v", err)
	}
}

func TestOpenRejectsConflictingHeaderAndQueryAPIKeys(t *testing.T) {
	service := &Service{}
	_, err := service.Open(context.Background(), "example", OpenRequest{
		Headers: map[string]string{"X-API-Key": "bp_live_header"},
		Query:   map[string][]string{"key": {"bp_live_query"}},
	})
	var business *apperrors.BusinessError
	if !stderrors.As(err, &business) || business.StatusCode != consts.StatusBadRequest {
		t.Fatalf("conflicting header/query keys error = %v", err)
	}
}

func TestOpenAllowsMatchingHeaderAndQueryAPIKeys(t *testing.T) {
	db, err := sql.Open("mysql", "invalid:invalid@tcp(127.0.0.1:1)/invalid?timeout=1ms")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	service := NewService(NewRepository(db), nil, nil, "gateway-risk-key-with-at-least-thirty-two-bytes")
	_, err = service.Open(context.Background(), "example", OpenRequest{
		Headers: map[string]string{"X-API-Key": "bp_live_same"},
		Query:   map[string][]string{"key": {"bp_live_same"}},
	})
	var business *apperrors.BusinessError
	if stderrors.As(err, &business) && business.StatusCode == consts.StatusBadRequest {
		t.Fatalf("matching header/query keys were rejected as ambiguous: %v", err)
	}
}

func TestDisplayUpdateSlotsAreBoundedAndReusable(t *testing.T) {
	service := &Service{displaySlots: make(chan struct{}, 1)}
	if !service.tryAcquireDisplaySlot() {
		t.Fatal("first display update slot was not acquired")
	}
	if service.tryAcquireDisplaySlot() {
		t.Fatal("display update slot exceeded its configured capacity")
	}
	service.releaseDisplaySlot()
	if !service.tryAcquireDisplaySlot() {
		t.Fatal("released display update slot was not reusable")
	}
	service.releaseDisplaySlot()
}

func TestDisplayUpdateSlotsFailClosedWhenUnconfigured(t *testing.T) {
	service := &Service{}
	if service.tryAcquireDisplaySlot() {
		t.Fatal("unconfigured display update slots accepted background work")
	}
	service.releaseDisplaySlot()
}

func TestCopyForwardHeadersRemovesHopByHopAndSpoofedIdentity(t *testing.T) {
	request, err := http.NewRequest(http.MethodPost, "https://upstream.example/path", nil)
	if err != nil {
		t.Fatal(err)
	}
	copyForwardHeaders(request, map[string]string{
		"Accept-Encoding":     "br",
		"Authorization":       "Bearer secret",
		"Connection":          "X-Hop, keep-alive",
		"Content-Length":      "999",
		"Content-Type":        "application/json",
		"Cookie":              "session=secret",
		"Forwarded":           "for=9.9.9.9",
		"Proxy-Authorization": "Basic secret",
		"Transfer-Encoding":   "chunked",
		"X-API-Key":           "bp_live_secret",
		"X-Custom":            "safe",
		"X-Hop":               "remove-me",
		"X-Real-IP":           "9.9.9.9",
		"X-Unsafe":            "line\r\nbreak",
	}, "1.2.3.4")

	if request.Header.Get("Content-Type") != "application/json" || request.Header.Get("X-Custom") != "safe" {
		t.Fatalf("safe headers were not forwarded: %#v", request.Header)
	}
	for _, name := range []string{
		"Accept-Encoding", "Authorization", "Connection", "Content-Length", "Cookie", "Forwarded",
		"Proxy-Authorization", "Transfer-Encoding", "X-API-Key", "X-Hop", "X-Unsafe",
	} {
		if value := request.Header.Get(name); value != "" {
			t.Fatalf("blocked header %s leaked as %q", name, value)
		}
	}
	if request.Header.Get("X-Forwarded-For") != "1.2.3.4" || request.Header.Get("X-Real-IP") != "1.2.3.4" {
		t.Fatalf("controlled client IP headers missing: %#v", request.Header)
	}
}

func TestOpenRejectsOversizedAPIKeyBeforeRepositoryLookup(t *testing.T) {
	service := &Service{}
	_, err := service.Open(context.Background(), "example", OpenRequest{Headers: map[string]string{
		"X-API-Key": strings.Repeat("k", 257),
	}})
	var business *apperrors.BusinessError
	if !stderrors.As(err, &business) || business.StatusCode != consts.StatusBadRequest {
		t.Fatalf("oversized API key error = %v", err)
	}
}

func TestInvalidKeyGuardKeysDoNotExposeClientIdentity(t *testing.T) {
	service := &Service{fingerprintKey: deriveFingerprintKey("invalid-key-guard-secret-with-at-least-thirty-two-bytes")}
	attemptKey := service.invalidKeyAttemptKey("phone_lookup", "203.0.113.42")
	cacheKey := invalidKeyCacheKey(secretHash("bp_live_super_secret"))
	for _, raw := range []string{"203.0.113.42", "phone_lookup", "bp_live_super_secret", secretHash("bp_live_super_secret")} {
		if strings.Contains(attemptKey, raw) || strings.Contains(cacheKey, raw) {
			t.Fatalf("invalid-key Redis keys leaked %q: %q %q", raw, attemptKey, cacheKey)
		}
	}
	if !strings.HasPrefix(attemptKey, "gateway:risk:invalid-key:attempt:") || !strings.HasPrefix(cacheKey, "gateway:risk:invalid-key:cache:") {
		t.Fatalf("unexpected invalid-key Redis keys: %q %q", attemptKey, cacheKey)
	}
}

func TestInvalidKeyGuardFailsClosedWhenRedisIsUnavailable(t *testing.T) {
	client := redis.NewClient(&redis.Options{
		Addr:         "127.0.0.1:1",
		DialTimeout:  20 * time.Millisecond,
		MaxRetries:   -1,
		ReadTimeout:  20 * time.Millisecond,
		WriteTimeout: 20 * time.Millisecond,
	})
	t.Cleanup(func() { _ = client.Close() })
	service := &Service{
		fingerprintKey: deriveFingerprintKey("invalid-key-guard-secret-with-at-least-thirty-two-bytes"),
		rdb:            client,
	}
	err := service.guardInvalidKeyLookup(context.Background(), "phone_lookup", "203.0.113.42", secretHash("invalid"))
	var business *apperrors.BusinessError
	if !stderrors.As(err, &business) || business.StatusCode != consts.StatusServiceUnavailable {
		t.Fatalf("guardInvalidKeyLookup() error = %v, want service unavailable", err)
	}
}

func TestRequestParamsLogCanonicalizesQueryAndDropsKey(t *testing.T) {
	first := requestParamsForLog(map[string][]string{
		"Key":    {"bp_live_secret"},
		"apiKey": {"query-secret"},
		"b":      {"2"},
		"a":      {"3", "1"},
	}, nil)
	second := requestParamsForLog(map[string][]string{
		"a":      {"1", "3"},
		"apiKey": {"query-secret"},
		"key":    {"bp_live_secret"},
		"b":      {"2"},
	}, nil)

	if first != second {
		t.Fatalf("canonical query log mismatch:\nfirst=%s\nsecond=%s", first, second)
	}
	for _, secret := range []string{"bp_live_secret", "query-secret"} {
		if strings.Contains(first, secret) {
			t.Fatalf("query log leaked secret %q: %s", secret, first)
		}
	}
	if strings.Contains(first, `"Key"`) || strings.Contains(first, `"key"`) {
		t.Fatalf("query log kept open-gateway key parameter: %s", first)
	}
	if !strings.Contains(first, `"a":["1","3"]`) || !strings.Contains(first, `"b":["2"]`) {
		t.Fatalf("query log did not keep normalized parameters: %s", first)
	}
}

func TestRequestParamsLogMasksStructuredBodyBeforePersisting(t *testing.T) {
	got := requestParamsForLog(
		map[string][]string{"email": {"alice@example.com"}, "normal": {"ok"}},
		[]byte(`{"password":"pw","nested":{"token":"tok","mobile":"13800138000"},"normal":"body-ok"}`),
	)

	for _, secret := range []string{"alice@example.com", `"pw"`, `"tok"`, "13800138000"} {
		if strings.Contains(got, secret) {
			t.Fatalf("request log leaked %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, "body-ok") || !strings.Contains(got, "ok") {
		t.Fatalf("request log lost non-sensitive shape: %s", got)
	}
	if !strings.Contains(got, "[REDACTED]") || !strings.Contains(got, "138****8000") {
		t.Fatalf("request log did not apply expected masks: %s", got)
	}
}

func TestRequestParamsLogParsesFormBodyAndRedactsPlainBody(t *testing.T) {
	form := requestParamsForLog(nil, []byte("password=pw&normal=ok&mobile=13800138000"))
	for _, secret := range []string{"pw", "13800138000"} {
		if strings.Contains(form, secret) {
			t.Fatalf("form request log leaked %q: %s", secret, form)
		}
	}
	if !strings.Contains(form, "ok") || !strings.Contains(form, "138****8000") {
		t.Fatalf("form request log did not preserve safe values: %s", form)
	}

	plain := requestParamsForLog(nil, []byte("raw token secret text"))
	if strings.Contains(plain, "raw token secret text") {
		t.Fatalf("plain request body should not be persisted: %s", plain)
	}
	if !strings.Contains(plain, nonStructuredBodyRedact) {
		t.Fatalf("plain request body did not use redaction marker: %s", plain)
	}
}

func TestRequestFingerprintCanonicalizesAndDistinguishesSensitiveValues(t *testing.T) {
	key := deriveFingerprintKey("test-risk-secret-with-at-least-thirty-two-bytes")
	first := requestFingerprintForRisk(key,
		map[string][]string{"b": {"2"}, "key": {"bp_live_first"}, "a": {"3", "1"}},
		[]byte(`{"normal":"ok","password":"first-secret"}`),
	)
	reordered := requestFingerprintForRisk(key,
		map[string][]string{"a": {"1", "3"}, "Key": {"bp_live_second"}, "b": {"2"}},
		[]byte(`{"password":"first-secret","normal":"ok"}`),
	)
	differentSecret := requestFingerprintForRisk(key,
		map[string][]string{"a": {"1", "3"}, "b": {"2"}},
		[]byte(`{"password":"second-secret","normal":"ok"}`),
	)

	if first != reordered {
		t.Fatalf("canonical request fingerprint changed after harmless reordering: %s != %s", first, reordered)
	}
	if first == differentSecret {
		t.Fatal("different sensitive request values must not collapse into one risk fingerprint")
	}
	if len(first) != sha256.Size*2 {
		t.Fatalf("fingerprint length = %d, want %d", len(first), sha256.Size*2)
	}
	for _, secret := range []string{"first-secret", "second-secret", "bp_live_first", "bp_live_second"} {
		if strings.Contains(first, secret) || strings.Contains(reordered, secret) || strings.Contains(differentSecret, secret) {
			t.Fatalf("fingerprint leaked source secret %q", secret)
		}
	}
}

func TestRequestFingerprintPreservesLargeJSONIntegerPrecision(t *testing.T) {
	key := deriveFingerprintKey("test-risk-secret-with-at-least-thirty-two-bytes")
	first := requestFingerprintForRisk(key, nil, []byte(`{"id":9007199254740992}`))
	second := requestFingerprintForRisk(key, nil, []byte(`{"id":9007199254740993}`))
	if first == second {
		t.Fatal("distinct large JSON integers collapsed into one risk fingerprint")
	}
}

func TestRequestFingerprintCanonicalizesEquivalentJSONNumbers(t *testing.T) {
	key := deriveFingerprintKey("test-risk-secret-with-at-least-thirty-two-bytes")
	fingerprints := []string{
		requestFingerprintForRisk(key, nil, []byte(`{"value":1000}`)),
		requestFingerprintForRisk(key, nil, []byte(`{"value":1000.0}`)),
		requestFingerprintForRisk(key, nil, []byte(`{"value":1e3}`)),
		requestFingerprintForRisk(key, nil, []byte(`{"value":10E2}`)),
	}
	for index := 1; index < len(fingerprints); index++ {
		if fingerprints[index] != fingerprints[0] {
			t.Fatalf("equivalent JSON numbers produced different fingerprints: %v", fingerprints)
		}
	}
	stringFingerprint := requestFingerprintForRisk(key, nil, []byte(`{"value":"1e3"}`))
	if stringFingerprint == fingerprints[0] {
		t.Fatal("JSON number and JSON string collided in the risk fingerprint")
	}
}

func TestCanonicalizeJSONNumberDoesNotExpandHugeExponent(t *testing.T) {
	got := canonicalizeJSONNumber("1e1000000")
	if got != "1e1000000" {
		t.Fatalf("huge exponent canonical form = %q", got)
	}
	if len(got) > 16 {
		t.Fatalf("huge exponent canonicalization expanded unexpectedly: %d bytes", len(got))
	}
}

func TestRequestArtifactsDoNotTreatDuplicateJSONFieldsAsStructuredData(t *testing.T) {
	key := deriveFingerprintKey("test-risk-secret-with-at-least-thirty-two-bytes")
	ambiguousBody := []byte(`{"id":1,"id":2}`)
	params, ambiguousFingerprint := requestArtifactsForRisk(key, nil, ambiguousBody)
	structuredFingerprint := requestFingerprintForRisk(key, nil, []byte(`{"id":2}`))
	if ambiguousFingerprint == structuredFingerprint {
		t.Fatal("ambiguous duplicate-key JSON collapsed into the structured request fingerprint")
	}
	if strings.Contains(params, string(ambiguousBody)) || !strings.Contains(params, nonStructuredBodyRedact) {
		t.Fatalf("ambiguous JSON body was persisted instead of redacted: %s", params)
	}
}

func TestApplyOpenRejectionPreservesDetailedBusinessError(t *testing.T) {
	result := OpenResult{
		Billable:       true,
		ChargeAmount:   "1.0000",
		ChargeType:     "BALANCE",
		ResponseStatus: 502,
		Success:        true,
	}
	applyOpenRejection(&result, apperrors.New(consts.StatusBadRequest, "缺少必填参数: phone"))
	if result.Success || result.Billable {
		t.Fatalf("rejected result remained successful or billable: %+v", result)
	}
	if result.ResponseStatus != consts.StatusBadRequest || result.ErrorMessage != "缺少必填参数: phone" {
		t.Fatalf("rejected result lost detailed error: %+v", result)
	}
}

func TestNewOpenBaseResultUsesServiceRiskFingerprintKey(t *testing.T) {
	service := &Service{fingerprintKey: deriveFingerprintKey("gateway-risk-key-with-at-least-thirty-two-bytes")}
	req := OpenRequest{
		Body:     []byte(`{"phone":"13800138000"}`),
		ClientIP: "203.0.113.10",
		Method:   "post",
		Query:    map[string][]string{"page": {"1"}},
		RawQuery: "page=1",
	}
	result := service.newOpenBaseResult(context.Background(), KeyAuth{ID: 7, KeyPrefix: "bp_live_abc", UserID: 9}, InterfaceConfig{ID: 11, APICode: "phone_lookup"}, req)
	want := requestFingerprintForRisk(service.fingerprintKey, req.Query, req.Body)
	if result.RequestFingerprint != want {
		t.Fatalf("base result fingerprint = %q, want service-key fingerprint %q", result.RequestFingerprint, want)
	}
	if result.APIKeyID != 7 || result.InterfaceID != 11 || result.UserID != 9 || result.RequestMethod != "POST" {
		t.Fatalf("unexpected rejected-call base result: %+v", result)
	}
}

func TestOpenRequestDisplayNeverExposesClientIP(t *testing.T) {
	// 回归测试：历史缺陷曾把客户端 IP 写入 request_value，导致“链接或值”列与“调用IP”列相同。
	req := OpenRequest{
		Body:     []byte(`{"phone":"13800138000"}`),
		ClientIP: "203.0.113.10",
		Method:   "post",
		Query:    map[string][]string{"page": {"1"}},
		RawQuery: "page=1",
	}
	display := openRequestDisplay(req)
	if strings.Contains(display, "203.0.113.10") {
		t.Fatalf("openRequestDisplay leaked client IP: %s", display)
	}
	if !strings.Contains(display, "page") {
		t.Fatalf("openRequestDisplay should prefer query params: %s", display)
	}
	bodyOnly := openRequestDisplay(OpenRequest{Body: []byte(`{"phone":"13800138000"}`), ClientIP: "203.0.113.10", Method: "post"})
	if strings.Contains(bodyOnly, "203.0.113.10") {
		t.Fatalf("openRequestDisplay leaked client IP in body-only request: %s", bodyOnly)
	}
	if !strings.Contains(bodyOnly, "phone") {
		t.Fatalf("openRequestDisplay should fall back to request body: %s", bodyOnly)
	}
}

func TestOpenRejectionLogKeysDoNotExposeClientIP(t *testing.T) {
	service := &Service{
		fingerprintKey: deriveFingerprintKey("gateway-risk-key-with-at-least-thirty-two-bytes"),
		now:            func() time.Time { return time.Unix(1_700_000_000, 0) },
	}
	globalKey, fineKey := service.openRejectionLogKeys(OpenResult{
		APIKeyID:           7,
		ClientIP:           "203.0.113.99",
		InterfaceID:        11,
		RequestFingerprint: strings.Repeat("a", 64),
	})
	if strings.Contains(globalKey, "203.0.113.99") || strings.Contains(fineKey, "203.0.113.99") {
		t.Fatalf("rejection limiter key leaked client IP: %q %q", globalKey, fineKey)
	}
	if !strings.Contains(globalKey, ":7:11:") || !strings.Contains(fineKey, strings.Repeat("a", 64)) {
		t.Fatalf("rejection limiter keys lost their bounded grouping dimensions: %q %q", globalKey, fineKey)
	}
}

func TestOpenRejectionLogLimiterFailsClosedWhenRedisIsUnavailable(t *testing.T) {
	client := redis.NewClient(&redis.Options{
		Addr:         "127.0.0.1:1",
		DialTimeout:  20 * time.Millisecond,
		MaxRetries:   -1,
		ReadTimeout:  20 * time.Millisecond,
		WriteTimeout: 20 * time.Millisecond,
	})
	t.Cleanup(func() { _ = client.Close() })
	service := &Service{
		fingerprintKey: deriveFingerprintKey("gateway-risk-key-with-at-least-thirty-two-bytes"),
		now:            time.Now,
		rdb:            client,
	}
	if service.allowOpenRejectionLog(context.Background(), OpenResult{APIKeyID: 7, InterfaceID: 11, ClientIP: "203.0.113.99"}) {
		t.Fatal("rejection logging limiter allowed a database write while Redis was unavailable")
	}
}

func TestOpenRejectionLimiterWarningIsThrottled(t *testing.T) {
	current := time.Unix(1_700_000_000, 0)
	service := &Service{now: func() time.Time { return current }}
	if !service.acquireOpenRejectionLimiterWarning() {
		t.Fatal("first rejection limiter warning was suppressed")
	}
	if service.acquireOpenRejectionLimiterWarning() {
		t.Fatal("duplicate rejection limiter warning was not throttled")
	}
	current = current.Add(openRejectionWarnThrottle - time.Second)
	if service.acquireOpenRejectionLimiterWarning() {
		t.Fatal("warning was emitted before the throttle window elapsed")
	}
	current = current.Add(2 * time.Second)
	if !service.acquireOpenRejectionLimiterWarning() {
		t.Fatal("warning remained suppressed after the throttle window elapsed")
	}
}

func TestOpenRejectionLimiterWarningAllowsOneConcurrentEmitter(t *testing.T) {
	service := &Service{now: func() time.Time { return time.Unix(1_700_000_000, 0) }}
	const workers = 64
	var wait sync.WaitGroup
	emitted := make(chan struct{}, workers)
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if service.acquireOpenRejectionLimiterWarning() {
				emitted <- struct{}{}
			}
		}()
	}
	wait.Wait()
	close(emitted)
	count := 0
	for range emitted {
		count++
	}
	if count != 1 {
		t.Fatalf("concurrent rejection limiter warnings = %d, want 1", count)
	}
}

func TestBackgroundLifecycleWaitsAndRejectsNewTasksDuringClose(t *testing.T) {
	service := &Service{}
	started := make(chan struct{})
	release := make(chan struct{})
	if !service.startBackground(func() {
		close(started)
		<-release
	}) {
		t.Fatal("initial background task was rejected")
	}
	<-started

	shortCtx, cancelShort := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelShort()
	if err := service.Close(shortCtx); !stderrors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close() error = %v, want deadline exceeded while task is active", err)
	}
	sharedDone := service.backgroundDone
	if sharedDone == nil {
		t.Fatal("Close() did not initialize the shared background completion channel")
	}
	secondCtx, cancelSecond := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelSecond()
	if err := service.Close(secondCtx); !stderrors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second Close() error = %v, want deadline exceeded while task is active", err)
	}
	if service.backgroundDone != sharedDone {
		t.Fatal("repeated Close() replaced the shared background completion channel")
	}
	if service.startBackground(func() {}) {
		t.Fatal("background task was accepted after shutdown started")
	}

	close(release)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := service.Close(ctx); err != nil {
		t.Fatalf("Close() after releasing task: %v", err)
	}
}

func TestSameParamsRiskRunsOutsideRequestContext(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	service := &Service{
		rdb:            &redis.Client{},
		backgroundDone: make(chan struct{}),
		sameParamsRiskFn: func(ctx context.Context, _ OpenResult) {
			close(started)
			if ctx.Err() != nil {
				t.Errorf("same-parameter risk worker inherited a canceled request context: %v", ctx.Err())
			}
			<-release
		},
	}

	requestCtx, cancel := context.WithCancel(context.Background())
	service.recordSameParamsRiskAsync(requestCtx, OpenResult{
		APIKeyID:           7,
		InterfaceID:        11,
		RequestFingerprint: strings.Repeat("a", 64),
	})
	cancel()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("same-parameter risk worker did not start asynchronously")
	}
	close(release)
	if err := service.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestSameParamsRiskDropsTasksWhenBackgroundRiskSlotsAreFull(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	service := &Service{
		rdb:       &redis.Client{},
		riskSlots: make(chan struct{}, 1),
		sameParamsRiskFn: func(context.Context, OpenResult) {
			started <- struct{}{}
			<-release
		},
	}
	result := OpenResult{
		APIKeyID:           7,
		InterfaceID:        11,
		RequestFingerprint: strings.Repeat("a", 64),
	}

	service.recordSameParamsRiskAsync(context.Background(), result)
	service.recordSameParamsRiskAsync(context.Background(), result)

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first same-parameter risk task did not start")
	}
	select {
	case <-started:
		t.Fatal("same-parameter risk started an unbounded second task")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	if err := service.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestUpstreamValidationMessageCarriesUpstreamBusinessText(t *testing.T) {
	body := `{"code":500,"msg":"余额不足","data":null}`
	result := &OpenResult{Body: body}
	if text := upstreamValidationMessage(result); text != "余额不足" {
		t.Fatalf("validation message = %q, want upstream msg", text)
	}
	if text := firstBusinessMessage(body); text != "余额不足" {
		t.Fatalf("firstBusinessMessage = %q, want upstream msg", text)
	}
}

func TestUpstreamValidationMessageFallsBackWithoutBusinessText(t *testing.T) {
	result := &OpenResult{Body: `{"code":500}`}
	if text := upstreamValidationMessage(result); text != "上游响应校验失败" {
		t.Fatalf("validation message = %q, want generic fallback", text)
	}
	if text := firstBusinessMessage(`not-json`); text != "" {
		t.Fatalf("firstBusinessMessage on non-JSON = %q, want empty", text)
	}
	if text := firstBusinessMessage(`{"data":{"nested":1}}`); text != "" {
		t.Fatalf("firstBusinessMessage without msg/message/error = %q, want empty", text)
	}
}

func TestApplySafeUpstreamFailureResponsePassesThroughUpstreamBusinessMsg(t *testing.T) {
	result := &OpenResult{
		ErrorMessage:        "上游响应校验失败",
		ResponseStatus:      http.StatusOK,
		UpstreamBusinessMsg: "余额不足",
	}
	applySafeUpstreamFailureResponse(result)
	var body map[string]any
	if err := json.Unmarshal([]byte(result.Body), &body); err != nil {
		t.Fatalf("safe failure response is not valid JSON: %v", err)
	}
	if body["message"] != "余额不足" || body["error"] != "余额不足" {
		t.Fatalf("upstream business message not passed through: %s", result.Body)
	}
	if result.ResponseStatus != consts.StatusBadGateway {
		t.Fatalf("response status = %d, want bad gateway", result.ResponseStatus)
	}
}

func TestInvokeUpstreamsSurfacesUpstreamBusinessMsgOnPollingCheckFailure(t *testing.T) {
	service := &Service{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			Body:       io.NopCloser(strings.NewReader(`{"code":500,"msg":"目标无数据，请更换查询条件"}`)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Request:    request,
			StatusCode: http.StatusOK,
		}, nil
	})}}
	result, err := service.invokeUpstreams(context.Background(), InterfaceConfig{
		ID:                      88,
		PollingEnabled:          0,
		PollingCheckEnabled:     1,
		PollingCheckField:       "code",
		PollingCheckExpected:    "200",
		RequestMethod:           http.MethodGet,
		RequestURL:              "https://example.com/api",
		ResponseCleaningEnabled: 0,
	}, http.MethodGet, OpenRequest{})
	if err == nil {
		t.Fatal("expected polling check failure to return an error")
	}
	if result.UpstreamBusinessMsg != "目标无数据，请更换查询条件" {
		t.Fatalf("UpstreamBusinessMsg = %q, want upstream msg", result.UpstreamBusinessMsg)
	}
}

func TestStripCooldownMetricsRemovesCoolingNodesAndKeepsOne(t *testing.T) {
	now := time.Date(2026, time.August, 29, 18, 0, 0, 0, time.UTC)
	candidates := []numberedUpstreamCandidate{
		{Config: UpstreamConfig{URL: "https://a.example/api"}, Node: 1},
		{Config: UpstreamConfig{URL: "https://b.example/api"}, Node: 2},
		{Config: UpstreamConfig{URL: "https://c.example/api"}, Node: 3},
	}
	metrics := []upstreamhealth.ReadResult{
		{Found: true, Metric: upstreamhealth.Metric{
			Status:        upstreamhealth.StatusUnhealthy,
			FailureCount:  9,
			CooldownUntil: strconv.FormatInt(now.Add(time.Minute).Unix(), 10),
		}},
		{Found: true, Metric: upstreamhealth.Metric{Status: upstreamhealth.StatusHealthy}},
		{Found: true, Metric: upstreamhealth.Metric{
			Status:        upstreamhealth.StatusUnhealthy,
			FailureCount:  3,
			CooldownUntil: strconv.FormatInt(now.Add(-time.Minute).Unix(), 10),
		}},
	}
	kept := stripCooldownMetrics(candidates, metrics, now)
	if len(kept) != 2 {
		t.Fatalf("kept count = %d, want 2 (a cooling, c cooldown expired)", len(kept))
	}
	if kept[0].Config.URL != "https://b.example/api" || kept[1].Config.URL != "https://c.example/api" {
		t.Fatalf("kept candidates = %+v", kept)
	}
}

func TestStripCooldownMetricsExcludesEveryCoolingNode(t *testing.T) {
	now := time.Date(2026, time.August, 29, 18, 0, 0, 0, time.UTC)
	candidates := []numberedUpstreamCandidate{
		{Config: UpstreamConfig{URL: "https://a.example/api"}, Node: 1},
		{Config: UpstreamConfig{URL: "https://b.example/api"}, Node: 2},
	}
	metrics := []upstreamhealth.ReadResult{
		{Found: true, Metric: upstreamhealth.Metric{
			Status:        upstreamhealth.StatusUnhealthy,
			CooldownUntil: strconv.FormatInt(now.Add(time.Hour).Unix(), 10),
		}},
		{Found: true, Metric: upstreamhealth.Metric{
			Status:        upstreamhealth.StatusUnhealthy,
			CooldownUntil: strconv.FormatInt(now.Add(time.Hour).Unix(), 10),
		}},
	}
	kept := stripCooldownMetrics(candidates, metrics, now)
	if len(kept) != 0 {
		t.Fatalf("cooling candidates must stay out of the polling table, got %+v", kept)
	}
}

func TestStripCooldownMetricsExcludesSingleCoolingNode(t *testing.T) {
	now := time.Date(2026, time.August, 29, 18, 0, 0, 0, time.UTC)
	candidates := []numberedUpstreamCandidate{
		{Config: UpstreamConfig{URL: "https://only.example/api"}, Node: 1},
	}
	metrics := []upstreamhealth.ReadResult{
		{Found: true, Metric: upstreamhealth.Metric{
			Status:        upstreamhealth.StatusUnhealthy,
			FailureCount:  2,
			CooldownUntil: strconv.FormatInt(now.Add(time.Hour).Unix(), 10),
		}},
	}
	if kept := stripCooldownMetrics(candidates, metrics, now); len(kept) != 0 {
		t.Fatalf("single cooling node must stay out of the polling table, got %+v", kept)
	}
}

func TestInvokeUpstreamsMergesTriedUpstreamsIntoSingleError(t *testing.T) {
	var calls []string
	service := &Service{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls = append(calls, request.URL.String())
		return &http.Response{
			Body:       io.NopCloser(strings.NewReader(`{"message":"boom"}`)),
			Header:     make(http.Header),
			Request:    request,
			StatusCode: http.StatusInternalServerError,
		}, nil
	})}}
	result, err := service.invokeUpstreams(context.Background(), InterfaceConfig{
		ID:              90,
		PollingEnabled:  1,
		PollingMode:     "PRIMARY",
		RequestURL:      "https://primary.example/api",
		UpstreamConfigs: []UpstreamConfig{{URL: "https://backup1.example/api"}, {URL: "https://backup2.example/api"}},
	}, http.MethodGet, OpenRequest{})
	if err == nil {
		t.Fatal("expected all-nodes failure to return error")
	}
	if len(calls) != 3 {
		t.Fatalf("expected all 3 nodes attempted, calls = %v", calls)
	}
	if len(result.TriedUpstreams) != 3 {
		t.Fatalf("TriedUpstreams = %v, want 3 entries", result.TriedUpstreams)
	}
	if !strings.Contains(err.Error(), "HTTP 500") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("merged error should carry upstream business message: %v", err)
	}
	if result.TriedUpstreams[0] != "https://primary.example/api" ||
		result.TriedUpstreams[1] != "https://backup1.example/api" ||
		result.TriedUpstreams[2] != "https://backup2.example/api" {
		t.Fatalf("TriedUpstreams order/content = %v", result.TriedUpstreams)
	}
}
