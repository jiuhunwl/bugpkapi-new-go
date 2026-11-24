package jsonrequest

import (
	"errors"
	"strings"
	"testing"

	apperrors "bugpk-api/internal/pkg/errors"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

func TestBindHertzRejectsOversizedDeclaredBodyBeforeRead(t *testing.T) {
	ctx := hertzapp.NewContext(0)
	ctx.Request.Header.SetContentLength(17)

	err := BindHertz(ctx, &map[string]any{}, 16)
	assertBindStatus(t, err, consts.StatusRequestEntityTooLarge)
}

func TestBindHertzRejectsOversizedBodyWithoutTrustedLength(t *testing.T) {
	ctx := hertzapp.NewContext(0)
	ctx.Request.SetBodyString(strings.Repeat("x", 17))
	ctx.Request.Header.SetContentLength(-1)

	err := BindHertz(ctx, &map[string]any{}, 16)
	assertBindStatus(t, err, consts.StatusRequestEntityTooLarge)
}

func TestBindHertzRejectsAmbiguousJSONAndAcceptsEmptyBody(t *testing.T) {
	ctx := hertzapp.NewContext(0)
	ctx.Request.SetBodyString(`{"id":1,"id":2}`)
	var target map[string]any
	err := BindHertz(ctx, &target, DefaultMaxBodyBytes)
	assertBindStatus(t, err, consts.StatusBadRequest)

	empty := hertzapp.NewContext(0)
	if err := BindHertz(empty, &target, DefaultMaxBodyBytes); err != nil {
		t.Fatalf("empty body rejected: %v", err)
	}
}

func TestBindHertzDecodesValidJSON(t *testing.T) {
	ctx := hertzapp.NewContext(0)
	ctx.Request.SetBodyString(`{"name":"BugPk-Api"}`)
	var target struct {
		Name string `json:"name"`
	}
	if err := BindHertz(ctx, &target, DefaultMaxBodyBytes); err != nil {
		t.Fatal(err)
	}
	if target.Name != "BugPk-Api" {
		t.Fatalf("decoded name=%q", target.Name)
	}
}

func assertBindStatus(t *testing.T, err error, want int) {
	t.Helper()
	var business *apperrors.BusinessError
	if !errors.As(err, &business) || business.StatusCode != want {
		t.Fatalf("BindHertz error=%v, want status %d", err, want)
	}
}
