package lint

import (
	"sort"

	"github.com/mmcdole/dgdtools/pkg/diag"
)

// Groups, in display order.
const (
	GroupSyntax     = "syntax"     // the driver refuses to compile it
	GroupRuntime    = "runtime"    // compiles; fails or returns nil at run time
	GroupSuspicious = "suspicious" // legal, almost certainly not what was meant
	GroupConvention = "convention" // lib policy; opt-in
)

// Rules returns a fresh copy of the built-in rule table, sorted by code.
// The table itself is immutable: there is no registration and no global
// state.
func Rules() []*Rule {
	rules := []*Rule{
		ruleSyntaxError, ruleAdjacentStrings, ruleLegacyClosure, ruleDeclInit,
		ruleDeclAfterStmt, ruleIncludeNotFound, ruleBadDirective, ruleInheritOrder,
		ruleParamType, ruleReturnValue,

		ruleCallableNotFound, ruleStaticCrossObj, ruleUndefinedPrototype,
		ruleTargetObjectMissing, ruleCallbackArity, ruleLifecycleChain,

		ruleAssignInCondition, ruleNoEffect, ruleSscanfFormat, ruleUnreachable,

		ruleRawInheritPath, ruleMissingVisibility, ruleUnformatted,
		ruleStaticAutosaveVar, ruleUnresolvedInherit, ruleUnusedSuppression,
	}
	out := make([]*Rule, len(rules))
	for i, r := range rules {
		c := *r
		out[i] = &c
	}
	sort.SliceStable(out, func(i, j int) bool { return groupRank(out[i]) < groupRank(out[j]) })
	return out
}

func groupRank(r *Rule) int {
	switch r.Group {
	case GroupSyntax:
		return 0
	case GroupRuntime:
		return 1
	case GroupSuspicious:
		return 2
	}
	return 3
}

var ruleUnusedSuppression = &Rule{
	Code: "C006", Name: "unused-suppression", Group: GroupConvention,
	Doc: "a dgdlint: suppression comment that suppresses nothing",
	Explain: "Reported for dgdlint:disable, disable-line and disable-next-line " +
		"comments that did not match any finding in this run. Stale suppressions " +
		"hide future findings on the same line. Only meaningful when the rules the " +
		"comment names are enabled.",
	Default: false, Severity: diag.Warning,
	Run: func(*Pass) {}, // implemented by the engine, which sees all findings
}
