package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"bugpk-api/internal/config"
	"bugpk-api/internal/domain/payment"
	"bugpk-api/internal/pkg/masking"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

type Application struct {
	Config config.Config
	Logger *zap.Logger
	DB     *sql.DB
	Redis  *redis.Client
	Server *server.Hertz

	billingReconcileCancel context.CancelFunc
	billingReconcileDone   <-chan struct{}
	banExpiryCancel        context.CancelFunc
	banExpiryDone          <-chan struct{}
	paymentExpiryCancel    context.CancelFunc
	paymentExpiryDone      <-chan struct{}
	backgroundClosers      []func(context.Context) error
	closeOnce              sync.Once
	requests               *requestDrain
	closeDone              chan struct{}
	closeErr               error
}

func New(ctx context.Context, cfg config.Config, log *zap.Logger) (*Application, error) {
	db, err := openDatabase(ctx, cfg.Database)
	if err != nil {
		return nil, fmt.Errorf("mysql connection failed: %w", err)
	}

	// 迁移开关：默认开启（迁移包随项目存在时启动自动执行）；
	// 手动导入全量 SQL 的部署设 MIGRATION_ENABLED=false 跳过，防止重复建表。
	if cfg.Migration.Enabled {
		if err := RunMigrations(db, cfg.Migration.Path); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("database migration failed: %w", err)
		}
	}
	adminPasswordInitialized, err := ensureInitialAdminPassword(ctx, db, cfg)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initial administrator setup failed: %w", err)
	}
	if adminPasswordInitialized && log != nil {
		log.Warn("initial administrator password was replaced; change it after the first login")
	}

	redisClient, err := openRedis(ctx, cfg.Redis)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("redis connection failed: %w", err)
	}

	return newApplicationWithConnections(ctx, cfg, log, db, redisClient)
}

// newApplicationWithConnections owns the already initialized database and Redis
// connections while composing routes and background services.
func newApplicationWithConnections(ctx context.Context, cfg config.Config, log *zap.Logger, db *sql.DB, redisClient *redis.Client) (_ *Application, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if log == nil {
		log = zap.NewNop()
	}
	application := &Application{Config: cfg, Logger: log, DB: db, Redis: redisClient}
	complete := false
	defer func() {
		if !complete {
			cleanupErr := application.Close()
			cleanupErr = errors.Join(cleanupErr, application.WaitClosed(context.Background()))
			if err != nil {
				err = errors.Join(err, cleanupErr)
			} else if cleanupErr != nil {
				log.Error("startup cleanup failed", zap.String("error", masking.Error(cleanupErr)))
			}
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	application.requests = newRequestDrain()
	application.Server = newHTTPServer(cfg.Server, application.requests)
	if err := RegisterRoutes(ctx, application.Server, cfg, db, redisClient, log, func(closer func(context.Context) error) {
		if closer != nil {
			application.backgroundClosers = append(application.backgroundClosers, closer)
		}
	}); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	application.billingReconcileCancel, application.billingReconcileDone = startBillingReconciler(ctx, db, log)
	// 临时封禁到期必须显式落库，否则 status=0 与「有效状态=1」并存，
	// 后台显示已封禁而网关继续放行（见 ban_expiry.go 的说明）。
	application.banExpiryCancel, application.banExpiryDone = startBanExpirySweeper(ctx, db, log)
	// 未支付订单到期后落库为 EXPIRED，并释放限量套餐预占库存，
	// 避免过期的待支付订单继续出现在「继续支付」提示里。
	application.paymentExpiryCancel, application.paymentExpiryDone = startPaymentExpirySweeper(ctx, payment.NewRepository(db), log)
	complete = true
	return application, nil
}

func (a *Application) shutdownTimeout() time.Duration {
	timeout := a.Config.Server.ShutdownTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return timeout
}
func (a *Application) Run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	errCh := make(chan error, 1)
	go func() { errCh <- runHTTPServer(a.Server) }()
	select {
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("http server stopped: %w", err)
		}
		return nil
	case <-ctx.Done():
		if a.requests != nil {
			a.requests.stop()
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), a.shutdownTimeout())
		defer cancel()
		serverErr := a.Server.Shutdown(shutdownCtx)
		var requestErr error
		if a.requests != nil {
			requestErr = a.requests.wait(shutdownCtx)
		}
		if err := shutdownCtx.Err(); err != nil {
			if a.requests != nil {
				a.requests.cancelActive()
			}
			return errors.Join(fmt.Errorf("HTTP shutdown did not drain: %w", err), serverErr, requestErr)
		}
		if err := errors.Join(serverErr, requestErr); err != nil {
			return fmt.Errorf("shutdown http server: %w", err)
		}
		select {
		case err := <-errCh:
			// Listener completion is not proof of a successful drain if the deadline
			// became ready at the same time (Hertz may return nil on transport timeout).
			if deadlineErr := shutdownCtx.Err(); deadlineErr != nil {
				return fmt.Errorf("HTTP shutdown did not drain: %w", deadlineErr)
			}
			if err != nil {
				return fmt.Errorf("http server stopped during shutdown: %w", err)
			}
			return nil
		case <-shutdownCtx.Done():
			return fmt.Errorf("wait for http server shutdown: %w", shutdownCtx.Err())
		}
	}
}

// Hertz's netpoll transport panics on a listener bind failure instead of
// returning it. Convert only that documented startup failure at the same
// goroutine boundary; unrelated panics must still propagate.
func runHTTPServer(h *server.Hertz) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			message, ok := recovered.(string)
			const prefix = "create netpoll listener fail: "
			if h.GetTransporterName() == "netpoll" && ok && strings.HasPrefix(message, prefix) {
				err = fmt.Errorf("listen HTTP: %s", strings.TrimPrefix(message, prefix))
				return
			}
			panic(recovered)
		}
	}()
	return h.Run()
}

// Close starts one ordered cleanup and bounds the caller's wait. On timeout,
// cleanup continues; dependencies are not closed ahead of active producers.
// A later Close can wait for the same cleanup, not execute callbacks twice.
func (a *Application) Close() error {
	if a == nil {
		return nil
	}
	if a.requests != nil {
		a.requests.stop()
	}
	a.beginClose()
	ctx, cancel := context.WithTimeout(context.Background(), a.shutdownTimeout())
	defer cancel()
	select {
	case <-a.closeDone:
		return a.closeErr
	default:
	}
	select {
	case <-a.closeDone:
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("application shutdown wait: %w", err)
		}
		return a.closeErr
	case <-ctx.Done():
		if a.requests != nil {
			a.requests.cancelActive()
		}
		return fmt.Errorf("application shutdown wait: %w", ctx.Err())
	}
}
func (a *Application) beginClose() {
	if a.requests != nil {
		a.requests.stop()
	}
	a.closeOnce.Do(func() { a.closeDone = make(chan struct{}); go a.closeResources() })
}

// WaitClosed lets lifecycle owners wait for the SAME cleanup after a bounded
// Close call times out. It never invokes resource callbacks a second time.
func (a *Application) WaitClosed(ctx context.Context) error {
	if a == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	a.beginClose()
	select {
	case <-a.closeDone:
		return a.closeErr
	default:
	}
	select {
	case <-a.closeDone:
		return a.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *Application) closeResources() {
	defer close(a.closeDone)
	if a.requests != nil {
		a.requests.stop()
		<-a.requests.done
	}
	var closeErrors error
	if a.billingReconcileCancel != nil {
		a.billingReconcileCancel()
	}
	if a.billingReconcileDone != nil {
		<-a.billingReconcileDone
	}
	if a.banExpiryCancel != nil {
		a.banExpiryCancel()
	}
	if a.banExpiryDone != nil {
		<-a.banExpiryDone
	}
	if a.paymentExpiryCancel != nil {
		a.paymentExpiryCancel()
	}
	if a.paymentExpiryDone != nil {
		<-a.paymentExpiryDone
	}
	// Detached waiting is deliberate: each worker initiates its own stop and
	// must actually finish before its dependencies are released. The public
	// Close wait is bounded independently, including Redis/driver Close calls.
	for index := len(a.backgroundClosers) - 1; index >= 0; index-- {
		closer := a.backgroundClosers[index]
		closeErrors = errors.Join(closeErrors, runCleanup("background service", func() error { return closer(context.Background()) }))
	}
	if a.Redis != nil {
		closeErrors = errors.Join(closeErrors, runCleanup("redis", a.Redis.Close))
	}
	if a.DB != nil {
		closeErrors = errors.Join(closeErrors, runCleanup("database", a.DB.Close))
	}
	a.closeErr = closeErrors
}
func runCleanup(name string, close func() error) (err error) {
	defer func() {
		if recover() != nil {
			err = fmt.Errorf("%s shutdown panicked", name)
		}
	}()
	if err = close(); err != nil {
		return fmt.Errorf("close %s: %w", name, err)
	}
	return nil
}
