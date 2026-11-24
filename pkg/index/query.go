package index

import (
	"context"

	"github.com/mmcdole/dgdtools/pkg/pp"
)

// Query asks a snapshot the cross-file questions that linting one file
// raises. Each object asked about is remembered as something that file's
// diagnostics depend on, which is how Invalidate knows that a change to
// the object affects the file. A Query belongs to one lint of one file
// and is not safe for concurrent use.
type Query struct {
	s    *Snapshot
	unit *Unit
	// noScan makes the query decline questions that need the whole-lib
	// scan when it has not completed, instead of running it.
	noScan     bool
	needsScan  bool
	askedAbout map[string]struct{}
	// users is the answer to "who inherits the file", once asked.
	users *usersAnswer
}

type usersAnswer struct {
	users []string
	ok    bool
}

// Query returns a query on behalf of the file unit describes. With
// noScan, a question that needs the whole-lib scan is answered "unknown"
// until the scan has completed (see NeedsScan) rather than starting it.
func (s *Snapshot) Query(unit *Unit, noScan bool) *Query {
	return &Query{s: s, unit: unit, noScan: noScan, askedAbout: map[string]struct{}{}}
}

// Snapshot returns the generation the query reads.
func (q *Query) Snapshot() *Snapshot { return q.s }

// NeedsScan reports whether some answer was "unknown" only because the
// whole-lib scan has not completed: asking again after Snapshot.Scan may
// find more.
func (q *Query) NeedsScan() bool { return q.needsScan }

// about records that the file's diagnostics depend on the object at
// libPath.
func (q *Query) about(libPath string) {
	if libPath == "" || libPath == q.unit.LibPath {
		return
	}
	if _, told := q.askedAbout[libPath]; told {
		return
	}
	q.askedAbout[libPath] = struct{}{}
	ix := q.s.ix
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ix.cur != q.s {
		return
	}
	set := ix.refs[libPath]
	if set == nil {
		set = map[string]uint64{}
		ix.refs[libPath] = set
	}
	set[q.unit.Path] = q.s.gen
}

// usersOf answers Snapshot.UsersOf for the file itself, once, and records
// that the file's diagnostics depend on who inherits it.
func (q *Query) usersOf(ctx context.Context, libPath string) ([]string, bool) {
	if q.users != nil {
		return q.users.users, q.users.ok
	}
	ix := q.s.ix
	ix.mu.Lock()
	if ix.cur == q.s {
		ix.asked[q.unit.Path] = q.s.gen
	}
	ix.mu.Unlock()
	a := &usersAnswer{}
	if q.noScan {
		a.users, a.ok = q.s.users(libPath)
		q.needsScan = !a.ok
	} else {
		a.users, a.ok = q.s.UsersOf(ctx, libPath)
	}
	if a.ok || ctx.Err() == nil {
		q.users = a // an answer cut short by cancellation is not kept
	}
	return a.users, a.ok
}

// ObjectPath turns an object path as written in the file into an absolute
// lib path.
func (q *Query) ObjectPath(p string) string { return q.s.ObjectPath(p, q.unit.LibPath) }

// Covers reports whether "not found" at libPath means "does not exist".
func (q *Query) Covers(libPath string) bool { return q.s.Covers(libPath) }

// IsVirtual reports whether libPath is served by a virtual-object daemon.
func (q *Query) IsVirtual(libPath string) bool { return q.s.IsVirtual(libPath) }

// ObjectExists reports whether libPath is backed by a file or a stub.
func (q *Query) ObjectExists(libPath string) bool {
	q.about(libPath)
	return q.s.ObjectExists(libPath)
}

// Chain returns the flattened inherit chain of the object at libPath.
func (q *Query) Chain(libPath string) *Chain {
	q.about(libPath)
	return q.s.Chain(libPath)
}

// LookupCallable resolves function fn in the object at target; see
// Snapshot.LookupCallable. With self, target is the file itself.
func (q *Query) LookupCallable(ctx context.Context, target, fn string, self bool) Lookup {
	q.about(target)
	return q.s.lookupCallable(ctx, target, fn, self, q.usersOf)
}

// ProvidedByUsers reports whether an object that inherits or includes
// the file supplies a real definition of fn; see
// Snapshot.ProvidedByUsers.
func (q *Query) ProvidedByUsers(ctx context.Context, fn string) bool {
	return q.s.providedByUsers(ctx, q.unit.LibPath, fn, q.usersOf)
}

// IncludeDeclares reports whether the file one of the file's #includes
// resolves to declares a function or a variable; see
// Snapshot.IncludeDeclares.
func (q *Query) IncludeDeclares(inc pp.Include) (string, bool) {
	return q.s.IncludeDeclares(q.unit.Path, inc)
}
