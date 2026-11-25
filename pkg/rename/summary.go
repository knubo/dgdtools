package rename

import (
	"fmt"
	"strconv"

	"github.com/mmcdole/dgdtools/pkg/resolve"
)

// What names the kind of symbol a plan renames, in a sentence: "function",
// "global variable", "macro", ...; "file" for a plan of moved files.
func (p *Plan) What() string {
	if p.Moves != nil {
		return "file"
	}
	switch k := p.Symbol.Kind; k {
	case resolve.Variable:
		return "global variable"
	case resolve.Local:
		return "local variable"
	case resolve.Label:
		return "inherit label"
	default:
		return k.String()
	}
}

// Title says what the plan does: "rename function query_name to qn",
// "move /std/room.c to /std/place.c".
func (p *Plan) Title() string {
	if p.Moves != nil {
		return "move " + p.Symbol.Name + " to " + p.New
	}
	return "rename " + p.What() + " " + p.Symbol.Name + " to " + p.New
}

// A Note is something a front end tells the person renaming beside the
// edits: a sentence, and the places it is about.
type Note struct {
	Kind NoteKind
	// Text is the sentence, without the places: "2 places may be the
	// renamed function and are not renamed".
	Text string
	// Places are where, in the order of the files.
	Places []Place
}

// NoteKind says what a Note is about.
type NoteKind uint8

const (
	// NotePossible: the possible places.
	NotePossible NoteKind = iota + 1
	// NoteWarning: a warning, which has no places.
	NoteWarning
	// NoteDynamic: the calls that make the name of the function they call
	// when they run, with a part the old name has.
	NoteDynamic
	// NoteUnsearchable: the other such calls.
	NoteUnsearchable
)

// Look reports whether the person renaming should look at the note's
// places before the rename is taken to be done: possible places that are
// not changed, calls that may call the renamed function. The other notes
// only inform.
func (n Note) Look(possibleMade bool) bool {
	return n.Kind == NoteDynamic || n.Kind == NotePossible && !possibleMade
}

// A Place is one place a Note is about.
type Place struct {
	Path      string
	Line, Col uint32
	// Detail says what is there: why it may be the symbol, the pattern of
	// the name a call makes.
	Detail string
}

// Notes returns what the plan has to tell beside its edits, in this
// order: the possible places (renamed too when possibleMade), the
// warnings, the calls that make the name of the function they call when
// they run and may call the renamed one, and those that cannot be
// searched.
func (p *Plan) Notes(possibleMade bool) []Note {
	var notes []Note
	if n := p.Count(Possible); n > 0 {
		var text string
		switch {
		case p.Moves != nil && len(p.PossibleEdits) > 0 && possibleMade:
			text = fmt.Sprintf("%s that may name a moved file %s changed too", Plural(n, "place"), were(n))
		case p.Moves != nil:
			text = p.PossiblePlaces() + fmt.Sprintf(" and %s not changed", are(n))
		case possibleMade:
			text = fmt.Sprintf("%s that may be the renamed %s %s renamed too", Plural(n, "place"), p.What(), were(n))
		default:
			text = p.PossiblePlaces() + fmt.Sprintf(" and %s not renamed", are(n))
		}
		note := Note{Kind: NotePossible, Text: text}
		for _, o := range p.Occurrences {
			if o.Class == Possible {
				note.Places = append(note.Places, Place{Path: o.Path, Line: o.Line, Col: o.Col, Detail: o.Reason})
			}
		}
		notes = append(notes, note)
	}
	for _, w := range p.Warnings {
		notes = append(notes, Note{Kind: NoteWarning, Text: w})
	}
	matching, others := Note{Kind: NoteDynamic}, Note{Kind: NoteUnsearchable}
	for _, d := range p.Dynamic {
		at := Place{Path: d.Path, Line: d.Line, Col: d.Col, Detail: d.Pattern}
		if d.Matches {
			matching.Places = append(matching.Places, at)
		} else {
			others.Places = append(others.Places, at)
		}
	}
	if n := len(matching.Places); n > 0 {
		matching.Text = fmt.Sprintf("%s %s the name of the function %s when %s, with a part the old name has, and may call it",
			Plural(n, "call"), verb(n, "makes", "make"), verb(n, "it calls", "they call"), verb(n, "it runs", "they run"))
		notes = append(notes, matching)
	}
	if n := len(others.Places); n > 0 {
		other := "call"
		if len(matching.Places) > 0 {
			other = "other call"
		}
		others.Text = fmt.Sprintf("%s in the mudlib %s the name of the function %s when %s, and cannot be searched",
			Plural(n, other), verb(n, "makes", "make"), verb(n, "it calls", "they call"), verb(n, "it runs", "they run"))
		notes = append(notes, others)
	}
	return notes
}

// PossiblePlaces says how many possible places the plan has, and what
// they may be: "2 places may be the renamed function", "1 place may name
// a moved file".
func (p *Plan) PossiblePlaces() string {
	n := p.Count(Possible)
	if p.Moves != nil {
		return Plural(n, "place") + " may name a moved file"
	}
	return fmt.Sprintf("%s may be the renamed %s", Plural(n, "place"), p.What())
}

// Plural says how many of a thing there are: "1 file", "2 files".
func Plural(n int, thing string) string {
	if n == 1 {
		return "1 " + thing
	}
	return strconv.Itoa(n) + " " + thing + "s"
}

func verb(n int, one, more string) string {
	if n == 1 {
		return one
	}
	return more
}

func are(n int) string  { return verb(n, "is", "are") }
func were(n int) string { return verb(n, "was", "were") }
