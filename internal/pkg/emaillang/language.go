// Package emaillang centralizes the language decision used by every email
// producer. The application currently has Chinese and English templates, so
// regional Chinese variants intentionally resolve to "zh".
package emaillang

import (
	"math"
	"strconv"
	"strings"
)

const (
	Chinese = "zh"
	English = "en"
)

// Normalize returns the supported template language represented by value.
// It accepts either a locale tag or an Accept-Language style preference list.
// Unsupported, disabled (q=0), and malformed values return an empty string so
// callers can continue down the language priority chain.
func Normalize(value string) string {
	type preference struct {
		language string
		q        float64
		order    int
	}

	preferences := make([]preference, 0, 4)
	for order, rawPart := range strings.Split(value, ",") {
		part := strings.TrimSpace(rawPart)
		if part == "" {
			continue
		}

		tag := part
		q := 1.0
		if separator := strings.IndexByte(part, ';'); separator >= 0 {
			tag = strings.TrimSpace(part[:separator])
			var valid bool
			q, valid = acceptLanguageQuality(part[separator+1:])
			if !valid {
				continue
			}
		}
		if q <= 0 {
			continue
		}

		tag = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(tag), "_", "-"))
		primary := tag
		if separator := strings.IndexByte(primary, '-'); separator >= 0 {
			primary = primary[:separator]
		}
		if primary != Chinese && primary != English {
			continue
		}
		preferences = append(preferences, preference{
			language: primary,
			q:        q,
			order:    order,
		})
	}

	if len(preferences) == 0 {
		return ""
	}
	best := preferences[0]
	for _, candidate := range preferences[1:] {
		if candidate.q > best.q || (candidate.q == best.q && candidate.order < best.order) {
			best = candidate
		}
	}
	return best.language
}

// FirstSupported returns the first recognizable preference. It is useful when
// more than one transport represents the same priority tier, for example an
// explicit language field followed by X-App-Locale.
func FirstSupported(values ...string) string {
	for _, value := range values {
		if language := Normalize(value); language != "" {
			return language
		}
	}
	return ""
}

// Resolve applies the product-wide email language priority:
//
//  1. explicit page language snapshot;
//  2. current user or target record language;
//  3. browser Accept-Language;
//  4. site language;
//  5. safe Chinese default.
//
// Invalid values never mask a valid lower-priority preference.
func Resolve(pageLanguage, targetLanguage, acceptLanguage, siteLanguage string) string {
	if language := FirstSupported(pageLanguage); language != "" {
		return language
	}
	if language := FirstSupported(targetLanguage); language != "" {
		return language
	}
	if language := Normalize(acceptLanguage); language != "" {
		return language
	}
	if language := FirstSupported(siteLanguage); language != "" {
		return language
	}
	return Chinese
}

func acceptLanguageQuality(raw string) (float64, bool) {
	quality := 1.0
	found := false
	for _, parameter := range strings.Split(raw, ";") {
		parameter = strings.TrimSpace(parameter)
		key, value, ok := strings.Cut(parameter, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "q") {
			continue
		}
		if found {
			return 0, false
		}
		found = true
		parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < 0 || parsed > 1 {
			return 0, false
		}
		quality = parsed
	}
	return quality, true
}
