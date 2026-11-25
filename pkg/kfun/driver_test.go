package kfun_test

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/mmcdole/dgdtools/pkg/kfun"
)

var update = flag.Bool("update", false, "write dgd.kfuns from the driver's source at $DGD_SRC")

// TestDriverSource holds the table to the driver: dgd.kfuns is what the
// source of DGD at $DGD_SRC (a checkout of github.com/dworkin/dgd, or
// its src directory) declares, and -update writes it:
//
//	DGD_SRC=~/code/dgd go test ./pkg/kfun -run TestDriverSource -update
//
// Without the variable the test is skipped: the driver is not part of
// this repository.
func TestDriverSource(t *testing.T) {
	src := os.Getenv("DGD_SRC")
	if src == "" {
		t.Skip("set DGD_SRC to a checkout of the driver to hold dgd.kfuns to it")
	}
	if _, err := os.Stat(filepath.Join(src, "src", "kfun")); err == nil {
		src = filepath.Join(src, "src")
	}
	want := driverTable(t, src)
	if *update {
		require.NoError(t, os.WriteFile("dgd.kfuns", []byte(want), 0o644))
		return
	}
	got, err := os.ReadFile("dgd.kfuns")
	require.NoError(t, err)
	assert.Equal(t, want, string(got), "dgd.kfuns is not what %s declares; -update writes it", src)
}

// driverFunc is one FUNCDEF of the driver's source.
type driverFunc struct {
	name, fn, proto string
	// cond is the build condition the entry stands under, "" for none.
	cond string
}

var (
	funcdefRE  = regexp.MustCompile(`^FUNCDEF\("([^"]+)",\s*(\w+),\s*(\w+),\s*\d+\)`)
	protoRE    = regexp.MustCompile(`(?s)\bchar (\w+)\[\]\s*=\s*\{(.*?)\};`)
	includeRE  = regexp.MustCompile(`#\s*include\s+"(\w+\.cpp)"`)
	defineRE   = regexp.MustCompile(`(?m)^#\s*define\s+(KF_\w+)\s+(\d+)`)
	versionRE  = regexp.MustCompile(`#\s*define\s+VERSION\s+"([^"]+)"`)
	condRE     = regexp.MustCompile(`^\s*#\s*(ifdef|ifndef|if|else|elif|endif)\b\s*(.*)`)
	remapRE    = regexp.MustCompile(`return (KF_\w+);`)
	identRE    = regexp.MustCompile(`^[A-Za-z_]\w*$`)
	refshiftRE = regexp.MustCompile(`\(\s*(\d+)\s*<<\s*REFSHIFT\s*\)`)
)

// wrongDocs are the functions of the driver whose comment says what
// another one does: the table says nothing of them rather than that.
var wrongDocs = map[string]string{
	"kf_asn_cmp": "it compares, and has the comment of kf_asn_sub",
}

// driverTable reads the driver's kfuns from its source and writes them
// as dgd.kfuns has them. What a program can call by name is what
// KFun::kfunc finds (kfun/table.cpp): an entry of the table past the
// first KF_BUILTINS, which are the operators and the compiler's own,
// whose name is an identifier, and the four names kfunc maps to an entry
// among those.
func driverTable(t *testing.T, src string) string {
	read := func(rel string) string {
		data, err := os.ReadFile(filepath.Join(src, filepath.FromSlash(rel)))
		require.NoError(t, err)
		return string(data)
	}
	tableCpp, tableH := read("kfun/table.cpp"), read("kfun/table.h")

	// The table is the FUNCDEFs of the files it includes, in order.
	_, body, ok := strings.Cut(tableCpp, "static KFun kforig[] = {")
	require.True(t, ok, "kfun/table.cpp has no kforig")
	body, _, _ = strings.Cut(body, "};")
	var entries []driverFunc
	protos, docs := map[string]string{}, map[string]string{}
	for _, m := range includeRE.FindAllStringSubmatch(body, -1) {
		text := read("kfun/" + m[1])
		var conds []string
		for _, line := range strings.Split(text, "\n") {
			if c := condRE.FindStringSubmatch(line); c != nil {
				switch c[1] {
				case "ifdef", "ifndef", "if":
					conds = append(conds, strings.TrimSpace(c[1]+" "+c[2]))
				case "endif":
					require.NotEmpty(t, conds, "%s: #endif without #if", m[1])
					conds = conds[:len(conds)-1]
				}
				continue
			}
			if d := funcdefRE.FindStringSubmatch(line); d != nil {
				// Every entry stands under "ifdef FUNCDEF", which is how
				// the table includes the file; anything more is a
				// condition of the build.
				var cond []string
				for _, c := range conds {
					if c != "ifdef FUNCDEF" {
						cond = append(cond, c)
					}
				}
				entries = append(entries, driverFunc{name: d[1], fn: d[2], proto: d[3], cond: strings.Join(cond, ", ")})
			}
		}
		for _, p := range protoRE.FindAllStringSubmatch(text, -1) {
			protos[p[1]] = p[2]
		}
		// What a function does: the first paragraph of the comment that
		// ends on the line above it.
		lines := strings.Split(text, "\n")
		for i, line := range lines {
			fn, _, isFunc := strings.Cut(line, "(Frame *")
			fn, isInt := strings.CutPrefix(fn, "int ")
			if !isFunc || !isInt || i == 0 || strings.TrimSpace(lines[i-1]) != "*/" {
				continue
			}
			var para []string
			for j := i - 2; j >= 0 && strings.TrimSpace(lines[j]) != "/*"; j-- {
				l := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[j]), "*"))
				if l == "" {
					para = nil // an earlier paragraph follows
					continue
				}
				para = append([]string{l}, para...)
			}
			// Its first sentence, when it goes on.
			doc, _, _ := strings.Cut(strings.Join(para, " "), ". ")
			docs[fn] = doc
		}
	}
	for fn, why := range wrongDocs {
		require.NotEmpty(t, docs[fn], "%s has no comment now (%s)", fn, why)
		delete(docs, fn)
	}

	index := map[string]int{}
	for _, d := range defineRE.FindAllStringSubmatch(tableH, -1) {
		index[d[1]], _ = strconv.Atoi(d[2])
	}
	builtins := index["KF_BUILTINS"]
	require.NotZero(t, builtins, "kfun/table.h has no KF_BUILTINS")
	require.Greater(t, len(entries), builtins)

	byName := map[string]driverFunc{}
	add := func(e driverFunc) {
		require.Empty(t, e.cond, "the kfun %s is there only in a driver built with %s: the table has no way to say so yet", e.name, e.cond)
		if old, dup := byName[e.name]; dup {
			require.Equal(t, protos[old.proto], protos[e.proto], "two kfuns called %s", e.name)
		}
		byName[e.name] = e
	}
	for _, e := range entries[builtins:] {
		if identRE.MatchString(e.name) {
			add(e)
		}
	}
	_, kfunc, ok := strings.Cut(tableCpp, "int KFun::kfunc(const char *name)")
	require.True(t, ok, "kfun/table.cpp has no KFun::kfunc")
	kfunc, _, _ = strings.Cut(kfunc, "\n}\n")
	remaps := remapRE.FindAllStringSubmatch(kfunc, -1)
	require.NotEmpty(t, remaps)
	for _, r := range remaps {
		i, ok := index[r[1]]
		require.True(t, ok && i < builtins, r[1])
		require.True(t, identRE.MatchString(entries[i].name), entries[i].name)
		delete(byName, entries[i].name)
		add(entries[i])
	}

	version := versionRE.FindStringSubmatch(read("version.h"))
	require.NotNil(t, version, "version.h has no VERSION")
	var b strings.Builder
	b.WriteString("# The kernel functions of DGD that a program can call by name: each as\n" +
		"# the driver declares it (src/kfun/*.cpp: the FUNCDEF tables and the\n" +
		"# prototypes they name, which have types and no parameter names), under\n" +
		"# the line that says what it does (the comment of the function in C).\n" +
		"#\n" +
		"# Written from the driver's source, and held to it, by\n" +
		"#   DGD_SRC=<dgd checkout> go test ./pkg/kfun -run TestDriverSource [-update]\n" +
		"# Not to be edited.\n" +
		"#\n" +
		"# source: " + version[1] + "\n")
	for _, name := range slices.Sorted(func(yield func(string) bool) {
		for name := range byName {
			if !yield(name) {
				return
			}
		}
	}) {
		e := byName[name]
		init, ok := protos[e.proto]
		require.True(t, ok, "no prototype %s for %s", e.proto, name)
		b.WriteString("\n")
		if doc := docs[e.fn]; doc != "" {
			b.WriteString("// " + doc + "\n")
		}
		b.WriteString(driverProto(t, name, init) + "\n")
	}
	return b.String()
}

// driverType reads a type of a prototype: T_STRING, T_MIXED | (1 <<
// REFSHIFT), in either order.
func driverType(t *testing.T, name, item string) string {
	stars := 0
	if m := refshiftRE.FindStringSubmatch(item); m != nil {
		stars, _ = strconv.Atoi(m[1])
		item = refshiftRE.ReplaceAllString(item, "")
	}
	item = strings.TrimSpace(strings.Trim(strings.TrimSpace(item), "|"))
	typ, ok := map[string]string{
		"T_INT": "int", "T_FLOAT": "float", "T_STRING": "string", "T_OBJECT": "object", "T_MAPPING": "mapping",
		"T_MIXED": "mixed", "T_VOID": "void", "T_LVALUE": "lvalue",
	}[item]
	require.True(t, ok, "%s: a type the table cannot write: %q", name, item)
	if stars > 0 {
		typ += " " + strings.Repeat("*", stars)
	}
	return typ
}

// driverProto writes a prototype of the driver's, the initializer of a
// char array (interpret.h, PROTO_*), as LPC:
//
//	{ class, nargs, vargs, size hi, size lo, return type, argument types... }
//
// nargs arguments must be given and vargs more may be; with C_ELLIPSIS
// in the class the last takes the rest.
func driverProto(t *testing.T, name, init string) string {
	var items []string
	for _, item := range strings.Split(init, ",") {
		items = append(items, strings.Join(strings.Fields(item), " "))
	}
	require.GreaterOrEqual(t, len(items), 6, "%s: %v", name, items)
	num := func(i int) int {
		n, err := strconv.Atoi(items[i])
		require.NoError(t, err, "%s: %v", name, items)
		return n
	}
	nargs, vargs := num(1), num(2)
	require.Equal(t, 6+nargs+vargs, num(3)<<8|num(4), "%s: the prototype's size", name)
	require.Len(t, items, 6+nargs+vargs, "%s: %v", name, items)
	ellipsis := false
	for _, c := range strings.Split(items[0], "|") {
		switch strings.TrimSpace(c) {
		case "C_STATIC", "C_TYPECHECKED":
		case "C_ELLIPSIS":
			ellipsis = true
		default:
			require.Fail(t, "a class the table cannot write", "%s: %s", name, c)
		}
	}
	var params []string
	for i, item := range items[6:] {
		p := driverType(t, name, item)
		last := i == nargs+vargs-1
		switch {
		case last && ellipsis:
			p += "..."
		case i == nargs:
			p = "varargs " + p
		}
		params = append(params, p)
	}
	ret := driverType(t, name, items[5])
	if !strings.HasSuffix(ret, "*") {
		ret += " "
	}
	return ret + name + "(" + strings.Join(params, ", ") + ")"
}

// The table as it is in the repository, whatever driver is at hand.
func TestDGD(t *testing.T) {
	tab := kfun.DGD()
	names := tab.Names()
	require.True(t, slices.IsSorted(names))
	t.Logf("%d kfuns", len(names))
	for _, name := range names {
		f := tab.Lookup(name)
		require.NotNil(t, f, name)
		assert.Equal(t, name, f.Name)
		assert.True(t, identRE.MatchString(name), name)
		assert.True(t, strings.HasPrefix(f.Origin, "DGD "), "%s: %q", name, f.Origin)
		// What is written is what is read.
		again, err := kfun.Parse(f.Decl)
		require.NoError(t, err, name)
		assert.Equal(t, f.Decl, again.Decl)
		for _, p := range f.Params {
			assert.Empty(t, p.Name, "%s: the driver names no parameters", name)
		}
	}
	decl := func(name string) string {
		f := tab.Lookup(name)
		require.NotNil(t, f, name)
		return fmt.Sprintf("%s [%d,%d]", f.Decl, f.MinArgs, f.MaxArgs)
	}
	assert.Equal(t, "mixed call_other(mixed, string, mixed...) [2,-1]", decl("call_other"))
	assert.Equal(t, "int call_out(string, mixed, mixed...) [2,-1]", decl("call_out"))
	assert.Equal(t, "int sscanf(string, string, lvalue...) [2,-1]", decl("sscanf"))
	assert.Equal(t, "string read_file(string, varargs int, int) [1,3]", decl("read_file"))
	assert.Equal(t, "string *explode(string, string) [2,2]", decl("explode"))
	assert.Equal(t, "mixed **get_dir(string) [1,1]", decl("get_dir"))
	assert.Equal(t, "object this_object() [0,0]", decl("this_object"))
	assert.Equal(t, "mixed *status(varargs mixed) [0,1]", decl("status"))
	assert.Equal(t, "int strlen(string) [1,1]", decl("strlen"))
	assert.Equal(t, "mixed **call_trace() [0,0]", decl("call_trace"))
	assert.Equal(t, "return the length of a string", tab.Lookup("strlen").Doc)
	// The operators and the compiler's own are no functions to call.
	for _, name := range []string{"nil", "sum", "+", "[..]", "new.function", "0.sscanf", "this_player", ""} {
		assert.Nil(t, tab.Lookup(name), name)
	}
	assert.Nil(t, (*kfun.Table)(nil).Lookup("strlen"))
	assert.Empty(t, (*kfun.Table)(nil).Names())
}

func TestParse(t *testing.T) {
	for proto, want := range map[string]string{
		"int *rusage()":            "int *rusage() [0,0]",
		"int  * rusage ( void ) ;": "int *rusage() [0,0]",
		"string argon2_hash(string password, int ops_limit, int mem_limit)": "string argon2_hash(string password, int ops_limit, int mem_limit) [3,3]",
		"void f(string, varargs int n, mixed *rest...)":                     "void f(string, varargs int n, mixed *rest...) [1,-1]",
		"mixed f(mixed...)":          "mixed f(mixed...) [0,-1]",
		"int f(object o, lvalue)":    "int f(object o, lvalue) [2,2]",
		"mapping m(varargs mapping)": "mapping m(varargs mapping) [0,1]",
	} {
		f, err := kfun.Parse(proto)
		require.NoError(t, err, proto)
		assert.Equal(t, want, fmt.Sprintf("%s [%d,%d]", f.Decl, f.MinArgs, f.MaxArgs), proto)
		assert.Empty(t, f.Origin)
	}
	f, err := kfun.Parse("void f(string, varargs int n, mixed *rest...)")
	require.NoError(t, err)
	assert.Equal(t, []kfun.Param{{Type: "string"}, {Type: "int", Name: "n", Optional: true}, {Type: "mixed *", Name: "rest", Optional: true}}, f.Params)
	assert.True(t, f.Ellipsis)
	assert.Equal(t, "void", f.Type)

	for _, proto := range []string{
		"", "rusage()", "int rusage", "int ()", "int f(", "int f(x)", "int f(void v)", "status f()", "lvalue f()",
		"int f(int a..., int b)", "int f(varargs int a, varargs int b)", "int f(int int)", "int 2f()", "int f() {}", "int f(int a b)",
	} {
		_, err := kfun.Parse(proto)
		assert.Error(t, err, proto)
	}
}

func TestWith(t *testing.T) {
	base := kfun.DGD()
	same, err := base.With(nil)
	require.NoError(t, err)
	assert.Same(t, base, same)

	ext, err := base.With([]string{"int *rusage()", "int strlen(string s, varargs int from)"})
	require.NoError(t, err)
	assert.Equal(t, "int *rusage()", ext.Lookup("rusage").Decl)
	assert.Empty(t, ext.Lookup("rusage").Origin)
	// The project's word of a kfun stands before the table's.
	assert.Equal(t, "int strlen(string s, varargs int from)", ext.Lookup("strlen").Decl)
	assert.Equal(t, "int strlen(string)", base.Lookup("strlen").Decl)
	assert.Nil(t, base.Lookup("rusage"))
	assert.Len(t, ext.Names(), len(base.Names())+1)

	_, err = base.With([]string{"int ok()", "nonsense"})
	assert.ErrorContains(t, err, `"nonsense"`)

	alone, err := (*kfun.Table)(nil).With([]string{"int only()"})
	require.NoError(t, err)
	assert.Equal(t, []string{"only"}, alone.Names())
}

var (
	extProtoRE = regexp.MustCompile(`(?s)static char (\w+)\[\]\s*=\s*\{(.*?)\};`)
	extKfunRE  = regexp.MustCompile(`\{\s*"(\w+)",\s*(\w+),`)
)

// TestExtensionExample holds the kfuns that the example configuration of
// a real mud declares (examples/viking/dgdtools.yml, dialect.kfuns) to
// the extension module that mud's driver loads, whose source is at
// $DGD_EXT_SRC: the same functions with the same types. The names of
// the parameters are the configuration's own; the module declares none.
func TestExtensionExample(t *testing.T) {
	var cfg struct {
		Dialect struct {
			Kfuns []string `yaml:"kfuns"`
		} `yaml:"dialect"`
	}
	data, err := os.ReadFile("../../examples/viking/dgdtools.yml")
	require.NoError(t, err)
	require.NoError(t, yaml.Unmarshal(data, &cfg))
	require.NotEmpty(t, cfg.Dialect.Kfuns)
	// The declarations without their names.
	var got []string
	for _, proto := range cfg.Dialect.Kfuns {
		f, err := kfun.Parse(proto)
		require.NoError(t, err)
		assert.Nil(t, kfun.DGD().Lookup(f.Name), "%s is the driver's own", f.Name)
		got = append(got, bare(t, f))
	}
	slices.Sort(got)

	src := os.Getenv("DGD_EXT_SRC")
	if src == "" {
		t.Skip("set DGD_EXT_SRC to the extension's source to hold the example to it")
	}
	files, err := filepath.Glob(filepath.Join(src, "*.c"))
	require.NoError(t, err)
	protos, names := map[string]string{}, map[string]string{}
	for _, file := range files {
		data, err := os.ReadFile(file)
		require.NoError(t, err)
		for _, m := range extProtoRE.FindAllStringSubmatch(string(data), -1) {
			protos[m[1]] = m[2]
		}
		for _, m := range extKfunRE.FindAllStringSubmatch(string(data), -1) {
			names[m[1]] = m[2]
		}
	}
	var want []string
	for name, proto := range names {
		init, ok := protos[proto]
		require.True(t, ok, "no prototype %s for %s", proto, name)
		f, err := kfun.Parse(extProto(t, name, init))
		require.NoError(t, err)
		want = append(want, f.Decl)
	}
	slices.Sort(want)
	require.NotEmpty(t, want, "no kfuns in %s", src)
	assert.Equal(t, want, got)
}

// bare writes a kfun's prototype without the names of its parameters.
func bare(t *testing.T, f *kfun.Func) string {
	var params []string
	for i, p := range f.Params {
		s := p.Type
		if p.Optional && (i == 0 || !f.Params[i-1].Optional) && !(f.Ellipsis && i == len(f.Params)-1) {
			s = "varargs " + s
		}
		if f.Ellipsis && i == len(f.Params)-1 {
			s += "..."
		}
		params = append(params, s)
	}
	typ := f.Type
	if !strings.HasSuffix(typ, "*") {
		typ += " "
	}
	g, err := kfun.Parse(typ + f.Name + "(" + strings.Join(params, ", ") + ")")
	require.NoError(t, err)
	return g.Decl
}

// extProto writes the prototype of an extension's kfun as LPC. A module
// gives it in the simplified form of lpc_ext.h (the driver's
// KFun::prototype reads it): the return type, the types of the
// parameters, and 0; LPC_TYPE_VARARGS stands before the first parameter
// that may be left out, and as LPC_TYPE_ELLIPSIS after the last when it
// takes the rest.
func extProto(t *testing.T, name, init string) string {
	typ := func(item string) string {
		stars := 0
		for strings.HasPrefix(item, "LPC_TYPE_ARRAY_OF(") {
			item = strings.TrimSuffix(strings.TrimPrefix(item, "LPC_TYPE_ARRAY_OF("), ")")
			stars++
		}
		typ, ok := map[string]string{
			"LPC_TYPE_INT": "int", "LPC_TYPE_FLOAT": "float", "LPC_TYPE_STRING": "string", "LPC_TYPE_OBJECT": "object",
			"LPC_TYPE_MAPPING": "mapping", "LPC_TYPE_MIXED": "mixed", "LPC_TYPE_VOID": "void", "LPC_TYPE_LVALUE": "lvalue",
		}[item]
		require.True(t, ok, "%s: a type that cannot be written: %q", name, item)
		if stars > 0 {
			typ += " " + strings.Repeat("*", stars)
		}
		return typ
	}
	var items []string
	for _, item := range strings.Split(init, ",") {
		items = append(items, strings.Join(strings.Fields(item), ""))
	}
	require.GreaterOrEqual(t, len(items), 2, name)
	require.Equal(t, "0", items[len(items)-1], name)
	ret, items := typ(items[0]), items[1:len(items)-1]
	if !strings.HasSuffix(ret, "*") {
		ret += " "
	}
	var params []string
	varargs := false
	for i, item := range items {
		switch {
		case item == "LPC_TYPE_VOID" && len(items) == 1:
		case item == "LPC_TYPE_VARARGS" || item == "LPC_TYPE_ELLIPSIS":
			if i == len(items)-1 {
				require.NotEmpty(t, params, name)
				params[len(params)-1] += "..."
			} else {
				varargs = true
			}
		default:
			p := typ(item)
			if varargs {
				p, varargs = "varargs "+p, false
			}
			params = append(params, p)
		}
	}
	return ret + name + "(" + strings.Join(params, ", ") + ")"
}
