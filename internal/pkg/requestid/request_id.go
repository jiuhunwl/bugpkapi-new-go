package requestid

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime"
	"strings"
	"time"

	"bugpk-api/internal/pkg/jsonappend"
)

const Header = "X-Request-ID"

type requestIDContextKey struct{}

// Only server-owned context values can be reused, never caller-provided headers.
func Ensure(ctx context.Context) context.Context {
	if FromContext(ctx) != "" {
		return ctx
	}
	return context.WithValue(ctx, requestIDContextKey{}, New())
}

func FromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDContextKey{}).(string)
	return id
}

// Decorate at the HTTP boundary, after caching and billing decisions. Raw JSON
// values preserve number precision and nested field order. Arrays and non-JSON
// responses retain their original contract and expose the ID via the header.
func Decorate(body []byte, contentType, id string) []byte {
	mediaType, _, _ := mime.ParseMediaType(contentType)
	mediaType = strings.ToLower(mediaType)
	if id == "" || (mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json")) || !json.Valid(body) {
		return body
	}
	if trimmed := bytes.TrimSpace(body); len(trimmed) == 0 || trimmed[0] != '{' {
		return body
	}
	return jsonappend.AppendTailFields(body, jsonappend.Field{Key: "requestId", Value: id})
}

func New() string {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return fmt.Sprintf("gw_%d", time.Now().UnixNano())
	}
	return "gw_" + hex.EncodeToString(id[:])
}
