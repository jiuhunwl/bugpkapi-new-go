package httpclient

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOutboundUsesPooledTransport(t *testing.T) {
	client := Outbound(3 * time.Second)
	if client.Timeout != 3*time.Second {
		t.Fatalf("Timeout = %s, want 3s", client.Timeout)
	}
	secure, ok := client.Transport.(secureTransport)
	if !ok {
		t.Fatalf("Transport = %T, want secureTransport", client.Transport)
	}
	transport := secure.base
	if transport.MaxIdleConns < 4096 {
		t.Fatalf("MaxIdleConns = %d, want at least 4096", transport.MaxIdleConns)
	}
	if transport.MaxIdleConnsPerHost < 512 {
		t.Fatalf("MaxIdleConnsPerHost = %d, want at least 512", transport.MaxIdleConnsPerHost)
	}
	if transport.MaxConnsPerHost < 1024 {
		t.Fatalf("MaxConnsPerHost = %d, want at least 1024", transport.MaxConnsPerHost)
	}
	if transport.MaxResponseHeaderBytes != 64<<10 {
		t.Fatalf("MaxResponseHeaderBytes = %d, want %d", transport.MaxResponseHeaderBytes, 64<<10)
	}
	if transport.ResponseHeaderTimeout <= 0 || transport.IdleConnTimeout <= 0 {
		t.Fatalf("expected transport timeouts to be set: %#v", transport)
	}
	if transport.Proxy != nil {
		t.Fatal("outbound transport must not use environment proxies")
	}
}

func TestOutboundRejectsOversizedResponseHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("X-Oversized", strings.Repeat("x", 96<<10))
		_, _ = writer.Write([]byte("unreachable"))
	}))
	defer server.Close()
	client := New(Options{
		AllowPrivateNetworks:   true,
		MaxResponseHeaderBytes: 8 << 10,
		Timeout:                time.Second,
	})
	response, err := client.Get(server.URL)
	if response != nil {
		_ = response.Body.Close()
	}
	if err == nil {
		t.Fatal("oversized response headers were accepted")
	}
}

func TestOutboundBlocksPrivateLiteralAddresses(t *testing.T) {
	client := Outbound(time.Second)
	for _, target := range []string{
		"http://127.0.0.1:8080",
		"http://169.254.169.254/latest/meta-data",
		"http://10.0.0.1",
		"http://[::1]",
		"http://[::ffff:127.0.0.1]",
	} {
		_, err := client.Get(target)
		if !errors.Is(err, ErrBlockedAddress) {
			t.Fatalf("Get(%q) error = %v, want ErrBlockedAddress", target, err)
		}
	}
}

func TestNewCanExplicitlyAllowPrivateUpstreams(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("ok"))
	}))
	defer server.Close()
	client := New(Options{AllowPrivateNetworks: true, Timeout: time.Second})
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("private upstream request failed: %v", err)
	}
	defer response.Body.Close()
	body, err := ReadBody(response.Body, 16)
	if err != nil || string(body) != "ok" {
		t.Fatalf("response = %q, err = %v", body, err)
	}
}

func TestPrivateUpstreamModeStillBlocksMetadataAndLinkLocal(t *testing.T) {
	client := New(Options{AllowPrivateNetworks: true, Timeout: time.Second})
	for _, target := range []string{
		"http://169.254.169.254/latest/meta-data",
		"http://[fe80::1]",
		"http://0.0.0.0",
	} {
		_, err := client.Get(target)
		if !errors.Is(err, ErrBlockedAddress) {
			t.Fatalf("Get(%q) error = %v, want ErrBlockedAddress", target, err)
		}
	}
}

func TestOutboundBlocksSpecialUseAndTransitionAddresses(t *testing.T) {
	for _, raw := range []string{
		"0.0.0.1",
		"192.88.99.1",
		"64:ff9b::a00:1",
		"64:ff9b:1::a00:1",
		"100::1",
		"2001::1",
		"2002:0a00:0001::1",
		"fec0::1",
	} {
		ip := net.ParseIP(raw)
		if ip == nil {
			t.Fatalf("invalid test IP %q", raw)
		}
		if !isBlockedIP(ip, false) {
			t.Fatalf("special-use address %s was allowed", raw)
		}
		if !isBlockedIP(ip, true) {
			t.Fatalf("special-use address %s was allowed in private-upstream mode", raw)
		}
	}
}

func TestNAT64PublicMappingRemainsAllowed(t *testing.T) {
	if ip := net.ParseIP("64:ff9b::808:808"); ip == nil || isBlockedIP(ip, false) {
		t.Fatalf("public NAT64 mapping was blocked: %v", ip)
	}
}

func TestOutboundBlocksMixedDNSAnswersBeforeDial(t *testing.T) {
	dialed := false
	client := New(Options{
		LookupIP: func(context.Context, string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("8.8.8.8"), net.ParseIP("127.0.0.1")}, nil
		},
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			dialed = true
			return nil, errors.New("unexpected dial")
		},
		Timeout: time.Second,
	})
	_, err := client.Get("http://mixed.example:8080")
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("mixed DNS error = %v, want ErrBlockedAddress", err)
	}
	if dialed {
		t.Fatal("dial must not run after a mixed public/private DNS answer")
	}
}

func TestOutboundBlocksRedirectToPrivateAddress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, "http://127.0.0.1:8080/private", http.StatusFound)
	}))
	defer server.Close()
	serverAddress := server.Listener.Addr().String()
	client := New(Options{
		LookupIP: func(context.Context, string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("8.8.8.8")}, nil
		},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, serverAddress)
		},
		Timeout: time.Second,
	})
	_, port, err := net.SplitHostPort(serverAddress)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Get("http://public.example:" + port)
	if !errors.Is(err, ErrBlockedAddress) && !errors.Is(err, ErrCrossOriginRedirect) {
		t.Fatalf("redirect error = %v, want blocked-address or cross-origin rejection", err)
	}
}

func TestOutboundRejectsCrossOriginRedirect(t *testing.T) {
	redirectReached := false
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.Host, "redirect.example:") {
			redirectReached = true
			_, _ = writer.Write([]byte("unexpected"))
			return
		}
		_, port, err := net.SplitHostPort(request.Host)
		if err != nil {
			t.Fatalf("split request host: %v", err)
		}
		http.Redirect(writer, request, "http://redirect.example:"+port+"/target", http.StatusFound)
	}))
	defer server.Close()

	serverAddress := server.Listener.Addr().String()
	_, port, err := net.SplitHostPort(serverAddress)
	if err != nil {
		t.Fatal(err)
	}
	client := New(Options{
		LookupIP: func(context.Context, string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("8.8.8.8")}, nil
		},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, serverAddress)
		},
		Timeout: time.Second,
	})
	_, err = client.Get("http://origin.example:" + port + "/start")
	if !errors.Is(err, ErrCrossOriginRedirect) {
		t.Fatalf("cross-origin redirect error = %v, want ErrCrossOriginRedirect", err)
	}
	if redirectReached {
		t.Fatal("cross-origin redirect target must not be requested")
	}
}

func TestOutboundAllowsSameOriginRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/start" {
			http.Redirect(writer, request, "/final", http.StatusFound)
			return
		}
		_, _ = writer.Write([]byte("ok"))
	}))
	defer server.Close()

	client := New(Options{AllowPrivateNetworks: true, Timeout: time.Second})
	response, err := client.Get(server.URL + "/start")
	if err != nil {
		t.Fatalf("same-origin redirect failed: %v", err)
	}
	defer response.Body.Close()
	body, err := ReadBody(response.Body, 16)
	if err != nil || string(body) != "ok" {
		t.Fatalf("same-origin redirect response = %q, err = %v", body, err)
	}
}

func TestOutboundHonorsExactRedirectLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/allowed/0":
			http.Redirect(writer, request, "/allowed/1", http.StatusFound)
		case "/allowed/1":
			http.Redirect(writer, request, "/allowed/2", http.StatusFound)
		case "/allowed/2":
			http.Redirect(writer, request, "/allowed/3", http.StatusFound)
		case "/allowed/3":
			_, _ = writer.Write([]byte("ok"))
		case "/blocked/0":
			http.Redirect(writer, request, "/blocked/1", http.StatusFound)
		case "/blocked/1":
			http.Redirect(writer, request, "/blocked/2", http.StatusFound)
		case "/blocked/2":
			http.Redirect(writer, request, "/blocked/3", http.StatusFound)
		case "/blocked/3":
			http.Redirect(writer, request, "/blocked/4", http.StatusFound)
		default:
			_, _ = writer.Write([]byte("unexpected"))
		}
	}))
	defer server.Close()

	client := New(Options{AllowPrivateNetworks: true, MaxRedirects: 3, Timeout: time.Second})
	response, err := client.Get(server.URL + "/allowed/0")
	if err != nil {
		t.Fatalf("exactly three redirects failed: %v", err)
	}
	body, readErr := ReadBody(response.Body, 16)
	_ = response.Body.Close()
	if readErr != nil || string(body) != "ok" {
		t.Fatalf("three-redirect response = %q, %v", body, readErr)
	}

	response, err = client.Get(server.URL + "/blocked/0")
	if response != nil {
		_ = response.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "stopped after 3 redirects") {
		t.Fatalf("fourth redirect error = %v", err)
	}
}

func TestValidateURLRejectsUnsafeForms(t *testing.T) {
	for _, target := range []string{
		"file:///etc/passwd",
		"http://",
		"http://user:pass@example.com",
		"http://example.com:70000",
	} {
		if err := ValidateURL(target); err == nil {
			t.Fatalf("ValidateURL(%q) accepted an unsafe URL", target)
		}
	}
	if err := ValidateURL("https://example.com/path?q=1"); err != nil {
		t.Fatalf("ValidateURL rejected a public URL: %v", err)
	}
}

func TestReadBodyRejectsOversizedResponse(t *testing.T) {
	data, err := ReadBody(strings.NewReader("1234"), 4)
	if err != nil || string(data) != "1234" {
		t.Fatalf("exact-limit body = %q, err = %v", data, err)
	}
	_, err = ReadBody(strings.NewReader("12345"), 4)
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("oversized body error = %v, want ErrResponseTooLarge", err)
	}
	reader := errorReader{}
	if _, err := ReadBody(reader, 4); err == nil {
		t.Fatal("ReadBody ignored a reader error")
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}
