package emailaddr

import (
	"net/mail"
	"strings"
	"unicode"
)

const maxAddressBytes = 254

func Valid(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > maxAddressBytes {
		return false
	}
	if strings.IndexFunc(value, func(r rune) bool {
		return unicode.IsControl(r) || unicode.IsSpace(r)
	}) >= 0 {
		return false
	}
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Name != "" {
		return false
	}
	return strings.EqualFold(parsed.Address, value)
}
