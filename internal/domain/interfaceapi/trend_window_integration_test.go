package interfaceapi

import (
	"context"
	"database/sql"
	"os"
	"strconv"
	"testing"
	"time"

	"bugpk-api/internal/pkg/testdb"
)

func TestInterfaceTrendQueryUsesTheRenderedDateWindow(t *testing.T) {
	dsn := os.Getenv("BUGPK_CALENDAR_OUTPUT_DSN")
	if dsn == "" {
		t.Skip("requires explicit isolated calendar MySQL")
	}
	db, cfg := testdb.OpenIsolatedMySQL(t, dsn, os.Getenv("BUGPK_CALENDAR_OUTPUT_DATADIR"), "bugpk_trendclock")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, "create table bp_user(id bigint primary key,status tinyint not null,ban_permanent tinyint not null default 1,ban_until datetime null); create table bp_call_api_metric_minute(interface_id bigint not null,user_id bigint not null,bucket_start datetime not null,call_count bigint not null,index idx_bp_call_api_metric_interface_time(interface_id,bucket_start)); insert into bp_user(id,status) values(1,1),(2,0)"); err != nil {
		t.Fatal(err)
	}
	reference := time.Now().In(time.FixedZone("expected +08", 8*60*60))
	for i := -8; i <= 3; i++ {
		day := reference.AddDate(0, 0, i).Format("2006-01-02")
		if _, err := db.ExecContext(ctx, "insert into bp_call_api_metric_minute values(42,1,?,1),(42,2,?,999),(43,1,?,777)", day, day, day); err != nil {
			t.Fatal(err)
		}
	}
	// Amplify the midnight scheduling gap: SQL's implicit today is later than
	// the renderer's captured today. Source rows still use the business clock.
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	cfg.Params["timestamp"] = strconv.FormatInt(reference.Add(48*time.Hour).Unix(), 10)
	shifted, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer shifted.Close()
	got, err := NewRepository(shifted).InterfaceTrend7d(ctx, 42)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 7 {
		t.Fatalf("not a seven-day window: %+v", got)
	}
	for _, day := range got {
		if day["value"] != int64(1) {
			t.Fatalf("rendered day lost data or included a foreign/inactive scope when DB clock advanced: %+v", got)
		}
	}
}
