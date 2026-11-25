//go:build corpus

package lsp_test

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/pprof"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/mmcdole/dgdtools/pkg/config"
	"github.com/mmcdole/dgdtools/pkg/fileset"
	"github.com/mmcdole/dgdtools/pkg/lsp"
	"github.com/mmcdole/dgdtools/pkg/lsp/lsptest"
	"github.com/mmcdole/dgdtools/pkg/vfs"
)

// TestCorpusMemory measures what the server holds and how fast it
// answers on a real lib, the server alone in the process: the whole-lib
// scan, which the first document with questions for it starts, then an editor's session over a
// sample of the lib's files ($LPC_SAMPLE, 300 when not set): open, the
// semantic tokens, hovers and completions at names of the file, an edit
// and its undo, close; then the references of two of the lib's busiest
// functions (add_item, set_short), from std/shop.c. It logs the heap after a collection at the end of the scan
// and of the session, the peak resident size, the scan's time and the
// latencies. With $DGDTOOLS_PPROF set to a directory, it writes heap
// profiles there (scan.heap, session.heap). $DGDTOOLS_CONFIG names the
// lib's configuration, with an absolute root.
func TestCorpusMemory(t *testing.T) {
	cfgPath := os.Getenv("DGDTOOLS_CONFIG")
	if cfgPath == "" {
		t.Skip("DGDTOOLS_CONFIG not set")
	}
	want := 300
	if n, err := strconv.Atoi(os.Getenv("LPC_SAMPLE")); err == nil {
		want = n
	}
	cfgText, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	cfg, err := config.Load(vfs.OS{}, cfgPath)
	require.NoError(t, err)
	root := cfg.AbsRoot()
	fsys := vfs.NewOverlay(vfs.OS{})
	fsys.Set(filepath.Join(root, config.FileName), cfgText)
	profiles := os.Getenv("DGDTOOLS_PPROF")
	heap := func(name string) (alloc, inuse uint64) {
		var mem runtime.MemStats
		runtime.GC()
		runtime.GC()
		runtime.ReadMemStats(&mem)
		if profiles != "" {
			f, err := os.Create(filepath.Join(profiles, name+".heap"))
			require.NoError(t, err)
			require.NoError(t, pprof.Lookup("heap").WriteTo(f, 0))
			require.NoError(t, f.Close())
		}
		return mem.HeapAlloc >> 20, mem.HeapInuse >> 20
	}

	c := lsptest.Start(t, lsp.Options{FS: fsys, Debounce: time.Millisecond, DependentDebounce: 20 * time.Millisecond})
	c.Initialize(lsptest.Capabilities{}, root)

	// The sample, spread over the lib.
	prj, err := config.Discover(fsys, []string{root}, config.DiscoverOptions{})
	require.NoError(t, err)
	var files []string
	require.NoError(t, prj.Tree().Walk(context.Background(), fsys, root, []string{".c"}, fileset.NewMatcher(prj.Config.Exclude),
		func(path string) { files = append(files, path) }))
	slices.Sort(files)
	var sample []string
	for i := 0; i < len(files) && len(sample) < want; i += max(len(files)/want, 1) {
		sample = append(sample, files[i])
	}

	// The scan: made by the server once a document has questions only
	// it answers, so the sample's files are opened until one has.
	first := filepath.Join(root, "std/room.c")
	scanned := func() bool {
		for _, m := range c.Received("window/logMessage") {
			if strings.Contains(string(m.Params), "mudlib scanned") {
				return true
			}
		}
		return false
	}
	var scanTook time.Duration
	for _, path := range append([]string{first}, sample...) {
		data, err := os.ReadFile(path)
		if err != nil || !utf8.Valid(data) {
			continue
		}
		start := time.Now()
		c.Open(path, string(data))
		c.Idle()
		scanTook = time.Since(start)
		c.Close(path)
		if scanned() {
			break
		}
	}
	require.True(t, scanned())
	scanAlloc, scanInuse := heap("scan")

	name := regexp.MustCompile(`\b[a-z_][a-z_0-9]*\(`)
	var opened, tokensTook, hoverTook, completeTook, edited []time.Duration
	began := time.Now()
	for i, path := range sample {
		data, err := os.ReadFile(path)
		if err != nil || !utf8.Valid(data) {
			continue
		}
		text := string(data)
		start := time.Now()
		c.Open(path, text)
		c.Idle()
		opened = append(opened, time.Since(start))
		start = time.Now()
		semanticTokens(t, c, "textDocument/semanticTokens/full", lsp.SemanticTokensParams{TextDocument: lsp.TextDocumentIdentifier{URI: lsptest.URI(path)}})
		tokensTook = append(tokensTook, time.Since(start))
		for _, m := range name.FindAllStringIndex(text, 5) {
			start = time.Now()
			ask(t, c, "textDocument/hover", path, m[0]+1)
			hoverTook = append(hoverTook, time.Since(start))
			start = time.Now()
			complete(t, c, path, m[0]+2, "")
			completeTook = append(completeTook, time.Since(start))
		}
		at := len(text) * (i % 7) / 7
		start = time.Now()
		c.Change(path, lsptest.Edit{Start: at, End: at, Text: " "})
		c.Idle()
		edited = append(edited, time.Since(start))
		c.Change(path, lsptest.Edit{Start: at, End: at + 1})
		c.Close(path)
	}
	c.Idle()
	sessionTook := time.Since(began)

	// References of two functions the lib calls most, from std/shop.c,
	// each asked five times (the first search of a generation builds
	// the list of files it looks at).
	shop := filepath.Join(root, "std/shop.c")
	shopText, err := os.ReadFile(shop)
	require.NoError(t, err)
	c.Open(shop, string(shopText))
	c.Idle()
	refs := map[string][]time.Duration{}
	counts := map[string]int{}
	for _, fn := range []string{"add_item", "set_short"} {
		off := regexp.MustCompile(`\b` + fn + `\b`).FindStringIndex(string(shopText))
		if off == nil {
			continue
		}
		for range 5 {
			start := time.Now()
			got := references(t, c, shop, off[0], true)
			refs[fn] = append(refs[fn], time.Since(start))
			counts[fn] = len(got)
		}
	}
	c.Close(shop)
	c.Idle()
	sessionAlloc, sessionInuse := heap("session")

	pct := func(d []time.Duration, p int) time.Duration {
		if len(d) == 0 {
			return 0
		}
		d = slices.Clone(d)
		slices.Sort(d)
		return d[min(len(d)*p/100, len(d)-1)].Round(10 * time.Microsecond)
	}
	t.Logf("scan: %v; heap after it %d MB (in use %d MB)", scanTook.Round(10*time.Millisecond), scanAlloc, scanInuse)
	t.Logf("session: %d files in %v; heap after it %d MB (in use %d MB); peak RSS %d MB", len(opened), sessionTook.Round(10*time.Millisecond), sessionAlloc, sessionInuse, maxRSS()>>20)
	t.Logf("open to idle p50 %v p95 %v; edit to idle p50 %v p95 %v", pct(opened, 50), pct(opened, 95), pct(edited, 50), pct(edited, 95))
	t.Logf("tokens p50 %v p95 %v; hover p50 %v p95 %v (%d); completion p50 %v p95 %v (%d)",
		pct(tokensTook, 50), pct(tokensTook, 95), pct(hoverTook, 50), pct(hoverTook, 95), len(hoverTook), pct(completeTook, 50), pct(completeTook, 95), len(completeTook))
	for _, fn := range []string{"add_item", "set_short"} {
		if d := refs[fn]; len(d) > 0 {
			t.Logf("references of %s: %d places; first %v, then p50 %v", fn, counts[fn], d[0].Round(time.Millisecond), pct(d[1:], 50))
		}
	}
}
