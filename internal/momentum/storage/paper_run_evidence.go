package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strconv"
	"time"

	"bybit_bot_liquidity/internal/momentum/paperwire"
)

var (
	// ErrPaperFactsArgument означает непригодный запрос.
	ErrPaperFactsArgument = errors.New("invalid paper facts argument")
	// ErrPaperRunEvidenceAbsent означает отсутствие запрошенного запуска в области.
	ErrPaperRunEvidenceAbsent = errors.New("paper facts absent")
	// ErrPaperRunEvidenceCorrupt означает нарушение целостности сохранённых фактов.
	ErrPaperRunEvidenceCorrupt = errors.New("corrupt paper facts")
	// ErrPaperRunEvidenceUnavailable означает ошибку чтения снимка.
	ErrPaperRunEvidenceUnavailable = errors.New("paper facts unavailable")
)

// PaperRunEvidence содержит фазы и проверенные строки исполнения одного запуска.
type PaperRunEvidence struct {
	PaperExecutionRows
	Lifecycle PaperRunLifecycle
	DataHash  string
}

// PaperIntentIdentity строит детерминированный ключ по исходному индексу входа.
func PaperIntentIdentity(inputHash string, intentIndex int) (string, error) {
	if _, err := datasetArtifactKey(inputHash); err != nil || intentIndex < 0 || intentIndex >= paperwire.MaxActualFillRecords {
		return "", ErrPaperFactsArgument
	}
	raw, _ := json.Marshal([]string{"momentum/paper-intent/v1", inputHash, strconv.Itoa(intentIndex)})
	return "paper/intent/" + paperFactsHash(raw), nil
}

// paperFactsHash возвращает lowercase SHA256 исходных байтов.
func paperFactsHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// ReadPaperRunEvidence читает согласованный снимок именованного запуска paper.
func ReadPaperRunEvidence(ctx context.Context, db *sql.DB, scope ControlScope, run string) (PaperRunEvidence, error) {
	if db == nil || !validLifecycleRequest(ctx, scope, run) {
		return PaperRunEvidence{}, ErrPaperFactsArgument
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return PaperRunEvidence{}, errors.Join(ErrPaperRunEvidenceUnavailable, ctx.Err())
	}
	defer func() { _ = tx.Rollback() }()
	f, err := readPaperRunEvidence(ctx, tx, scope, run)
	if err != nil {
		return PaperRunEvidence{}, err
	}
	if err := tx.Commit(); err != nil {
		return PaperRunEvidence{}, errors.Join(ErrPaperRunEvidenceUnavailable, ctx.Err())
	}
	return f, nil
}

// readPaperRunEvidence видит собственные записи транзакции и оставляет её открытой.
func readPaperRunEvidence(ctx context.Context, tx *sql.Tx, scope ControlScope, run string) (PaperRunEvidence, error) {
	if tx == nil || !validLifecycleRequest(ctx, scope, run) {
		return PaperRunEvidence{}, ErrPaperFactsArgument
	}
	life, err := readPaperRunLifecycle(ctx, tx, scope, run)
	if err != nil {
		return PaperRunEvidence{}, evidenceReadError(err)
	}
	rows, err := readPaperExecutionRows(ctx, tx, scope, run)
	if err != nil {
		return PaperRunEvidence{}, evidenceReadError(err)
	}
	if rows.Scope != scope || rows.RunID != run || life.Scope != scope || life.RunID != run || !boundPaperExecution(rows, life) {
		return PaperRunEvidence{}, ErrPaperRunEvidenceCorrupt
	}
	return PaperRunEvidence{rows, life, paperRunDataHash(rows, life)}, nil
}

// evidenceReadError сохраняет безопасные категории исходного reader.
func evidenceReadError(err error) error {
	kind := ErrPaperRunEvidenceUnavailable
	switch {
	case errors.Is(err, ErrPaperRunLifecycleAbsent):
		kind = ErrPaperRunEvidenceAbsent
	case errors.Is(err, ErrPaperRunLifecycleCorrupt), errors.Is(err, ErrPaperExecutionRowsCorrupt),
		errors.Is(err, ErrPaperExecutionRowsAbsent), errors.Is(err, paperwire.ErrPaperResourceLimit):
		kind = ErrPaperRunEvidenceCorrupt
	}
	return errors.Join(kind, err)
}

// boundPaperExecution проверяет ключ каждой фактической строки по исходным индексам и плану.
func boundPaperExecution(rows PaperExecutionRows, life PaperRunLifecycle) bool {
	if !life.PlanFound {
		return len(rows.intents) == 0 && len(rows.fills) == 0
	}
	if !life.InputFound {
		return false
	}
	fills := life.Input.Values().Fills
	expected := make(map[string]bool, len(fills))
	for i := range fills {
		id, err := PaperIntentIdentity(life.Input.Hash(), i)
		if err != nil {
			return false
		}
		expected[id] = true
	}
	revision := strconv.FormatUint(life.Plan.Metadata().Revision, 10)
	for _, intent := range rows.intents {
		if !expected[intent.IntentID] || intent.IntentID != intent.OrderLinkID || intent.PlanHash != life.Plan.Hash() || intent.PlanRevision != revision {
			return false
		}
	}
	return true
}

// paperRunDataHash кодирует неизменяемые поля run в фиксированном порядке.
func paperRunDataHash(rows PaperExecutionRows, life PaperRunLifecycle) string {
	wire := struct {
		Format     string     `json:"format"`
		Scope      [3]string  `json:"scope"`
		Run        string     `json:"run_id"`
		Input      string     `json:"input_sha256"`
		Plan       string     `json:"plan_sha256"`
		Intents    [][]any    `json:"intents"`
		Fills      [][]string `json:"fills"`
		Completion []string   `json:"completion"`
	}{"momentum/paper-db-facts/v1", [3]string{rows.Scope.Environment, rows.Scope.AccountID, rows.Scope.StrategyID},
		rows.RunID, life.Input.Hash(), life.Plan.Hash(), [][]any{}, [][]string{}, nil}
	intents, fills := rows.intents, rows.fills
	for _, key := range slices.Sorted(maps.Keys(intents)) {
		i := intents[key]
		wire.Intents = append(wire.Intents, []any{i.IntentID, i.OrderLinkID, i.Symbol, i.Side, i.Quantity, i.PlanRevision, i.PlanHash, i.State, i.ReduceOnly})
	}
	for _, key := range slices.Sorted(maps.Keys(fills)) {
		v := fills[key]
		wire.Fills = append(wire.Fills, []string{v.OrderLinkID, v.ExecutionID, v.Symbol, v.Side, v.Quantity, v.ExecutionPrice, v.FeeUSDT, v.ExecutedAt.UTC().Format(time.RFC3339Nano)})
	}
	if life.Completed {
		c := life.Completion
		revision := ""
		if c.PlanRevision != 0 {
			revision = strconv.FormatUint(c.PlanRevision, 10)
		}
		wire.Completion = []string{string(c.Outcome), revision, c.PlanHash, paperFactsHash([]byte(c.Reason)), c.CompletedAt.UTC().Format(time.RFC3339Nano)}
	}
	raw, _ := json.Marshal(wire)
	return paperFactsHash(raw)
}
