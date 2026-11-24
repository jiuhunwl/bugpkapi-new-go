package payment

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/testdb"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

type wechatConfigSaveTx struct {
	active             bool
	commits, rollbacks int
}

func (tx *wechatConfigSaveTx) Commit() error   { tx.active = false; tx.commits++; return nil }
func (tx *wechatConfigSaveTx) Rollback() error { tx.active = false; tx.rollbacks++; return nil }

func TestWechatConfigRetainedVerifierMatchesLockedSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name, requestedID, snapshotCipher, lockedID, lockedCipher string
		replace, conflict                                         bool
	}{
		{name: "same ID with a newer key", requestedID: "KEY_A", snapshotCipher: "cipher-old", lockedID: "KEY_A", lockedCipher: "cipher-new", conflict: true},
		{name: "inline key removed after validation", requestedID: "KEY_A", snapshotCipher: "cipher-old", lockedID: "KEY_A", lockedCipher: "", conflict: true},
		{name: "cache replaced by inline key", requestedID: "KEY_A", snapshotCipher: "", lockedID: "KEY_A", lockedCipher: "cipher-new", conflict: true},
		{name: "identity and key both rotated", requestedID: "KEY_A", snapshotCipher: "cipher-old", lockedID: "KEY_B", lockedCipher: "cipher-new", conflict: true},
		{name: "unchanged inline verifier", requestedID: "KEY_A", snapshotCipher: "cipher-old", lockedID: "KEY_A", lockedCipher: "cipher-old"},
		{name: "unchanged cache verifier", requestedID: "KEY_A", snapshotCipher: "", lockedID: "KEY_A", lockedCipher: ""},
		{name: "explicit replacement is an atomic pair", requestedID: "KEY_C", snapshotCipher: "cipher-submitted", lockedID: "KEY_B", lockedCipher: "cipher-new", replace: true},
	} {
		for _, updateKey := range []bool{false, true} {
			name := tc.name + "/secrets=" + map[bool]string{false: "retained", true: "updated"}[updateKey]
			t.Run(name, func(t *testing.T) {
				tx := &wechatConfigSaveTx{}
				reads, writes := 0, 0
				db := testdb.OpenStub(t, testdb.SQLStub{
					Begin: func(context.Context, driver.TxOptions) (driver.Tx, error) {
						tx.active = true
						return tx, nil
					},
					Query: func(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
						reads++
						if !tx.active || !strings.Contains(strings.ToLower(q), "for update") {
							t.Error("verifier was not read under the write transaction's row lock")
						}
						return testdb.Rows([]driver.Value{tc.lockedID, tc.lockedCipher}), nil
					},
					Exec: func(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
						writes++
						if !tx.active || reads != 1 || tx.commits != 0 {
							t.Error("save escaped its locked transaction")
						}
						compact := strings.ReplaceAll(strings.Join(strings.Fields(strings.ToLower(q)), ""), "`", "")
						if strings.Contains(compact, "platform_public_key_cipher=?") != tc.replace {
							t.Error("masked edit must not write ciphertext; explicit replacement must write it")
						}
						if tc.replace {
							hasID, hasCipher := false, false
							for _, arg := range args {
								if arg.Value == tc.requestedID {
									hasID = true
								}
								if arg.Value == tc.snapshotCipher {
									hasCipher = true
								}
							}
							if !hasID || !hasCipher {
								t.Error("replacement ID and key must be written together")
							}
						}
						return driver.RowsAffected(1), nil
					},
				})
				err := NewRepository(db).SaveWechatConfig(context.Background(), WechatConfig{PlatformPublicKeyID: tc.requestedID}, "v3-cipher", "merchant-cipher", tc.snapshotCipher, updateKey, tc.replace)
				if tc.conflict {
					if !errors.Is(err, ErrWechatVerifierStateChanged) || writes != 0 || tx.commits != 0 || tx.rollbacks != 1 {
						t.Fatalf("stale validated key was accepted: err=%v writes=%d commits=%d rollbacks=%d", err, writes, tx.commits, tx.rollbacks)
					}
				} else if err != nil || reads != 1 || writes != 1 || tx.commits != 1 || tx.rollbacks != 0 {
					t.Fatalf("valid save not committed atomically: err=%v reads=%d writes=%d commits=%d rollbacks=%d", err, reads, writes, tx.commits, tx.rollbacks)
				}
			})
		}
	}
}

type conflictingWechatSave struct{ *wechatCacheConfigStub }

func (*conflictingWechatSave) SaveWechatConfig(context.Context, WechatConfig, string, string, string, bool, bool) error {
	return ErrWechatVerifierStateChanged
}
func TestWechatConcurrentConfigSaveRequestsReload(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			service, repo, req := verifierConfigFixture(t)
			req.Enabled = enabled
			service.wechatConfigRepo = &conflictingWechatSave{repo}
			err := service.saveWechatConfig(context.Background(), req)
			business, ok := apperrors.AsBusinessError(err)
			if !ok || business.StatusCode != consts.StatusConflict || !errors.Is(err, ErrWechatVerifierStateChanged) {
				t.Fatalf("conflict not mapped to a reload response: %v", err)
			}
			if !strings.Contains(err.Error(), "重新加载") {
				t.Fatal("conflict lacks actionable reload guidance")
			}
		})
	}
}
