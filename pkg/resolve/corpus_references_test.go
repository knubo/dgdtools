//go:build corpus

package resolve_test

import (
	"context"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/mmcdole/dgdtools/internal/par"
	"github.com/mmcdole/dgdtools/pkg/calls"
	"github.com/mmcdole/dgdtools/pkg/config"
	"github.com/mmcdole/dgdtools/pkg/index"
	"github.com/mmcdole/dgdtools/pkg/lint"
	"github.com/mmcdole/dgdtools/pkg/profilebuild"
	"github.com/mmcdole/dgdtools/pkg/resolve"
	"github.com/mmcdole/dgdtools/pkg/token"
	"github.com/mmcdole/dgdtools/pkg/vfs"
)

// TestCorpusReferences holds the references of a sample of the
// declarations of a real lib ($DGDTOOLS_CONFIG, with an absolute root;
// $LPC_SAMPLE declarations, 60 when not set) to a search without the
// index: At asked of every name of every file the search covers
// (Snapshot.UseUnits: the lib's .c files and the headers they include),
// read again from disk. For each declaration the places References finds
// are those where At names it, the name of a macro in an inherit
// statement and the declaration itself added; nothing more, nothing
// less.
//
// The declarations are those of the places of a sample of files, of
// every kind a search goes through the index for, and a function of the
// lib's that is called everywhere. How long the scan and each search
// take is logged.
func TestCorpusReferences(t *testing.T) {
	cfgPath := os.Getenv("DGDTOOLS_CONFIG")
	if cfgPath == "" {
		t.Skip("DGDTOOLS_CONFIG not set")
	}
	want := 60
	if n, err := strconv.Atoi(os.Getenv("LPC_SAMPLE")); err == nil {
		want = n
	}
	fsys := vfs.OS{}
	prj, err := config.Discover(fsys, nil, config.DiscoverOptions{ConfigPath: cfgPath})
	require.NoError(t, err)
	require.NotEmpty(t, prj.Root, "the configuration needs the whole lib")
	l, err := lint.New(prj, fsys)
	require.NoError(t, err)
	holdReferences(t, prj, l, want)
}

// TestCorpusReferencesPartial is TestCorpusReferences in wizards'
// checkouts: $DGDTOOLS_CONFIG is the lib's configuration, with
// profile_build, and $LPC_CHECKOUTS lists trees a wizard might have
// ("players/drake,d/Valinor"). A profile is built from the lib, and each
// tree is copied, alone, to a temporary directory with a dgdtools.yml
// that names the profile. The search covers the checkout's files and is
// partial; a declaration may be a stub's.
func TestCorpusReferencesPartial(t *testing.T) {
	cfgPath, trees := os.Getenv("DGDTOOLS_CONFIG"), os.Getenv("LPC_CHECKOUTS")
	if cfgPath == "" || trees == "" {
		t.Skip("DGDTOOLS_CONFIG and LPC_CHECKOUTS not both set")
	}
	ctx := context.Background()
	fsys := vfs.OS{}
	full, err := config.Discover(fsys, nil, config.DiscoverOptions{ConfigPath: cfgPath})
	require.NoError(t, err)
	built, err := profilebuild.Build(ctx, full, fsys)
	require.NoError(t, err)
	data, err := built.Marshal()
	require.NoError(t, err)
	tmp := t.TempDir()
	prof := filepath.Join(tmp, "lib.profile")
	require.NoError(t, os.WriteFile(prof, data, 0o644))
	for _, tree := range strings.Split(trees, ",") {
		tree = strings.Trim(tree, "/ ")
		dst := filepath.Join(tmp, "co", filepath.FromSlash(tree))
		copyTree(t, filepath.Join(full.Root, filepath.FromSlash(tree)), dst)
		y, _ := yaml.Marshal(map[string]any{"profile": prof, "exclude": full.Config.Exclude})
		require.NoError(t, os.WriteFile(filepath.Join(dst, "dgdtools.yml"), y, 0o644))
		part, err := config.Discover(fsys, []string{dst}, config.DiscoverOptions{})
		require.NoError(t, err)
		require.Equal(t, "/"+tree, part.CheckoutLib)
		l, err := lint.New(part, fsys)
		require.NoError(t, err)
		t.Logf("checkout /%s:", tree)
		holdReferences(t, part, l, 20)
	}
}

// copyTree copies the regular files under src to dst.
func copyTree(t *testing.T, src, dst string) {
	require.NoError(t, filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	}))
}

// holdReferences holds the references of a sample of want declarations
// of the files of l's lib to At asked at every name of those files, as
// TestCorpusReferences says.
func holdReferences(t *testing.T, prj *config.Project, l *lint.Linter, want int) {
	ctx := context.Background()
	start := time.Now()
	require.True(t, l.Scan(ctx))
	snap := l.Index().Snapshot()
	units, complete := snap.UseUnits(ctx)
	require.True(t, complete)
	uses := 0
	for _, u := range units {
		uses += len(u.AllUses())
	}
	t.Logf("scan with the places: %d files and headers, %d places, %v", len(units), uses, time.Since(start).Round(time.Millisecond))

	// The declarations: what the places of a sample of files name, one
	// of each kind a file, and the function called most.
	type target struct {
		ref  *resolve.Ref
		file string
		off  uint32
	}
	var targets []target
	have := map[[2]string]bool{}
	step := max(len(units)/want, 1)
	for i := 0; i < len(units) && len(targets) < want; i += step {
		u := units[i]
		doc, err := l.Lint(ctx, u.Path, lint.Options{})
		if err != nil {
			continue
		}
		kinds := map[resolve.Kind]bool{}
		for _, use := range doc.Unit.AllUses() {
			ref := doc.Resolve(use.Start)
			if ref == nil || len(ref.Defs) == 0 || kinds[ref.Kind] {
				continue
			}
			k := [2]string{ref.Kind.String(), fmt.Sprintf("%s:%d", ref.Defs[0].Path, ref.Defs[0].Start)}
			if have[k] {
				continue
			}
			have[k], kinds[ref.Kind] = true, true
			targets = append(targets, target{ref, u.Path, use.Start})
		}
	}
	// The function called most often, by name, from where it is first
	// called.
	counts := map[string]int{}
	for _, u := range units {
		for _, use := range u.AllUses() {
			if use.Kind == index.UseFunction && use.Form == index.FormOwn && !use.Decl {
				counts[use.Name]++
			}
		}
	}
	names := slices.Collect(maps.Keys(counts))
	sort.Slice(names, func(i, j int) bool { return counts[names[i]] > counts[names[j]] })
	added := false
	for _, name := range names[:min(len(names), 10)] {
		for _, u := range units {
			if added {
				break
			}
			for _, use := range u.Uses(name) {
				if use.Kind != index.UseFunction || use.Form != index.FormOwn {
					continue
				}
				doc, err := l.Lint(ctx, u.Path, lint.Options{})
				if err != nil {
					break
				}
				if ref := doc.Resolve(use.Start); ref != nil && len(ref.Defs) > 0 {
					targets = append(targets, target{ref, u.Path, use.Start})
					added = true
				}
				break
			}
		}
		if added {
			break
		}
	}
	require.NotEmpty(t, targets)

	// The search without the index: At, at every name of every file.
	type key struct {
		kind  resolve.Kind
		path  string
		start uint32
	}
	byDef := map[key][]int{}
	for i, tg := range targets {
		for _, d := range tg.ref.Defs {
			byDef[key{d.Kind, d.Path, d.Start}] = append(byDef[key{d.Kind, d.Path, d.Start}], i)
		}
	}
	found := make([]map[string]bool, len(targets))
	for i := range found {
		found[i] = map[string]bool{}
	}
	var mu sync.Mutex
	reg := calls.NewRegistry(prj.Config.Conventions)
	kfuns, err := prj.Config.Kfuns()
	require.NoError(t, err)
	start = time.Now()
	asked := 0
	par.Each(ctx, units, func(u *index.Unit) {
		parsed, err := snap.Parse(u.Path)
		if err != nil {
			return
		}
		r := resolve.New(resolve.File{Tok: parsed.Tok, Tree: parsed.Tree, Env: parsed.Env, Unit: parsed.Unit, Snapshot: snap, Registry: reg, Kfuns: kfuns})
		n := 0
		hit := func(ref *resolve.Ref) {
			for _, d := range ref.Defs {
				for _, i := range byDef[key{d.Kind, d.Path, d.Start}] {
					if ref.Kind != targets[i].ref.Kind {
						continue
					}
					if (ref.Kind == resolve.Local || ref.Kind == resolve.Parameter || ref.Kind == resolve.Label) && u.Path != targets[i].file {
						continue
					}
					mu.Lock()
					found[i][fmt.Sprintf("%s:%d-%d", u.Path, ref.Start, ref.End)] = true
					mu.Unlock()
				}
			}
		}
		ask := func(off uint32, tok token.Token) {
			n++
			ref := r.At(off)
			if ref == nil {
				return
			}
			hit(ref)
			if ref.Kind == resolve.Object && tok.Kind == token.Ident {
				if m := parsed.Env.Definition(string(parsed.Tok.Text(tok))); m != nil {
					hit(&resolve.Ref{Kind: resolve.Macro, Start: tok.Off, End: tok.End,
						Defs: []resolve.Def{{Kind: resolve.Macro, Path: m.Path, Start: m.NameOff}}})
				}
			}
		}
		src := parsed.Tok.Src
		for _, tok := range parsed.Tok.Tokens {
			switch tok.Kind {
			case token.Ident, token.StringLit:
				ask(tok.Off, tok)
			case token.Directive:
				ask(tok.Off, tok)
				for off := tok.Off + 1; off < tok.End; off++ {
					if token.IsIdentByte(src[off]) && !token.IsIdentByte(src[off-1]) {
						ask(off, tok)
					}
				}
			}
		}
		mu.Lock()
		asked += n
		mu.Unlock()
	})
	t.Logf("At asked at %d places of %d files in %v", asked, len(units), time.Since(start).Round(time.Millisecond))

	// Each declaration's references, held to that.
	var lines []string
	failures := 0
	var slowest time.Duration
	for i, tg := range targets {
		doc, err := l.Lint(ctx, tg.file, lint.Options{})
		require.NoError(t, err)
		start := time.Now()
		refs := doc.References(ctx, tg.off, true)
		took := time.Since(start)
		slowest = max(slowest, took)
		require.NotNil(t, refs, "%s:%d", tg.file, tg.off)
		want := found[i]
		for _, d := range tg.ref.Defs {
			if d.Kind != resolve.Object && d.Kind != resolve.Include {
				want[fmt.Sprintf("%s:%d-%d", d.Path, d.Start, d.End)] = true
			}
		}
		got := map[string]bool{}
		files := map[string]bool{}
		for _, s := range refs.Sites {
			got[fmt.Sprintf("%s:%d-%d", s.Path, s.Start, s.End)] = true
			files[s.Path] = true
		}
		var missing, extra []string
		for p := range want {
			if !got[p] {
				missing = append(missing, p)
			}
		}
		for p := range got {
			if !want[p] {
				extra = append(extra, p)
			}
		}
		sort.Strings(missing)
		sort.Strings(extra)
		if len(missing) > 0 || len(extra) > 0 {
			failures++
			t.Errorf("%s %s (%s:%d): %d missing %v, %d more %v", tg.ref.Kind, tg.ref.Name, tg.file, tg.off,
				len(missing), missing[:min(len(missing), 5)], len(extra), extra[:min(len(extra), 5)])
		}
		lines = append(lines, fmt.Sprintf("%-9s %-28s %7d places in %6d files, %5d possible, %v",
			tg.ref.Kind, tg.ref.Name, len(refs.Sites), len(files), refs.Possible, took.Round(10*time.Microsecond)))
	}
	slices.Sort(lines)
	for _, line := range lines {
		t.Log(line)
	}
	partial := 0
	for _, tg := range targets {
		if refs := func() *resolve.References {
			doc, err := l.Lint(ctx, tg.file, lint.Options{})
			require.NoError(t, err)
			return doc.References(ctx, tg.off, false)
		}(); refs.Partial {
			partial++
		}
	}
	t.Logf("%d declarations (%d searches partial), %d differ; the slowest search %v", len(targets), partial, failures, slowest.Round(time.Millisecond))
	assert.Zero(t, failures)
	assert.Empty(t, snap.Crashed())
}
