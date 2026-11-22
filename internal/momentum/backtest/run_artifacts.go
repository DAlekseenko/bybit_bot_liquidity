package backtest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"bybit_bot_liquidity/internal/momentum/data"
	"bybit_bot_liquidity/internal/momentum/model"
)

// ErrReplayBundle обозначает несовпадение входов и завершённого журнала.
var ErrReplayBundle = errors.New("invalid completed replay bundle")

type replayRunManifest struct {
	Format                     string                `json:"format"`
	Status                     string                `json:"status"`
	RunID                      string                `json:"run_id"`
	ConfigSHA256               string                `json:"config_sha256"`
	DatasetManifestSHA256      string                `json:"dataset_manifest_sha256"`
	SimulationModelSHA256      string                `json:"simulation_model_sha256"`
	JournalSHA256              string                `json:"journal_sha256"`
	Start                      string                `json:"start"`
	End                        string                `json:"end"`
	StartPolicy                string                `json:"start_policy"`
	Symbols                    []string              `json:"symbols"`
	SourceModelAssumptions     data.ManifestArtifact `json:"source_model_assumptions"`
	ApprovalReference          string                `json:"approval_reference"`
	AdmissionPolicyRevision    string                `json:"admission_policy_revision"`
	EventOrderVersion          string                `json:"event_order_version"`
	SignalModelRevision        string                `json:"signal_model_revision"`
	ExecutionReferenceRevision string                `json:"execution_reference_revision"`
	MarkObservationRevision    string                `json:"mark_observation_revision"`
	FundingMode                string                `json:"funding_mode"`
	ExpectedFundingCoverage    string                `json:"expected_funding_coverage"`
	HistoricalMargin           string                `json:"historical_margin"`
	Liquidation                string                `json:"liquidation"`
	EquityStop                 string                `json:"equity_stop"`
	Counts                     ReplayJournalCounts   `json:"counts"`
}

// BuildReplayBundle связывает канонические снимки входов с новым полным журналом.
func BuildReplayBundle(input BaselineReplayInput, result BaselineReplayResult, approvalRef string) (ReplayBundle, error) {
	if approvalRef == "" || strings.TrimSpace(approvalRef) != approvalRef {
		return ReplayBundle{}, ErrReplayBundle
	}
	slippage, err := input.Model.Parameters().SlippageRate.Cmp(model.Zero())
	if err != nil {
		return ReplayBundle{}, errors.Join(ErrReplayBundle, err)
	}
	if slippage != 0 {
		return ReplayBundle{}, ErrReplayBundle
	}
	cfg := input.Config.Values()
	cfgRaw, cfgHash, err := input.Config.Snapshot()
	if err != nil {
		return ReplayBundle{}, errors.Join(ErrReplayBundle, err)
	}
	modelRaw, modelHash, err := input.Model.Snapshot()
	if err != nil {
		return ReplayBundle{}, errors.Join(ErrReplayBundle, err)
	}
	bound, err := NewSimulationModel(input.Config, input.Dataset.Manifest, input.Model.Parameters())
	if err != nil {
		return ReplayBundle{}, errors.Join(ErrReplayBundle, err)
	}
	boundRaw, _, err := bound.Snapshot()
	if err != nil {
		return ReplayBundle{}, errors.Join(ErrReplayBundle, err)
	}
	if !bytes.Equal(modelRaw, boundRaw) {
		return ReplayBundle{}, ErrReplayBundle
	}
	sourceRaw, sourceHash, err := data.EncodeManifest(input.Dataset.Manifest)
	if err != nil {
		return ReplayBundle{}, errors.Join(ErrReplayBundle, err)
	}
	id := result.Identity
	if id.ConfigSHA256 != cfgHash || id.ModelSHA256 != modelHash || id.DatasetSHA256 != sourceHash || sourceHash != cfg.DataSource.ManifestSHA256 || !id.Start.Equal(input.Start) || !id.End.Equal(input.End) || id.StartPolicy != cfg.StartPolicy || !slices.Equal(id.Symbols, cfg.Symbols) || id.SourceModelAssumptions != input.Dataset.Manifest.ModelAssumptions {
		return ReplayBundle{}, ErrReplayBundle
	}
	journal, err := EncodeReplayJournal(result)
	if err != nil {
		return ReplayBundle{}, errors.Join(ErrReplayBundle, err)
	}
	manifest := replayRunManifest{
		Format: "momentum/replay-run/v1", Status: "complete", RunID: id.RunID,
		ConfigSHA256: cfgHash, DatasetManifestSHA256: sourceHash, SimulationModelSHA256: modelHash, JournalSHA256: journal.SHA256(),
		Start: journalTime(input.Start), End: journalTime(input.End), StartPolicy: cfg.StartPolicy, Symbols: slices.Clone(cfg.Symbols),
		SourceModelAssumptions: input.Dataset.Manifest.ModelAssumptions, ApprovalReference: approvalRef,
		AdmissionPolicyRevision: "d01-next-open-frozen-share/v1", EventOrderVersion: "D-07/v1",
		SignalModelRevision: "daily-roc30-closed/v1", ExecutionReferenceRevision: "next-trade-open/v1",
		MarkObservationRevision: "previous-hour-close-at-boundary/v1", FundingMode: "observed-events",
		ExpectedFundingCoverage: "unknown", HistoricalMargin: "unavailable", Liquidation: "unavailable", EquityStop: "disabled",
		Counts: journal.Counts(),
	}
	runRaw, err := json.Marshal(manifest)
	if err != nil {
		return ReplayBundle{}, errors.Join(ErrReplayBundle, err)
	}
	files := map[string][]byte{
		"config.json": cfgRaw, "journal.json": journal.Bytes(), "run.json": runRaw,
		"simulation-model.json": modelRaw, "source-manifest.json": sourceRaw,
	}
	var sums strings.Builder
	for _, name := range replayFileNames() {
		fmt.Fprintf(&sums, "%s  %s\n", journalDigest(files[name]), name)
	}
	files["SHA256SUMS"] = []byte(sums.String())
	return ReplayBundle{files: files}, nil
}
