package user

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/testdb"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// Run the existing full repair flow against an owned, disposable schema. Never
// infer credentials from app configuration, and never repurpose a caller DB.
func TestAdminOverviewIsolatedRepairFlow(t *testing.T) {
	dsn := os.Getenv("BUGPK_OVERVIEW_ISOLATION_DSN")
	if dsn == "" {
		t.Skip("requires explicit isolated overview MySQL")
	}
	db, cfg := testdb.OpenIsolatedMySQL(t, dsn, os.Getenv("BUGPK_OVERVIEW_ISOLATION_DATADIR"), "bugpk_overview")
	paths, err := filepath.Glob(filepath.Join("..", "..", "..", "migrations", "*.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	var schema strings.Builder
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		schema.Write(b)
		schema.WriteByte('\n')
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, schema.String()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BUGPK_MYSQL_TEST_DSN", cfg.FormatDSN())
	t.Run("all sections through real migration repair", TestAdminStatsRecoverRealDataAfterMigrationRepair)
	t.Run("missing projections keep independent zero metrics", func(t *testing.T) {
		if _, err := db.ExecContext(ctx, "drop table bp_call_metric_minute; drop table bp_call_user_metric_minute"); err != nil {
			t.Fatal(err)
		}
		svc := &Service{repo: NewRepository(db), now: time.Now}
		got, err := svc.loadAdminWorkbenchStats(ctx, []AdminWorkbenchSection{AdminWorkbenchSectionOverview}, "")
		if err != nil {
			t.Fatalf("independent zero metrics discarded during missing projection: %v", err)
		}
		if got.AnalyticsReady {
			t.Fatal("missing projections marked healthy")
		}
		missing := map[string]bool{}
		for _, field := range got.Overview.UnavailableFields {
			missing[field] = true
		}
		if !missing["todayCalls"] || !missing["todayRevenue"] || missing["totalUsers"] || missing["totalRevenue"] {
			t.Fatalf("wrong field isolation: %v", got.Overview.UnavailableFields)
		}
	})
	t.Run("unavailable pool makes full request retryable", func(t *testing.T) {
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		svc := &Service{repo: NewRepository(db), now: time.Now}
		_, err := svc.loadAdminWorkbenchStats(ctx, allAdminWorkbenchSections, "")
		var business *apperrors.BusinessError
		if !errors.As(err, &business) || business.StatusCode != consts.StatusServiceUnavailable {
			t.Fatalf("closed pool concealed by empty alert feed: %v", err)
		}
	})
}
