//go:build corpus

package resolve_test

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/mmcdole/dgdtools/pkg/calls"
	"github.com/mmcdole/dgdtools/pkg/config"
	"github.com/mmcdole/dgdtools/pkg/fileset"
	"github.com/mmcdole/dgdtools/pkg/index"
	"github.com/mmcdole/dgdtools/pkg/lexer"
	"github.com/mmcdole/dgdtools/pkg/lint"
	"github.com/mmcdole/dgdtools/pkg/pp"
	"github.com/mmcdole/dgdtools/pkg/profile"
	"github.com/mmcdole/dgdtools/pkg/profilebuild"
	"github.com/mmcdole/dgdtools/pkg/resolve"
	"github.com/mmcdole/dgdtools/pkg/syntax"
	"github.com/mmcdole/dgdtools/pkg/token"
	"github.com/mmcdole/dgdtools/pkg/vfs"
)

// TestCorpusResolve asks, in a sample of the files of a real lib
// ($DGDTOOLS_CONFIG, with an absolute root; $LPC_SAMPLE files, 2000 when
// not set), what every name refers to: each identifier and string
// constant of the code, each name in a directive.
//
// Invariants: nothing panics; the text a reference names is in the file;
// and every declaration it resolves to is one: the file is there, the
// name stands at the place given, and reading that file again finds a
// declaration of that kind and name exactly there (a function's
// definition or prototype, a variable's declarator, a #define, an
// inherit statement's label), the file an #include or an object path
// names being the one of that name.
//
// How many references of each kind resolve is logged, not asserted.
func TestCorpusResolve(t *testing.T) {
	cfgPath := os.Getenv("DGDTOOLS_CONFIG")
	if cfgPath == "" {
		t.Skip("DGDTOOLS_CONFIG not set")
	}
	want := 2000
	if n, err := strconv.Atoi(os.Getenv("LPC_SAMPLE")); err == nil {
		want = n
	}
	ctx := context.Background()
	fsys := vfs.OS{}
	prj, err := config.Discover(fsys, nil, config.DiscoverOptions{ConfigPath: cfgPath})
	require.NoError(t, err)
	require.NotEmpty(t, prj.Root, "the configuration needs the whole lib")
	l, err := lint.New(prj, fsys)
	require.NoError(t, err)

	var files []string
	require.NoError(t, prj.Tree().Walk(ctx, fsys, prj.Root, []string{".c"}, fileset.NewMatcher(prj.Config.Exclude),
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

	v := &verifier{t: t, cfg: prj.Config, decls: map[string]*declared{}}
	type count struct{ refs, resolved, defs int }
	counts := map[string]*count{}
	unresolved := map[string]map[string]int{}
	var asked, idents, named int
	var took time.Duration
	var slowest time.Duration
	var slowestAt string
	linted := 0
	for _, file := range sample {
		doc, err := l.Lint(ctx, file, lint.Options{NoScan: true})
		if err != nil {
			continue // a dangling link, a file that is not text
		}
		linted++
		v.snap = doc.Snapshot
		src := doc.File.Src
		seen := map[uint32]bool{}
		ask := func(ti int, off uint32) {
			start := time.Now()
			ref := doc.Resolve(off)
			d := time.Since(start)
			took += d
			asked++
			if d > slowest {
				slowest, slowestAt = d, fmt.Sprintf("%s:%d", file, off)
			}
			if ref == nil || seen[ref.Start] {
				return
			}
			seen[ref.Start] = true
			named++
			if int(ref.End) > len(src) || ref.Start >= ref.End || off < ref.Start && doc.File.Tokens[ti].Kind != token.Directive || off > ref.End {
				v.fail("%s:%d: reference %s %s is at [%d,%d)", file, off, ref.Kind, ref.Name, ref.Start, ref.End)
				return
			}
			kind := form(doc, ti, ref)
			c := counts[kind]
			if c == nil {
				c = &count{}
				counts[kind] = c
			}
			c.refs++
			if len(ref.Defs) > 0 {
				c.resolved++
			} else {
				if unresolved[kind] == nil {
					unresolved[kind] = map[string]int{}
				}
				unresolved[kind][ref.Name]++
			}
			c.defs += len(ref.Defs)
			v.check(file, src, ref)
		}
		for ti, tok := range doc.File.Tokens {
			switch tok.Kind {
			case token.Ident:
				idents++
				ask(ti, tok.Off)
			case token.StringLit:
				ask(ti, tok.Off+(tok.End-tok.Off)/2)
			case token.Directive:
				// The line as a whole, and each word of it.
				ask(ti, tok.Off)
				for off := tok.Off + 1; off < tok.End; off++ {
					if token.IsIdentByte(src[off]) && !token.IsIdentByte(src[off-1]) {
						ask(ti, off)
					}
				}
			}
		}
	}

	t.Logf("%d of %d files, %d identifiers, %d places asked about, %d that name something", linted, len(files), idents, asked, named)
	t.Logf("%-34s %9s %9s %6s", "what", "refs", "resolved", "")
	total := count{}
	for _, kind := range slices.Sorted(maps.Keys(counts)) {
		c := counts[kind]
		total.refs += c.refs
		total.resolved += c.resolved
		total.defs += c.defs
		extra := ""
		if c.defs > c.resolved {
			extra = fmt.Sprintf("  (%d declarations)", c.defs)
		}
		t.Logf("%-34s %9d %9d %5.1f%%%s", kind, c.refs, c.resolved, 100*float64(c.resolved)/float64(c.refs), extra)
	}
	t.Logf("%-34s %9d %9d %5.1f%%", "all", total.refs, total.resolved, 100*float64(total.resolved)/float64(total.refs))
	for _, kind := range []string{"function: direct call", "function: ::fn()", "function: label::fn()", "function: -> a known object", "function: -> this_object()", "function: callback, this_object()", "include", "object: inherit", "macro"} {
		type nc struct {
			name string
			n    int
		}
		var top []nc
		for name, n := range unresolved[kind] {
			top = append(top, nc{name, n})
		}
		sort.Slice(top, func(i, j int) bool { return top[i].n > top[j].n || top[i].n == top[j].n && top[i].name < top[j].name })
		var names []string
		for _, e := range top[:min(len(top), 12)] {
			names = append(names, fmt.Sprintf("%s (%d)", e.name, e.n))
		}
		if len(names) > 0 {
			t.Logf("not resolved, %s: %d names, most often %s", kind, len(top), strings.Join(names, ", "))
		}
	}
	t.Logf("declarations checked: %d in %d files; %v for a question on average, %v at most (%s)",
		v.checked, len(v.decls), (took / time.Duration(max(asked, 1))).Round(100*time.Nanosecond), slowest.Round(time.Microsecond), slowestAt)
	assert.Zero(t, v.failures, "see the log")
	assert.NotZero(t, total.resolved)
	assert.Empty(t, l.Index().Snapshot().Crashed())
}

// form says what kind of reference ref, at token ti, is, as the numbers
// are reported: its kind and, for a function, how it is named.
func form(doc *lint.Document, ti int, ref *resolve.Ref) string {
	toks := doc.File.Tokens
	prev := func(i int) int {
		for i--; i >= 0; i-- {
			if !toks[i].Kind.IsTrivia() {
				return i
			}
		}
		return -1
	}
	directive := toks[ti].Kind == token.Directive
	switch ref.Kind {
	case resolve.Object:
		for _, n := range syntax.PathAt(doc.Tree, ti) {
			if _, ok := n.(*syntax.InheritDecl); ok {
				return "object: inherit"
			}
		}
		return "object: path argument"
	case resolve.Macro:
		switch {
		case ref.Decl:
			return "macro: its #define"
		case directive:
			return "macro: in a directive"
		}
		return "macro"
	case resolve.Variable, resolve.Label:
		if ref.Decl {
			return ref.Kind.String() + ": its declaration"
		}
		return ref.Kind.String()
	case resolve.Function:
		if c := ref.Call; c != nil {
			how := "callback"
			if c.Via == "->" {
				how = "->"
			} else if c.Kind == calls.CrossObject {
				how = "call_other"
			}
			switch c.Target {
			case calls.TargetSelf:
				return "function: " + how + ", this_object()"
			case calls.TargetPath:
				return "function: " + how + " a known object"
			}
			return "function: " + how + " an unknown receiver"
		}
		if ref.Decl {
			return "function: its declaration"
		}
		if p := prev(ti); p >= 0 && toks[p].Kind == token.ColonColon {
			if before := prev(p); before >= 0 && toks[before].Kind == token.Ident {
				return "function: label::fn()"
			}
			return "function: ::fn()"
		}
		return "function: direct call"
	}
	return ref.Kind.String()
}

// verifier holds declarations to the files they are said to be in.
type verifier struct {
	t        *testing.T
	cfg      *config.Config
	snap     *index.Snapshot
	decls    map[string]*declared
	checked  int
	failures int
	// stubs says the lib is a checkout (the directory named) and a
	// profile: a declaration is in the one or in a stub of the other.
	stubs    bool
	checkout string
}

// declared is what reading a file finds declared in it, by the offset of
// the name.
type declared struct {
	src                          []byte
	funcs, vars, macros, inherit map[uint32]string
}

func (v *verifier) fail(format string, args ...any) {
	if v.failures++; v.failures <= 30 {
		v.t.Errorf(format, args...)
	}
}

var defineName = regexp.MustCompile(`^#(?:[ \t]|\\\r?\n|/\*(?s:.*?)\*/)*define(?:[ \t]|\\\r?\n|/\*(?s:.*?)\*/)*([A-Za-z_][A-Za-z_0-9]*)`)

// read reads the file at path the two ways the index reads a file: as a
// program's source, after the force-included header and with its
// includes, and as a header, by itself.
func (v *verifier) read(path string) *declared {
	if d, ok := v.decls[path]; ok {
		return d
	}
	var d *declared
	defer func() { v.decls[path] = d }()
	src, err := v.snap.ReadFile(path)
	if err != nil {
		return nil
	}
	d = &declared{src: src, funcs: map[uint32]string{}, vars: map[uint32]string{}, macros: map[uint32]string{}, inherit: map[uint32]string{}}
	collect := func(tree *syntax.File) {
		tree.Funcs(func(fn *syntax.FuncDecl) bool {
			if fn.Name != nil {
				d.funcs[tree.Off(fn.Name)] = fn.Name.Name
			}
			return true
		})
		for _, decl := range tree.Decls {
			switch decl := decl.(type) {
			case *syntax.VarDecl:
				for _, dv := range decl.Vars {
					d.vars[tree.Off(dv.Name)] = dv.Name.Name
				}
			case *syntax.InheritDecl:
				if decl.Label != nil {
					d.inherit[tree.Off(decl.Label)] = decl.Label.Name
				}
			}
		}
	}
	if parsed, err := v.snap.Parse(path); err == nil {
		collect(parsed.Tree)
	}
	f := lexer.Lex(path, src, v.cfg.TokenDialect())
	env := pp.NewEnv(v.cfg.Predefined(), v.cfg.MacroClasses())
	env.Header = true
	collect(syntax.Parse(f, syntax.Options{Env: env, Sel: env.Scan(f, nil)}))
	for _, tok := range f.Tokens {
		if tok.Kind == token.Directive {
			if m := defineName.FindSubmatchIndex(f.Text(tok)); m != nil {
				d.macros[tok.Off+uint32(m[2])] = string(f.Text(tok)[m[2]:m[3]])
			}
		}
	}
	return d
}

// check holds every declaration of ref, a reference in file, to the
// file it is said to be in.
func (v *verifier) check(file string, src []byte, ref *resolve.Ref) {
	at := fmt.Sprintf("%s:%d: %s %s", file, ref.Start, ref.Kind, ref.Name)
	named := string(src[ref.Start:ref.End])
	switch ref.Kind {
	case resolve.Function, resolve.Variable, resolve.Macro, resolve.Label:
		if named != ref.Name {
			v.fail("%s: the text there is %q", at, named)
		}
	case resolve.Include:
		if !strings.Contains(named, ref.Name) {
			v.fail("%s: the text there is %q", at, named)
		}
	}
	for _, def := range ref.Defs {
		v.checked++
		if def.Kind != ref.Kind || def.Name != ref.Name {
			v.fail("%s: resolves to %s %s", at, def.Kind, def.Name)
			continue
		}
		if _, _, stub := profile.SplitLocation(def.Path); stub != v.stubs && (stub || !strings.HasPrefix(def.Path, v.checkout)) {
			v.fail("%s: a stub in a whole lib, or a file of the disk outside a checkout: %s", at, def.Path)
			continue
		}
		d := v.read(def.Path)
		if d == nil {
			v.fail("%s: %s cannot be read", at, def.Path)
			continue
		}
		there := fmt.Sprintf("%s:%d", def.Path, def.Start)
		var found map[uint32]string
		switch def.Kind {
		case resolve.Include:
			if path.Base(filepath.ToSlash(def.Path)) != path.Base(ref.Name) || def.Start != 0 || def.End != 0 {
				v.fail("%s: is not %s", at, there)
			}
			continue
		case resolve.Object:
			if def.Path != v.snap.ObjectFile(ref.Name) || !strings.HasSuffix(filepath.ToSlash(def.Path), ref.Name+".c") && !strings.HasSuffix(filepath.ToSlash(def.Path), ref.Name) {
				v.fail("%s: is not %s", at, there)
			}
			continue
		case resolve.Function:
			found = d.funcs
			if def.Func == nil || def.Object == "" {
				v.fail("%s: no facts of the function at %s", at, there)
			}
		case resolve.Variable:
			found = d.vars
			if def.Var == nil {
				v.fail("%s: no facts of the variable at %s", at, there)
			}
		case resolve.Macro:
			found = d.macros
			if def.Macro == nil && !ref.Decl {
				v.fail("%s: no facts of the macro at %s", at, there)
			}
		case resolve.Label:
			found = d.inherit
			if def.Path != file {
				v.fail("%s: a label of another file: %s", at, there)
			}
		}
		if int(def.End) > len(d.src) || !bytes.Equal(d.src[def.Start:def.End], []byte(ref.Name)) {
			v.fail("%s: the name is not at %s", at, there)
			continue
		}
		if found[def.Start] != ref.Name {
			v.fail("%s: nothing declares it at %s", at, there)
		}
	}
}

// TestCorpusResolvePartial is the same question asked by a wizard who
// has one tree of the lib and a profile. $DGDTOOLS_CONFIG is the whole
// lib's configuration, with profile_build; $LPC_CHECKOUTS lists trees a
// wizard might have ("players/drake,d/Valinor"). A profile is built from
// the lib; each tree is copied, alone, to a temporary directory, and
// every name in its files is resolved twice: there, with the profile,
// and in place, with the whole lib.
//
// Invariants: every declaration the checkout resolves to is one, in the
// checkout or in a stub of the profile (as in TestCorpusResolve); and
// where both sides resolve a name they agree on the lib file it is
// declared in, all but a few in a thousand. What the checkout cannot
// resolve and the whole lib can (code in neither the checkout nor the
// profile) is logged.
func TestCorpusResolvePartial(t *testing.T) {
	cfgPath, trees := os.Getenv("DGDTOOLS_CONFIG"), os.Getenv("LPC_CHECKOUTS")
	if cfgPath == "" || trees == "" {
		t.Skip("DGDTOOLS_CONFIG and LPC_CHECKOUTS not both set")
	}
	ctx := context.Background()
	fsys := vfs.OS{}
	full, err := config.Discover(fsys, nil, config.DiscoverOptions{ConfigPath: cfgPath})
	require.NoError(t, err)
	fl, err := lint.New(full, fsys)
	require.NoError(t, err)
	built, err := profilebuild.Build(ctx, full, fsys)
	require.NoError(t, err)
	data, err := built.Marshal()
	require.NoError(t, err)
	tmp := t.TempDir()
	prof := filepath.Join(tmp, "lib.profile")
	require.NoError(t, os.WriteFile(prof, data, 0o644))
	t.Logf("profile %s: %d stubs, %d bytes", built.Name, len(built.Files), len(data))

	// What a reference resolves to, as both sides can say it: the kind,
	// the name and the lib file of each declaration.
	targets := func(doc *lint.Document, ref *resolve.Ref) string {
		var out []string
		for _, d := range ref.Defs {
			out = append(out, fmt.Sprintf("%s %s in %s", d.Kind, d.Name, doc.Snapshot.LibPath(d.Path)))
		}
		slices.Sort(out)
		return strings.Join(out, ", ")
	}
	var total struct{ files, same, neither, lost, gained, differ, onlyWhole, onlyPartial, stubs, checked int }
	failures := 0
	for _, tree := range strings.Split(trees, ",") {
		tree = strings.Trim(tree, "/ ")
		src := filepath.Join(full.Root, filepath.FromSlash(tree))
		dst := filepath.Join(tmp, "co", filepath.FromSlash(tree))
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
		y, _ := yaml.Marshal(map[string]any{"profile": prof, "exclude": full.Config.Exclude})
		require.NoError(t, os.WriteFile(filepath.Join(dst, "dgdtools.yml"), y, 0o644))
		part, err := config.Discover(fsys, []string{dst}, config.DiscoverOptions{})
		require.NoError(t, err)
		require.Equal(t, "/"+tree, part.CheckoutLib, "the checkout is recognized from its path")
		pl, err := lint.New(part, fsys)
		require.NoError(t, err)

		var files []string
		require.NoError(t, part.Tree().Walk(ctx, fsys, dst, []string{".c"}, fileset.NewMatcher(part.Config.Exclude),
			func(p string) { files = append(files, p) }))
		slices.Sort(files)
		v := &verifier{t: t, cfg: part.Config, decls: map[string]*declared{}, stubs: true, checkout: dst}
		var n struct{ same, neither, lost, gained, differ, onlyWhole, onlyPartial, stubs int }
		shown := 0
		for _, file := range files {
			rel, _ := filepath.Rel(dst, file)
			pdoc, err := pl.Lint(ctx, file, lint.Options{NoScan: true})
			if err != nil {
				continue
			}
			fdoc, err := fl.Lint(ctx, filepath.Join(src, rel), lint.Options{NoScan: true})
			if err != nil || !bytes.Equal(fdoc.File.Src, pdoc.File.Src) {
				continue
			}
			total.files++
			v.snap = pdoc.Snapshot
			text := pdoc.File.Src
			seen := map[uint32]bool{}
			ask := func(off uint32) {
				pr, fr := pdoc.Resolve(off), fdoc.Resolve(off)
				switch {
				case pr == nil && fr == nil:
					return
				case pr == nil:
					if !seen[fr.Start] {
						seen[fr.Start] = true
						n.onlyWhole++
					}
					return
				case seen[pr.Start]:
					return
				}
				seen[pr.Start] = true
				v.check(file, text, pr)
				for _, d := range pr.Defs {
					if _, _, stub := profile.SplitLocation(d.Path); stub {
						n.stubs++
					}
				}
				if fr == nil {
					n.onlyPartial++
					return
				}
				got, want := targets(pdoc, pr), targets(fdoc, fr)
				switch {
				case got == want && got == "":
					n.neither++
				case got == want:
					n.same++
				case got == "":
					n.lost++
				default:
					if want == "" {
						n.gained++
					} else {
						n.differ++
					}
					if shown++; shown <= 4 {
						t.Logf("  %s:%d %s %s: with the profile %q, in the whole lib %q", rel, pr.Start, pr.Kind, pr.Name, got, want)
					}
				}
			}
			for _, tok := range pdoc.File.Tokens {
				switch tok.Kind {
				case token.Ident:
					ask(tok.Off)
				case token.StringLit:
					ask(tok.Off + (tok.End-tok.Off)/2)
				case token.Directive:
					ask(tok.Off)
					for off := tok.Off + 1; off < tok.End; off++ {
						if token.IsIdentByte(text[off]) && !token.IsIdentByte(text[off-1]) {
							ask(off)
						}
					}
				}
			}
		}
		t.Logf("%-18s %4d files: same %6d, in a stub %6d, neither %5d, lost %4d, gained %3d, differ %3d, named by the whole lib only %4d, by the checkout only %3d",
			tree, len(files), n.same, n.stubs, n.neither, n.lost, n.gained, n.differ, n.onlyWhole, n.onlyPartial)
		total.same += n.same
		total.neither += n.neither
		total.lost += n.lost
		total.gained += n.gained
		total.differ += n.differ
		total.onlyWhole += n.onlyWhole
		total.onlyPartial += n.onlyPartial
		total.stubs += n.stubs
		total.checked += v.checked
		failures += v.failures
	}
	t.Logf("%-18s %4d files: same %6d, in a stub %6d, neither %5d, lost %4d, gained %3d, differ %3d, named by the whole lib only %4d, by the checkout only %3d; %d declarations checked",
		"TOTAL", total.files, total.same, total.stubs, total.neither, total.lost, total.gained, total.differ, total.onlyWhole, total.onlyPartial, total.checked)
	assert.Zero(t, failures, "see the log")
	assert.NotZero(t, total.same)
	assert.NotZero(t, total.stubs)
	assert.LessOrEqual(t, (total.gained+total.differ)*1000, 5*(total.same+total.lost), "the checkout resolves a name to something else than the whole lib does")
}
