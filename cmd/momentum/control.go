package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"time"

	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/paper"
	"bybit_bot_liquidity/internal/momentum/planner"
	s "bybit_bot_liquidity/internal/momentum/storage"
)

// runOperator открывает существующую базу для одной операторской команды.
func runOperator(ctx context.Context, args []string, out io.Writer) error {
	f := flag.NewFlagSet("paper command", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	cfgFile := f.String("config", "", "")
	account := f.String("account", "", "")
	cmd := paper.OperatorCommand{Kind: args[0]}
	f.StringVar(&cmd.ID, "id", "", "")
	f.StringVar(&cmd.ActorID, "actor", "", "")
	f.StringVar(&cmd.Reason, "reason", "", "")
	f.Uint64Var(&cmd.ExpectedRevision, "revision", 0, "")
	f.BoolVar(&cmd.Confirmed, "confirm", false, "")
	if f.Parse(args[1:]) != nil || f.NArg() != 0 || *cfgFile == "" || *account == "" {
		return paper.ErrOperatorCommand
	}
	cfg, err := config.Load(*cfgFile)
	if err != nil || cfg.Values().Mode != "paper" {
		return paper.ErrOperatorCommand
	}
	_, hash, err := cfg.Snapshot()
	if err != nil {
		return paper.ErrOperatorCommand
	}
	db, err := s.OpenMomentumPostgres(ctx, os.Getenv("MOMENTUM_POSTGRES_DSN"))
	if err != nil {
		return safeJournalError(ctx, "cannot open operator journal", err)
	}
	defer db.Close()
	scope := s.ControlScope{Environment: "paper", AccountID: *account, StrategyID: cfg.Values().StrategyID}
	svc := paper.OperatorService{RecoveryRequest: paper.RecoveryRequest{DB: db, Scope: scope, PlanBuilder: planner.BuildBaselinePaperPlan, Admission: planner.AdmitBaselineExecution}, ConfigHash: hash}
	svc.WithOwner = func(ctx context.Context, fn func(*s.ExecutionOwner) error) error {
		return withOperatorOwner(ctx, db, scope, fn)
	}
	var result paper.OperatorResult
	if cmd.Kind == "status" {
		if cmd != (paper.OperatorCommand{Kind: "status"}) {
			return paper.ErrOperatorCommand
		}
		result, err = svc.Status(ctx)
	} else {
		result, err = svc.Apply(ctx, cmd)
	}
	if result.State != "" {
		if e := json.NewEncoder(out).Encode(result); e != nil {
			return errors.New("cannot write operator output")
		}
	}
	if err != nil {
		for _, known := range []error{paper.ErrOperatorCloseInProgress, s.ErrOwnerBusy, s.ErrControlConflict, s.ErrPaperCommandCurrentUnavailable} {
			if errors.Is(err, known) {
				return known
			}
		}
		return safeJournalError(ctx, "operator command incomplete", err)
	}
	return nil
}

// withOperatorOwner освобождает только собственную сессию после verified RESUME.
func withOperatorOwner(ctx context.Context, db *sql.DB, scope s.ControlScope, fn func(*s.ExecutionOwner) error) (result error) {
	host, err := os.Hostname()
	if err != nil {
		return errors.New("cannot identify paper operator host")
	}
	id := s.OwnerIdentity{Environment: scope.Environment, AccountID: scope.AccountID, StrategyID: scope.StrategyID, SessionID: "operator/" + rand.Text(), Host: host, ProcessID: int64(os.Getpid())}
	owner, err := s.AcquireExecutionOwner(ctx, db, id, time.Now().UTC())
	if err != nil {
		return err
	}
	defer func() {
		release, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		result = errors.Join(result, owner.Release(release, time.Now().UTC()))
	}()
	return fn(owner)
}
