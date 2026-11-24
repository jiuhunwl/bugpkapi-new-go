package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"bugpk-api/internal/domain/analytics"
	"bugpk-api/internal/pkg/httpclient"

	"bugpk-api/internal/pkg/businessclock"

	mysqldriver "github.com/go-sql-driver/mysql"
	"gopkg.in/yaml.v3"
)

const developmentJWTSecret = "change-me-dev-only-bugpk-api-jwt-secret-32-bytes"

type Config struct {
	App       AppConfig       `yaml:"app"`
	Server    ServerConfig    `yaml:"server"`
	API       APIConfig       `yaml:"api"`
	Frontend  FrontendConfig  `yaml:"frontend"`
	Database  DatabaseConfig  `yaml:"database"`
	Analytics AnalyticsConfig `yaml:"analytics"`
	Redis     RedisConfig     `yaml:"redis"`
	Migration MigrationConfig `yaml:"migration"`
	Auth      AuthConfig      `yaml:"auth"`
	OAuth     OAuthConfig     `yaml:"oauth"`
	Captcha   CaptchaConfig   `yaml:"captcha"`
	Message   MessageConfig   `yaml:"message"`
	Network   NetworkConfig   `yaml:"network"`
	IPRegion  IPRegionConfig  `yaml:"ipRegion"`
	Payment   PaymentConfig   `yaml:"payment"`
	System    SystemConfig    `yaml:"system"`
	CORS      CORSConfig      `yaml:"cors"`
	SEO       SEOConfig       `yaml:"seo"`
}

// SEOConfig 控制 SEO 相关能力（动态 sitemap 等）。
// SiteURL 是站点对外基础地址（如 https://www.example.com，不含结尾斜杠），
// 用于生成 sitemap 的绝对 URL；留空时 sitemap 接口回退使用请求 Host。
type SEOConfig struct {
	SiteURL string `yaml:"siteUrl"`
}

type AppConfig struct {
	Name string `yaml:"name"`
	Env  string `yaml:"env"`
}

type ServerConfig struct {
	Host               string        `yaml:"host"`
	IdleTimeout        time.Duration `yaml:"idleTimeout"`
	MaxRequestBodySize int           `yaml:"maxRequestBodySize"`
	Port               int           `yaml:"port"`
	ReadTimeout        time.Duration `yaml:"readTimeout"`
	ShutdownTimeout    time.Duration `yaml:"shutdownTimeout"`
	TrustedProxies     []string      `yaml:"trustedProxies"`
	WriteTimeout       time.Duration `yaml:"writeTimeout"`
}

func (c ServerConfig) Addr() string {
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}

type APIConfig struct {
	SystemPrefix string `yaml:"systemPrefix"`
}

// FrontendConfig controls the optional built-in SPA/static-file server.
// When enabled, the Go service serves frontend/dist itself and can be exposed
// directly without path-specific Nginx API proxy rules.
type FrontendConfig struct {
	Enabled  bool   `yaml:"enabled"`
	DistPath string `yaml:"distPath"`
}

type DatabaseConfig struct {
	Host                     string        `yaml:"host"`
	Port                     int           `yaml:"port"`
	Name                     string        `yaml:"name"`
	User                     string        `yaml:"user"`
	Password                 string        `yaml:"password"`
	SSL                      bool          `yaml:"ssl"`
	TimezoneDataConfirmation string        `yaml:"timezoneDataConfirmation"`
	ConnMaxIdleTime          time.Duration `yaml:"connMaxIdleTime"`
	MaxOpenConns             int           `yaml:"maxOpenConns"`
	MaxIdleConns             int           `yaml:"maxIdleConns"`
	ConnMaxLifetime          time.Duration `yaml:"connMaxLifetime"`
	DialTimeout              time.Duration `yaml:"dialTimeout"`
	ReadTimeout              time.Duration `yaml:"readTimeout"`
	WriteTimeout             time.Duration `yaml:"writeTimeout"`
	QueryTimeout             time.Duration `yaml:"queryTimeout"`
}

// AnalyticsConfig controls the bounded incremental rollup pipeline used by
// dashboards. Raw call logs remain the source of truth for audit/detail pages;
// dashboard queries read compact time buckets so their cost does not grow with
// the lifetime row count of bp_interface_call_log.
type AnalyticsConfig struct {
	Enabled                       bool          `yaml:"enabled"`
	BatchSize                     int           `yaml:"batchSize"`
	PollInterval                  time.Duration `yaml:"pollInterval"`
	OverviewRefreshInterval       time.Duration `yaml:"overviewRefreshInterval"`
	OverviewSnapshotWindowMinutes int           `yaml:"overviewSnapshotWindowMinutes"`
	// IPHashSecret 为落库 IP 去标识化（汇总/风控/审计哈希）的按环境密钥。
	// 空值保持与历史公开字面前缀逐字节一致（零行为变化）；配置后即轮换
	// 分组键，需按交付说明重建受影响的三张汇总表。也可用环境变量
	// ANALYTICS_IP_HASH_SECRET 覆盖。
	IPHashSecret string `yaml:"ipHashSecret"`
}

func (c DatabaseConfig) DSN() string {
	tlsConfig := "false"
	if c.SSL {
		tlsConfig = "true"
	}

	dsnConfig := mysqldriver.Config{
		User:   c.User,
		Passwd: c.Password,
		Net:    "tcp",
		Addr:   net.JoinHostPort(c.Host, strconv.Itoa(c.Port)),
		DBName: c.Name,
		// MySQL civil-time functions must match the DATETIME decoder below.
		// Driver params are applied to every new/recreated physical connection.
		Params:               map[string]string{"charset": "utf8mb4", "time_zone": "'" + businessclock.UTCOffset + "'"},
		AllowNativePasswords: true,
		ParseTime:            true,
		Loc:                  businessclock.Location(),
		MultiStatements:      true,
		TLSConfig:            tlsConfig,
		Timeout:              durationOrDefault(c.DialTimeout, 5*time.Second),
		ReadTimeout:          durationOrDefault(c.ReadTimeout, 15*time.Second),
		WriteTimeout:         durationOrDefault(c.WriteTimeout, 15*time.Second),
	}
	return dsnConfig.FormatDSN()
}

type RedisConfig struct {
	Database     int           `yaml:"database"`
	Host         string        `yaml:"host"`
	MaxRetries   int           `yaml:"maxRetries"`
	MinIdleConns int           `yaml:"minIdleConns"`
	Password     string        `yaml:"password"`
	PoolSize     int           `yaml:"poolSize"`
	PoolTimeout  time.Duration `yaml:"poolTimeout"`
	Port         int           `yaml:"port"`
	DialTimeout  time.Duration `yaml:"dialTimeout"`
	ReadTimeout  time.Duration `yaml:"readTimeout"`
	WriteTimeout time.Duration `yaml:"writeTimeout"`
}

func (c RedisConfig) Addr() string {
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}

type MigrationConfig struct {
	// Enabled 控制启动时是否自动执行 golang-migrate 迁移。
	// 默认 true（保持既有行为）；手动导入全量 SQL 的部署请设 false，防止重复建表。
	Enabled bool   `yaml:"enabled"`
	Path    string `yaml:"path"`
}

type AuthConfig struct {
	Issuer               string        `yaml:"issuer"`
	Secret               string        `yaml:"secret"`
	InitialAdminPassword string        `yaml:"initialAdminPassword"`
	AccessTokenTTL       time.Duration `yaml:"accessTokenTTL"`
	RefreshTokenTTL      time.Duration `yaml:"refreshTokenTTL"`
	// RefreshRotationGrace 是 refresh token 轮换后的并发宽限窗口。多个标签页
	// 几乎同时续期时，窗口内重复使用同一 refresh token 只补发 access token，
	// 不再二次轮换；窗口外的重复消费仍按 401 处理。0 表示关闭宽限。
	RefreshRotationGrace  time.Duration `yaml:"refreshRotationGrace"`
	RefreshCookieName     string        `yaml:"refreshCookieName"`
	RefreshCookieSecure   bool          `yaml:"refreshCookieSecure"`
	RefreshCookieSameSite string        `yaml:"refreshCookieSameSite"`
	// TwoFAChallengeTTL 是密码校验通过后、等待输入 TOTP 验证码的
	// 一次性 challenge token 有效期。默认 5 分钟。
	TwoFAChallengeTTL time.Duration `yaml:"twoFAChallengeTTL"`
	// TwoFAPendingTTL 是 2FA 绑定流程中「已生成密钥、等待首次验证确认」
	// 的 Redis 暂存有效期。默认 10 分钟。
	TwoFAPendingTTL time.Duration `yaml:"twoFAPendingTTL"`
	// PasswordResetAccountDayLimit 限制同一账号每天最多触发多少次
	// 「发送找回密码邮件」。0 = 使用默认值 5。防止攻击者对管理员账号
	// 反复触发重置邮件（骚扰 + 碰撞验证码）。
	PasswordResetAccountDayLimit int `yaml:"passwordResetAccountDayLimit"`
}

type OAuthConfig struct {
	// AllowPrivateEndpoints is only for local integration tests. Production
	// providers must resolve to public addresses.
	AllowPrivateEndpoints bool          `yaml:"allowPrivateEndpoints"`
	HTTPTimeout           time.Duration `yaml:"httpTimeout"`
}

type CaptchaConfig struct {
	DefaultProvider string        `yaml:"defaultProvider"`
	TTL             time.Duration `yaml:"ttl"`
	PowDifficulty   int           `yaml:"powDifficulty"`
}

type MessageConfig struct {
	Email EmailMessageConfig `yaml:"email"`
	SMS   SMSMessageConfig   `yaml:"sms"`
}

type EmailMessageConfig struct {
	Provider      string        `yaml:"provider"`
	Enabled       bool          `yaml:"enabled"`
	CodeTTL       time.Duration `yaml:"codeTTL"`
	SMTPHost      string        `yaml:"smtpHost"`
	SMTPPort      int           `yaml:"smtpPort"`
	SMTPUsername  string        `yaml:"smtpUsername"`
	SMTPPassword  string        `yaml:"smtpPassword"`
	SMTPFrom      string        `yaml:"smtpFrom"`
	SMTPFromName  string        `yaml:"smtpFromName"`
	SMTPTLS       bool          `yaml:"smtpTLS"`
	SMTPTimeout   time.Duration `yaml:"smtpTimeout"`
	CodeSubject   string        `yaml:"codeSubject"`
	CodeTemplate  string        `yaml:"codeTemplate"`
	ResetSubject  string        `yaml:"resetSubject"`
	ResetTemplate string        `yaml:"resetTemplate"`
	// 英文模板：邮件语言跟随浏览器 Accept-Language（en → 英文模板，英文模板为空时
	// 回退默认模板；zh/其它/未携带 → 默认模板）。占位符与默认模板一致
	// （{{code}}/{{minutes}}/{{siteName}}）。
	CodeSubjectEn   string `yaml:"codeSubjectEn"`
	CodeTemplateEn  string `yaml:"codeTemplateEn"`
	ResetSubjectEn  string `yaml:"resetSubjectEn"`
	ResetTemplateEn string `yaml:"resetTemplateEn"`
	// 全站发信熔断：所有验证码邮件（注册/绑定/找回密码）共用一个计数器，
	// 防止攻击者用海量不同邮箱把 SMTP 账号的发信配额打爆导致正常用户收不到信。
	// 0 表示使用内置默认值（30/分钟、300/小时）。
	SendGlobalMinuteLimit int `yaml:"sendGlobalMinuteLimit"`
	SendGlobalHourLimit   int `yaml:"sendGlobalHourLimit"`
}

type SMSMessageConfig struct {
	Provider string        `yaml:"provider"`
	Enabled  bool          `yaml:"enabled"`
	CodeTTL  time.Duration `yaml:"codeTTL"`
}

type NetworkConfig struct {
	AllowPrivateUpstreams bool   `yaml:"allowPrivateUpstreams"`
	ProxyTestURL          string `yaml:"proxyTestUrl"`
}

type IPRegionConfig struct {
	Enabled                  bool          `yaml:"enabled"`
	Provider                 string        `yaml:"provider"`
	Timeout                  time.Duration `yaml:"timeout"`
	CacheTTL                 time.Duration `yaml:"cacheTTL"`
	IP2RegionV4Path          string        `yaml:"ip2regionV4Path"`
	IP2RegionV6Path          string        `yaml:"ip2regionV6Path"`
	IPApiEndpoint            string        `yaml:"ipApiEndpoint"`
	BilibiliEndpoint         string        `yaml:"bilibiliEndpoint"`
	TencentMapKey            string        `yaml:"tencentMapKey"`
	TencentMapEndpoint       string        `yaml:"tencentMapEndpoint"`
	AmapKey                  string        `yaml:"amapKey"`
	AmapEndpoint             string        `yaml:"amapEndpoint"`
	IgnoreProviderErrorInLog bool          `yaml:"ignoreProviderErrorInLog"`
}

type PaymentConfig struct {
	ConfigSecret string `yaml:"configSecret"`
}

type SystemConfig struct {
	ConfigSecret string `yaml:"configSecret"`
}

// CORSConfig 控制明确允许跨源的非会话接口响应头；它不解除刷新/退出的同源校验。
// AllowedOrigins 为空（默认）表示仅同域部署：前端与后端同域或经 Nginx 反代访问，
// 后端不注册 CORS 中间件，对正常请求零影响。
type CORSConfig struct {
	// AllowedOrigins 是允许跨域访问的来源列表（完整 Origin，如 https://www.example.com）。
	// 可包含 "*"（允许所有来源，不推荐生产使用）；留空=仅同域。
	AllowedOrigins []string `yaml:"allowed_origins"`
}

func Default() Config {
	return Config{
		App: AppConfig{
			Name: "BugPk-Api",
			Env:  "development",
		},
		Server: ServerConfig{
			Host:               "0.0.0.0",
			IdleTimeout:        120 * time.Second,
			MaxRequestBodySize: 4 * 1024 * 1024,
			Port:               8080,
			ReadTimeout:        10 * time.Second,
			ShutdownTimeout:    30 * time.Second,
			TrustedProxies:     nil,
			WriteTimeout:       30 * time.Second,
		},
		API: APIConfig{
			SystemPrefix: "/system-api",
		},
		Frontend: FrontendConfig{
			Enabled:  true,
			DistPath: "frontend/dist",
		},
		Database: DatabaseConfig{
			Host:            "localhost",
			Port:            3306,
			Name:            "bugpk_api",
			User:            "root",
			Password:        "root",
			SSL:             false,
			ConnMaxIdleTime: 5 * time.Minute,
			MaxOpenConns:    100,
			MaxIdleConns:    50,
			ConnMaxLifetime: 30 * time.Minute,
			DialTimeout:     5 * time.Second,
			ReadTimeout:     15 * time.Second,
			WriteTimeout:    15 * time.Second,
			QueryTimeout:    15 * time.Second,
		},
		Analytics: AnalyticsConfig{
			Enabled:                       true,
			BatchSize:                     5000,
			PollInterval:                  time.Second,
			OverviewRefreshInterval:       5 * time.Minute,
			OverviewSnapshotWindowMinutes: 60,
		},
		Redis: RedisConfig{
			Database:     0,
			Host:         "localhost",
			MaxRetries:   2,
			MinIdleConns: 32,
			Password:     "",
			PoolSize:     256,
			PoolTimeout:  2 * time.Second,
			Port:         6379,
			DialTimeout:  2 * time.Second,
			ReadTimeout:  2 * time.Second,
			WriteTimeout: 2 * time.Second,
		},
		Migration: MigrationConfig{
			Enabled: true,
			Path:    "migrations",
		},
		Auth: AuthConfig{
			Issuer:                       "BugPk-Api",
			Secret:                       developmentJWTSecret,
			AccessTokenTTL:               30 * time.Minute,
			RefreshTokenTTL:              7 * 24 * time.Hour,
			RefreshRotationGrace:         60 * time.Second,
			RefreshCookieName:            "REFRESH_TOKEN",
			RefreshCookieSecure:          false,
			RefreshCookieSameSite:        "Lax",
			TwoFAChallengeTTL:            5 * time.Minute,
			TwoFAPendingTTL:              10 * time.Minute,
			PasswordResetAccountDayLimit: 5,
		},
		OAuth: OAuthConfig{
			AllowPrivateEndpoints: false,
			HTTPTimeout:           10 * time.Second,
		},
		Captcha: CaptchaConfig{
			DefaultProvider: "pow",
			TTL:             5 * time.Minute,
			PowDifficulty:   3,
		},
		Message: MessageConfig{
			Email: EmailMessageConfig{
				Provider:      "smtp",
				Enabled:       false,
				CodeTTL:       10 * time.Minute,
				SMTPHost:      "",
				SMTPPort:      587,
				SMTPUsername:  "",
				SMTPPassword:  "",
				SMTPFrom:      "",
				SMTPFromName:  "BugPk-Api",
				SMTPTLS:       true,
				SMTPTimeout:   10 * time.Second,
				CodeSubject:   "BugPk-Api 验证码",
				CodeTemplate:  "您的验证码是 {{code}}，{{minutes}} 分钟内有效。如非本人操作，请忽略本邮件。",
				ResetSubject:  "BugPk-Api 找回密码验证码",
				ResetTemplate: "您正在找回密码，验证码是 {{code}}，{{minutes}} 分钟内有效。如非本人操作，请忽略本邮件。",
				// 英文模板默认值：en 浏览器收到英文邮件，不配置时回退上面中文默认模板。
				CodeSubjectEn:   "BugPk-Api verification code",
				CodeTemplateEn:  "Your verification code is {{code}} and is valid for {{minutes}} minutes. Ignore this email if you did not request it.",
				ResetSubjectEn:  "BugPk-Api password reset code",
				ResetTemplateEn: "Your password reset code is {{code}} and is valid for {{minutes}} minutes. Ignore this email if you did not request it.",
				// 全站发信熔断默认值：单分钟 30 封、单小时 300 封。
				SendGlobalMinuteLimit: 30,
				SendGlobalHourLimit:   300,
			},
			SMS: SMSMessageConfig{
				Provider: "",
				Enabled:  false,
				CodeTTL:  10 * time.Minute,
			},
		},
		Network: NetworkConfig{
			AllowPrivateUpstreams: false,
			ProxyTestURL:          "https://api.ipify.org?format=json",
		},
		IPRegion: IPRegionConfig{
			Enabled:                  true,
			Provider:                 "ip-api",
			Timeout:                  5 * time.Second,
			CacheTTL:                 24 * time.Hour,
			IP2RegionV4Path:          filepath.Join("data", "ip2region_v4.xdb"),
			IP2RegionV6Path:          filepath.Join("data", "ip2region_v6.xdb"),
			IPApiEndpoint:            "https://ipwho.is/{ip}?lang=zh-CN",
			BilibiliEndpoint:         "https://api.live.bilibili.com/ip_service/v1/ip_service/get_ip_addr",
			TencentMapEndpoint:       "https://apis.map.qq.com/ws/location/v1/ip",
			AmapEndpoint:             "https://restapi.amap.com/v3/ip",
			IgnoreProviderErrorInLog: true,
		},
		Payment: PaymentConfig{
			ConfigSecret: "",
		},
		System: SystemConfig{
			ConfigSecret: "",
		},
		CORS: CORSConfig{
			// 默认仅同域部署（前端与后端同域/经 Nginx 反代），不启用跨域；
			// 非会话接口确有跨源需求时通过 config.yaml 的 cors.allowed_origins 或
			// 环境变量 CORS_ALLOWED_ORIGINS 配置允许的前端来源。
			AllowedOrigins: nil,
		},
		SEO: SEOConfig{
			// 站点对外基础地址（用于 sitemap 绝对 URL）；留空时 sitemap 接口回退请求 Host。
			// 生产部署建议在 .env 设置 SEO_SITE_URL=https://你的域名
			SiteURL: "",
		},
	}
}

// defaultConfigPath 返回默认配置文件路径：
//  1. 优先：可执行文件同目录的 config.yaml（生产平铺部署，宝塔直接填二进制即可）
//  2. 兜底：当前工作目录的 configs/config.yaml（开发 go run 场景）
//
// exePaths 为可选参数，仅用于单元测试注入假的可执行文件路径，避免依赖真实的 os.Executable。
func defaultConfigPath(exePaths ...string) string {
	exePath := ""
	if len(exePaths) > 0 {
		exePath = exePaths[0]
	} else if exe, err := os.Executable(); err == nil {
		exePath = exe
	}
	if exePath != "" {
		p := filepath.Join(filepath.Dir(exePath), "config.yaml")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return filepath.Join("configs", "config.yaml")
}

func Load(path string) (Config, error) {
	// 先自动加载同目录 .env（宝塔「Go 项目」等直接 exec 二进制时进程环境变量为空，
	// 不依赖 start.sh source；已存在的环境变量不会被覆盖）
	if err := loadDotEnv(); err != nil {
		return Config{}, err
	}

	cfg := Default()

	if path == "" {
		path = defaultConfigPath()
	}

	if data, err := os.ReadFile(path); err == nil {
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return Config{}, fmt.Errorf("load config file %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return Config{}, fmt.Errorf("read config file %s: %w", path, err)
	}

	if err := applyEnv(&cfg); err != nil {
		return Config{}, err
	}

	if cfg.Migration.Path == "" {
		cfg.Migration.Path = "migrations"
	}

	normalizeRuntime(&cfg)

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func (c Config) Validate() error {
	if !isKnownEnvironment(c.App.Env) {
		return fmt.Errorf("APP_ENV must be one of development, test, staging, or production")
	}
	if err := validateSystemPrefix(c.API.SystemPrefix); err != nil {
		return err
	}
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fmt.Errorf("SERVER_PORT must be between 1 and 65535")
	}
	if c.Server.MaxRequestBodySize < 3*1024*1024 || c.Server.MaxRequestBodySize > 64*1024*1024 {
		return fmt.Errorf("SERVER_MAX_REQUEST_BODY_MB must be between 3 and 64")
	}
	if c.Server.ReadTimeout < time.Second || c.Server.ReadTimeout > 5*time.Minute {
		return fmt.Errorf("SERVER_READ_TIMEOUT_SECONDS must be between 1 and 300")
	}
	if c.Server.WriteTimeout < time.Second || c.Server.WriteTimeout > 10*time.Minute {
		return fmt.Errorf("SERVER_WRITE_TIMEOUT_SECONDS must be between 1 and 600")
	}
	if c.Server.IdleTimeout < 5*time.Second || c.Server.IdleTimeout > 30*time.Minute {
		return fmt.Errorf("SERVER_IDLE_TIMEOUT_SECONDS must be between 5 and 1800")
	}
	if c.Server.ShutdownTimeout < 5*time.Second || c.Server.ShutdownTimeout > 5*time.Minute {
		return fmt.Errorf("SERVER_SHUTDOWN_TIMEOUT_SECONDS must be between 5 and 300")
	}
	if len(c.Server.TrustedProxies) > 64 {
		return fmt.Errorf("SERVER_TRUSTED_PROXIES cannot contain more than 64 entries")
	}
	for _, value := range c.Server.TrustedProxies {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if strings.Contains(value, "/") {
			_, network, err := net.ParseCIDR(value)
			if err != nil {
				return fmt.Errorf("invalid trusted proxy CIDR %q", value)
			}
			ones, _ := network.Mask.Size()
			if ones == 0 {
				return fmt.Errorf("trusted proxy CIDR %q cannot trust an entire address family", value)
			}
			continue
		}
		if net.ParseIP(value) == nil {
			return fmt.Errorf("invalid trusted proxy IP %q", value)
		}
	}
	if c.Database.Port < 1 || c.Database.Port > 65535 {
		return fmt.Errorf("DB_PORT must be between 1 and 65535")
	}
	if strings.TrimSpace(c.Database.Host) == "" {
		return fmt.Errorf("DB_HOST is required")
	}
	if strings.TrimSpace(c.Database.Name) == "" {
		return fmt.Errorf("DB_NAME is required")
	}
	if strings.TrimSpace(c.Database.User) == "" {
		return fmt.Errorf("DB_USER is required")
	}
	if c.Database.MaxOpenConns < 1 || c.Database.MaxOpenConns > 2000 {
		return fmt.Errorf("DB_MAX_OPEN_CONNS must be between 1 and 2000")
	}
	if c.Database.MaxIdleConns < 0 || c.Database.MaxIdleConns > c.Database.MaxOpenConns {
		return fmt.Errorf("DB_MAX_IDLE_CONNS must be between 0 and DB_MAX_OPEN_CONNS")
	}
	if c.Database.ConnMaxLifetime < time.Minute || c.Database.ConnMaxLifetime > 24*time.Hour {
		return fmt.Errorf("DB_CONN_MAX_LIFETIME_MINUTES must be between 1 and 1440")
	}
	if c.Database.ConnMaxIdleTime < time.Minute || c.Database.ConnMaxIdleTime > time.Hour {
		return fmt.Errorf("DB_CONN_MAX_IDLE_MINUTES must be between 1 and 60")
	}
	for name, value := range map[string]time.Duration{
		"DB_DIAL_TIMEOUT_SECONDS":  c.Database.DialTimeout,
		"DB_READ_TIMEOUT_SECONDS":  c.Database.ReadTimeout,
		"DB_WRITE_TIMEOUT_SECONDS": c.Database.WriteTimeout,
	} {
		if value < time.Second || value > time.Minute {
			return fmt.Errorf("%s must be between 1 and 60", name)
		}
	}
	if c.Database.QueryTimeout < 0 || c.Database.QueryTimeout > time.Minute {
		return fmt.Errorf("database queryTimeout must be between 0 and 60s")
	}
	if c.Analytics.BatchSize < 100 || c.Analytics.BatchSize > analytics.MaxBatchSize {
		return fmt.Errorf("ANALYTICS_BATCH_SIZE must be between 100 and %d", analytics.MaxBatchSize)
	}
	if c.Analytics.PollInterval < 100*time.Millisecond || c.Analytics.PollInterval > time.Minute {
		return fmt.Errorf("ANALYTICS_POLL_INTERVAL_MS must be between 100 and 60000")
	}
	if c.Analytics.OverviewRefreshInterval < time.Minute || c.Analytics.OverviewRefreshInterval > time.Hour {
		return fmt.Errorf("ANALYTICS_OVERVIEW_REFRESH_INTERVAL_MINUTES must be between 1 and 60")
	}
	if c.Analytics.OverviewSnapshotWindowMinutes < 5 || c.Analytics.OverviewSnapshotWindowMinutes > 1440 {
		return fmt.Errorf("ANALYTICS_OVERVIEW_SNAPSHOT_WINDOW_MINUTES must be between 5 and 1440")
	}
	if c.Redis.Port < 1 || c.Redis.Port > 65535 {
		return fmt.Errorf("REDIS_PORT must be between 1 and 65535")
	}
	if strings.TrimSpace(c.Redis.Host) == "" {
		return fmt.Errorf("REDIS_HOST is required")
	}
	if c.Redis.Database < 0 || c.Redis.Database > 1023 {
		return fmt.Errorf("REDIS_DATABASE must be between 0 and 1023")
	}
	if c.Redis.PoolSize < 1 || c.Redis.PoolSize > 5000 {
		return fmt.Errorf("REDIS_POOL_SIZE must be between 1 and 5000")
	}
	if c.Redis.MinIdleConns < 0 || c.Redis.MinIdleConns > c.Redis.PoolSize {
		return fmt.Errorf("REDIS_MIN_IDLE_CONNS must be between 0 and REDIS_POOL_SIZE")
	}
	if c.Redis.MaxRetries < -1 || c.Redis.MaxRetries > 10 {
		return fmt.Errorf("REDIS_MAX_RETRIES must be between -1 and 10")
	}
	for name, value := range map[string]time.Duration{
		"REDIS_POOL_TIMEOUT_MS": c.Redis.PoolTimeout,
		"redis dial timeout":    c.Redis.DialTimeout,
		"redis read timeout":    c.Redis.ReadTimeout,
		"redis write timeout":   c.Redis.WriteTimeout,
	} {
		if value < 100*time.Millisecond || value > time.Minute {
			return fmt.Errorf("%s must be between 100ms and 60s", name)
		}
	}
	if err := validateIPRegionConfig(c.IPRegion); err != nil {
		return err
	}
	if err := validateProxyTestURL(c.Network.ProxyTestURL); err != nil {
		return err
	}
	if strings.TrimSpace(c.Auth.Secret) == "" {
		return fmt.Errorf("JWT_SECRET is required")
	}
	if len([]byte(c.Auth.Secret)) < 32 {
		return fmt.Errorf("JWT_SECRET must be at least 32 bytes")
	}
	sameSite := strings.ToLower(strings.TrimSpace(c.Auth.RefreshCookieSameSite))
	if sameSite == "" {
		sameSite = "lax"
	}
	if sameSite != "lax" && sameSite != "strict" && sameSite != "none" {
		return fmt.Errorf("JWT_COOKIE_SAME_SITE must be Lax, Strict, or None")
	}
	if sameSite == "none" && !c.Auth.RefreshCookieSecure {
		return fmt.Errorf("JWT_COOKIE_SECURE must be true when JWT_COOKIE_SAME_SITE=None")
	}
	if c.Auth.RefreshRotationGrace < 0 || c.Auth.RefreshRotationGrace > 5*time.Minute {
		return fmt.Errorf("JWT_REFRESH_ROTATION_GRACE_SECONDS must be between 0 and 300")
	}
	if c.OAuth.HTTPTimeout < 3*time.Second || c.OAuth.HTTPTimeout > time.Minute {
		return fmt.Errorf("OAUTH_HTTP_TIMEOUT_SECONDS must be between 3 and 60")
	}
	if c.isProduction() {
		if isInsecureSecret(c.Auth.Secret) {
			return fmt.Errorf("JWT_SECRET must be explicitly configured for production and cannot use a development placeholder")
		}
		if c.Database.User == "root" || c.Database.Password == "root" || strings.TrimSpace(c.Database.Password) == "" {
			return fmt.Errorf("production database credentials must not use root/root or an empty password")
		}
		if !c.Auth.RefreshCookieSecure {
			return fmt.Errorf("JWT_COOKIE_SECURE must be true in production")
		}
		if sameSite == "none" {
			return fmt.Errorf("JWT_COOKIE_SAME_SITE=None is not supported in production without an explicit CSRF protocol")
		}
		if len([]byte(strings.TrimSpace(c.Payment.ConfigSecret))) < 32 {
			return fmt.Errorf("PAYMENT_CONFIG_SECRET must be at least 32 bytes in production")
		}
		if len([]byte(strings.TrimSpace(c.System.ConfigSecret))) < 32 {
			return fmt.Errorf("SYSTEM_CONFIG_SECRET must be at least 32 bytes in production")
		}
	}
	return nil
}

func validateIPRegionConfig(cfg IPRegionConfig) error {
	if !cfg.Enabled {
		return nil
	}
	if cfg.Timeout < 100*time.Millisecond || cfg.Timeout > 30*time.Second {
		return fmt.Errorf("IP_REGION_TIMEOUT_MS must be between 100 and 30000")
	}
	if cfg.CacheTTL < 0 || cfg.CacheTTL > 30*24*time.Hour {
		return fmt.Errorf("IP_REGION_CACHE_TTL_MINUTES must be between 0 and 43200")
	}

	provider := strings.ToLower(strings.TrimSpace(cfg.Provider))
	switch provider {
	case "ip-api":
		return validateIPApiEndpoint(cfg.IPApiEndpoint)
	case "bilibili":
		return validateIPRegionEndpoint("BILIBILI_IP_REGION_ENDPOINT", cfg.BilibiliEndpoint)
	case "tencent-map":
		if strings.TrimSpace(cfg.TencentMapKey) == "" {
			return fmt.Errorf("TENCENT_MAP_KEY is required when IP_REGION_PROVIDER=tencent-map")
		}
		return validateIPRegionEndpoint("TENCENT_MAP_ENDPOINT", defaultConfigText(cfg.TencentMapEndpoint, "https://apis.map.qq.com/ws/location/v1/ip"))
	case "amap":
		if strings.TrimSpace(cfg.AmapKey) == "" {
			return fmt.Errorf("AMAP_KEY is required when IP_REGION_PROVIDER=amap")
		}
		return validateIPRegionEndpoint("AMAP_ENDPOINT", defaultConfigText(cfg.AmapEndpoint, "https://restapi.amap.com/v3/ip"))
	case "ip2region":
		if strings.TrimSpace(cfg.IP2RegionV4Path) == "" || strings.TrimSpace(cfg.IP2RegionV6Path) == "" {
			return fmt.Errorf("IP2REGION_V4_PATH and IP2REGION_V6_PATH are required when IP_REGION_PROVIDER=ip2region")
		}
		return nil
	default:
		return fmt.Errorf("IP_REGION_PROVIDER must be one of ip-api, bilibili, tencent-map, amap, or ip2region")
	}
}

// validateIPApiEndpoint 校验内置 IP 归属地源地址。为兼容历史部署仍允许显式
// 配置 HTTP，但代码默认使用 HTTPS，生产环境应优先采用 HTTPS 数据源。
func validateIPApiEndpoint(raw string) error {
	endpoint := strings.TrimSpace(raw)
	if endpoint == "" {
		return nil
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("IP_API_ENDPOINT must be an absolute http(s) URL")
	}
	if parsed.User != nil {
		return fmt.Errorf("IP_API_ENDPOINT must not contain embedded credentials")
	}
	if !strings.Contains(endpoint, "{ip}") {
		return fmt.Errorf("IP_API_ENDPOINT must contain the {ip} placeholder")
	}
	return nil
}

func validateIPRegionEndpoint(name, raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return fmt.Errorf("%s must be an absolute https URL", name)
	}
	if parsed.User != nil {
		return fmt.Errorf("%s must not contain embedded credentials", name)
	}
	return nil
}

func validateProxyTestURL(raw string) error {
	value := strings.TrimSpace(raw)
	if err := httpclient.ValidateURL(value); err != nil {
		return fmt.Errorf("PROXY_TEST_URL must be an absolute public HTTP(S) URL without credentials: %w", err)
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return fmt.Errorf("PROXY_TEST_URL is invalid: %w", err)
	}
	if ip := net.ParseIP(parsed.Hostname()); ip != nil && !httpclient.IsPublicIP(ip) {
		return fmt.Errorf("PROXY_TEST_URL must not use a private or reserved IP address")
	}
	return nil
}

func defaultConfigText(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

// isProduction 决定是否启用生产加固块（Validate 中的占位密钥、root/空库口令、
// Cookie Secure、支付与系统密钥长度检查）。
//
// 判定方向刻意是 fail-closed：只有**显式承认的开发/测试环境名**才免加固，
// 其余任何能通过 isKnownEnvironment 校验的取值一律加固。这样未来任何人往
// isKnownEnvironment 的白名单里加名字，都会自动被加固覆盖，不会再出现
// 「合法取值却能静默关掉生产加固」的口径分叉。
//
// 对现存 8 个可接受取值，本函数与「白名单 stage/staging/prod/production」
// 的旧写法行为完全等价（dev/development/test/testing 免加固，其余加固），
// 因此不改变任何现有部署的对外行为。
func (c Config) isProduction() bool {
	env := strings.ToLower(strings.TrimSpace(c.App.Env))
	return env != "dev" && env != "development" && env != "test" && env != "testing"
}

func isKnownEnvironment(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "dev", "development", "test", "testing", "stage", "staging", "prod", "production":
		return true
	default:
		return false
	}
}

func isInsecureSecret(secret string) bool {
	value := strings.ToLower(strings.TrimSpace(secret))
	return value == "" ||
		value == developmentJWTSecret ||
		strings.Contains(value, "change-me") ||
		strings.Contains(value, "replace-with") ||
		strings.Contains(value, "placeholder")
}

func validateSystemPrefix(prefix string) error {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" || prefix == "/" {
		return fmt.Errorf("SYSTEM_API_PREFIX must be a non-root absolute path")
	}
	if !strings.HasPrefix(prefix, "/") || strings.HasSuffix(prefix, "/") {
		return fmt.Errorf("SYSTEM_API_PREFIX must start with / and must not end with /")
	}
	if strings.ContainsAny(prefix, "?#\\") {
		return fmt.Errorf("SYSTEM_API_PREFIX must not contain query, fragment, or backslash characters")
	}
	for _, value := range prefix {
		if value <= 0x20 || value == 0x7f {
			return fmt.Errorf("SYSTEM_API_PREFIX must not contain whitespace or control characters")
		}
	}
	for _, segment := range strings.Split(strings.TrimPrefix(prefix, "/"), "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("SYSTEM_API_PREFIX contains an invalid path segment")
		}
	}
	lower := strings.ToLower(prefix)
	if lower == "/api" || strings.HasPrefix(lower, "/api/") {
		return fmt.Errorf("SYSTEM_API_PREFIX must stay outside /api/*; /api/{apiCode} is reserved for open gateway dispatch")
	}
	return nil
}

func applyEnv(cfg *Config) error {
	var err error
	cfg.App.Env = envString("APP_ENV", cfg.App.Env)
	cfg.Server.Host = envString("SERVER_HOST", cfg.Server.Host)
	cfg.API.SystemPrefix = normalizePrefix(envString("SYSTEM_API_PREFIX", cfg.API.SystemPrefix))
	cfg.Frontend.DistPath = envString("FRONTEND_DIST_PATH", cfg.Frontend.DistPath)
	if cfg.Frontend.Enabled, err = envBool("FRONTEND_SERVE_ENABLED", cfg.Frontend.Enabled); err != nil {
		return err
	}
	if cfg.Server.Port, err = envInt("SERVER_PORT", cfg.Server.Port); err != nil {
		return err
	}
	if cfg.Server.ReadTimeout, err = envDurationSeconds("SERVER_READ_TIMEOUT_SECONDS", cfg.Server.ReadTimeout); err != nil {
		return err
	}
	if cfg.Server.WriteTimeout, err = envDurationSeconds("SERVER_WRITE_TIMEOUT_SECONDS", cfg.Server.WriteTimeout); err != nil {
		return err
	}
	if cfg.Server.IdleTimeout, err = envDurationSeconds("SERVER_IDLE_TIMEOUT_SECONDS", cfg.Server.IdleTimeout); err != nil {
		return err
	}
	if cfg.Server.ShutdownTimeout, err = envDurationSeconds("SERVER_SHUTDOWN_TIMEOUT_SECONDS", cfg.Server.ShutdownTimeout); err != nil {
		return err
	}
	if cfg.Server.MaxRequestBodySize, err = envMegabytes("SERVER_MAX_REQUEST_BODY_MB", cfg.Server.MaxRequestBodySize); err != nil {
		return err
	}
	if raw := strings.TrimSpace(os.Getenv("SERVER_TRUSTED_PROXIES")); raw != "" {
		cfg.Server.TrustedProxies = splitCSV(raw)
	}

	cfg.Database.Host = envString("DB_HOST", cfg.Database.Host)
	if cfg.Database.Port, err = envInt("DB_PORT", cfg.Database.Port); err != nil {
		return err
	}
	cfg.Database.Name = envString("DB_NAME", cfg.Database.Name)
	cfg.Database.User = envString("DB_USER", cfg.Database.User)
	cfg.Database.Password = envString("DB_PASSWORD", cfg.Database.Password)
	if cfg.Database.SSL, err = envBool("DB_SSL", cfg.Database.SSL); err != nil {
		return err
	}
	// An explicit empty value revokes a stored acknowledgement; do not fall back.
	if value, present := os.LookupEnv("DB_TIMEZONE_DATA_CONFIRMATION"); present {
		cfg.Database.TimezoneDataConfirmation = strings.TrimSpace(value)
	}
	if cfg.Database.MaxOpenConns, err = envInt("DB_MAX_OPEN_CONNS", cfg.Database.MaxOpenConns); err != nil {
		return err
	}
	if cfg.Database.MaxIdleConns, err = envInt("DB_MAX_IDLE_CONNS", cfg.Database.MaxIdleConns); err != nil {
		return err
	}
	if cfg.Database.ConnMaxLifetime, err = envDurationMinutes("DB_CONN_MAX_LIFETIME_MINUTES", cfg.Database.ConnMaxLifetime); err != nil {
		return err
	}
	if cfg.Database.ConnMaxIdleTime, err = envDurationMinutes("DB_CONN_MAX_IDLE_MINUTES", cfg.Database.ConnMaxIdleTime); err != nil {
		return err
	}
	if cfg.Database.DialTimeout, err = envDurationSeconds("DB_DIAL_TIMEOUT_SECONDS", cfg.Database.DialTimeout); err != nil {
		return err
	}
	if cfg.Database.ReadTimeout, err = envDurationSeconds("DB_READ_TIMEOUT_SECONDS", cfg.Database.ReadTimeout); err != nil {
		return err
	}
	if cfg.Database.WriteTimeout, err = envDurationSeconds("DB_WRITE_TIMEOUT_SECONDS", cfg.Database.WriteTimeout); err != nil {
		return err
	}
	if cfg.Analytics.Enabled, err = envBool("ANALYTICS_ENABLED", cfg.Analytics.Enabled); err != nil {
		return err
	}
	if cfg.Analytics.BatchSize, err = envInt("ANALYTICS_BATCH_SIZE", cfg.Analytics.BatchSize); err != nil {
		return err
	}
	if cfg.Analytics.PollInterval, err = envDurationMilliseconds("ANALYTICS_POLL_INTERVAL_MS", cfg.Analytics.PollInterval); err != nil {
		return err
	}
	if cfg.Analytics.OverviewRefreshInterval, err = envDurationMinutes("ANALYTICS_OVERVIEW_REFRESH_INTERVAL_MINUTES", cfg.Analytics.OverviewRefreshInterval); err != nil {
		return err
	}
	if cfg.Analytics.OverviewSnapshotWindowMinutes, err = envInt("ANALYTICS_OVERVIEW_SNAPSHOT_WINDOW_MINUTES", cfg.Analytics.OverviewSnapshotWindowMinutes); err != nil {
		return err
	}
	cfg.Analytics.IPHashSecret = envString("ANALYTICS_IP_HASH_SECRET", cfg.Analytics.IPHashSecret)

	cfg.Redis.Host = envString("REDIS_HOST", cfg.Redis.Host)
	if cfg.Redis.Port, err = envInt("REDIS_PORT", cfg.Redis.Port); err != nil {
		return err
	}
	cfg.Redis.Password = envString("REDIS_PASSWORD", cfg.Redis.Password)
	if cfg.Redis.Database, err = envInt("REDIS_DATABASE", cfg.Redis.Database); err != nil {
		return err
	}
	if cfg.Redis.PoolSize, err = envInt("REDIS_POOL_SIZE", cfg.Redis.PoolSize); err != nil {
		return err
	}
	if cfg.Redis.MinIdleConns, err = envInt("REDIS_MIN_IDLE_CONNS", cfg.Redis.MinIdleConns); err != nil {
		return err
	}
	if cfg.Redis.MaxRetries, err = envInt("REDIS_MAX_RETRIES", cfg.Redis.MaxRetries); err != nil {
		return err
	}
	if cfg.Redis.PoolTimeout, err = envDurationMilliseconds("REDIS_POOL_TIMEOUT_MS", cfg.Redis.PoolTimeout); err != nil {
		return err
	}

	cfg.Migration.Path = envString("MIGRATIONS_PATH", cfg.Migration.Path)
	if cfg.Migration.Enabled, err = envBool("MIGRATION_ENABLED", cfg.Migration.Enabled); err != nil {
		return err
	}
	cfg.Auth.Issuer = envString("JWT_ISSUER", cfg.Auth.Issuer)
	cfg.Auth.Secret = envString("JWT_SECRET", cfg.Auth.Secret)
	cfg.Auth.InitialAdminPassword = envString("INITIAL_ADMIN_PASSWORD", cfg.Auth.InitialAdminPassword)
	if cfg.Auth.AccessTokenTTL, err = envDurationMinutes("JWT_ACCESS_TOKEN_MINUTES", cfg.Auth.AccessTokenTTL); err != nil {
		return err
	}
	if cfg.Auth.RefreshTokenTTL, err = envDurationDays("JWT_REFRESH_TOKEN_DAYS", cfg.Auth.RefreshTokenTTL); err != nil {
		return err
	}
	if cfg.Auth.RefreshRotationGrace, err = envDurationSeconds("JWT_REFRESH_ROTATION_GRACE_SECONDS", cfg.Auth.RefreshRotationGrace); err != nil {
		return err
	}
	cfg.Auth.RefreshCookieName = envString("JWT_REFRESH_COOKIE_NAME", cfg.Auth.RefreshCookieName)
	if cfg.Auth.RefreshCookieSecure, err = envBool("JWT_COOKIE_SECURE", cfg.Auth.RefreshCookieSecure); err != nil {
		return err
	}
	cfg.Auth.RefreshCookieSameSite = envString("JWT_COOKIE_SAME_SITE", cfg.Auth.RefreshCookieSameSite)
	if cfg.Auth.TwoFAChallengeTTL, err = envDurationSeconds("TWOFA_CHALLENGE_SECONDS", cfg.Auth.TwoFAChallengeTTL); err != nil {
		return err
	}
	if cfg.Auth.TwoFAPendingTTL, err = envDurationSeconds("TWOFA_PENDING_SECONDS", cfg.Auth.TwoFAPendingTTL); err != nil {
		return err
	}
	if cfg.Auth.PasswordResetAccountDayLimit, err = envInt("PASSWORD_RESET_ACCOUNT_DAY_LIMIT", cfg.Auth.PasswordResetAccountDayLimit); err != nil {
		return err
	}
	if cfg.OAuth.AllowPrivateEndpoints, err = envBool("OAUTH_ALLOW_PRIVATE_ENDPOINTS", cfg.OAuth.AllowPrivateEndpoints); err != nil {
		return err
	}
	if cfg.OAuth.HTTPTimeout, err = envDurationSeconds("OAUTH_HTTP_TIMEOUT_SECONDS", cfg.OAuth.HTTPTimeout); err != nil {
		return err
	}
	cfg.Captcha.DefaultProvider = envString("CAPTCHA_PROVIDER", cfg.Captcha.DefaultProvider)
	if cfg.Captcha.PowDifficulty, err = envInt("CAPTCHA_POW_DIFFICULTY", cfg.Captcha.PowDifficulty); err != nil {
		return err
	}
	cfg.Message.Email.Provider = envString("EMAIL_PROVIDER", cfg.Message.Email.Provider)
	if cfg.Message.Email.Enabled, err = envBool("EMAIL_ENABLED", cfg.Message.Email.Enabled); err != nil {
		return err
	}
	cfg.Message.Email.SMTPHost = envString("SMTP_HOST", cfg.Message.Email.SMTPHost)
	if cfg.Message.Email.SMTPPort, err = envInt("SMTP_PORT", cfg.Message.Email.SMTPPort); err != nil {
		return err
	}
	cfg.Message.Email.SMTPUsername = envString("SMTP_USERNAME", cfg.Message.Email.SMTPUsername)
	cfg.Message.Email.SMTPPassword = envString("SMTP_PASSWORD", cfg.Message.Email.SMTPPassword)
	cfg.Message.Email.SMTPFrom = envString("SMTP_FROM", cfg.Message.Email.SMTPFrom)
	cfg.Message.Email.SMTPFromName = envString("SMTP_FROM_NAME", cfg.Message.Email.SMTPFromName)
	if cfg.Message.Email.SMTPTLS, err = envBool("SMTP_TLS", cfg.Message.Email.SMTPTLS); err != nil {
		return err
	}
	cfg.Message.Email.CodeSubject = envString("EMAIL_CODE_SUBJECT", cfg.Message.Email.CodeSubject)
	cfg.Message.Email.CodeTemplate = envString("EMAIL_CODE_TEMPLATE", cfg.Message.Email.CodeTemplate)
	cfg.Message.Email.ResetSubject = envString("EMAIL_RESET_SUBJECT", cfg.Message.Email.ResetSubject)
	cfg.Message.Email.ResetTemplate = envString("EMAIL_RESET_TEMPLATE", cfg.Message.Email.ResetTemplate)
	cfg.Message.Email.CodeSubjectEn = envString("EMAIL_CODE_SUBJECT_EN", cfg.Message.Email.CodeSubjectEn)
	cfg.Message.Email.CodeTemplateEn = envString("EMAIL_CODE_TEMPLATE_EN", cfg.Message.Email.CodeTemplateEn)
	cfg.Message.Email.ResetSubjectEn = envString("EMAIL_RESET_SUBJECT_EN", cfg.Message.Email.ResetSubjectEn)
	cfg.Message.Email.ResetTemplateEn = envString("EMAIL_RESET_TEMPLATE_EN", cfg.Message.Email.ResetTemplateEn)
	if cfg.Message.Email.SendGlobalMinuteLimit, err = envInt("EMAIL_SEND_GLOBAL_MINUTE_LIMIT", cfg.Message.Email.SendGlobalMinuteLimit); err != nil {
		return err
	}
	if cfg.Message.Email.SendGlobalHourLimit, err = envInt("EMAIL_SEND_GLOBAL_HOUR_LIMIT", cfg.Message.Email.SendGlobalHourLimit); err != nil {
		return err
	}
	cfg.Message.SMS.Provider = envString("SMS_PROVIDER", cfg.Message.SMS.Provider)
	if cfg.Message.SMS.Enabled, err = envBool("SMS_ENABLED", cfg.Message.SMS.Enabled); err != nil {
		return err
	}
	if cfg.Network.AllowPrivateUpstreams, err = envBool("UPSTREAM_ALLOW_PRIVATE_NETWORKS", cfg.Network.AllowPrivateUpstreams); err != nil {
		return err
	}
	cfg.Network.ProxyTestURL = envString("PROXY_TEST_URL", cfg.Network.ProxyTestURL)
	if cfg.IPRegion.Enabled, err = envBool("IP_REGION_ENABLED", cfg.IPRegion.Enabled); err != nil {
		return err
	}
	cfg.IPRegion.Provider = envString("IP_REGION_PROVIDER", cfg.IPRegion.Provider)
	if cfg.IPRegion.Timeout, err = envDurationMilliseconds("IP_REGION_TIMEOUT_MS", cfg.IPRegion.Timeout); err != nil {
		return err
	}
	if cfg.IPRegion.CacheTTL, err = envDurationMinutes("IP_REGION_CACHE_TTL_MINUTES", cfg.IPRegion.CacheTTL); err != nil {
		return err
	}
	cfg.IPRegion.IP2RegionV4Path = envString("IP2REGION_V4_PATH", cfg.IPRegion.IP2RegionV4Path)
	cfg.IPRegion.IP2RegionV6Path = envString("IP2REGION_V6_PATH", cfg.IPRegion.IP2RegionV6Path)
	cfg.IPRegion.IPApiEndpoint = envString("IP_API_ENDPOINT", cfg.IPRegion.IPApiEndpoint)
	cfg.IPRegion.BilibiliEndpoint = envString("BILIBILI_IP_REGION_ENDPOINT", cfg.IPRegion.BilibiliEndpoint)
	cfg.IPRegion.TencentMapKey = envString("TENCENT_MAP_KEY", cfg.IPRegion.TencentMapKey)
	cfg.IPRegion.AmapKey = envString("AMAP_KEY", cfg.IPRegion.AmapKey)
	cfg.Payment.ConfigSecret = envString("PAYMENT_CONFIG_SECRET", cfg.Payment.ConfigSecret)
	cfg.System.ConfigSecret = envString("SYSTEM_CONFIG_SECRET", cfg.System.ConfigSecret)
	if raw := strings.TrimSpace(os.Getenv("CORS_ALLOWED_ORIGINS")); raw != "" {
		cfg.CORS.AllowedOrigins = splitCSV(raw)
	}
	cfg.SEO.SiteURL = envString("SEO_SITE_URL", cfg.SEO.SiteURL)
	return nil
}

func envString(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func splitCSV(raw string) []string {
	seen := map[string]bool{}
	result := make([]string, 0)
	for _, value := range strings.Split(raw, ",") {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

func envInt(key string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", key, err)
	}
	return value, nil
}

func envBool(key string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("invalid %s: %w", key, err)
	}
	return value, nil
}

func envDurationMinutes(key string, fallback time.Duration) (time.Duration, error) {
	value, err := envInt(key, int(fallback/time.Minute))
	if err != nil {
		return 0, err
	}
	return time.Duration(value) * time.Minute, nil
}

func envDurationDays(key string, fallback time.Duration) (time.Duration, error) {
	value, err := envInt(key, int(fallback/(24*time.Hour)))
	if err != nil {
		return 0, err
	}
	return time.Duration(value) * 24 * time.Hour, nil
}

func envDurationSeconds(key string, fallback time.Duration) (time.Duration, error) {
	value, err := envInt(key, int(fallback/time.Second))
	if err != nil {
		return 0, err
	}
	return time.Duration(value) * time.Second, nil
}

func envDurationMilliseconds(key string, fallback time.Duration) (time.Duration, error) {
	value, err := envInt(key, int(fallback/time.Millisecond))
	if err != nil {
		return 0, err
	}
	return time.Duration(value) * time.Millisecond, nil
}

func envMegabytes(key string, fallback int) (int, error) {
	value, err := envInt(key, fallback/(1024*1024))
	if err != nil {
		return 0, err
	}
	if value <= 0 {
		return fallback, nil
	}
	maxInt := int(^uint(0) >> 1)
	if value > maxInt/(1024*1024) {
		return 0, fmt.Errorf("%s is too large", key)
	}
	return value * 1024 * 1024, nil
}

func normalizePrefix(prefix string) string {
	text := strings.TrimSpace(prefix)
	if text == "" {
		return "/system-api"
	}
	if !strings.HasPrefix(text, "/") {
		text = "/" + text
	}
	return strings.TrimRight(text, "/")
}

func normalizeRuntime(cfg *Config) {
	if strings.TrimSpace(cfg.Frontend.DistPath) == "" {
		cfg.Frontend.DistPath = "frontend/dist"
	}
	if strings.TrimSpace(cfg.System.ConfigSecret) == "" {
		cfg.System.ConfigSecret = cfg.Payment.ConfigSecret
	}
	if strings.TrimSpace(cfg.Network.ProxyTestURL) == "" {
		cfg.Network.ProxyTestURL = "https://api.ipify.org?format=json"
	}
	if cfg.Server.ReadTimeout <= 0 {
		cfg.Server.ReadTimeout = 10 * time.Second
	}
	if cfg.Server.WriteTimeout <= 0 {
		cfg.Server.WriteTimeout = 30 * time.Second
	}
	if cfg.Server.IdleTimeout <= 0 {
		cfg.Server.IdleTimeout = 120 * time.Second
	}
	if cfg.Server.ShutdownTimeout <= 0 {
		cfg.Server.ShutdownTimeout = 30 * time.Second
	}
	if cfg.Server.MaxRequestBodySize <= 0 {
		cfg.Server.MaxRequestBodySize = 4 * 1024 * 1024
	}
	if cfg.OAuth.HTTPTimeout <= 0 {
		cfg.OAuth.HTTPTimeout = 10 * time.Second
	}
	if cfg.Database.MaxOpenConns <= 0 {
		cfg.Database.MaxOpenConns = 100
	}
	if cfg.Database.MaxIdleConns <= 0 {
		cfg.Database.MaxIdleConns = cfg.Database.MaxOpenConns / 2
	}
	if cfg.Database.ConnMaxLifetime <= 0 {
		cfg.Database.ConnMaxLifetime = 30 * time.Minute
	}
	if cfg.Database.ConnMaxIdleTime <= 0 {
		cfg.Database.ConnMaxIdleTime = 5 * time.Minute
	}
	if cfg.Database.DialTimeout <= 0 {
		cfg.Database.DialTimeout = 5 * time.Second
	}
	if cfg.Database.ReadTimeout <= 0 {
		cfg.Database.ReadTimeout = 15 * time.Second
	}
	if cfg.Database.WriteTimeout <= 0 {
		cfg.Database.WriteTimeout = 15 * time.Second
	}
	if cfg.Redis.PoolSize <= 0 {
		cfg.Redis.PoolSize = 256
	}
	if cfg.Redis.MinIdleConns < 0 {
		cfg.Redis.MinIdleConns = 0
	}
	if cfg.Redis.PoolTimeout <= 0 {
		cfg.Redis.PoolTimeout = 2 * time.Second
	}
}

func durationOrDefault(value, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	return value
}
