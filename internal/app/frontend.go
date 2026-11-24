package app

import (
	"context"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"bugpk-api/internal/config"
	"bugpk-api/internal/pkg/response"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"go.uber.org/zap"
)

const frontendNotFoundMessage = "接口不存在"
const frontendMethodNotAllowedMessage = "请求方法不支持"

// registerFrontendRoutes installs the final fallback for the unified Go
// process. API-shaped paths always receive a JSON 404, while browser GET/HEAD
// requests are served from the built SPA when frontend serving is enabled.
// This keeps a missing reverse-proxy location from turning an API response
// into index.html.
func registerFrontendRoutes(h *server.Hertz, cfg config.FrontendConfig, systemPrefix string, log *zap.Logger) {
	if h == nil {
		return
	}
	if log == nil {
		log = zap.NewNop()
	}

	var distRoot, indexPath string
	if cfg.Enabled {
		distRoot = resolveFrontendDist(cfg.DistPath)
		if distRoot != "" {
			var ok bool
			indexPath, ok = resolveFrontendFile(distRoot, "/index.html")
			if !ok {
				log.Warn("frontend serving enabled but index.html is unavailable",
					zap.String("dist_path", distRoot),
				)
				indexPath = ""
			}
		} else {
			log.Warn("frontend serving enabled but the dist directory is unavailable",
				zap.String("configured_dist_path", strings.TrimSpace(cfg.DistPath)),
			)
		}
	}

	h.NoRoute(func(_ context.Context, c *hertzapp.RequestContext) {
		requestPath := string(c.Path())
		// Hertz normalizes dot segments before route lookup. Inspect the raw
		// URI as well so /api/../x cannot be reclassified as a frontend route.
		rawRequestPath := string(c.URI().PathOriginal())
		apiRequest := isAPIRequestPath(requestPath, systemPrefix) ||
			isAPIRequestPath(rawRequestPath, systemPrefix)
		jsonRequest := apiRequest || acceptsJSON(c)

		if !jsonRequest && cfg.Enabled && (c.IsGet() || c.IsHead()) && distRoot != "" {
			if staticPath, ok := resolveFrontendFile(distRoot, requestPath); ok {
				serveFrontendFile(c, staticPath)
				return
			}
			if indexPath != "" && shouldServeSPA(requestPath) {
				serveFrontendFile(c, indexPath)
				return
			}
		}

		if jsonRequest {
			response.Fail(c, consts.StatusNotFound, frontendNotFoundMessage)
			return
		}

		// Keep non-API misses deterministic and non-HTML when the built-in
		// frontend is disabled or a requested asset does not exist.
		c.Data(consts.StatusNotFound, "text/plain; charset=utf-8", []byte("Not Found"))
	})

	h.NoMethod(func(_ context.Context, c *hertzapp.RequestContext) {
		apiRequest := isAPIRequestPath(string(c.Path()), systemPrefix) ||
			isAPIRequestPath(string(c.URI().PathOriginal()), systemPrefix)
		if apiRequest || acceptsJSON(c) {
			response.Fail(c, consts.StatusMethodNotAllowed, frontendMethodNotAllowedMessage)
			return
		}
		c.Data(consts.StatusMethodNotAllowed, "text/plain; charset=utf-8", []byte("Method Not Allowed"))
	})
}

func serveFrontendFile(c *hertzapp.RequestContext, filePath string) {
	// RequestContext.File rewrites the request URI to the absolute filesystem
	// path. Restore it so post-handler middleware, tracing, and access logs keep
	// reporting the public URL requested by the client.
	originalURI := string(c.Request.URI().RequestURI())
	defer c.Request.SetRequestURI(originalURI)
	c.File(filePath)
}

func resolveFrontendDist(configured string) string {
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return ""
	}

	candidates := make([]string, 0, 4)
	addCandidate := func(candidate string) {
		if strings.TrimSpace(candidate) == "" {
			return
		}
		absolute, err := filepath.Abs(candidate)
		if err != nil {
			return
		}
		for _, existing := range candidates {
			if sameFilePath(existing, absolute) {
				return
			}
		}
		candidates = append(candidates, absolute)
	}

	if filepath.IsAbs(configured) {
		addCandidate(configured)
	} else {
		// The normal release layout starts the binary from its package root,
		// but resolving against both cwd and executable directory also covers
		// supervisors/systemd units that choose a different working directory.
		if cwd, err := os.Getwd(); err == nil {
			addCandidate(filepath.Join(cwd, configured))
		}
		if executable, err := os.Executable(); err == nil {
			addCandidate(filepath.Join(filepath.Dir(executable), configured))
		}
		// CONFIG_FILE is commonly placed next to the binary in deployments;
		// use its directory as a final relative-path fallback.
		if configFile := strings.TrimSpace(os.Getenv("CONFIG_FILE")); configFile != "" {
			if absoluteConfig, err := filepath.Abs(configFile); err == nil {
				addCandidate(filepath.Join(filepath.Dir(absoluteConfig), configured))
			}
		}
	}

	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err == nil && info.IsDir() {
			return candidate
		}
	}
	return ""
}

func sameFilePath(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if filepath.Separator == '\\' {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func resolveFrontendFile(root, requestPath string) (string, bool) {
	candidate, ok := safeFrontendCandidate(root, requestPath)
	if !ok {
		return "", false
	}
	return candidate, true
}

func safeFrontendCandidate(root, requestPath string) (string, bool) {
	decoded, err := url.PathUnescape(requestPath)
	if err != nil || decoded == "" {
		decoded = requestPath
	}
	if decoded == "" {
		decoded = "/"
	}
	if !strings.HasPrefix(decoded, "/") || strings.ContainsRune(decoded, '\x00') || strings.Contains(decoded, "\\") {
		return "", false
	}

	relativeURL := strings.TrimPrefix(decoded, "/")
	if relativeURL == "" {
		relativeURL = "."
	}
	relative := filepath.FromSlash(relativeURL)
	if filepath.IsAbs(relative) || filepath.VolumeName(relative) != "" {
		return "", false
	}
	relative = filepath.Clean(relative)
	if relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return "", false
	}
	if containsPrivateFrontendSegment(relative) {
		return "", false
	}

	rootAbsolute, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	candidate := filepath.Join(rootAbsolute, relative)
	if !pathWithin(rootAbsolute, candidate) {
		return "", false
	}

	rootResolved, err := filepath.EvalSymlinks(rootAbsolute)
	if err != nil {
		return "", false
	}
	info, err := os.Stat(candidate)
	if err != nil {
		return "", false
	}
	if info.IsDir() {
		candidate = filepath.Join(candidate, "index.html")
		if !pathWithin(rootAbsolute, candidate) {
			return "", false
		}
		info, err = os.Stat(candidate)
		if err != nil {
			return "", false
		}
	}
	if !info.Mode().IsRegular() {
		return "", false
	}

	resolvedCandidate, err := filepath.EvalSymlinks(candidate)
	if err != nil || !pathWithin(rootResolved, resolvedCandidate) {
		return "", false
	}
	return candidate, true
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator)))
}

func containsPrivateFrontendSegment(relative string) bool {
	for _, segment := range strings.Split(filepath.ToSlash(relative), "/") {
		if segment == "" || segment == "." {
			continue
		}
		lower := strings.ToLower(segment)
		if strings.HasPrefix(segment, ".") {
			return true
		}
		switch lower {
		case "config.yaml", "config.yml", ".env", "bugpk-api", "bugpk-api.exe", "bugpk_api.sql", "bugpk_api_init.sql":
			return true
		}
	}
	return false
}

func shouldServeSPA(requestPath string) bool {
	decoded, err := url.PathUnescape(requestPath)
	if err != nil {
		decoded = requestPath
	}
	if decoded == "" || decoded == "/" {
		return true
	}
	if strings.ContainsRune(decoded, '\x00') || strings.Contains(decoded, "\\") {
		return false
	}
	cleaned := path.Clean(decoded)
	if cleaned == "." || cleaned == "/" {
		return true
	}
	// A missing URL with an extension is an asset miss, not a client-side
	// route. Returning index.html for it causes a misleading JavaScript/CSS
	// MIME error in the browser.
	return path.Ext(path.Base(cleaned)) == ""
}

// apiRequestPrefixes 是「接口形态」的固定路径前缀（系统前缀另行传入）。
// 这些前缀下的未匹配请求必须返回 JSON 错误，绝不能落到 SPA 兜底：否则
// 客户端会拿到 HTTP 200 的 HTML，把路径写错误判成调用成功。
var apiRequestPrefixes = []string{"/api", "/open-api"}

func isAPIRequestPath(requestPath, systemPrefix string) bool {
	decoded, err := url.PathUnescape(requestPath)
	if err == nil && decoded != "" {
		requestPath = decoded
	}
	// Keep dot segments intact here. A request such as /api/../console must
	// still be treated as an API-shaped request and must never receive SPA
	// HTML as its error response.
	requestPath = "/" + strings.TrimPrefix(requestPath, "/")
	if routePrefixMatches(requestPath, normalizeRoutePrefix(systemPrefix)) {
		return true
	}
	for _, prefix := range apiRequestPrefixes {
		if routePrefixMatches(requestPath, prefix) {
			return true
		}
	}
	return false
}

func normalizeRoutePrefix(prefix string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" || prefix == "/" {
		return "/system-api"
	}
	if !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}
	return strings.TrimRight(prefix, "/")
}

func routePrefixMatches(requestPath, prefix string) bool {
	if prefix == "" || prefix == "/" {
		return requestPath == "/"
	}
	return requestPath == prefix || strings.HasPrefix(requestPath, prefix+"/")
}

func acceptsJSON(c *hertzapp.RequestContext) bool {
	accept := strings.ToLower(strings.TrimSpace(string(c.GetHeader("Accept"))))
	if accept == "" {
		return false
	}
	for _, value := range strings.Split(accept, ",") {
		mediaType := strings.TrimSpace(strings.SplitN(value, ";", 2)[0])
		if mediaType == "application/json" || mediaType == "text/json" || strings.HasSuffix(mediaType, "+json") {
			return true
		}
	}
	return false
}
