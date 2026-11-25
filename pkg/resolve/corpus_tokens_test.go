//go:build corpus

package resolve_test

import (
	"context"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mmcdole/dgdtools/pkg/lint"
	"github.com/mmcdole/dgdtools/pkg/resolve"
)

// limited is a testing.T that reports the first 20 failures of many.
type limited struct {
	t      *testing.T
	failed atomic.Int64
}

func (l *limited) Errorf(format string, args ...any) {
	if l.failed.Add(1) <= 20 {
		l.t.Errorf(format, args...)
	}
}

// TestCorpusTokens makes the semantic tokens of every file of the sample
// (corpusSample; LPC_SAMPLE=200000 for the whole lib), each file linted
// first, as the server has it before it is asked.
//
// Invariants: nothing panics; the tokens are in the order of the file,
// none overlapping another, each within its line. In one file of every
// 20 of the sample, every token is what At says at its offset, and At
// names nothing where there is no token but what has none
// (TestTokensAreAt).
//
// Logged: the tokens by type, and how long the tokens of a file take
// (median, 95th percentile, slowest).
func TestCorpusTokens(t *testing.T) {
	_, l, sample := corpusSample(t)
	ctx := context.Background()
	rep := &limited{t: t}
	byType := map[resolve.TokenType]int{}
	var took []time.Duration
	var slowest string
	var max time.Duration
	total, held := 0, 0
	for i, file := range sample {
		doc, err := l.Lint(ctx, file, lint.Options{NoScan: true})
		if err != nil {
			continue
		}
		start := time.Now()
		toks := doc.Tokens()
		d := time.Since(start)
		if d > max {
			max = d
			slowest = fmt.Sprintf("%s (%d tokens, %d bytes)", file, len(toks), len(doc.File.Src))
		}
		took = append(took, d)
		total += len(toks)
		src := doc.File.Src
		end := uint32(0)
		for _, tk := range toks {
			byType[tk.Type]++
			if tk.Start < end || tk.End <= tk.Start || int(tk.End) > len(src) {
				rep.Errorf("%s:%d: token out of order or empty", file, tk.Start)
			}
			if doc.File.Pos(tk.Start).Line != doc.File.Pos(tk.End-1).Line {
				rep.Errorf("%s:%d: token across lines", file, tk.Start)
			}
			end = tk.End
		}
		if i%20 == 0 {
			held += tokensAreAt(rep, doc)
		}
	}
	slices.Sort(took)
	if len(took) == 0 {
		t.Fatal("nothing linted")
	}
	pct := func(p int) time.Duration { return took[min(len(took)-1, len(took)*p/100)] }
	t.Logf("%d files, %d tokens; %d held to At", len(took), total, held)
	for typ := resolve.TokenNamespace; int(typ) < len(resolve.TokenTypes); typ++ {
		t.Logf("  %-10s %d", typ, byType[typ])
	}
	t.Logf("tokens of a file: median %v, p95 %v, max %v: %s", pct(50), pct(95), took[len(took)-1], slowest)
}
