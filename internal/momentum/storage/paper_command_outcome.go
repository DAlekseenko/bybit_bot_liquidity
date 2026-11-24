package storage

import (
	"context"
	"database/sql"
	"errors"
	"math"
)

var (
	// ErrPaperCommandOutcomeCorrupt означает неверные данные сохранённого события.
	ErrPaperCommandOutcomeCorrupt = errors.New("invalid paper command outcome")
	// ErrPaperCommandOutcomeUnavailable означает недоступность исторического или текущего чтения.
	ErrPaperCommandOutcomeUnavailable = errors.New("paper command outcome unavailable")
	// ErrPaperCommandCurrentUnavailable сохраняет подтверждённый Applied при отказе чтения Current.
	ErrPaperCommandCurrentUnavailable = errors.New("current paper command unavailable")
)

// PaperCommandOutcome содержит исходную применённую команду и отдельное наблюдение текущего режима.
type PaperCommandOutcome struct {
	Scope            ControlScope
	Applied, Current PaperCommandState
}

// Matches сверяет запрос оператора или INIT со всеми полями сохранённого подтверждения в той же области.
func (o PaperCommandOutcome) Matches(in PaperCommandRequest) bool {
	mode := paperCommandMode(in.Action, in.Kind, in.Actor)
	if in.Scope != o.Scope || !paperControlScope(in.Scope) || in.ExpectedRevision >= math.MaxInt64 || mode == "" || (in.Action == "INITIALIZE") != (in.Config != nil) {
		return false
	}
	if in.Config != nil {
		_, hash, err := in.Config.Snapshot()
		if err != nil || hash != in.ConfigHash || in.ExpectedRevision != 0 {
			return false
		}
	} else if !in.Confirmed || in.ExpectedRevision == 0 {
		return false
	}
	want := PaperCommandState{
		State: ControlState{Revision: in.ExpectedRevision + 1, Mode: mode}, CreatedAt: o.Applied.CreatedAt,
		Action: in.Action, Actor: in.Actor, ActorID: in.ActorID, CommandID: in.CommandID,
		Kind: in.Kind, Reason: in.Reason, ConfigHash: in.ConfigHash, RecoveryID: in.RecoveryID,
	}
	return want == o.Applied
}

// ReadPaperCommandOutcome подтверждает одну историческую запись, затем независимо читает актуальную ревизию.
func ReadPaperCommandOutcome(ctx context.Context, db *sql.DB, scope ControlScope, id string) (PaperCommandOutcome, bool, error) {
	if ctx == nil || db == nil || !paperControlScope(scope) || !paperControlText(id) {
		return PaperCommandOutcome{}, false, ErrControlArgument
	}
	return readPaperCommandOutcome(ctx, db, scope, id)
}

// readPaperCommandOutcome наблюдает историю и текущую ревизию в двух отдельных statement.
func readPaperCommandOutcome(ctx context.Context, q paperInputReader, scope ControlScope, id string) (PaperCommandOutcome, bool, error) {
	applied, err := readPaperCommand(ctx, q, scope, id)
	if errors.Is(err, ErrControlAbsent) {
		return PaperCommandOutcome{}, false, nil
	}
	if err != nil {
		return PaperCommandOutcome{}, false, paperOutcomeError(ctx, err)
	}
	if applied.ActorID == "" || applied.CommandID != id || !legalPaperControl(applied) {
		return PaperCommandOutcome{}, false, ErrPaperCommandOutcomeCorrupt
	}
	o := PaperCommandOutcome{Scope: scope, Applied: applied}
	current, err := readPaperCommand(ctx, q, scope, "")
	if err != nil {
		return o, true, errors.Join(ErrPaperCommandCurrentUnavailable, paperOutcomeError(ctx, err))
	}
	if !legalPaperControl(current) || current.State.Revision < applied.State.Revision ||
		current.State.Revision == applied.State.Revision && current != applied {
		return o, true, errors.Join(ErrPaperCommandCurrentUnavailable, ErrPaperCommandOutcomeCorrupt, ctx.Err())
	}
	o.Current = current
	return o, true, nil
}

// paperOutcomeError сохраняет безопасную причину ошибки чтения.
func paperOutcomeError(ctx context.Context, err error) error {
	if errors.Is(err, errPaperCommandSemantic) || errors.Is(err, ErrControlAbsent) {
		return errors.Join(ErrPaperCommandOutcomeCorrupt, err, ctx.Err())
	}
	return errors.Join(ErrPaperCommandOutcomeUnavailable, err, ctx.Err())
}

// legalPaperControl проверяет допустимые legacy-ревизии и формы аудита бумажного журнала.
func legalPaperControl(c PaperCommandState) bool {
	if c.ActorID != "" {
		mode := paperCommandMode(c.Action, c.Kind, c.Actor)
		return mode != "" && mode == c.State.Mode
	}
	legacy := c.Actor == "SYSTEM" && c.Action == "INITIALIZE" && c.State.Mode == "ACTIVE" ||
		c.Actor == "RISK_CONTROLLER" && (c.Action == "PAUSE_ENTRIES" && c.State.Mode == "PAUSED_ENTRIES" ||
			c.Action == "HALT" && c.State.Mode == "HALTED")
	return legacy && c.CommandID == "" && c.RecoveryID == ""
}
