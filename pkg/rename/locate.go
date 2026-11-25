package rename

import (
	"fmt"
	"strings"

	"github.com/mmcdole/dgdtools/pkg/index"
	"github.com/mmcdole/dgdtools/pkg/profile"
)

// Locate finds where the symbol that a file's lib path and a name stand
// for is declared: the file and the byte offset of the name, which is
// what Prepare and Rename are asked about. An object ("/std/living", ".c"
// may be written) is asked for a function or a global variable, its own
// or one it inherits (not another's private one), and then for a macro
// its file #defines; another file ("/include/std.h") for a macro. An
// object with a function and a global variable of the name is an error
// that says to name the one meant by its place; one declared in a
// profile's stub is a *Refusal, since only the checkout can be renamed.
func Locate(snap *index.Snapshot, lib, name string) (string, uint32, error) {
	lib = "/" + strings.TrimPrefix(lib, "/")
	if strings.HasSuffix(lib, ".h") {
		return macro(snap, lib, name)
	}
	lp := strings.TrimSuffix(lib, ".c")
	if !snap.ObjectExists(lp) {
		return "", 0, fmt.Errorf("%s: no such object in the mudlib", lib)
	}
	fn, isFn := snap.ChainFunc(lp, name)
	if isFn && fn.Private && fn.DefinedIn != lp {
		isFn = false
	}
	v, isVar := snap.ChainVar(lp, name)
	if isVar && v.Private && v.DefinedIn != lp {
		isVar = false
	}
	var loc index.Loc
	switch {
	case isFn && isVar:
		return "", 0, fmt.Errorf("%s has a function and a global variable %s: name the one meant by its place, FILE:LINE:COL", lp, name)
	case isFn:
		loc = fn.Loc
	case isVar:
		loc = v.Loc
	default:
		return macro(snap, lp+".c", name)
	}
	if _, file, ok := profile.SplitLocation(loc.Path); ok {
		return "", 0, refuse("'%s' is declared in %s in the mudlib profile, not in your checkout", name, file)
	}
	return loc.Path, loc.Off, nil
}

// macro locates the #define of the name in the lib file.
func macro(snap *index.Snapshot, libFile, name string) (string, uint32, error) {
	path := snap.File(libFile)
	if path == "" || !snap.HasLib() {
		return "", 0, fmt.Errorf("%s is not a file of this mudlib", libFile)
	}
	parsed, err := snap.Parse(path)
	if err != nil {
		return "", 0, fmt.Errorf("%s: no such file in the mudlib", libFile)
	}
	if parsed.Env != nil && parsed.Env.Own() != nil {
		if m := parsed.Env.Own().Macros[name]; m != nil {
			if _, file, ok := profile.SplitLocation(path); ok {
				return "", 0, refuse("'%s' is defined in %s in the mudlib profile, not in your checkout", name, file)
			}
			return path, m.NameOff, nil
		}
	}
	if strings.HasSuffix(libFile, ".h") {
		return "", 0, fmt.Errorf("%s has no #define of %s", libFile, name)
	}
	return "", 0, fmt.Errorf("%s has no function, global variable or macro %s", strings.TrimSuffix(libFile, ".c"), name)
}
