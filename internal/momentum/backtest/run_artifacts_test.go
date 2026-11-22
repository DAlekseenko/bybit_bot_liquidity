package backtest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/data"
)

// bundleFixture создаёт завершённый независимый виртуальный запуск.
func bundleFixture(t *testing.T) (BaselineReplayInput, BaselineReplayResult) {
	t.Helper()
	input := runnerFixture(t, "now", "")
	result, err := ReplayBaseline(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	return input, result
}

// TestBuildReplayBundle проверяет файлы, wire-порядок и независимость байтов.
func TestBuildReplayBundle(t *testing.T) {
	input, result := bundleFixture(t)
	bundle, err := BuildReplayBundle(input, result, "audit/offline-run/376")
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(bundle.files))
	for name := range bundle.files {
		names = append(names, name)
	}
	sort.Strings(names)
	if !reflect.DeepEqual(names, []string{"SHA256SUMS", "config.json", "journal.json", "run.json", "simulation-model.json", "source-manifest.json"}) {
		t.Fatalf("names %v", names)
	}
	wantFields := []string{`"format"`, `"status"`, `"run_id"`, `"config_sha256"`, `"dataset_manifest_sha256"`, `"simulation_model_sha256"`, `"journal_sha256"`, `"start"`, `"end"`, `"start_policy"`, `"symbols"`, `"source_model_assumptions"`, `"approval_reference"`, `"admission_policy_revision"`, `"event_order_version"`, `"signal_model_revision"`, `"execution_reference_revision"`, `"mark_observation_revision"`, `"funding_mode"`, `"expected_funding_coverage"`, `"historical_margin"`, `"liquidation"`, `"equity_stop"`, `"counts"`}
	var manifest map[string]json.RawMessage
	if err := json.Unmarshal(bundle.files["run.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest) != len(wantFields) || bytes.HasSuffix(bundle.files["run.json"], []byte("\n")) {
		t.Fatal("run.json shape or newline")
	}
	previous := -1
	for _, field := range wantFields {
		position := bytes.Index(bundle.files["run.json"], []byte(field+":"))
		if position <= previous {
			t.Fatalf("missing field %s", field)
		}
		previous = position
	}
	var sums strings.Builder
	for _, name := range replayFileNames() {
		digest := sha256.Sum256(bundle.files[name])
		sums.WriteString(hex.EncodeToString(digest[:]) + "  " + name + "\n")
	}
	if string(bundle.files["SHA256SUMS"]) != sums.String() {
		t.Fatal("checksums")
	}
	var run replayRunManifest
	if err := json.Unmarshal(bundle.files["run.json"], &run); err != nil {
		t.Fatal(err)
	}
	if run.Format != "momentum/replay-run/v1" || run.Status != "complete" || run.RunID != result.Identity.RunID || run.ConfigSHA256 != result.Identity.ConfigSHA256 || run.DatasetManifestSHA256 != result.Identity.DatasetSHA256 || run.SimulationModelSHA256 != result.Identity.ModelSHA256 || run.ApprovalReference != "audit/offline-run/376" || run.Start != journalTime(input.Start) || run.End != journalTime(input.End) || run.StartPolicy != result.Identity.StartPolicy || !reflect.DeepEqual(run.Symbols, result.Identity.Symbols) || run.SourceModelAssumptions != input.Dataset.Manifest.ModelAssumptions || run.AdmissionPolicyRevision != "d01-next-open-frozen-share/v1" || run.EventOrderVersion != "D-07/v1" || run.SignalModelRevision != "daily-roc30-closed/v1" || run.ExecutionReferenceRevision != "next-trade-open/v1" || run.MarkObservationRevision != "previous-hour-close-at-boundary/v1" || run.FundingMode != "observed-events" || run.ExpectedFundingCoverage != "unknown" || run.HistoricalMargin != "unavailable" || run.Liquidation != "unavailable" || run.EquityStop != "disabled" {
		t.Fatalf("run %+v", run)
	}
	journal, err := EncodeReplayJournal(result)
	if err != nil || run.Counts != journal.Counts() {
		t.Fatalf("journal counts mismatch: %+v, %v", run.Counts, err)
	}
	cfgRaw, _, _ := input.Config.Snapshot()
	modelRaw, _, _ := input.Model.Snapshot()
	sourceRaw, _, _ := data.EncodeManifest(input.Dataset.Manifest)
	for name, want := range map[string][]byte{"config.json": cfgRaw, "simulation-model.json": modelRaw, "source-manifest.json": sourceRaw} {
		if !bytes.Equal(bundle.files[name], want) {
			t.Fatalf("changed %s", name)
		}
	}
	second, err := BuildReplayBundle(input, result, "audit/offline-run/376")
	if err != nil || !reflect.DeepEqual(bundle, second) {
		t.Fatal("non-deterministic repeat", err)
	}
	reordered := input
	reordered.Dataset.Manifest.Streams = slices.Clone(input.Dataset.Manifest.Streams)
	slices.Reverse(reordered.Dataset.Manifest.Streams)
	third, err := BuildReplayBundle(reordered, result, "audit/offline-run/376")
	if err != nil || !reflect.DeepEqual(second, third) {
		t.Fatal("manifest order changed canonical output", err)
	}
	bundle.files["config.json"][0] = 'x'
	fourth, err := BuildReplayBundle(input, result, "audit/offline-run/376")
	if err != nil || !reflect.DeepEqual(second, fourth) {
		t.Fatal("returned bytes alias later builds", err)
	}
}

// TestBuildReplayBundleRefusals проверяет stale pins, форму и approval reference.
func TestBuildReplayBundleRefusals(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*BaselineReplayInput, *BaselineReplayResult)
	}{
		{"config pin", func(_ *BaselineReplayInput, r *BaselineReplayResult) {
			r.Identity.ConfigSHA256 = strings.Repeat("f", 64)
		}},
		{"model pin", func(_ *BaselineReplayInput, r *BaselineReplayResult) {
			r.Identity.ModelSHA256 = strings.Repeat("f", 64)
		}},
		{"dataset pin", func(_ *BaselineReplayInput, r *BaselineReplayResult) {
			r.Identity.DatasetSHA256 = strings.Repeat("f", 64)
		}},
		{"source assumptions", func(_ *BaselineReplayInput, r *BaselineReplayResult) {
			r.Identity.SourceModelAssumptions.SHA256 = strings.Repeat("f", 64)
		}},
		{"range", func(_ *BaselineReplayInput, r *BaselineReplayResult) { r.Identity.Start = r.Identity.End }},
		{"symbols", func(_ *BaselineReplayInput, r *BaselineReplayResult) { r.Identity.Symbols = nil }},
		{"policy", func(_ *BaselineReplayInput, r *BaselineReplayResult) { r.Identity.StartPolicy = "other" }},
		{"unfinished", func(_ *BaselineReplayInput, r *BaselineReplayResult) { r.Status = "incomplete" }},
		{"late journal", func(_ *BaselineReplayInput, r *BaselineReplayResult) { r.Points[len(r.Points)-1].Terminal = false }},
		{"input model", func(i *BaselineReplayInput, _ *BaselineReplayResult) { i.Model = SimulationModel{} }},
		{"input config", func(i *BaselineReplayInput, _ *BaselineReplayResult) {
			i.Config = simulationConfig(t, i.Dataset.Manifest, "paper", "baseline_delta_1x", "9000")
		}},
		{"input source", func(i *BaselineReplayInput, _ *BaselineReplayResult) {
			i.Dataset.Manifest.ModelAssumptions.SHA256 = strings.Repeat("f", 64)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			input, result := bundleFixture(t)
			test.mutate(&input, &result)
			got, err := BuildReplayBundle(input, result, "audit/approved")
			if !errors.Is(err, ErrReplayBundle) || !reflect.DeepEqual(got, ReplayBundle{}) {
				t.Fatalf("partial result: %#v, %v", got, err)
			}
		})
	}
	input, result := bundleFixture(t)
	for _, reference := range []string{"", " approved", "approved\n", "approved\t"} {
		got, err := BuildReplayBundle(input, result, reference)
		if !errors.Is(err, ErrReplayBundle) || !reflect.DeepEqual(got, ReplayBundle{}) {
			t.Fatalf("accepted approval reference %q", reference)
		}
	}
	result.Status = "incomplete"
	got, err := BuildReplayBundle(input, result, "audit/approved")
	if !errors.Is(err, ErrReplayBundle) || !errors.Is(err, ErrReplayJournal) || !reflect.DeepEqual(got, ReplayBundle{}) {
		t.Fatalf("journal cause lost: %#v, %v", got, err)
	}
}

// TestBundleRejectsNonzeroSlipWithoutCycle проверяет запрет без исполнения циклов.
func TestBundleRejectsNonzeroSlipWithoutCycle(t *testing.T) {
	input := runnerFixture(t, "next_monday", "")
	input.Start = input.Start.Add(time.Hour)
	input.End = input.Start.Add(time.Hour)
	result, err := ReplayBaseline(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	for _, point := range result.Points {
		if point.Cycle != nil {
			t.Fatal("fixture unexpectedly ran a cycle")
		}
	}
	parameters := input.Model.Parameters()
	parameters.SlippageRate = amount(t, "0.01")
	input.Model = constructSimulation(t, input.Config, input.Dataset.Manifest, parameters)
	_, result.Identity.ModelSHA256, err = input.Model.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	id := result.Identity
	result.Identity.RunID = baselineIdentity(baselineReplayIdentity{Format: "momentum/baseline-replay/v1", ConfigSHA256: id.ConfigSHA256, DatasetSHA256: id.DatasetSHA256, ModelSHA256: id.ModelSHA256, Start: journalTime(id.Start), End: journalTime(id.End)})
	got, err := BuildReplayBundle(input, result, "audit/offline")
	if !errors.Is(err, ErrReplayBundle) || !reflect.DeepEqual(got, ReplayBundle{}) {
		t.Fatal("nonzero-slip no-cycle model accepted", err)
	}
}
