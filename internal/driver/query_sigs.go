// Why: a tool that reasons about calls (a lint that needs every function
// taking `mut Heap`) needs every signature of a package, and `sig Name`
// answers one name per process start. Functions a gen line writes are no
// declaration the tool can read from the source.
//
// What: `mk query sig --member <name> [--json]`: Session.Signatures,
// SigList (JSON, Text) and SigEntry.
//
// How: one Check of the member, then every function and method of its
// modules from the checked items, never capped: generated ones included,
// impl methods named Type.Method. A function whose check failed is named in
// Missing. Each entry's sig is the line `sig Name` answers first.

package driver

import (
	"bytes"
	"cmp"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"

	"mk/internal/source"
	"mk/internal/types"
)

// SigListVersion is the version of the --json document of
// `mk query sig --member`. A field added keeps the version; a field
// removed or changed in meaning raises it.
const SigListVersion = 1

// SigList is the answer of `mk query sig --member`: complete, never capped
// and never paged.
type SigList struct {
	Version int    `json:"version"`
	Query   string `json:"query"`  // "sig"
	Member  string `json:"member"` // the member's package name
	// Complete is false when a function has no checked signature; Missing
	// names those functions.
	Complete  bool       `json:"complete"`
	Missing   []string   `json:"missing"`
	Functions []SigEntry `json:"functions"`
}

// SigEntry is one function or method.
type SigEntry struct {
	// Name is Name, Type.Name for a method, and Type.Name for an impl
	// method too, whose interface Impl names.
	Name   string `json:"name"`
	Module string `json:"module"`
	// File, Line and Col locate the name, as diagnostics print it. A
	// generated function's file is its module's gen view.
	File string `json:"file"`
	Line int    `json:"line"`
	Col  int    `json:"col"`
	// Sig is the signature as `mk query sig Name` spells its first line.
	Sig string `json:"sig"`
	// Contracts are the requires and ensures clauses, one per entry.
	Contracts []string `json:"contracts"`
	Exported  bool     `json:"exported"`
	// Impl is the interface an impl method implements, "" otherwise.
	Impl string `json:"impl"`
	// Generated is true for a function a gen line wrote; Gen is that
	// line, file:line:col, "" otherwise.
	Generated bool   `json:"generated"`
	Gen       string `json:"gen"`
}

// JSON is the listing as one object and a newline.
func (l *SigList) JSON() []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(l)
	return b.Bytes()
}

// Text is the listing as lines: a header, then one signature per line with
// its position.
func (l *SigList) Text() string {
	var b strings.Builder
	b.WriteString("member " + l.Member + ": " + itoaQ(len(l.Functions)) + " function(s)\n")
	if !l.Complete {
		b.WriteString("incomplete: no signature for " + strings.Join(l.Missing, ", ") + "\n")
	}
	for _, e := range l.Functions {
		b.WriteString(e.Sig + "  " + e.File + ":" + itoaQ(e.Line) + ":" + itoaQ(e.Col))
		if e.Generated {
			b.WriteString(" (gen " + e.Gen + ")")
		}
		b.WriteString("\n")
		for _, c := range e.Contracts {
			b.WriteString("    " + c + "\n")
		}
	}
	return b.String()
}

// Signatures is the listing over the session's package, whose name is
// member. It is nil, with the Result saying why, when the package does not
// check as far as types.
func (s *Session) Signatures(member string) (*SigList, Result) {
	if strings.HasPrefix(member, "std.") {
		s.fullStd = true
	}
	res := s.Check()
	if s.w == nil || s.w.sem5 == nil || !checkedToTypes(res) {
		return nil, res
	}
	qx := &queryIndex{s: s, w: s.w}
	l := &SigList{Version: SigListVersion, Query: "sig", Member: member, Complete: true,
		Missing: []string{}, Functions: []SigEntry{}}
	mods := qx.s.nonStdMods()
	if qx.s.pkgB != nil {
		mods = qx.s.pkgB.mods
	}
	for _, m := range mods {
		if m.mod == nil {
			continue
		}
		for _, id := range m.mod.Items {
			it := qx.w.items.Get(id)
			if it.Kind != types.KindFunc {
				continue // tests and fn literals are no callee by name
			}
			name, impl, ok := qx.sigName(it)
			if !ok {
				continue // a contract predicate or an invariant
			}
			sig := qx.funcSig(id)
			if sig == nil {
				l.Complete = false
				l.Missing = append(l.Missing, name)
				continue
			}
			e := SigEntry{Name: name, Module: m.path, Sig: qx.sigLine(name, sig), Contracts: []string{},
				Exported: it.Exported, Impl: impl}
			e.File, e.Line, e.Col = qx.spanAt(it.Span)
			for _, c := range sig.Contracts {
				w := "requires "
				if c.Kind == types.Ensures {
					w = "ensures "
				}
				e.Contracts = append(e.Contracts, w+c.Text)
			}
			// An impl method carries no origin of its own: its impl does.
			o := it.Origin
			if o == nil && impl != "" {
				o = qx.w.items.Get(it.Owner).Origin
			}
			if o != nil {
				f, ln, col := qx.spanAt(o.Gen)
				e.Generated, e.Gen = true, f+":"+itoaQ(ln)+":"+itoaQ(col)
			}
			l.Functions = append(l.Functions, e)
		}
	}
	if len(s.depRoot) > 0 {
		// A dependency's names as the root spells them, as sig Name's.
		names := s.displayNames(nil)
		for i := range l.Functions {
			e := &l.Functions[i]
			e.Sig = localizeText(e.Sig, names)
			for j, c := range e.Contracts {
				e.Contracts[j] = localizeText(c, names)
			}
		}
	}
	slices.SortFunc(l.Functions, func(a, b SigEntry) int {
		return cmp.Or(cmp.Compare(a.File, b.File), cmp.Compare(a.Line, b.Line), cmp.Compare(a.Col, b.Col),
			cmp.Compare(a.Name, b.Name))
	})
	slices.Sort(l.Missing)
	return l, res
}

// sigName is the listing's name for function item it, and for an impl
// method its interface; ok is false for an item no call names (a contract
// predicate, an invariant).
func (qx *queryIndex) sigName(it *types.Item) (name, impl string, ok bool) {
	if it.Owner != types.NoItem {
		o := qx.w.items.Get(it.Owner)
		if o.Kind == types.KindImpl {
			im, checked := o.Decl.(*types.ImplDecl)
			if !checked || im.For == types.NoType {
				return it.Key.String(), "", true // unchecked: listed as missing
			}
			// Named as the type's own methods are, Box.Area for an impl
			// for Box[T]; an impl for an error is keyed by its set of one.
			owner := qx.w.tab.TypeString(im.For, im.Generics)
			switch ty := qx.w.tab.Get(im.For); {
			case ty.Kind == types.KNamed:
				owner = qx.str(qx.w.items.Get(ty.Item).Name)
			case ty.Kind == types.KErrorSet && singleError(qx.w.tab, ty.Errors) != types.NoItem:
				owner = qx.str(qx.w.items.Get(singleError(qx.w.tab, ty.Errors)).Name)
			}
			return owner + "." + qx.str(it.Name), qx.boundString(im.Bound, im.Generics), true
		}
	}
	if it.Name == source.NoSymbol {
		return "", "", false // an invariant
	}
	name = qx.displayName(it)
	return name, "", name != ""
}

// spanAt is sp as file, line and column, the file as diagnostics print it.
func (qx *queryIndex) spanAt(sp source.Span) (string, int, int) {
	f := qx.w.sm.File(sp.File)
	if f == nil {
		return "?", 0, 0
	}
	lc := f.LineCol(sp.Start)
	return filepath.ToSlash(qx.w.sitePath(sp.File)), int(lc.Line), int(lc.Col)
}
