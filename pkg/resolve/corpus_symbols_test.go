//go:build corpus

package resolve_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mmcdole/dgdtools/pkg/config"
	"github.com/mmcdole/dgdtools/pkg/fileset"
	"github.com/mmcdole/dgdtools/pkg/index"
	"github.com/mmcdole/dgdtools/pkg/lint"
	"github.com/mmcdole/dgdtools/pkg/resolve"
	"github.com/mmcdole/dgdtools/pkg/vfs"
)

// corpusSample is the lib TestCorpusResolve reads and its sample of
// files: $LPC_SAMPLE of them (2000 when not set), spread over the lib of
// $DGDTOOLS_CONFIG, and a few everybody inherits.
func corpusSample(t *testing.T) (*config.Project, *lint.Linter, []string) {
	cfgPath := os.Getenv("DGDTOOLS_CONFIG")
	if cfgPath == "" {
		t.Skip("DGDTOOLS_CONFIG not set")
	}
	want := 2000
	if n, err := strconv.Atoi(os.Getenv("LPC_SAMPLE")); err == nil {
		want = n
	}
	fsys := vfs.OS{}
	prj, err := config.Discover(fsys, nil, config.DiscoverOptions{ConfigPath: cfgPath})
	require.NoError(t, err)
	require.NotEmpty(t, prj.Root, "the configuration needs the whole lib")
	l, err := lint.New(prj, fsys)
	require.NoError(t, err)
	var files []string
	require.NoError(t, prj.Tree().Walk(context.Background(), fsys, prj.Root, []string{".c"}, fileset.NewMatcher(prj.Config.Exclude),
		func(p string) { files = append(files, p) }))
	slices.Sort(files)
	var sample []string
	for i := 0; i < len(files) && len(sample) < want; i += max(len(files)/want, 1) {
		sample = append(sample, files[i])
	}
	for _, rel := range []string{"std/room.c", "std/object.c", "std/living.c", "std/monster.c"} {
		if p := filepath.Join(prj.Root, rel); fsys.Exists(p) && !slices.Contains(sample, p) {
			sample = append(sample, p)
		}
	}
	return prj, l, sample
}

// TestCorpusSymbols lists the outline of every file of the sample and
// then searches the whole lib for names.
//
// Invariants: nothing panics. An outline is in the order of the file,
// each name inside its declaration and, of a function, a variable or a
// macro, written there; every function and global variable the index
// has of the file itself is in the outline at its place. A symbol the
// search finds is declared where it says: the name stands there. The
// results are in the order of their scores, and no more than asked for;
// a name searched for exactly comes first.
//
// Logged: the symbols by kind, how long an outline takes, how long the
// scan of the lib and the searches take, and how much they find.
func TestCorpusSymbols(t *testing.T) {
	_, l, sample := corpusSample(t)
	ctx := context.Background()
	kinds := map[string]int{}
	var took, slowest time.Duration
	linted, failures := 0, 0
	fail := func(format string, args ...any) {
		if failures++; failures <= 20 {
			t.Errorf(format, args...)
		}
	}
	var snap *index.Snapshot
	var names []string
	for _, file := range sample {
		doc, err := l.Lint(ctx, file, lint.Options{NoScan: true})
		if err != nil {
			continue
		}
		linted++
		snap = doc.Snapshot
		src := doc.File.Src
		start := time.Now()
		syms := doc.Symbols()
		d := time.Since(start)
		took += d
		slowest = max(slowest, d)
		at := map[uint32]string{}
		last := uint32(0)
		for _, s := range syms {
			kinds[s.Kind.String()]++
			if s.Start < last || !(s.Start <= s.NameStart && s.NameStart < s.NameEnd && s.NameEnd <= s.End && int(s.End) <= len(src)) {
				fail("%s: %s %s is at [%d,%d), its name at [%d,%d), after %d", file, s.Kind, s.Name, s.Start, s.End, s.NameStart, s.NameEnd, last)
				continue
			}
			last = s.Start
			if s.Kind != resolve.Object {
				at[s.NameStart] = s.Name
				if text := string(src[s.NameStart:s.NameEnd]); text != s.Name && text != "operator" {
					fail("%s: %s %s is written %q", file, s.Kind, s.Name, text)
				}
			}
		}
		for _, fn := range doc.Unit.Funcs {
			name := fn.Name
			if fn.Path == file && at[fn.Off] != name {
				fail("%s: function %s at %d is not in the outline", file, name, fn.Off)
			}
			if fn.Path == file && len(names) < 400 && linted%5 == 0 {
				names = append(names, name)
			}
		}
		for _, v := range doc.Unit.Vars {
			name := v.Name
			if v.Path == file && at[v.Off] != name {
				fail("%s: variable %s at %d is not in the outline", file, name, v.Off)
			}
		}
	}
	require.NotNil(t, snap)
	t.Logf("%d files: outlines of %v; %v in all, the slowest %v", linted, kinds, took, slowest)

	start := time.Now()
	require.True(t, snap.Scan(ctx))
	t.Logf("the scan of the lib: %v", time.Since(start))
	const limit = 200
	texts := map[string][]byte{}
	search := func(query string) ([]index.Symbol, time.Duration) {
		start := time.Now()
		syms, more := snap.FindSymbols(query, limit)
		d := time.Since(start)
		if len(syms) > limit || more && len(syms) != limit {
			fail("%q: %d symbols, more %v", query, len(syms), more)
		}
		for i, s := range syms {
			if i > 0 && s.Score > syms[i-1].Score {
				fail("%q: %s (%d) after %s (%d)", query, s.Name, s.Score, syms[i-1].Name, syms[i-1].Score)
			}
			src, ok := texts[s.Path]
			if !ok {
				src, _ = snap.ReadFile(s.Path)
				texts[s.Path] = src
			}
			if end := int(s.Off) + len(s.Name); end > len(src) || string(src[s.Off:end]) != s.Name {
				fail("%q: %s is not at %s:%d", query, s.Name, s.Path, s.Off)
			}
		}
		return syms, d
	}
	syms, first := search("create")
	t.Logf("the first search, which makes the table: %v, %d found", first, len(syms))
	for _, q := range []string{"c", "qn", "set_", "query_name", "ROOM", "room", "short", "zzzzqqq"} {
		syms, d := search(q)
		best := ""
		if len(syms) > 0 {
			best = syms[0].Name + " in " + syms[0].In
		}
		t.Logf("  %-12q %8v  %3d found, the best %s", q, d, len(syms), best)
	}
	var total, worst time.Duration
	exact := 0
	for _, name := range names {
		syms, d := search(name)
		total += d
		worst = max(worst, d)
		if len(syms) == 0 || syms[0].Name != name {
			fail("%q is not the first found", name)
		} else {
			exact++
		}
	}
	if len(names) > 0 {
		t.Logf("%d names of the sample searched for: %d first in their results; %v a search, the slowest %v", len(names), exact, total/time.Duration(len(names)), worst)
	}
	assert.NotZero(t, kinds["function"])
	assert.Zero(t, failures)
}
