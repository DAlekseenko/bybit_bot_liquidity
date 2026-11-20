package backtest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"

	"bybit_bot_liquidity/internal/momentum/data"
)

// ErrFrozenDataset обозначает непригодный замороженный набор данных.
var ErrFrozenDataset = errors.New("invalid momentum frozen dataset")

// FrozenDataset содержит проверенный manifest и только его загруженные артефакты.
type FrozenDataset struct {
	Manifest  data.ManifestInput
	Artifacts map[string][]byte
}

// LoadFrozenDataset загружает артефакты только по доверенному pin выбора запуска.
func LoadFrozenDataset(ctx context.Context, source fs.FS, selector data.ManifestSelector) (FrozenDataset, error) {
	if ctx == nil || source == nil {
		return FrozenDataset{}, ErrFrozenDataset
	}
	manifestBytes, err := readFrozenFile(ctx, source, "manifest.json")
	if err != nil {
		return FrozenDataset{}, err
	}
	manifest, err := data.DecodeManifest(manifestBytes, selector)
	if err != nil {
		return FrozenDataset{}, ErrFrozenDataset
	}
	refs := []data.ManifestArtifact{manifest.ModelAssumptions, manifest.MarginEvidence}
	for _, stream := range manifest.Streams {
		refs = append(refs, stream.Content, stream.Coverage)
	}
	artifacts := make(map[string][]byte, len(refs))
	for _, ref := range refs {
		if _, exists := artifacts[ref.SHA256]; exists {
			continue
		}
		path := "objects/" + ref.SHA256 + ".bin"
		raw, err := readFrozenFile(ctx, source, path)
		if err != nil {
			return FrozenDataset{}, err
		}
		digest := sha256.Sum256(raw)
		if len(raw) == 0 || hex.EncodeToString(digest[:]) != ref.SHA256 {
			return FrozenDataset{}, ErrFrozenDataset
		}
		artifacts[ref.SHA256] = append([]byte(nil), raw...)
	}
	if data.CheckManifestContentBindings(manifest, artifacts) != nil {
		return FrozenDataset{}, ErrFrozenDataset
	}
	if err := frozenContextError(ctx); err != nil {
		return FrozenDataset{}, err
	}
	return FrozenDataset{Manifest: manifest, Artifacts: artifacts}, nil
}

// readFrozenFile читает один путь и проверяет отмену вокруг операции чтения.
func readFrozenFile(ctx context.Context, source fs.FS, path string) ([]byte, error) {
	if err := frozenContextError(ctx); err != nil {
		return nil, err
	}
	raw, err := fs.ReadFile(source, path)
	if err != nil {
		if contextErr := frozenContextError(ctx); contextErr != nil {
			return nil, contextErr
		}
		return nil, fmt.Errorf("%w: read %s", ErrFrozenDataset, path)
	}
	if err := frozenContextError(ctx); err != nil {
		return nil, err
	}
	return raw, nil
}

// frozenContextError сохраняет классификацию исходной отмены вместе с sentinel.
func frozenContextError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %w", ErrFrozenDataset, err)
	}
	return nil
}
