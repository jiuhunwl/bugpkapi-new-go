package analytics

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/pkg/testdb"
)

func TestLatestSourceProbesUseIndexTailAndPreserveEligibility(t *testing.T) {
	dsn := os.Getenv("BUGPK_ANALYTICS_ISOLATION_DSN")
	if dsn == "" {
		t.Skip("requires explicit disposable analytics MySQL")
	}
	db, _ := testdb.OpenIsolatedMySQL(t, dsn, os.Getenv("BUGPK_ANALYTICS_ISOLATION_DATADIR"), "bugpk_source_probe")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, q := range []string{
		"create table bp_user(id bigint primary key)",
		"insert into bp_user values(1)",
		`create table bp_interface_call_log(
			id bigint primary key auto_increment,user_id bigint,created_at datetime,charge_type varchar(32),
			success tinyint,response_status int,client_ip varchar(64),request_fingerprint varchar(64),
			key idx_bp_interface_call_log_created_id(created_at,id),
			key idx_bp_interface_call_log_success(success,created_at),
			key idx_bp_call_log_risk_window(created_at,charge_type,request_fingerprint,client_ip,user_id)
		) engine=InnoDB`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	values := strings.TrimSuffix(strings.Repeat("(1,date_sub(now(),interval 1 hour),'FREE',1,200,'203.0.113.1','history'),", 2000), ",")
	if _, err := db.ExecContext(ctx, "insert into bp_interface_call_log(user_id,created_at,charge_type,success,response_status,client_ip,request_fingerprint) values "+values); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"insert into bp_interface_call_log values(3001,1,date_sub(now(),interval 10 second),'FREE',0,500,'203.0.113.1','failure')",
		"insert into bp_interface_call_log values(3002,1,date_sub(now(),interval 2 second),'FREE',0,404,'203.0.113.1','not-found')",
		"insert into bp_interface_call_log values(3003,1,date_sub(now(),interval 1 second),'ADMIN',0,500,'203.0.113.1','admin')",
		"insert into bp_interface_call_log values(3004,999,now(),'FREE',0,500,'203.0.113.1','orphan')",
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	reads := func() uint64 {
		t.Helper()
		rows, err := conn.QueryContext(ctx, "show session status where variable_name in ('Handler_read_next','Handler_read_prev','Handler_read_rnd_next')")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var total uint64
		for rows.Next() {
			var name string
			var n uint64
			if err := rows.Scan(&name, &n); err != nil {
				t.Fatal(err)
			}
			total += n
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return total
	}
	for _, tc := range []struct {
		name, query string
		expectedID  int
	}{
		{"common", recentCommonSourceLatestQuery, 3002},
		{"failure-ip", recentFailureWithIPLatestQuery, 3001},
		{"failure", recentFailureLatestQuery, 3001},
		{"risk", recentRiskLatestQuery, 3002},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var expected, got time.Time
			if err := conn.QueryRowContext(ctx, "select created_at from bp_interface_call_log where id=?", tc.expectedID).Scan(&expected); err != nil {
				t.Fatal(err)
			}
			before := reads()
			if err := conn.QueryRowContext(ctx, tc.query, 3004).Scan(&got); err != nil {
				t.Fatal(err)
			}
			examined := reads() - before
			if !got.Equal(expected) {
				t.Fatalf("wrong eligible watermark: got=%s want=%s", got, expected)
			}
			if examined > 32 {
				t.Errorf("latest-row probe read %d index rows for a matching tail; must stop without scanning history", examined)
			}
			var empty sql.NullTime
			if err := conn.QueryRowContext(ctx, tc.query, 0).Scan(&empty); err != nil || empty.Valid {
				t.Fatalf("empty range must yield NULL, got=%+v err=%v", empty, err)
			}
			t.Logf("index rows examined=%d", examined)
		})
	}
	t.Run("expired-risk-does-not-trigger-repair", func(t *testing.T) {
		if _, err := conn.ExecContext(ctx, "update bp_interface_call_log set created_at=date_sub(now(),interval 2 day)"); err != nil {
			t.Fatal(err)
		}
		var latest sql.NullTime
		if err := conn.QueryRowContext(ctx, recentRiskLatestQuery, 3004).Scan(&latest); err != nil {
			t.Fatal(err)
		}
		if latest.Valid {
			t.Fatalf("expired risk rows would schedule an unfillable repair after pruning: %+v", latest)
		}
	})
}
