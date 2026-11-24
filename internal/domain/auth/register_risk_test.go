package auth

import (
	"strings"
	"testing"
)

func TestDeviceCookieSignatureRoundTrip(t *testing.T) {
	value, id, err := newDeviceID("test-secret")
	if err != nil {
		t.Fatal(err)
	}
	if got := verifyDeviceID("test-secret", value); got != id {
		t.Fatalf("verified id = %q, want %q", got, id)
	}
	if got := verifyDeviceID("other-secret", value); got != "" {
		t.Fatalf("forged secret verified as %q", got)
	}
	tampered := "A" + value[1:]
	if value[0] == 'A' {
		tampered = "B" + value[1:]
	}
	if got := verifyDeviceID("test-secret", tampered); got != "" {
		t.Fatalf("tampered id verified as %q", got)
	}
}

func TestDeviceFingerprintIsStableAndNotTheRawID(t *testing.T) {
	a := deviceFingerprint("device-a")
	b := deviceFingerprint("device-a")
	c := deviceFingerprint("device-b")
	if len(a) != 32 {
		t.Fatalf("fingerprint length = %d", len(a))
	}
	if string(a) != string(b) {
		t.Fatal("same device id must produce the same fingerprint")
	}
	if string(a) == string(c) {
		t.Fatal("different device ids must not collide")
	}
	if strings.Contains(string(a), "device-a") {
		t.Fatal("fingerprint must not contain the raw device id")
	}
}

func TestLooksLikeRandomUsername(t *testing.T) {
	for _, test := range []struct {
		username string
		email    string
		want     bool
	}{
		{username: "bqjlu3hb", email: "bqjlu3hb@daodao.app", want: true},
		{username: "alice2024", email: "alice2024@qq.com", want: false},
		{username: "bqjlu3hb", email: "other@daodao.app", want: false},
		{username: "ABC12345", email: "abc12345@example.com", want: true},
	} {
		if got := looksLikeRandomUsername(test.username, test.email); got != test.want {
			t.Fatalf("looksLikeRandomUsername(%q,%q) = %v, want %v", test.username, test.email, got, test.want)
		}
	}
}

func TestCommonEmailDomainsDoNotGetNewDomainSignal(t *testing.T) {
	for _, domain := range []string{"qq.com", "gmail.com", "outlook.com", "school.edu.cn", "mit.edu"} {
		if !isCommonEmailDomain(domain) {
			t.Fatalf("%q should be treated as a common/trusted domain", domain)
		}
	}
	for _, domain := range []string{"daodao.app", "textdiff.net", "random-example.xyz"} {
		if isCommonEmailDomain(domain) {
			t.Fatalf("%q should not be treated as a common domain", domain)
		}
	}
}
