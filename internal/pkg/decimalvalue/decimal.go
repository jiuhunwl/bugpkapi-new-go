package decimalvalue

import (
	"errors"
	"math/big"
	"regexp"
	"strconv"
	"strings"
)

const (
	MaxExponentAbs = int64(1000)
	MaxTextLength  = 64
)

var (
	ErrExponentRange = errors.New("decimal exponent is out of range")
	ErrFormat        = errors.New("invalid decimal format")
	ErrNegative      = errors.New("decimal must not be negative")
	ErrTextTooLong   = errors.New("decimal text is too long")
)

var decimalPattern = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)?(?:[eE]([+-]?[0-9]+))?$`)
var signedDecimalPattern = regexp.MustCompile(`^-?[0-9]+(?:\.[0-9]+)?(?:[eE]([+-]?[0-9]+))?$`)

func Parse(text string) (*big.Rat, error) {
	return parse(text, signedDecimalPattern)
}

// ParseNonNegative accepts canonical decimal or scientific notation only.
// It validates the exponent before big.Rat parsing to bound allocation work.
func ParseNonNegative(text string) (*big.Rat, error) {
	if strings.HasPrefix(strings.TrimSpace(text), "-") {
		return nil, ErrNegative
	}
	return parse(text, decimalPattern)
}

func parse(text string, pattern *regexp.Regexp) (*big.Rat, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, ErrFormat
	}
	if len(text) > MaxTextLength {
		return nil, ErrTextTooLong
	}
	matches := pattern.FindStringSubmatch(text)
	if matches == nil {
		return nil, ErrFormat
	}
	if exponentText := matches[1]; exponentText != "" {
		exponent, err := strconv.ParseInt(exponentText, 10, 64)
		if err != nil || exponent > MaxExponentAbs || exponent < -MaxExponentAbs {
			return nil, ErrExponentRange
		}
	}

	value, ok := new(big.Rat).SetString(text)
	if !ok {
		return nil, ErrFormat
	}
	return value, nil
}
