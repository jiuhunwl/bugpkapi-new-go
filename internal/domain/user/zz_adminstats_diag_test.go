package user

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"bugpk-api/internal/pkg/testdb"

	_ "github.com/go-sql-driver/mysql"
)

func TestZZAdminStatsDiagnostic(t *testing.T) {
	dsn := os.Getenv("BUGPK_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("BUGPK_MYSQL_TEST_DSN is not configured")
	}
	if err := testdb.ValidateIsolatedMySQLDSN(dsn); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	repo := NewRepository(db)
	o, err := repo.AdminOverview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	trend, err := repo.AdminCallTrend(ctx, dayLabels(time.Now(), 7))
	if err != nil {
		t.Fatal(err)
	}
	hot, err := repo.AdminHotAPIs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	users, err := repo.AdminActiveUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	access, locations, err := repo.AdminAccess(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("DIAG overview=%+v\ntrend=%+v\nhot=%+v\nusers=%+v\naccess=%+v locations=%+v\n", o, trend, hot, users, access, locations)
	for name, query := range map[string]string{
		"clock":     `select now(),current_date(),@@session.time_zone,@@system_time_zone`,
		"migration": `select version,dirty from schema_migrations`,
		"checkpoint": `select worker_key,last_log_id,source_rows,contribution_rows,last_error,
			coalesce(repair_status,''),coalesce(repair_window_days,0),repair_requested_at,repair_started_at,repair_completed_at
			from bp_analytics_checkpoint`,
		"logs": `select count(*),coalesce(max(id),0),coalesce(sum(created_at>=current_date()),0),
			min(created_at),max(created_at) from bp_interface_call_log`,
		"eligible": `select count(*),coalesce(sum(l.created_at>=current_date()),0)
			from bp_interface_call_log l inner join bp_user u on u.id=l.user_id
			where l.charge_type<>'ADMIN'
			and (case when u.status=0 and u.ban_permanent=0 and u.ban_until is not null and u.ban_until<=now() then 1 else u.status end)=1`,
		"metric": `select count(*),coalesce(sum(call_count),0),coalesce(sum(bucket_start>=current_date()),0),max(bucket_start) from bp_call_metric_minute`,
		"api":    `select count(*),coalesce(sum(call_count),0),coalesce(sum(bucket_start>=current_date()),0),max(bucket_start) from bp_call_api_metric_minute`,
		"user":   `select count(*),coalesce(sum(call_count),0),coalesce(sum(bucket_start>=current_date()),0),max(bucket_start) from bp_call_user_metric_minute`,
		"region": `select count(*),coalesce(sum(call_count),0),coalesce(sum(bucket_start>=current_date()),0),max(bucket_start) from bp_call_region_hour`,
	} {
		rows, queryErr := db.QueryContext(ctx, query)
		if queryErr != nil {
			fmt.Printf("DBDIAG %s ERROR=%v\n", name, queryErr)
			continue
		}
		columns, _ := rows.Columns()
		fmt.Printf("DBDIAG %s columns=%v\n", name, columns)
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for index := range values {
				pointers[index] = &values[index]
			}
			if scanErr := rows.Scan(pointers...); scanErr != nil {
				fmt.Printf("DBDIAG %s SCAN=%v\n", name, scanErr)
				break
			}
			fmt.Printf("DBDIAG %s values=%v\n", name, values)
		}
		_ = rows.Close()
	}
}
