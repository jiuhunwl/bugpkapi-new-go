// Package auditprincipal carries verified attribution through one HTTP request.
// It never authenticates, authorizes, or supplies credentials to business code.
package auditprincipal

import (
	"context"
	"crypto/sha256"
	"slices"
	"strings"
	"sync"
)

type Identity struct {
	UserID   int64
	Username string
	Roles    []string
}
type contextKey struct{}
type capture struct {
	mu        sync.Mutex
	tokenHash [32]byte
	bound     bool
	identity  *Identity
}

// WithRequest always starts an empty capture, even when a parent carries one.
func WithRequest(ctx context.Context, token string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	token = strings.TrimSpace(token)
	return context.WithValue(ctx, contextKey{}, &capture{tokenHash: sha256.Sum256([]byte(token)), bound: token != ""})
}

// Record is called only AFTER an authentication implementation has verified the
// JWT and current account. It accepts only the incoming request's credential and
// retains the first verified identity, before subsequent business mutations.
func Record(ctx context.Context, token string, identity Identity) {
	if ctx == nil || identity.UserID <= 0 {
		return
	}
	state, ok := ctx.Value(contextKey{}).(*capture)
	if !ok || state == nil || !state.bound || state.tokenHash != sha256.Sum256([]byte(strings.TrimSpace(token))) {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.identity == nil {
		identity.Roles = slices.Clone(identity.Roles)
		state.identity = &identity
	}
}

// FromContext is for audit finalization only. Cancellation or later account
// changes must not erase attribution of a completed operation. The result is
// deliberately not an authenticated session and must never grant permissions.
func FromContext(ctx context.Context) (Identity, bool) {
	if ctx == nil {
		return Identity{}, false
	}
	state, ok := ctx.Value(contextKey{}).(*capture)
	if !ok || state == nil {
		return Identity{}, false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.identity == nil {
		return Identity{}, false
	}
	identity := *state.identity
	identity.Roles = slices.Clone(identity.Roles)
	return identity, true
}
