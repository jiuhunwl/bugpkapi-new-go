package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bugpk-api/internal/config"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

func TestFrontendFallbackAndAPINotFoundResponse(t *testing.T) {
	dist, err := filepath.Abs(filepath.Join("testdata", "frontend"))
	if err != nil {
		t.Fatal(err)
	}

	h := server.New()
	registerFrontendRoutes(h, config.FrontendConfig{Enabled: true, DistPath: dist}, "/system-api", nil)

	tests := []struct {
		name       string
		method     string
		path       string
		headers    []ut.Header
		statusCode int
		contains   string
		notContain string
	}{
		{name: "root", method: consts.MethodGet, path: "/", statusCode: consts.StatusOK, contains: "<!doctype html>"},
		{name: "spa route", method: consts.MethodGet, path: "/console/workspace", statusCode: consts.StatusOK, contains: "<!doctype html>"},
		{name: "static asset", method: consts.MethodGet, path: "/assets/app.js", statusCode: consts.StatusOK, contains: "console.log('ok')", notContain: "<!doctype html>"},
		{name: "missing asset", method: consts.MethodGet, path: "/assets/missing.js", statusCode: consts.StatusNotFound, contains: "Not Found", notContain: "<!doctype html>"},
		{name: "open api miss", method: consts.MethodGet, path: "/api/missing", statusCode: consts.StatusNotFound, contains: `"code":-1`, notContain: "<!doctype html>"},
		{name: "normalized open api miss", method: consts.MethodGet, path: "/api/../missing", statusCode: consts.StatusNotFound, contains: `"code":-1`, notContain: "<!doctype html>"},
		{name: "system api miss", method: consts.MethodGet, path: "/system-api/missing", statusCode: consts.StatusNotFound, contains: `"code":-1`, notContain: "<!doctype html>"},
		{name: "open-api miss", method: consts.MethodGet, path: "/open-api/missing", statusCode: consts.StatusNotFound, contains: `"code":-1`, notContain: "<!doctype html>"},
		{name: "normalized open-api miss", method: consts.MethodGet, path: "/open-api/../missing", statusCode: consts.StatusNotFound, contains: `"code":-1`, notContain: "<!doctype html>"},
		{name: "open-api prefix lookalike stays spa", method: consts.MethodGet, path: "/open-apiary", statusCode: consts.StatusOK, contains: "<!doctype html>"},
		{name: "json accept miss", method: consts.MethodGet, path: "/some/missing", headers: []ut.Header{{Key: "Accept", Value: "application/json"}}, statusCode: consts.StatusNotFound, contains: `"code":-1`, notContain: "<!doctype html>"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := ut.PerformRequest(h.Engine, tt.method, tt.path, nil, tt.headers...)
			if recorder.Code != tt.statusCode {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, tt.statusCode, recorder.Body.String())
			}
			body := recorder.Body.String()
			if tt.contains != "" && !strings.Contains(body, tt.contains) {
				t.Fatalf("body does not contain %q: %s", tt.contains, body)
			}
			if tt.notContain != "" && strings.Contains(body, tt.notContain) {
				t.Fatalf("body unexpectedly contains %q: %s", tt.notContain, body)
			}
			if strings.Contains(tt.name, "api miss") || tt.name == "json accept miss" {
				contentType := strings.ToLower(string(recorder.Header().ContentType()))
				if !strings.Contains(contentType, "application/json") {
					t.Fatalf("API miss content type = %q, want application/json", contentType)
				}
				var payload map[string]any
				if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
					t.Fatalf("API miss is not JSON: %v; body=%s", err, body)
				}
			}
		})
	}
}

func TestFrontendDisabledStillReturnsJSONForAPIPaths(t *testing.T) {
	h := server.New()
	registerFrontendRoutes(h, config.FrontendConfig{Enabled: false}, "/system-api", nil)

	for _, apiPath := range []string{"/system-api/missing", "/api/missing", "/open-api/missing"} {
		recorder := ut.PerformRequest(h.Engine, consts.MethodGet, apiPath, nil)
		if recorder.Code != consts.StatusNotFound {
			t.Fatalf("%s status = %d, want %d", apiPath, recorder.Code, consts.StatusNotFound)
		}
		if strings.Contains(recorder.Body.String(), "<!doctype html>") {
			t.Fatalf("%s API miss returned HTML: %s", apiPath, recorder.Body.String())
		}
	}
}

func TestFrontendPathTraversalIsRejected(t *testing.T) {
	dist := t.TempDir()
	outside := filepath.Join(filepath.Dir(dist), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })

	if _, ok := resolveFrontendFile(dist, "/../outside.txt"); ok {
		t.Fatal("path traversal unexpectedly resolved")
	}
	if _, ok := resolveFrontendFile(dist, "/config.yaml"); ok {
		t.Fatal("private deployment file unexpectedly resolved")
	}
}

func TestServeFrontendFileRestoresRequestURI(t *testing.T) {
	indexPath, err := filepath.Abs(filepath.Join("testdata", "frontend", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	c := ut.CreateUtRequestContext(consts.MethodGet, "/console/workspace?tab=summary", nil)

	serveFrontendFile(c, indexPath)

	if got := string(c.Request.URI().RequestURI()); got != "/console/workspace?tab=summary" {
		t.Fatalf("request URI = %q, want original public URI", got)
	}
}
