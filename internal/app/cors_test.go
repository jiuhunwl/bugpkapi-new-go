package app

import (
	"strings"
	"testing"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
)

func TestSetCORSHeadersExposesAccountBanMetadata(t *testing.T) {
	var ctx hertzapp.RequestContext
	setCORSHeaders(&ctx, "https://console.example", true)
	if got := ctx.Response.Header.Get("Access-Control-Expose-Headers"); got != corsExposedHeaders {
		t.Fatalf("exposed headers = %q, want %q", got, corsExposedHeaders)
	}
	if got := ctx.Response.Header.Get("Access-Control-Allow-Origin"); got != "https://console.example" {
		t.Fatalf("allow origin = %q", got)
	}
	if got := ctx.Response.Header.Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("allow credentials = %q", got)
	}
}

func TestCORSExposesFeedbackRequestID(t *testing.T) {
	var ctx hertzapp.RequestContext
	setCORSHeaders(&ctx, "https://client.example", false)
	if !strings.Contains(ctx.Response.Header.Get("Access-Control-Expose-Headers"), "X-Request-ID") {
		t.Fatal("cross-origin clients cannot read request ID")
	}
}
