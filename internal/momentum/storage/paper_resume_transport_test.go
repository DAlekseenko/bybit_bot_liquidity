package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// TestResumeTransport проверяет подтверждение реальной командной транзакции при потере ответа.
func TestResumeTransport(t *testing.T) {
	lost := errors.New("ответ потерян")
	for _, tc := range []struct {
		name        string
		server      func(*sql.Tx) error
		reply, want error
	}{
		{"lost", (*sql.Tx).Commit, lost, nil}, {"current", (*sql.Tx).Commit, nil, ErrPaperCommandCurrentUnavailable}, {"absent", (*sql.Tx).Rollback, lost, ErrPaperFenceUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			db, init := paperCommandFixture(t)
			paperRequire(t, db, init, nil)
			paperRequire(t, db, paperNext(init, "PAUSE_ENTRIES", "pause", "pause", 1), nil)
			id := OwnerIdentity{SessionID: "new", Host: "test", ProcessID: 2, Environment: init.Scope.Environment, AccountID: init.Scope.AccountID, StrategyID: init.Scope.StrategyID}
			owner := acquireOwner(t, db, id, ownerT0)
			facts, err := ReadPaperRunFacts(ctx, db, init.Scope, "")
			migrationResult(t, err, nil)
			in := paperNext(init, "RESUME", "resume", "confirmed", 2)
			in.RecoveryID = strings.Repeat("a", 64)
			stamp := PaperRecoveryStamp{Scope: init.Scope, ConfigHash: init.ConfigHash, RecoveryID: in.RecoveryID, OwnerSessionID: id.SessionID, ControlRevision: 2, DataHash: facts.DataHash}
			finish := func(tx *sql.Tx) error {
				migrationResult(t, tc.server(tx), nil)
				return tc.reply
			}
			pool := completionPool(t, db)
			var reader paperInputReader = pool
			if tc.name == "current" {
				reader = &outcomeBarrier{db: pool, beforeCurrent: cancel}
			}
			got, err := resumePaperCommand(ctx, db, in, owner, stamp, func(f PaperRunFacts) error {
				if f.HeadFound || len(f.Fills()) != 0 || f.DataHash != facts.DataHash || f.Control.State != (ControlState{3, "ACTIVE"}) {
					return errors.New("неверный пустой снимок")
				}
				return nil
			}, finish, reader)
			row, found, e := ReadPaperCommandOutcome(t.Context(), db, init.Scope, in.CommandID)
			migrationResult(t, e, nil)
			migrationResult(t, err, tc.want)
			expected := PaperResumeResult{Applied: row.Applied, Current: row.Current, AppliedConfirmed: found, PathKnown: tc.reply == nil}
			if tc.name == "current" {
				expected.Current = PaperCommandState{}
				migrationResult(t, err, context.Canceled)
			}
			planRepositoryCheck(t, found == (tc.name != "absent") && (!found || row.Matches(in)) && got == expected, "неверный путь или состояние команды")
		})
	}
}
