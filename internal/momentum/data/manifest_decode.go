package data

import (
	"bytes"
	"encoding/json"
	"time"
)

// ManifestSelector содержит доверенный pin конфигурации, без дополнительного диапазона.
type ManifestSelector struct {
	Environment string
	DatasetID   string
	SHA256      string
}

// DecodeManifest возвращает канонический manifest только для явно выбранного pin.
func DecodeManifest(raw []byte, selector ManifestSelector) (ManifestInput, error) {
	expected := ManifestSource{Environment: selector.Environment, DatasetID: selector.DatasetID, Source: "bybit_v5", Category: "linear"}
	if !validManifestSource(expected) || !manifestDigest.MatchString(selector.SHA256) {
		return ManifestInput{}, ErrManifest
	}
	var wire manifestWire
	if json.Unmarshal(raw, &wire) != nil {
		return ManifestInput{}, ErrManifest
	}
	input := ManifestInput{
		Source: wire.Source, Range: ManifestRange{Start: time.UnixMilli(wire.Range.StartMS).UTC(), End: time.UnixMilli(wire.Range.EndMS).UTC()},
		LoadedAt: time.UnixMilli(wire.LoadedMS).UTC(), ModelAssumptions: wire.ModelAssumptions, MarginEvidence: wire.MarginEvidence,
		Streams: make([]ManifestStream, len(wire.Streams)),
	}
	for i, stream := range wire.Streams {
		input.Streams[i] = ManifestStream{
			Source: wire.Source, Range: ManifestRange{Start: time.UnixMilli(stream.Range.StartMS).UTC(), End: time.UnixMilli(stream.Range.EndMS).UTC()},
			Kind: stream.Kind, Symbol: stream.Symbol, IntervalCode: stream.IntervalCode, Endpoint: stream.Endpoint,
			Content: stream.Content, Coverage: stream.Coverage,
		}
	}
	encoded, digest, err := EncodeManifest(input)
	if err != nil || !bytes.Equal(raw, encoded) || digest != selector.SHA256 || input.Source != expected {
		return ManifestInput{}, ErrManifest
	}
	return input, nil
}
