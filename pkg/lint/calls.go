package lint

import (
	"github.com/mmcdole/dgdtools/pkg/calls"
	"github.com/mmcdole/dgdtools/pkg/index"
)

// callFacts are the file's call sites and, for the named ones, what the
// index says of each, asked on first need and once for all the rules.
type callFacts struct {
	*calls.Facts
	lookups []*lookedUp
}

type lookedUp struct {
	index.Lookup
	target string // the object looked in, for messages
}

// calls extracts (once per file) every call site from the tree.
func (p *Pass) calls() *callFacts {
	if p.shared.calls == nil {
		cf := calls.Extract(calls.File{
			Tree: p.Tree, Env: p.Env, LibPath: p.LibPath, ObjectPath: p.Index.ObjectPath,
		}, p.registry)
		p.shared.calls = &callFacts{Facts: cf, lookups: make([]*lookedUp, len(cf.Named))}
		st := &p.shared.stats
		st.callSites += len(cf.Named)
		for _, c := range cf.Named {
			if c.Target == calls.TargetUnknown {
				st.unknownReceivers++
			}
		}
	}
	return p.shared.calls
}

// lookup resolves the i'th named call through the index, once for all
// the rules that ask.
func (p *Pass) lookup(i int) (index.Lookup, string) {
	cf := p.calls()
	if cf.lookups[i] == nil {
		c, lk := cf.Named[i], &lookedUp{}
		switch c.Target {
		case calls.TargetSelf:
			lk.Lookup, lk.target = p.Index.LookupCallable(p.Ctx, p.LibPath, c.Func, true), p.LibPath+" (this_object)"
		case calls.TargetPath:
			lk.Lookup, lk.target = p.Index.LookupCallable(p.Ctx, c.TargetPath, c.Func, false), c.TargetPath
		}
		cf.lookups[i] = lk
	}
	return cf.lookups[i].Lookup, cf.lookups[i].target
}
