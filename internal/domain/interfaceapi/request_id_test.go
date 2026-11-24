package interfaceapi

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestManagementUpstreamIncludesRequestID(t *testing.T) {
	var id string
	service := &Service{httpClient: &http.Client{Transport: requestIDTransport(func(req *http.Request) (*http.Response, error) {
		id = req.Header.Get("X-Request-ID")
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"code":200}`)), Request: req}, nil
	})}}
	_, err := service.invokeOne(context.Background(), InterfaceItem{}, http.MethodGet, UpstreamConfig{URL: "https://upstream.example/api"}, InvokePayload{})
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("management invoke has no feedback request ID")
	}
}

type requestIDTransport func(*http.Request) (*http.Response, error)

func (fn requestIDTransport) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }
