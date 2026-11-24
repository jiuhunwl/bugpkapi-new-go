package httpclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"bugpk-api/internal/pkg/jsonrequest"
)

const (
	MaxPaymentResponseBytes  = int64(1 << 20)
	MaxProviderResponseBytes = int64(256 << 10)
	MaxUpstreamResponseBytes = int64(2 << 20)
)

var (
	ErrBlockedAddress      = errors.New("outbound address is not allowed")
	ErrCrossOriginRedirect = errors.New("cross-origin redirect is not allowed")
	ErrResponseTooLarge    = errors.New("response body exceeds configured limit")
)

type LookupIPFunc func(context.Context, string) ([]net.IP, error)

type Options struct {
	AllowPrivateNetworks   bool
	DialContext            func(context.Context, string, string) (net.Conn, error)
	ExpectContinueTimeout  time.Duration
	IdleConnTimeout        time.Duration
	LookupIP               LookupIPFunc
	MaxConnsPerHost        int
	MaxIdleConns           int
	MaxIdleConnsPerHost    int
	MaxResponseHeaderBytes int64
	MaxRedirects           int
	ResponseHeaderTimeout  time.Duration
	Timeout                time.Duration
	TLSHandshakeTimeout    time.Duration
}

func New(options Options) *http.Client {
	options = withDefaults(options)
	lookupIP := options.LookupIP
	if lookupIP == nil {
		lookupIP = func(ctx context.Context, host string) ([]net.IP, error) {
			return net.DefaultResolver.LookupIP(ctx, "ip", host)
		}
	}
	dialContext := options.DialContext
	if dialContext == nil {
		dialContext = (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	}
	secureDial := func(ctx context.Context, network, address string) (net.Conn, error) {
		resolvedAddress, err := resolveDialAddress(ctx, address, options.AllowPrivateNetworks, lookupIP)
		if err != nil {
			return nil, err
		}
		return dialContext(ctx, network, resolvedAddress)
	}
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            secureDial,
		ForceAttemptHTTP2:      true,
		MaxIdleConns:           options.MaxIdleConns,
		MaxIdleConnsPerHost:    options.MaxIdleConnsPerHost,
		MaxConnsPerHost:        options.MaxConnsPerHost,
		MaxResponseHeaderBytes: options.MaxResponseHeaderBytes,
		IdleConnTimeout:        options.IdleConnTimeout,
		TLSHandshakeTimeout:    options.TLSHandshakeTimeout,
		ExpectContinueTimeout:  options.ExpectContinueTimeout,
		ResponseHeaderTimeout:  options.ResponseHeaderTimeout,
	}
	return &http.Client{
		Timeout: options.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > options.MaxRedirects {
				return fmt.Errorf("stopped after %d redirects", options.MaxRedirects)
			}
			if err := ValidateURL(req.URL.String()); err != nil {
				return fmt.Errorf("unsafe redirect target: %w", err)
			}
			if len(via) > 0 && !sameOrigin(via[len(via)-1].URL, req.URL) {
				return fmt.Errorf(
					"%w: %s -> %s",
					ErrCrossOriginRedirect,
					urlOrigin(via[len(via)-1].URL),
					urlOrigin(req.URL),
				)
			}
			return nil
		},
		Transport: secureTransport{base: transport},
	}
}

type secureTransport struct {
	base *http.Transport
}

func (transport secureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil {
		return nil, errors.New("outbound request URL is required")
	}
	if err := ValidateURL(request.URL.String()); err != nil {
		return nil, err
	}
	return transport.base.RoundTrip(request)
}

func (transport secureTransport) CloseIdleConnections() {
	transport.base.CloseIdleConnections()
}

func Outbound(timeout time.Duration) *http.Client {
	return New(Options{Timeout: timeout})
}

func ValidateURL(raw string) error {
	parsed, err := url.ParseRequestURI(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("invalid outbound URL: %w", err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("invalid outbound URL scheme %q", parsed.Scheme)
	}
	if parsed.Hostname() == "" {
		return errors.New("outbound URL host is required")
	}
	if parsed.User != nil {
		return errors.New("outbound URL user info is not allowed")
	}
	if port := parsed.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value <= 0 || value > 65535 {
			return errors.New("outbound URL port is invalid")
		}
	}
	return nil
}

// IsPublicIP reports whether an address is safe for a public-only outbound
// destination or response contract.
func IsPublicIP(ip net.IP) bool {
	return !isBlockedIP(ip, false)
}

func ReadBody(body io.Reader, maxBytes int64) ([]byte, error) {
	if body == nil {
		return nil, errors.New("response body is nil")
	}
	if maxBytes <= 0 {
		return nil, errors.New("response body limit must be positive")
	}
	data, err := io.ReadAll(io.LimitReader(body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%w: limit=%d", ErrResponseTooLarge, maxBytes)
	}
	return data, nil
}

func sameOrigin(left, right *url.URL) bool {
	if left == nil || right == nil {
		return false
	}
	return strings.EqualFold(left.Scheme, right.Scheme) &&
		strings.EqualFold(left.Hostname(), right.Hostname()) &&
		effectivePort(left) == effectivePort(right)
}

func effectivePort(value *url.URL) string {
	if value == nil {
		return ""
	}
	if port := value.Port(); port != "" {
		return port
	}
	switch strings.ToLower(value.Scheme) {
	case "http":
		return "80"
	case "https":
		return "443"
	default:
		return ""
	}
}

func urlOrigin(value *url.URL) string {
	if value == nil {
		return "<nil>"
	}
	return strings.ToLower(value.Scheme) + "://" + net.JoinHostPort(
		strings.ToLower(value.Hostname()),
		effectivePort(value),
	)
}

func DecodeJSON(body io.Reader, maxBytes int64, target any) error {
	data, err := ReadBody(body, maxBytes)
	if err != nil {
		return err
	}
	if err := jsonrequest.UnmarshalUnambiguous(data, target, jsonrequest.DefaultMaxDepth); err != nil {
		return fmt.Errorf("decode JSON response: %w", err)
	}
	return nil
}

func resolveDialAddress(ctx context.Context, address string, allowPrivate bool, lookupIP LookupIPFunc) (string, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", fmt.Errorf("invalid outbound address %q: %w", address, err)
	}
	if ip := net.ParseIP(strings.TrimSpace(host)); ip != nil {
		if isBlockedIP(ip, allowPrivate) {
			return "", fmt.Errorf("%w: %s", ErrBlockedAddress, ip.String())
		}
		return net.JoinHostPort(ip.String(), port), nil
	}
	addresses, err := lookupIP(ctx, strings.TrimSpace(host))
	if err != nil {
		return "", fmt.Errorf("resolve outbound host %q: %w", host, err)
	}
	if len(addresses) == 0 {
		return "", fmt.Errorf("resolve outbound host %q: no addresses", host)
	}
	for _, ip := range addresses {
		if ip == nil || isBlockedIP(ip, allowPrivate) {
			return "", fmt.Errorf("%w: host=%s address=%v", ErrBlockedAddress, host, ip)
		}
	}
	return net.JoinHostPort(addresses[0].String(), port), nil
}

func isBlockedIP(ip net.IP, allowPrivate bool) bool {
	if ip == nil {
		return true
	}
	if ipv4 := ip.To4(); ipv4 != nil {
		ip = ipv4
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}
	if nat64WellKnownNetwork != nil && nat64WellKnownNetwork.Contains(ip) {
		ipv6 := ip.To16()
		if ipv6 == nil {
			return true
		}
		embedded := net.IPv4(ipv6[12], ipv6[13], ipv6[14], ipv6[15])
		return isBlockedIP(embedded, false)
	}
	for _, network := range alwaysBlockedNetworks {
		if network.Contains(ip) {
			return true
		}
	}
	if ip.IsPrivate() || ip.IsLoopback() || containsIP(privateNetworks, ip) {
		return !allowPrivate
	}
	if !ip.IsGlobalUnicast() {
		return true
	}
	return false
}

var alwaysBlockedNetworks = mustNetworks(
	"0.0.0.0/8",
	"192.0.0.0/24",
	"192.0.2.0/24",
	"192.88.99.0/24",
	"198.18.0.0/15",
	"198.51.100.0/24",
	"203.0.113.0/24",
	"240.0.0.0/4",
	"64:ff9b:1::/48",
	"100::/64",
	"2001::/32",
	"2001:2::/48",
	"2001:10::/28",
	"2001:20::/28",
	"2001:db8::/32",
	"2002::/16",
	"fec0::/10",
)

var privateNetworks = mustNetworks("100.64.0.0/10")

var nat64WellKnownNetwork = parseNetwork("64:ff9b::/96")

func containsIP(networks []*net.IPNet, ip net.IP) bool {
	for _, network := range networks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func mustNetworks(values ...string) []*net.IPNet {
	result := make([]*net.IPNet, 0, len(values))
	for _, value := range values {
		_, network, err := net.ParseCIDR(value)
		if err != nil {
			continue
		}
		result = append(result, network)
	}
	return result
}

func parseNetwork(value string) *net.IPNet {
	_, network, _ := net.ParseCIDR(value)
	return network
}

func withDefaults(options Options) Options {
	if options.Timeout <= 0 {
		options.Timeout = 15 * time.Second
	}
	if options.MaxIdleConns <= 0 {
		options.MaxIdleConns = 4096
	}
	if options.MaxIdleConnsPerHost <= 0 {
		options.MaxIdleConnsPerHost = 512
	}
	if options.MaxConnsPerHost <= 0 {
		options.MaxConnsPerHost = 1024
	}
	if options.MaxResponseHeaderBytes <= 0 {
		options.MaxResponseHeaderBytes = 64 << 10
	}
	if options.IdleConnTimeout <= 0 {
		options.IdleConnTimeout = 90 * time.Second
	}
	if options.TLSHandshakeTimeout <= 0 {
		options.TLSHandshakeTimeout = 5 * time.Second
	}
	if options.ExpectContinueTimeout <= 0 {
		options.ExpectContinueTimeout = time.Second
	}
	if options.ResponseHeaderTimeout <= 0 {
		options.ResponseHeaderTimeout = minDuration(10*time.Second, options.Timeout)
	}
	if options.MaxRedirects <= 0 {
		options.MaxRedirects = 3
	}
	return options
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
