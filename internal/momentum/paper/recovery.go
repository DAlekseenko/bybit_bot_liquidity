package paper

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"

	"bybit_bot_liquidity/internal/momentum/paperwire"
	s "bybit_bot_liquidity/internal/momentum/storage"
)

var (
	// ErrPaperRecoveryArgument означает неверные параметры восстановления.
	ErrPaperRecoveryArgument = errors.New("invalid recovery arguments")
	// ErrPaperRecoveryCorrupt означает нарушение сохранённой истории.
	ErrPaperRecoveryCorrupt = errors.New("corrupt paper recovery")
	// ErrPaperRecoveryStale означает изменение снимка во время или после сверки.
	ErrPaperRecoveryStale = errors.New("stale paper recovery")
	// ErrPaperRecoveryUnavailable означает недоступность согласованного чтения.
	ErrPaperRecoveryUnavailable = errors.New("paper recovery unavailable")
)

// RecoveryRequest задаёт область и политику проверки записанных планов.
type RecoveryRequest struct {
	DB          *sql.DB
	Scope       s.ControlScope
	PlanBuilder PlanBuilder
	Admission   FillAdmission
}

// RecoveredPaper содержит открытый цикл и последний проверенный итог.
type RecoveredPaper struct {
	current  PreparedCycle
	progress Progress
	last     *ResolvedCycle
	proof    RecoveryProof
}

// RecoveryProof связывает проверенные факты с конфигурацией и регистрацией владельца.
type RecoveryProof struct {
	id        string
	facts     s.PaperRunFacts
	previous  *ResolvedCycle
	builder   PlanBuilder
	admission FillAdmission
}

// Current возвращает открытый финансовый цикл.
func (r RecoveredPaper) Current() (PreparedCycle, bool) { return r.current, r.current.Hash() != "" }

// Progress возвращает фактическое состояние только открытого цикла.
func (r RecoveredPaper) Progress() (Progress, bool) { return r.progress, r.current.Hash() != "" }

// Last возвращает последний закрытый цикл.
func (r RecoveredPaper) Last() (ResolvedCycle, bool) {
	if r.last == nil {
		return ResolvedCycle{}, false
	}
	return *r.last, true
}

// Proof возвращает проверенную неизменяемую идентичность.
func (r RecoveredPaper) Proof() RecoveryProof { return r.proof }

// ID возвращает идентификатор доказательства.
func (p RecoveryProof) ID() string { return p.id }

// Scope возвращает область доказательства.
func (p RecoveryProof) Scope() s.ControlScope { return p.facts.Scope }

// ConfigHash возвращает версию исходной конфигурации.
func (p RecoveryProof) ConfigHash() string { return p.facts.Control.ConfigHash }

// ControlRevision возвращает ревизию предшественника RESUME.
func (p RecoveryProof) ControlRevision() uint64 { return p.facts.Control.State.Revision }

// OwnerSessionID возвращает подтверждённую регистрацию.
func (p RecoveryProof) OwnerSessionID() string { return p.facts.OwnerSessionID }

// Head возвращает проверенный head журнала.
func (p RecoveryProof) Head() (paperwire.CycleBinding, bool) { return p.facts.Head, p.facts.HeadFound }

// DataHash возвращает отпечаток текущих данных.
func (p RecoveryProof) DataHash() string { return p.facts.DataHash }

// recoveryError сопоставляет отсутствие или недоступность с безопасной категорией.
func recoveryError(err error) error {
	kind := ErrPaperRecoveryUnavailable
	switch {
	case errors.Is(err, s.ErrPaperFactsArgument), errors.Is(err, s.ErrPaperInputArgument):
		kind = ErrPaperRecoveryArgument
	case errors.Is(err, s.ErrPaperFactsCorrupt), errors.Is(err, s.ErrPaperInputCorrupt), errors.Is(err, ErrRecordedCycleCorrupt):
		kind = ErrPaperRecoveryCorrupt
	}
	return errors.Join(kind, err)
}

// Recover восстанавливает журнал по одной записи и сравнивает повторный снимок.
func Recover(ctx context.Context, r RecoveryRequest) (RecoveredPaper, error) {
	var out RecoveredPaper
	if ctx == nil || r.DB == nil || r.PlanBuilder == nil || r.Admission == nil {
		return out, ErrPaperRecoveryArgument
	}
	first, err := s.ReadPaperRunFacts(ctx, r.DB, r.Scope, "")
	if err != nil {
		return out, recoveryError(err)
	}
	proof := RecoveryProof{facts: first, builder: r.PlanBuilder, admission: r.Admission}
	for i := uint64(1); i != 0 && i <= first.Head.Sequence; i++ {
		row, ok, err := s.ReadPaperInput(ctx, r.DB, paperwire.Scope(r.Scope), i)
		if err != nil {
			return RecoveredPaper{}, recoveryError(err)
		}
		if !ok || row.Input.Header().Sequence != i {
			return RecoveredPaper{}, ErrPaperRecoveryCorrupt
		}
		h := row.Input.Header()
		f := first
		if i != first.Head.Sequence {
			f, err = s.ReadPaperRunFacts(ctx, r.DB, r.Scope, h.RunID)
			if err != nil {
				if errors.Is(err, s.ErrPaperFactsAbsent) {
					return RecoveredPaper{}, errors.Join(ErrPaperRecoveryCorrupt, err)
				}
				return RecoveredPaper{}, recoveryError(err)
			}
			if f.Head != first.Head || f.HeadFound != first.HeadFound || f.OwnerSessionID != first.OwnerSessionID || f.Control != first.Control {
				return RecoveredPaper{}, ErrPaperRecoveryStale
			}
		}
		if !f.InputFound || f.Input.Header() != h || f.Input.Hash() != row.Input.Hash() || i < first.Head.Sequence && !f.Completed {
			return RecoveredPaper{}, ErrPaperRecoveryCorrupt
		}
		proof.previous = out.last
		view, err := replayRecordedPaperCycle(f, out.last, r.PlanBuilder, r.Admission)
		if err != nil {
			return RecoveredPaper{}, recoveryError(err)
		}
		if view.resolution == nil {
			out.current, out.progress = view.prepared, view.progress
		} else {
			out.last = view.resolution
		}
	}
	fresh, err := s.ReadPaperRunFacts(ctx, r.DB, r.Scope, "")
	if err != nil {
		return RecoveredPaper{}, recoveryError(err)
	}
	if !proof.matches(fresh) || first.Control != fresh.Control {
		return RecoveredPaper{}, ErrPaperRecoveryStale
	}
	resolution := ""
	if fresh.Completed {
		resolution = out.last.Hash()
	}
	proof.id = proof.digest(resolution)
	out.proof = proof
	return out, nil
}

// digest кодирует versioned JSON; encoding/json сортирует строковые ключи объекта.
func (p RecoveryProof) digest(resolution string) string {
	f := p.facts
	raw, _ := json.Marshal(map[string]any{
		"format": "momentum/paper-recovery-proof/v1", "scope": [3]string{f.Scope.Environment, f.Scope.AccountID, f.Scope.StrategyID},
		"config_sha256": f.Control.ConfigHash, "control_revision": f.Control.State.Revision, "owner_session_id": f.OwnerSessionID,
		"head_sequence": f.Head.Sequence, "head_input_sha256": f.Head.InputHash, "head_resolution_sha256": resolution, "data_sha256": f.DataHash,
	})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// matches сравнивает сохранённые данные без режима и ревизии управляющего CAS.
func (p RecoveryProof) matches(f s.PaperRunFacts) bool {
	old := p.facts
	return old.Scope == f.Scope && old.RunID == f.RunID && old.Control.ConfigHash == f.Control.ConfigHash && old.OwnerSessionID == f.OwnerSessionID &&
		old.Head == f.Head && old.HeadFound == f.HeadFound && old.InputFound == f.InputFound && old.Completed == f.Completed && old.DataHash == f.DataHash
}

// CheckData повторно проверяет реальные строки в транзакции RESUME.
func (p RecoveryProof) CheckData(f s.PaperRunFacts) error {
	if p.id == "" {
		return ErrPaperRecoveryArgument
	}
	if !p.matches(f) {
		return ErrPaperRecoveryStale
	}
	if f.HeadFound && f.Input.Header() != f.Head {
		return ErrPaperRecoveryCorrupt
	}
	if !f.HeadFound {
		return nil
	}
	_, err := replayRecordedPaperCycle(f, p.previous, p.builder, p.admission)
	if err != nil {
		return errors.Join(ErrPaperRecoveryCorrupt, err)
	}
	return nil
}
