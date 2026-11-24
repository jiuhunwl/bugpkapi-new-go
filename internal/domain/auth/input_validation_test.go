package auth

import (
	"context"
	"errors"
	"strings"
	"testing"

	apperrors "bugpk-api/internal/pkg/errors"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

func TestBindJSONRejectsOversizedAuthenticationBody(t *testing.T) {
	for name, configure := range map[string]func(*hertzapp.RequestContext){
		"declared length": func(ctx *hertzapp.RequestContext) {
			ctx.Request.SetBodyString("{}")
			ctx.Request.Header.SetContentLength(maxAuthRequestBodyBytes + 1)
		},
		"unknown length": func(ctx *hertzapp.RequestContext) {
			ctx.Request.SetBodyString(strings.Repeat("x", maxAuthRequestBodyBytes+1))
			ctx.Request.Header.SetContentLength(-1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := hertzapp.NewContext(0)
			ctx.Request.SetMethod("POST")
			configure(ctx)
			var target LoginRequest
			err := bindJSON(ctx, &target)
			var business *apperrors.BusinessError
			if !errors.As(err, &business) || business.StatusCode != consts.StatusRequestEntityTooLarge {
				t.Fatalf("oversized auth body error = %v", err)
			}
		})
	}
}

func TestBindJSONRejectsAmbiguousAndDeepAuthenticationJSON(t *testing.T) {
	for name, body := range map[string]string{
		"duplicate": `{"username":"first","username":"second"}`,
		"deep":      strings.Repeat(`{"nested":`, maxAuthJSONNestingDepth+1) + `0` + strings.Repeat(`}`, maxAuthJSONNestingDepth+1),
	} {
		t.Run(name, func(t *testing.T) {
			ctx := hertzapp.NewContext(0)
			ctx.Request.SetMethod("POST")
			ctx.Request.SetBodyString(body)
			var target LoginRequest
			err := bindJSON(ctx, &target)
			var business *apperrors.BusinessError
			if !errors.As(err, &business) || business.StatusCode != consts.StatusBadRequest {
				t.Fatalf("ambiguous auth JSON error = %v", err)
			}
		})
	}
}

func TestLoginRejectsOversizedCredentialsBeforeDependencies(t *testing.T) {
	service := &Service{}
	for name, request := range map[string]LoginRequest{
		"account":  {Username: strings.Repeat("a", maxLoginAccountBytes+1), Password: "valid-password"},
		"password": {Username: "admin", Password: strings.Repeat("p", 73)},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := service.Login(context.Background(), request, "binding", "203.0.113.1", "agent")
			var business *apperrors.BusinessError
			if !errors.As(err, &business) || business.StatusCode != consts.StatusBadRequest {
				t.Fatalf("oversized %s error = %v", name, err)
			}
		})
	}
}
