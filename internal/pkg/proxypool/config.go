package proxypool

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const (
	SourceAPI   = "API"
	SourceFixed = "FIXED"

	ProtocolHTTP   = "HTTP"
	ProtocolHTTPS  = "HTTPS"
	ProtocolSOCKS5 = "SOCKS5"
)

type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type Config struct {
	APIHeaders         []Header `json:"apiHeaders,omitempty"`
	APIURL             string   `json:"apiUrl,omitempty"`
	Enabled            int      `json:"enabled"`
	Host               string   `json:"host,omitempty"`
	JSONPath           string   `json:"jsonPath,omitempty"`
	Password           string   `json:"password,omitempty"`
	PasswordConfigured bool     `json:"passwordConfigured,omitempty"`
	Port               int      `json:"port,omitempty"`
	Protocol           string   `json:"protocol,omitempty"`
	RefreshSecs        int      `json:"refreshSeconds,omitempty"`
	Source             string   `json:"source,omitempty"`
	Username           string   `json:"username,omitempty"`
}

type Candidate struct {
	Path  string `json:"path"`
	Value string `json:"value"`
}

type Endpoint struct {
	Host     string
	Password string
	Port     int
	Protocol string
	Username string
}

func Normalize(cfg Config) (Config, error) {
	if cfg.Enabled != 0 && cfg.Enabled != 1 {
		return Config{}, errors.New("代理启用状态不合法")
	}
	if cfg.Enabled == 0 && configIsEmpty(cfg) {
		return Config{Enabled: 0}, nil
	}
	cfg.Source = strings.ToUpper(strings.TrimSpace(cfg.Source))
	cfg.Protocol = strings.ToUpper(strings.TrimSpace(cfg.Protocol))
	if cfg.Protocol == "" {
		cfg.Protocol = ProtocolHTTP
	}
	if cfg.Protocol != ProtocolHTTP && cfg.Protocol != ProtocolHTTPS && cfg.Protocol != ProtocolSOCKS5 {
		return Config{}, errors.New("代理协议只能是 HTTP、HTTPS 或 SOCKS5")
	}
	cfg.Username = strings.TrimSpace(cfg.Username)
	cfg.Password = strings.TrimSpace(cfg.Password)
	switch cfg.Source {
	case SourceFixed:
		cfg.Host = strings.TrimSpace(strings.Trim(cfg.Host, "[]"))
		if cfg.Enabled == 1 || cfg.Host != "" || cfg.Port != 0 {
			if err := validateHostPort(cfg.Host, cfg.Port); err != nil {
				return Config{}, err
			}
		}
		cfg.APIURL = ""
		cfg.APIHeaders = nil
		cfg.JSONPath = ""
		cfg.RefreshSecs = 0
	case SourceAPI:
		apiURL := strings.TrimSpace(cfg.APIURL)
		if cfg.Enabled == 1 || apiURL != "" {
			parsed, err := url.Parse(apiURL)
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil {
				return Config{}, errors.New("代理池接口必须是绝对 HTTP(S) 地址且不能内嵌账号密码")
			}
			cfg.APIURL = parsed.String()
		} else {
			cfg.APIURL = ""
		}
		cfg.JSONPath = strings.TrimPrefix(strings.TrimSpace(cfg.JSONPath), "$.")
		if cfg.RefreshSecs == 0 {
			cfg.RefreshSecs = 30
		}
		if cfg.RefreshSecs < 5 || cfg.RefreshSecs > 3600 {
			return Config{}, errors.New("代理池刷新间隔必须在 5 到 3600 秒之间")
		}
		headers := make([]Header, 0, len(cfg.APIHeaders))
		seen := map[string]bool{}
		for _, item := range cfg.APIHeaders {
			name := http.CanonicalHeaderKey(strings.TrimSpace(item.Name))
			value := strings.TrimSpace(item.Value)
			if name == "" && value == "" {
				continue
			}
			if name == "" || value == "" || strings.ContainsAny(name, "\r\n:") || strings.ContainsAny(value, "\r\n") {
				return Config{}, errors.New("代理池请求头格式不合法")
			}
			if name == "Host" || name == "Content-Length" || name == "Connection" {
				return Config{}, fmt.Errorf("不允许自定义请求头 %s", name)
			}
			if seen[name] {
				return Config{}, fmt.Errorf("代理池请求头 %s 重复", name)
			}
			seen[name] = true
			headers = append(headers, Header{Name: name, Value: value})
		}
		if len(headers) > 20 {
			return Config{}, errors.New("代理池请求头不能超过 20 个")
		}
		cfg.APIHeaders = headers
		cfg.Host = ""
		cfg.Port = 0
	default:
		return Config{}, errors.New("代理来源只能是固定代理或代理池接口")
	}
	return cfg, nil
}

func configIsEmpty(cfg Config) bool {
	return strings.TrimSpace(cfg.Source) == "" &&
		strings.TrimSpace(cfg.Protocol) == "" &&
		strings.TrimSpace(cfg.Host) == "" &&
		cfg.Port == 0 &&
		strings.TrimSpace(cfg.APIURL) == "" &&
		len(cfg.APIHeaders) == 0 &&
		strings.TrimSpace(cfg.JSONPath) == "" &&
		cfg.RefreshSecs == 0 &&
		strings.TrimSpace(cfg.Username) == "" &&
		strings.TrimSpace(cfg.Password) == "" &&
		!cfg.PasswordConfigured
}

func ParseAPIResponse(cfg Config, data []byte) (Endpoint, []Candidate, error) {
	var root any
	if err := json.Unmarshal(data, &root); err != nil {
		return Endpoint{}, nil, fmt.Errorf("代理池接口返回的不是有效 JSON: %w", err)
	}
	candidates := detectCandidates(root)
	if cfg.JSONPath != "" {
		value, ok := valueAtPath(root, cfg.JSONPath)
		if !ok {
			return Endpoint{}, candidates, fmt.Errorf("代理标签 %s 不存在", cfg.JSONPath)
		}
		endpoint, err := endpointFromValue(value, cfg.JSONPath, root, cfg)
		return endpoint, candidates, err
	}
	for _, candidate := range candidates {
		value, ok := valueAtPath(root, candidate.Path)
		if !ok {
			continue
		}
		endpoint, err := endpointFromValue(value, candidate.Path, root, cfg)
		if err == nil {
			return endpoint, candidates, nil
		}
	}
	return Endpoint{}, candidates, errors.New("未在代理池 JSON 中识别到可用的代理地址和端口")
}

func FixedEndpoint(cfg Config) (Endpoint, error) {
	normalized, err := Normalize(cfg)
	if err != nil {
		return Endpoint{}, err
	}
	if normalized.Enabled != 1 || normalized.Source != SourceFixed {
		return Endpoint{}, errors.New("当前配置不是固定代理")
	}
	return Endpoint{Protocol: normalized.Protocol, Host: normalized.Host, Port: normalized.Port, Username: normalized.Username, Password: normalized.Password}, nil
}

func endpointFromValue(value any, path string, root any, cfg Config) (Endpoint, error) {
	if object, ok := value.(map[string]any); ok {
		host, port, ok := hostPortFromObject(object)
		if !ok {
			return Endpoint{}, fmt.Errorf("代理标签 %s 未包含地址和端口", path)
		}
		return newEndpoint(cfg, host, port)
	}
	text := strings.TrimSpace(fmt.Sprint(value))
	if text == "" || text == "<nil>" {
		return Endpoint{}, fmt.Errorf("代理标签 %s 为空", path)
	}
	host, port, err := splitEndpoint(text)
	if err == nil {
		return newEndpoint(cfg, host, port)
	}
	parentPath := path
	if index := strings.LastIndex(parentPath, "."); index >= 0 {
		parentPath = parentPath[:index]
	} else {
		parentPath = ""
	}
	parent, ok := valueAtPath(root, parentPath)
	if !ok {
		return Endpoint{}, err
	}
	if object, ok := parent.(map[string]any); ok {
		for _, key := range []string{"port", "proxy_port", "proxyPort"} {
			if rawPort, exists := object[key]; exists {
				portValue, portErr := parsePort(rawPort)
				if portErr == nil {
					return newEndpoint(cfg, text, portValue)
				}
			}
		}
	}
	return Endpoint{}, err
}

func newEndpoint(cfg Config, host string, port int) (Endpoint, error) {
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if err := validateHostPort(host, port); err != nil {
		return Endpoint{}, err
	}
	return Endpoint{Protocol: cfg.Protocol, Host: host, Port: port, Username: cfg.Username, Password: cfg.Password}, nil
}

func splitEndpoint(text string) (string, int, error) {
	text = strings.TrimSpace(text)
	if strings.Contains(text, "://") {
		parsed, err := url.Parse(text)
		if err != nil || parsed.Hostname() == "" || parsed.Port() == "" {
			return "", 0, errors.New("代理地址必须包含端口")
		}
		port, err := strconv.Atoi(parsed.Port())
		return parsed.Hostname(), port, err
	}
	host, rawPort, err := net.SplitHostPort(text)
	if err != nil {
		return "", 0, errors.New("代理地址必须是 host:port")
	}
	port, err := strconv.Atoi(rawPort)
	return host, port, err
}

func hostPortFromObject(object map[string]any) (string, int, bool) {
	var host string
	for _, key := range []string{"proxy", "ip", "host", "address", "proxy_ip", "proxyIp"} {
		if value, ok := object[key]; ok {
			text := strings.TrimSpace(fmt.Sprint(value))
			if parsedHost, parsedPort, err := splitEndpoint(text); err == nil {
				return parsedHost, parsedPort, true
			}
			if text != "" && text != "<nil>" {
				host = text
				break
			}
		}
	}
	if host == "" {
		return "", 0, false
	}
	for _, key := range []string{"port", "proxy_port", "proxyPort"} {
		if value, ok := object[key]; ok {
			port, err := parsePort(value)
			if err == nil {
				return host, port, true
			}
		}
	}
	return "", 0, false
}

func parsePort(value any) (int, error) {
	text := strings.TrimSpace(fmt.Sprint(value))
	port, err := strconv.Atoi(text)
	if err != nil || port < 1 || port > 65535 {
		return 0, errors.New("代理端口不合法")
	}
	return port, nil
}

func validateHostPort(host string, port int) error {
	if strings.TrimSpace(host) == "" || strings.ContainsAny(host, "/?#@ ") {
		return errors.New("代理地址不能为空且不能包含路径或凭据")
	}
	if port < 1 || port > 65535 {
		return errors.New("代理端口必须在 1 到 65535 之间")
	}
	return nil
}

func valueAtPath(root any, path string) (any, bool) {
	if path == "" {
		return root, true
	}
	current := root
	for _, segment := range strings.Split(path, ".") {
		switch typed := current.(type) {
		case map[string]any:
			value, ok := typed[segment]
			if !ok {
				return nil, false
			}
			current = value
		case []any:
			index, err := strconv.Atoi(segment)
			if err != nil || index < 0 || index >= len(typed) {
				return nil, false
			}
			current = typed[index]
		default:
			return nil, false
		}
	}
	return current, true
}

func detectCandidates(root any) []Candidate {
	result := make([]Candidate, 0)
	seen := map[string]bool{}
	var walk func(any, string)
	walk = func(value any, path string) {
		switch typed := value.(type) {
		case map[string]any:
			if host, port, ok := hostPortFromObject(typed); ok {
				hostPath := path
				for _, key := range []string{"proxy", "ip", "host", "address", "proxy_ip", "proxyIp"} {
					if _, exists := typed[key]; exists {
						hostPath = joinPath(path, key)
						break
					}
				}
				if !seen[hostPath] {
					seen[hostPath] = true
					result = append(result, Candidate{Path: hostPath, Value: net.JoinHostPort(host, strconv.Itoa(port))})
				}
			}
			priority := []string{"proxy", "ip", "host", "address", "proxy_ip", "proxyIp"}
			for _, key := range priority {
				if child, ok := typed[key]; ok {
					walk(child, joinPath(path, key))
				}
			}
			for key, child := range typed {
				if contains(priority, key) {
					continue
				}
				walk(child, joinPath(path, key))
			}
		case []any:
			for index, child := range typed {
				walk(child, joinPath(path, strconv.Itoa(index)))
			}
		case string:
			if host, port, err := splitEndpoint(typed); err == nil && !seen[path] {
				seen[path] = true
				result = append(result, Candidate{Path: path, Value: net.JoinHostPort(host, strconv.Itoa(port))})
			}
		}
	}
	walk(root, "")
	return result
}

func joinPath(parent, child string) string {
	if parent == "" {
		return child
	}
	return parent + "." + child
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
