package gateway

import (
	"context"

	"bugpk-api/internal/pkg/requestid"
	"go.uber.org/zap"
)

func (s *Service) requestLogger(ctx context.Context) *zap.Logger {
	if s.log == nil {
		return nil
	}
	return s.log.With(zap.String("request_id", requestid.FromContext(ctx)))
}
