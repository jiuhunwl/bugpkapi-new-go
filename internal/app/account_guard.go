package app

import (
	"context"
	"strings"

	"bugpk-api/internal/domain/rbac"
	"bugpk-api/internal/pkg/accountstatus"
	"bugpk-api/internal/pkg/response"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
)

// newAccountStatusGuard makes "banned means every feature is unavailable" a
// property of the route group instead of a promise each handler keeps by hand.
//
// The /system-api tree is composed without a router-level authenticator: every
// handler calls its own principal helper. That design means a single handler
// that forgets the call silently serves a banned account, and there is no
// central place to notice. This guard runs before all of them and closes that
// hole for the whole tree, including handlers added later.
//
// It deliberately narrows what it terminates. Only a rejection that carries ban
// details stops the request; a 401 from an expired or malformed token keeps
// flowing to the handler, because the browser relies on that 401 to trigger the
// refresh flow and turning it into a terminal response would log out every user
// whose access token merely aged out. Anonymous requests also pass through:
// public routes such as the site configuration and the market catalog live in
// the same tree and must stay reachable without a credential.
func newAccountStatusGuard(authz *rbac.Service) hertzapp.HandlerFunc {
	if authz == nil {
		return func(ctx context.Context, c *hertzapp.RequestContext) { c.Next(ctx) }
	}
	return func(ctx context.Context, c *hertzapp.RequestContext) {
		token := bearerTokenFromRequest(c)
		if token == "" {
			c.Next(ctx)
			return
		}
		// Share the request-local authentication scope with the layers around
		// this one: the audit middleware already opened a scope before us, and
		// downstream handlers authenticate again. Replacing it would resolve the
		// same credential once per layer.
		scopeCtx := rbac.EnsureRequestAuthentication(ctx)
		if _, err := authz.Authenticate(scopeCtx, token); err != nil {
			if accountstatus.IsBanError(err) {
				response.FromError(c, err)
				c.Abort()
				return
			}
		}
		c.Next(scopeCtx)
	}
}

func bearerTokenFromRequest(c *hertzapp.RequestContext) string {
	value := string(c.Request.Header.Peek("Authorization"))
	if len(value) < 7 || !strings.EqualFold(value[:7], "bearer ") {
		return ""
	}
	return strings.TrimSpace(value[7:])
}
