package rbac

import (
	"context"
	"crypto/sha256"
	"slices"
	"sync"
)

type principalContextKeyType struct{}

var principalContextKey = principalContextKeyType{}

// WithPrincipal supplies attribution only. It is never an authorization shortcut.
// A derived context is visible downstream, not to an already running parent.
func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalContextKey, clonePrincipal(principal))
}
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	if ctx == nil {
		return Principal{}, false
	}
	principal, ok := ctx.Value(principalContextKey).(Principal)
	return clonePrincipal(principal), ok
}
func clonePrincipal(principal Principal) Principal {
	principal.Roles = slices.Clone(principal.Roles)
	return principal
}

type authenticationScopeKey struct{}
type authenticationScope struct {
	mu    sync.Mutex
	entry *authenticationEntry
}
type authenticationEntry struct {
	owner     *Service
	tokenHash [32]byte
	done      chan struct{}
	principal Principal
	valid     bool
}

// WithRequestAuthentication starts a NEW request-local scope, even if its parent
// already carries one. Only a successful live authentication can populate it.
// Bound to one service and exact credential; there is no cross-request TTL cache.
func WithRequestAuthentication(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, authenticationScopeKey{}, &authenticationScope{})
}

// EnsureRequestAuthentication reuses the request-local scope when one is
// already present and only creates one otherwise.
//
// Middleware stacked in front of a handler must share the scope instead of
// replacing it: a replacement silently discards work the outer layer already
// did, so the same credential is resolved once per layer and the audit trail
// loses the identity recorded onto the outer context.
func EnsureRequestAuthentication(ctx context.Context) context.Context {
	if ctx == nil {
		return WithRequestAuthentication(nil)
	}
	if _, ok := ctx.Value(authenticationScopeKey{}).(*authenticationScope); ok {
		return ctx
	}
	return WithRequestAuthentication(ctx)
}

func (s *Service) authenticateInRequest(ctx context.Context, token string, load func() (Principal, error)) (Principal, error) {
	scope, ok := ctx.Value(authenticationScopeKey{}).(*authenticationScope)
	if !ok {
		return load()
	}
	hash := sha256.Sum256([]byte(token))
	scope.mu.Lock()
	entry := scope.entry
	first := entry == nil
	if first {
		entry = &authenticationEntry{owner: s, tokenHash: hash, done: make(chan struct{})}
		scope.entry = entry
	}
	scope.mu.Unlock()
	// Do not share a result across credentials or across authorizer instances.
	// A single entry bounds request-local memory even for unusual internal callers.
	if entry.owner != s || entry.tokenHash != hash {
		return load()
	}
	if first {
		// Even a panic releases waiters, without publishing a zero-value identity.
		defer func() {
			if !entry.valid {
				scope.mu.Lock()
				if scope.entry == entry {
					scope.entry = nil
				}
				scope.mu.Unlock()
			}
			close(entry.done)
		}()
		principal, err := load()
		if err == nil {
			entry.principal = clonePrincipal(principal)
			entry.valid = true
		}
		return principal, err
	}
	select {
	case <-ctx.Done():
		return Principal{}, authenticationUnavailable(ctx.Err())
	case <-entry.done:
		if err := ctx.Err(); err != nil {
			return Principal{}, authenticationUnavailable(err)
		}
		if entry.valid {
			return clonePrincipal(entry.principal), nil
		}
		// Failed/aborted authentication is not cached. A retry may publish its
		// own successful snapshot, including for audit after a transient failure.
		return s.authenticateInRequest(ctx, token, load)
	}
}
