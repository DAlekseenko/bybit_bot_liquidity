package backtest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrReplayPublication обозначает отказ локальной публикации готового результата.
var ErrReplayPublication = errors.New("invalid replay publication")

// ReplayBundle хранит созданные штатным сборщиком канонические файлы результата.
type ReplayBundle struct{ files map[string][]byte }

// replayFileNames возвращает фиксированный отсортированный набор файлов.
func replayFileNames() []string {
	return []string{"config.json", "journal.json", "run.json", "simulation-model.json", "source-manifest.json"}
}

// validateReplayBundle сверяет закрытый набор файлов и точную строку контрольных сумм.
func validateReplayBundle(bundle ReplayBundle) error {
	if len(bundle.files) != 6 {
		return ErrReplayPublication
	}
	var sums bytes.Buffer
	for _, name := range replayFileNames() {
		raw, ok := bundle.files[name]
		if !ok || len(raw) == 0 {
			return ErrReplayPublication
		}
		fmt.Fprintf(&sums, "%x  %s\n", sha256.Sum256(raw), name)
	}
	if !bytes.Equal(bundle.files["SHA256SUMS"], sums.Bytes()) {
		return ErrReplayPublication
	}
	return nil
}

// WriteReplayBundle публикует целиком готовый набор после проверки локальных байтов.
func WriteReplayBundle(ctx context.Context, output string, bundle ReplayBundle) error {
	fail := func(err error) error { return errors.Join(ErrReplayPublication, err) }
	if ctx == nil || output == "" {
		return ErrReplayPublication
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if err := validateReplayBundle(bundle); err != nil {
		return err
	}
	output = filepath.Clean(output)
	if (filepath.Base(output) == "." || filepath.Base(output) == "..") || output == string(filepath.Separator) {
		return ErrReplayPublication
	}
	lock := output + ".lock"
	if err := os.Mkdir(lock, 0700); err != nil {
		return fail(err)
	}
	defer os.Remove(lock)
	if _, err := os.Lstat(output); err == nil {
		return compareReplayDirectory(ctx, output, bundle)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fail(err)
	}
	temporary, err := os.MkdirTemp(filepath.Dir(output), "."+filepath.Base(output)+".tmp-")
	if err != nil {
		return fail(err)
	}
	defer os.RemoveAll(temporary)
	for _, name := range append(replayFileNames(), "SHA256SUMS") {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		if err := os.WriteFile(filepath.Join(temporary, name), bundle.files[name], 0600); err != nil {
			return fail(err)
		}
	}
	if err := compareReplayDirectory(ctx, temporary, bundle); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if err := os.Rename(temporary, output); err != nil {
		return fail(err)
	}
	return nil
}

// compareReplayDirectory проверяет только обычные файлы точного набора без символических ссылок.
func compareReplayDirectory(ctx context.Context, directory string, bundle ReplayBundle) error {
	fail := func(err error) error { return errors.Join(ErrReplayPublication, err) }
	info, err := os.Lstat(directory)
	if err != nil {
		return fail(err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrReplayPublication
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return fail(err)
	}
	if len(entries) != len(bundle.files) {
		return ErrReplayPublication
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		expected, ok := bundle.files[entry.Name()]
		if !ok || !entry.Type().IsRegular() {
			return ErrReplayPublication
		}
		raw, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			return fail(err)
		}
		if !bytes.Equal(raw, expected) {
			return ErrReplayPublication
		}
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	return nil
}
