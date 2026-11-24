package gateway

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/pkg/upstreamhealth"
	"github.com/redis/go-redis/v9"
)

// coolingHealthHook supplies an active cooldown metric for every HGETALL in
// the gateway health lookup.  Pipeline calls are intercepted so this test is
// deterministic and does not need a Redis process.
type coolingHealthHook struct {
	until string
}

func (h *coolingHealthHook) DialHook(next redis.DialHook) redis.DialHook { return next }

func (h *coolingHealthHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		return next(ctx, cmd)
	}
}

func (h *coolingHealthHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		for _, cmd := range cmds {
			if !strings.EqualFold(cmd.Name(), "hgetall") {
				continue
			}
			value, ok := cmd.(*redis.MapStringStringCmd)
			if !ok {
				continue
			}
			value.SetVal(map[string]string{
				"status":        upstreamhealth.StatusUnhealthy,
				"failureCount":  "2",
				"cooldownUntil": h.until,
			})
		}
		return nil
	}
}

type keyedHealthHook struct {
	values map[string]map[string]string
}

func (h *keyedHealthHook) DialHook(next redis.DialHook) redis.DialHook { return next }

func (h *keyedHealthHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		return next(ctx, cmd)
	}
}

func (h *keyedHealthHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		for _, cmd := range cmds {
			if !strings.EqualFold(cmd.Name(), "hgetall") {
				continue
			}
			value, ok := cmd.(*redis.MapStringStringCmd)
			if !ok || len(cmd.Args()) < 2 {
				continue
			}
			redisKey := fmt.Sprint(cmd.Args()[1])
			if fields, found := h.values[redisKey]; found {
				value.SetVal(fields)
			}
		}
		return nil
	}
}

func gatewayHealthKey(apiID int64, upstreamURL string) string {
	sum := sha1.Sum([]byte(strings.TrimSpace(upstreamURL)))
	return fmt.Sprintf("upstream:health:%d:%s", apiID, hex.EncodeToString(sum[:]))
}

func mixedHealthClient(t *testing.T, apiID int64, readyURL, coolingURL string) *redis.Client {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	client.AddHook(&keyedHealthHook{values: map[string]map[string]string{
		gatewayHealthKey(apiID, readyURL): {
			"status":       upstreamhealth.StatusHealthy,
			"checkedAt":    time.Now().UTC().Format(time.RFC3339),
			"latencyMs":    "5",
			"failureCount": "0",
		},
		gatewayHealthKey(apiID, coolingURL): {
			"status":        upstreamhealth.StatusUnhealthy,
			"checkedAt":     time.Now().UTC().Format(time.RFC3339),
			"latencyMs":     "25",
			"failureCount":  "2",
			"cooldownUntil": strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10),
		},
	}})
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// A health cooldown is an ordering hint, not permission to skip the only
// available recovery path.  When every node is cooling and the current
// response is non-terminal, the gateway must still issue a half-open probe and
// walk the remaining candidates instead of returning with zero HTTP calls.
func TestInvokeUpstreamsProbesCoolingCandidatesInsteadOfSkippingPolling(t *testing.T) {
	var calls []string
	service := &Service{
		httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
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
		})},
		healthRecordFn: func(context.Context, int64, string, bool, int64) error { return nil },
	}
	// A failed health read must not prevent the gateway from issuing a
	// half-open probe.  The hook supplies an active cooldown metric for every
	// candidate without requiring a live Redis server.
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	client.AddHook(&coolingHealthHook{until: strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)})
	service.rdb = client
	t.Cleanup(func() { _ = client.Close() })

	result, err := service.invokeUpstreams(context.Background(), InterfaceConfig{
		ID:              992,
		PollingEnabled:  1,
		PollingMode:     "PRIMARY",
		RequestURL:      "https://primary.example/api",
		UpstreamConfigs: []UpstreamConfig{{URL: "https://backup.example/api"}},
	}, http.MethodGet, OpenRequest{})
	if err != nil {
		t.Fatalf("cooling fallback should reach the healthy backup: %v", err)
	}
	if len(calls) != 2 || !strings.Contains(calls[0], "primary.example") || !strings.Contains(calls[1], "backup.example") {
		t.Fatalf("cooling nodes were skipped instead of probed: calls=%v result=%+v", calls, result)
	}
	if !result.Success || !result.UpstreamSwitched {
		t.Fatalf("unexpected cooling fallback result: %+v", result)
	}
}

// A cooling candidate stays behind healthy/unknown candidates, but it must
// remain in the same request's fallback chain.  Otherwise a non-terminal
// business failure from the ready node can never discover that the cooling
// backup has recovered.
func TestInvokeUpstreamsFallsBackToCoolingCandidateAfterNonTerminalCode(t *testing.T) {
	const apiID int64 = 994
	const primaryURL = "https://primary.example/api"
	const backupURL = "https://backup.example/api"
	var calls []string
	service := &Service{
		httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
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
		})},
		healthRecordFn: func(context.Context, int64, string, bool, int64) error { return nil },
	}
	service.rdb = mixedHealthClient(t, apiID, primaryURL, backupURL)

	result, err := service.invokeUpstreams(context.Background(), InterfaceConfig{
		ID:              apiID,
		PollingEnabled:  1,
		PollingMode:     "PRIMARY",
		RequestURL:      primaryURL,
		UpstreamConfigs: []UpstreamConfig{{URL: backupURL}},
	}, http.MethodGet, OpenRequest{})
	if err != nil {
		t.Fatalf("cooling backup should recover after a non-terminal primary code: %v", err)
	}
	if len(calls) != 2 || !strings.Contains(calls[0], "primary.example") || !strings.Contains(calls[1], "backup.example") {
		t.Fatalf("ready node must precede the cooling fallback: calls=%v result=%+v", calls, result)
	}
	if !result.Success || !result.UpstreamSwitched {
		t.Fatalf("unexpected recovered fallback result: %+v", result)
	}
}

// Retaining cooling nodes as last-resort candidates must not generate extra
// traffic when an earlier healthy/unknown node already returned valid data.
func TestInvokeUpstreamsDoesNotProbeCoolingCandidateAfterReadySuccess(t *testing.T) {
	const apiID int64 = 995
	const primaryURL = "https://primary.example/api"
	const backupURL = "https://backup.example/api"
	var calls []string
	service := &Service{
		httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			calls = append(calls, request.URL.String())
			if strings.Contains(request.URL.Host, "backup") {
				t.Fatal("cooling backup must not be called after a ready-node success")
			}
			return &http.Response{
				Body:       io.NopCloser(strings.NewReader(`{"code":200,"data":{"ok":true}}`)),
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Request:    request,
				StatusCode: http.StatusOK,
			}, nil
		})},
		healthRecordFn: func(context.Context, int64, string, bool, int64) error { return nil },
	}
	service.rdb = mixedHealthClient(t, apiID, primaryURL, backupURL)

	result, err := service.invokeUpstreams(context.Background(), InterfaceConfig{
		ID:              apiID,
		PollingEnabled:  1,
		PollingMode:     "PRIMARY",
		RequestURL:      primaryURL,
		UpstreamConfigs: []UpstreamConfig{{URL: backupURL}},
	}, http.MethodGet, OpenRequest{})
	if err != nil {
		t.Fatalf("ready-node success returned error: %v", err)
	}
	if len(calls) != 1 || !result.Success || result.UpstreamSwitched {
		t.Fatalf("ready-node success should stop before cooling fallback: calls=%v result=%+v", calls, result)
	}
}
