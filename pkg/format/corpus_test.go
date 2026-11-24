//go:build corpus

package format

import (
	"bytes"
	"errors"
	"fmt"
	"hash/fnv"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/mmcdole/dgdtools/pkg/lexer"
	"github.com/mmcdole/dgdtools/pkg/token"
)

// corpus runs check on every .c and .h file under LPC_CORPUS that lexes
// cleanly, on all CPUs. A non-nil error or a panic is a failure.
func corpus(t *testing.T, check func(f *token.File) error) {
	root := os.Getenv("LPC_CORPUS")
	if root == "" {
		t.Skip("LPC_CORPUS not set")
	}

	paths := make(chan string, 1024)
	var checked, refused, failures atomic.Int64
	var mu sync.Mutex
	var sample []string
	fail := func(msg string) {
		failures.Add(1)
		mu.Lock()
		if len(sample) < 30 {
			sample = append(sample, msg)
		}
		mu.Unlock()
	}

	var wg sync.WaitGroup
	for range runtime.NumCPU() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range paths {
				src, err := os.ReadFile(path)
				if err != nil {
					continue
				}
				func() {
					defer func() {
						if r := recover(); r != nil {
							fail(fmt.Sprintf("%s: PANIC: %v", path, r))
						}
					}()
					switch err := check(lexer.Lex(path, src, lexer.Default)); {
					case err == nil:
						checked.Add(1)
					case errors.Is(err, ErrIllegal):
						refused.Add(1)
					default:
						fail(err.Error())
					}
				}()
			}
		}()
	}

	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if ext := filepath.Ext(path); ext == ".c" || ext == ".h" {
			paths <- path
		}
		return nil
	})
	close(paths)
	wg.Wait()

	t.Logf("corpus: %d formatted, %d refused (illegal), %d gate failures",
		checked.Load(), refused.Load(), failures.Load())
	for _, s := range sample {
		t.Errorf("  FAIL: %s", s)
	}
}

// TestCorpusFormatGate formats every cleanly-lexable corpus file and relies
// on Format's internal gate: output token stream must equal the input's and
// the engine must be idempotent. Any gate trip is an engine bug. Each file
// is formatted with spaces and with tabs.
func TestCorpusFormatGate(t *testing.T) {
	corpus(t, func(f *token.File) error {
		if _, err := Format(f, Options{}); err != nil {
			return err
		}
		_, err := Format(f, Options{IndentStyle: Tabs})
		return err
	})
}

// TestCorpusLines formats ranges of lines of every corpus file: the gate
// must hold for any range, every line as a range gives what Format gives,
// and a range of a formatted file changes nothing.
func TestCorpusLines(t *testing.T) {
	corpus(t, func(f *token.File) error {
		whole, err := Format(f, Options{})
		if err != nil {
			return err
		}
		n := len(f.Lines)
		all, err := Lines(f, Options{}, []LineRange{{1, n}})
		if err != nil {
			return fmt.Errorf("all lines: %w", err)
		}
		if !bytes.Equal(all, whole) {
			return fmt.Errorf("%s: all lines as a range differ from Format", f.Path)
		}
		// Three ranges per file, chosen by its name.
		h := fnv.New32a()
		h.Write([]byte(f.Path))
		seed := int(h.Sum32() >> 1)
		var ranges []LineRange
		for i := range 3 {
			a := 1 + (seed>>(8*i))%n
			ranges = append(ranges, LineRange{a, a + (seed>>(4*i))%12})
		}
		if _, err := Lines(f, Options{}, ranges[:1]); err != nil {
			return fmt.Errorf("lines %v: %w", ranges[:1], err)
		}
		if _, err := Lines(f, Options{}, ranges); err != nil {
			return fmt.Errorf("lines %v: %w", ranges, err)
		}
		g := lexer.Lex(f.Path, whole, f.Dialect)
		out, err := Lines(g, Options{}, ranges)
		if err != nil {
			return fmt.Errorf("lines %v of the formatted file: %w", ranges, err)
		}
		if !bytes.Equal(out, whole) {
			return fmt.Errorf("%s: lines %v of the formatted file change it", f.Path, ranges)
		}
		return nil
	})
}
