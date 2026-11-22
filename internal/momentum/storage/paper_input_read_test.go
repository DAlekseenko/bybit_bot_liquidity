package storage

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/paperwire"
)

// TestPaperInputRead проверяет durable цепь, оба receipt и bounded point walk.
func TestPaperInputRead(t *testing.T) {
	db := openTestPostgres(t)
	ctx := context.Background()
	migrationResult(t, MigrateMomentumPostgres(ctx, db), nil)
	var previous paperwire.InputRecord
	var hash string
	var chain []paperwire.InputRecord
	for sequence := uint64(1); sequence <= 3; sequence++ {
		input := paperInputFixture(t, db, sequence, previous, hash, "paper/journal", true)
		paperInputSeed(t, db, input)
		if sequence < 3 {
			completion, receipt := paperInputCompletion(t, db, input, []string{"COMPLETED", "ABORTED"}[sequence-1])
			_, err := CompleteRebalanceRun(ctx, db, completion)
			migrationResult(t, err, nil)
			hash = receipt
		}
		chain = append(chain, input)
		previous = input
	}
	// Отделяем время записи от scheduled_at, чтобы проверить исходный SQL timestamp.
	tx, err := db.BeginTx(ctx, nil)
	migrationResult(t, err, nil)
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `SET LOCAL session_replication_role=replica; UPDATE momentum.paper_run_inputs SET created_at=created_at+interval '1 second'`)
	migrationResult(t, err, nil)
	migrationResult(t, tx.Commit(), nil)
	reopened := completionPool(t, db)
	scope := previous.Header().Scope
	for sequence := uint64(1); sequence <= 4; sequence++ {
		got, found, e := ReadPaperInput(ctx, reopened, scope, sequence)
		if e != nil || found != (sequence <= 3) || (found && (!got.CreatedAt.Equal(chain[sequence-1].Header().ScheduledAt.Add(time.Second)) || got.Input.Header().Sequence != sequence ||
			got.Input.Hash() != chain[sequence-1].Hash() || !bytes.Equal(got.Input.Payload(), chain[sequence-1].Payload()))) {
			t.Fatal("point/exact timestamp", sequence, e)
		}
	}
	got, found, err := ReadPaperInputHead(ctx, reopened, scope)
	if err != nil || !found || got.Input.Hash() != previous.Hash() || !got.CreatedAt.Equal(previous.Header().ScheduledAt.Add(time.Second)) || !bytes.Equal(got.Input.Payload(), previous.Payload()) {
		t.Fatal("head exact bytes/hash", err)
	}
	payload := got.Input.Payload()
	payload[0] = '!'
	values := got.Input.Values()
	delete(values.Before.Positions, values.Run.Config.Values().Symbols[0])
	if !bytes.Equal(got.Input.Payload(), previous.Payload()) || len(got.Input.Values().Before.Positions) != 3 {
		t.Fatal("copy isolation")
	}
	scope.AccountID = "paper/other"
	_, found, err = ReadPaperInputHead(ctx, reopened, scope)
	if err != nil || found {
		t.Fatal("scope isolation", err)
	}
}

// TestPaperInputReadCorruption проверяет selected rows после локального bypass в disposable TX.
func TestPaperInputReadCorruption(t *testing.T) {
	db := openTestPostgres(t)
	ctx := context.Background()
	migrationResult(t, MigrateMomentumPostgres(ctx, db), nil)
	first := paperInputFixture(t, db, 1, paperwire.InputRecord{}, "", "paper/journal", true)
	paperInputSeed(t, db, first)
	completion, hash := paperInputCompletion(t, db, first, "COMPLETED")
	_, err := CompleteRebalanceRun(ctx, db, completion)
	migrationResult(t, err, nil)
	child := paperInputFixture(t, db, 2, first, hash, "paper/journal", true)
	paperInputSeed(t, db, child)
	if first.Header().ConfigHash == child.Header().ConfigHash {
		t.Fatal("для проверки нужны два разных canonical config")
	}
	for name, mutation := range map[string]string{
		"malformed":                 `UPDATE momentum.paper_run_inputs SET payload=decode('7b','hex') WHERE sequence=2`,
		"noncanonical":              `UPDATE momentum.paper_run_inputs SET payload=payload||decode('20','hex') WHERE sequence=2`,
		"embedded-run":              `UPDATE momentum.paper_run_inputs SET payload=convert_to(replace(convert_from(payload,'UTF8'),'paper-run-2','paper-run-x'),'UTF8') WHERE sequence=2`,
		"embedded-scope":            `UPDATE momentum.paper_run_inputs SET payload=convert_to(replace(convert_from(payload,'UTF8'),'paper/journal','paper/unknown'),'UTF8') WHERE sequence=2`,
		"embedded-config":           `UPDATE momentum.paper_run_inputs SET payload=convert_to(replace(convert_from(payload,'UTF8'),'config_sha256','invalid_hash'),'UTF8') WHERE sequence=2`,
		"embedded-model":            `UPDATE momentum.paper_run_inputs SET payload=convert_to(replace(convert_from(payload,'UTF8'),'model_sha256','invalid_hash'),'UTF8') WHERE sequence=2`,
		"embedded-time":             `UPDATE momentum.paper_run_inputs SET payload=convert_to(replace(convert_from(payload,'UTF8'),'2023-11-15T00:13:20Z','2023-11-15T00:13:21Z'),'UTF8') WHERE sequence=2`,
		"embedded-sequence":         `UPDATE momentum.paper_run_inputs SET payload=convert_to(replace(convert_from(payload,'UTF8'),'"sequence":"2"','"sequence":"3"'),'UTF8') WHERE sequence=2`,
		"embedded-previous":         `UPDATE momentum.paper_run_inputs SET payload=convert_to(replace(convert_from(payload,'UTF8'),'previous_input_sha256','invalid_previous'),'UTF8') WHERE sequence=2`,
		"embedded-previous-receipt": `UPDATE momentum.paper_run_inputs SET payload=convert_to(replace(convert_from(payload,'UTF8'),'previous_resolution_sha256','invalid_previous'),'UTF8') WHERE sequence=2`,
		"weekly-onboarding-empty":   `UPDATE momentum.rebalance_runs SET onboarding_id='' WHERE run_id='paper-run-2'`,
		"weekly-onboarding-extra":   `UPDATE momentum.rebalance_runs SET onboarding_id='unexpected' WHERE run_id='paper-run-2'`,
		"prev-onboarding-changed":   `UPDATE momentum.rebalance_runs SET onboarding_id='other-start' WHERE run_id='paper-run-1'`,
		"first-input-link":          `DELETE FROM momentum.paper_run_inputs WHERE sequence=2; UPDATE momentum.paper_run_inputs SET previous_input_hash=decode('','hex')`,
		"first-receipt-link":        `DELETE FROM momentum.paper_run_inputs WHERE sequence=2; UPDATE momentum.paper_run_inputs SET previous_resolution_hash=decode('','hex')`,
		"canonical-config":          `UPDATE momentum.rebalance_runs SET config_hash=(SELECT config_hash FROM momentum.rebalance_runs WHERE run_id='paper-run-1') WHERE run_id='paper-run-2'`,
		"hash-consistent-noncanonical-config": `WITH changed AS (UPDATE momentum.config_snapshots
			SET payload=payload||decode('20','hex'),config_hash=sha256(payload||decode('20','hex'))
			WHERE config_hash=(SELECT config_hash FROM momentum.rebalance_runs WHERE run_id='paper-run-2') RETURNING config_hash)
			UPDATE momentum.rebalance_runs SET config_hash=(SELECT config_hash FROM changed) WHERE run_id='paper-run-2'`,
		"run-account":          `UPDATE momentum.rebalance_runs SET account_id='paper/other' WHERE run_id='paper-run-2'`,
		"run-strategy":         `UPDATE momentum.rebalance_runs SET strategy_id='other' WHERE run_id='paper-run-2'`,
		"run-time":             `UPDATE momentum.rebalance_runs SET scheduled_at=scheduled_at+interval '1 second' WHERE run_id='paper-run-2'`,
		"infinite-run":         `UPDATE momentum.rebalance_runs SET scheduled_at='infinity' WHERE run_id='paper-run-2'`,
		"infinite-prev-run":    `UPDATE momentum.rebalance_runs SET scheduled_at='-infinity' WHERE run_id='paper-run-1'`,
		"infinite-created":     `UPDATE momentum.paper_run_inputs SET created_at='infinity' WHERE sequence=2`,
		"numeric-fraction":     `UPDATE momentum.paper_run_inputs SET sequence=2.4 WHERE sequence=2`,
		"numeric-nan":          `UPDATE momentum.paper_run_inputs SET sequence='NaN' WHERE sequence=2`,
		"numeric-infinity":     `UPDATE momentum.paper_run_inputs SET sequence='Infinity' WHERE sequence=2`,
		"numeric-overflow":     `UPDATE momentum.paper_run_inputs SET sequence=18446744073709551616 WHERE sequence=2`,
		"sequence":             `UPDATE momentum.paper_run_inputs SET sequence=3 WHERE sequence=2`,
		"previous-hash":        `UPDATE momentum.paper_run_inputs SET previous_input_hash=decode(repeat('ab',32),'hex') WHERE sequence=2`,
		"decoded-receipt-hash": `UPDATE momentum.paper_run_inputs SET previous_resolution_hash=decode(repeat('ab',32),'hex') WHERE sequence=2`,
		"receipt-hash":         `UPDATE momentum.paper_run_inputs SET previous_resolution_hash=decode(repeat('ab',32),'hex') WHERE sequence=2`,
		"missing-receipt-hash": `UPDATE momentum.paper_run_inputs SET previous_resolution_hash=NULL WHERE sequence=2`,
		"config-bytes":         `UPDATE momentum.config_snapshots SET payload=payload||decode('20','hex')`,
		"run-config-hash":      `UPDATE momentum.rebalance_runs SET config_hash=decode(repeat('ab',32),'hex') WHERE run_id='paper-run-2'`,
		"missing-current-run":  `DELETE FROM momentum.rebalance_runs WHERE run_id='paper-run-2'`,
		"missing-config":       `DELETE FROM momentum.config_snapshots`,
		"missing-predecessor":  `DELETE FROM momentum.paper_run_inputs WHERE sequence=1`,
		"missing-prev-run":     `DELETE FROM momentum.rebalance_runs WHERE run_id='paper-run-1'`,
		"missing-prev-config":  `UPDATE momentum.rebalance_runs SET config_hash=decode(repeat('ab',32),'hex') WHERE run_id='paper-run-1'`,
		"missing-completion":   `DELETE FROM momentum.run_completions`,
		"plain-reason":         `UPDATE momentum.run_completions SET reason='plain reason'`,
	} {
		t.Run(name, func(t *testing.T) {
			tx, e := db.BeginTx(ctx, nil)
			migrationResult(t, e, nil)
			defer tx.Rollback()
			_, e = tx.ExecContext(ctx, `SET LOCAL session_replication_role=replica; DO $$ DECLARE c record; BEGIN FOR c IN SELECT conrelid::regclass AS tbl,conname FROM pg_constraint WHERE contype='c' AND connamespace='momentum'::regnamespace LOOP EXECUTE format('ALTER TABLE %s DROP CONSTRAINT %I',c.tbl,c.conname); END LOOP; END $$`)
			migrationResult(t, e, nil)
			_, e = tx.ExecContext(ctx, mutation+`; UPDATE momentum.paper_run_inputs SET input_hash=sha256(payload)`)
			migrationResult(t, e, nil)
			if name == "decoded-receipt-hash" {
				doc := child.Values()
				doc.PreviousResolutionHash = strings.Repeat("ab", 32)
				rebound, err := paperwire.EncodeInputRecord(doc)
				migrationResult(t, err, nil)
				_, err = tx.ExecContext(ctx, `UPDATE momentum.paper_run_inputs SET payload=$1,input_hash=sha256($1) WHERE sequence=2`, rebound.Payload())
				migrationResult(t, err, nil)
			}
			point := uint64(2)
			if name == "sequence" {
				point = 3
			} else if strings.HasPrefix(name, "first-") {
				point = 1
			}
			for _, sequence := range []uint64{0, point} {
				if sequence != 0 && strings.HasPrefix(name, "numeric-") {
					continue
				}
				got, found, e := readPaperInput(ctx, tx, child.Header().Scope, sequence)
				if !errors.Is(e, ErrPaperInputCorrupt) || found || got.Input.Hash() != "" || !got.CreatedAt.IsZero() {
					t.Fatal("corrupt selected row", e)
				}
			}
		})
	}
}

// TestPaperInputOpaqueModel проверяет paper binding при codec-valid входе и точном SQL hash.
func TestPaperInputOpaqueModel(t *testing.T) {
	db := openTestPostgres(t)
	ctx := context.Background()
	migrationResult(t, MigrateMomentumPostgres(ctx, db), nil)
	in := paperInputFixture(t, db, 1, paperwire.InputRecord{}, "", "paper/journal", false)
	decoded, err := paperwire.DecodeInputRecord(in.Payload())
	migrationResult(t, err, nil)
	if decoded.Header().ModelHash == decoded.Values().Run.Config.Values().ModelID {
		t.Fatal("для проверки нужен opaque ModelID")
	}
	paperInputSeed(t, db, in)
	got, found, err := ReadPaperInput(ctx, db, in.Header().Scope, 1)
	if !errors.Is(err, ErrPaperInputCorrupt) || found || got.Input.Hash() != "" || !got.CreatedAt.IsZero() {
		t.Fatal("opaque ModelID point", err)
	}
	got, found, err = ReadPaperInputHead(ctx, db, in.Header().Scope)
	if !errors.Is(err, ErrPaperInputCorrupt) || found || got.Input.Hash() != "" || !got.CreatedAt.IsZero() {
		t.Fatal("opaque ModelID head", err)
	}
}

// TestPaperInputReadArguments различает invalid, cancelled и недоступную БД.
func TestPaperInputReadArguments(t *testing.T) {
	db := openTestPostgres(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	valid := paperwire.Scope{Environment: "paper", AccountID: "paper/journal", StrategyID: "momentum"}
	for _, scope := range []paperwire.Scope{
		{}, {Environment: "live", AccountID: "paper/journal", StrategyID: "x"},
		{Environment: "paper", AccountID: "real", StrategyID: "x"}, {Environment: "paper", AccountID: "paper/", StrategyID: "x"},
		{Environment: "paper", AccountID: "paper/journal ", StrategyID: "x"}, {Environment: "paper", AccountID: "paper/x", StrategyID: " x"},
		{Environment: "paper", AccountID: "paper/x", StrategyID: "x\x00"},
	} {
		for _, head := range []bool{false, true} {
			var err error
			if head {
				_, _, err = ReadPaperInputHead(ctx, db, scope)
			} else {
				_, _, err = ReadPaperInput(ctx, db, scope, 1)
			}
			migrationResult(t, err, ErrPaperInputArgument)
		}
	}
	for _, invalid := range []struct {
		ctx context.Context
		db  *sql.DB
	}{
		{nil, db}, {context.Background(), nil},
	} {
		_, _, err := ReadPaperInputHead(invalid.ctx, invalid.db, valid)
		migrationResult(t, err, ErrPaperInputArgument)
		_, _, err = ReadPaperInput(invalid.ctx, invalid.db, valid, 1)
		migrationResult(t, err, ErrPaperInputArgument)
	}
	_, _, err := ReadPaperInput(context.Background(), db, valid, 0)
	migrationResult(t, err, ErrPaperInputArgument)
	for _, head := range []bool{false, true} {
		read := func(ctx context.Context) (StoredPaperInput, bool, error) {
			if head {
				return ReadPaperInputHead(ctx, db, valid)
			}
			return ReadPaperInput(ctx, db, valid, 1)
		}
		got, found, e := read(ctx)
		if !errors.Is(e, context.Canceled) || !errors.Is(e, ErrPaperInputUnavailable) || found || got.Input.Hash() != "" || !got.CreatedAt.IsZero() {
			t.Fatal("cancel", e)
		}
		got, found, e = read(context.Background())
		if !errors.Is(e, ErrPaperInputUnavailable) || e.Error() != ErrPaperInputUnavailable.Error() || found || got.Input.Hash() != "" || !got.CreatedAt.IsZero() {
			t.Fatal("SQL failure", e)
		}
	}
}

// TestPaperInputReadTransaction проверяет собственные input/completion и read-only TX.
func TestPaperInputReadTransaction(t *testing.T) {
	db := openTestPostgres(t)
	ctx := context.Background()
	migrationResult(t, MigrateMomentumPostgres(ctx, db), nil)
	first := paperInputFixture(t, db, 1, paperwire.InputRecord{}, "", "paper/journal", true)
	completion, hash := paperInputCompletion(t, db, first, "COMPLETED")
	child := paperInputFixture(t, db, 2, first, hash, "paper/journal", true)
	tx, err := db.BeginTx(ctx, nil)
	migrationResult(t, err, nil)
	defer tx.Rollback()
	paperInputSeed(t, tx, first)
	_, err = tx.ExecContext(ctx, `INSERT INTO momentum.run_completions(run_id,outcome,plan_revision,plan_hash,reason) VALUES($1,$2,1,decode($3,'hex'),$4)`, completion.RunID, completion.Outcome, completion.PlanHash, completion.Reason)
	migrationResult(t, err, nil)
	paperInputSeed(t, tx, child)
	got, found, err := readPaperInputHead(ctx, tx, child.Header().Scope)
	if err != nil || !found || got.Input.Hash() != child.Hash() {
		t.Fatal("own TX", err)
	}
	_, found, err = ReadPaperInputHead(ctx, completionPool(t, db), child.Header().Scope)
	if err != nil || found {
		t.Fatal("uncommitted visibility", err)
	}
	migrationResult(t, tx.Commit(), nil)
	ro, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	migrationResult(t, err, nil)
	defer ro.Rollback()
	got, found, err = readPaperInput(ctx, ro, child.Header().Scope, 2)
	if err != nil || !found || got.Input.Hash() != child.Hash() {
		t.Fatal("read only TX", err)
	}
}
