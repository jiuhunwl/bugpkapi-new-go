package middleware

import (
	"io"
	"net"
	"testing"
	"time"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
)

func TestTrustedProxyClientIPAcceptsIPsAndCIDRs(t *testing.T) {
	resolver, err := TrustedProxyClientIP([]string{"127.0.0.1", "10.0.0.0/8", "::1"})
	if err != nil {
		t.Fatalf("TrustedProxyClientIP() error = %v", err)
	}
	if resolver == nil {
		t.Fatal("TrustedProxyClientIP() returned nil resolver")
	}
}

func TestTrustedProxyClientIPRejectsInvalidEntry(t *testing.T) {
	if _, err := TrustedProxyClientIP([]string{"not-an-ip"}); err == nil {
		t.Fatal("TrustedProxyClientIP accepted an invalid entry")
	}
}

func TestTrustedProxyClientIPRejectsTrustingEntireAddressFamily(t *testing.T) {
	for _, value := range []string{"0.0.0.0/0", "::/0"} {
		t.Run(value, func(t *testing.T) {
			if _, err := TrustedProxyClientIP([]string{value}); err == nil {
				t.Fatalf("TrustedProxyClientIP accepted unsafe CIDR %q", value)
			}
		})
	}
}

func TestTrustedProxyClientIPEmptyListIsSecureDefault(t *testing.T) {
	resolver, err := TrustedProxyClientIP(nil)
	if err != nil || resolver == nil {
		t.Fatalf("empty trusted proxy list = %v, %v", resolver, err)
	}
}

func TestTrustedProxyClientIPIgnoresSpoofedHeadersFromDirectClient(t *testing.T) {
	resolver, err := TrustedProxyClientIP(nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := hertzapp.NewContext(0)
	ctx.SetConn(&testConn{remote: &net.TCPAddr{IP: net.ParseIP("203.0.113.9"), Port: 1234}})
	ctx.SetClientIPFunc(resolver)
	ctx.Request.Header.Set("X-Forwarded-For", "1.1.1.1")
	ctx.Request.Header.Set("X-Real-IP", "2.2.2.2")
	if got := ctx.ClientIP(); got != "203.0.113.9" {
		t.Fatalf("ClientIP() = %q, want TCP peer", got)
	}
}

func TestTrustedProxyClientIPUsesOnlyConfiguredProxyChain(t *testing.T) {
	resolver, err := TrustedProxyClientIP([]string{"127.0.0.1", "10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := hertzapp.NewContext(0)
	ctx.SetConn(&testConn{remote: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1234}})
	ctx.SetClientIPFunc(resolver)
	ctx.Request.Header.Set("X-Forwarded-For", "1.1.1.1, 10.1.2.3")
	if got := ctx.ClientIP(); got != "1.1.1.1" {
		t.Fatalf("ClientIP() = %q, want original client", got)
	}
}

func TestTrustedProxyClientIPStopsAtRightmostUntrustedHop(t *testing.T) {
	resolver, err := TrustedProxyClientIP([]string{"127.0.0.1", "10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := hertzapp.NewContext(0)
	ctx.SetConn(&testConn{remote: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1234}})
	ctx.SetClientIPFunc(resolver)
	ctx.Request.Header.Set("X-Forwarded-For", "6.6.6.6, 198.51.100.40, 10.1.2.3")
	if got := ctx.ClientIP(); got != "198.51.100.40" {
		t.Fatalf("ClientIP() = %q, want rightmost untrusted hop", got)
	}
}

func TestTrustedProxyClientIPFallsBackToRealIPAfterMalformedForwardedChain(t *testing.T) {
	resolver, err := TrustedProxyClientIP([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := hertzapp.NewContext(0)
	ctx.SetConn(&testConn{remote: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1234}})
	ctx.SetClientIPFunc(resolver)
	ctx.Request.Header.Set("X-Forwarded-For", "198.51.100.10, invalid")
	ctx.Request.Header.Set("X-Real-IP", "203.0.113.20")
	if got := ctx.ClientIP(); got != "203.0.113.20" {
		t.Fatalf("ClientIP() = %q, want validated X-Real-IP fallback", got)
	}
}

type testConn struct {
	remote net.Addr
}

func (conn *testConn) Read([]byte) (int, error)             { return 0, io.EOF }
func (conn *testConn) Write(data []byte) (int, error)       { return len(data), nil }
func (conn *testConn) Close() error                         { return nil }
func (conn *testConn) LocalAddr() net.Addr                  { return &net.TCPAddr{} }
func (conn *testConn) RemoteAddr() net.Addr                 { return conn.remote }
func (conn *testConn) SetDeadline(time.Time) error          { return nil }
func (conn *testConn) SetReadDeadline(time.Time) error      { return nil }
func (conn *testConn) SetWriteDeadline(time.Time) error     { return nil }
func (conn *testConn) Peek(int) ([]byte, error)             { return nil, io.EOF }
func (conn *testConn) Skip(int) error                       { return io.EOF }
func (conn *testConn) Release() error                       { return nil }
func (conn *testConn) Len() int                             { return 0 }
func (conn *testConn) ReadByte() (byte, error)              { return 0, io.EOF }
func (conn *testConn) ReadBinary(int) ([]byte, error)       { return nil, io.EOF }
func (conn *testConn) Malloc(size int) ([]byte, error)      { return make([]byte, size), nil }
func (conn *testConn) WriteBinary(data []byte) (int, error) { return len(data), nil }
func (conn *testConn) Flush() error                         { return nil }
func (conn *testConn) SetReadTimeout(time.Duration) error   { return nil }
func (conn *testConn) SetWriteTimeout(time.Duration) error  { return nil }
