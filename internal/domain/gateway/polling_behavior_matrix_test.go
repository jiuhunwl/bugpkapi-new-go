package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestGatewayPollingBusinessCodeBehaviorMatrix(t *testing.T) {
	nonTerminalBodies := []struct {
		name       string
		body       string
		statusCode int
	}{
		{name: "number-negative-one", body: `{"code":-1,"message":"temporary"}`, statusCode: http.StatusOK},
		{name: "string-negative-one", body: `{"code":"-1","message":"temporary"}`, statusCode: http.StatusOK},
		{name: "number-400", body: `{"code":400,"message":"temporary"}`, statusCode: http.StatusOK},
		{name: "string-400", body: `{"code":"400","message":"temporary"}`, statusCode: http.StatusOK},
		{name: "number-500", body: `{"code":500,"message":"temporary"}`, statusCode: http.StatusOK},
		{name: "string-500", body: `{"code":"500","message":"temporary"}`, statusCode: http.StatusOK},
		{name: "http-400", body: `{"code":-1,"message":"temporary"}`, statusCode: http.StatusBadRequest},
		{name: "http-500", body: `{"code":-1,"message":"temporary"}`, statusCode: http.StatusInternalServerError},
		{name: "http-502", body: `{"code":-1,"message":"temporary"}`, statusCode: http.StatusBadGateway},
	}
	terminalResponses := []struct {
		name       string
		body       string
		statusCode int
	}{
		{name: "http-404", body: `{"message":"not found"}`, statusCode: http.StatusNotFound},
		{name: "business-number-404", body: `{"code":404,"message":"not found"}`, statusCode: http.StatusOK},
		{name: "business-string-404", body: `{"code":"404","message":"not found"}`, statusCode: http.StatusOK},
	}

	for _, mode := range []string{"PRIMARY", "ROUND_ROBIN"} {
		for _, cleaningEnabled := range []int{0, 1} {
			for _, response := range nonTerminalBodies {
				mode := mode
				cleaningEnabled := cleaningEnabled
				response := response
				t.Run(fmt.Sprintf("non-terminal/%s/cleaning-%d/%s", mode, cleaningEnabled, response.name), func(t *testing.T) {
					var calls []string
					service := &Service{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
						calls = append(calls, request.URL.String())
						body := response.body
						if strings.Contains(request.URL.Host, "backup") {
							body = `{"code":200,"data":{"ok":true}}`
						}
						statusCode := response.statusCode
						if strings.Contains(request.URL.Host, "backup") {
							statusCode = http.StatusOK
						}
						return pollingMatrixHTTPResponse(request, statusCode, body), nil
					})}}

					result, err := service.invokeUpstreams(context.Background(), gatewayPollingMatrixConfig(mode, cleaningEnabled), http.MethodGet, OpenRequest{})
					if err != nil {
						t.Fatalf("successful backup returned error: %v", err)
					}
					if len(calls) != 2 || !result.Success || !result.UpstreamSwitched || !strings.Contains(result.UpstreamURL, "backup") {
						t.Fatalf("non-404 business response did not advance to backup: calls=%v result=%+v", calls, result)
					}
				})
			}

			for _, response := range terminalResponses {
				mode := mode
				cleaningEnabled := cleaningEnabled
				response := response
				t.Run(fmt.Sprintf("terminal/%s/cleaning-%d/%s", mode, cleaningEnabled, response.name), func(t *testing.T) {
					var calls []string
					service := &Service{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
						calls = append(calls, request.URL.String())
						return pollingMatrixHTTPResponse(request, response.statusCode, response.body), nil
					})}}

					result, err := service.invokeUpstreams(context.Background(), gatewayPollingMatrixConfig(mode, cleaningEnabled), http.MethodGet, OpenRequest{})
					if err != nil {
						t.Fatalf("404 terminal response returned error: %v", err)
					}
					if len(calls) != 1 || result.Success || result.UpstreamSwitched || result.ResponseStatus != response.statusCode {
						t.Fatalf("404 response did not terminate on the first node: calls=%v result=%+v", calls, result)
					}
				})
			}
		}
	}
}

func gatewayPollingMatrixConfig(mode string, cleaningEnabled int) InterfaceConfig {
	config := InterfaceConfig{
		ID:                      9950,
		PollingEnabled:          1,
		PollingMode:             mode,
		ResponseCleaningEnabled: cleaningEnabled,
		RequestURL:              "https://primary.example/api",
	}
	if mode == "ROUND_ROBIN" {
		config.UpstreamConfigs = []UpstreamConfig{
			{URL: "https://primary.example/api"},
			{URL: "https://backup.example/api"},
		}
		return config
	}
	config.UpstreamConfigs = []UpstreamConfig{{URL: "https://backup.example/api"}}
	return config
}

func pollingMatrixHTTPResponse(request *http.Request, statusCode int, body string) *http.Response {
	return &http.Response{
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Request:    request,
		StatusCode: statusCode,
	}
}
