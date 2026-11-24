package dbx

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ErrCommitUncertain means a COMMIT failed and durable state could not establish
// its outcome. Callers must not blindly repeat non-idempotent mutations.
var ErrCommitUncertain = errors.New("transaction commit outcome is uncertain")

const CommitVerificationTimeout = 2 * time.Second

// CommitWithVerification never assumes a transport error means rollback. The
// verifier must use a durable witness and a locking read (or equivalent) so it
// cannot race an original transaction that is still committing/rolling back.
func CommitWithVerification(ctx context.Context, tx *sql.Tx, verify func(context.Context) (bool, error)) error {
	commitErr := tx.Commit()
	if commitErr == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), CommitVerificationTimeout)
	defer cancel()
	if verify == nil {
		return errors.Join(ErrCommitUncertain, commitErr)
	}
	committed, verifyErr := verify(checkCtx)
	if verifyErr != nil {
		return errors.Join(ErrCommitUncertain, commitErr, verifyErr)
	}
	if committed {
		return nil
	}
	return commitErr
}
