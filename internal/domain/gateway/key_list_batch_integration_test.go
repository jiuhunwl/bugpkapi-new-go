package gateway

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/pkg/testdb"

	_ "github.com/go-sql-driver/mysql"
)

func TestListKeysLoadsRelationCountsWithSingleDatabaseConnection(t *testing.T) {
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
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	userID := time.Now().UnixNano()
	prefix := fmt.Sprintf("batch-key-%d", userID)
	if _, err := db.ExecContext(ctx, "insert into bp_user(id,username,password_hash,display_name,home_path,status) values(?,?,'test-only',?,'/console/workspace',1)", userID, prefix, prefix); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec("delete from bp_user where id=?", userID) })
	var keyIDs []int64
	t.Cleanup(func() {
		if len(keyIDs) == 0 {
			return
		}
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(keyIDs)), ",")
		args := make([]any, len(keyIDs))
		for index, id := range keyIDs {
			args[index] = id
		}
		_, _ = db.Exec("delete from bp_api_key_interface where key_id in ("+placeholders+")", args...)
		_, _ = db.Exec("delete from bp_api_key_ip_whitelist where key_id in ("+placeholders+")", args...)
		_, _ = db.Exec("delete from bp_api_key where id in ("+placeholders+")", args...)
	})

	for index := range 3 {
		name := fmt.Sprintf("%s-%d", prefix, index)
		digest := sha256.Sum256([]byte(name))
		result, err := db.ExecContext(ctx, `
			insert into bp_api_key(user_id,name,key_hash,key_prefix,key_suffix,key_type,status)
			values(?,?,?,?,'abcd','INTERFACE',1)
		`, userID, name, hex.EncodeToString(digest[:]), fmt.Sprintf("bp_%02d", index))
		if err != nil {
			t.Fatal(err)
		}
		keyID, err := result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		keyIDs = append(keyIDs, keyID)
		for relationIndex := 0; relationIndex <= index%2; relationIndex++ {
			if _, err := db.ExecContext(ctx, "insert into bp_api_key_interface(key_id,interface_id) values(?,?)", keyID, int64(1000+relationIndex)); err != nil {
				t.Fatal(err)
			}
		}
		for relationIndex := 0; relationIndex < index; relationIndex++ {
			if _, err := db.ExecContext(ctx, "insert into bp_api_key_ip_whitelist(key_id,ip_rule) values(?,?)", keyID, fmt.Sprintf("192.0.2.%d", relationIndex+1)); err != nil {
				t.Fatal(err)
			}
		}
	}

	repo := NewRepository(db)
	result, err := repo.ListKeys(ctx, userID, true, KeyListParams{Keyword: prefix, Page: 1, PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 3 || result.Total != 3 {
		t.Fatalf("key list = total %d items %d", result.Total, len(result.Items))
	}
	for _, item := range result.Items {
		if item.InterfaceCount < 1 || item.InterfaceCount > 2 || item.WhitelistCount < 0 || item.WhitelistCount > 2 {
			t.Fatalf("invalid relation counts for %s: interfaces=%d whitelist=%d", item.Name, item.InterfaceCount, item.WhitelistCount)
		}
	}
	item, err := repo.KeyByID(ctx, keyIDs[2], userID, true)
	if err != nil {
		t.Fatal(err)
	}
	if item.InterfaceCount != 1 || item.WhitelistCount != 2 {
		t.Fatalf("detail relation counts = interfaces %d whitelist %d", item.InterfaceCount, item.WhitelistCount)
	}
}
