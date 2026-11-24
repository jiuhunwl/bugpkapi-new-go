package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

func secretRedisKey(prefix, secret string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(secret)))
	return prefix + hex.EncodeToString(sum[:])
}
