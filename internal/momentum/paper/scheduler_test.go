package paper

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/storage"
)

// fakeStore повторяет семантику storage: create-or-read по ключу и один итог на run.
type fakeStore struct {
	runs    map[string]storage.StoredRebalanceRun
	planned map[string]bool
	done    map[string]storage.RunCompletion
	creates int
}

// newFakeStore создаёт пустое хранилище.
func newFakeStore() *fakeStore {
	return &fakeStore{runs: map[string]storage.StoredRebalanceRun{}, planned: map[string]bool{}, done: map[string]storage.RunCompletion{}}
}

// CreateOrReadRebalanceRun возвращает существующий run по ID или создаёт новый.
func (f *fakeStore) CreateOrReadRebalanceRun(_ context.Context, in storage.RebalanceRunInput) (storage.StoredRebalanceRun, error) {
	if run, ok := f.runs[in.RunID]; ok {
		return run, nil
	}
	f.creates++
	run := storage.StoredRebalanceRun{RebalanceRunInput: in}
	f.runs[in.RunID] = run
	return run, nil
}

// ReadRunCompletion возвращает итог run.
func (f *fakeStore) ReadRunCompletion(_ context.Context, id string) (storage.RunCompletion, bool, error) {
	c, ok := f.done[id]
	return c, ok, nil
}

// ReadLatestPlanRevision сообщает только наличие плана.
func (f *fakeStore) ReadLatestPlanRevision(_ context.Context, id string) (storage.StoredPlanRevision, bool, error) {
	return storage.StoredPlanRevision{}, f.planned[id], nil
}

// CompleteRebalanceRun сохраняет первый итог.
func (f *fakeStore) CompleteRebalanceRun(_ context.Context, c storage.RunCompletion) (storage.RunCompletion, error) {
	if _, ok := f.done[c.RunID]; ok {
		return storage.RunCompletion{}, errors.New("conflict")
	}
	f.done[c.RunID] = c
	return c, nil
}

// ReadUnfinishedPlannedRun возвращает самый ранний run с планом и без итога.
func (f *fakeStore) ReadUnfinishedPlannedRun(_ context.Context, account, strategy string) (storage.StoredRebalanceRun, bool, error) {
	var found storage.StoredRebalanceRun
	ok := false
	for id, run := range f.runs {
		_, done := f.done[id]
		if f.planned[id] && !done && run.AccountID == account && run.StrategyID == strategy &&
			(!ok || run.ScheduledAt.Before(found.ScheduledAt)) {
			found, ok = run, true
		}
	}
	return found, ok, nil
}

// fakeData возвращает заданную границу закрытого среза или ошибку чтения.
type fakeData struct {
	through time.Time
	err     error
}

// ClosedThrough возвращает границу или ошибку.
func (d *fakeData) ClosedThrough(context.Context) (time.Time, error) { return d.through, d.err }

var monday = time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)

// testScheduler собирает расписание с конфигом paper и заданной политикой старта.
func testScheduler(t *testing.T, policy, onboarding string) (Scheduler, *fakeStore, *fakeData) {
	t.Helper()
	raw, err := os.ReadFile("../../../configs/momentum.paper.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse([]byte(strings.Replace(string(raw), "start_policy: next_monday", "start_policy: "+policy, 1)))
	if err != nil {
		t.Fatal(err)
	}
	store, data := newFakeStore(), &fakeData{}
	return Scheduler{Store: store, Data: data, Config: cfg, AccountID: "acc", OnboardingID: onboarding,
		Deadline: 6 * time.Hour, Poll: time.Minute}, store, data
}

// tick выполняет одно пробуждение и проверяет действие.
func tick(t *testing.T, s Scheduler, now time.Time, want Action) Decision {
	t.Helper()
	d, err := s.Tick(context.Background(), now)
	if err != nil || d.Action != want {
		t.Fatalf("tick %s: %+v %v, want %s", now, d, err, want)
	}
	return d
}

// TestSchedulerWeekly проверяет границу понедельника, повторные пробуждения, рестарт и следующую неделю.
func TestSchedulerWeekly(t *testing.T) {
	s, store, data := testScheduler(t, "next_monday", "")
	data.through = monday
	tick(t, s, monday.Add(-time.Second), ActionSkipped) // прошлая неделя без плана — пропуск, не catch-up
	d := tick(t, s, monday, ActionRun)
	if d.Resume || !d.Run.ScheduledAt.Equal(monday) || d.Run.OnboardingID != "" {
		t.Fatalf("weekly run=%+v", d)
	}
	tick(t, s, monday.Add(time.Minute), ActionRun)
	tick(t, s, monday.Add(time.Minute), ActionRun)
	store.planned[d.Run.RunID] = true
	if d = tick(t, s, monday.Add(7*time.Hour), ActionRun); !d.Resume {
		t.Fatal("restart after deadline must resume a planned run")
	}
	if store.creates != 2 {
		t.Fatalf("one record per slot, got %d", store.creates)
	}
	store.done[d.Run.RunID] = storage.RunCompletion{RunID: d.Run.RunID, Outcome: storage.RunCompleted}
	if d = tick(t, s, monday.Add(8*time.Hour), ActionWait); !d.WakeAt.Equal(monday.AddDate(0, 0, 7)) {
		t.Fatalf("wake=%s", d.WakeAt)
	}
}

// TestSchedulerLateData проверяет ожидание данных до дедлайна, пропуск недели и отказ от позднего запуска.
func TestSchedulerLateData(t *testing.T) {
	s, store, data := testScheduler(t, "next_monday", "")
	data.through = monday.Add(-24 * time.Hour)
	if d := tick(t, s, monday, ActionWait); !d.WakeAt.Equal(monday.Add(time.Minute)) {
		t.Fatalf("poll wake=%s", d.WakeAt)
	}
	if d := tick(t, s, monday.Add(6*time.Hour-30*time.Second), ActionWait); !d.WakeAt.Equal(monday.Add(6 * time.Hour)) {
		t.Fatalf("deadline wake=%s", d.WakeAt)
	}
	if store.creates != 0 {
		t.Fatal("no run record before data or deadline")
	}
	d := tick(t, s, monday.Add(6*time.Hour), ActionSkipped)
	if store.done[d.Run.RunID].Reason != SkipDeadlineMissed {
		t.Fatalf("skip=%+v", store.done[d.Run.RunID])
	}
	data.through = monday
	tick(t, s, monday.Add(7*time.Hour), ActionWait)
	if store.creates != 1 {
		t.Fatalf("skipped week must have one record, got %d", store.creates)
	}
}

// TestSchedulerOnboarding проверяет явный onboarding, запрет неявного первого входа и пропуск той же недели.
func TestSchedulerOnboarding(t *testing.T) {
	for _, tc := range []struct{ policy, onboarding string }{{"now", ""}, {"next_monday", "onb-1"}} {
		s, _, _ := testScheduler(t, tc.policy, tc.onboarding)
		if _, err := s.Tick(context.Background(), monday); !errors.Is(err, ErrSchedulerConfig) {
			t.Fatalf("%+v: want config error, got %v", tc, err)
		}
	}
	s, store, data := testScheduler(t, "now", "onb-1")
	wednesday := monday.Add(2*24*time.Hour + 3*time.Hour)
	data.through = wednesday.Add(-27 * time.Hour)
	tick(t, s, wednesday, ActionWait)
	data.through = monday.Add(2 * 24 * time.Hour)
	d := tick(t, s, wednesday.Add(time.Hour), ActionRun)
	if d.Run.OnboardingID != "onb-1" || !d.Run.ScheduledAt.Equal(wednesday) {
		t.Fatalf("onboarding run=%+v", d.Run)
	}
	store.done[d.Run.RunID] = storage.RunCompletion{RunID: d.Run.RunID, Outcome: storage.RunCompleted}
	tick(t, s, wednesday.Add(2*time.Hour), ActionWait)
	data.through = monday.AddDate(0, 0, 7)
	if d = tick(t, s, monday.AddDate(0, 0, 7), ActionRun); d.Run.OnboardingID != "" {
		t.Fatalf("next weekly run=%+v", d.Run)
	}
	if store.creates != 2 {
		t.Fatalf("records=%d, want onboarding + one weekly", store.creates)
	}
}

// TestSchedulerResumeUnfinished проверяет resume плана прошлой недели и сохранённого плана без готовых данных.
func TestSchedulerResumeUnfinished(t *testing.T) {
	s, store, data := testScheduler(t, "next_monday", "")
	data.through = monday
	d := tick(t, s, monday, ActionRun)
	store.planned[d.Run.RunID] = true
	nextMonday := monday.AddDate(0, 0, 7)
	data.through = nextMonday
	if got := tick(t, s, nextMonday.Add(time.Hour), ActionRun); got.Run.RunID != d.Run.RunID || !got.Resume {
		t.Fatalf("restart in a later week must resume the unfinished run, got %+v", got)
	}
	data.err = errors.New("daily data unavailable")
	if got := tick(t, s, monday.Add(time.Minute), ActionRun); got.Run.RunID != d.Run.RunID || !got.Resume {
		t.Fatalf("saved plan must resume before data readiness, got %+v", got)
	}
	if store.creates != 1 {
		t.Fatalf("resume must not create a new cycle, got %d records", store.creates)
	}
	store.done[d.Run.RunID] = storage.RunCompletion{RunID: d.Run.RunID, Outcome: storage.RunCompleted}
	data.err = nil
	if got := tick(t, s, nextMonday.Add(time.Hour), ActionRun); got.Resume || !got.Run.ScheduledAt.Equal(nextMonday) {
		t.Fatalf("after completion the current week starts, got %+v", got)
	}
}
