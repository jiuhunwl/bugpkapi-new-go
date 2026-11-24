package interfaceapi

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"bugpk-api/internal/pkg/httpclient"
	"bugpk-api/internal/pkg/proxypool"
)

func TestProxyPoolConfigTestUsesDisabledFixedProxyDraft(t *testing.T) {
	proxyServer, proxyCalls := newExitIPProxyServer(t, `{"ip":"8.8.8.8"}`)
	host, port := serverHostPort(t, proxyServer)
	base := httpclient.New(httpclient.Options{AllowPrivateNetworks: true, Timeout: time.Second})

	result, err := testProxyPoolConfig(context.Background(), proxypool.NewResolver(base), base, proxypool.Config{
		Enabled:  0,
		Host:     host,
		Password: "must-not-leak",
		Port:     port,
		Protocol: proxypool.ProtocolHTTP,
		Source:   proxypool.SourceFixed,
		Username: "proxy-user",
	}, "http://public-exit-check.example/ip")
	if err != nil {
		t.Fatalf("testProxyPoolConfig() error = %v", err)
	}
	if proxyCalls.Load() != 1 {
		t.Fatalf("proxy calls = %d, want 1", proxyCalls.Load())
	}
	if !result.Success || result.ExitIP != "8.8.8.8" || result.Protocol != proxypool.ProtocolHTTP {
		t.Fatalf("proxy test result = %+v", result)
	}
	if result.ProxyAddress != "http://"+net.JoinHostPort(host, strconv.Itoa(port)) {
		t.Fatalf("masked proxy address = %q", result.ProxyAddress)
	}
}

func TestProxyPoolConfigTestFetchesAPIProxyBeforeTesting(t *testing.T) {
	proxyServer, proxyCalls := newExitIPProxyServer(t, `{"ip":"1.1.1.1"}`)
	proxyHost, proxyPort := serverHostPort(t, proxyServer)
	var poolCalls atomic.Int64
	poolServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		poolCalls.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"proxy":"` + net.JoinHostPort(proxyHost, strconv.Itoa(proxyPort)) + `"}`))
	}))
	t.Cleanup(poolServer.Close)
	base := httpclient.New(httpclient.Options{AllowPrivateNetworks: true, Timeout: time.Second})

	result, err := testProxyPoolConfig(context.Background(), proxypool.NewResolver(base), base, proxypool.Config{
		APIURL:      poolServer.URL,
		Enabled:     0,
		Protocol:    proxypool.ProtocolHTTP,
		RefreshSecs: 30,
		Source:      proxypool.SourceAPI,
	}, "http://public-exit-check.example/ip")
	if err != nil {
		t.Fatalf("testProxyPoolConfig() error = %v", err)
	}
	if poolCalls.Load() != 1 || proxyCalls.Load() != 1 {
		t.Fatalf("pool calls = %d, proxy calls = %d", poolCalls.Load(), proxyCalls.Load())
	}
	if !result.Success || result.ExitIP != "1.1.1.1" {
		t.Fatalf("proxy test result = %+v", result)
	}
}

func TestProxyPoolConfigTestRejectsNonPublicExitIP(t *testing.T) {
	proxyServer, _ := newExitIPProxyServer(t, `{"ip":"127.0.0.1"}`)
	host, port := serverHostPort(t, proxyServer)
	base := httpclient.New(httpclient.Options{AllowPrivateNetworks: true, Timeout: time.Second})

	_, err := testProxyPoolConfig(context.Background(), proxypool.NewResolver(base), base, proxypool.Config{
		Enabled:  1,
		Host:     host,
		Port:     port,
		Protocol: proxypool.ProtocolHTTP,
		Source:   proxypool.SourceFixed,
	}, "http://public-exit-check.example/ip")
	if err == nil {
		t.Fatal("private exit IP was accepted")
	}
}

func newExitIPProxyServer(t *testing.T, body string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.URL.Host != "public-exit-check.example" {
			t.Errorf("proxy request target = %q", request.URL.String())
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server, &calls
}

func serverHostPort(t *testing.T, server *httptest.Server) (string, int) {
	t.Helper()
	host, portText, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split test server address: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("parse test server port: %v", err)
	}
	return host, port
}
