package data

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"
)

// ErrManifestContentBinding обозначает несогласованные метаданные и байты content.
var ErrManifestContentBinding = errors.New("invalid momentum manifest content binding")

// CheckManifestContentBindings проверяет только content-ссылки; успех не означает replay readiness.
// Evidence-ссылки остаются непрозрачными; функция не читает I/O и не меняет входы.
func CheckManifestContentBindings(input ManifestInput, artifacts map[string][]byte) error {
	if _, _, err := EncodeManifest(input); err != nil {
		return ErrManifestContentBinding
	}
	for _, stream := range input.Streams {
		raw := artifacts[stream.Content.SHA256]
		sum := sha256.Sum256(raw)
		if len(raw) == 0 || hex.EncodeToString(sum[:]) != stream.Content.SHA256 {
			return ErrManifestContentBinding
		}
		var source ManifestSource
		var start, end time.Time
		var symbol, interval, endpoint, format string
		switch stream.Kind {
		case "funding":
			got, err := DecodeFundingContent(raw)
			if err != nil {
				return ErrManifestContentBinding
			}
			source = ManifestSource{got.Environment, got.DatasetID, got.Source, got.Category}
			start, end, symbol, endpoint = got.Start, got.End, got.Symbol, got.Endpoint
			format = fundingContentFormat
		case "trade":
			got, err := DecodeTradeCandleContent(raw)
			if err != nil {
				return ErrManifestContentBinding
			}
			source = ManifestSource{got.Environment, got.DatasetID, got.Source, got.Category}
			start, end, symbol, interval, endpoint = got.Start, got.End, got.Symbol, got.IntervalCode, got.Endpoint
			format = tradeCandleContentFormat
		case "mark":
			got, err := DecodeMarkPriceContent(raw)
			if err != nil {
				return ErrManifestContentBinding
			}
			source = ManifestSource{got.Environment, got.DatasetID, got.Source, got.Category}
			start, end, symbol, interval, endpoint = got.Start, got.End, got.Symbol, got.IntervalCode, got.Endpoint
			format = markPriceContentFormat
		}
		if source != stream.Source || symbol != stream.Symbol || interval != stream.IntervalCode ||
			endpoint != stream.Endpoint || format != stream.Content.Format ||
			!start.Equal(stream.Range.Start) || !end.Equal(stream.Range.End) {
			return ErrManifestContentBinding
		}
	}
	return nil
}
