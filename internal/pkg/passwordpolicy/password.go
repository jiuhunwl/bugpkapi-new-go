package passwordpolicy

import "unicode/utf8"

const (
	MinCharacters = 8
	MaxCharacters = 64
	MaxBytes      = 72
)

func Valid(value string) bool {
	characters := utf8.RuneCountInString(value)
	return characters >= MinCharacters && characters <= MaxCharacters && len([]byte(value)) <= MaxBytes
}

func WithinMaximum(value string) bool {
	return utf8.RuneCountInString(value) <= MaxCharacters && len([]byte(value)) <= MaxBytes
}
