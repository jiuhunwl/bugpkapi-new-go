package httpclient

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWithProxyURLRoutesRequestThroughAuthenticatedHTTPProxy(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("upstream-ok"))
	}))
	defer upstream.Close()

	var calls atomic.Int64
	proxyServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.Header.Get("Proxy-Authorization") != "Basic dXNlcjpwYXNz" {
			t.Fatalf("Proxy-Authorization = %q", request.Header.Get("Proxy-Authorization"))
		}
		outbound := request.Clone(request.Context())
		outbound.RequestURI = ""
		response, err := http.DefaultTransport.RoundTrip(outbound)
		if err != nil {
			http.Error(writer, err.Error(), http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		writer.WriteHeader(response.StatusCode)
		_, _ = io.Copy(writer, response.Body)
	}))
	defer proxyServer.Close()

	proxyURL := strings.Replace(proxyServer.URL, "http://", "http://user:pass@", 1)
	client, err := WithProxyURL(New(Options{AllowPrivateNetworks: true, Timeout: 2 * time.Second}), proxyURL)
	if err != nil {
		t.Fatalf("WithProxyURL() error = %v", err)
	}
	response, err := client.Get(upstream.URL)
	if err != nil {
		t.Fatalf("proxied request failed: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read proxied response body: %v", err)
	}
	if string(body) != "upstream-ok" || calls.Load() != 1 {
		t.Fatalf("body=%q proxyCalls=%d", body, calls.Load())
	}
}

func TestWithProxyURLSupportsConfiguredProtocolsAndRejectsInvalidURLs(t *testing.T) {
	base := Outbound(time.Second)
	for _, value := range []string{
		"http://proxy.example.com:8080",
		"https://proxy.example.com:8443",
		"socks5://user:pass@proxy.example.com:1080",
	} {
		if _, err := WithProxyURL(base, value); err != nil {
			t.Fatalf("WithProxyURL(%q) error = %v", value, err)
		}
	}
	for _, value := range []string{"", "ftp://proxy.example.com:21", "http://proxy.example.com"} {
		if _, err := WithProxyURL(base, value); err == nil {
			t.Fatalf("WithProxyURL(%q) unexpectedly succeeded", value)
		}
	}
}
