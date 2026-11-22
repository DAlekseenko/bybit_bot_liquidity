package storage

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// paperNext задаёт самостоятельную подтверждённую команду без смены конфигурации.
func paperNext(in PaperCommandRequest, action, kind, id string, revision uint64) PaperCommandRequest {
	in.Config, in.Actor, in.Action, in.Kind, in.CommandID = nil, "OPERATOR", action, kind, id
	in.ExpectedRevision, in.Confirmed = revision, true
	return in
}

// TestPaperCommands проверяет audit/replay/rollback, config binding и запрет обхода HALT.
func TestPaperCommands(t *testing.T) {
	db, init := paperCommandFixture(t)
	ctx := context.Background()
	first := paperRequire(t, db, init, nil)
	if first.State != (ControlState{1, "ACTIVE"}) || first.ActorID != init.ActorID || first.CreatedAt.IsZero() || first.CreatedAt.Location() != time.UTC {
		t.Fatal("initial audit changed")
	}
	if paperRequire(t, db, init, nil) != first {
		t.Fatal("INIT retry changed original audit")
	}
	pause := paperNext(init, "PAUSE_ENTRIES", "pause", "pause-1", 1)
	_, err := applyPaperCommandState(ctx, db, pause, func(*sql.Tx) error { return errors.New("injected") })
	migrationResult(t, err, ErrControl)
	migrationValue(t, db, "SELECT count(*) FROM momentum.control_revisions", "1")
	_, err = applyPaperCommandState(ctx, db, pause, func(tx *sql.Tx) error {
		migrationResult(t, tx.Commit(), nil)
		return errors.New("lost commit response")
	})
	migrationResult(t, err, ErrControl)
	paused := paperRequire(t, db, pause, nil)
	if paused.State != (ControlState{2, "PAUSED_ENTRIES"}) || paused.ActorID != pause.ActorID {
		t.Fatal("pause audit mismatch")
	}
	_, err = applyPaperCommandState(ctx, db, pause, func(*sql.Tx) error {
		t.Error("replay called mutation callback")
		return nil
	})
	migrationResult(t, err, nil)
	halt := paperNext(init, "HALT", "close-all", "close-1", 2)
	stopped := paperRequire(t, db, halt, nil)
	if stopped.State.Mode != "HALTED" || stopped.Kind != "close-all" || paperRequire(t, db, pause, nil) != paused {
		t.Fatal("HALT or historical retry changed")
	}
	for name, mutate := range map[string]func(*PaperCommandRequest){
		"action":      func(p *PaperCommandRequest) { p.Action, p.Kind = "HALT", "halt" },
		"config":      func(p *PaperCommandRequest) { p.ConfigHash = strings.Repeat("a", 64) },
		"actor":       func(p *PaperCommandRequest) { p.ActorID = "other" },
		"reason":      func(p *PaperCommandRequest) { p.Reason = "other" },
		"predecessor": func(p *PaperCommandRequest) { p.ExpectedRevision = 2 },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := pause
			mutate(&candidate)
			paperRequire(t, db, candidate, ErrControlConflict)
		})
	}
	paperRequire(t, db, paperNext(init, "PAUSE_ENTRIES", "pause", "pause-2", 3), ErrControlConflict)
	paperRequire(t, db, paperNext(init, "INITIALIZE", "initialize", "bypass", 3), ErrControlArgument)
	paperRequire(t, db, init, nil)
	resume := paperNext(init, "RESUME", "resume", "resume-1", 3)
	resume.RecoveryID = "paper-proof/1"
	resumed := paperRequire(t, db, resume, nil)
	changed := resume
	changed.RecoveryID = "other-proof"
	paperRequire(t, db, changed, ErrControlConflict)
	u, _ := url.Parse(os.Getenv("MOMENTUM_TEST_POSTGRES_DSN"))
	u.Path, u.RawPath = "/"+migrationValue(t, db, "SELECT current_database()"), ""
	reopened, err := OpenMomentumPostgres(ctx, u.String())
	migrationResult(t, err, nil)
	defer reopened.Close()
	read, err := ReadPaperCommandState(ctx, reopened, init.Scope)
	migrationResult(t, err, nil)
	if read != resumed || read.State != (ControlState{4, "ACTIVE"}) || read.RecoveryID != resume.RecoveryID || paperRequire(t, reopened, init, nil) != first {
		t.Fatal("reopen lost command identity")
	}
	migrationValue(t, db, "SELECT count(*) FROM momentum.config_snapshots", "1")
	migrationValue(t, db, "SELECT count(*) FROM momentum.rebalance_runs", "0")
	migrationValue(t, db, "SELECT count(*) FROM momentum.control_revisions", "4")
	badScope := pause
	badScope.Scope.AccountID = "paper/missing"
	paperRequire(t, db, badScope, ErrControlConflict)
	cfg := runRepositoryConfig(t, true)
	payload, otherHash, _ := cfg.Snapshot()
	_, err = db.Exec(`INSERT INTO momentum.config_snapshots(config_hash,payload) VALUES(decode($1,'hex'),$2)`, otherHash, payload)
	migrationResult(t, err, nil)
	_, err = db.Exec(`INSERT INTO momentum.control_revisions(environment,account_id,strategy_id,revision,mode,action,actor,reason,config_hash)
 VALUES('paper','paper/test',$1,5,'HALTED','HALT','RISK_CONTROLLER','changed snapshot',decode($2,'hex'))`, init.Scope.StrategyID, otherHash)
	migrationResult(t, err, nil)
	paperRequire(t, db, pause, ErrControlConflict)
	_, err = db.Exec(`ALTER TABLE momentum.control_revisions DROP CONSTRAINT control_revisions_config_hash_fkey; DELETE FROM momentum.config_snapshots`)
	migrationResult(t, err, nil)
	paperRequire(t, db, pause, ErrControl)
	migrationValue(t, db, "SELECT count(*) FROM momentum.control_revisions", "5")
}

// TestPaperCommandArguments отвергает неподтверждённые и небумажные запросы до I/O.
func TestPaperCommandArguments(t *testing.T) {
	cfg := runRepositoryConfig(t, false)
	_, hash, _ := cfg.Snapshot()
	in := PaperCommandRequest{Scope: ControlScope{"paper", "paper/test", cfg.Values().StrategyID}, ConfigHash: hash,
		Action: "RESUME", Actor: "OPERATOR", ActorID: "user", CommandID: "resume", Kind: "resume", Reason: "reason", RecoveryID: "proof", ExpectedRevision: 1, Confirmed: true}
	db, _ := sql.Open("postgres", "postgres://invalid.invalid/none")
	defer db.Close()
	for name, mutate := range map[string]func(*PaperCommandRequest){
		"confirm":         func(p *PaperCommandRequest) { p.Confirmed = false },
		"actor":           func(p *PaperCommandRequest) { p.Actor = "SYSTEM" },
		"actor-id":        func(p *PaperCommandRequest) { p.ActorID = " " },
		"recovery":        func(p *PaperCommandRequest) { p.RecoveryID = "" },
		"recovery-trim":   func(p *PaperCommandRequest) { p.RecoveryID = " proof" },
		"recovery-nul":    func(p *PaperCommandRequest) { p.RecoveryID = "proof\x00" },
		"hash-length":     func(p *PaperCommandRequest) { p.ConfigHash = "00" },
		"scope":           func(p *PaperCommandRequest) { p.Scope.Environment = "testnet" },
		"account":         func(p *PaperCommandRequest) { p.Scope.AccountID = "real" },
		"empty-id":        func(p *PaperCommandRequest) { p.CommandID = "" },
		"utf8":            func(p *PaperCommandRequest) { p.Reason = string([]byte{255}) },
		"nul":             func(p *PaperCommandRequest) { p.CommandID = "id\x00" },
		"trim":            func(p *PaperCommandRequest) { p.Reason = " reason" },
		"hash":            func(p *PaperCommandRequest) { p.ConfigHash = strings.ToUpper(hash) },
		"kind":            func(p *PaperCommandRequest) { p.Kind = "halt" },
		"config":          func(p *PaperCommandRequest) { p.Config = &cfg },
		"uint-overflow":   func(p *PaperCommandRequest) { p.ExpectedRevision = math.MaxUint64 },
		"bigint-overflow": func(p *PaperCommandRequest) { p.ExpectedRevision = math.MaxInt64 },
		"zero":            func(p *PaperCommandRequest) { p.ExpectedRevision = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := in
			mutate(&candidate)
			paperRequire(t, db, candidate, ErrControlArgument)
		})
	}
	var ctx context.Context
	_, err := ReadPaperCommandState(ctx, db, in.Scope)
	migrationResult(t, err, ErrControlArgument)
	_, err = ApplyPaperCommandState(ctx, db, in)
	migrationResult(t, err, ErrControlArgument)
}

// TestPaperCommandFences проверяет оба порядка actual risk/operator и read-mode fence.
func TestPaperCommandFences(t *testing.T) {
	for _, pair := range [][2]string{{"guard", "operator"}, {"operator", "guard"}, {"operator", "risk"}, {"risk", "operator"}} {
		t.Run(pair[0]+"-"+pair[1], func(t *testing.T) {
			db, init := paperCommandFixture(t)
			paperRequire(t, db, init, nil)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			operator := paperNext(init, "PAUSE_ENTRIES", "pause", "pause-1", 1)
			risk := RiskTransition{Scope: init.Scope, ExpectedRevision: 1, Action: "HALT", Reason: "stale data", ConfigHash: init.ConfigHash,
				OccurredAt: time.Unix(1700000000, 0).UTC(), Events: []RiskEventRecord{{EventID: "risk-1", Kind: "STALE_DATA", Detail: "stale"}}}
			holder, err := db.BeginTx(ctx, nil)
			migrationResult(t, err, nil)
			defer holder.Rollback()
			holding := make(chan struct{})
			held := make(chan error, 1)
			release := make(chan struct{})
			switch pair[0] {
			case "guard":
				migrationResult(t, lockPaperControls(ctx, holder, false), nil)
			case "operator":
				go func() {
					_, e := applyPaperCommandState(ctx, db, operator, func(*sql.Tx) error {
						close(holding)
						select {
						case <-release:
							return nil
						case <-ctx.Done():
							return ctx.Err()
						}
					})
					held <- e
				}()
				select {
				case <-holding:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			case "risk":
				_, err = holder.ExecContext(ctx, "LOCK TABLE momentum.risk_events IN ACCESS EXCLUSIVE MODE")
				migrationResult(t, err, nil)
				go func() {
					_, e := AppendRiskTransition(ctx, db, risk)
					held <- e
				}()
				waitPaperBlock(t, ctx, db, "momentum.risk_events", "RowExclusiveLock")
			}
			done := make(chan error, 1)
			var guardMode string
			go func() {
				var e error
				switch pair[1] {
				case "operator":
					_, e = ApplyPaperCommandState(ctx, db, operator)
				case "risk":
					_, e = AppendRiskTransition(ctx, db, risk)
				case "guard":
					tx, beginErr := db.BeginTx(ctx, nil)
					e = beginErr
					if e == nil {
						defer tx.Rollback()
						e = lockPaperControls(ctx, tx, false)
						if e == nil {
							var state PaperCommandState
							state, e = readPaperCommand(ctx, tx, init.Scope, "")
							guardMode = state.State.Mode
						}
					}
				}
				done <- e
			}()
			mode := map[string]string{"operator": "ShareRowExclusiveLock", "risk": "RowExclusiveLock", "guard": "ShareLock"}[pair[1]]
			waitPaperBlock(t, ctx, db, "momentum.control_revisions", mode)
			if pair[0] == "operator" {
				close(release)
				migrationResult(t, <-held, nil)
			} else {
				migrationResult(t, holder.Commit(), nil)
				if pair[0] == "risk" {
					migrationResult(t, <-held, nil)
				}
			}
			want := error(nil)
			if pair[0] != "guard" && pair[1] != "guard" {
				want = ErrControlConflict
			}
			migrationResult(t, <-done, want)
			if pair[1] == "guard" && guardMode != map[string]string{"risk": "HALTED", "operator": "PAUSED_ENTRIES"}[pair[0]] {
				t.Fatal("guard did not read committed latest mode")
			}
		})
	}
}

// TestPaperCommandConcurrent проверяет CAS и exact duplicate на конкурентных запросах.
func TestPaperCommandConcurrent(t *testing.T) {
	for _, duplicate := range []bool{false, true} {
		db, init := paperCommandFixture(t)
		paperRequire(t, db, init, nil)
		pause := paperNext(init, "PAUSE_ENTRIES", "pause", "pause-1", 1)
		other := paperNext(init, "HALT", "halt", "halt-1", 1)
		if duplicate {
			other = pause
		}
		type result struct {
			state PaperCommandState
			err   error
		}
		start, done := make(chan struct{}), make(chan result, 2)
		for _, in := range []PaperCommandRequest{pause, other} {
			go func() {
				<-start
				state, err := ApplyPaperCommandState(context.Background(), db, in)
				done <- result{state, err}
			}()
		}
		close(start)
		success, original := 0, (PaperCommandState{})
		for range 2 {
			got := <-done
			if got.err == nil {
				if success > 0 && got.state != original {
					t.Fatal("duplicate returned different row")
				}
				success, original = success+1, got.state
			} else {
				migrationResult(t, got.err, ErrControlConflict)
			}
		}
		if success != 1 && !duplicate || success != 2 && duplicate {
			t.Fatal("CAS/duplicate result", success)
		}
		migrationValue(t, db, "SELECT count(*) FROM momentum.control_revisions", "2")
	}
}
