// Package paper собирает paper runtime Momentum: расписание, данные, planner и
// виртуальные fills. Торговых вызовов биржи пакет не делает.
package paper

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/storage"
)

var (
	// ErrScheduler — общая категория ошибок расписания.
	ErrScheduler = errors.New("momentum scheduler failed")
	// ErrSchedulerConfig означает несовместимые параметры расписания.
	ErrSchedulerConfig = errors.New("invalid momentum scheduler configuration")
)

// Action — решение одного пробуждения расписания.
type Action string

const (
	// ActionWait — ничего не запускать до WakeAt.
	ActionWait Action = "WAIT"
	// ActionRun — исполнять или продолжать Run (Resume — план уже есть).
	ActionRun Action = "RUN"
	// ActionSkipped — неделя пропущена и зафиксирована SKIPPED, без catch-up.
	ActionSkipped Action = "SKIPPED"
)

// SkipDeadlineMissed — причина пропуска: общий закрытый срез не готов к дедлайну.
const SkipDeadlineMissed = "data_deadline_missed"

// SkipFinancialHistory означает, что слот уже перекрыт сохранённой финансовой историей.
const SkipFinancialHistory = "superseded_by_financial_history"

// Decision — результат Tick.
type Decision struct {
	Action Action
	Run    storage.StoredRebalanceRun
	Resume bool
	WakeAt time.Time
	Reason string
}

// RunStore — хранилище runs, нужное расписанию.
type RunStore interface {
	CreateOrReadRebalanceRun(ctx context.Context, input storage.RebalanceRunInput) (storage.StoredRebalanceRun, error)
	ReadRunCompletion(ctx context.Context, runID string) (storage.RunCompletion, bool, error)
	ReadLatestPlanRevision(ctx context.Context, runID string) (storage.StoredPlanRevision, bool, error)
	CompleteRebalanceRun(ctx context.Context, c storage.RunCompletion) (storage.RunCompletion, error)
	ReadUnfinishedPlannedRun(ctx context.Context, accountID, strategyID string) (storage.StoredRebalanceRun, bool, error)
}

// DataReadiness сообщает границу общего закрытого дневного среза: все символы
// имеют закрытые дневные свечи с open строго раньше возвращённого момента.
type DataReadiness interface {
	ClosedThrough(ctx context.Context) (time.Time, error)
}

// Scheduler решает, когда создавать weekly или onboarding run.
type Scheduler struct {
	Store        RunStore
	Data         DataReadiness
	Config       config.Config
	AccountID    string
	OnboardingID string        // обязателен для start_policy=now, запрещён для next_monday
	Deadline     time.Duration // от понедельника 00:00 UTC до отказа от недели
	Poll         time.Duration // интервал повторной проверки данных
}

// TickRecovered выбирает слот после проверки состояния под владельцем paper runtime.
func (s Scheduler) TickRecovered(ctx context.Context, now time.Time, recovered RecoveredPaper) (Decision, error) {
	proof := recovered.Proof()
	scope := storage.ControlScope{Environment: "paper", AccountID: s.AccountID, StrategyID: s.Config.Values().StrategyID}
	_, hash, err := s.Config.Snapshot()
	if _, open := recovered.Current(); open || err != nil || proof.ID() == "" || proof.Scope() != scope || proof.ConfigHash() != hash {
		return Decision{}, errors.Join(ErrScheduler, ErrSchedulerConfig)
	}
	head, _ := proof.Head()
	return s.tick(ctx, now, head.ScheduledAt)
}

// Tick принимает одно решение для момента now. Повторные пробуждения
// идемпотентны: ключ run детерминирован, поэтому на слот создаётся одна запись.
// Run с сохранённым планом и без итога (в том числе из прошлой недели)
// продолжается раньше выбора нового слота и без проверки готовности данных.
func (s Scheduler) Tick(ctx context.Context, now time.Time) (Decision, error) {
	return s.tick(ctx, now, time.Time{})
}

// tick учитывает последний завершённый финансовый слот при выборе нового запуска.
func (s Scheduler) tick(ctx context.Context, now, resolvedThrough time.Time) (Decision, error) {
	values := s.Config.Values()
	now = now.UTC()
	if ctx == nil || s.Store == nil || s.Data == nil || s.AccountID == "" || s.Deadline <= 0 || s.Poll <= 0 ||
		s.Deadline > 7*24*time.Hour || (values.StartPolicy == "now") != (s.OnboardingID != "") {
		return Decision{}, errors.Join(ErrScheduler, ErrSchedulerConfig)
	}
	if run, found, err := s.Store.ReadUnfinishedPlannedRun(ctx, s.AccountID, values.StrategyID); err != nil {
		return Decision{}, errors.Join(ErrScheduler, err)
	} else if found {
		return Decision{Action: ActionRun, Run: run, Resume: true}, nil
	}
	monday := mondayOf(now)
	next := monday.AddDate(0, 0, 7)
	if s.OnboardingID != "" {
		onboard, err := s.Store.CreateOrReadRebalanceRun(ctx, s.input("onboarding/", s.OnboardingID, now.Truncate(time.Microsecond)))
		if err != nil {
			return Decision{}, errors.Join(ErrScheduler, err)
		}
		if _, done, err := s.Store.ReadRunCompletion(ctx, onboard.RunID); err != nil {
			return Decision{}, errors.Join(ErrScheduler, err)
		} else if !done {
			return s.onboarding(ctx, now, onboard, resolvedThrough)
		}
		if !monday.After(onboard.ScheduledAt) {
			return Decision{Action: ActionWait, WakeAt: next}, nil
		}
	}
	weekly := s.input("weekly/", "", monday)
	if _, done, err := s.Store.ReadRunCompletion(ctx, weekly.RunID); err != nil {
		return Decision{}, errors.Join(ErrScheduler, err)
	} else if done {
		return Decision{Action: ActionWait, WakeAt: next}, nil
	}
	deadline := monday.Add(s.Deadline)
	superseded := !resolvedThrough.IsZero() && !weekly.ScheduledAt.After(resolvedThrough)
	if !superseded && now.Before(deadline) {
		ready, err := s.ready(ctx, monday)
		if err != nil || !ready {
			return Decision{Action: ActionWait, WakeAt: minTime(now.Add(s.Poll), deadline)}, err
		}
	}
	run, err := s.Store.CreateOrReadRebalanceRun(ctx, weekly)
	if err != nil {
		return Decision{}, errors.Join(ErrScheduler, err)
	}
	_, planned, err := s.Store.ReadLatestPlanRevision(ctx, run.RunID)
	if err != nil {
		return Decision{}, errors.Join(ErrScheduler, err)
	}
	if planned || (!superseded && now.Before(deadline)) {
		return Decision{Action: ActionRun, Run: run, Resume: planned}, nil
	}
	if superseded {
		return s.skip(ctx, run, SkipFinancialHistory, next)
	}
	return s.skip(ctx, run, SkipDeadlineMissed, next)
}

// skip фиксирует отсутствие плана и сверяет возвращённую запись до подтверждения решения.
func (s Scheduler) skip(ctx context.Context, run storage.StoredRebalanceRun, reason string, wake time.Time) (Decision, error) {
	row, err := s.Store.CompleteRebalanceRun(ctx, storage.RunCompletion{RunID: run.RunID, Outcome: storage.RunSkipped, Reason: reason})
	if err != nil {
		return Decision{}, errors.Join(ErrScheduler, err)
	}
	if row.RunID != run.RunID || row.Outcome != storage.RunSkipped || row.Reason != reason || row.PlanRevision != 0 || row.PlanHash != "" {
		return Decision{}, errors.Join(ErrScheduler, storage.ErrRunCompletionConflict)
	}
	return Decision{Action: ActionSkipped, Run: run, WakeAt: wake, Reason: reason}, nil
}

// onboarding возобновляет план, пропускает устаревший слот или ждёт закрытых данных.
func (s Scheduler) onboarding(ctx context.Context, now time.Time, run storage.StoredRebalanceRun, resolvedThrough time.Time) (Decision, error) {
	_, planned, err := s.Store.ReadLatestPlanRevision(ctx, run.RunID)
	if err != nil {
		return Decision{}, errors.Join(ErrScheduler, err)
	}
	if !planned {
		if !resolvedThrough.IsZero() && !run.ScheduledAt.After(resolvedThrough) {
			return s.skip(ctx, run, SkipFinancialHistory, now)
		}
		ready, err := s.ready(ctx, dayOf(run.ScheduledAt))
		if err != nil || !ready {
			return Decision{Action: ActionWait, WakeAt: now.Add(s.Poll)}, err
		}
	}
	return Decision{Action: ActionRun, Run: run, Resume: planned}, nil
}

// ready проверяет, что общий закрытый срез доходит до through.
func (s Scheduler) ready(ctx context.Context, through time.Time) (bool, error) {
	closed, err := s.Data.ClosedThrough(ctx)
	if err != nil {
		return false, errors.Join(ErrScheduler, err)
	}
	return !closed.Before(through), nil
}

// input строит детерминированный ключ run: weekly по понедельнику, onboarding по ID.
func (s Scheduler) input(prefix, onboarding string, at time.Time) storage.RebalanceRunInput {
	values := s.Config.Values()
	slot := at.Format(time.RFC3339)
	if onboarding != "" {
		slot = onboarding
	}
	return storage.RebalanceRunInput{RunID: prefix + values.StrategyID + "/" + s.AccountID + "/" + slot,
		AccountID: s.AccountID, StrategyID: values.StrategyID, OnboardingID: onboarding, ScheduledAt: at, Config: s.Config}
}

// mondayOf возвращает понедельник 00:00 UTC текущей недели.
func mondayOf(t time.Time) time.Time {
	day := dayOf(t)
	return day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7))
}

// dayOf возвращает начало UTC-дня.
func dayOf(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// minTime возвращает более ранний момент.
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// DBRunStore адаптирует функции storage к RunStore.
type DBRunStore struct{ DB *sql.DB }

// CreateOrReadRebalanceRun делегирует storage.CreateOrReadRebalanceRun.
func (d DBRunStore) CreateOrReadRebalanceRun(ctx context.Context, in storage.RebalanceRunInput) (storage.StoredRebalanceRun, error) {
	return storage.CreateOrReadRebalanceRun(ctx, d.DB, in)
}

// ReadRunCompletion делегирует storage.ReadRunCompletion.
func (d DBRunStore) ReadRunCompletion(ctx context.Context, runID string) (storage.RunCompletion, bool, error) {
	return storage.ReadRunCompletion(ctx, d.DB, runID)
}

// ReadLatestPlanRevision делегирует storage.ReadLatestPlanRevision.
func (d DBRunStore) ReadLatestPlanRevision(ctx context.Context, runID string) (storage.StoredPlanRevision, bool, error) {
	return storage.ReadLatestPlanRevision(ctx, d.DB, runID)
}

// ReadUnfinishedPlannedRun делегирует storage.ReadUnfinishedPlannedRun.
func (d DBRunStore) ReadUnfinishedPlannedRun(ctx context.Context, accountID, strategyID string) (storage.StoredRebalanceRun, bool, error) {
	return storage.ReadUnfinishedPlannedRun(ctx, d.DB, accountID, strategyID)
}

// CompleteRebalanceRun делегирует storage.CompleteRebalanceRun.
func (d DBRunStore) CompleteRebalanceRun(ctx context.Context, c storage.RunCompletion) (storage.RunCompletion, error) {
	return storage.CompleteRebalanceRun(ctx, d.DB, c)
}
