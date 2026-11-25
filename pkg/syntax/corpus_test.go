//go:build corpus

package syntax

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/mmcdole/dgdtools/pkg/lexer"
	"github.com/mmcdole/dgdtools/pkg/pp"
)

// TestCorpusParse parses every .c/.h file under $LPC_CORPUS twice (with no
// macro knowledge, and claiming complete knowledge of only the file's own
// macros) and checks the parser's invariants: it never panics, every node
// span is ordered and inside the file, a function's body lies inside the
// function, and errors come out in source order. What the errors say is
// not judged here.
func TestCorpusParse(t *testing.T) {
	root := os.Getenv("LPC_CORPUS")
	if root == "" {
		t.Skip("LPC_CORPUS not set")
	}
	paths := make(chan string, 1024)
	var files, withErrors, failures atomic.Int64
	var mu sync.Mutex
	var sample []string
	fail := func(format string, args ...any) {
		failures.Add(1)
		mu.Lock()
		if len(sample) < 20 {
			sample = append(sample, fmt.Sprintf(format, args...))
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
				files.Add(1)
				for _, complete := range []bool{false, true} {
					func() {
						defer func() {
							if r := recover(); r != nil {
								fail("%s: panic (complete=%v): %v", path, complete, r)
							}
						}()
						f := lexer.Lex(path, src, lexer.Default)
						env := pp.NewEnv(map[string]bool{"__DGD__": true}, nil)
						env.Complete = complete
						tree := Parse(f, Options{Env: env, Sel: env.Scan(f, nil)})
						if complete && len(tree.Errors) > 0 {
							withErrors.Add(1)
						}
						n := len(f.Tokens)
						Inspect(tree, func(node Node) bool {
							a, b := node.Span()
							if a < 0 || b < a || b >= n {
								fail("%s: %T has span [%d,%d] of %d tokens", path, node, a, b, n)
								return false
							}
							if fn, ok := node.(*FuncDecl); ok && fn.Body != nil {
								if fn.Body.First < fn.First || fn.Body.Last > fn.Last {
									fail("%s: function body outside its function", path)
								}
							}
							return true
						})
						if complete && n <= 5000 {
							// One walk finds the paths a walk for each token does.
							PathsAt(tree, tree.Sel.Sig, func(tok int, nodes []Node) {
								if !slices.Equal(nodes, PathAt(tree, tok)) {
									fail("%s: PathsAt and PathAt differ at token %d", path, tok)
								}
							})
						}
						last := -1
						for _, e := range tree.Errors {
							if e.Tok < last || e.Tok < 0 || e.Tok >= n {
								fail("%s: errors out of order or out of range", path)
							}
							last = e.Tok
						}
					}()
				}
			}
		}()
	}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
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
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("parsed %d files twice; %d have syntax errors under a self-contained view", files.Load(), withErrors.Load())
	for _, s := range sample {
		t.Error(s)
	}
	if n := failures.Load(); n > 0 {
		t.Fatalf("%d invariant violations", n)
	}
}
