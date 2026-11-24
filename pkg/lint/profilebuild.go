package lint

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mmcdole/dgdtools/pkg/config"
	"github.com/mmcdole/dgdtools/pkg/fileset"
	"github.com/mmcdole/dgdtools/pkg/index"
	"github.com/mmcdole/dgdtools/pkg/pp"
	"github.com/mmcdole/dgdtools/pkg/profile"
	"github.com/mmcdole/dgdtools/pkg/syntax"
	"github.com/mmcdole/dgdtools/pkg/token"
)

// BuildProfile captures the lib this linter sees as a profile: the
// driver facts and conventions from the configuration, and a stub for
// every shared file. Shared files are those matching
// profile_build.share (config.DefaultShare when empty), everything under
// the include dirs, the force-included header, the auto objects, and
// whatever those inherit or include, minus profile_build.omit.
//
// It needs the whole lib: a profile cannot be built from a partial
// checkout.
func (l *Linter) BuildProfile(ctx context.Context) (*profile.Profile, error) {
	prj, cfg, ix := l.Project, l.Project.Config, l.Index
	if prj.Root == "" || prj.Partial {
		return nil, fmt.Errorf("profile build needs the whole lib (a dgdtools.yml with root: at the lib)")
	}
	if !ix.Complete {
		return nil, fmt.Errorf("profile build: %s", strings.Join(ix.Notes, "; "))
	}
	pb := cfg.ProfileBuild
	share := pb.Share
	if len(share) == 0 {
		share = config.DefaultShare
	}
	shared := fileset.NewMatcher(share)
	omit := fileset.NewMatcher(pb.Omit)
	exclude := fileset.NewMatcher(cfg.Exclude)
	incDirs := map[string]bool{}
	for _, d := range cfg.Lint.IncludeDirs {
		incDirs[strings.Trim(d, "/")] = true
	}
	underInc := func(rel string) bool {
		for d := path.Dir(rel); d != "." && d != "/"; d = path.Dir(d) {
			if incDirs[d] {
				return true
			}
		}
		return false
	}

	p := &profile.Profile{Files: map[string]string{}}
	p.Format = profile.Format
	p.Holes = append(append([]string{}, pb.Omit...), cfg.Exclude...)
	p.Name = pb.Name
	if p.Name == "" {
		p.Name = filepath.Base(prj.Root)
	}
	d := cfg.TokenDialect()
	p.Dialect = profile.Dialect{
		Language: "lpc", Driver: "dgd", Version: "1.7",
		SlashSlash: d.SlashSlash, Closures: d.Closures,
		Typechecking: cfg.Lint.Typechecking, Predefined: pp.Predefined,
	}
	p.IncludeDirs, p.IncludeFile, p.AutoObjects = cfg.Lint.IncludeDirs, cfg.Lint.IncludeFile, cfg.Lint.AutoObjects
	p.RelativeObjectPaths = cfg.Lint.RelativeObjectPaths
	p.Checkouts = pb.Checkouts
	p.Conventions = profile.Conventions{
		SpecifierMacros: cfg.Lint.SpecifierMacros, Macros: cfg.Lint.Macros, Defines: cfg.Lint.Defines,
		CallRegistry: cfg.Lint.CallRegistry, ObjectRegistry: cfg.Lint.ObjectRegistry,
		FormatRegistry: cfg.Lint.FormatRegistry, AutosaveMarkers: cfg.Lint.AutosaveMarkers,
		VirtualPaths: cfg.Lint.VirtualPaths,
	}

	// Seeds: shared files and include dirs, by walking the lib once.
	var queue []string
	queued := map[string]bool{}
	add := func(fsPath string) {
		fsPath = filepath.Clean(fsPath)
		rel, err := filepath.Rel(prj.Root, fsPath)
		if err != nil || strings.HasPrefix(rel, "..") {
			return // outside the lib: nothing to say about it
		}
		rel = filepath.ToSlash(rel)
		if queued[fsPath] || omit.Match(rel) || !l.FS.Exists(fsPath) || l.FS.IsDir(fsPath) {
			return
		}
		queued[fsPath] = true
		queue = append(queue, fsPath)
	}
	covered := map[string]bool{}
	err := l.FS.WalkDir(prj.Root, func(fp string, de fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, rerr := filepath.Rel(prj.Root, fp)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if de.IsDir() {
			if rel != "." && (exclude.Match(rel) || exclude.MatchDir(rel) || omit.MatchDir(rel)) {
				return filepath.SkipDir
			}
			return nil
		}
		if ext := path.Ext(rel); ext != ".c" && ext != ".h" {
			return nil
		}
		if exclude.Match(rel) {
			return nil
		}
		if shared.Match(rel) || underInc(rel) {
			add(fp)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// A directory is covered when a share glob takes all of it.
	for _, g := range share {
		if dir, ok := strings.CutSuffix(strings.Trim(g, "/"), "/**"); ok && !strings.ContainsAny(dir, "*?") {
			if l.FS.IsDir(filepath.Join(prj.Root, filepath.FromSlash(dir))) && !omit.MatchDir(dir) {
				covered["/"+dir+"/"] = true
			}
		}
	}
	for d := range incDirs {
		covered["/"+d+"/"] = true
	}
	if f := cfg.Lint.IncludeFile; f != "" {
		add(filepath.Join(prj.Root, filepath.FromSlash(f)))
	}
	for _, a := range cfg.Lint.AutoObjects {
		add(fileset.FSPath(prj.Root, a))
	}

	// Closure: everything a shared file includes or inherits is needed to
	// read it. Files that are #included by another file are headers in
	// effect, whatever their extension: their macros are part of what
	// they give their includers.
	included := map[string]bool{}
	for i := 0; i < len(queue); i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		parsed := ix.Parse(queue[i])
		if parsed == nil {
			continue
		}
		for _, dep := range parsed.Unit.Deps[1:] {
			included[filepath.Clean(dep)] = true
			add(dep)
		}
		for _, inh := range parsed.Unit.Inherits {
			if inh.Resolved {
				add(fileset.FSPath(prj.Root, inh.Path))
			}
		}
	}
	for _, fsPath := range queue {
		parsed := ix.Parse(fsPath)
		if parsed == nil {
			continue
		}
		rel, _ := filepath.Rel(prj.Root, fsPath)
		p.Files["/"+filepath.ToSlash(rel)] = Stub(parsed, included[fsPath] || path.Ext(fsPath) != ".c")
	}
	for c := range covered {
		p.Covered = append(p.Covered, c)
	}
	sort.Strings(p.Covered)
	return p, nil
}

// Stub renders the public interface of a parsed file as source, the way
// a C header or a .d.ts file does: its #include directives, its inherit
// statements, and every non-private function and global variable
// declaration exactly as written (specifiers, types, parameter names,
// varargs and ellipsis), with function bodies replaced by `{}`. Comments,
// bodies, private functions and private variables are left out. A
// declaration written without a type stays without one: that matters to a
// driver with typechecking on.
//
// header says the file is #included by others: all of its macros are
// kept. A plain object keeps only the macros its declarations use (a type
// macro, a path macro in an inherit statement).
//
// A file that could not be fully read says so with
// `#pragma dgdlint partial`, which makes every lookup through it unknown
// rather than wrong.
func Stub(p *index.Parsed, header bool) string {
	f, tree := p.Tok, p.Tree
	partial := tree.Partial
	for _, e := range tree.Errors {
		if e.Severe {
			partial = true
		}
	}

	type piece struct {
		node syntax.Node
		text string
	}
	var decls []piece
	for _, d := range tree.Decls {
		switch d := d.(type) {
		case *syntax.InheritDecl:
			decls = append(decls, piece{d, index.SourceText(f, d.First, d.Last) + "\n"})
		case *syntax.FuncDecl:
			if d.Name != nil && !d.Has(token.KwPrivate) {
				end := " {}\n"
				if d.Body == nil {
					end = ";\n"
				}
				decls = append(decls, piece{d, index.FuncDeclText(f, d) + end})
			}
		case *syntax.VarDecl:
			if d.Has(token.KwPrivate) {
				continue
			}
			text := ""
			if len(d.Vars) > 0 {
				var names []string
				for _, v := range d.Vars {
					names = append(names, strings.Repeat("*", v.Stars)+v.Name.Name)
				}
				text = index.DeclPrefix(f, d.Specs, d.Type) + strings.Join(names, ", ") + ";\n"
			}
			for _, proto := range d.Protos {
				if proto.Name != nil {
					text += index.FuncDeclText(f, proto) + ";\n"
				}
			}
			if text != "" {
				decls = append(decls, piece{d, text})
			}
		case *syntax.BadDecl:
			partial = true
		}
	}

	// Which of the file's own macros do the declarations need? Close
	// over macro bodies, since one macro may be written with another.
	needed := map[string]bool{}
	if !header && len(p.View.Layers) > 0 {
		own := p.View.Layers[0].Macros
		var mark func(text string)
		mark = func(text string) {
			for _, w := range strings.FieldsFunc(text, func(r rune) bool {
				return !(r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z')
			}) {
				if m := own[w]; m != nil && !needed[w] {
					needed[w] = true
					mark(m.Body)
				}
			}
		}
		for _, d := range decls {
			mark(d.text)
		}
	}

	var b strings.Builder
	// Directives, in order. Conditionals were already resolved by branch
	// selection and are not reproduced.
	for _, i := range tree.Sel.Dirs {
		name, rest := pp.SplitDirective(f.Text(f.Tokens[i]))
		rest = strings.TrimSpace(rest)
		switch name {
		case "include":
			b.WriteString("#include " + rest + "\n")
		case "define", "undef":
			macro := rest
			if j := strings.IndexAny(rest, " \t("); j >= 0 {
				macro = rest[:j]
			}
			if header || needed[macro] {
				b.WriteString("#" + name + " " + rest + "\n")
			}
		}
	}
	for _, d := range decls {
		// A declaration in a conditional branch that was only guessed at
		// is kept under a condition the reader will also only guess at.
		if a, _ := d.node.Span(); tree.Sel.Guessed(a) {
			b.WriteString("#ifdef __DGDLINT_UNDECIDED__\n" + d.text + "#endif\n")
		} else {
			b.WriteString(d.text)
		}
	}
	if partial {
		b.WriteString("#pragma dgdlint partial\n")
	}
	return b.String()
}
