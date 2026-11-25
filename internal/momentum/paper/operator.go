package paper

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"bybit_bot_liquidity/internal/momentum/paperwire"
	s "bybit_bot_liquidity/internal/momentum/storage"
)

// ErrOperatorCommand означает неверные параметры операторской команды.
var ErrOperatorCommand = errors.New("invalid paper operator command")

// ErrOperatorCloseInProgress означает, что для записанного close-all ещё нет terminal receipt.
var ErrOperatorCloseInProgress = errors.New("unfinished paper close-all input")

// OperatorCommand связывает подтверждение с актором, причиной и управляющей ревизией.
type OperatorCommand struct {
	Kind, ID, ActorID, Reason string
	ExpectedRevision          uint64
	Confirmed                 bool
}

// OperatorResult различает историю команды, текущий режим, финансовое состояние и закрытие.
type OperatorResult struct {
	State                                      string
	History                                    s.PaperCommandOutcome
	Control                                    s.PaperCommandState
	Head                                       paperwire.CycleBinding
	DataHash, ReceiptHash, RegisteredSessionID string
	Actual                                     json.RawMessage
	Close                                      PaperCloseResult
}

// OperatorService выполняет общую политику локального CLI и будущего адаптера.
type OperatorService struct {
	RecoveryRequest
	ConfigHash string
	WithOwner  func(context.Context, func(*s.ExecutionOwner) error) error
}

// Status восстанавливает согласованные control и финансовые данные без полномочий записи.
func (svc OperatorService) Status(ctx context.Context) (OperatorResult, error) {
	rec, err := Recover(ctx, svc.RecoveryRequest)
	if err != nil {
		return OperatorResult{}, err
	}
	if svc.ConfigHash != rec.proof.ConfigHash() {
		return OperatorResult{}, ErrOperatorCommand
	}
	out := OperatorResult{State: "STATUS", Control: rec.proof.facts.Control, DataHash: rec.proof.DataHash(), RegisteredSessionID: rec.proof.OwnerSessionID()}
	out.Close, err = PaperCloseState(rec)
	if err != nil {
		return OperatorResult{}, err
	}
	out.Head, _ = rec.Proof().Head()
	if last, ok := rec.Last(); ok {
		out.ReceiptHash = last.Hash()
	}
	out.Actual = append(json.RawMessage(nil), out.Close.Actual...)
	return out, err
}

// Apply сохраняет операторскую команду или возвращает точную историю при повторе.
func (svc OperatorService) Apply(ctx context.Context, in OperatorCommand) (OperatorResult, error) {
	action := map[string]string{"pause": "PAUSE_ENTRIES", "close-all": "HALT", "resume": "RESUME"}[in.Kind]
	if action == "" || !in.Confirmed || in.ExpectedRevision == 0 {
		return OperatorResult{}, ErrOperatorCommand
	}
	for _, v := range []string{in.ID, in.ActorID, in.Reason} {
		if v == "" || strings.TrimSpace(v) != v {
			return OperatorResult{}, ErrOperatorCommand
		}
	}
	req := s.PaperCommandRequest{Scope: svc.Scope, ConfigHash: svc.ConfigHash, Action: action, Kind: in.Kind, Actor: "OPERATOR", ActorID: in.ActorID, CommandID: in.ID, Reason: in.Reason, ExpectedRevision: in.ExpectedRevision, Confirmed: true}
	read := func() (OperatorResult, bool, error) {
		history, ok, err := s.ReadPaperCommandOutcome(ctx, svc.DB, svc.Scope, in.ID)
		if !ok {
			return OperatorResult{}, false, err
		}
		if action == "RESUME" {
			req.RecoveryID = history.Applied.RecoveryID
		}
		if !history.Matches(req) {
			return OperatorResult{}, true, s.ErrControlConflict
		}
		state := "APPLIED"
		if errors.Is(err, s.ErrPaperCommandCurrentUnavailable) {
			state = "APPLIED_CURRENT_UNAVAILABLE"
		}
		return OperatorResult{State: state, History: history, Control: history.Current}, true, err
	}
	if out, found, err := read(); err != nil || found {
		return out, err
	}
	var writeErr error
	if action == "RESUME" {
		if svc.WithOwner == nil {
			return OperatorResult{}, s.ErrPaperFenceDenied
		}
		c, e := s.ReadPaperCommandState(ctx, svc.DB, svc.Scope)
		if e != nil {
			return OperatorResult{}, e
		}
		if c.ConfigHash != svc.ConfigHash || c.State.Revision != in.ExpectedRevision || c.State.Mode != "HALTED" && c.State.Mode != "PAUSED_ENTRIES" {
			return OperatorResult{}, s.ErrControlConflict
		}
		writeErr = svc.WithOwner(ctx, func(owner *s.ExecutionOwner) error {
			if owner == nil {
				return s.ErrPaperFenceDenied
			}
			rec, err := Recover(ctx, svc.RecoveryRequest)
			if err != nil {
				return err
			}
			if current, open := rec.Current(); open && current.Purpose().Kind == "close-all" {
				return ErrOperatorCloseInProgress
			}
			if owner.NeedsReconciliation() {
				if err = ReconcilePaperOwner(ctx, svc.DB, owner, rec.Proof()); err != nil {
					return err
				}
			}
			req.RecoveryID = rec.Proof().ID()
			_, err = ResumePaperCommand(ctx, svc.DB, req, owner, rec.Proof())
			return err
		})
	} else {
		_, writeErr = s.ApplyPaperCommandState(ctx, svc.DB, req)
	}
	out, found, err := read()
	if found {
		return out, err
	}
	if writeErr == nil && err == nil {
		return OperatorResult{}, s.ErrPaperCommandOutcomeUnavailable
	}
	return OperatorResult{}, errors.Join(writeErr, err)
}
