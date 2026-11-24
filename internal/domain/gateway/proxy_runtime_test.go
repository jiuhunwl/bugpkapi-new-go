package gateway

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"bugpk-api/internal/pkg/httpclient"
	"bugpk-api/internal/pkg/proxypool"
)

func TestInvokeOneRoutesThroughConfiguredFixedProxy(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("gateway-ok"))
	}))
	defer upstream.Close()

	var proxyCalls atomic.Int64
	proxyServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		proxyCalls.Add(1)
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

	base := httpclient.New(httpclient.Options{AllowPrivateNetworks: true, Timeout: 2 * time.Second})
	service := &Service{httpClient: base, proxyResolver: proxypool.NewResolver(base)}
	host, port := proxyTestHostPort(t, strings.TrimPrefix(proxyServer.URL, "http://"))
	result, err := service.invokeOne(context.Background(), http.MethodGet, UpstreamConfig{URL: upstream.URL}, OpenRequest{}, proxypool.Config{
		Enabled:  1,
		Host:     host,
		Port:     port,
		Protocol: proxypool.ProtocolHTTP,
		Source:   proxypool.SourceFixed,
	})
	if err != nil {
		t.Fatalf("invokeOne() error = %v", err)
	}
	if result.Body != "gateway-ok" || proxyCalls.Load() != 1 {
		t.Fatalf("body=%q proxyCalls=%d", result.Body, proxyCalls.Load())
	}
}

func TestInvokeOneRefreshesAPIProxyOnceAfterConnectionFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("gateway-ok"))
	}))
	defer upstream.Close()

	var proxyCalls atomic.Int64
	proxyServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		proxyCalls.Add(1)
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
	workingHost, workingPort := proxyTestHostPort(t, strings.TrimPrefix(proxyServer.URL, "http://"))

	closedListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedHost, closedPort := proxyTestHostPort(t, closedListener.Addr().String())
	if err := closedListener.Close(); err != nil {
		t.Fatal(err)
	}

	var poolCalls atomic.Int64
	poolServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		call := poolCalls.Add(1)
		host, port := closedHost, closedPort
		if call > 1 {
			host, port = workingHost, workingPort
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"host":"` + host + `","port":` + strconv.Itoa(port) + `}`))
	}))
	defer poolServer.Close()

	base := httpclient.New(httpclient.Options{AllowPrivateNetworks: true, Timeout: 2 * time.Second})
	service := &Service{httpClient: base, proxyResolver: proxypool.NewResolver(base)}
	result, err := service.invokeOne(context.Background(), http.MethodGet, UpstreamConfig{URL: upstream.URL}, OpenRequest{}, proxypool.Config{
		APIURL:      poolServer.URL,
		Enabled:     1,
		Protocol:    proxypool.ProtocolHTTP,
		RefreshSecs: 60,
		Source:      proxypool.SourceAPI,
	})
	if err != nil {
		t.Fatalf("invokeOne() error = %v", err)
	}
	if result.Body != "gateway-ok" {
		t.Fatalf("body = %q, want gateway-ok", result.Body)
	}
	if poolCalls.Load() != 2 {
		t.Fatalf("pool calls = %d, want one initial resolve and one forced refresh", poolCalls.Load())
	}
	if proxyCalls.Load() != 1 {
		t.Fatalf("working proxy calls = %d, want 1", proxyCalls.Load())
	}
}

func proxyTestHostPort(t *testing.T, address string) (string, int) {
	t.Helper()
	host, rawPort, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", address, err)
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil {
		t.Fatalf("Atoi(%q): %v", rawPort, err)
	}
	return host, port
}
