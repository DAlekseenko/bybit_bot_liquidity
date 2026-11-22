package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// unreachableDB открывает пул без подключения: с отменённым ctx запросы падают до сети.
func unreachableDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("postgres", "host=127.0.0.1 port=1 sslmode=disable connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestValidRunCompletion проверяет согласованность итога, ссылки на план и причины без PostgreSQL.
func TestValidRunCompletion(t *testing.T) {
	hash := strings.Repeat("ab", 32)
	for _, tc := range []struct {
		name string
		c    RunCompletion
		want bool
	}{
		{"completed with plan", RunCompletion{RunID: "run", Outcome: RunCompleted, PlanRevision: 1, PlanHash: hash, Reason: "done"}, true},
		{"skipped without plan", RunCompletion{RunID: "run", Outcome: RunSkipped, Reason: "week"}, true},
		{"failed with plan", RunCompletion{RunID: "run", Outcome: RunFailed, PlanRevision: 2, PlanHash: hash, Reason: "halt"}, true},
		{"completed without plan", RunCompletion{RunID: "run", Outcome: RunCompleted, Reason: "done"}, false},
		{"unknown outcome", RunCompletion{RunID: "run", Outcome: "GUESS", Reason: "x"}, false},
		{"revision without hash", RunCompletion{RunID: "run", Outcome: RunFailed, PlanRevision: 1, Reason: "x"}, false},
		{"hash without revision", RunCompletion{RunID: "run", Outcome: RunSkipped, PlanHash: hash, Reason: "x"}, false},
		{"large reason", RunCompletion{RunID: "run", Outcome: RunSkipped, Reason: strings.Repeat("r", 10241)}, true},
		{"invalid UTF-8 reason", RunCompletion{RunID: "run", Outcome: RunSkipped, Reason: "\xff"}, false},
		{"blank reason", RunCompletion{RunID: "run", Outcome: RunSkipped, Reason: " "}, false},
		{"NUL reason", RunCompletion{RunID: "run", Outcome: RunSkipped, Reason: "a\x00"}, false},
		{"blank run", RunCompletion{RunID: " ", Outcome: RunSkipped, Reason: "x"}, false},
		{"invalid UTF-8 run", RunCompletion{RunID: "\xff", Outcome: RunSkipped, Reason: "x"}, false},
	} {
		hashLen := len(mustHex(tc.c.PlanHash))
		if got := validRunCompletion(tc.c, hashLen); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
	if mustHex("") != nil || len(mustHex(hash)) != 32 {
		t.Fatal("mustHex must decode package-written hex and map empty to nil")
	}
}

// TestRunCompletionArgumentsOffline проверяет отказ до базы и ошибку базы при отменённом ctx.
func TestRunCompletionArgumentsOffline(t *testing.T) {
	db := unreachableDB(t)
	ok := RunCompletion{RunID: "run", Outcome: RunSkipped, Reason: "week"}
	var nilCtx context.Context
	for _, err := range []error{
		func() error { _, err := CompleteRebalanceRun(nilCtx, db, ok); return err }(),
		func() error { _, err := CompleteRebalanceRun(context.Background(), nil, ok); return err }(),
		func() error {
			_, err := CompleteRebalanceRun(context.Background(), db, RunCompletion{RunID: "run", Outcome: RunFailed, PlanRevision: 1, PlanHash: "zz", Reason: "x"})
			return err
		}(),
		func() error { _, _, err := ReadRunCompletion(context.Background(), db, " "); return err }(),
		func() error { _, _, err := ReadRunCompletion(nilCtx, db, "run"); return err }(),
	} {
		if !errors.Is(err, ErrRunCompletion) || !errors.Is(err, ErrRunCompletionArgument) {
			t.Fatalf("want argument error, got %v", err)
		}
	}
	if _, _, err := ReadLatestPlanRevision(context.Background(), db, " "); !errors.Is(err, ErrPlanRevision) {
		t.Fatalf("latest plan with blank run: %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CompleteRebalanceRun(canceled, db, ok); !errors.Is(err, ErrRunCompletion) || !errors.Is(err, context.Canceled) {
		t.Fatalf("complete with canceled ctx: %v", err)
	}
	if _, _, err := ReadRunCompletion(canceled, db, "run"); !errors.Is(err, ErrRunCompletion) || !errors.Is(err, context.Canceled) {
		t.Fatalf("read with canceled ctx: %v", err)
	}
	if _, _, err := ReadLatestPlanRevision(canceled, db, "run"); !errors.Is(err, ErrPlanRevision) {
		t.Fatalf("latest plan with canceled ctx: %v", err)
	}
}
