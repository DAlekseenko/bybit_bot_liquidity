package storage

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"strconv"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

var (
	// ErrControl обозначает неподтверждённую операцию с состоянием управления.
	ErrControl = errors.New("momentum control repository failed")
	// ErrControlArgument обозначает некорректный вход.
	ErrControlArgument = errors.New("invalid momentum control argument")
	// ErrControlAbsent обозначает отсутствие цепочки ревизий управления.
	ErrControlAbsent = errors.New("momentum control state absent")
	// ErrControlConflict обозначает, что последняя ревизия уже не совпадает с ожидаемой.
	ErrControlConflict = errors.New("momentum control revision conflict")
)

// Режим, который выставляет действие risk controller.
var riskActionModes = map[string]string{"PAUSE_ENTRIES": "PAUSED_ENTRIES", "HALT": "HALTED"}

// ControlScope — область состояния управления.
type ControlScope struct {
	Environment, AccountID, StrategyID string
}

// ControlState — последняя ревизия управления области.
type ControlState struct {
	Revision uint64
	Mode     string
}

// RiskEventRecord — событие риска; Threshold и Equity обязательны для EQUITY_STOP.
type RiskEventRecord struct {
	EventID, Kind, Detail     string
	ThresholdUSDT, EquityUSDT *model.Decimal
}

// RiskTransition — переход risk controller (PAUSE_ENTRIES или HALT) поверх
// ExpectedRevision с событиями, ссылающимися на новую ревизию.
type RiskTransition struct {
	Scope            ControlScope
	ExpectedRevision uint64
	Action, Reason   string
	ConfigHash       string
	OccurredAt       time.Time
	Events           []RiskEventRecord
}

// LatestControlState читает последнюю ревизию управления области; без цепочки — ErrControlAbsent.
func LatestControlState(ctx context.Context, db *sql.DB, scope ControlScope) (ControlState, error) {
	if ctx == nil || db == nil || !nonBlank(scope.Environment, scope.AccountID, scope.StrategyID) {
		return ControlState{}, controlFailure(ctx, ErrControlArgument)
	}
	var revision, mode string
	err := db.QueryRowContext(ctx, `SELECT revision::text, mode FROM momentum.control_revisions
 WHERE environment=$1 AND account_id=$2 AND strategy_id=$3 ORDER BY revision DESC LIMIT 1`,
		scope.Environment, scope.AccountID, scope.StrategyID).Scan(&revision, &mode)
	if errors.Is(err, sql.ErrNoRows) {
		return ControlState{}, controlFailure(ctx, ErrControlAbsent)
	} else if err != nil {
		return ControlState{}, controlFailure(ctx, nil)
	}
	n, err := strconv.ParseUint(revision, 10, 64)
	if err != nil {
		return ControlState{}, controlFailure(ctx, nil)
	}
	return ControlState{Revision: n, Mode: mode}, nil
}

// AppendRiskTransition одной транзакцией добавляет ревизию RISK_CONTROLLER и
// её события риска. Если последняя ревизия не равна ExpectedRevision (в том
// числе при конкурентной записи или повторе), ничего не пишет и возвращает
// ErrControlConflict; вызывающий перечитывает состояние и оценивает заново.
func AppendRiskTransition(ctx context.Context, db *sql.DB, tr RiskTransition) (ControlState, error) {
	return appendRiskTransition(ctx, db, tr, nil)
}

// appendRiskTransition реализует AppendRiskTransition; beforeCommit внедряет сбой в тестах.
func appendRiskTransition(ctx context.Context, db *sql.DB, tr RiskTransition, beforeCommit func(*sql.Tx) error) (ControlState, error) {
	cfg, cfgErr := hex.DecodeString(tr.ConfigHash)
	mode, ok := riskActionModes[tr.Action]
	s := tr.Scope
	if ctx == nil || db == nil || !ok || cfgErr != nil || len(cfg) != 32 || tr.ExpectedRevision == 0 ||
		tr.OccurredAt.IsZero() || len(tr.Events) == 0 || !nonBlank(s.Environment, s.AccountID, s.StrategyID, tr.Reason) {
		return ControlState{}, controlFailure(ctx, ErrControlArgument)
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return ControlState{}, controlFailure(ctx, nil)
	}
	defer tx.Rollback()
	var latest sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT max(revision)::text FROM momentum.control_revisions
 WHERE environment=$1 AND account_id=$2 AND strategy_id=$3`, s.Environment, s.AccountID, s.StrategyID).Scan(&latest)
	if err != nil {
		return ControlState{}, controlFailure(ctx, nil)
	}
	expected := strconv.FormatUint(tr.ExpectedRevision, 10)
	if !latest.Valid || latest.String != expected {
		return ControlState{}, controlFailure(ctx, ErrControlConflict)
	}
	next := tr.ExpectedRevision + 1
	res, err := tx.ExecContext(ctx, `INSERT INTO momentum.control_revisions(environment,account_id,strategy_id,revision,
 mode,action,actor,reason,config_hash) VALUES ($1,$2,$3,$4::numeric,$5,$6,'RISK_CONTROLLER',$7,$8) ON CONFLICT DO NOTHING`,
		s.Environment, s.AccountID, s.StrategyID, strconv.FormatUint(next, 10), mode, tr.Action, tr.Reason, cfg)
	if err != nil {
		return ControlState{}, controlFailure(ctx, nil)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ControlState{}, controlFailure(ctx, ErrControlConflict)
	}
	at := tr.OccurredAt.UTC().Format(time.RFC3339Nano)
	for _, ev := range tr.Events {
		threshold, equity, ok := optionalWire(ev.ThresholdUSDT, ev.EquityUSDT)
		if !ok || !nonBlank(ev.EventID, ev.Kind, ev.Detail) {
			return ControlState{}, controlFailure(ctx, ErrControlArgument)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO momentum.risk_events(event_id,environment,account_id,strategy_id,kind,
 threshold_usdt,equity_usdt,config_hash,control_revision,detail,occurred_at)
 VALUES ($1,$2,$3,$4,$5,$6::numeric,$7::numeric,$8,$9::numeric,$10,$11::timestamptz)`, ev.EventID, s.Environment,
			s.AccountID, s.StrategyID, ev.Kind, threshold, equity, cfg, strconv.FormatUint(next, 10), ev.Detail, at)
		if err != nil {
			return ControlState{}, controlFailure(ctx, ErrControlArgument)
		}
	}
	if beforeCommit != nil {
		if err := beforeCommit(tx); err != nil {
			return ControlState{}, controlFailure(ctx, nil)
		}
	}
	if err := tx.Commit(); err != nil {
		return ControlState{}, controlFailure(ctx, nil)
	}
	return ControlState{Revision: next, Mode: mode}, nil
}

// optionalWire переводит необязательные decimal в строки SQL; nil остаётся NULL.
func optionalWire(values ...*model.Decimal) (*string, *string, bool) {
	out := make([]*string, len(values))
	for i, v := range values {
		if v == nil {
			continue
		}
		s, err := v.WireDecimal()
		if err != nil {
			return nil, nil, false
		}
		out[i] = &s
	}
	return out[0], out[1], true
}

// controlFailure сохраняет категорию и отмену контекста без сырых SQL-диагностик.
func controlFailure(ctx context.Context, kind error) error {
	if ctx == nil {
		return errors.Join(ErrControl, kind)
	}
	return errors.Join(ErrControl, kind, ctx.Err())
}
