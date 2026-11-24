package app

import (
	"bugpk-api/internal/middleware"
	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/route"
	"go.uber.org/zap"
)

// All system routes use this composition boundary; the public gateway does not.
//
// Account-ban enforcement is NOT mounted here: it is registered once on the
// server (see RegisterRoutes) so that every prefix inherits it, including
// groups added later. Keeping it off this boundary also avoids two
// authentications per request now that the global guard runs first.
func newSystemRouteGroup(h *server.Hertz, prefix string, audit hertzapp.HandlerFunc, log *zap.Logger) *route.RouterGroup {
	group := h.Group(prefix)
	// Recover business panics before audit finalization reads the HTTP result.
	// The outer application recovery still protects failures in audit itself.
	group.Use(audit, middleware.Recovery(log))
	return group
}
