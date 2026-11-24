package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"strings"
)

// DeviceCookieName is a signed, HttpOnly browser identifier used only to count
// how many accounts one browser creates in 24h. It is not a fingerprint and is
// never exposed to JavaScript or other services.
const DeviceCookieName = "bp_dcid"

const (
	deviceCookieMaxAgeSeconds = 31536000
	deviceIDBytes             = 16
	deviceCookieParts         = 2
)

// signDeviceID returns "base64url(id).base64url(hmac)".
func signDeviceID(secret, id string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("bugpk-device-v1\x00" + id))
	return base64.RawURLEncoding.EncodeToString([]byte(id)) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// verifyDeviceID returns the embedded id only when the signature is valid.
func verifyDeviceID(secret, value string) string {
	parts := strings.Split(strings.TrimSpace(value), ".")
	if len(parts) != deviceCookieParts {
		return ""
	}
	rawID, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(rawID) != deviceIDBytes {
		return ""
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("bugpk-device-v1\x00" + string(rawID)))
	if subtle.ConstantTimeCompare(signature, mac.Sum(nil)) != 1 {
		return ""
	}
	return string(rawID)
}

func newDeviceID(secret string) (string, string, error) {
	raw := make([]byte, deviceIDBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	id := string(raw)
	return signDeviceID(secret, id), id, nil
}

// deviceFingerprint is the value persisted in bp_register_event. It is a plain
// SHA-256 of the random id: the id itself carries no personal data, and the
// digest keeps rows from being linkable to a cookie by anyone holding the DB.
func deviceFingerprint(id string) []byte {
	if strings.TrimSpace(id) == "" {
		return make([]byte, sha256.Size)
	}
	sum := sha256.Sum256([]byte("bugpk-device-fp-v1\x00" + id))
	return sum[:]
}

func deviceFingerprintHex(id string) string {
	return hex.EncodeToString(deviceFingerprint(id))
}
