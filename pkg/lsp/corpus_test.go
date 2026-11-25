//go:build corpus

package lsp_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mmcdole/dgdtools/pkg/config"
	"github.com/mmcdole/dgdtools/pkg/diag"
	"github.com/mmcdole/dgdtools/pkg/fileset"
	"github.com/mmcdole/dgdtools/pkg/index"
	"github.com/mmcdole/dgdtools/pkg/lint"
	"github.com/mmcdole/dgdtools/pkg/lsp"
	"github.com/mmcdole/dgdtools/pkg/lsp/lsptest"
	"github.com/mmcdole/dgdtools/pkg/vfs"
)

// TestCorpusSession is an editor going through a real lib: it opens a
// sample of its files one after the other ($LPC_SAMPLE of them, 2000
// when not set), types a character into each, takes it out again, and
// closes it. $DGDTOOLS_CONFIG names the lib's configuration, with an
// absolute root; the server finds it as an editor's would, by a
// dgdtools.yml above the file, which the test puts in the lib's root
// without writing anything there.
//
// Invariants: no panic; every published range lies in the document;
// what is published for a file is what a linter of the whole lib finds
// in it, for the text the editor has (a file that is not UTF-8 is opened
// as Latin-1, as an editor set up for the lib opens it); after the edit
// and its undo it is that again. Then it asks for the references of
// functions, which are as many as the library finds. Latencies and
// memory are logged.
func TestCorpusSession(t *testing.T) {
	cfgPath := os.Getenv("DGDTOOLS_CONFIG")
	if cfgPath == "" {
		t.Skip("DGDTOOLS_CONFIG not set")
	}
	want := 2000
	if n, err := strconv.Atoi(os.Getenv("LPC_SAMPLE")); err == nil {
		want = n
	}
	cfgText, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	cfg, err := config.Load(vfs.OS{}, cfgPath)
	require.NoError(t, err)
	root := cfg.AbsRoot()
	require.True(t, filepath.IsAbs(cfg.Root), "the configuration needs an absolute root")
	fsys := vfs.NewOverlay(vfs.OS{})
	fsys.Set(filepath.Join(root, config.FileName), cfgText)

	// The files of the sample, spread over the lib.
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
	for _, rel := range []string{"std/room.c", "std/object.c", "std/living.c"} {
		if p := filepath.Join(root, rel); fsys.Exists(p) {
			sample = append(sample, p)
		}
	}

	// The linter the server is held to: one of the whole lib, reading
	// the same texts.
	texts := vfs.NewOverlay(fsys)
	ref, err := lint.New(prj, texts)
	require.NoError(t, err)
	fresh := func(path, text string) []string {
		texts.Set(path, []byte(text))
		ref.Invalidate(path)
		doc, err := ref.Lint(context.Background(), path, lint.Options{})
		require.NoError(t, err, path)
		out := []string{}
		for _, d := range doc.Diagnostics {
			r := lsp.Range{Start: lsptest.PositionOf(text, int(d.Start), lsp.UTF16), End: lsptest.PositionOf(text, int(d.End), lsp.UTF16)}
			out = append(out, finding(r, map[diag.Severity]int{diag.Error: 1, diag.Warning: 2, diag.Info: 3}[d.Severity], d.Rule, wire(t, d.Message), len(d.Related)))
		}
		texts.Delete(path)
		ref.Invalidate(path)
		return out
	}

	var log safeBuffer
	c := lsptest.Start(t, lsp.Options{FS: fsys, Debounce: time.Millisecond, DependentDebounce: 20 * time.Millisecond,
		Log: slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelWarn}))})
	c.Initialize(lsptest.Capabilities{}, root)

	var first, edited []time.Duration
	latin1, findings := 0, 0
	began := time.Now()
	for i, path := range sample {
		data, err := os.ReadFile(path)
		if err != nil {
			continue // a dangling link
		}
		text := string(data)
		if !utf8.Valid(data) {
			latin1++
			runes := make([]rune, len(data))
			for i, b := range data {
				runes[i] = rune(b)
			}
			text = string(runes)
		}
		start := time.Now()
		c.Open(path, text)
		got := published(t, c, path)
		first = append(first, time.Since(start))
		if i == 0 {
			t.Logf("the first file, with the scan of the lib: %v", time.Since(start).Round(time.Millisecond))
		}
		require.Equal(t, fresh(path, text), got, path)
		findings += len(got)
		end := lsptest.PositionOf(text, len(text), c.Encoding)
		for _, d := range c.Diagnostics(path).Diagnostics {
			require.True(t, d.Range.End.Line < end.Line || d.Range.End == end || d.Range.End.Line == end.Line && d.Range.End.Character <= end.Character,
				"%s: %+v ends after the text (%+v)", path, d.Range, end)
		}

		// A character in, at the start of some line, and out again.
		at := 0
		if n := strings.Count(text, "\n"); n > 0 {
			at = strings.Index(text[len(text)*(i%7)/7:], "\n") + len(text)*(i%7)/7 + 1
		}
		start = time.Now()
		c.Change(path, lsptest.Edit{Start: at, End: at, Text: " "})
		published(t, c, path)
		edited = append(edited, time.Since(start))
		c.Change(path, lsptest.Edit{Start: at, End: at + 1})
		require.Equal(t, got, published(t, c, path), "%s: after an edit and its undo", path)
		c.Close(path)
	}
	c.Idle()

	pct := func(d []time.Duration, p int) time.Duration {
		slices.Sort(d)
		return d[len(d)*p/100].Round(10 * time.Microsecond)
	}
	// References through the server, against the library's: of the
	// first function called in some of the files, and of the function
	// called most often in the lib.
	require.True(t, ref.Scan(context.Background()))
	units, _ := ref.Index().Snapshot().UseUnits(context.Background())
	calls := map[string]int{}
	for _, u := range units {
		for _, use := range u.AllUses() {
			if use.Kind == index.UseFunction && use.Form == index.FormOwn && !use.Decl {
				calls[use.Name]++
			}
		}
	}
	most := ""
	for name, n := range calls {
		if n > calls[most] || n == calls[most] && name < most {
			most = name
		}
	}
	var took []time.Duration
	asked := 0
	ask := func(path string, pick func(index.Use) bool) {
		doc, err := ref.Lint(context.Background(), path, lint.Options{})
		if err != nil || !utf8.Valid(doc.File.Src) {
			return
		}
		for _, use := range doc.Unit.AllUses() {
			if !pick(use) {
				continue
			}
			want := doc.References(context.Background(), use.Start, true)
			if want == nil {
				continue
			}
			text := string(doc.File.Src)
			c.Open(path, text)
			start := time.Now()
			got := references(t, c, path, int(use.Start), true)
			took = append(took, time.Since(start))
			c.Close(path)
			require.Len(t, got, len(want.Sites), "%s: references of %s", path, use.Name)
			if use.Name == most {
				t.Logf("references of %s through the server: %d places in %v", most, len(got), took[len(took)-1].Round(time.Millisecond))
			}
			asked++
			return
		}
	}
	for _, u := range units {
		if found := slices.ContainsFunc(u.Uses(most), func(use index.Use) bool { return use.Form == index.FormOwn }); found {
			ask(u.Path, func(use index.Use) bool { return use.Name == most && use.Form == index.FormOwn })
			break
		}
	}
	for _, path := range sample[:min(len(sample), 50)] {
		ask(path, func(use index.Use) bool { return use.Kind == index.UseFunction && use.Form == index.FormOwn })
	}
	t.Logf("references of %d functions through the server: p50 %v, max %v", asked, pct(took, 50), took[len(took)-1].Round(time.Millisecond))

	// What the server holds, the reference linter gone.
	ref, units = nil, nil
	var mem runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&mem)

	t.Logf("%d of %d files (%d not UTF-8), %d findings, %v", len(first), len(files), latin1, findings, time.Since(began).Round(time.Millisecond))
	t.Logf("open to diagnostics: p50 %v p95 %v max %v; edit to diagnostics: p50 %v p95 %v max %v (each includes 1 ms of waiting for more changes)",
		pct(first, 50), pct(first, 95), first[len(first)-1].Round(time.Millisecond), pct(edited, 50), pct(edited, 95), edited[len(edited)-1].Round(time.Millisecond))
	t.Logf("heap in use at the end: %d MB", mem.HeapAlloc>>20)
	assert.NotContains(t, log.String(), "msg=panic")
	assert.NoError(t, c.Shutdown())
	if s := log.String(); s != "" {
		t.Logf("the server's log (%d lines), its start:\n%.2000s", strings.Count(s, "\n"), s)
	}
}
