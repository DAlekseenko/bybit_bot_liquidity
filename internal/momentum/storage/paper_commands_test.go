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

	"bybit_bot_liquidity/internal/momentum/config"
)

// paperCommandFixture создаёт отдельную БД и явный INIT нового paper namespace.
func paperCommandFixture(t *testing.T) (*sql.DB, PaperCommandRequest) {
	t.Helper()
	db, run := runRepositoryFixture(t)
	_, hash, _ := run.Config.Snapshot()
	return db, PaperCommandRequest{Scope: ControlScope{"paper", "paper/test", run.StrategyID},
		Config: &run.Config, ConfigHash: hash, Action: "INITIALIZE", Actor: "SYSTEM", ActorID: "system/test",
		CommandID: "init-1", Kind: "initialize", Reason: "explicit paper initialization"}
}

// paperRequire сверяет sentinel и нулевой результат ошибки без сырых SQL-диагностик.
func paperRequire(t *testing.T, db *sql.DB, in PaperCommandRequest, want error) PaperCommandState {
	t.Helper()
	got, err := ApplyPaperCommandState(context.Background(), db, in)
	migrationResult(t, err, want)
	if err != nil && got != (PaperCommandState{}) {
		t.Fatal("error returned applied state")
	}
	return got
}

// TestPaperInitialization проверяет атомарность, неизвестный commit, reopen и retry после risk.
func TestPaperInitialization(t *testing.T) {
	for _, committed := range []bool{false, true} {
		db, init := paperCommandFixture(t)
		ctx := context.Background()
		got, err := applyPaperCommandState(ctx, db, init, func(tx *sql.Tx) error {
			if committed {
				migrationResult(t, tx.Commit(), nil)
			}
			return errors.New("lost response")
		})
		migrationResult(t, err, ErrControl)
		if got != (PaperCommandState{}) {
			t.Fatal("unknown outcome claimed success")
		}
		for _, table := range []string{"config_snapshots", "control_revisions"} {
			want := "0"
			if committed {
				want = "1"
			}
			migrationValue(t, db, "SELECT count(*) FROM momentum."+table, want)
		}
		first := paperRequire(t, db, init, nil)
		if first.State != (ControlState{1, "ACTIVE"}) || first.ActorID != init.ActorID || first.CreatedAt.IsZero() || first.CreatedAt.Location() != time.UTC {
			t.Fatal("initial audit changed")
		}
		for i, action := range []string{"PAUSE_ENTRIES", "HALT"} {
			risk := RiskTransition{Scope: init.Scope, ExpectedRevision: uint64(i + 1), Action: action, Reason: "stale data", ConfigHash: init.ConfigHash,
				OccurredAt: time.Unix(1700000000, 0).UTC(), Events: []RiskEventRecord{{EventID: action, Kind: "STALE_DATA", Detail: "stale"}}}
			state, riskErr := AppendRiskTransition(ctx, db, risk)
			migrationResult(t, riskErr, nil)
			if paperRequire(t, db, init, nil) != first {
				t.Fatal("retry changed original audit")
			}
			latest, readErr := ReadPaperCommandState(ctx, db, init.Scope)
			if readErr != nil || latest.State != state || latest.ActorID != "" || latest.Reason != risk.Reason {
				t.Fatal("retry changed risk state or fabricated actor", readErr)
			}
		}
		u, _ := url.Parse(os.Getenv("MOMENTUM_TEST_POSTGRES_DSN"))
		u.Path, u.RawPath = "/"+migrationValue(t, db, "SELECT current_database()"), ""
		reopened, err := OpenMomentumPostgres(ctx, u.String())
		migrationResult(t, err, nil)
		if paperRequire(t, reopened, init, nil) != first {
			t.Fatal("reopen lost INIT identity")
		}
		migrationResult(t, reopened.Close(), nil)
		modified := init
		modified.Reason = "changed"
		paperRequire(t, db, modified, ErrControlConflict)
		modified.CommandID = "new-init"
		paperRequire(t, db, modified, ErrControlConflict)
		migrationValue(t, db, "SELECT count(*) FROM momentum.config_snapshots", "1")
		migrationValue(t, db, "SELECT count(*) FROM momentum.control_revisions", "3")
		migrationValue(t, db, "SELECT count(*) FROM momentum.rebalance_runs", "0")
	}
}

// TestPaperInitializationArguments отвергает unsupported и неверный INIT до I/O.
func TestPaperInitializationArguments(t *testing.T) {
	cfg := runRepositoryConfig(t, false)
	_, hash, _ := cfg.Snapshot()
	in := PaperCommandRequest{Scope: ControlScope{"paper", "paper/test", cfg.Values().StrategyID}, Config: &cfg, ConfigHash: hash,
		Action: "INITIALIZE", Actor: "SYSTEM", ActorID: "system", CommandID: "init", Kind: "initialize", Reason: "reason"}
	db, _ := sql.Open("postgres", "postgres://invalid.invalid/none")
	defer db.Close()
	for name, mutate := range map[string]func(*PaperCommandRequest){
		"non-init":        func(p *PaperCommandRequest) { p.Action, p.Actor, p.Kind = "HALT", "OPERATOR", "halt" },
		"actor":           func(p *PaperCommandRequest) { p.Actor = "OPERATOR" },
		"actor-id":        func(p *PaperCommandRequest) { p.ActorID = " " },
		"recovery":        func(p *PaperCommandRequest) { p.RecoveryID = "unexpected" },
		"scope":           func(p *PaperCommandRequest) { p.Scope.Environment = "testnet" },
		"account":         func(p *PaperCommandRequest) { p.Scope.AccountID = "real" },
		"strategy":        func(p *PaperCommandRequest) { p.Scope.StrategyID = "other" },
		"empty-id":        func(p *PaperCommandRequest) { p.CommandID = "" },
		"utf8":            func(p *PaperCommandRequest) { p.Reason = string([]byte{255}) },
		"nul":             func(p *PaperCommandRequest) { p.CommandID = "id\x00" },
		"trim":            func(p *PaperCommandRequest) { p.Reason = " reason" },
		"hash":            func(p *PaperCommandRequest) { p.ConfigHash = strings.ToUpper(hash) },
		"mismatch":        func(p *PaperCommandRequest) { p.ConfigHash = strings.Repeat("a", 64) },
		"kind":            func(p *PaperCommandRequest) { p.Kind = "halt" },
		"nil-config":      func(p *PaperCommandRequest) { p.Config = nil },
		"zero-config":     func(p *PaperCommandRequest) { p.Config = &config.Config{} },
		"uint-overflow":   func(p *PaperCommandRequest) { p.ExpectedRevision = math.MaxUint64 },
		"bigint-overflow": func(p *PaperCommandRequest) { p.ExpectedRevision = math.MaxInt64 },
		"revision":        func(p *PaperCommandRequest) { p.ExpectedRevision = 1 },
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
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = ApplyPaperCommandState(ctx, db, in)
	migrationResult(t, err, context.Canceled)
}

// TestPaperInitializationConcurrent проверяет duplicate INIT и конкурирующий другой config.
func TestPaperInitializationConcurrent(t *testing.T) {
	for _, duplicate := range []bool{false, true} {
		db, init := paperCommandFixture(t)
		other := init
		if !duplicate {
			cfg := runRepositoryConfig(t, true)
			_, other.ConfigHash, _ = cfg.Snapshot()
			other.Config, other.CommandID = &cfg, "other-init"
		}
		type result struct {
			state PaperCommandState
			err   error
		}
		start, done := make(chan struct{}), make(chan result, 2)
		for _, in := range []PaperCommandRequest{init, other} {
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
					t.Fatal("duplicate audit differs")
				}
				success, original = success+1, got.state
			} else {
				migrationResult(t, got.err, ErrControlConflict)
			}
		}
		if success != 1 && !duplicate || success != 2 && duplicate {
			t.Fatal("CAS/duplicate result", success)
		}
		migrationValue(t, db, "SELECT count(*) FROM momentum.control_revisions", "1")
		migrationValue(t, db, "SELECT count(*) FROM momentum.config_snapshots", "1")
	}
}

// TestPaperControlRead проверяет legacy/audit и fail-closed при повреждённом хранилище.
func TestPaperControlRead(t *testing.T) {
	for _, kind := range []string{"legacy", "operator", "bad-system", "bad-operator", "missing-config", "noncanonical-config"} {
		t.Run(kind, func(t *testing.T) {
			db, init := paperCommandFixture(t)
			payload, _, _ := init.Config.Snapshot()
			if kind == "noncanonical-config" {
				payload = append(payload, ' ')
			}
			_, err := db.Exec(`INSERT INTO momentum.config_snapshots(config_hash,payload) VALUES(sha256($1::bytea),$1)`, payload)
			migrationResult(t, err, nil)
			reason, command := "legacy reason", ""
			if kind == "bad-system" {
				reason, command = " {broken", ""
			}
			_, err = db.Exec(`INSERT INTO momentum.control_revisions(environment,account_id,strategy_id,revision,mode,action,actor,reason,config_hash,confirmation_id)
 VALUES('paper','paper/test',$1,1,'ACTIVE','INITIALIZE','SYSTEM',$2,sha256($3::bytea),NULLIF($4,''))`, init.Scope.StrategyID, reason, payload, command)
			migrationResult(t, err, nil)
			if kind == "operator" || kind == "bad-operator" {
				reason = `{"format":"momentum/paper-control-audit/v1","command_id":"manual","actor_id":"human","kind":"halt","reason":"manual halt"}`
				if kind == "bad-operator" {
					reason = "{broken"
				}
				_, err = db.Exec(`INSERT INTO momentum.control_revisions(environment,account_id,strategy_id,revision,mode,action,actor,reason,config_hash,confirmation_id)
 VALUES('paper','paper/test',$1,2,'HALTED','HALT','OPERATOR',$2,sha256($3::bytea),'manual')`, init.Scope.StrategyID, reason, payload)
				migrationResult(t, err, nil)
			}
			if kind == "missing-config" {
				_, err = db.Exec(`ALTER TABLE momentum.control_revisions DROP CONSTRAINT control_revisions_config_hash_fkey; DELETE FROM momentum.config_snapshots`)
				migrationResult(t, err, nil)
			}
			state, err := ReadPaperCommandState(context.Background(), db, init.Scope)
			switch kind {
			case "legacy":
				if err != nil || state.ActorID != "" || state.Reason != reason {
					t.Fatal("legacy actor fabricated", err)
				}
			case "operator":
				if err != nil || state.ActorID != "human" || state.Kind != "halt" || state.Reason != "manual halt" {
					t.Fatal("valid operator audit rejected", err)
				}
			default:
				migrationResult(t, err, ErrControl)
				paperRequire(t, db, init, ErrControl)
			}
		})
	}
}

// waitPaperBlock требует фактического lock wait указанного типа в PostgreSQL.
func waitPaperBlock(t *testing.T, ctx context.Context, db *sql.DB, relation, mode string) {
	t.Helper()
	for ctx.Err() == nil {
		var blocked bool
		err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks l WHERE l.relation=$1::regclass
 AND NOT l.granted AND l.mode=$2 AND cardinality(pg_blocking_pids(l.pid))>0)`, relation, mode).Scan(&blocked)
		migrationResult(t, err, nil)
		if blocked {
			return
		}
	}
	t.Fatal("expected PostgreSQL lock wait absent")
}

// TestPaperControlRiskFences проверяет guard→public risk и public risk→guard.
func TestPaperControlRiskFences(t *testing.T) {
	for _, riskFirst := range []bool{false, true} {
		db, init := paperCommandFixture(t)
		paperRequire(t, db, init, nil)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		holder, err := db.BeginTx(ctx, nil)
		migrationResult(t, err, nil)
		defer holder.Rollback()
		if riskFirst {
			_, err = holder.ExecContext(ctx, "LOCK TABLE momentum.risk_events IN ACCESS EXCLUSIVE MODE")
			migrationResult(t, err, nil)
		} else {
			migrationResult(t, lockPaperControls(ctx, holder, false), nil)
		}
		risk := RiskTransition{Scope: init.Scope, ExpectedRevision: 1, Action: "HALT", Reason: "stale data", ConfigHash: init.ConfigHash,
			OccurredAt: time.Unix(1700000000, 0).UTC(), Events: []RiskEventRecord{{EventID: "risk", Kind: "STALE_DATA", Detail: "stale"}}}
		riskDone := make(chan error, 1)
		go func() {
			_, e := AppendRiskTransition(ctx, db, risk)
			riskDone <- e
		}()
		guardDone := make(chan error, 1)
		if riskFirst {
			waitPaperBlock(t, ctx, db, "momentum.risk_events", "RowExclusiveLock")
			go func() {
				tx, e := db.BeginTx(ctx, nil)
				if e == nil {
					defer tx.Rollback()
					e = lockPaperControls(ctx, tx, false)
					if e == nil {
						state, readErr := readPaperCommand(ctx, tx, init.Scope, "")
						e = readErr
						if e == nil && (state.State.Mode != "HALTED" || state.ActorID != "") {
							e = errors.New("guard read stale mode")
						}
					}
				}
				guardDone <- e
			}()
			waitPaperBlock(t, ctx, db, "momentum.control_revisions", "ShareLock")
		} else {
			waitPaperBlock(t, ctx, db, "momentum.control_revisions", "RowExclusiveLock")
		}
		migrationResult(t, holder.Commit(), nil)
		migrationResult(t, <-riskDone, nil)
		if riskFirst {
			migrationResult(t, <-guardDone, nil)
		}
	}
}
