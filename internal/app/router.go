package app

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"bugpk-api/internal/config"
	"bugpk-api/internal/domain/advertising"
	"bugpk-api/internal/domain/analytics"
	"bugpk-api/internal/domain/auth"
	"bugpk-api/internal/domain/content"
	"bugpk-api/internal/domain/gateway"
	"bugpk-api/internal/domain/inbox"
	"bugpk-api/internal/domain/interfaceapi"
	"bugpk-api/internal/domain/ipconfig"
	"bugpk-api/internal/domain/ipregion"
	"bugpk-api/internal/domain/notification"
	"bugpk-api/internal/domain/observability"
	"bugpk-api/internal/domain/openapi"
	"bugpk-api/internal/domain/packagepkg"
	"bugpk-api/internal/domain/payment"
	"bugpk-api/internal/domain/rbac"
	"bugpk-api/internal/domain/redeem"
	"bugpk-api/internal/domain/revenue"
	"bugpk-api/internal/domain/riskcontrol"
	"bugpk-api/internal/domain/seo"
	"bugpk-api/internal/domain/systemsettings"
	"bugpk-api/internal/domain/ticket"
	"bugpk-api/internal/domain/translation"
	userdomain "bugpk-api/internal/domain/user"
	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"bugpk-api/internal/middleware"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/httpclient"
	"bugpk-api/internal/pkg/imagehost"
	"bugpk-api/internal/pkg/iphash"
	"bugpk-api/internal/pkg/masking"
	"bugpk-api/internal/pkg/requesttimeout"
	"bugpk-api/internal/pkg/response"
	"bugpk-api/internal/pkg/secretbox"
)

func RegisterRoutes(startupCtx context.Context, h *server.Hertz, cfg config.Config, db *sql.DB, redisClient *redis.Client, log *zap.Logger, registerCloser func(func(context.Context) error)) error {
	if startupCtx == nil {
		startupCtx = context.Background()
	}
	if err := startupCtx.Err(); err != nil {
		return err
	}
	if log == nil {
		log = zap.NewNop()
	}
	clientIP, err := middleware.TrustedProxyClientIP(cfg.Server.TrustedProxies)
	if err != nil {
		return fmt.Errorf("configure trusted proxies: %w", err)
	}
	h.SetClientIPFunc(clientIP)
	response.SetLogger(log)
	// CORS：仅前后端分离部署（cors.allowed_origins 非空）时注册；同域部署返回 nil，零影响。
	// 必须放在最前面，让 OPTIONS 预检请求直接返回 204，避免被后续限流/安全中间件拦截。
	if corsHandler := corsMiddleware(cfg.CORS); corsHandler != nil {
		h.Use(corsHandler)
	}
	h.Use(middleware.SecurityHeaders(cfg.API.SystemPrefix))
	h.Use(middleware.Recovery(log))
	h.Use(middleware.PublicContentRateLimit(redisClient, cfg.API.SystemPrefix, cfg.Auth.Secret, 120, time.Minute))

	// 安装按环境密钥的 IP 去标识化盐（汇总/风控/审计哈希共用）。必须在任何
	// 汇总或写入路径执行前完成；未配置密钥时与历史公开前缀逐字节一致（零
	// 行为变化），配置后即轮换分组键（需按交付说明重建受影响的汇总表）。
	iphash.Configure(cfg.Analytics.IPHashSecret)

	// Roll gateway logs into bounded minute/hour buckets in the background. The
	// migration has already completed before routes are registered, so starting
	// here guarantees every destination table exists. The worker is deliberately
	// serial and its closer participates in the application's graceful shutdown.
	if cfg.Analytics.Enabled {
		analyticsWorker := analytics.NewRepository(db).SetLogger(log).Start(
			context.Background(),
			cfg.Analytics.PollInterval,
			cfg.Analytics.BatchSize,
		)
		if registerCloser != nil {
			registerCloser(analyticsWorker.Close)
		}
		log.Info("analytics rollup worker started",
			zap.Duration("pollInterval", cfg.Analytics.PollInterval),
			zap.Int("batchSize", cfg.Analytics.BatchSize),
		)
	} else {
		// 关闭后聚合表不会被填充，工作台/仪表盘将展示 0 或空。这是显式关闭，
		// 不是兜底：真实数据来自该 worker。需要真实数据时请启用
		// analytics.enabled=true（或环境变量 ANALYTICS_ENABLED=true）。
		log.Warn("analytics rollup worker is DISABLED; dashboard/rollup tables will be empty until enabled (set analytics.enabled=true or ANALYTICS_ENABLED=true)")
	}

	h.GET("/healthz", func(ctx context.Context, c *hertzapp.RequestContext) {
		response.OK(c, map[string]string{"status": "ok"})
	})
	readiness := newReadinessProbe(db.PingContext, func(pingCtx context.Context) error {
		return redisClient.Ping(pingCtx).Err()
	})
	h.GET("/readyz", func(ctx context.Context, c *hertzapp.RequestContext) {
		if err := readiness.Check(ctx); err != nil {
			response.FromError(c, apperrors.Wrap(consts.StatusServiceUnavailable, "服务暂不可用", err))
			return
		}
		response.OK(c, map[string]string{"status": "ready"})
	})

	// SEO：公开 sitemap.xml（无需登录，动态生成，供搜索引擎抓取）
	seoService := seo.NewService(db)
	seoHandler := seo.NewHandler(seoService, cfg)

	systemConfigCipher := secretbox.New(cfg.System.ConfigSecret)
	systemSettingsRepo := systemsettings.NewRepository(db, systemConfigCipher)
	secretMigrationCtx, cancelSecretMigration := context.WithTimeout(startupCtx, 10*time.Second)
	defer cancelSecretMigration()
	if err := systemSettingsRepo.MigrateLegacySecrets(secretMigrationCtx); err != nil {
		return fmt.Errorf("migrate legacy provider secrets: %w", err)
	}
	authRepo := auth.NewRepository(db, systemConfigCipher)
	notificationRepo := notification.NewRepository(db)
	notificationService := notification.NewService(notificationRepo, cfg.Message.Email, authRepo, log)
	jwtService, err := auth.NewJWTService(cfg.Auth)
	if err != nil {
		return err
	}
	captchaService := auth.NewCaptchaService(cfg.Captcha, authRepo, redisClient)
	rateLimiter := auth.NewRateLimiter(redisClient, cfg.Auth.Secret)
	codeService := auth.NewVerificationCodeService(cfg.Message, cfg.Auth, authRepo, rateLimiter, redisClient)
	authService := auth.NewService(cfg.Auth, authRepo, captchaService, jwtService, rateLimiter, redisClient, codeService)
	authService.SetLogger(log)
	if registerCloser != nil {
		registerCloser(authService.Close)
	}

	userRepo := userdomain.NewRepository(db)
	userService := userdomain.NewService(userRepo, jwtService, captchaService, codeService, redisClient)
	userService.SetLogger(log)
	rbacRepo := rbac.NewRepository(db)
	rbacService := rbac.NewService(rbacRepo, jwtService)
	authService.SetPermissionProvider(rbacService)
	userService.SetAuthorizer(rbacService)

	// 翻译字典：默认语言原文留在业务表，字典只保存非默认语言译文。
	// 启动时加载进程内快照，后续通过 Redis 广播 + 定时刷新保持一致。
	translationService := translation.NewService(db, redisClient, rbacService)
	translationService.SetLogger(log)
	translationService.Start(context.Background())
	if registerCloser != nil {
		registerCloser(func(context.Context) error {
			translationService.Close()
			return nil
		})
	}
	// 用户域输出（工作台用户组名等）同样走字典译文。
	userService.SetLocalizer(translationService)

	observabilityRepo := observability.NewRepository(db)
	observabilityRepo.SetQueryTimeout(cfg.Database.QueryTimeout)
	observabilityRepo.SetSlowQueryLogger(func(name string, took time.Duration) {
		stats := db.Stats()
		// Wait counters are pool-wide cumulative values, not this SQL's duration.
		fields := []zap.Field{zap.String("query", name), zap.Duration("took", took),
			zap.Int("db_in_use", stats.InUse), zap.Int("db_idle", stats.Idle),
			zap.Int("db_max_open", stats.MaxOpenConnections),
			zap.Int64("db_wait_count_total", stats.WaitCount), zap.Duration("db_wait_duration_total", stats.WaitDuration)}
		if took >= 5*time.Second {
			log.Error("very slow observability query", fields...)
			return
		}
		log.Warn("slow observability query", fields...)
	})
	observabilityService := observability.NewService(observabilityRepo, rbacService)
	// 认证域通过窄接口投递审计记录，router 层做一次字段搬运，避免 auth 反向依赖业务模块。
	authService.SetAuditRecorder(observability.NewAuthAuditAdapter(observabilityService))
	// 账号封禁的全局主网关：凡是携带访问令牌的请求，在进入任何业务 handler
	// 之前先判定账号有效状态。挂在服务器级而不是某个路由组上，新增的路由
	// 前缀自动继承，不再依赖「记得把路由挂到 systemGroup」。
	// /api 与 /open-api 使用各自的 Key 体系，账号判定必须等解出 Key 所属
	// 账号后才能进行，分别见 gateway.Service.OpenKey 与 openapi 中间件。
	h.Use(newAccountStatusGuard(rbacService))

	// 所有管理路由必须从同一个分组派生，并在注册路由前安装审计中间件。
	// Hertz 不会把同前缀的独立分组关联起来；开放网关仍不挂审计。
	systemGroup := newSystemRouteGroup(h, cfg.API.SystemPrefix, observabilityService.OperationAuditMiddleware(cfg.API.SystemPrefix), log)
	systemGroup.Use(middleware.Locale(translationService))

	translationHandler := translation.NewHandler(translationService)
	translationHandler.Register(systemGroup.Group("/i18n"))

	seoHandler.Register(systemGroup.Group("/seo"))

	authHandler := auth.NewHandler(cfg, authService)
	authHandler.Register(systemGroup.Group("/auth"))

	oauthHTTPClient := httpclient.New(httpclient.Options{
		AllowPrivateNetworks:  cfg.OAuth.AllowPrivateEndpoints,
		Timeout:               cfg.OAuth.HTTPTimeout,
		ResponseHeaderTimeout: cfg.OAuth.HTTPTimeout,
	})
	oauthService := auth.NewOAuthService(
		cfg.App.Env,
		cfg.API.SystemPrefix,
		authService,
		auth.NewOAuthRepository(db, systemConfigCipher),
		redisClient,
		oauthHTTPClient,
	)
	oauthService.SetLogger(log)
	oauthService.SetAdminPermissionChecker(rbacService)
	oauthHandler := auth.NewOAuthHandler(cfg, oauthService)
	oauthHandler.RegisterPublic(systemGroup.Group("/auth/oauth"))
	oauthHandler.RegisterUser(systemGroup.Group("/user"))
	oauthHandler.RegisterSystem(systemGroup.Group("/system"))

	userHandler := userdomain.NewHandler(userService)
	userHandler.RegisterUser(systemGroup.Group("/user"))
	userHandler.RegisterSystem(systemGroup.Group("/system"))

	rbacHandler := rbac.NewHandler(rbacService)
	rbacHandler.RegisterMenu(systemGroup.Group("/menu"))
	rbacHandler.RegisterSystem(systemGroup.Group("/system"))

	systemSettingsService := systemsettings.NewService(systemSettingsRepo, rbacService, cfg.API.SystemPrefix)
	imageHostUploader := imagehost.NewUploader(httpclient.Outbound(20 * time.Second))
	systemSettingsService.SetImageHostUploader(imageHostUploader)
	// 登录态有效期：启动时从 bp_auth_session_config 装载覆盖 JWT 静态 TTL，
	// 之后管理端每次保存即时推送（观察者只动 JWTService 的原子覆盖值）。
	systemSettingsService.SetSessionConfigObserver(func(sc systemsettings.SessionConfig) {
		jwtService.ApplySessionTTL(sc.AccessTokenMinutes, sc.RefreshTokenDays)
	})
	if err := systemSettingsService.LoadSessionConfig(startupCtx); err != nil {
		log.Warn("load session config failed; falling back to static TTL", zap.Error(err))
	}
	systemSettingsHandler := systemsettings.NewHandler(systemSettingsService)
	systemSettingsHandler.RegisterSystem(systemGroup.Group("/system"))
	systemSettingsHandler.RegisterPublicSite(systemGroup.Group("/site"))

	ipConfigRepo := ipconfig.NewRepository(db)
	ipConfigService := ipconfig.NewService(ipConfigRepo, rbacService)
	ipConfigHandler := ipconfig.NewHandler(ipConfigService)
	ipConfigHandler.RegisterSystem(systemGroup.Group("/system"))

	advertisementRepo := advertising.NewRepository(db)
	advertisementService := advertising.NewService(advertisementRepo, rbacService, redisClient)
	advertisementService.SetLocalizer(translationService)
	advertisementHandler := advertising.NewHandler(advertisementService)
	advertisementHandler.Register(systemGroup.Group("/advertisement"))

	interfaceRepo := interfaceapi.NewRepository(db, systemConfigCipher)
	interfaceService := interfaceapi.NewService(interfaceRepo, rbacService, cfg.API.SystemPrefix, redisClient)
	interfaceService.SetLocalizer(translationService)
	gatewayRepo := gateway.NewRepository(db)
	gatewayService := gateway.NewService(gatewayRepo, rbacService, redisClient, cfg.Auth.Secret, systemConfigCipher)
	interfaceInvalidationBus := startInterfaceConfigInvalidationBus(
		newRedisInterfaceConfigInvalidationTransport(redisClient),
		gatewayService.InvalidateInterfaceConfig,
		log,
	)
	interfaceService.SetInterfaceChanged(interfaceInvalidationBus.Notify)
	if registerCloser != nil {
		registerCloser(interfaceInvalidationBus.Close)
	}
	// Confirmed ledger rows are the durable work queue. The worker projects
	// batches transactionally; hints contain no monetary deltas and may be lost.
	revenueSnapshot := gateway.NewRevenueSnapshot(revenue.NewRepository(db), log)
	gatewayRepo.SetRevenueSnapshot(revenueSnapshot)
	revenueSnapshot.Start(context.Background(), time.Second)
	if registerCloser != nil {
		registerCloser(revenueSnapshot.CloseContext)
	}
	userService.SetSpecifiedResponseChanged(func(ctx context.Context, userID int64) {
		gatewayService.InvalidateSpecifiedResponse(userID)
	})
	if registerCloser != nil {
		registerCloser(gatewayService.Close)
	}
	upstreamHTTPClient := httpclient.New(httpclient.Options{
		AllowPrivateNetworks: cfg.Network.AllowPrivateUpstreams,
		// The business timeout is enforced per request via context. Keep the
		// shared transport/client hard ceiling at the maximum configurable value
		// so a 15s legacy default cannot truncate a 30s interface override.
		Timeout:               time.Duration(requesttimeout.MaxMilliseconds) * time.Millisecond,
		ResponseHeaderTimeout: time.Duration(requesttimeout.MaxMilliseconds) * time.Millisecond,
	})
	interfaceService.SetHTTPClient(upstreamHTTPClient)
	interfaceService.SetProxyTestURL(cfg.Network.ProxyTestURL)
	interfaceService.SetLogger(log)
	gatewayService.SetHTTPClient(upstreamHTTPClient)
	gatewayService.SetLogger(log)
	gatewayService.SetLocalizer(translationService)
	gatewayService.SetResponseAdvertisementProvider(advertisementService)
	ipRegionResolver := ipregion.NewResolver(cfg.IPRegion, redisClient)
	ipRegionResolver.SetSourceLoader(ipConfigService.ActiveSource)
	gatewayService.SetIPRegionResolver(ipRegionResolver)
	// 审计记录的 IP 归属地与网关调用日志共用同一个解析器与缓存，保证同一 IP
	// 在两张表里得到一致结论；解析结果由 observability 异步回填。
	observabilityService.SetIPRegionResolver(ipRegionResolver)
	if registerCloser != nil {
		registerCloser(observabilityService.Close)
	}
	interfaceService.SetMarketKeyProvider(gatewayService)
	if probeWorker := interfaceService.StartUpstreamProbe(context.Background(), 60*time.Second); probeWorker != nil && registerCloser != nil {
		registerCloser(probeWorker.Close)
	}
	ipRegionHandler := ipregion.NewHandler(ipRegionResolver)
	h.GET(cfg.API.SystemPrefix+"/geoip", ipRegionHandler.GeoIP)
	interfaceHandler := interfaceapi.NewHandler(interfaceService)
	interfaceHandler.RegisterInterface(systemGroup.Group("/interface"))
	interfaceHandler.RegisterMarket(systemGroup.Group("/market"))
	interfaceHandler.RegisterUpload(systemGroup.Group("/upload"))

	gatewayHandler := gateway.NewHandler(gatewayService)
	gatewayHandler.RegisterKey(systemGroup.Group("/key"))
	gatewayHandler.RegisterUserQuota(systemGroup.Group("/user"))
	gatewayHandler.RegisterOpen(h.Group("/api"))

	packageRepo := packagepkg.NewRepository(db)
	packageService := packagepkg.NewService(packageRepo, rbacService)
	packageService.SetLocalizer(translationService)
	packageHandler := packagepkg.NewHandler(packageService)
	packageHandler.RegisterPackage(systemGroup.Group("/package"))
	packageHandler.RegisterUser(systemGroup.Group("/user"))

	// 操作日志按天预聚合：控制台的「今日操作数」与运营告警都读这张表，
	// 不直接扫明细。任务幂等（按自然日重算），失败下一轮自愈。
	if cfg.Analytics.Enabled {
		operationLogRollup := observabilityRepo.StartOperationLogRollup(
			context.Background(),
			2*time.Minute,
			func(err error) {
				log.Warn("operation log rollup failed", zap.String("error", masking.Error(err)))
			},
		)
		if registerCloser != nil {
			registerCloser(operationLogRollup.Close)
		}
	}
	observabilityHandler := observability.NewHandler(observabilityService)
	observabilityHandler.RegisterInterface(systemGroup.Group("/interface"))
	observabilityHandler.RegisterUser(systemGroup.Group("/user"))
	observabilityHandler.RegisterDashboard(systemGroup.Group("/dashboard"))
	observabilityHandler.RegisterOperationLog(systemGroup.Group("/system"))

	riskControlRepo := riskcontrol.NewRepository(db)
	riskControlService := riskcontrol.NewService(riskControlRepo, rbacService, redisClient)
	if cfg.Analytics.Enabled {
		riskSnapshotWorker := riskControlService.StartOverviewSnapshotWorker(
			context.Background(),
			cfg.Analytics,
			func(err error) {
				log.Warn("risk-control overview snapshot refresh failed", zap.String("error", masking.Error(err)))
			},
		)
		if registerCloser != nil {
			registerCloser(riskSnapshotWorker.Close)
		}
	}
	riskControlHandler := riskcontrol.NewHandler(riskControlService)
	riskControlHandler.Register(systemGroup.Group("/risk-control"))

	ticketRepo := ticket.NewRepository(db)
	ticketService := ticket.NewService(ticketRepo, rbacService, redisClient)
	ticketService.SetNotifier(notificationService)
	ticketHandler := ticket.NewHandler(ticketService, cfg.Server.MaxRequestBodySize)
	ticketHandler.SetImageHost(systemSettingsRepo, imageHostUploader)
	ticketHandler.Register(systemGroup.Group("/tickets"))

	paymentRepo := payment.NewRepository(db)
	paymentService := payment.NewService(paymentRepo, rbacService, payment.NewConfigCipher(cfg.Payment.ConfigSecret))
	paymentService.SetNotifier(notificationService)
	paymentService.SetLocalizer(translationService)
	paymentHandler := payment.NewHandler(paymentService)
	paymentHandler.Register(systemGroup.Group("/payment"))
	// TRC20 链上对账：按分钟拉取收款地址的已确认转账并匹配充值订单。
	trc20Worker := paymentService.StartTrc20Reconciliation(context.Background(), time.Minute, func(err error) {
		log.Warn("trc20 reconciliation failed", zap.Error(err))
	})
	if registerCloser != nil {
		registerCloser(trc20Worker.Close)
	}
	// BEP20（BSC）链上对账：与 TRC20 并行，独立区块游标。
	bep20Worker := paymentService.StartBep20Reconciliation(context.Background(), time.Minute, func(err error) {
		log.Warn("bep20 reconciliation failed", zap.Error(err))
	})
	if registerCloser != nil {
		registerCloser(bep20Worker.Close)
	}

	// 兑换码：频控 Guard 复用 auth 密钥派生摘要（HMAC 盐），Redis 故障
	// fail-closed；敏感操作（生成/作废/导出）由 service 显式写审计。
	redeemRepo := redeem.NewRepository(db)
	redeemGuard := redeem.NewGuard(redisClient, cfg.Auth.Secret)
	redeemService := redeem.NewService(redeemRepo, rbacService, redeemGuard, db)
	redeemService.SetOperationRecorder(observabilityService)
	redeemService.SetLocalizer(translationService)
	redeemHandler := redeem.NewHandler(redeemService)
	redeemHandler.RegisterSystem(systemGroup.Group("/redeem"))
	redeemHandler.RegisterUser(systemGroup.Group("/user/redeem"))

	// 站内消息中心：管理端全站/定向通知 + 用户端消息列表（可选邮件副本复用统一 outbox）。
	inboxService := inbox.NewService(inbox.NewRepository(db), rbacService)
	inboxService.SetNotifier(notificationService)
	inboxHandler := inbox.NewHandler(inboxService)
	inboxHandler.RegisterSystem(systemGroup.Group("/inbox"))
	inboxHandler.RegisterUser(systemGroup.Group("/user/inbox"))

	// 系统开放 API（/open-api 第三平面）：Key 鉴权中间件 × scope 门，
	// 编排层把开放 Key 身份适配为归属账号 Principal 后调用各域 service，
	// 因此页面侧既有校验（含实时 RBAC）在开放 API 上天然生效。
	openAPIRepo := openapi.NewRepository(db)
	openAPIService := openapi.NewService(openAPIRepo, rbacService, redisClient, db, cfg.Auth.Secret)
	openAPIService.SetSources(userService, packageService, gatewayService, paymentService, redeemService)
	openAPIService.SetOperationRecorder(observabilityService)
	if registerCloser != nil {
		registerCloser(openAPIService.Close)
	}
	openAPIHandler := openapi.NewHandler(openAPIService)
	openAPIHandler.RegisterUser(systemGroup.Group("/user"))
	openAPIHandler.RegisterAdmin(systemGroup.Group("/system"))
	openAPIHandler.RegisterOpen(h.Group("/open-api"))

	contentRepo := content.NewRepository(db)
	contentService := content.NewService(contentRepo, rbacService, cfg.API.SystemPrefix)
	contentService.SetLocalizer(translationService)
	translationService.SetHTMLSanitizer(content.SanitizeNoticeHTML)
	contentService.SetCaptchaConsumer(captchaService)
	contentService.SetNotifier(notificationService)
	contentService.SetPublicCache(redisClient, 30*time.Second)
	// Without this the homepage would publish zeros for traffic metrics when
	// the rollup worker is switched off. The homepage now reports those fields
	// as unavailable instead, so a visitor never reads "0 calls" as truth.
	contentService.SetAnalyticsEnabled(cfg.Analytics.Enabled)
	homePrimeCtx, cancelHomePrime := context.WithTimeout(startupCtx, 30*time.Second)
	if err := contentService.RefreshHomeAnalyticsSnapshot(homePrimeCtx); err != nil {
		log.Warn("home overview analytics snapshot prime failed", zap.String("error", masking.Error(err)))
	}
	cancelHomePrime()
	if err := startupCtx.Err(); err != nil {
		return err
	}
	homeSnapshotWorker := contentService.StartHomeAnalyticsSnapshotWorker(
		context.Background(),
		time.Minute,
		func(err error) {
			log.Warn("home overview analytics snapshot refresh failed", zap.String("error", masking.Error(err)))
		},
	)
	if registerCloser != nil {
		registerCloser(homeSnapshotWorker.Close)
	}
	userService.SetAccountTrafficChanged(contentService.InvalidateHomeOverview)
	contentHandler := content.NewHandler(contentService)
	contentHandler.RegisterNotice(systemGroup.Group("/notice"))
	contentHandler.RegisterSite(systemGroup.Group("/site"))
	contentHandler.RegisterAgreement(systemGroup.Group("/site"))
	contentHandler.RegisterDonation(systemGroup.Group("/donation"))
	contentHandler.RegisterFriendLinks(systemGroup.Group("/friend-links"))
	contentHandler.RegisterHome(systemGroup.Group("/home"))
	contentHandler.RegisterUpload(systemGroup.Group("/upload"))
	if registerCloser != nil {
		uploadCleaner := startUploadCleaner(db, "uploads", log)
		registerCloser(uploadCleaner.Close)
		notificationWorker := notificationService.Start(context.Background(), 2*time.Second)
		registerCloser(notificationWorker.Close)
	}

	// Register the final fallback only after every API route has been added.
	// It prevents an unmatched /api or /system-api request from being handled
	// by a frontend SPA fallback when the service is exposed directly.
	registerFrontendRoutes(h, cfg.Frontend, cfg.API.SystemPrefix, log)

	return nil
}
