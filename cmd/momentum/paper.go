package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"flag"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/paper"
	"bybit_bot_liquidity/internal/momentum/paperwire"
	"bybit_bot_liquidity/internal/momentum/planner"
	s "bybit_bot_liquidity/internal/momentum/storage"
)

// paperRuntime связывает команды, часы и управляемое ожидание одного процесса.
type paperRuntime struct {
	now       func() time.Time
	wait      func(context.Context, time.Duration) error
	transport http.RoundTripper
	before    func(context.Context, paper.RecoveredPaper) error
}

type publicPaperTransport struct{ base http.RoundTripper }

// RoundTrip пропускает только публичные GET Bybit Testnet, в том числе при redirect.
func (t publicPaperTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method != http.MethodGet || r.URL.Scheme != "https" || r.URL.Host != "api-testnet.bybit.com" ||
		r.Host != "" && r.Host != r.URL.Host || r.URL.User != nil || r.URL.Opaque != "" || r.URL.RawPath != "" {
		return nil, errors.New("invalid paper public request")
	}
	switch r.URL.Path {
	case "/v5/market/time", "/v5/market/kline", "/v5/market/mark-price-kline", "/v5/market/funding/history":
	default:
		return nil, errors.New("invalid paper market endpoint")
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, errors.New("invalid paper market query")
	}
	for name := range query {
		switch name {
		case "category", "symbol", "interval", "limit", "start", "end", "startTime", "endTime":
		default:
			return nil, errors.New("invalid paper market parameter")
		}
	}
	for name := range r.Header {
		if strings.EqualFold(name, "Authorization") || strings.HasPrefix(strings.ToLower(name), "x-bapi-") {
			return nil, errors.New("invalid paper request header")
		}
	}
	if t.base == nil {
		return http.DefaultTransport.RoundTrip(r)
	}
	return t.base.RoundTrip(r)
}

// waitPaper ожидает следующий такт или отмену команды.
func waitPaper(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// serve проверяет явный профиль и управляет временем жизни пула и владельца.
func (rt paperRuntime) serve(ctx context.Context, args []string) (result error) {
	fs := flag.NewFlagSet("paper", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfgPath := fs.String("config", "", "")
	profilePath := fs.String("profile", "", "")
	account := fs.String("account", "", "")
	onboarding := fs.String("onboarding", "", "")
	dsn := os.Getenv("MOMENTUM_POSTGRES_DSN")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *cfgPath == "" || *profilePath == "" || !strings.HasPrefix(*account, "paper/") || len(*account) == len("paper/") || strings.TrimSpace(*account) != *account || dsn == "" {
		return errors.New("invalid paper command")
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil || (cfg.Values().StartPolicy == "now") != (*onboarding != "") || strings.TrimSpace(*onboarding) != *onboarding {
		return errors.New("invalid paper configuration")
	}
	file, err := os.Open(*profilePath)
	if err != nil {
		return errors.New("cannot open paper profile")
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, paperwire.MaxCanonicalBytes+1))
	profile, decodeErr := paperwire.DecodeProfile(raw)
	if err != nil || decodeErr != nil || paper.ValidatePaperModel(cfg, profile) != nil || profile.DataDeadline > 7*24*time.Hour {
		return errors.New("invalid paper profile")
	}
	db, err := s.OpenMomentumPostgres(ctx, dsn)
	if err != nil {
		return safeJournalError(ctx, "cannot open paper journal", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			result = errors.Join(result, errors.New("cannot close paper journal"))
		}
	}()
	if err = s.MigrateMomentumPostgres(ctx, db); err != nil {
		return safeJournalError(ctx, "cannot prepare paper journal", err)
	}
	scope := s.ControlScope{Environment: "paper", AccountID: *account, StrategyID: cfg.Values().StrategyID}
	host, err := os.Hostname()
	if err != nil {
		return errors.New("cannot identify paper host")
	}
	id := s.OwnerIdentity{Environment: scope.Environment, AccountID: scope.AccountID, StrategyID: scope.StrategyID, SessionID: rand.Text(), Host: host, ProcessID: int64(os.Getpid())}
	owner, err := s.AcquireExecutionOwner(ctx, db, id, rt.now().UTC())
	if err != nil {
		return safeJournalError(ctx, "cannot acquire paper owner", err)
	}
	defer func() {
		release, cancel := context.WithTimeout(context.Background(), profile.RPCTimeout)
		defer cancel()
		if e := owner.Release(release, rt.now().UTC()); e != nil {
			result = errors.Join(result, errors.New("cannot release paper owner"))
		}
	}()
	if err = paperControl(ctx, db, scope, cfg, owner); err != nil {
		return safeJournalError(ctx, "cannot verify paper control", err)
	}
	source := publicPaperTransport{rt.transport}
	r := paper.CycleRunner{RecoveryRequest: paper.RecoveryRequest{DB: db, Scope: scope, PlanBuilder: planner.BuildBaselinePaperPlan, Admission: planner.AdmitBaselineExecution}, Owner: owner, DailyBars: planner.BaselinePaperDailyBars, FormulaID: planner.BaselinePaperFormulaID, Now: rt.now, Transport: source}
	ready := paper.PublicReadiness{Config: cfg, DailyBars: r.DailyBars, RPCTimeout: profile.RPCTimeout, SourceMaxAge: profile.SourceMaxAge, Now: r.Now, Transport: r.Transport}
	scheduler := paper.Scheduler{Store: paper.DBRunStore{DB: db}, Data: ready, Config: cfg, AccountID: *account, OnboardingID: *onboarding, Deadline: profile.DataDeadline, Poll: profile.Poll}
	for {
		wake, err := paperWake(ctx, r, scheduler, profile, rt.before)
		if err != nil {
			return safeJournalError(ctx, "paper step failed", err)
		}
		if err = rt.wait(ctx, wake.Sub(rt.now())); err != nil {
			return safeJournalError(ctx, "paper wait ended", err)
		}
	}
}

// paperControl инициализирует только отсутствующую область и сверяет сохранённый config.
func paperControl(ctx context.Context, db *sql.DB, scope s.ControlScope, cfg config.Config, owner *s.ExecutionOwner) error {
	_, hash, err := cfg.Snapshot()
	if err != nil {
		return err
	}
	c, err := s.ReadPaperCommandState(ctx, db, scope)
	if errors.Is(err, s.ErrControlAbsent) {
		c, err = s.InitializeEmptyPaperControl(ctx, db, owner, s.PaperCommandRequest{Scope: scope, Config: &cfg, ConfigHash: hash, Action: "INITIALIZE", Actor: "SYSTEM", ActorID: "system/paper", CommandID: "paper-init", Kind: "initialize", Reason: "paper startup"})
	}
	if err == nil && c.ConfigHash != hash {
		return errors.New("paper config differs")
	}
	return err
}

// paperWake возобновляет сохранённый финансовый вход до выбора новой недели.
func paperWake(ctx context.Context, r paper.CycleRunner, scheduler paper.Scheduler, profile paperwire.Profile, before func(context.Context, paper.RecoveredPaper) error) (time.Time, error) {
	now := r.Now()
	if err := r.Owner.Heartbeat(ctx, now); err != nil {
		return time.Time{}, err
	}
	recovered, err := paper.Recover(ctx, r.RecoveryRequest)
	if err != nil {
		return time.Time{}, err
	}
	if r.Owner.NeedsReconciliation() {
		if err = paper.ReconcilePaperOwner(ctx, r.DB, r.Owner, recovered.Proof()); err != nil {
			return time.Time{}, err
		}
	}
	if before != nil {
		if err = before(ctx, recovered); err != nil {
			return time.Time{}, err
		}
		recovered, err = paper.Recover(ctx, r.RecoveryRequest)
		if err != nil {
			return time.Time{}, err
		}
	}
	if err = r.Owner.Permit(ctx); err != nil {
		return time.Time{}, err
	}
	next := now.Add(profile.Poll)
	if p, open := recovered.Current(); open {
		saved, err := paperwire.DecodeInputRecord(p.Payload())
		if err != nil {
			return time.Time{}, err
		}
		run, err := s.CreateOrReadRebalanceRun(ctx, r.DB, s.RebalanceRunInput(saved.Values().Run))
		if err != nil {
			return time.Time{}, err
		}
		_, err = r.AdvanceCycle(ctx, run, p.Purpose(), p.Profile())
		return next, err
	}
	dec, err := scheduler.TickRecovered(ctx, now, recovered)
	if err != nil {
		return next, err
	}
	switch dec.Action {
	case paper.ActionRun:
		_, err = r.AdvanceCycle(ctx, dec.Run, paperwire.Purpose{Kind: "rebalance"}, profile)
	case paper.ActionSkipped:
		row, found, e := s.ReadRunCompletion(ctx, r.DB, dec.Run.RunID)
		if e != nil {
			return time.Time{}, e
		}
		if !found || row.Outcome != s.RunSkipped || row.Reason != dec.Reason {
			return time.Time{}, errors.New("paper skip unconfirmed")
		}
	case paper.ActionWait:
	default:
		return time.Time{}, errors.New("unknown paper scheduler decision")
	}
	if !dec.WakeAt.IsZero() && dec.WakeAt.Before(next) {
		next = dec.WakeAt
	}
	return next, err
}
