package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"strconv"
	"time"

	"bybit_bot_liquidity/internal/momentum/paperwire"
)

var (
	// ErrPaperInputArgument означает неподходящий вход до запроса.
	ErrPaperInputArgument = errors.New("invalid paper input argument")
	// ErrPaperInputCorrupt означает повреждённую сохранённую связь.
	ErrPaperInputCorrupt = errors.New("corrupt paper input")
	// ErrPaperInputUnavailable означает неподтверждённый результат обращения к БД.
	ErrPaperInputUnavailable = errors.New("paper input unavailable")
)

// StoredPaperInput возвращает immutable вход и исходное время записи.
type StoredPaperInput struct {
	Input     paperwire.InputRecord
	CreatedAt time.Time
}

// paperInputReader допускает DB и supplied TX без открытия второго соединения.
type paperInputReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// paperInputFailure сохраняет отмену, исключая SQL diagnostics.
func paperInputFailure(ctx context.Context, kind error) error {
	if ctx == nil {
		return kind
	}
	return errors.Join(kind, ctx.Err())
}

// ReadPaperInput читает ровно одну запись и непосредственную связь.
func ReadPaperInput(ctx context.Context, db *sql.DB, scope paperwire.Scope, sequence uint64) (StoredPaperInput, bool, error) {
	if ctx == nil || db == nil || sequence == 0 || !paperInputScope(scope) {
		return StoredPaperInput{}, false, paperInputFailure(ctx, ErrPaperInputArgument)
	}
	return readPaperInput(ctx, db, scope, sequence)
}

// ReadPaperInputHead читает максимальную sequence одного namespace.
func ReadPaperInputHead(ctx context.Context, db *sql.DB, scope paperwire.Scope) (StoredPaperInput, bool, error) {
	if ctx == nil || db == nil || !paperInputScope(scope) {
		return StoredPaperInput{}, false, paperInputFailure(ctx, ErrPaperInputArgument)
	}
	return readPaperInputHead(ctx, db, scope)
}

// readPaperInputHead использует тот же bounded query, включая supplied TX.
func readPaperInputHead(ctx context.Context, q paperInputReader, scope paperwire.Scope) (StoredPaperInput, bool, error) {
	return readPaperInput(ctx, q, scope, 0)
}

// paperInputScope запрещает реальный аккаунт и неявное пространство.
func paperInputScope(s paperwire.Scope) bool {
	return paperControlScope(ControlScope(s))
}

// paperInputIdentity проверяет sealed bytes против immutable run/config.
func paperInputIdentity(raw, hash, cfg []byte, run, account, strategy string, onboarding sql.NullString, at sql.NullTime) (paperwire.InputRecord, bool) {
	r, err := paperwire.DecodeInputRecord(raw)
	if err != nil || !at.Valid {
		return paperwire.InputRecord{}, false
	}
	h := r.Header()
	embedded := r.Values().Run
	canonical, configHash, err := embedded.Config.Snapshot()
	return r, err == nil && bytes.Equal(canonical, cfg) && h.ConfigHash == configHash &&
		r.Hash() == hex.EncodeToString(hash) && h.RunID == run &&
		h.Scope == (paperwire.Scope{Environment: "paper", AccountID: account, StrategyID: strategy}) &&
		onboarding.Valid == (embedded.OnboardingID != "") && onboarding.String == embedded.OnboardingID &&
		h.ModelHash == embedded.Config.Values().ModelID && h.ScheduledAt.Equal(at.Time)
}

// readPaperInput одним statement получает input, predecessor и immutable dependencies.
func readPaperInput(ctx context.Context, q paperInputReader, scope paperwire.Scope, sequence uint64) (StoredPaperInput, bool, error) {
	var raw, hash, cfg, previous, previousHash, previousCfg, link, receiptHash []byte
	var run, account, strategy, seq string
	var onboarding, prevOnboarding, prevRun, prevAccount, prevStrategy, outcome, reason, revision, planHash sql.NullString
	var at, prevAt, created sql.NullTime
	var linksValid bool
	err := q.QueryRowContext(ctx, `SELECT i.payload,i.input_hash,
 CASE WHEN i.sequence=trunc(i.sequence) AND i.sequence BETWEEN 1 AND 18446744073709551615
      THEN trunc(i.sequence)::text ELSE '' END,i.previous_input_hash,i.previous_resolution_hash,
 COALESCE(r.run_id,''),COALESCE(r.account_id,''),COALESCE(r.strategy_id,''),r.onboarding_id,
 CASE WHEN isfinite(r.scheduled_at) THEN r.scheduled_at END,
 CASE WHEN r.config_hash=pg_catalog.sha256(c.payload) THEN c.payload END,
 CASE WHEN isfinite(i.created_at) THEN i.created_at END,
 p.payload,p.input_hash,pr.run_id,pr.account_id,pr.strategy_id,pr.onboarding_id,
 CASE WHEN isfinite(pr.scheduled_at) THEN pr.scheduled_at END,
 CASE WHEN pr.config_hash=pg_catalog.sha256(pc.payload) THEN pc.payload END,
 (i.sequence<>1 OR (i.previous_input_hash IS NULL AND i.previous_resolution_hash IS NULL)),
 CASE WHEN isfinite(x.completed_at) THEN x.outcome END,x.reason,
 CASE WHEN x.plan_revision=trunc(x.plan_revision) AND x.plan_revision BETWEEN 1 AND 18446744073709551615
      THEN trunc(x.plan_revision)::text END,encode(x.plan_hash,'hex')
 FROM momentum.paper_run_inputs i
 LEFT JOIN momentum.rebalance_runs r ON r.run_id=i.run_id
 LEFT JOIN momentum.config_snapshots c ON c.config_hash=r.config_hash
 LEFT JOIN momentum.paper_run_inputs p ON p.environment=i.environment AND p.account_id=i.account_id
 AND p.strategy_id=i.strategy_id AND p.sequence=i.predecessor_sequence
 LEFT JOIN momentum.rebalance_runs pr ON pr.run_id=p.run_id
 LEFT JOIN momentum.config_snapshots pc ON pc.config_hash=pr.config_hash
 LEFT JOIN momentum.run_completions x ON x.run_id=p.run_id
 WHERE i.environment=$1 AND i.account_id=$2 AND i.strategy_id=$3 AND ($4='0' OR i.sequence=$4::numeric)
 ORDER BY i.sequence DESC LIMIT 1`, scope.Environment, scope.AccountID, scope.StrategyID, strconv.FormatUint(sequence, 10)).Scan(
		&raw, &hash, &seq, &link, &receiptHash, &run, &account, &strategy, &onboarding, &at, &cfg, &created,
		&previous, &previousHash, &prevRun, &prevAccount, &prevStrategy, &prevOnboarding, &prevAt, &previousCfg, &linksValid, &outcome, &reason, &revision, &planHash)
	if errors.Is(err, sql.ErrNoRows) {
		return StoredPaperInput{}, false, nil
	}
	if err != nil {
		return StoredPaperInput{}, false, paperInputFailure(ctx, ErrPaperInputUnavailable)
	}
	r, valid := paperInputIdentity(raw, hash, cfg, run, account, strategy, onboarding, at)
	h := r.Header()
	if !valid || !linksValid || !created.Valid || h.Scope != scope || strconv.FormatUint(h.Sequence, 10) != seq ||
		h.PreviousInputHash != hex.EncodeToString(link) || h.PreviousResolutionHash != hex.EncodeToString(receiptHash) {
		return StoredPaperInput{}, false, paperInputFailure(ctx, ErrPaperInputCorrupt)
	}
	if h.Sequence > 1 {
		p, ok := paperInputIdentity(previous, previousHash, previousCfg, prevRun.String, prevAccount.String, prevStrategy.String, prevOnboarding, prevAt)
		if !ok || p.Header().Scope != scope || p.Header().Sequence != h.Sequence-1 || p.Hash() != h.PreviousInputHash {
			return StoredPaperInput{}, false, paperInputFailure(ctx, ErrPaperInputCorrupt)
		}
		receipt, validReceipt := decodePaperCompletion(p, outcome.String, reason.String, revision.String, planHash.String)
		if !validReceipt || receipt.Hash() != h.PreviousResolutionHash {
			return StoredPaperInput{}, false, ErrPaperInputCorrupt
		}
	}
	return StoredPaperInput{r, created.Time.UTC()}, true, nil
}
