package revenue

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bugpk-api/internal/pkg/testdb"
	mysql "github.com/go-sql-driver/mysql"
)

var databaseSequence atomic.Uint64

func revenueTestDB(t *testing.T) (*sql.DB, *mysql.Config) {
	t.Helper()
	dsn := os.Getenv("BUGPK_REVENUE_TEST_DSN")
	if dsn == "" {
		t.Skip("requires dedicated BUGPK_REVENUE_TEST_DSN and data directory")
	}
	if err := testdb.ValidateIsolatedMySQLDSN(dsn); err != nil {
		t.Fatal(err)
	}
	expected := os.Getenv("BUGPK_REVENUE_TEST_DATADIR")
	if expected == "" {
		t.Fatal("test data directory must be explicit")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	host, _, err := net.SplitHostPort(cfg.Addr)
	if err != nil || host != "127.0.0.1" {
		t.Fatal("only an explicitly isolated loopback MySQL is allowed")
	}
	admin, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	var actual string
	if err := admin.QueryRow("select @@datadir").Scan(&actual); err != nil {
		t.Fatal(err)
	}
	norm := func(s string) string { return strings.TrimRight(strings.ReplaceAll(filepath.Clean(s), "\\", "/"), "/") }
	if !strings.EqualFold(norm(actual), norm(expected)) {
		t.Fatalf("refuse unexpected MySQL data directory: %s", actual)
	}
	name := fmt.Sprintf("bugpk_revenue_%d_%d_test", time.Now().UnixNano(), databaseSequence.Add(1))
	if _, err := admin.Exec("create database " + name + " character set utf8mb4 collate utf8mb4_unicode_ci"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, "drop database "+name); err != nil {
			t.Errorf("test database cleanup: %v", err)
		}
	})
	cfg.DBName = name
	cfg.MultiStatements = true
	cfg.ParseTime = true
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec("create table bp_billing_record(id bigint primary key,status varchar(32) not null,charge_amount decimal(12,4) not null default 0) engine=InnoDB; create table bp_billing_revenue_total(id tinyint primary key,confirmed_charge_amount decimal(38,4)) engine=InnoDB; insert into bp_billing_revenue_total values(1,999999.0000);")
	if err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "000098_idempotent_revenue_projection.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	return db, cfg
}
func mustRevenueSQL(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}
func expectRevenue(t *testing.T, repo *Repository, want string) {
	t.Helper()
	got, err := repo.Total(context.Background())
	if err != nil || got != want {
		t.Fatalf("revenue=%q err=%v want=%q", got, err, want)
	}
}

func TestRevenueIntegrationDurableRecoveryAndOutOfOrderConfirmation(t *testing.T) {
	db, _ := revenueTestDB(t)
	repo := NewRepository(db)
	mustRevenueSQL(t, db, "insert into bp_billing_record(id,status,charge_amount) values(1,'RESERVED',2.0000),(2,'CONFIRMED',1.2500),(3,'RELEASED',99.0000),(4,'CONFIRMED',0.0000)")
	expectRevenue(t, repo, "1.2500")
	if n, err := repo.Process(context.Background(), 1); err != nil || n != 1 {
		t.Fatalf("first batch=%d %v", n, err)
	}
	expectRevenue(t, repo, "1.2500")
	// An older reservation can confirm after newer IDs were already processed.
	mustRevenueSQL(t, db, "update bp_billing_record set status='CONFIRMED' where id=1")
	expectRevenue(t, NewRepository(db), "3.2500")
	for {
		n, err := NewRepository(db).Process(context.Background(), 1)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}
	for range 3 {
		if n, err := repo.ProcessRecord(context.Background(), 1); err != nil || n != 0 {
			t.Fatalf("duplicate projection=%d %v", n, err)
		}
	}
	expectRevenue(t, repo, "3.2500")
}

func TestRevenueIntegrationTransactionFailureRollsBackTotalAndMarkers(t *testing.T) {
	db, _ := revenueTestDB(t)
	repo := NewRepository(db)
	mustRevenueSQL(t, db, "insert into bp_billing_record(id,status,charge_amount) values(1,'CONFIRMED',3.2500)")
	mustRevenueSQL(t, db, "create trigger reject_marker before update on bp_billing_record for each row signal sqlstate '45000' set message_text='marker failure'")
	if _, err := repo.Process(context.Background(), 10); err == nil {
		t.Fatal("marker failure was ignored")
	}
	var value string
	var flag int
	if err := db.QueryRow("select cast(confirmed_charge_amount as char) from bp_billing_revenue_projection where id=1").Scan(&value); err != nil || value != "0.0000" {
		t.Fatalf("partial counter commit: %s %v", value, err)
	}
	if err := db.QueryRow("select revenue_projected from bp_billing_record where id=1").Scan(&flag); err != nil || flag != 0 {
		t.Fatal("partial marker commit")
	}
	expectRevenue(t, repo, "3.2500")
	mustRevenueSQL(t, db, "drop trigger reject_marker")
	if _, err := repo.Process(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	expectRevenue(t, repo, "3.2500")
}

func TestRevenueIntegrationConcurrentWorkersAndConsistentReads(t *testing.T) {
	db, _ := revenueTestDB(t)
	db.SetMaxOpenConns(16)
	for id := 1; id <= 60; id++ {
		mustRevenueSQL(t, db, "insert into bp_billing_record(id,status,charge_amount) values(?,'CONFIRMED',0.1000)", id)
	}
	repo := NewRepository(db)
	var workers sync.WaitGroup
	errs := make(chan error, 8)
	for range 6 {
		workers.Go(func() {
			for {
				n, err := repo.Process(context.Background(), 3)
				if err != nil {
					errs <- err
					return
				}
				if n == 0 {
					return
				}
			}
		})
	}
	for range 60 {
		expectRevenue(t, repo, "6.0000")
	}
	workers.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	expectRevenue(t, repo, "6.0000")
	var count int
	if err := db.QueryRow("select count(*) from bp_billing_record where revenue_projected=1").Scan(&count); err != nil || count != 60 {
		t.Fatalf("processed count=%d %v", count, err)
	}
}

type lostCommitConnection struct {
	net.Conn
	armed, observed *atomic.Bool
	beforeCommit    bool
	drop            atomic.Bool
	pending         bytes.Buffer
}

func (c *lostCommitConnection) Write(data []byte) (int, error) {
	if len(data) > 5 && data[4] == 3 && strings.EqualFold(strings.TrimSpace(string(data[5:])), "commit") && c.armed.CompareAndSwap(true, false) {
		if c.beforeCommit {
			c.observed.Store(true)
			c.Conn.Close()
			return 0, io.ErrClosedPipe
		}
		c.drop.Store(true)
	}
	return c.Conn.Write(data)
}
func (c *lostCommitConnection) Read(data []byte) (int, error) {
	if c.pending.Len() > 0 {
		return c.pending.Read(data)
	}
	if c.drop.Swap(false) {
		header := make([]byte, 4)
		if _, err := io.ReadFull(c.Conn, header); err != nil {
			return 0, err
		}
		size := int(header[0]) | int(header[1])<<8 | int(header[2])<<16
		if size < 1 || size > 1024*1024 {
			return 0, errors.New("invalid test COMMIT response")
		}
		payload := make([]byte, size)
		if _, err := io.ReadFull(c.Conn, payload); err != nil {
			return 0, err
		}
		if payload[0] == 0 {
			c.observed.Store(true)
			c.Conn.Close()
			return 0, io.ErrUnexpectedEOF
		}
		c.pending.Write(header)
		c.pending.Write(payload)
		return c.pending.Read(data)
	}
	return c.Conn.Read(data)
}
func TestRevenueIntegrationLostCommitAcknowledgementCannotDoubleCount(t *testing.T) {
	db, cfg := revenueTestDB(t)
	mustRevenueSQL(t, db, "insert into bp_billing_record(id,status,charge_amount) values(1,'CONFIRMED',1.2500)")
	var armed, observed atomic.Bool
	armed.Store(true)
	network := fmt.Sprintf("revenue-cut-%d", databaseSequence.Add(1))
	mysql.RegisterDialContext(network, func(ctx context.Context, address string) (net.Conn, error) {
		conn, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", address)
		if err != nil {
			return nil, err
		}
		return &lostCommitConnection{Conn: conn, armed: &armed, observed: &observed}, nil
	})
	t.Cleanup(func() { mysql.DeregisterDialContext(network) })
	cfg.Net = network
	faultDB, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { faultDB.Close() })
	if _, err := NewRepository(faultDB).Process(context.Background(), 10); err == nil {
		t.Fatal("lost COMMIT acknowledgement was not exposed")
	}
	if !observed.Load() {
		t.Fatal("fault did not consume a successful server COMMIT acknowledgement")
	}
	expectRevenue(t, NewRepository(db), "1.2500")
	if n, err := NewRepository(faultDB).Process(context.Background(), 10); err != nil || n != 0 {
		t.Fatalf("unknown commit was replayed: count=%d error=%v", n, err)
	}
	expectRevenue(t, NewRepository(db), "1.2500")
}

func TestRevenueIntegrationMissingProjectionAndMigrationRollbackRemainExact(t *testing.T) {
	db, _ := revenueTestDB(t)
	repo := NewRepository(db)
	mustRevenueSQL(t, db, "insert into bp_billing_record(id,status,charge_amount) values(1,'CONFIRMED',4.2500)")
	if _, err := repo.Process(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	mustRevenueSQL(t, db, "delete from bp_billing_revenue_projection where id=1")
	expectRevenue(t, repo, "4.2500")
	if _, err := repo.Process(context.Background(), 10); !errors.Is(err, ErrProjectionMissing) {
		t.Fatalf("missing metadata was silently reset: %v", err)
	}
	down, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "000098_idempotent_revenue_projection.down.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(down)); err != nil {
		t.Fatal(err)
	}
	expectRevenue(t, repo, "4.2500")
	var legacy string
	if err := db.QueryRow("select cast(confirmed_charge_amount as char) from bp_billing_revenue_total where id=1").Scan(&legacy); err != nil || legacy != "4.2500" {
		t.Fatalf("rollback left stale legacy total: %s %v", legacy, err)
	}
	up, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "000098_idempotent_revenue_projection.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(up)); err != nil {
		t.Fatal(err)
	}
	expectRevenue(t, repo, "4.2500")
	if _, err := repo.Process(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	expectRevenue(t, repo, "4.2500")

}

func TestRevenueIntegrationReaderCannotObserveHalfAppliedProjection(t *testing.T) {
	db, cfg := revenueTestDB(t)
	mustRevenueSQL(t, db, "insert into bp_billing_record(id,status,charge_amount) values(1,'CONFIRMED',9.5000)")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	gate, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Close()
	lockName := fmt.Sprintf("revenue-read-gate-%d", databaseSequence.Add(1))
	var acquired int
	if err := gate.QueryRowContext(ctx, "select get_lock(?,0)", lockName).Scan(&acquired); err != nil || acquired != 1 {
		t.Fatalf("lock fixture: %d %v", acquired, err)
	}
	released := false
	release := func() {
		if !released {
			gate.ExecContext(context.Background(), "do release_lock(?)", lockName)
			released = true
		}
	}
	defer release()
	mustRevenueSQL(t, db, "create trigger pause_projection before update on bp_billing_record for each row begin set @revenue_gate=get_lock('"+lockName+"',10); do release_lock('"+lockName+"'); end")
	writer, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	writer.SetMaxOpenConns(1)
	var writerID int64
	if err := writer.QueryRowContext(ctx, "select connection_id()").Scan(&writerID); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { _, err := NewRepository(writer).Process(ctx, 10); finished <- err }()
	waiting := false
	for until := time.Now().Add(2 * time.Second); time.Now().Before(until); {
		var state string
		if err := db.QueryRowContext(ctx, "select coalesce(state,'') from information_schema.processlist where id=?", writerID).Scan(&state); err == nil && strings.Contains(strings.ToLower(state), "user lock") {
			waiting = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !waiting {
		release()
		<-finished
		t.Fatal("writer did not pause between counter and marker updates")
	}
	reader, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	reader.SetMaxOpenConns(1)
	for _, level := range []string{"READ UNCOMMITTED", "READ COMMITTED", "REPEATABLE READ"} {
		if _, err := reader.ExecContext(ctx, "set session transaction isolation level "+level); err != nil {
			t.Fatal(err)
		}
		value, err := NewRepository(reader).Total(ctx)
		if err != nil || value != "9.5000" {
			t.Fatalf("%s reader saw half a transaction: %s %v", level, value, err)
		}
	}
	release()
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	expectRevenue(t, NewRepository(db), "9.5000")
}

func TestRevenueIntegrationPendingReadUsesCoveringIndex(t *testing.T) {
	db, _ := revenueTestDB(t)
	rows, err := db.Query("explain " + TotalQuery)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for rows.Next() {
		values := make([]sql.RawBytes, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			t.Fatal(err)
		}
		row := map[string]string{}
		for i, name := range columns {
			row[name] = string(values[i])
		}
		if row["table"] == "b" {
			found = true
			if row["key"] != "idx_bp_billing_revenue_pending" || !strings.Contains(strings.ToLower(row["Extra"]), "using index") {
				t.Fatalf("pending revenue read lost its covering index: %v", row)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("EXPLAIN did not include pending ledger read")
	}
}

func TestRevenueIntegrationConnectionLossBeforeCommitRecoversFromLedger(t *testing.T) {
	db, cfg := revenueTestDB(t)
	mustRevenueSQL(t, db, "insert into bp_billing_record(id,status,charge_amount) values(1,'CONFIRMED',1.2500)")
	var armed, observed atomic.Bool
	armed.Store(true)
	network := fmt.Sprintf("revenue-before-%d", databaseSequence.Add(1))
	mysql.RegisterDialContext(network, func(ctx context.Context, address string) (net.Conn, error) {
		conn, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", address)
		if err != nil {
			return nil, err
		}
		return &lostCommitConnection{Conn: conn, armed: &armed, observed: &observed, beforeCommit: true}, nil
	})
	t.Cleanup(func() { mysql.DeregisterDialContext(network) })
	cfg.Net = network
	faultDB, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { faultDB.Close() })
	if _, err := NewRepository(faultDB).Process(context.Background(), 10); err == nil {
		t.Fatal("connection loss before COMMIT was not exposed")
	}
	if !observed.Load() {
		t.Fatal("crash simulation did not intercept COMMIT")
	}
	var stored string
	var flag int
	if err := db.QueryRow("select cast(confirmed_charge_amount as char) from bp_billing_revenue_projection where id=1").Scan(&stored); err != nil || stored != "0.0000" {
		t.Fatalf("uncommitted total persisted: %s %v", stored, err)
	}
	if err := db.QueryRow("select revenue_projected from bp_billing_record where id=1").Scan(&flag); err != nil || flag != 0 {
		t.Fatalf("uncommitted marker persisted: %d %v", flag, err)
	}
	expectRevenue(t, NewRepository(db), "1.2500")
	if n, err := NewRepository(faultDB).Process(context.Background(), 10); err != nil || n != 1 {
		t.Fatalf("restart failed to recover work: %d %v", n, err)
	}
	expectRevenue(t, NewRepository(db), "1.2500")
}
