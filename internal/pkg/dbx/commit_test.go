package dbx

import (
	"bugpk-api/internal/pkg/testdb"
	"context"
	"database/sql/driver"
	"errors"
	"testing"
	"time"
)

type outcomeTx struct{ err error }

func (t outcomeTx) Commit() error { return t.err }
func (outcomeTx) Rollback() error { return nil }
func TestCommitVerificationKeepsDefiniteAndUnknownOutcomesSeparate(t *testing.T) {
	commitErr := errors.New("commit transport failed")
	probeErr := errors.New("state probe unavailable")
	for _, tc := range []struct {
		name                     string
		commitErr                error
		committed                bool
		probeErr                 error
		wantSuccess, wantUnknown bool
	}{
		{"ordinary success", nil, false, nil, true, false}, {"verified commit", commitErr, true, nil, true, false},
		{"verified rollback", commitErr, false, nil, false, false}, {"unknown", commitErr, false, probeErr, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := testdb.OpenStub(t, testdb.SQLStub{Begin: func(context.Context, driver.TxOptions) (driver.Tx, error) { return outcomeTx{tc.commitErr}, nil }})
			tx, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			called := false
			var probeCtx context.Context
			err = CommitWithVerification(ctx, tx, func(ctx context.Context) (bool, error) {
				called = true
				probeCtx = ctx
				deadline, ok := ctx.Deadline()
				if ctx.Err() != nil || !ok || time.Until(deadline) > CommitVerificationTimeout {
					t.Error("verification context is cancelled or unbounded")
				}
				return tc.committed, tc.probeErr
			})
			if (err == nil) != tc.wantSuccess || errors.Is(err, ErrCommitUncertain) != tc.wantUnknown {
				t.Fatalf("outcome misclassified: %v", err)
			}
			if called != (tc.commitErr != nil) {
				t.Fatal("unnecessary/missing outcome verification")
			}
			if probeCtx != nil && probeCtx.Err() == nil {
				t.Fatal("verification resources not released")
			}
			if !tc.wantSuccess && !errors.Is(err, tc.commitErr) {
				t.Fatal("original commit error lost")
			}
		})
	}
}
