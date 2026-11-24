// Command bugpk-analytics-bench creates reproducible synthetic gateway traffic
// in an isolated MySQL database, drains the production analytics rollup, and
// measures bounded rollup-table queries. It deliberately refuses the normal
// production database name.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"bugpk-api/internal/domain/analytics"
	"bugpk-api/internal/domain/content"
	"bugpk-api/internal/pkg/masking"

	"github.com/go-sql-driver/mysql"
)

const (
	defaultRows    = int64(1_000_000)
	defaultBatch   = 1_000
	defaultWorkers = 4
	maxWorkers     = 64
	maxRows        = int64(1_000_000_000)
	maxInsertBatch = 2_000
	safetyDBName   = "bugpk_api"
)

type options struct {
	DSN            string
	Rows           int64
	Batch          int
	Workers        int
	RollupBatch    int
	RollupBatches  int
	QueryTimeout   time.Duration
	Cleanup        bool
	CleanupOnly    bool
	Migrate        bool
	MigrationPath  string
	ExplainAnalyze bool
}

type databaseTarget struct {
	Name string
	Host string
	User string
}

type syntheticShape struct {
	Interfaces int64
	Users      int64
	IPs        int64
	Hours      int64
}

type benchmarkQuery struct {
	Name string
	SQL  string
	Args []any
}

type queryResult struct {
	Name   string
	Cold   time.Duration
	Hot    time.Duration
	Rows   int
	Digest uint64
	Plan   []string
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	opts, err := parseOptions(os.Args[1:], os.Getenv)
	if err != nil {
		exitf("invalid arguments: %v", err)
	}
	target, err := validateTarget(opts.DSN)
	if err != nil {
		exitf("unsafe database target: %v", err)
	}
	printSafetyBanner(os.Stdout, target, opts)

	db, err := sql.Open("mysql", opts.DSN)
	if err != nil {
		exitf("open mysql: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(opts.Workers + 4)
	db.SetMaxIdleConns(opts.Workers + 2)
	db.SetConnMaxLifetime(5 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		exitf("ping isolated mysql %s: %v", target.Name, err)
	}

	if opts.Migrate {
		if err := applyConfiguredMigration(ctx, db, opts.MigrationPath); err != nil {
			exitf("apply analytics migration: %v", err)
		}
	}
	if err := requireSchema(ctx, db); err != nil {
		exitf("schema check failed: %v", err)
	}

	if opts.CleanupOnly {
		if err := cleanupSynthetic(ctx, db); err != nil {
			exitf("cleanup: %v", err)
		}
		fmt.Println("cleanup=complete")
		return
	}

	if err := cleanupSynthetic(ctx, db); err != nil {
		exitf("pre-run cleanup: %v", err)
	}
	if err := requireEmptyCallLog(ctx, db); err != nil {
		exitf("isolation check: %v", err)
	}
	if err := seedDimensions(ctx, db); err != nil {
		exitf("seed benchmark dimensions: %v", err)
	}

	insertStarted := time.Now()
	inserted, err := insertSyntheticLogs(ctx, db, opts)
	if err != nil {
		exitf("insert synthetic logs: %v", err)
	}
	insertElapsed := time.Since(insertStarted)
	fmt.Printf("insert_rows=%d\n", inserted)
	fmt.Printf("insert_elapsed=%s\n", insertElapsed.Round(time.Millisecond))
	fmt.Printf("insert_rows_per_second=%.0f\n", float64(inserted)/insertElapsed.Seconds())

	repo := analytics.NewRepository(db)
	rollupStarted := time.Now()
	rollup, err := repo.RunUntilCaughtUp(ctx, opts.RollupBatch, opts.RollupBatches)
	if err != nil {
		exitf("run analytics rollup: %v", err)
	}
	rollupElapsed := time.Since(rollupStarted)
	checkpoint, err := repo.Checkpoint(ctx)
	if err != nil {
		exitf("read analytics checkpoint: %v", err)
	}
	if rollup.HasMore {
		exitf("rollup stopped with backlog after %d source rows; increase -rollup-batches", rollup.SourceRows)
	}
	fmt.Printf("rollup_source_rows=%d\n", rollup.SourceRows)
	fmt.Printf("rollup_contribution_rows=%d\n", rollup.ContributionRows)
	fmt.Printf("rollup_elapsed=%s\n", rollupElapsed.Round(time.Millisecond))
	fmt.Printf("rollup_rows_per_second=%.0f\n", float64(rollup.SourceRows)/rollupElapsed.Seconds())
	fmt.Printf("checkpoint_last_log_id=%d\n", checkpoint.LastLogID)
	homeRefreshStarted := time.Now()
	homeRefreshCtx, cancelHomeRefresh := context.WithTimeout(ctx, opts.QueryTimeout)
	err = content.NewService(content.NewRepository(db), nil, "").RefreshHomeAnalyticsSnapshot(homeRefreshCtx)
	cancelHomeRefresh()
	if err != nil {
		exitf("refresh persisted homepage snapshot: %v", err)
	}
	fmt.Printf("home_snapshot_refresh_elapsed=%s\n", time.Since(homeRefreshStarted).Round(time.Microsecond))

	now := time.Now().UTC()
	queries := append(analyticsQueries(now), callLogListQueries(now)...)
	results := make([]queryResult, 0, len(queries))
	for _, query := range queries {
		result, err := benchmark(ctx, db, opts.QueryTimeout, opts.ExplainAnalyze, query)
		if err != nil {
			exitf("benchmark %s: %v", query.Name, err)
		}
		results = append(results, result)
		printQueryResult(os.Stdout, result)
	}

	if err := printRollupCardinality(ctx, db); err != nil {
		exitf("read rollup cardinality: %v", err)
	}
	if opts.Cleanup {
		if err := cleanupSynthetic(ctx, db); err != nil {
			exitf("post-run cleanup: %v", err)
		}
		fmt.Println("cleanup=complete")
	} else {
		fmt.Println("cleanup=skipped (run again with -cleanup or -cleanup-only)")
	}
}

func parseOptions(args []string, getenv func(string) string) (options, error) {
	fs := flag.NewFlagSet("bugpk-analytics-bench", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	defaults := options{
		DSN:            strings.TrimSpace(getenv("BUGPK_ANALYTICS_BENCH_DSN")),
		Rows:           defaultRows,
		Batch:          defaultBatch,
		Workers:        defaultWorkers,
		RollupBatch:    analytics.MaxBatchSize,
		RollupBatches:  10_000,
		QueryTimeout:   30 * time.Second,
		MigrationPath:  "migrations/000090_analytics_rollup_repair.up.sql",
		ExplainAnalyze: true,
	}
	fs.StringVar(&defaults.DSN, "dsn", defaults.DSN, "isolated MySQL DSN; may also use BUGPK_ANALYTICS_BENCH_DSN")
	fs.Int64Var(&defaults.Rows, "rows", defaults.Rows, "synthetic call-log rows (default 1,000,000)")
	fs.IntVar(&defaults.Batch, "batch", defaults.Batch, "rows per multi-value INSERT")
	fs.IntVar(&defaults.Workers, "workers", defaults.Workers, "parallel insert workers")
	fs.IntVar(&defaults.RollupBatch, "rollup-batch", defaults.RollupBatch, "source rows per analytics transaction")
	fs.IntVar(&defaults.RollupBatches, "rollup-batches", defaults.RollupBatches, "maximum analytics transactions")
	fs.DurationVar(&defaults.QueryTimeout, "query-timeout", defaults.QueryTimeout, "timeout for each query or EXPLAIN")
	fs.BoolVar(&defaults.Cleanup, "cleanup", false, "remove generated rows and reset rollups after the run")
	fs.BoolVar(&defaults.CleanupOnly, "cleanup-only", false, "only remove generated rows and reset rollups")
	fs.BoolVar(&defaults.Migrate, "migrate", false, "apply the configured analytics migration before running (base schema must already exist)")
	fs.StringVar(&defaults.MigrationPath, "migration", defaults.MigrationPath, "path to the analytics migration SQL (defaults to 000090)")
	fs.BoolVar(&defaults.ExplainAnalyze, "explain-analyze", defaults.ExplainAnalyze, "run MySQL 8 EXPLAIN ANALYZE for every query")
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	if len(fs.Args()) != 0 {
		return options{}, fmt.Errorf("unexpected positional arguments: %s", strings.Join(fs.Args(), " "))
	}
	if defaults.DSN == "" {
		return options{}, errors.New("-dsn or BUGPK_ANALYTICS_BENCH_DSN is required")
	}
	if defaults.Rows <= 0 || defaults.Rows > maxRows {
		return options{}, fmt.Errorf("-rows must be between 1 and %d", maxRows)
	}
	if defaults.Batch <= 0 || defaults.Batch > maxInsertBatch {
		return options{}, fmt.Errorf("-batch must be between 1 and %d (keeps prepared statements below MySQL's parameter limit)", maxInsertBatch)
	}
	if defaults.Workers <= 0 || defaults.Workers > maxWorkers {
		return options{}, fmt.Errorf("-workers must be between 1 and %d", maxWorkers)
	}
	if defaults.RollupBatch <= 0 || defaults.RollupBatch > analytics.MaxBatchSize {
		return options{}, fmt.Errorf("-rollup-batch must be between 1 and %d", analytics.MaxBatchSize)
	}
	if defaults.RollupBatches <= 0 {
		return options{}, errors.New("-rollup-batches must be positive")
	}
	if defaults.QueryTimeout <= 0 {
		return options{}, errors.New("-query-timeout must be positive")
	}
	return defaults, nil
}

func validateTarget(dsn string) (databaseTarget, error) {
	cfg, err := mysql.ParseDSN(strings.TrimSpace(dsn))
	if err != nil {
		return databaseTarget{}, fmt.Errorf("parse DSN: %w", err)
	}
	name := strings.TrimSpace(cfg.DBName)
	if name == "" {
		return databaseTarget{}, errors.New("DSN must name an existing isolated database")
	}
	if strings.EqualFold(name, safetyDBName) {
		return databaseTarget{}, fmt.Errorf("database %q is reserved for normal application use", name)
	}
	if strings.EqualFold(name, "mysql") || strings.EqualFold(name, "information_schema") || strings.EqualFold(name, "performance_schema") || strings.EqualFold(name, "sys") {
		return databaseTarget{}, fmt.Errorf("system database %q is forbidden", name)
	}
	lowerName := strings.ToLower(name)
	if !strings.Contains(lowerName, "test") && !strings.Contains(lowerName, "e2e") && !strings.Contains(lowerName, "bench") {
		return databaseTarget{}, fmt.Errorf("database %q must be a dedicated test/e2e/bench database", name)
	}
	return databaseTarget{Name: name, Host: cfg.Addr, User: cfg.User}, nil
}

func printSafetyBanner(w io.Writer, target databaseTarget, opts options) {
	estimate := estimateStorage(opts.Rows)
	fmt.Fprintln(w, "BUGPK_ANALYTICS_BENCH=ISOLATED_SYNTHETIC_RUN")
	// The benchmark banner is often captured in CI artifacts and support logs.
	// Do not echo connection topology or the database account, even though the
	// target has already passed the isolated-database guard.  Keep the database
	// name (which is an explicitly test/bench-scoped value) as a useful
	// correlation hint while replacing the connection details with stable
	// placeholders.
	fmt.Fprintf(w, "database=%s host=[UPSTREAM_REDACTED] user=[REDACTED]\n", target.Name)
	fmt.Fprintf(w, "requested_rows=%d batch=%d workers=%d\n", opts.Rows, opts.Batch, opts.Workers)
	fmt.Fprintf(w, "estimated_raw_data=%s estimated_with_indexes=%s\n", humanBytes(estimate.RawBytes), humanBytes(estimate.WithIndexesBytes))
	fmt.Fprintln(w, "scope=local requested row count only; this is not a trillion-row insertion claim")
}

type storageEstimate struct {
	RawBytes         uint64
	WithIndexesBytes uint64
}

func estimateStorage(rows int64) storageEstimate {
	if rows <= 0 {
		return storageEstimate{}
	}
	// Compact benchmark rows omit large bodies, yet InnoDB records plus the
	// production secondary indexes still commonly occupy roughly 0.8-1.8 KiB.
	raw := uint64(rows) * 800
	return storageEstimate{RawBytes: raw, WithIndexesBytes: uint64(rows) * 1800}
}

func humanBytes(value uint64) string {
	const unit = uint64(1024)
	if value < unit {
		return fmt.Sprintf("%dB", value)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	n := float64(value)
	index := -1
	for n >= float64(unit) && index+1 < len(units) {
		n /= float64(unit)
		index++
	}
	return fmt.Sprintf("%.1f%s", n, units[index])
}

func applyConfiguredMigration(ctx context.Context, db *sql.DB, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	statements := splitSQLStatements(string(data))
	// Migration 000090 uses MySQL session variables and prepared statements.
	// Executing through *sql.DB for every statement lets database/sql move the
	// statements between pooled connections, which loses that session state.
	// Reserve one connection for the complete script instead.
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("reserve migration connection: %w", err)
	}
	defer conn.Close()
	for index, statement := range statements {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("statement %d: %w", index+1, err)
		}
	}
	return nil
}

func splitSQLStatements(script string) []string {
	lines := strings.Split(strings.ReplaceAll(script, "\r\n", "\n"), "\n")
	var clean strings.Builder
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") || trimmed == "" {
			continue
		}
		clean.WriteString(line)
		clean.WriteByte('\n')
	}

	// Migration comments and column definitions may legitimately contain
	// semicolons inside quoted strings. Split only on statement terminators
	// outside SQL string literals and quoted identifiers.
	text := clean.String()
	result := make([]string, 0, strings.Count(text, ";")+1)
	var statement strings.Builder
	var quote rune
	for index := 0; index < len(text); index++ {
		current := rune(text[index])
		if quote != 0 {
			statement.WriteByte(text[index])
			if current != quote {
				continue
			}
			if index+1 < len(text) && rune(text[index+1]) == quote {
				statement.WriteByte(text[index+1])
				index++
				continue
			}
			if quote != '`' {
				backslashes := 0
				for cursor := index - 1; cursor >= 0 && text[cursor] == '\\'; cursor-- {
					backslashes++
				}
				if backslashes%2 == 1 {
					continue
				}
			}
			quote = 0
			continue
		}
		switch current {
		case '\'', '"', '`':
			quote = current
			statement.WriteByte(text[index])
		case ';':
			if value := strings.TrimSpace(statement.String()); value != "" {
				result = append(result, value)
			}
			statement.Reset()
		default:
			statement.WriteByte(text[index])
		}
	}
	if value := strings.TrimSpace(statement.String()); value != "" {
		result = append(result, value)
	}
	return result
}

func requireSchema(ctx context.Context, db *sql.DB) error {
	required := []string{
		"bp_user", "bp_interface_api", "bp_interface_call_log",
		"bp_call_metric_minute", "bp_call_api_metric_minute", "bp_call_api_metric_day", "bp_call_user_metric_minute",
		"bp_call_region_ip_hour", "bp_call_region_hour", "bp_call_failure_ip_hour", "bp_call_failure_user_hour",
		"bp_call_risk_fingerprint_5m", "bp_analytics_checkpoint", "bp_home_overview_snapshot",
	}
	for _, table := range required {
		var count int
		if err := db.QueryRowContext(ctx, `
			select count(*) from information_schema.tables
			where table_schema=database() and table_name=?
		`, table).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("missing table %s; migrate an isolated copy of the complete schema first", table)
		}
	}
	return nil
}

func seedDimensions(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
		insert into bp_user(id,username,password_hash,display_name,email,email_verified,
			mobile,mobile_verified,home_path,balance,points,status,ban_permanent,ban_until,ban_reason,token_version)
		values
			(900000001,'bench_user_1','$2a$10$benchmarkbenchmarkbenchmarkbenchmarkbenchmarkbenchmark','Bench User 1',null,0,null,0,'/console/workspace',0,0,1,0,null,'',0),
			(900000002,'bench_user_2','$2a$10$benchmarkbenchmarkbenchmarkbenchmarkbenchmarkbenchmark','Bench User 2',null,0,null,0,'/console/workspace',0,0,1,0,null,'',0),
			(900000003,'bench_user_3','$2a$10$benchmarkbenchmarkbenchmarkbenchmarkbenchmarkbenchmark','Bench User 3',null,0,null,0,'/console/workspace',0,0,1,0,null,'',0),
			(900000004,'bench_user_4','$2a$10$benchmarkbenchmarkbenchmarkbenchmarkbenchmarkbenchmark','Bench User 4',null,0,null,0,'/console/workspace',0,0,1,0,null,'',0),
			(900000005,'bench_user_5','$2a$10$benchmarkbenchmarkbenchmarkbenchmarkbenchmarkbenchmark','Bench User 5',null,0,null,0,'/console/workspace',0,0,1,0,null,'',0)
		on duplicate key update status=1,ban_permanent=0,ban_until=null,ban_reason=''
	`)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
		insert into bp_interface_api(id,api_code,name,request_url,request_method,price,point_price,qps_limit,
			is_top,is_featured,status,remark,polling_enabled,polling_mode,polling_check_enabled,current_node,
			doc_response_type,cache_enabled,cache_default_ttl_seconds,response_ad_enabled,response_ad_field,
			cache_policy_override,response_ad_policy_override,delivery_policy_version,request_validation_enabled,
			request_validation_default_result,response_cleaning_enabled)
		values
			(900001,'bench.weather','Bench Weather','https://bench.invalid/weather','GET',0,0,0,0,0,1,'benchmark',0,'SINGLE',0,1,'JSON',0,0,0,'_advertisement',0,0,1,0,'PASS',0),
			(900002,'bench.ip','Bench IP','https://bench.invalid/ip','GET',0,0,0,0,0,1,'benchmark',0,'SINGLE',0,1,'JSON',0,0,0,'_advertisement',0,0,1,0,'PASS',0),
			(900003,'bench.video','Bench Video','https://bench.invalid/video','POST',0,0,0,0,0,1,'benchmark',0,'SINGLE',0,1,'JSON',0,0,0,'_advertisement',0,0,1,0,'PASS',0),
			(900004,'bench.sms','Bench SMS','https://bench.invalid/sms','POST',0,0,0,0,0,1,'benchmark',0,'SINGLE',0,1,'JSON',0,0,0,'_advertisement',0,0,1,0,'PASS',0),
			(900005,'bench.translate','Bench Translate','https://bench.invalid/translate','GET',0,0,0,0,0,1,'benchmark',0,'SINGLE',0,1,'JSON',0,0,0,'_advertisement',0,0,1,0,'PASS',0)
		on duplicate key update status=1,name=values(name),remark='benchmark'
	`)
	return err
}

func insertSyntheticLogs(ctx context.Context, db *sql.DB, opts options) (int64, error) {
	tasks := make(chan [2]int64, opts.Workers*2)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var inserted atomic.Int64
	var firstErr error
	var errOnce sync.Once
	var wg sync.WaitGroup
	for range opts.Workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for task := range tasks {
				if ctx.Err() != nil {
					return
				}
				if err := insertRange(ctx, db, task[0], task[1]); err != nil {
					errOnce.Do(func() {
						firstErr = err
						cancel()
					})
					return
				}
				inserted.Add(task[1])
			}
		}()
	}
schedule:
	for offset := int64(0); offset < opts.Rows; offset += int64(opts.Batch) {
		count := int64(opts.Batch)
		if remaining := opts.Rows - offset; remaining < count {
			count = remaining
		}
		select {
		case tasks <- [2]int64{offset, count}:
		case <-ctx.Done():
			break schedule
		}
	}
	close(tasks)
	wg.Wait()
	return inserted.Load(), firstErr
}

func insertRange(ctx context.Context, db *sql.DB, offset, count int64) error {
	const columns = 31
	var sqlText strings.Builder
	sqlText.Grow(int(count)*80 + 1024)
	sqlText.WriteString(`insert into bp_interface_call_log(
		interface_id,user_id,api_code,request_method,request_params,request_value,request_fingerprint,
		response_status,response_body,response_size,response_saved_size,response_truncated,success,billable,
		charge_type,charge_amount,point_price,elapsed_ms,error_message,client_ip,client_region,
		ip_country,ip_province,ip_city,ip_isp,latitude,longitude,trace_id,api_key_id,user_visible,created_at
	) values `)
	placeholders := "(" + strings.TrimRight(strings.Repeat("?,", columns), ",") + ")"
	args := make([]any, 0, int(count)*columns)
	shape := benchmarkShape()
	for i := int64(0); i < count; i++ {
		if i > 0 {
			sqlText.WriteByte(',')
		}
		sqlText.WriteString(placeholders)
		sequence := offset + i
		interfaceID := int64(900001) + sequence%shape.Interfaces
		userID := int64(900000001) + sequence%shape.Users
		ipIndex := sequence % shape.IPs
		clientIP := syntheticIP(ipIndex)
		country, province, city, isp, latitude, longitude := syntheticRegion(ipIndex)
		success := 1
		status := 200
		errorMessage := ""
		if sequence%17 == 0 {
			success = 0
			status = 429
			errorMessage = "benchmark simulated rate limit"
		}
		createdAt := time.Now().UTC().Add(-time.Duration(sequence%(shape.Hours*3600)) * time.Second).Truncate(time.Second)
		apiCode := []string{"bench.weather", "bench.ip", "bench.video", "bench.sms", "bench.translate"}[interfaceID-900001]
		fingerprint := fmt.Sprintf("%064x", sequence%4096)
		traceID := fmt.Sprintf("bench-trace-%010d", sequence)
		apiKeyID := int64(700001) + sequence%200
		args = append(args,
			interfaceID, userID, apiCode, "GET", "", "", fingerprint,
			status, "", 256+sequence%8192, 0, 1, success, 1,
			"BALANCE", "0.0100", 0, 5+sequence%700, errorMessage, clientIP, province+city,
			country, province, city, isp, latitude, longitude, traceID, apiKeyID, 1, createdAt,
		)
	}
	_, err := db.ExecContext(ctx, sqlText.String(), args...)
	return err
}

func requireEmptyCallLog(ctx context.Context, db *sql.DB) error {
	var count int64
	if err := db.QueryRowContext(ctx, "select count(*) from bp_interface_call_log").Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return fmt.Errorf("bp_interface_call_log still contains %d non-benchmark rows; use a schema-only disposable database", count)
	}
	return nil
}

func benchmarkShape() syntheticShape {
	return syntheticShape{Interfaces: 5, Users: 5, IPs: 4096, Hours: 24 * 35}
}

func syntheticIP(index int64) string {
	return fmt.Sprintf("198.18.%d.%d", (index/254)%256, index%254+1)
}

func syntheticRegion(index int64) (string, string, string, string, float64, float64) {
	regions := []struct {
		country, province, city, isp string
		lat, lng                     float64
	}{
		{"中国", "广东省", "深圳市", "中国电信", 22.543096, 114.057865},
		{"中国", "北京市", "北京市", "中国联通", 39.904200, 116.407396},
		{"中国", "台湾省", "台北市", "中华电信", 25.033000, 121.565400},
		{"中国", "香港特别行政区", "香港", "PCCW", 22.319300, 114.169400},
		{"中国", "澳门特别行政区", "澳门", "CTM", 22.198700, 113.543900},
		{"美国", "California", "San Francisco", "Benchmark ISP", 37.774900, -122.419400},
		{"德国", "Berlin", "Berlin", "Benchmark ISP", 52.520000, 13.405000},
		{"日本", "東京都", "東京", "Benchmark ISP", 35.676200, 139.650300},
	}
	region := regions[index%int64(len(regions))]
	return region.country, region.province, region.city, region.isp, region.lat, region.lng
}

func analyticsQueries(now time.Time) []benchmarkQuery {
	dayStart := now.Add(-24 * time.Hour).Truncate(time.Minute)
	monthStart := now.Add(-30 * 24 * time.Hour).Truncate(time.Hour)
	monthDate := now.AddDate(0, 0, -29).Format("2006-01-02")
	activeUser := `case when u.status=0 and u.ban_permanent=0 and u.ban_until is not null and u.ban_until<=now() then 1 else u.status end=1`
	return []benchmarkQuery{
		{
			Name: "homepage_snapshot_read",
			SQL: `select cast(payload as char)
				from bp_home_overview_snapshot
				where id=1 and source_updated_at is not null`,
		},
		{
			Name: "homepage_overview_24h",
			SQL: `select coalesce(sum(m.call_count),0),coalesce(sum(m.success_count),0),
				coalesce(sum(m.failure_count),0),coalesce(sum(m.response_bytes),0),
				coalesce(sum(m.elapsed_sum),0)
				from bp_call_user_metric_minute m join bp_user u on u.id=m.user_id
				where m.bucket_start>=? and ` + activeUser,
			Args: []any{dayStart},
		},
		{
			Name: "homepage_hot_apis_30d_daily",
			SQL: `select d.interface_id,sum(d.call_count) calls
				from bp_call_api_metric_day d join bp_user u on u.id=d.user_id
				where d.bucket_date>=? and ` + activeUser + `
				group by d.interface_id order by calls desc,d.interface_id limit 8`,
			Args: []any{monthDate},
		},
		{
			Name: "admin_dashboard_hot_apis_24h",
			SQL: `select m.interface_id,max(m.api_code) api_code,sum(m.call_count) calls,
				sum(m.success_count) successes,round(sum(m.elapsed_sum)/nullif(sum(m.call_count),0),2) avg_ms
				from bp_call_api_metric_minute m join bp_user u on u.id=m.user_id
				where m.bucket_start>=? and ` + activeUser + `
				group by m.interface_id order by calls desc,m.interface_id limit 20`,
			Args: []any{dayStart},
		},
		{
			Name: "admin_access_situation_30d",
			SQL: `select r.country,r.province,r.city,r.isp,r.latitude,r.longitude,
				sum(r.call_count) calls,sum(r.unique_ip_count) unique_ip_estimate,
				sum(r.success_count) successes,sum(r.failure_count) failures
				from bp_call_region_hour r join bp_user u on u.id=r.user_id
				where r.bucket_start>=? and ` + activeUser + `
				group by r.country,r.province,r.city,r.isp,r.latitude,r.longitude
				order by calls desc limit 200`,
			Args: []any{monthStart},
		},
		{
			Name: "risk_failure_ip_ranking_24h",
			SQL: `select f.client_ip,sum(f.failure_count) failures,count(distinct f.user_id) users,
				count(distinct f.interface_id) interfaces,max(f.last_seen) last_seen
				from bp_call_failure_ip_hour f join bp_user u on u.id=f.user_id
				where f.bucket_start>=? and ` + activeUser + `
				group by f.client_ip order by failures desc,f.client_ip limit 100`,
			Args: []any{dayStart},
		},
	}
}

func callLogListQueries(now time.Time) []benchmarkQuery {
	userID := int64(900000001)
	interfaceID := int64(900001)
	apiKeyID := int64(700001)
	windowStart := now.Add(-31 * 24 * time.Hour).Format("2006-01-02 15:04:05")
	nowText := now.Format("2006-01-02 15:04:05")
	seekBefore := now.Add(-24 * time.Hour).Format("2006-01-02 15:04:05")
	like := "%bench%"
	return []benchmarkQuery{
		{
			Name: "call_log_user_first_page",
			SQL: `select l.id from bp_interface_call_log l
				where l.user_id=? and l.user_visible=1
				order by l.created_at desc,l.id desc limit 20`,
			Args: []any{userID},
		},
		{
			Name: "call_log_user_interface_seek",
			SQL: `select l.id from bp_interface_call_log l
				where l.user_id=? and l.interface_id=?
				  and (l.created_at<? or (l.created_at=? and l.id<?))
				order by l.created_at desc,l.id desc limit 20`,
			Args: []any{userID, interfaceID, seekBefore, seekBefore, int64(1)},
		},
		{
			Name: "call_log_admin_offset_deep",
			SQL: `select l.id from bp_interface_call_log l
				order by l.created_at desc,l.id desc limit 20 offset 100000`,
		},
		{
			Name: "call_log_admin_api_key",
			SQL: `select l.id from bp_interface_call_log l
				where l.api_key_id=? and l.created_at>=?
				order by l.created_at desc,l.id desc limit 20`,
			Args: []any{apiKeyID, windowStart},
		},
		{
			Name: "call_log_admin_trace",
			SQL:  `select l.id from bp_interface_call_log l where l.trace_id=?`,
			Args: []any{"bench-trace-0000000042"},
		},
		{
			Name: "call_log_admin_client_ip",
			SQL: `select l.id from bp_interface_call_log l
				where l.client_ip=? and l.created_at>=?
				order by l.created_at desc,l.id desc limit 20`,
			Args: []any{syntheticIP(42), windowStart},
		},
		{
			Name: "call_log_admin_keyword_fuzzy",
			SQL: `select l.id from bp_interface_call_log l
				left join bp_interface_api a on a.id=l.interface_id
				left join bp_user u on u.id=l.user_id
				where l.created_at>=? and l.created_at<=?
				  and (u.username like ? or u.display_name like ? or a.name like ?
				       or a.api_code like ? or l.request_params like ? or l.client_ip like ? or l.trace_id like ?)
				order by l.created_at desc,l.id desc limit 20`,
			Args: []any{windowStart, nowText, like, like, like, like, like, like, like},
		},
		{
			Name: "call_log_user_interface_count",
			SQL: `select count(*) from bp_interface_call_log l
				where l.user_id=? and l.interface_id=? and l.created_at>=?`,
			Args: []any{userID, interfaceID, windowStart},
		},
	}
}

func benchmark(parent context.Context, db *sql.DB, timeout time.Duration, explain bool, query benchmarkQuery) (queryResult, error) {
	result := queryResult{Name: query.Name}
	rows, digest, cold, err := runAndConsume(parent, db, timeout, query.SQL, query.Args...)
	if err != nil {
		return result, err
	}
	result.Rows, result.Digest, result.Cold = rows, digest, cold
	rows, digest, hot, err := runAndConsume(parent, db, timeout, query.SQL, query.Args...)
	if err != nil {
		return result, err
	}
	if rows != result.Rows || digest != result.Digest {
		return result, errors.New("cold and hot query results differ")
	}
	result.Hot = hot
	if explain {
		plan, err := explainAnalyze(parent, db, timeout, query.SQL, query.Args...)
		if err != nil {
			return result, err
		}
		result.Plan = plan
	}
	return result, nil
}

func runAndConsume(parent context.Context, db *sql.DB, timeout time.Duration, statement string, args ...any) (int, uint64, time.Duration, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	started := time.Now()
	rows, err := db.QueryContext(ctx, statement, args...)
	if err != nil {
		return 0, 0, 0, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return 0, 0, 0, err
	}
	count := 0
	var digest uint64 = 1469598103934665603
	for rows.Next() {
		values := make([]sql.RawBytes, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return 0, 0, 0, err
		}
		for _, value := range values {
			for _, b := range value {
				digest ^= uint64(b)
				digest *= 1099511628211
			}
			digest ^= 0xff
			digest *= 1099511628211
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, 0, 0, err
	}
	return count, digest, time.Since(started), nil
}

func explainAnalyze(parent context.Context, db *sql.DB, timeout time.Duration, statement string, args ...any) ([]string, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	rows, err := db.QueryContext(ctx, "explain analyze "+statement, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var plans []string
	for rows.Next() {
		var plan string
		if err := rows.Scan(&plan); err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}
	return plans, rows.Err()
}

func printQueryResult(w io.Writer, result queryResult) {
	fmt.Fprintf(w, "QUERY=%s cold=%s hot=%s rows=%d digest=%016x\n",
		result.Name, result.Cold.Round(time.Microsecond), result.Hot.Round(time.Microsecond), result.Rows, result.Digest)
	for _, plan := range result.Plan {
		for _, line := range strings.Split(plan, "\n") {
			fmt.Fprintf(w, "EXPLAIN[%s] %s\n", result.Name, line)
		}
	}
}

func printRollupCardinality(ctx context.Context, db *sql.DB) error {
	tables := []string{
		"bp_call_metric_minute", "bp_call_api_metric_minute", "bp_call_api_metric_day", "bp_call_user_metric_minute",
		"bp_call_region_ip_hour", "bp_call_region_hour", "bp_call_failure_ip_hour", "bp_call_failure_user_hour",
		"bp_call_risk_fingerprint_5m",
	}
	sort.Strings(tables)
	for _, table := range tables {
		var count int64
		if err := db.QueryRowContext(ctx, "select count(*) from "+table).Scan(&count); err != nil {
			return err
		}
		fmt.Printf("rollup_table[%s]=%d\n", table, count)
	}
	return nil
}

var syntheticCleanupTruncateTables = []string{
	"bp_call_risk_fingerprint_5m",
	"bp_call_failure_user_hour",
	"bp_call_failure_ip_hour",
	"bp_call_region_hour",
	"bp_call_region_ip_hour",
	"bp_call_user_metric_minute",
	"bp_call_api_metric_day",
	"bp_call_api_metric_minute",
	"bp_call_metric_minute",
	"bp_home_overview_snapshot",
	"bp_interface_call_log",
}

func cleanupSynthetic(ctx context.Context, db *sql.DB) error {
	var foreignRows int
	if err := db.QueryRowContext(ctx, `
		select exists(
			select 1 from bp_interface_call_log
			where not (
				interface_id between 900001 and 900005
				and user_id between 900000001 and 900000005
			)
			limit 1
		)
	`).Scan(&foreignRows); err != nil {
		return err
	}
	if foreignRows != 0 {
		return errors.New("refusing benchmark cleanup because non-synthetic call logs exist")
	}
	// This command accepts only explicitly named test/e2e/bench databases and
	// refuses cleanup when any non-synthetic call log exists. TRUNCATE avoids a
	// multi-million-row undo log and long-running row locks in the disposable
	// benchmark schema while preserving the previous all-rollups reset contract.
	for _, table := range syntheticCleanupTruncateTables {
		if _, err := db.ExecContext(ctx, "truncate table "+table); err != nil {
			return fmt.Errorf("truncate benchmark table %s: %w", table, err)
		}
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "delete from bp_analytics_checkpoint where worker_key=?", analytics.CheckpointKey); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "delete from bp_interface_api where id between 900001 and 900005"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "delete from bp_user where id between 900000001 and 900000005"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "insert into bp_analytics_checkpoint(worker_key) values (?)", analytics.CheckpointKey); err != nil {
		return err
	}
	return tx.Commit()
}

func exitf(format string, args ...any) {
	// Database drivers may include an address, DSN fragment, or nested URL in
	// an error.  Benchmark failures are still useful after applying the same
	// endpoint and credential redaction policy as runtime logs.
	fmt.Fprintln(os.Stderr, sanitizeExitMessage(format, args...))
	os.Exit(2)
}

func sanitizeExitMessage(format string, args ...any) string {
	return masking.ErrorText(fmt.Sprintf(format, args...))
}
