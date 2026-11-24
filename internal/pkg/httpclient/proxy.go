package httpclient

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// WithProxyURL clones a managed outbound client and routes it through one
// explicit proxy. Environment proxy variables remain ignored.
func WithProxyURL(base *http.Client, rawProxyURL string) (*http.Client, error) {
	if base == nil {
		return nil, errors.New("base HTTP client is required")
	}
	parsed, err := url.Parse(strings.TrimSpace(rawProxyURL))
	if err != nil || parsed.Hostname() == "" || parsed.Port() == "" {
		return nil, errors.New("proxy URL must include scheme, host, and port")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "socks5":
	default:
		return nil, errors.New("proxy URL scheme must be http, https, or socks5")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 {
		return nil, errors.New("proxy URL port is invalid")
	}
	var managed secureTransport
	switch transport := base.Transport.(type) {
	case secureTransport:
		managed = transport
	case *secureTransport:
		if transport == nil {
			return nil, errors.New("base HTTP transport is nil")
		}
		managed = *transport
	default:
		return nil, errors.New("base HTTP client is not a managed outbound client")
	}
	clonedTransport := managed.base.Clone()
	clonedTransport.Proxy = http.ProxyURL(parsed)
	return &http.Client{
		CheckRedirect: base.CheckRedirect,
		Jar:           base.Jar,
		Timeout:       base.Timeout,
		Transport:     secureTransport{base: clonedTransport},
	}, nil
}
