package diag

import "slices"

// SafeEdits returns the edits of every safe fix of the diagnostics (of
// one file), in the order of the text: what a fix-all applies. Of two
// fixes that would change the same text the first is taken; the other is
// there again when the file is next linted. fixes is how many fixes the
// edits are of.
func SafeEdits(ds []Diagnostic) (edits []TextEdit, fixes int) {
	var safe []Fix
	for _, d := range ds {
		for _, fix := range d.Fixes {
			if fix.Applicability == Safe && len(fix.Edits) > 0 {
				safe = append(safe, fix)
			}
		}
	}
	first := func(f Fix) uint32 {
		return slices.MinFunc(f.Edits, func(a, b TextEdit) int { return int(a.Start) - int(b.Start) }).Start
	}
	slices.SortStableFunc(safe, func(a, b Fix) int { return int(first(a)) - int(first(b)) })
	var all []TextEdit
	for _, fix := range safe {
		clash := slices.ContainsFunc(fix.Edits, func(e TextEdit) bool {
			return slices.ContainsFunc(all, func(have TextEdit) bool {
				return e.Start < have.End && have.Start < e.End || e.Start == have.Start
			})
		})
		if !clash {
			all = append(all, fix.Edits...)
			fixes++
		}
	}
	slices.SortStableFunc(all, func(a, b TextEdit) int { return int(a.Start) - int(b.Start) })
	return all, fixes
}

// Apply returns src with the edits made: edits in the order of the
// text, none overlapping another, as SafeEdits returns them. False when
// they are not so, or reach past the end of src.
func Apply(src []byte, edits []TextEdit) ([]byte, bool) {
	var out []byte
	last := uint32(0)
	for _, e := range edits {
		if e.Start < last || e.End < e.Start || int(e.End) > len(src) {
			return nil, false
		}
		out = append(append(out, src[last:e.Start]...), e.NewText...)
		last = e.End
	}
	return append(out, src[last:]...), true
}
