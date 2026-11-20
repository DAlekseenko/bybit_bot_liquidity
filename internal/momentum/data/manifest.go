package data

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// ErrManifest обозначает непригодные входные метаданные manifest.
var ErrManifest = errors.New("invalid momentum dataset manifest")

const manifestVersion = "momentum/dataset-manifest/v1"

// ManifestSource задаёт точное происхождение одного зарегистрированного набора.
type ManifestSource struct {
	Environment string `json:"environment"`
	DatasetID   string `json:"dataset_id"`
	Source      string `json:"source"`
	Category    string `json:"category"`
}

// ManifestRange задаёт полуоткрытое UTC-окно метаданных.
type ManifestRange struct {
	Start time.Time
	End   time.Time
}

// ManifestArtifact связывает внешний артефакт с версией формата и хешем.
type ManifestArtifact struct {
	Format string `json:"format"`
	SHA256 string `json:"sha256"`
}

// ManifestStream описывает отдельный ряд и ссылки на материализованные данные и coverage.
type ManifestStream struct {
	Source       ManifestSource
	Range        ManifestRange
	Kind         string
	Symbol       string
	IntervalCode string
	Endpoint     string
	Content      ManifestArtifact
	Coverage     ManifestArtifact
}

// ManifestInput хранит уже предоставленные извне метаданные для кодирования.
type ManifestInput struct {
	Source   ManifestSource
	Range    ManifestRange
	LoadedAt time.Time
	// ModelAssumptions ссылается на допущения без хеша этого manifest.
	ModelAssumptions ManifestArtifact
	MarginEvidence   ManifestArtifact
	Streams          []ManifestStream
}

type manifestWindow struct {
	StartMS int64 `json:"start_ms"`
	EndMS   int64 `json:"end_ms"`
}

type manifestStreamWire struct {
	Kind         string           `json:"kind"`
	Symbol       string           `json:"symbol"`
	IntervalCode string           `json:"interval_code,omitempty"`
	Endpoint     string           `json:"endpoint"`
	Range        manifestWindow   `json:"range"`
	Content      ManifestArtifact `json:"content"`
	Coverage     ManifestArtifact `json:"coverage"`
}

type manifestWire struct {
	Version          string               `json:"version"`
	Source           ManifestSource       `json:"source"`
	Range            manifestWindow       `json:"range"`
	LoadedMS         int64                `json:"loaded_ms"`
	ModelAssumptions ManifestArtifact     `json:"model_assumptions"`
	MarginEvidence   ManifestArtifact     `json:"margin_evidence"`
	Streams          []manifestStreamWire `json:"streams"`
}

var (
	manifestSymbol = regexp.MustCompile(`^[A-Z0-9]+USDT$`)
	manifestFormat = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]{0,127}$`)
	manifestDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// EncodeManifest возвращает канонические байты и SHA-256 предоставленных метаданных.
func EncodeManifest(input ManifestInput) ([]byte, string, error) {
	window, rangeOK := manifestRange(input.Range)
	loaded, loadedOK := manifestMS(input.LoadedAt)
	if !validManifestSource(input.Source) || !rangeOK || !loadedOK ||
		!validManifestArtifact(input.ModelAssumptions) || !validManifestArtifact(input.MarginEvidence) ||
		len(input.Streams) == 0 {
		return nil, "", ErrManifest
	}
	wire := manifestWire{
		Version: manifestVersion, Source: input.Source, Range: window, LoadedMS: loaded,
		ModelAssumptions: input.ModelAssumptions, MarginEvidence: input.MarginEvidence,
		Streams: make([]manifestStreamWire, 0, len(input.Streams)),
	}
	for _, stream := range input.Streams {
		span, ok := manifestRange(stream.Range)
		if stream.Source != input.Source || !ok || span.StartMS < window.StartMS || span.EndMS > window.EndMS ||
			!manifestSymbol.MatchString(stream.Symbol) || !validManifestEndpoint(stream) ||
			!validManifestArtifact(stream.Content) || !validManifestArtifact(stream.Coverage) {
			return nil, "", ErrManifest
		}
		wire.Streams = append(wire.Streams, manifestStreamWire{
			Kind: stream.Kind, Symbol: stream.Symbol, IntervalCode: stream.IntervalCode, Endpoint: stream.Endpoint,
			Range: span, Content: stream.Content, Coverage: stream.Coverage,
		})
	}
	slices.SortFunc(wire.Streams, compareManifestStreams)
	for i := 1; i < len(wire.Streams); i++ {
		if compareManifestStreams(wire.Streams[i-1], wire.Streams[i]) == 0 {
			return nil, "", ErrManifest
		}
	}
	payload, err := json.Marshal(wire)
	if err != nil {
		return nil, "", ErrManifest
	}
	digest := sha256.Sum256(payload)
	return payload, hex.EncodeToString(digest[:]), nil
}

// validManifestSource проверяет правила существующего исторического namespace.
func validManifestSource(source ManifestSource) bool {
	n := utf8.RuneCountInString(source.DatasetID)
	return (source.Environment == "mainnet" || source.Environment == "testnet") &&
		source.Source == "bybit_v5" && source.Category == "linear" && utf8.ValidString(source.DatasetID) &&
		n >= 1 && n <= 128 && strings.Trim(source.DatasetID, " ") == source.DatasetID &&
		!strings.ContainsRune(source.DatasetID, 0)
}

// manifestMS сохраняет точную UTC-миллисекунду из диапазона хранилища.
func manifestMS(stamp time.Time) (int64, bool) {
	ms := stamp.UnixMilli()
	return ms, ms >= 0 && stamp.UTC().Year() <= 294276 && time.UnixMilli(ms).Equal(stamp)
}

// manifestRange проверяет обе границы полуоткрытого окна.
func manifestRange(span ManifestRange) (manifestWindow, bool) {
	start, startOK := manifestMS(span.Start)
	end, endOK := manifestMS(span.End)
	return manifestWindow{StartMS: start, EndMS: end}, startOK && endOK && start < end
}

// validManifestArtifact проверяет форму ссылки без чтения внешнего содержимого.
func validManifestArtifact(ref ManifestArtifact) bool {
	return manifestFormat.MatchString(ref.Format) && manifestDigest.MatchString(ref.SHA256)
}

// validManifestEndpoint проверяет точный endpoint и interval каждого ряда.
func validManifestEndpoint(stream ManifestStream) bool {
	var path string
	switch stream.Kind {
	case "trade":
		path = "/v5/market/kline"
	case "mark":
		path = "/v5/market/mark-price-kline"
	case "funding":
		path = "/v5/market/funding/history"
	default:
		return false
	}
	if stream.Kind == "funding" {
		if stream.IntervalCode != "" {
			return false
		}
	} else if stream.IntervalCode != "D" && stream.IntervalCode != "60" {
		return false
	}
	origin := "https://api.bybit.com"
	if stream.Source.Environment == "testnet" {
		origin = "https://api-testnet.bybit.com"
	}
	return stream.Endpoint == origin+path
}

// compareManifestStreams сравнивает полные ключи ряда в одном dataset.
func compareManifestStreams(a, b manifestStreamWire) int {
	return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Symbol, b.Symbol), cmp.Compare(a.IntervalCode, b.IntervalCode))
}
