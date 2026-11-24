package testdb

import (
	"context"
	"database/sql/driver"
	"testing"
)

func TestSQLStubRejectsUnexpectedIO(t *testing.T) {
	db := OpenStub(t, SQLStub{})
	if _, err := db.QueryContext(context.Background(), "SELECT unexpected"); err == nil {
		t.Fatal("missing query callback silently succeeded")
	}
	if _, err := db.ExecContext(context.Background(), "UPDATE unexpected"); err == nil {
		t.Fatal("missing write callback silently succeeded")
	}
}
func TestSQLStubRowsExerciseDatabaseScanningAndRejectIncompleteFixtures(t *testing.T) {
	db := OpenStub(t, SQLStub{Query: func(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
		return Rows([]driver.Value{int64(42), "admin"}, []driver.Value{int64(43)}), nil
	}})
	rows, err := db.QueryContext(context.Background(), "SELECT id,role")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var id int64
	var role string
	if !rows.Next() {
		t.Fatal("missing first row")
	}
	if err := rows.Scan(&id, &role); err != nil {
		t.Fatal(err)
	}
	if id != 42 || role != "admin" {
		t.Fatal("fixture scanning changed values")
	}
	if rows.Next() || rows.Err() == nil {
		t.Fatal("incomplete fixture silently reused values")
	}
}
