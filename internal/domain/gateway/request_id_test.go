package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"bugpk-api/internal/pkg/requestid"
	hertzapp "github.com/cloudwego/hertz/pkg/app"
)

func TestOpenErrorResponseIncludesRequestID(t *testing.T) {
	seen := map[string]bool{}
	for _, uri := range []string{"/api/example?bad=%zz", "/api/example"} {
		c := hertzapp.NewContext(0)
		c.Request.SetRequestURI(uri)
		c.Request.Header.Set("X-Request-ID", "client-forged")
		NewHandler(&Service{}).open(context.Background(), c)
		id := string(c.Response.Header.Peek("X-Request-ID"))
		if id == "" || id == "client-forged" || seen[id] {
			t.Fatalf("request ID must be server generated and unique: %q", id)
		}
		seen[id] = true
		var body map[string]json.RawMessage
		if err := json.Unmarshal(c.Response.Body(), &body); err != nil {
			t.Fatal(err)
		}
		var jsonID string
		if err := json.Unmarshal(body["requestId"], &jsonID); err != nil || jsonID != id {
			t.Fatalf("JSON/header ID mismatch: %s / %q", c.Response.Body(), id)
		}
	}
}

func TestUpstreamFallbackCarriesOneRequestID(t *testing.T) {
	var ids []string
	service := &Service{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		ids = append(ids, req.Header.Get("X-Request-ID"))
		body := `{"code":500}`
		if len(ids)%2 == 0 {
			body = `{"code":200,"data":9007199254740993}`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})}}
	api := InterfaceConfig{ID: 991, PollingEnabled: 1, PollingMode: "PRIMARY", RequestURL: "https://primary.example/api", PollingCheckEnabled: 1, PollingCheckField: "code", PollingCheckExpected: "200", PollingStopCode: "404", UpstreamConfigs: []UpstreamConfig{{URL: "https://backup.example/api"}}}
	for n := 0; n < 2; n++ {
		result, err := service.invokeUpstreams(context.Background(), api, http.MethodGet, OpenRequest{Headers: map[string]string{"X-Request-ID": "forged", "x-request-id": "also-forged"}})
		if err != nil {
			t.Fatal(err)
		}
		if len(ids) != (n+1)*2 {
			t.Fatalf("expected fallback: %v", ids)
		}
		id := ids[n*2]
		if id == "" || id == "forged" || id == "also-forged" || id != ids[n*2+1] || result.TraceID != id {
			t.Fatalf("inconsistent request IDs: ids=%v result=%q", ids, result.TraceID)
		}
	}
	if ids[0] == ids[2] {
		t.Fatalf("different requests reused ID: %v", ids)
	}
}

func TestCallLogBaseUsesRequestContextID(t *testing.T) {
	ctx := requestid.Ensure(context.Background())
	service := &Service{}
	base := service.newOpenBaseResult(ctx, KeyAuth{}, InterfaceConfig{}, OpenRequest{Headers: map[string]string{"X-Request-ID": "forged"}})
	if base.TraceID != requestid.FromContext(ctx) || base.TraceID == "" {
		t.Fatalf("log ID differs from request context: %q", base.TraceID)
	}
}
