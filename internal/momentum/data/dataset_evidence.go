package data

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// DatasetEvidenceFormat обозначает точную первую версию wire-контракта evidence.
const DatasetEvidenceFormat = "momentum/dataset-evidence/v1"

// ErrDatasetEvidence обозначает невалидную форму evidence без частичного вывода.
var ErrDatasetEvidence = errors.New("invalid momentum dataset evidence")

// FundingComparisonEvidence сохраняет явно переданные expected и результат существующего helper.
type FundingComparisonEvidence struct {
	Expected []time.Time
	Coverage model.FundingCoverage
}

// StreamEvidence связывает уже рассчитанный статус с конкретными captured bytes и окном.
type StreamEvidence struct {
	Kind, Symbol, IntervalCode, ContentSHA256, Status, Reason string
	Range                                                     ManifestRange
	Funding                                                   *FundingComparisonEvidence
}

// HistoricalMarginEvidence в v1 хранит только явное отсутствие предоставленных правил.
type HistoricalMarginEvidence struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

// DatasetEvidenceInput передаёт факты caller; serializer не проверяет качество или admission.
type DatasetEvidenceInput struct {
	Source           ManifestSource
	Range            ManifestRange
	Streams          []StreamEvidence
	HistoricalMargin HistoricalMarginEvidence
}

type fundingComparisonWire struct {
	Expected   []int64 `json:"expected_times_ms"`
	Missing    []int64 `json:"missing_times_ms"`
	Unexpected []int64 `json:"unexpected_times_ms"`
}
type streamEvidenceWire struct {
	Kind          string                 `json:"kind"`
	Symbol        string                 `json:"symbol"`
	Interval      string                 `json:"interval_code,omitempty"`
	Range         manifestWindow         `json:"range"`
	ContentSHA256 string                 `json:"content_sha256"`
	Status        string                 `json:"status"`
	Reason        string                 `json:"reason,omitempty"`
	Funding       *fundingComparisonWire `json:"funding_comparison"`
}
type datasetEvidenceWire struct {
	Format           string                   `json:"format"`
	Source           ManifestSource           `json:"source"`
	Range            manifestWindow           `json:"range"`
	Streams          []streamEvidenceWire     `json:"streams"`
	HistoricalMargin HistoricalMarginEvidence `json:"historical_margin"`
}

// EncodeDatasetEvidence канонически сериализует supplied evidence, не вычисляя его истинность.
func EncodeDatasetEvidence(input DatasetEvidenceInput) ([]byte, string, error) {
	span, ok := manifestRange(input.Range)
	if !ok || !validManifestSource(input.Source) || len(input.Streams) == 0 || input.HistoricalMargin != (HistoricalMarginEvidence{Status: "unknown", Reason: "historical_margin_rules_not_supplied"}) {
		return nil, "", ErrDatasetEvidence
	}
	wire := datasetEvidenceWire{Format: DatasetEvidenceFormat, Source: input.Source, Range: span, Streams: make([]streamEvidenceWire, 0, len(input.Streams)), HistoricalMargin: input.HistoricalMargin}
	for _, row := range input.Streams {
		window, ok := manifestRange(row.Range)
		if !ok || window.StartMS < span.StartMS || window.EndMS > span.EndMS || !manifestSymbol.MatchString(row.Symbol) || !manifestDigest.MatchString(row.ContentSHA256) {
			return nil, "", ErrDatasetEvidence
		}
		converted := streamEvidenceWire{Kind: row.Kind, Symbol: row.Symbol, Interval: row.IntervalCode, Range: window, ContentSHA256: row.ContentSHA256, Status: row.Status, Reason: row.Reason}
		switch row.Kind {
		case "trade", "mark":
			if (row.IntervalCode != "D" && row.IntervalCode != "60") || row.Funding != nil {
				return nil, "", ErrDatasetEvidence
			}
			switch row.Status {
			case "unknown":
				if row.Reason != "coverage_not_assessed" {
					return nil, "", ErrDatasetEvidence
				}
			case "covered", "empty", "short_prefix", "gap", "unconfirmed":
				if row.Reason != "" {
					return nil, "", ErrDatasetEvidence
				}
			default:
				return nil, "", ErrDatasetEvidence
			}
		case "funding":
			if row.IntervalCode != "" {
				return nil, "", ErrDatasetEvidence
			}
			switch row.Status {
			case "unknown":
				if row.Reason != "expected_funding_schedule_not_supplied" || row.Funding != nil {
					return nil, "", ErrDatasetEvidence
				}
			case "compared":
				if row.Reason != "" || row.Funding == nil || len(row.Funding.Expected) == 0 {
					return nil, "", ErrDatasetEvidence
				}
				expected, ok := evidenceTimes(row.Funding.Expected, window)
				if !ok {
					return nil, "", ErrDatasetEvidence
				}
				missing, ok := evidenceTimes(row.Funding.Coverage.Missing, window)
				if !ok {
					return nil, "", ErrDatasetEvidence
				}
				unexpected, ok := evidenceTimes(row.Funding.Coverage.Unexpected, window)
				if !ok {
					return nil, "", ErrDatasetEvidence
				}
				converted.Funding = &fundingComparisonWire{expected, missing, unexpected}
			default:
				return nil, "", ErrDatasetEvidence
			}
		default:
			return nil, "", ErrDatasetEvidence
		}
		wire.Streams = append(wire.Streams, converted)
	}
	slices.SortFunc(wire.Streams, compareStreamEvidence)
	for i := 1; i < len(wire.Streams); i++ {
		if compareStreamEvidence(wire.Streams[i-1], wire.Streams[i]) == 0 {
			return nil, "", ErrDatasetEvidence
		}
	}
	payload, err := json.Marshal(wire)
	if err != nil {
		return nil, "", ErrDatasetEvidence
	}
	sum := sha256.Sum256(payload)
	return payload, hex.EncodeToString(sum[:]), nil
}

// evidenceTimes сохраняет точные списки caller; nil comparison lists становятся явными пустыми.
func evidenceTimes(times []time.Time, window manifestWindow) ([]int64, bool) {
	result := make([]int64, 0, len(times))
	for i, t := range times {
		ms, ok := manifestMS(t)
		if !ok || ms < window.StartMS || ms >= window.EndMS || (i > 0 && ms <= result[i-1]) {
			return nil, false
		}
		result = append(result, ms)
	}
	return result, true
}

// compareStreamEvidence задаёт стабильный порядок и точный duplicate key.
func compareStreamEvidence(a, b streamEvidenceWire) int {
	return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Symbol, b.Symbol), cmp.Compare(a.Interval, b.Interval))
}
