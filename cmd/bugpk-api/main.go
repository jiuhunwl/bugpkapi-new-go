package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"bugpk-api/internal/app"
	"bugpk-api/internal/config"
	"bugpk-api/internal/pkg/logger"
	"bugpk-api/internal/pkg/masking"

	_ "github.com/go-sql-driver/mysql"
	"github.com/golang-migrate/migrate/v4"
	"go.uber.org/zap"
)

var (
	version     = "dev"
	releaseDate = "unknown"
)

func main() {
	if len(os.Args) >= 2 && os.Args[1] == "call-log-partitions" {
		os.Exit(runCallLogPartitionCommand(os.Args[2:], os.Stdout))
	}
	if len(os.Args) >= 2 && os.Args[1] == "operation-log-regions" {
		os.Exit(runOperationLogRegionCommand(os.Args[2:], os.Stdout))
	}

	if len(os.Args) == 2 && (os.Args[1] == "--version" || os.Args[1] == "version") {
		fmt.Printf("BugPk-Api %s (%s)\n", version, releaseDate)
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "migrate-version" {
		os.Exit(runMigrationCommand(false))
	}
	if len(os.Args) == 2 && os.Args[1] == "migrate-up" {
		os.Exit(runMigrationCommand(true))
	}
	if len(os.Args) == 3 && os.Args[1] == "database-backup" {
		os.Exit(runDatabaseBackup(os.Args[2]))
	}
	os.Exit(run())
}

func runMigrationCommand(apply bool) int {
	cfg, err := config.Load(os.Getenv("CONFIG_FILE"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %s\n", masking.Error(err))
		return 1
	}
	db, err := app.OpenDatabase(context.Background(), cfg.Database)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to open database: %s\n", masking.Error(err))
		return 1
	}
	defer db.Close()
	if err := db.PingContext(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "failed to connect database: %s\n", masking.Error(err))
		return 1
	}

	if apply {
		if err := app.RunMigrations(db, cfg.Migration.Path); err != nil {
			fmt.Fprintf(os.Stderr, "database migration failed: %s\n", masking.Error(err))
			return 1
		}
	}

	state, err := app.MigrationVersion(db, cfg.Migration.Path)
	if err != nil {
		if errors.Is(err, migrate.ErrNilVersion) {
			fmt.Println("schema_migrations=nil:0")
			return 0
		}
		fmt.Fprintf(os.Stderr, "failed to read migration version: %s\n", masking.Error(err))
		return 1
	}
	fmt.Printf("schema_migrations=%d:%d\n", state.Version, boolToInt(state.Dirty))
	return 0
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func runDatabaseBackup(path string) int {
	cfg, err := config.Load(os.Getenv("CONFIG_FILE"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %s\n", masking.Error(err))
		return 1
	}
	dumpBinary, err := exec.LookPath("mysqldump")
	if err != nil {
		fmt.Fprintln(os.Stderr, "database backup requires mysqldump in PATH")
		return 1
	}
	output, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create database backup: %s\n", masking.Error(err))
		return 1
	}

	args := []string{
		"--single-transaction",
		"--quick",
		"--routines",
		"--triggers",
		"--events",
		"--hex-blob",
		"--set-gtid-purged=OFF",
		"--no-tablespaces",
		"--default-character-set=utf8mb4",
		"--host=" + cfg.Database.Host,
		"--port=" + strconv.Itoa(cfg.Database.Port),
		"--user=" + cfg.Database.User,
		cfg.Database.Name,
	}
	command := exec.Command(dumpBinary, args...)
	command.Env = append(os.Environ(), "MYSQL_PWD="+cfg.Database.Password)
	command.Stdout = output
	var commandStderr bytes.Buffer
	command.Stderr = &commandStderr
	runErr := command.Run()
	closeErr := output.Close()
	// mysqldump writes connection and authentication diagnostics to stderr;
	// capture it so no raw host, DSN fragment, or password can bypass the
	// application's endpoint/credential masking policy.
	if stderrText := maskedCommandStderr(commandStderr.String()); stderrText != "" {
		fmt.Fprintln(os.Stderr, stderrText)
	}
	if runErr != nil || closeErr != nil {
		_ = os.Remove(path)
		fmt.Fprintf(os.Stderr, "database backup failed: %s\n", masking.Error(errors.Join(runErr, closeErr)))
		return 1
	}
	fmt.Printf("database_backup=%s\n", path)
	return 0
}

func maskedCommandStderr(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return masking.ErrorText(value)
}

func run() int {
	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	// The first termination signal starts graceful shutdown. Restore the
	// default signal action so an explicit second signal can force termination.
	go func() { <-ctx.Done(); stopSignals() }()
	defer stopSignals()

	cfg, err := config.Load(os.Getenv("CONFIG_FILE"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %s\n", masking.Error(err))
		return 1
	}

	log, err := logger.New(cfg.App.Env)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to initialize logger: %s\n", masking.Error(err))
		return 1
	}
	defer func() {
		_ = log.Sync()
	}()

	application, err := app.New(ctx, cfg, log)
	if err != nil {
		log.Error("failed to bootstrap application", zap.String("error", masking.Error(err)))
		return 1
	}

	log.Info("starting BugPk-Api", zap.String("addr", cfg.Server.Addr()))
	runErr := application.Run(ctx)
	stopSignals()
	closeErr := closeApplication(application, log)
	if runErr != nil {
		log.Error("application stopped unexpectedly", zap.String("error", masking.Error(runErr)))
		if closeErr != nil {
			log.Error("application cleanup failed", zap.String("error", masking.Error(closeErr)))
		}
		return 1
	}
	if closeErr != nil {
		log.Error("application cleanup failed", zap.String("error", masking.Error(closeErr)))
		return 1
	}
	log.Info("BugPk-Api stopped gracefully")
	return 0
}

func closeApplication(application interface {
	Close() error
	WaitClosed(context.Context) error
}, log *zap.Logger) error {
	initialErr := application.Close()
	if initialErr != nil && log != nil {
		log.Warn("initial cleanup wait failed; waiting for final cleanup (another termination signal forces exit)", zap.String("error", masking.Error(initialErr)))
	}
	// Returning from run leads directly to os.Exit. Never abandon the ongoing
	// ordered cleanup just because its initial wait budget has been exceeded.
	finalErr := application.WaitClosed(context.Background())
	return errors.Join(initialErr, finalErr)
}
