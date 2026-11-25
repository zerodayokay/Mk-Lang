// Why: passes run on many goroutines and report in any order, yet the user,
// the editor and the corpus need the same report for the same program every
// time, showing root causes and not their echoes.
//
// What: Order, which turns every diagnostic of a build into a Report; Emitted;
// WriteJSON and WriteDiagnostic for the JSON envelope; Text for a terminal.
//
// How: Order keeps internal errors, the lowest failing phase and the causes
// those name, sorts by a total key (span, code, message, then the rendered
// rest), drops duplicates, resolves each Cause to a root index, then cuts the
// lowest phase's diagnostics to the cap. Diagnostics are collected and
// sorted before the cut, never cut on arrival.

package diag

import (
	"cmp"
	"slices"
	"strconv"
	"strings"

	"mk/internal/source"
)

// Emitted is one diagnostic in its final position: Root is the 1-based index,
// within the report, of the diagnostic it is a consequence of (its own index
// for a root cause).
type Emitted struct {
	Root int
	Diag *Diagnostic
}

// Report is what the driver prints: the diagnostics of the earliest failing
// phase plus every E0000 and the causes they name, sorted, deduplicated,
// roots resolved, then the phase's diagnostics cut to the cap. The same set
// of input diagnostics always gives the same Report, whatever order they
// arrived in.
type Report struct {
	// Phase is the earliest non-internal phase present, or PhaseInternal when
	// only internal errors are present. HasPhase is false when there are none.
	Phase    Phase
	HasPhase bool
	// Diags are the kept diagnostics in emission order.
	Diags []Emitted
	// Omitted is how many diagnostics the cap dropped (internal errors and
	// causes of a later phase are never dropped).
	Omitted int
}

// NoCap passed as max to Order keeps every diagnostic: the default for JSON.
// Text output uses DefaultTextCap.
const NoCap = 0

// DefaultTextCap is the number of diagnostics text output shows by default
// (a chosen default, not measured).
const DefaultTextCap = 50

// Order applies the phase rule and the ordering of doc/tools.md
// and doc/compiler.md to every diagnostic of a build:
//
//  1. Keep every PhaseInternal diagnostic and those of the lowest other phase,
//     and every diagnostic a kept one names as its Cause, whatever its phase.
//  2. Sort by (file path bytewise, start line, start column, code, message),
//     then by end position, then by the rest of the rendered diagnostic
//     (facts, fixes, alternatives, phase) and its cause, so the order is
//     total and never depends on arrival order.
//  3. Drop a diagnostic whose (code, span, message) equals the previous one's:
//     of a run of duplicates the first in that total order is kept.
//  4. Resolve each Cause to the 1-based index of the first kept diagnostic
//     with that code and span; a missing cause makes the diagnostic its own root.
//  5. Keep the first max diagnostics of the lowest phase (max <= 0 keeps
//     all), plus every internal error and every cause of a later phase, which
//     may sort after its consequences; a kept diagnostic whose root was
//     dropped becomes its own root.
//
// It does not modify the diagnostics. sm resolves paths and positions.
func Order(ds []*Diagnostic, sm *source.SourceMap, max int) Report {
	var r Report
	lowest := numPhases
	for _, d := range ds {
		if d.Phase != PhaseInternal && d.Phase < lowest {
			lowest = d.Phase
		}
	}
	kept := make([]*Diagnostic, 0, len(ds))
	hasInternal := false
	for _, d := range ds {
		if d.Phase == PhaseInternal {
			hasInternal = true
			kept = append(kept, d)
		} else if d.Phase == lowest {
			kept = append(kept, d)
		}
	}
	kept = keepCauses(ds, kept)
	switch {
	case lowest != numPhases:
		r.Phase, r.HasPhase = lowest, true
	case hasInternal:
		r.Phase, r.HasPhase = PhaseInternal, true
	}
	keys := make(map[*Diagnostic]sortKey, len(kept))
	for _, d := range kept {
		keys[d] = makeSortKey(d, sm)
	}
	slices.SortStableFunc(kept, func(a, b *Diagnostic) int {
		return compareKeys(keys[a], keys[b])
	})
	// Step 3: deduplicate.
	dedup := kept[:0]
	for i, d := range kept {
		if i > 0 {
			p := dedup[len(dedup)-1]
			if p.Code == d.Code && p.At == d.At && p.Message == d.Message {
				continue
			}
		}
		dedup = append(dedup, d)
	}
	// Step 5 before step 4's numbering: choose the survivors, then number them.
	var final []*Diagnostic
	capped := 0
	for _, d := range dedup {
		if d.Phase == lowest {
			if max > 0 && capped >= max {
				r.Omitted++
				continue
			}
			capped++
		}
		final = append(final, d)
	}
	// Step 4: resolve roots against the final list.
	first := make(map[Cause]int, len(final))
	for i, d := range final {
		k := Cause{Code: d.Code, At: d.At}
		if _, ok := first[k]; !ok {
			first[k] = i + 1
		}
	}
	r.Diags = make([]Emitted, len(final))
	for i, d := range final {
		root := i + 1
		if d.Cause != nil {
			if j, ok := first[*d.Cause]; ok {
				root = j
			}
		}
		r.Diags[i] = Emitted{Root: root, Diag: d}
	}
	return r
}

// keepCauses appends to kept, once each, the diagnostics of ds that a kept
// diagnostic names as its Cause but the phase rule left out, and theirs in
// turn: a consequence is never reported without its root (a failed gen line's
// E0903, in types, behind the unknown names its discarded output leaves).
func keepCauses(ds, kept []*Diagnostic) []*Diagnostic {
	// Built on the first Cause: most reports have none.
	var by map[Cause][]*Diagnostic
	var in map[*Diagnostic]bool
	for i := 0; i < len(kept); i++ {
		c := kept[i].Cause
		if c == nil {
			continue
		}
		if by == nil {
			by = make(map[Cause][]*Diagnostic, len(ds))
			for _, d := range ds {
				k := Cause{Code: d.Code, At: d.At}
				by[k] = append(by[k], d)
			}
			in = make(map[*Diagnostic]bool, len(kept))
			for _, d := range kept {
				in[d] = true
			}
		}
		for _, d := range by[*c] {
			if !in[d] {
				in[d] = true
				kept = append(kept, d)
			}
		}
	}
	return kept
}

// sortKey is the precomputed order of one diagnostic.
type sortKey struct {
	path            string
	line, col       uint32
	endLine, endCol uint32
	code            Code
	message         string
	// rest is everything else the diagnostic carries, rendered: the JSON
	// object with root 0, then the cause. It breaks the last ties.
	rest string
	// raw is the span itself, the final tie when sm is nil.
	raw source.Span
}

func makeSortKey(d *Diagnostic, sm *source.SourceMap) sortKey {
	k := sortKey{code: d.Code, message: d.Message, raw: d.At}
	if d.At.File != source.NoFile && sm != nil {
		s, e := sm.SpanPositions(d.At)
		k.path, k.line, k.col, k.endLine, k.endCol = s.Path, s.Line, s.Col, e.Line, e.Col
	}
	var w JSONWriter
	w.BeginArray()
	WriteDiagnostic(&w, Emitted{Diag: d}, sm)
	if d.Cause != nil {
		w.String(d.Cause.Code.String())
		writeRange(&w, d.Cause.At, sm, false, "")
		w.Uint(uint64(d.Cause.At.File))
		w.Uint(uint64(d.Cause.At.Start))
		w.Uint(uint64(d.Cause.At.End))
	}
	w.EndArray()
	k.rest = string(w.Finish())
	return k
}

func compareKeys(a, b sortKey) int {
	return cmp.Or(
		strings.Compare(a.path, b.path),
		cmp.Compare(a.line, b.line),
		cmp.Compare(a.col, b.col),
		cmp.Compare(a.code, b.code),
		strings.Compare(a.message, b.message),
		cmp.Compare(a.endLine, b.endLine),
		cmp.Compare(a.endCol, b.endCol),
		strings.Compare(a.rest, b.rest),
		cmp.Compare(a.raw.File, b.raw.File),
		cmp.Compare(a.raw.Start, b.raw.Start),
		cmp.Compare(a.raw.End, b.raw.End),
	)
}

// WriteJSON renders the report as one JSON document:
// {"phase": <name or null>, "diagnostics": [...], "omitted": n}.
func (r Report) WriteJSON(sm *source.SourceMap) []byte {
	var w JSONWriter
	w.BeginObject()
	w.Key("phase")
	if r.HasPhase {
		w.String(r.Phase.String())
	} else {
		w.Null()
	}
	w.Key("diagnostics")
	w.BeginArray()
	for _, e := range r.Diags {
		WriteDiagnostic(&w, e, sm)
	}
	w.EndArray()
	w.Key("omitted")
	w.Int(int64(r.Omitted))
	w.EndObject()
	return append(w.Finish(), '\n')
}

// WriteDiagnostic writes one diagnostic object with exactly these keys, in
// this order: code, name, phase, root, at, message, facts, fixes,
// alternatives, explain. Every key is always present.
func WriteDiagnostic(w *JSONWriter, e Emitted, sm *source.SourceMap) {
	d := e.Diag
	w.BeginObject()
	w.Key("code")
	w.String(d.Code.String())
	w.Key("name")
	w.String(d.Code.Name())
	w.Key("phase")
	w.String(d.Phase.String())
	w.Key("root")
	w.Int(int64(e.Root))
	w.Key("at")
	writeRange(w, d.At, sm, false, "")
	w.Key("message")
	w.String(d.Message)
	w.Key("facts")
	writeFacts(w, d, sm)
	w.Key("fixes")
	w.BeginArray()
	for _, f := range d.Fixes {
		w.BeginObject()
		w.Key("class")
		w.String(f.Class.String())
		w.Key("edits")
		w.BeginArray()
		for _, ed := range f.Edits {
			writeRange(w, ed.Span, sm, true, ed.Text)
		}
		w.EndArray()
		w.Key("why")
		w.String(f.Why)
		w.EndObject()
	}
	w.EndArray()
	w.Key("alternatives")
	w.BeginArray()
	for _, a := range d.Alternatives {
		w.String(a)
	}
	w.EndArray()
	w.Key("explain")
	w.String("mk explain " + d.Code.String())
	w.EndObject()
}

// writeRange writes {"file","line","col","end_line","end_col"} and, for an
// edit, a trailing "text" (an insert has an empty range). The zero span writes
// file "" and zeros.
func writeRange(w *JSONWriter, s source.Span, sm *source.SourceMap, edit bool, text string) {
	var start, end source.Position
	if s.File != source.NoFile && sm != nil {
		start, end = sm.SpanPositions(s)
	}
	w.BeginObject()
	w.Key("file")
	w.String(start.Path)
	w.Key("line")
	w.Uint(uint64(start.Line))
	w.Key("col")
	w.Uint(uint64(start.Col))
	w.Key("end_line")
	w.Uint(uint64(end.Line))
	w.Key("end_col")
	w.Uint(uint64(end.Col))
	if edit {
		w.Key("text")
		w.String(text)
	}
	w.EndObject()
}

func writeFacts(w *JSONWriter, d *Diagnostic, sm *source.SourceMap) {
	w.BeginObject()
	for _, f := range d.Facts {
		w.Key(f.Key)
		f.Value.writeJSON(w, sm)
	}
	w.EndObject()
}

// Text renders the report for a terminal: per diagnostic a header
// "path:line:col: E0501 exclusive-conflict: message", the source line with a
// caret under the span, one line per fact, fix and alternative, and the
// explain line; then "N errors" with the omitted count when nonzero.
func (r Report) Text(sm *source.SourceMap) string {
	var b strings.Builder
	for _, e := range r.Diags {
		d := e.Diag
		b.WriteString(Location(sm, d.At))
		b.WriteString(": ")
		b.WriteString(d.Code.String())
		b.WriteByte(' ')
		b.WriteString(d.Code.Name())
		b.WriteString(": ")
		b.WriteString(d.Message)
		b.WriteByte('\n')
		writeSourceLine(&b, d.At, sm)
		for _, f := range d.Facts {
			b.WriteString("    ")
			b.WriteString(f.Key)
			b.WriteString(": ")
			b.WriteString(f.Value.text(sm))
			b.WriteByte('\n')
		}
		for _, f := range d.Fixes {
			b.WriteString("    fix (" + f.Class.String() + "): " + f.Why + "\n")
		}
		for _, a := range d.Alternatives {
			b.WriteString("    try: " + a + "\n")
		}
		b.WriteString("    explain: mk explain " + d.Code.String() + "\n")
	}
	n := len(r.Diags)
	b.WriteString(strconv.Itoa(n))
	if n == 1 {
		b.WriteString(" error")
	} else {
		b.WriteString(" errors")
	}
	if r.Omitted > 0 {
		b.WriteString(" (" + strconv.Itoa(r.Omitted) + " omitted)")
	}
	b.WriteByte('\n')
	return b.String()
}

// writeSourceLine writes the span's first line and a caret run under it.
func writeSourceLine(b *strings.Builder, at source.Span, sm *source.SourceMap) {
	if at.File == source.NoFile || sm == nil {
		return
	}
	f := sm.File(at.File)
	lc := f.LineCol(at.Start)
	line := f.Line(int(lc.Line))
	b.WriteString("    | ")
	b.WriteString(line)
	b.WriteString("\n    | ")
	for i := uint32(1); i < lc.Col; i++ {
		if line[i-1] == '\t' {
			b.WriteByte('\t')
		} else {
			b.WriteByte(' ')
		}
	}
	width := at.Len()
	if rest := uint32(len(line)) - (lc.Col - 1); width > rest {
		width = rest
	}
	if width == 0 {
		width = 1
	}
	b.WriteString(strings.Repeat("^", int(width)))
	b.WriteByte('\n')
}
