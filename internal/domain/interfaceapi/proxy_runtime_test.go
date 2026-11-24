package interfaceapi

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
	"bugpk-api/internal/pkg/upstreamhealth"
)

func TestProbeOneRoutesThroughConfiguredFixedProxy(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
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
	result := service.probeOne(context.Background(), InterfaceItem{
		RequestMethod: "GET",
		ProxyPoolConfig: proxypool.Config{
			Enabled:  1,
			Host:     host,
			Port:     port,
			Protocol: proxypool.ProtocolHTTP,
			Source:   proxypool.SourceFixed,
		},
	}, UpstreamConfig{URL: upstream.URL})
	if result.HealthStatus != upstreamhealth.StatusHealthy || proxyCalls.Load() != 1 {
		t.Fatalf("result=%+v proxyCalls=%d", result, proxyCalls.Load())
	}
}

func TestInvokeOneDoesNotDirectConnectWhenProxyPoolResolutionFails(t *testing.T) {
	var upstreamCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		_, _ = writer.Write([]byte("must-not-be-called"))
	}))
	defer upstream.Close()
	pool := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "pool unavailable", http.StatusBadGateway)
	}))
	defer pool.Close()

	base := httpclient.New(httpclient.Options{AllowPrivateNetworks: true, Timeout: 2 * time.Second})
	service := &Service{httpClient: base, proxyResolver: proxypool.NewResolver(base)}
	_, err := service.invokeOne(context.Background(), InterfaceItem{ProxyPoolConfig: proxypool.Config{
		APIURL:      pool.URL,
		Enabled:     1,
		Protocol:    proxypool.ProtocolHTTP,
		RefreshSecs: 30,
		Source:      proxypool.SourceAPI,
	}}, http.MethodGet, UpstreamConfig{URL: upstream.URL}, InvokePayload{})
	if err == nil {
		t.Fatal("invokeOne() unexpectedly succeeded")
	}
	if upstreamCalls.Load() != 0 {
		t.Fatalf("upstreamCalls=%d, want 0", upstreamCalls.Load())
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
