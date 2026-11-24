package middleware

import (
	"context"
	"fmt"
	"runtime/debug"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"go.uber.org/zap"

	"bugpk-api/internal/pkg/masking"
	"bugpk-api/internal/pkg/response"
)

// Recovery 统一兜底 panic：响应只返回 code/message/error_id，
// 完整 stack 仅写入日志，避免泄露内部实现细节。
func Recovery(log *zap.Logger) hertzapp.HandlerFunc {
	if log == nil {
		log = zap.NewNop()
	}
	return func(ctx context.Context, c *hertzapp.RequestContext) {
		defer func() {
			if recovered := recover(); recovered != nil {
				errorID := response.NewErrorID()
				stack := masking.StackText(string(debug.Stack()))
				log.Error("panic recovered",
					zap.String("error_id", errorID),
					zap.String("panic", masking.ErrorText(fmt.Sprint(recovered))),
					zap.String("method", string(c.Method())),
					zap.String("path", string(c.Path())),
					zap.String("client_ip", masking.IP(c.ClientIP())),
					zap.String("stack", stack),
				)
				response.FailServerErrorID(c, consts.StatusInternalServerError, errorID, nil)
				c.Abort()
			}
		}()

		c.Next(ctx)
	}
}
