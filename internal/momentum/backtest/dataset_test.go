package backtest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"bybit_bot_liquidity/internal/momentum/data"
)

// datasetFixture хранит канонический вход и исходные байты объектов.
type datasetFixture struct {
	files    fstest.MapFS
	selector data.ManifestSelector
	manifest data.ManifestInput
	funding  data.FundingContentInput
	objects  map[string][]byte
	refs     map[string]string
}

// newDatasetFixture создаёт manifest и funding content существующими encoder-ами.
func newDatasetFixture(t *testing.T) datasetFixture {
	t.Helper()
	funding := data.FundingContentInput{
		Environment: "testnet", DatasetID: "frozen-1", Source: "bybit_v5", Category: "linear",
		Symbol: "BTCUSDT", Endpoint: "https://api-testnet.bybit.com/v5/market/funding/history",
		Start: time.UnixMilli(1000).UTC(), End: time.UnixMilli(2000).UTC(),
	}
	content, contentHash, err := data.EncodeFundingContent(funding)
	if err != nil {
		t.Fatal(err)
	}
	opaque := map[string][]byte{"assumptions": []byte("assumptions remain unknown"), "evidence": []byte("historical margin is unknown"), "coverage": []byte("coverage remains opaque")}
	objects, refs := map[string][]byte{contentHash: content}, map[string]string{"content": contentHash}
	for name, raw := range opaque {
		digest := contentDigest(raw)
		objects[digest], refs[name] = raw, digest
	}
	source := data.ManifestSource{Environment: "testnet", DatasetID: "frozen-1", Source: "bybit_v5", Category: "linear"}
	manifest := data.ManifestInput{
		Source: source, Range: data.ManifestRange{Start: funding.Start, End: funding.End}, LoadedAt: time.UnixMilli(3000).UTC(),
		ModelAssumptions: data.ManifestArtifact{Format: "assumptions/v1", SHA256: refs["assumptions"]},
		MarginEvidence:   data.ManifestArtifact{Format: "evidence/v1", SHA256: refs["evidence"]},
		Streams: []data.ManifestStream{{
			Source: source, Range: data.ManifestRange{Start: funding.Start, End: funding.End},
			Kind: "funding", Symbol: funding.Symbol, Endpoint: funding.Endpoint,
			Content:  data.ManifestArtifact{Format: "momentum/funding-content/v1", SHA256: refs["content"]},
			Coverage: data.ManifestArtifact{Format: "coverage/v1", SHA256: refs["coverage"]},
		}},
	}
	fixture := datasetFixture{files: make(fstest.MapFS), manifest: manifest, funding: funding, objects: objects, refs: refs}
	fixture.repin(t)
	for digest, raw := range objects {
		fixture.files[objectPath(digest)] = &fstest.MapFile{Data: bytes.Clone(raw)}
	}
	return fixture
}

// repin канонически кодирует fixture и синхронизирует manifest pin.
func (fixture *datasetFixture) repin(t *testing.T) {
	t.Helper()
	raw, digest, err := data.EncodeManifest(fixture.manifest)
	if err != nil {
		t.Fatal(err)
	}
	fixture.selector = data.ManifestSelector{Environment: fixture.manifest.Source.Environment, DatasetID: fixture.manifest.Source.DatasetID, SHA256: digest}
	fixture.files["manifest.json"] = &fstest.MapFile{Data: raw}
}

// contentDigest вычисляет lowercase SHA-256 для fixture-байтов.
func contentDigest(raw []byte) string { return fmt.Sprintf("%x", sha256.Sum256(raw)) }

// objectPath строит фиксированный путь объекта из его digest.
func objectPath(digest string) string { return "objects/" + digest + ".bin" }

// readFileFS считает операции ReadFile и подменяет только ошибку или context hook.
type readFileFS struct {
	fstest.MapFS
	reads      map[string]int
	reject     map[string]bool
	beforeRead func(string)
}

// ReadFile отслеживает чтение, запускает hook и делегирует MapFS.
func (source *readFileFS) ReadFile(name string) ([]byte, error) {
	source.reads[name]++
	if source.beforeRead != nil {
		source.beforeRead(name)
	}
	if source.reject[name] {
		return nil, fs.ErrPermission
	}
	return source.MapFS.ReadFile(name)
}

// countingContext отменяет базовый context на выбранной проверке Err.
type countingContext struct {
	context.Context
	cancelAt, checks int
	cancel           context.CancelFunc
}

// Err считает проверки и отменяет context при достижении порога.
func (ctx *countingContext) Err() error {
	ctx.checks++
	if ctx.checks >= ctx.cancelAt {
		ctx.cancel()
	}
	return ctx.Context.Err()
}

// requireRejected проверяет нулевой результат, sentinel и заданную context classification.
func requireRejected(t *testing.T, ctx context.Context, source fs.FS, selector data.ManifestSelector, contextErr error) {
	t.Helper()
	got, err := LoadFrozenDataset(ctx, source, selector)
	failed := err == nil || !errors.Is(err, ErrFrozenDataset) || !reflect.DeepEqual(got, FrozenDataset{}) || contextErr != nil && !errors.Is(err, contextErr)
	leaked := err != nil && (strings.Contains(err.Error(), "funding content") || strings.Contains(err.Error(), "wrong"))
	if failed || leaked {
		t.Fatalf("unexpected rejected result for context %v: %#v, %v", contextErr, got, err)
	}
}

// TestLoadFrozenDatasetOwnedBytes проверяет exact bytes, repeatability и изоляцию результата.
func TestLoadFrozenDatasetOwnedBytes(t *testing.T) {
	fixture := newDatasetFixture(t)
	first, err := LoadFrozenDataset(context.Background(), &readFileFS{MapFS: fixture.files, reads: make(map[string]int)}, fixture.selector)
	if err != nil || !reflect.DeepEqual(first.Manifest, fixture.manifest) || !reflect.DeepEqual(first.Artifacts, fixture.objects) {
		t.Fatalf("loaded dataset differs from fixture: %v", err)
	}
	second, err := LoadFrozenDataset(context.Background(), &readFileFS{MapFS: fixture.files, reads: make(map[string]int)}, fixture.selector)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated load differs: %v", err)
	}
	for _, raw := range first.Artifacts {
		raw[0] ^= 0xff
	}
	first.Manifest.Streams[0].Symbol = "ALTERED"
	third, err := LoadFrozenDataset(context.Background(), &readFileFS{MapFS: fixture.files, reads: make(map[string]int)}, fixture.selector)
	if err != nil || !reflect.DeepEqual(second, third) {
		t.Fatalf("prior output mutation affected a later load: %v", err)
	}
}

// TestLoadFrozenDatasetDeduplicatesReferences проверяет exact keys и отсутствие лишних чтений.
func TestLoadFrozenDatasetDeduplicatesReferences(t *testing.T) {
	fixture := newDatasetFixture(t)
	fixture.manifest.MarginEvidence = fixture.manifest.ModelAssumptions
	fixture.manifest.Streams[0].Coverage = fixture.manifest.ModelAssumptions
	fixture.repin(t)
	for _, name := range []string{"audit/private", "provenance/private", "SHA256SUMS", objectPath(strings.Repeat("f", 64))} {
		fixture.files[name] = &fstest.MapFile{Data: []byte("unused")}
	}
	source := &readFileFS{MapFS: fixture.files, reads: make(map[string]int)}
	got, err := LoadFrozenDataset(context.Background(), source, fixture.selector)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]byte{fixture.refs["content"]: fixture.objects[fixture.refs["content"]], fixture.refs["assumptions"]: fixture.objects[fixture.refs["assumptions"]]}
	if !reflect.DeepEqual(got.Artifacts, want) {
		t.Fatalf("unexpected exact artifact keys: %v", got.Artifacts)
	}
	expectedReads := map[string]int{"manifest.json": 1, objectPath(fixture.refs["content"]): 1, objectPath(fixture.refs["assumptions"]): 1}
	if !reflect.DeepEqual(source.reads, expectedReads) {
		t.Fatalf("unexpected or repeated reads: %v", source.reads)
	}
}

// TestLoadFrozenDatasetRejectsManifestPins проверяет все selector и canonical откази до objects.
func TestLoadFrozenDatasetRejectsManifestPins(t *testing.T) {
	for _, test := range []string{"hash", "environment", "dataset", "noncanonical"} {
		t.Run(test, func(t *testing.T) {
			fixture := newDatasetFixture(t)
			if test == "hash" {
				fixture.selector.SHA256 = strings.Repeat("0", 64)
			}
			if test == "environment" {
				fixture.selector.Environment = "mainnet"
			}
			if test == "dataset" {
				fixture.selector.DatasetID = "other"
			}
			if test == "noncanonical" {
				raw := append([]byte(" "), fixture.files["manifest.json"].Data...)
				fixture.files["manifest.json"] = &fstest.MapFile{Data: raw}
				fixture.selector.SHA256 = contentDigest(raw)
			}
			source := &readFileFS{MapFS: fixture.files, reads: make(map[string]int)}
			requireRejected(t, context.Background(), source, fixture.selector, nil)
			if !reflect.DeepEqual(source.reads, map[string]int{"manifest.json": 1}) {
				t.Fatalf("objects were read before rejecting manifest: %v", source.reads)
			}
		})
	}
}

// TestLoadFrozenDatasetRejectsFilesAndObjects сохраняет все проверки чтения и artifact bytes.
func TestLoadFrozenDatasetRejectsFilesAndObjects(t *testing.T) {
	cases := []struct{ name, ref, action string }{
		{"missing manifest", "", "missing-manifest"}, {"unreadable manifest", "", "unreadable-manifest"},
		{"missing content", "content", "missing"}, {"empty content", "content", "empty"}, {"mismatched content", "content", "mismatch"},
		{"missing evidence", "evidence", "missing"}, {"empty evidence", "evidence", "empty"}, {"mismatched evidence", "evidence", "mismatch"},
		{"missing model", "assumptions", "missing"}, {"empty model", "assumptions", "empty"}, {"mismatched model", "assumptions", "mismatch"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fixture := newDatasetFixture(t)
			source := &readFileFS{MapFS: fixture.files, reads: make(map[string]int), reject: make(map[string]bool)}
			switch test.action {
			case "missing-manifest":
				delete(source.MapFS, "manifest.json")
			case "unreadable-manifest":
				source.reject["manifest.json"] = true
			case "missing":
				delete(source.MapFS, objectPath(fixture.refs[test.ref]))
			case "empty":
				source.MapFS[objectPath(fixture.refs[test.ref])] = &fstest.MapFile{Data: nil}
			case "mismatch":
				source.MapFS[objectPath(fixture.refs[test.ref])] = &fstest.MapFile{Data: []byte("wrong")}
			}
			requireRejected(t, context.Background(), source, fixture.selector, nil)
		})
	}
}

// TestLoadFrozenDatasetChecksBindings отправляет валидный hash к existing content binding helper.
func TestLoadFrozenDatasetChecksBindings(t *testing.T) {
	fixture := newDatasetFixture(t)
	fixture.funding.Symbol = "ETHUSDT"
	wrong, wrongHash, err := data.EncodeFundingContent(fixture.funding)
	if err != nil {
		t.Fatal(err)
	}
	fixture.files[objectPath(wrongHash)] = &fstest.MapFile{Data: wrong}
	fixture.objects[wrongHash] = wrong
	fixture.manifest.Streams[0].Content.SHA256 = wrongHash
	fixture.manifest.Streams[0].Symbol = "BTCUSDT"
	fixture.repin(t)
	source := &readFileFS{MapFS: fixture.files, reads: make(map[string]int)}
	requireRejected(t, context.Background(), source, fixture.selector, nil)
	if source.reads[objectPath(wrongHash)] != 1 || contentDigest(wrong) != wrongHash {
		t.Fatalf("valid mismatched content was not read by its own hash path: %v", source.reads)
	}
}

// TestLoadFrozenDatasetContextRails проверяет context errors, финальную проверку и nil inputs.
func TestLoadFrozenDatasetContextRails(t *testing.T) {
	cases := []struct {
		name, kind string
		want       error
	}{
		{"pre-canceled", "pre-cancel", context.Canceled},
		{"expired deadline", "deadline", context.DeadlineExceeded},
		{"cancel during object read", "object-read", context.Canceled},
		{"cancel with read error", "read-error", context.Canceled},
		{"after final binding", "final", context.Canceled},
		{"nil context", "nil-context", nil}, {"nil source", "nil-source", nil},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fixture := newDatasetFixture(t)
			base, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := context.Context(base)
			source := &readFileFS{MapFS: fixture.files, reads: make(map[string]int)}
			var input fs.FS = source
			switch test.kind {
			case "pre-cancel":
				cancel()
			case "deadline":
				var expire context.CancelFunc
				ctx, expire = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				defer expire()
			case "object-read", "read-error":
				source.beforeRead = func(name string) {
					if test.kind == "object-read" && strings.HasPrefix(name, "objects/") || test.kind == "read-error" && name == "manifest.json" {
						cancel()
					}
				}
				if test.kind == "read-error" {
					source.reject = map[string]bool{"manifest.json": true}
				}
			case "final":
				ctx = &countingContext{Context: base, cancelAt: 11, cancel: cancel}
			case "nil-context":
				ctx = nil
			case "nil-source":
				input = nil
			}
			requireRejected(t, ctx, input, fixture.selector, test.want)
		})
	}
}
