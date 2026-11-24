package emailaddr

import (
	"strings"
	"testing"
)

func TestValidAcceptsPlainMailboxAddresses(t *testing.T) {
	for _, value := range []string{
		"user@example.com",
		"USER+alerts@Example.COM",
		"first.last@example.co.uk",
	} {
		if !Valid(value) {
			t.Fatalf("Valid(%q) = false", value)
		}
	}
}

func TestValidRejectsHeaderAndEnvelopeAmbiguity(t *testing.T) {
	for _, value := range []string{
		"",
		" user@example.com",
		"user@example.com ",
		"User <user@example.com>",
		"user(comment)@example.com",
		"user@example.com\r\nBcc: attacker@example.com",
		"user @example.com",
		"user@example.com\x00",
	} {
		if Valid(value) {
			t.Fatalf("Valid(%q) = true", value)
		}
	}
}

func TestValidRejectsOversizedAddress(t *testing.T) {
	value := strings.Repeat("a", maxAddressBytes) + "@example.com"
	if Valid(value) {
		t.Fatal("Valid accepted an oversized address")
	}
}
