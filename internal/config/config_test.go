package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/domain/analytics"

	mysqldriver "github.com/go-sql-driver/mysql"
)

func TestEmailEnglishTemplateEnvironmentOverride(t *testing.T) {
	t.Setenv("SERVER_PORT", "")
	t.Setenv("DB_USER", "")
	t.Setenv("DB_PASSWORD", "")
	t.Setenv("EMAIL_CODE_SUBJECT_EN", "Custom EN subject")
	t.Setenv("EMAIL_CODE_TEMPLATE_EN", "Custom EN template {{code}}")
	t.Setenv("EMAIL_RESET_SUBJECT_EN", "Custom EN reset subject")
	t.Setenv("EMAIL_RESET_TEMPLATE_EN", "Custom EN reset template {{code}}")

	cfg, err := Load("missing-config.yaml")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Message.Email.CodeSubjectEn != "Custom EN subject" {
		t.Fatalf("EMAIL_CODE_SUBJECT_EN was not applied: %q", cfg.Message.Email.CodeSubjectEn)
	}
	if cfg.Message.Email.CodeTemplateEn != "Custom EN template {{code}}" {
		t.Fatalf("EMAIL_CODE_TEMPLATE_EN was not applied: %q", cfg.Message.Email.CodeTemplateEn)
	}
	if cfg.Message.Email.ResetSubjectEn != "Custom EN reset subject" {
		t.Fatalf("EMAIL_RESET_SUBJECT_EN was not applied: %q", cfg.Message.Email.ResetSubjectEn)
	}
	if cfg.Message.Email.ResetTemplateEn != "Custom EN reset template {{code}}" {
		t.Fatalf("EMAIL_RESET_TEMPLATE_EN was not applied: %q", cfg.Message.Email.ResetTemplateEn)
	}
	// 默认中文模板不应被英文环境变量污染。
	if cfg.Message.Email.CodeSubject != "BugPk-Api 验证码" {
		t.Fatalf("default code subject was unexpectedly changed: %q", cfg.Message.Email.CodeSubject)
	}
}

func TestLoadDefault(t *testing.T) {
	t.Setenv("SERVER_PORT", "")
	t.Setenv("DB_USER", "")
	t.Setenv("DB_PASSWORD", "")

	cfg, err := Load("missing-config.yaml")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Server.Port != 8080 {
		t.Fatalf("expected default server port 8080, got %d", cfg.Server.Port)
	}
	if cfg.Server.MaxRequestBodySize != 4*1024*1024 {
		t.Fatalf("expected default request body limit 4 MiB, got %d", cfg.Server.MaxRequestBodySize)
	}
	if cfg.Database.User != "root" || cfg.Database.Password != "root" {
		t.Fatalf("expected root/root database defaults, got %s/%s", cfg.Database.User, cfg.Database.Password)
	}
	if cfg.Redis.Addr() != "localhost:6379" {
		t.Fatalf("expected localhost:6379 redis addr, got %s", cfg.Redis.Addr())
	}
	if cfg.API.SystemPrefix != "/system-api" {
		t.Fatalf("expected default system prefix /system-api, got %s", cfg.API.SystemPrefix)
	}
	if !cfg.Frontend.Enabled || cfg.Frontend.DistPath != "frontend/dist" {
		t.Fatalf("unexpected frontend defaults: %#v", cfg.Frontend)
	}
	if !cfg.Analytics.Enabled || cfg.Analytics.BatchSize != 5000 || cfg.Analytics.PollInterval != time.Second {
		t.Fatalf("unexpected analytics defaults: %#v", cfg.Analytics)
	}
	if cfg.Network.AllowPrivateUpstreams {
		t.Fatal("private upstream access must be disabled by default")
	}
	if cfg.Network.ProxyTestURL != "https://api.ipify.org?format=json" {
		t.Fatalf("default proxy test URL = %q", cfg.Network.ProxyTestURL)
	}
}

func TestLoadProxyTestURLEnvironmentOverride(t *testing.T) {
	t.Setenv("PROXY_TEST_URL", "https://check.example.com/ip")

	cfg, err := Load("missing-config.yaml")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Network.ProxyTestURL != "https://check.example.com/ip" {
		t.Fatalf("PROXY_TEST_URL was not applied: %q", cfg.Network.ProxyTestURL)
	}
}

func TestValidateRejectsUnsafeProxyTestURL(t *testing.T) {
	for _, value := range []string{
		"/relative/ip",
		"ftp://api.example.com/ip",
		"https://user:password@api.example.com/ip",
		"http://127.0.0.1/ip",
		"http://10.0.0.8/ip",
		"http://[::1]/ip",
	} {
		t.Run(value, func(t *testing.T) {
			cfg := Default()
			cfg.Network.ProxyTestURL = value
			if err := cfg.Validate(); err == nil {
				t.Fatalf("unsafe proxy test URL accepted: %q", value)
			}
		})
	}
}

func TestLoadFrontendEnvironmentOverride(t *testing.T) {
	t.Setenv("FRONTEND_SERVE_ENABLED", "false")
	t.Setenv("FRONTEND_DIST_PATH", "public/dist")

	cfg, err := Load("missing-config.yaml")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Frontend.Enabled {
		t.Fatal("FRONTEND_SERVE_ENABLED=false was not applied")
	}
	if cfg.Frontend.DistPath != "public/dist" {
		t.Fatalf("frontend dist path = %q, want public/dist", cfg.Frontend.DistPath)
	}
}

func TestSystemConfigSecretFallsBackToPaymentSecret(t *testing.T) {
	cfg := Default()
	cfg.Payment.ConfigSecret = strings.Repeat("s", 32)
	normalizeRuntime(&cfg)
	if cfg.System.ConfigSecret != cfg.Payment.ConfigSecret {
		t.Fatalf("system config secret did not use payment fallback")
	}
}

func TestProductionRejectsShortExplicitSystemConfigSecret(t *testing.T) {
	cfg := Default()
	cfg.App.Env = "production"
	cfg.Auth.Secret = strings.Repeat("j", 32)
	cfg.Auth.RefreshCookieSecure = true
	cfg.Database.User = "bugpk"
	cfg.Database.Password = "strong-database-password"
	cfg.Payment.ConfigSecret = strings.Repeat("p", 32)
	cfg.System.ConfigSecret = "too-short"
	if err := cfg.Validate(); err == nil {
		t.Fatal("production validation accepted a short SYSTEM_CONFIG_SECRET")
	}
}

func TestLoadCORSEnvOverride(t *testing.T) {
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://www.example.com, http://192.168.1.10:8080,https://www.example.com, ")

	cfg, err := Load("missing-config.yaml")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	want := []string{"https://www.example.com", "http://192.168.1.10:8080"}
	if len(cfg.CORS.AllowedOrigins) != len(want) {
		t.Fatalf("CORS allowed origins = %#v, want %#v", cfg.CORS.AllowedOrigins, want)
	}
	for i := range want {
		if cfg.CORS.AllowedOrigins[i] != want[i] {
			t.Fatalf("CORS allowed origins = %#v, want %#v", cfg.CORS.AllowedOrigins, want)
		}
	}
}

func TestCORSDefaultIsSameOrigin(t *testing.T) {
	cfg := Default()
	if len(cfg.CORS.AllowedOrigins) != 0 {
		t.Fatalf("default CORS allowed origins must be empty (same-origin), got %#v", cfg.CORS.AllowedOrigins)
	}
}

func TestLoadExampleConfig(t *testing.T) {
	cfg, err := Load("../../configs/config.yaml.example")
	if err != nil {
		t.Fatalf("Load(example) error = %v", err)
	}
	if cfg.App.Name != "BugPk-Api" {
		t.Fatalf("expected BugPk-Api app name, got %s", cfg.App.Name)
	}
	if cfg.Auth.Secret != developmentJWTSecret {
		t.Fatal("expected development JWT placeholder in example config")
	}
}

func TestLoadEnvOverride(t *testing.T) {
	t.Setenv("SERVER_PORT", "9090")
	t.Setenv("DB_NAME", "custom_db")
	t.Setenv("REDIS_DATABASE", "2")
	t.Setenv("SYSTEM_API_PREFIX", "internal-api")
	t.Setenv("UPSTREAM_ALLOW_PRIVATE_NETWORKS", "true")
	t.Setenv("SERVER_TRUSTED_PROXIES", "127.0.0.1, 10.0.0.0/8,127.0.0.1")
	t.Setenv("ANALYTICS_ENABLED", "false")
	t.Setenv("ANALYTICS_BATCH_SIZE", "12000")
	t.Setenv("ANALYTICS_POLL_INTERVAL_MS", "750")

	cfg, err := Load("missing-config.yaml")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Server.Port != 9090 {
		t.Fatalf("expected env server port 9090, got %d", cfg.Server.Port)
	}
	if cfg.Database.Name != "custom_db" {
		t.Fatalf("expected custom db name, got %s", cfg.Database.Name)
	}
	if cfg.Redis.Database != 2 {
		t.Fatalf("expected redis db 2, got %d", cfg.Redis.Database)
	}
	if cfg.API.SystemPrefix != "/internal-api" {
		t.Fatalf("expected normalized system prefix /internal-api, got %s", cfg.API.SystemPrefix)
	}
	if !cfg.Network.AllowPrivateUpstreams {
		t.Fatal("expected private upstream env override to be enabled")
	}
	if cfg.Analytics.Enabled || cfg.Analytics.BatchSize != 12000 || cfg.Analytics.PollInterval != 750*time.Millisecond {
		t.Fatalf("analytics environment overrides were not applied: %#v", cfg.Analytics)
	}
	if len(cfg.Server.TrustedProxies) != 2 || cfg.Server.TrustedProxies[0] != "127.0.0.1" || cfg.Server.TrustedProxies[1] != "10.0.0.0/8" {
		t.Fatalf("unexpected trusted proxy list: %#v", cfg.Server.TrustedProxies)
	}
}

func TestLoadRuntimePoolEnvOverride(t *testing.T) {
	t.Setenv("SERVER_READ_TIMEOUT_SECONDS", "7")
	t.Setenv("SERVER_WRITE_TIMEOUT_SECONDS", "21")
	t.Setenv("SERVER_MAX_REQUEST_BODY_MB", "16")
	t.Setenv("DB_MAX_OPEN_CONNS", "150")
	t.Setenv("DB_MAX_IDLE_CONNS", "75")
	t.Setenv("DB_CONN_MAX_IDLE_MINUTES", "3")
	t.Setenv("REDIS_POOL_SIZE", "300")
	t.Setenv("REDIS_MIN_IDLE_CONNS", "40")
	t.Setenv("REDIS_POOL_TIMEOUT_MS", "2500")
	t.Setenv("REDIS_MAX_RETRIES", "3")

	cfg, err := Load("missing-config.yaml")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Server.ReadTimeout != 7*time.Second || cfg.Server.WriteTimeout != 21*time.Second {
		t.Fatalf("unexpected server timeouts: %#v", cfg.Server)
	}
	if cfg.Server.MaxRequestBodySize != 16*1024*1024 {
		t.Fatalf("MaxRequestBodySize = %d", cfg.Server.MaxRequestBodySize)
	}
	if cfg.Database.MaxOpenConns != 150 || cfg.Database.MaxIdleConns != 75 {
		t.Fatalf("unexpected db pool: %#v", cfg.Database)
	}
	if cfg.Database.ConnMaxIdleTime != 3*time.Minute {
		t.Fatalf("ConnMaxIdleTime = %s", cfg.Database.ConnMaxIdleTime)
	}
	if cfg.Redis.PoolSize != 300 || cfg.Redis.MinIdleConns != 40 || cfg.Redis.MaxRetries != 3 {
		t.Fatalf("unexpected redis pool: %#v", cfg.Redis)
	}
	if cfg.Redis.PoolTimeout != 2500*time.Millisecond {
		t.Fatalf("PoolTimeout = %s", cfg.Redis.PoolTimeout)
	}
}

func TestLoadInvalidEnv(t *testing.T) {
	t.Setenv("SERVER_PORT", "bad")

	_, err := Load("missing-config.yaml")
	if err == nil {
		t.Fatal("expected invalid SERVER_PORT error")
	}

	_ = os.Unsetenv("SERVER_PORT")
}

func TestLoadProductionRejectsDevelopmentSecrets(t *testing.T) {
	t.Setenv("APP_ENV", "production")

	_, err := Load("missing-config.yaml")
	if err == nil {
		t.Fatal("expected production config validation error")
	}
}

func TestLoadRejectsUnknownEnvironment(t *testing.T) {
	t.Setenv("APP_ENV", "prd")

	if _, err := Load("missing-config.yaml"); err == nil {
		t.Fatal("unknown APP_ENV bypassed production configuration validation")
	}
}

// TestLoadHardeningAppliesToEveryAcceptedEnvironment 防止 isProduction 的口径
// 再次与 isKnownEnvironment 分叉：在测试主机环境下提供不安全（占位 JWT 密钥 +
// 空库口令 + 非 Secure Cookie）配置，只有显式承认的开发/测试环境名可以放行，
// 其余任何被接受的环境名都必须被生产加固块拒绝。
//
// 这是 fail-closed 门控的回归护栏：若有人把 isProduction 改回「白名单 stage/
// staging/prod/production」，本用例会在 stage/production 之外的取值上先红。
func TestLoadHardeningAppliesToEveryAcceptedEnvironment(t *testing.T) {
	// 不安全基线：占位 JWT 密钥、root 库账号、空库口令、非 Secure Cookie、
	// 过短的支付/系统密钥。所有非开发环境都应因此被拒绝。
	unset := []string{
		"JWT_SECRET", "DB_USER", "DB_PASSWORD", "DB_NAME",
		"JWT_COOKIE_SECURE", "JWT_COOKIE_SAME_SITE",
		"PAYMENT_CONFIG_SECRET", "SYSTEM_CONFIG_SECRET", "SERVER_HOST",
	}
	for _, key := range unset {
		t.Setenv(key, "")
	}
	t.Setenv("DB_USER", "root")
	t.Setenv("DB_PASSWORD", "")
	t.Setenv("JWT_COOKIE_SECURE", "false")

	cases := []struct {
		env       string
		acceptDev bool
	}{
		{"dev", true},
		{"development", true},
		{"test", true},
		{"testing", true},
		{"stage", false},
		{"staging", false},
		{"prod", false},
		{"production", false},
	}

	for _, tc := range cases {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv("APP_ENV", tc.env)

			_, err := Load("missing-config.yaml")
			if tc.acceptDev {
				if err != nil {
					t.Fatalf("APP_ENV=%q should be exempt from production hardening, got error: %v", tc.env, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("APP_ENV=%q passed validation with placeholder secrets and root/empty database credentials: hardening gate is not fail-closed", tc.env)
			}
		})
	}
}

// TestIsProductionIsFailClosed 直接锁定判定方向：只有 dev/development/test/
// testing 是非生产，其余取值（含未来可能新增的白名单值）一律生产。
func TestIsProductionIsFailClosed(t *testing.T) {
	nonProduction := map[string]bool{"dev": true, "development": true, "test": true, "testing": true}
	for _, env := range []string{"dev", "development", "test", "testing", "stage", "staging", "prod", "production", "future-name"} {
		got := Config{App: AppConfig{Env: env}}.isProduction()
		if want := !nonProduction[env]; got != want {
			t.Errorf("isProduction(%q) = %v, want %v", env, got, want)
		}
	}
}

func TestLoadStagingRequiresProductionSecurity(t *testing.T) {
	t.Setenv("APP_ENV", "staging")

	if _, err := Load("missing-config.yaml"); err == nil {
		t.Fatal("staging accepted development secrets and insecure cookies")
	}
}

func TestLoadProductionAcceptsExplicitSecureConfig(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("JWT_SECRET", "prod-jwt-secret-with-at-least-thirty-two-bytes")
	t.Setenv("DB_USER", "bugpk")
	t.Setenv("DB_PASSWORD", "prod-db-password-not-root")
	t.Setenv("JWT_COOKIE_SECURE", "true")
	t.Setenv("PAYMENT_CONFIG_SECRET", "prod-payment-config-secret-32-bytes-minimum")

	cfg, err := Load("missing-config.yaml")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Auth.Secret == developmentJWTSecret {
		t.Fatal("expected production JWT secret override")
	}
	if !cfg.Auth.RefreshCookieSecure {
		t.Fatal("expected secure refresh cookie in production")
	}
}

func TestLoadRejectsUnsafeRefreshCookieSameSite(t *testing.T) {
	t.Setenv("JWT_COOKIE_SAME_SITE", "invalid")
	if _, err := Load("missing-config.yaml"); err == nil {
		t.Fatal("expected invalid SameSite value to fail")
	}

	t.Setenv("JWT_COOKIE_SAME_SITE", "None")
	t.Setenv("JWT_COOKIE_SECURE", "false")
	if _, err := Load("missing-config.yaml"); err == nil {
		t.Fatal("expected SameSite=None without Secure to fail")
	}
}

func TestLoadProductionRejectsCrossSiteRefreshCookie(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("JWT_SECRET", "prod-jwt-secret-with-at-least-thirty-two-bytes")
	t.Setenv("DB_USER", "bugpk")
	t.Setenv("DB_PASSWORD", "prod-db-password-not-root")
	t.Setenv("JWT_COOKIE_SECURE", "true")
	t.Setenv("JWT_COOKIE_SAME_SITE", "None")
	t.Setenv("PAYMENT_CONFIG_SECRET", "prod-payment-config-secret-32-bytes-minimum")
	if _, err := Load("missing-config.yaml"); err == nil {
		t.Fatal("expected production SameSite=None to fail without CSRF protocol")
	}
}

func TestLoadRejectsApiSystemPrefix(t *testing.T) {
	t.Setenv("SYSTEM_API_PREFIX", "/api")

	_, err := Load("missing-config.yaml")
	if err == nil {
		t.Fatal("expected /api system prefix validation error")
	}
}

func TestLoadRejectsAnySystemPrefixUnderOpenAPI(t *testing.T) {
	for _, prefix := range []string{"/api/system", "/API/auth", "/api//system"} {
		t.Run(prefix, func(t *testing.T) {
			t.Setenv("SYSTEM_API_PREFIX", prefix)
			if _, err := Load("missing-config.yaml"); err == nil {
				t.Fatalf("open gateway namespace accepted as system prefix: %q", prefix)
			}
		})
	}
}

func TestLoadRejectsMalformedSystemPrefix(t *testing.T) {
	for _, prefix := range []string{"/", "/system api", "/system-api?debug=1", "/system-api\\admin", "/system-api/../admin"} {
		t.Run(prefix, func(t *testing.T) {
			t.Setenv("SYSTEM_API_PREFIX", prefix)
			if _, err := Load("missing-config.yaml"); err == nil {
				t.Fatalf("malformed system prefix accepted: %q", prefix)
			}
		})
	}
}

func TestDatabaseDSNPreservesStrongCredentialsAndIPv6(t *testing.T) {
	cfg := DatabaseConfig{
		Host:     "2001:db8::10",
		Port:     3307,
		Name:     "bugpk api/production",
		User:     "bugpk-admin",
		Password: "p@ss:w/rd?#%&=+",
		SSL:      true,
	}

	parsed, err := mysqldriver.ParseDSN(cfg.DSN())
	if err != nil {
		t.Fatalf("ParseDSN() error = %v", err)
	}
	if parsed.Addr != "[2001:db8::10]:3307" {
		t.Fatalf("unexpected IPv6 address: %q", parsed.Addr)
	}
	if parsed.User != cfg.User || parsed.Passwd != cfg.Password || parsed.DBName != cfg.Name {
		t.Fatalf("DSN lost credentials or database name: user=%q password=%q database=%q", parsed.User, parsed.Passwd, parsed.DBName)
	}
	if !parsed.AllowNativePasswords || !parsed.ParseTime || !parsed.MultiStatements || parsed.TLSConfig != "true" {
		t.Fatalf("DSN lost required options: %#v", parsed)
	}
	if parsed.Loc == nil || parsed.Loc.String() != "Etc/GMT-8" {
		t.Fatalf("unexpected DSN location: %v", parsed.Loc)
	}
	if parsed.Timeout != 5*time.Second || parsed.ReadTimeout != 15*time.Second || parsed.WriteTimeout != 15*time.Second {
		t.Fatalf("unexpected MySQL network timeouts: dial=%s read=%s write=%s", parsed.Timeout, parsed.ReadTimeout, parsed.WriteTimeout)
	}
}

func TestLoadDatabaseNetworkTimeoutOverrides(t *testing.T) {
	t.Setenv("DB_DIAL_TIMEOUT_SECONDS", "3")
	t.Setenv("DB_READ_TIMEOUT_SECONDS", "11")
	t.Setenv("DB_WRITE_TIMEOUT_SECONDS", "12")
	cfg, err := Load("missing-config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.DialTimeout != 3*time.Second || cfg.Database.ReadTimeout != 11*time.Second || cfg.Database.WriteTimeout != 12*time.Second {
		t.Fatalf("database timeout overrides were not loaded: %+v", cfg.Database)
	}
	parsed, err := mysqldriver.ParseDSN(cfg.Database.DSN())
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Timeout != 3*time.Second || parsed.ReadTimeout != 11*time.Second || parsed.WriteTimeout != 12*time.Second {
		t.Fatalf("database timeout overrides were not applied to DSN: %#v", parsed)
	}
}

func TestLoadRejectsUnsafeDatabaseNetworkTimeout(t *testing.T) {
	t.Setenv("DB_READ_TIMEOUT_SECONDS", "61")
	if _, err := Load("missing-config.yaml"); err == nil {
		t.Fatal("excessive MySQL read timeout bypassed validation")
	}
}

func TestDatabaseQueryTimeoutDefaultAndBounds(t *testing.T) {
	cfg := Default()
	if cfg.Database.QueryTimeout != 15*time.Second {
		t.Fatalf("QueryTimeout default = %s, want 15s", cfg.Database.QueryTimeout)
	}
	cfg.Database.QueryTimeout = 90 * time.Second
	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected validation error for oversized queryTimeout")
	}
}

func TestValidateRequiresDatabaseAndRedisEndpoints(t *testing.T) {
	tests := map[string]func(*Config){
		"database host": func(cfg *Config) { cfg.Database.Host = " " },
		"database name": func(cfg *Config) { cfg.Database.Name = " " },
		"database user": func(cfg *Config) { cfg.Database.User = " " },
		"redis host":    func(cfg *Config) { cfg.Redis.Host = " " },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := Default()
			mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("expected missing endpoint field to fail validation")
			}
		})
	}
}

func TestValidateRejectsInvalidIPRegionConfiguration(t *testing.T) {
	tests := map[string]func(*Config){
		"unknown provider": func(cfg *Config) { cfg.IPRegion.Provider = "unknown" },
		"short timeout":    func(cfg *Config) { cfg.IPRegion.Timeout = 50 * time.Millisecond },
		"long cache ttl":   func(cfg *Config) { cfg.IPRegion.CacheTTL = 31 * 24 * time.Hour },
		"http endpoint": func(cfg *Config) {
			cfg.IPRegion.Provider = "bilibili"
			cfg.IPRegion.BilibiliEndpoint = "http://api.example.com/ip"
		},
		"endpoint userinfo": func(cfg *Config) {
			cfg.IPRegion.Provider = "bilibili"
			cfg.IPRegion.BilibiliEndpoint = "https://user:password@api.example.com/ip"
		},
		"ip-api bad endpoint": func(cfg *Config) {
			cfg.IPRegion.Provider = "ip-api"
			cfg.IPRegion.IPApiEndpoint = "not-a-url"
		},
		"ip-api endpoint userinfo": func(cfg *Config) {
			cfg.IPRegion.Provider = "ip-api"
			cfg.IPRegion.IPApiEndpoint = "http://user:password@api.example.com/{ip}"
		},
		"ip-api missing placeholder": func(cfg *Config) {
			cfg.IPRegion.Provider = "ip-api"
			cfg.IPRegion.IPApiEndpoint = "https://api.example.com/ip"
		},
		"tencent key": func(cfg *Config) {
			cfg.IPRegion.Provider = "tencent-map"
			cfg.IPRegion.TencentMapKey = ""
		},
		"amap key": func(cfg *Config) {
			cfg.IPRegion.Provider = "amap"
			cfg.IPRegion.AmapKey = ""
		},
		"ip2region paths": func(cfg *Config) {
			cfg.IPRegion.Provider = "ip2region"
			cfg.IPRegion.IP2RegionV6Path = ""
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := Default()
			mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("expected invalid IP region configuration to fail")
			}
		})
	}
}

func TestValidateAcceptsConfiguredIPRegionProviders(t *testing.T) {
	for _, provider := range []string{"ip-api", "bilibili", "tencent-map", "amap", "ip2region"} {
		t.Run(provider, func(t *testing.T) {
			cfg := Default()
			cfg.IPRegion.Provider = provider
			cfg.IPRegion.TencentMapKey = "tencent-test-key"
			cfg.IPRegion.AmapKey = "amap-test-key"
			if err := cfg.Validate(); err != nil {
				t.Fatalf("valid provider rejected: %v", err)
			}
		})
	}
}

func TestLoadRejectsInvalidTrustedProxy(t *testing.T) {
	t.Setenv("SERVER_TRUSTED_PROXIES", "not-an-ip")
	if _, err := Load("missing-config.yaml"); err == nil {
		t.Fatal("expected invalid trusted proxy validation error")
	}
}

func TestLoadRejectsTrustedProxyEntireAddressFamily(t *testing.T) {
	for _, value := range []string{"0.0.0.0/0", "::/0"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("SERVER_TRUSTED_PROXIES", value)
			if _, err := Load("missing-config.yaml"); err == nil {
				t.Fatalf("expected unsafe trusted proxy CIDR %q to fail", value)
			}
		})
	}
}

func TestValidateRejectsResourceExhaustionConfiguration(t *testing.T) {
	tests := map[string]func(*Config){
		"request body":         func(cfg *Config) { cfg.Server.MaxRequestBodySize = 65 * 1024 * 1024 },
		"server port":          func(cfg *Config) { cfg.Server.Port = 70000 },
		"database pool":        func(cfg *Config) { cfg.Database.MaxOpenConns = 2001 },
		"database idle":        func(cfg *Config) { cfg.Database.MaxIdleConns = cfg.Database.MaxOpenConns + 1 },
		"analytics batch low":  func(cfg *Config) { cfg.Analytics.BatchSize = 99 },
		"analytics batch high": func(cfg *Config) { cfg.Analytics.BatchSize = analytics.MaxBatchSize + 1 },
		"analytics poll":       func(cfg *Config) { cfg.Analytics.PollInterval = 50 * time.Millisecond },
		"redis pool":           func(cfg *Config) { cfg.Redis.PoolSize = 5001 },
		"redis idle":           func(cfg *Config) { cfg.Redis.MinIdleConns = cfg.Redis.PoolSize + 1 },
		"redis retries":        func(cfg *Config) { cfg.Redis.MaxRetries = 11 },
		"read timeout":         func(cfg *Config) { cfg.Server.ReadTimeout = 6 * time.Minute },
		"shutdown timeout":     func(cfg *Config) { cfg.Server.ShutdownTimeout = 301 * time.Second },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := Default()
			mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("expected resource bound validation error")
			}
		})
	}
}

func TestValidateAcceptsAnalyticsBatchBounds(t *testing.T) {
	for _, batchSize := range []int{100, analytics.MaxBatchSize} {
		t.Run(fmt.Sprintf("batch-%d", batchSize), func(t *testing.T) {
			cfg := Default()
			cfg.Analytics.BatchSize = batchSize
			if err := cfg.Validate(); err != nil {
				t.Fatalf("valid analytics batch size %d rejected: %v", batchSize, err)
			}
		})
	}
}

func TestLoadDoesNotSilentlyClampInvalidPoolRelationships(t *testing.T) {
	t.Setenv("DB_MAX_OPEN_CONNS", "10")
	t.Setenv("DB_MAX_IDLE_CONNS", "11")
	if _, err := Load("missing-config.yaml"); err == nil {
		t.Fatal("expected invalid database pool relationship to fail")
	}

	t.Setenv("DB_MAX_IDLE_CONNS", "5")
	t.Setenv("REDIS_POOL_SIZE", "10")
	t.Setenv("REDIS_MIN_IDLE_CONNS", "11")
	if _, err := Load("missing-config.yaml"); err == nil {
		t.Fatal("expected invalid Redis pool relationship to fail")
	}
}

func TestDefaultConfigPathUsesExeDir(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("app:\n  name: BugPk-Api\n"), 0o600); err != nil {
		t.Fatalf("failed to create config.yaml: %v", err)
	}

	fakeExe := filepath.Join(dir, "bugpk-api.exe")
	got := defaultConfigPath(fakeExe)
	if got != configPath {
		t.Fatalf("defaultConfigPath(%q) = %q, want %q", fakeExe, got, configPath)
	}
}

func TestDefaultConfigPathFallsBackToConfigs(t *testing.T) {
	dir := t.TempDir()

	fakeExe := filepath.Join(dir, "bugpk-api.exe")
	got := defaultConfigPath(fakeExe)
	want := filepath.Join("configs", "config.yaml")
	if got != want {
		t.Fatalf("defaultConfigPath(%q) = %q, want %q", fakeExe, got, want)
	}
}
