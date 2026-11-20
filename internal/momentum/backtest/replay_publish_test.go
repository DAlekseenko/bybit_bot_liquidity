package backtest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// afterCheckContext выполняет тестовое действие после заданной проверки контекста.
type afterCheckContext struct {
	context.Context
	checks  int
	trigger int
	action  func()
}

// requireNoError останавливает тест при неожиданной ошибке фикстуры.
func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// Err считает проверки и запускает действие в выбранной точке публикации.
func (ctx *afterCheckContext) Err() error {
	ctx.checks++
	if ctx.checks == ctx.trigger {
		ctx.action()
	}
	return ctx.Context.Err()
}

// publicationFixture создаёт только проверяемые байты без утверждения их финансовой семантики.
func publicationFixture() ReplayBundle {
	files := map[string][]byte{}
	var sums bytes.Buffer
	for _, name := range replayFileNames() {
		files[name] = []byte("{}")
		fmt.Fprintf(&sums, "%x  %s\n", sha256.Sum256(files[name]), name)
	}
	files["SHA256SUMS"] = sums.Bytes()
	return ReplayBundle{files: files}
}

// TestWriteReplayBundle проверяет повторную локальную публикацию и полное совпадение файлов.
func TestWriteReplayBundle(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "result")
	b := publicationFixture()
	for i := 0; i < 2; i++ {
		requireNoError(t, WriteReplayBundle(context.Background(), output, b))
	}
	for name, want := range b.files {
		got, err := os.ReadFile(filepath.Join(output, name))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("file %s mismatch: %v", name, err)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary/lock leak: %v %v", entries, err)
	}
}

// TestWriteReplayBundleRefusals проверяет неизменность существующего вывода при отказах.
func TestWriteReplayBundleRefusals(t *testing.T) {
	for _, name := range []string{"nil-context", "canceled", "expired-deadline", "empty-path", "root", "no-parent", "wrong-checksum", "missing", "extra", "empty-file", "existing-empty", "existing-extra", "existing-partial", "conflict", "symlink-directory", "symlink-file", "foreign-lock", "regular-target"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			output := filepath.Join(root, "result")
			b := publicationFixture()
			var preserved map[string][]byte
			ctx := context.Background()
			switch name {
			case "nil-context":
				ctx = nil
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "expired-deadline":
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
			case "empty-path":
				output = ""
			case "root":
				output = "/"
			case "no-parent":
				output = filepath.Join(root, "missing", "result")
			case "wrong-checksum":
				b.files["SHA256SUMS"] = []byte("invalid")
			case "missing":
				delete(b.files, "run.json")
			case "extra":
				b.files["other.json"] = []byte("{}")
			case "empty-file":
				b.files["run.json"] = nil
			case "existing-empty":
				requireNoError(t, os.Mkdir(output, 0700))
			case "existing-extra":
				requireNoError(t, WriteReplayBundle(ctx, output, b))
				preserved = make(map[string][]byte, len(b.files)+1)
				for name, contents := range b.files {
					preserved[name] = bytes.Clone(contents)
				}
				preserved["extra.json"] = []byte("preserve")
				requireNoError(t, os.WriteFile(filepath.Join(output, "extra.json"), preserved["extra.json"], 0600))
			case "existing-partial":
				requireNoError(t, os.Mkdir(output, 0700))
				preserved = map[string][]byte{"run.json": []byte("preserve")}
				requireNoError(t, os.WriteFile(filepath.Join(output, "run.json"), preserved["run.json"], 0600))
			case "conflict", "symlink-file":
				requireNoError(t, WriteReplayBundle(ctx, output, b))
				path := filepath.Join(output, "run.json")
				if name == "conflict" {
					requireNoError(t, os.WriteFile(path, []byte("old"), 0600))
				} else {
					requireNoError(t, os.Remove(path))
					requireNoError(t, os.Symlink(filepath.Join(output, "config.json"), path))
				}
			case "symlink-directory":
				requireNoError(t, os.Symlink(root, output))
			case "foreign-lock":
				requireNoError(t, os.Mkdir(output+".lock", 0700))
			case "regular-target":
				requireNoError(t, os.WriteFile(output, []byte("old"), 0600))
			}
			publishErr := WriteReplayBundle(ctx, output, b)
			if !errors.Is(publishErr, ErrReplayPublication) {
				t.Fatalf("refusal missing: %v", publishErr)
			}
			if preserved != nil {
				files, readErr := os.ReadDir(output)
				if readErr != nil || len(files) != len(preserved) {
					t.Fatalf("existing output inventory changed: files=%v read=%v", files, readErr)
				}
				for file, want := range preserved {
					got, fileErr := os.ReadFile(filepath.Join(output, file))
					if fileErr != nil || !bytes.Equal(got, want) {
						t.Fatalf("existing file %s changed: %v", file, fileErr)
					}
				}
			}
			if name == "canceled" && !errors.Is(publishErr, context.Canceled) {
				t.Fatal(publishErr)
			}
			if name == "expired-deadline" && !errors.Is(WriteReplayBundle(ctx, output, b), context.DeadlineExceeded) {
				t.Fatal("expired deadline was not preserved")
			}
			if name == "conflict" {
				got, err := os.ReadFile(filepath.Join(output, "run.json"))
				if err != nil || string(got) != "old" {
					t.Fatal("existing result changed")
				}
			}
			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".result.tmp-") {
					t.Fatal("temporary publication leaked")
				}
			}
		})
	}
}

// TestWriteReplayBundleLateFailures проверяет отмену после записи и отказ перед rename.
func TestWriteReplayBundleLateFailures(t *testing.T) {
	for _, tc := range []struct {
		name    string
		trigger int
		remove  bool
	}{
		{name: "after-writes", trigger: 4},
		{name: "after-readback", trigger: 15},
		{name: "filesystem-before-rename", trigger: 15, remove: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			output := filepath.Join(root, "result")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			controlled := &afterCheckContext{Context: ctx, trigger: tc.trigger}
			controlled.action = func() {
				if tc.remove {
					entries, err := os.ReadDir(root)
					if err != nil {
						t.Fatalf("staging directory missing: %v %v", entries, err)
					}
					var staging string
					for _, entry := range entries {
						if strings.HasPrefix(entry.Name(), ".result.tmp-") {
							staging = entry.Name()
						}
					}
					if staging == "" {
						t.Fatal("staging directory missing")
					}
					if err := os.RemoveAll(filepath.Join(root, staging)); err != nil {
						t.Fatal(err)
					}
					return
				}
				cancel()
			}
			err := WriteReplayBundle(controlled, output, publicationFixture())
			if tc.remove {
				if !errors.Is(err, ErrReplayPublication) || errors.Is(err, context.Canceled) {
					t.Fatalf("filesystem failure classification: %v", err)
				}
			} else if !errors.Is(err, context.Canceled) {
				t.Fatalf("late cancellation not preserved: %v", err)
			}
			entries, readErr := os.ReadDir(root)
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("partial publication or residue: %v %v", entries, readErr)
			}
		})
	}
}

// TestWriteReplayBundleConcurrent проверяет сохранность победившего результата при одновременных писателях.
func TestWriteReplayBundleConcurrent(t *testing.T) {
	output := filepath.Join(t.TempDir(), "result")
	b := publicationFixture()
	var wg sync.WaitGroup
	outcomes := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); outcomes <- WriteReplayBundle(context.Background(), output, b) }()
	}
	wg.Wait()
	close(outcomes)
	success := 0
	for err := range outcomes {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrReplayPublication) {
			t.Fatal(err)
		}
	}
	if success == 0 {
		t.Fatal("no winner")
	}
	requireNoError(t, WriteReplayBundle(context.Background(), output, b))
	other := publicationFixture()
	other.files["run.json"] = []byte(`{"other":true}`)
	var sums bytes.Buffer
	for _, name := range replayFileNames() {
		fmt.Fprintf(&sums, "%x  %s\n", sha256.Sum256(other.files[name]), name)
	}
	other.files["SHA256SUMS"] = sums.Bytes()
	output = filepath.Join(t.TempDir(), "competing")
	start := make(chan struct{})
	type result struct {
		bundle ReplayBundle
		err    error
	}
	results := make(chan result, 2)
	for _, candidate := range []ReplayBundle{b, other} {
		go func(candidate ReplayBundle) {
			<-start
			results <- result{bundle: candidate, err: WriteReplayBundle(context.Background(), output, candidate)}
		}(candidate)
	}
	close(start)
	var winner ReplayBundle
	for range 2 {
		got := <-results
		if got.err == nil {
			if winner.files != nil {
				t.Fatal("incompatible writers both succeeded")
			}
			winner = got.bundle
		} else if !errors.Is(got.err, ErrReplayPublication) {
			t.Fatal(got.err)
		}
	}
	if winner.files == nil {
		t.Fatal("no competing writer won")
	}
	for name, want := range winner.files {
		got, err := os.ReadFile(filepath.Join(output, name))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("winner changed at %s: %v", name, err)
		}
	}
}
