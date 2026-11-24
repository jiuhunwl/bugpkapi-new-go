//go:build windows

package app

import (
	"bugpk-api/internal/config"

	"github.com/cloudwego/hertz/pkg/app/server"
)

func newHTTPServer(cfg config.ServerConfig, trackers ...*requestDrain) *server.Hertz {
	h := server.Default(
		server.WithHostPorts(cfg.Addr()),
		server.WithReadTimeout(cfg.ReadTimeout),
		server.WithWriteTimeout(cfg.WriteTimeout),
		server.WithIdleTimeout(cfg.IdleTimeout),
		server.WithMaxRequestBodySize(cfg.MaxRequestBodySize),
		server.WithExitWaitTime(cfg.ShutdownTimeout),
	)
	if len(trackers) > 0 && trackers[0] != nil {
		h.Use(trackers[0].middleware())
	}
	return h
}
