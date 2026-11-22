package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"bybit_bot_liquidity/internal/momentum/config"
)

// PaperCommandState содержит исходный audit применённой бумажной команды.
type PaperCommandState struct {
	State                                                                   ControlState
	Action, Actor, ActorID, CommandID, Kind, Reason, ConfigHash, RecoveryID string
	CreatedAt                                                               time.Time
}

// PaperCommandRequest меняет только бумажный режим; Config разрешён лишь для INIT.
type PaperCommandRequest struct {
	Scope                                                                   ControlScope
	ExpectedRevision                                                        uint64
	Config                                                                  *config.Config
	ConfigHash, Action, Actor, ActorID, CommandID, Kind, Reason, RecoveryID string
	Confirmed                                                               bool
}

// paperCommandAudit хранит идентичность и автора отдельно от SQL actor enum.
type paperCommandAudit struct {
	Format    string `json:"format"`
	CommandID string `json:"command_id"`
	ActorID   string `json:"actor_id"`
	Kind      string `json:"kind"`
	Reason    string `json:"reason"`
}

// ReadPaperCommandState читает последнюю ревизию, проверяя снимок и command audit.
func ReadPaperCommandState(ctx context.Context, db *sql.DB, scope ControlScope) (PaperCommandState, error) {
	if ctx == nil || db == nil || !paperControlScope(scope) {
		return PaperCommandState{}, controlFailure(ctx, ErrControlArgument)
	}
	return readPaperCommand(ctx, db, scope, "")
}

// readPaperCommand одним statement читает согласованные metadata и canonical config.
func readPaperCommand(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, scope ControlScope, commandID string) (PaperCommandState, error) {
	var out PaperCommandState
	var revision string
	var payload []byte
	var created sql.NullTime
	err := q.QueryRowContext(ctx, `SELECT r.revision::text,r.mode,r.action,r.actor,r.reason,
 encode(r.config_hash,'hex'),COALESCE(r.confirmation_id,''),COALESCE(r.reconciliation_id,''),
 CASE WHEN isfinite(r.created_at) THEN r.created_at END,c.payload
 FROM momentum.control_revisions r LEFT JOIN momentum.config_snapshots c ON c.config_hash=r.config_hash
 WHERE r.environment=$1 AND r.account_id=$2 AND r.strategy_id=$3
 AND ($4='' OR r.confirmation_id=$4) ORDER BY r.revision DESC LIMIT 1`,
		scope.Environment, scope.AccountID, scope.StrategyID, commandID).Scan(&revision, &out.State.Mode,
		&out.Action, &out.Actor, &out.Reason, &out.ConfigHash, &out.CommandID, &out.RecoveryID, &created, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return PaperCommandState{}, controlFailure(ctx, ErrControlAbsent)
	}
	if err != nil {
		return PaperCommandState{}, controlFailure(ctx, nil)
	}
	out.State.Revision, err = strconv.ParseUint(revision, 10, 63)
	cfg, parseErr := config.Parse(payload)
	canonical, hash, snapshotErr := cfg.Snapshot()
	v := cfg.Values()
	if err != nil || out.State.Revision == 0 || !created.Valid || parseErr != nil || snapshotErr != nil ||
		!bytes.Equal(canonical, payload) || hash != out.ConfigHash || v.Mode != "paper" ||
		v.DataSource.Kind != "bybit_testnet_public" || v.DataSource.Environment != "testnet" || v.StrategyID != scope.StrategyID {
		return PaperCommandState{}, controlFailure(ctx, nil)
	}
	out.CreatedAt = created.Time.UTC()
	if out.Actor == "OPERATOR" || out.Actor == "SYSTEM" && (out.CommandID != "" || strings.HasPrefix(strings.TrimSpace(out.Reason), "{")) {
		var audit paperCommandAudit
		if err := json.Unmarshal([]byte(out.Reason), &audit); err != nil {
			return PaperCommandState{}, controlFailure(ctx, nil)
		}
		encoded, _ := json.Marshal(audit)
		mode := paperCommandMode(out.Action, audit.Kind, out.Actor)
		if audit.Format != "momentum/paper-control-audit/v1" || audit.CommandID != out.CommandID ||
			!paperControlText(audit.ActorID, audit.CommandID, audit.Reason) || !bytes.Equal(encoded, []byte(out.Reason)) || mode != out.State.Mode ||
			(out.Action == "RESUME" && !paperControlText(out.RecoveryID)) || (out.Action != "RESUME" && out.RecoveryID != "") {
			return PaperCommandState{}, controlFailure(ctx, nil)
		}
		out.ActorID, out.Kind, out.Reason = audit.ActorID, audit.Kind, audit.Reason
	}
	return out, nil
}

// ApplyPaperCommandState атомарно создаёт paper scope или возвращает исходный INIT audit.
func ApplyPaperCommandState(ctx context.Context, db *sql.DB, in PaperCommandRequest) (PaperCommandState, error) {
	return applyPaperCommandState(ctx, db, in, nil)
}

// applyPaperCommandState позволяет проверить rollback до commit без внешних эффектов.
func applyPaperCommandState(ctx context.Context, db *sql.DB, in PaperCommandRequest, beforeCommit func(*sql.Tx) error) (PaperCommandState, error) {
	key, keyErr := hex.DecodeString(in.ConfigHash)
	if ctx == nil || db == nil || !paperControlScope(in.Scope) || in.Action != "INITIALIZE" ||
		in.Actor != "SYSTEM" || in.Kind != "initialize" || in.ExpectedRevision != 0 || in.Config == nil ||
		!paperControlText(in.ActorID, in.CommandID, in.Reason) || keyErr != nil || len(key) != 32 ||
		hex.EncodeToString(key) != in.ConfigHash || in.RecoveryID != "" {
		return PaperCommandState{}, controlFailure(ctx, ErrControlArgument)
	}
	payload, hash, err := in.Config.Snapshot()
	v := in.Config.Values()
	if err != nil || hash != in.ConfigHash || v.Mode != "paper" || v.StrategyID != in.Scope.StrategyID ||
		v.DataSource.Kind != "bybit_testnet_public" || v.DataSource.Environment != "testnet" {
		return PaperCommandState{}, controlFailure(ctx, ErrControlArgument)
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return PaperCommandState{}, controlFailure(ctx, nil)
	}
	defer tx.Rollback()
	if err = lockPaperControls(ctx, tx, true); err != nil {
		return PaperCommandState{}, err
	}
	latest, err := readPaperCommand(ctx, tx, in.Scope, "")
	absent := errors.Is(err, ErrControlAbsent)
	if err != nil && !absent {
		return PaperCommandState{}, err
	}
	if !absent && latest.ConfigHash != in.ConfigHash {
		return PaperCommandState{}, controlFailure(ctx, ErrControlConflict)
	}
	want := PaperCommandState{State: ControlState{Revision: 1, Mode: "ACTIVE"},
		Action: in.Action, Actor: in.Actor, ActorID: in.ActorID, CommandID: in.CommandID,
		Kind: in.Kind, Reason: in.Reason, ConfigHash: in.ConfigHash, RecoveryID: in.RecoveryID}
	if !absent {
		saved, readErr := readPaperCommand(ctx, tx, in.Scope, in.CommandID)
		if readErr == nil {
			want.CreatedAt = saved.CreatedAt
			if saved != want {
				return PaperCommandState{}, controlFailure(ctx, ErrControlConflict)
			}
			return saved, nil
		}
		if !errors.Is(readErr, ErrControlAbsent) {
			return PaperCommandState{}, readErr
		}
	}
	if !absent {
		return PaperCommandState{}, controlFailure(ctx, ErrControlConflict)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO momentum.config_snapshots(config_hash,payload) VALUES($1,$2) ON CONFLICT DO NOTHING`, key, payload)
	if err != nil {
		return PaperCommandState{}, controlFailure(ctx, nil)
	}
	audit, _ := json.Marshal(paperCommandAudit{"momentum/paper-control-audit/v1", in.CommandID, in.ActorID, in.Kind, in.Reason})
	_, err = tx.ExecContext(ctx, `INSERT INTO momentum.control_revisions
 (environment,account_id,strategy_id,revision,mode,action,actor,reason,config_hash,confirmation_id,reconciliation_id)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NULLIF($11,''))`, in.Scope.Environment, in.Scope.AccountID,
		in.Scope.StrategyID, int64(want.State.Revision), want.State.Mode, in.Action, in.Actor, string(audit), key, in.CommandID, in.RecoveryID)
	if err != nil {
		return PaperCommandState{}, controlFailure(ctx, nil)
	}
	saved, err := readPaperCommand(ctx, tx, in.Scope, in.CommandID)
	if err != nil {
		return PaperCommandState{}, err
	}
	want.CreatedAt = saved.CreatedAt
	if saved != want {
		return PaperCommandState{}, controlFailure(ctx, ErrControlConflict)
	}
	if beforeCommit != nil {
		if err := beforeCommit(tx); err != nil {
			return PaperCommandState{}, controlFailure(ctx, nil)
		}
	}
	if err := tx.Commit(); err != nil {
		return PaperCommandState{}, controlFailure(ctx, nil)
	}
	return saved, nil
}

// lockPaperControls сериализует writers и будущие guards в одной короткой TX.
func lockPaperControls(ctx context.Context, tx *sql.Tx, write bool) error {
	query := `LOCK TABLE momentum.control_revisions IN SHARE MODE`
	if write {
		query = `LOCK TABLE momentum.control_revisions IN SHARE ROW EXCLUSIVE MODE`
	}
	if _, err := tx.ExecContext(ctx, query); err != nil {
		return controlFailure(ctx, nil)
	}
	return nil
}

// paperControlScope исключает реальные аккаунты и неявное пространство имён.
func paperControlScope(s ControlScope) bool {
	return s.Environment == "paper" && strings.HasPrefix(s.AccountID, "paper/") && len(s.AccountID) > 6 && paperControlText(s.AccountID, s.StrategyID)
}

// paperControlText проверяет opaque UTF-8 без нормализации и окружающих пробелов.
func paperControlText(values ...string) bool {
	for _, value := range values {
		if value == "" || !utf8.ValidString(value) || strings.ContainsRune(value, 0) || strings.TrimSpace(value) != value {
			return false
		}
	}
	return true
}

// paperCommandMode проверяет семантику сохранённых actor/action, не разрешая запись.
func paperCommandMode(action, kind, actor string) string {
	switch {
	case action == "INITIALIZE" && kind == "initialize" && actor == "SYSTEM", action == "RESUME" && kind == "resume" && actor == "OPERATOR":
		return "ACTIVE"
	case action == "HALT" && (kind == "halt" || kind == "close-all") && actor == "OPERATOR":
		return "HALTED"
	case action == "PAUSE_ENTRIES" && kind == "pause" && actor == "OPERATOR":
		return "PAUSED_ENTRIES"
	}
	return ""
}
