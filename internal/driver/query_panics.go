// Why: `panics F` walks F's direct calls and so misses functions a program
// reaches through a const table of fn values or a struct of fn hooks. An
// audit needs every panic site of a package, reached or not.
//
// What: `mk query panics --member <name> [--json]`: Session.PanicSites,
// PanicSiteList (JSON, Text), PanicSite, PanicSiteKinds, and MemberDir,
// which resolves a member name to its directory.
//
// How: reads every function of the member's modules from the lowered MIR the
// session holds after its Check, never capped. A generic function's sites are
// listed once, from its generic body. A site is a TermPanic of the body
// (Panic, Assert, Must, a contract clause, a bounds check, an enum tag check:
// lower makes each its own block, at its own span), or a statement whose code
// generation adds a trap (codegen's rvalue.go and cast.go: + - * and negation
// on integers, / and %, << and >>, Cast[T], a str slice), at its span.

package driver

import (
	"bytes"
	"cmp"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"

	"mk/internal/mir"
	"mk/internal/types"
)

// PanicSitesVersion is the version of the --json document of
// `mk query panics --member`. A field added keeps the version; a field
// removed or changed in meaning raises it.
const PanicSitesVersion = 1

// PanicSiteList is the answer of `mk query panics --member`: complete,
// never capped and never paged.
type PanicSiteList struct {
	Version int    `json:"version"`
	Query   string `json:"query"`  // "panics"
	Member  string `json:"member"` // the member's package name
	// Functions counts the member's functions with a body that were read:
	// functions, methods, fn literals, contract predicates and tests.
	Functions int `json:"functions"`
	// Complete is false when some function that should have a body has
	// none (its check failed); Missing names them.
	Complete bool     `json:"complete"`
	Missing  []string `json:"missing"`
	// Sites are sorted by file, line, column, kind and function.
	Sites []PanicSite `json:"sites"`
}

// PanicSite is one site.
type PanicSite struct {
	// File is the path as diagnostics print it: relative to the member's
	// directory, with forward slashes.
	File string `json:"file"`
	Line int    `json:"line"`
	Col  int    `json:"col"`
	// Kind is one of panicKinds' names; Code is the runtime's frozen panic
	// code for it (types.PanicCode: the number a panic report prints).
	Kind string `json:"kind"`
	Code int    `json:"code"`
	// Explicit is true when the source writes the panic (panic, assert,
	// unwrap, contract), false for a check the compiler adds.
	Explicit bool `json:"explicit"`
	// Function is the function holding the site, as mk query names it
	// (Name, Type.Name, "F (fn literal)", a test's key).
	Function string `json:"function"`
	Module   string `json:"module"`
	// Generic is true when the function is generic: the site is listed
	// once, from the generic body, and stands for the same site in every
	// instance. (No operator that traps applies to a type parameter's
	// values, E0703, so no site exists in some instances only.)
	Generic bool `json:"generic"`
	// Test is true for a site inside a test block.
	Test bool `json:"test"`
}

// panicKinds names each panic code a site can have, and whether the
// source writes it.
var panicKinds = map[types.PanicCode]struct {
	name     string
	explicit bool
}{
	types.PanicExplicit:     {"panic", true},
	types.PanicAssert:       {"assert", true},
	types.PanicMust:         {"unwrap", true},
	types.PanicContract:     {"contract", true},
	types.PanicBounds:       {"bounds", false},
	types.PanicOverflow:     {"overflow", false},
	types.PanicDivideByZero: {"divide", false},
	types.PanicShiftAmount:  {"shift", false},
	types.PanicCast:         {"cast", false},
	types.PanicStrBoundary:  {"str-boundary", false},
	types.PanicEnumTag:      {"enum-tag", false},
	types.PanicUnreachable:  {"unreachable", false},
}

// PanicSiteKinds are the kind names in code order, for the docs' test.
func PanicSiteKinds() []string {
	var cs []types.PanicCode
	for c := range panicKinds {
		cs = append(cs, c)
	}
	slices.Sort(cs)
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = panicKinds[c].name
	}
	return out
}

// JSON is the listing as one object and a newline.
func (l *PanicSiteList) JSON() []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(l)
	return b.Bytes()
}

// Text is the listing as lines: a header, then one site per line.
func (l *PanicSiteList) Text() string {
	var b strings.Builder
	nx := 0
	for _, s := range l.Sites {
		if s.Explicit {
			nx++
		}
	}
	b.WriteString("member " + l.Member + ": " + itoaQ(l.Functions) + " function(s) read; " +
		itoaQ(len(l.Sites)) + " panic site(s), " + itoaQ(nx) + " explicit\n")
	if !l.Complete {
		b.WriteString("incomplete: no body for " + strings.Join(l.Missing, ", ") + "\n")
	}
	for _, s := range l.Sites {
		b.WriteString(s.Kind + "  " + s.File + ":" + itoaQ(s.Line) + ":" + itoaQ(s.Col) + "  in " + s.Function)
		if s.Generic {
			b.WriteString(" (generic: every instance)")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// PanicSites is the listing over the session's package, whose name is
// member. It is nil, with the Result saying why, when the package does not
// check as far as types.
func (s *Session) PanicSites(member string) (*PanicSiteList, Result) {
	if strings.HasPrefix(member, "std.") {
		s.fullStd = true
	}
	res := s.Check()
	if s.w == nil || s.w.sem5 == nil || !checkedToTypes(res) {
		return nil, res
	}
	qx := &queryIndex{s: s, w: s.w}
	l := &PanicSiteList{Version: PanicSitesVersion, Query: "panics", Member: member, Complete: true,
		Missing: []string{}, Sites: []PanicSite{}}
	for _, id := range qx.pkgBodies() {
		it := qx.w.items.Get(id)
		if fd, ok := it.Decl.(*types.FuncDecl); ok && fd.Bodiless {
			continue
		}
		b := qx.bodyOf(id)
		if b == nil {
			l.Complete = false
			l.Missing = append(l.Missing, qx.name(id))
			continue
		}
		l.Functions++
		l.Sites = append(l.Sites, qx.sitesOf(id, b)...)
	}
	slices.SortFunc(l.Sites, func(a, b PanicSite) int {
		return cmp.Or(cmp.Compare(a.File, b.File), cmp.Compare(a.Line, b.Line), cmp.Compare(a.Col, b.Col),
			cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Function, b.Function))
	})
	slices.Sort(l.Missing)
	return l, res
}

// sitesOf is body b's sites, b being item id's.
func (qx *queryIndex) sitesOf(id types.ItemID, b *mir.Body) []PanicSite {
	it := qx.w.items.Get(id)
	base := PanicSite{Function: qx.name(id), Module: qx.w.items.Module(it.Module).Path,
		Generic: qx.isGeneric(id), Test: qx.inTest(id)}
	tab := qx.w.tab
	var out []PanicSite
	add := func(code types.PanicCode, sp mir.SpanID) {
		s := base
		k, ok := panicKinds[code]
		if !ok {
			k.name = code.String()
		}
		s.Kind, s.Code, s.Explicit = k.name, int(code), k.explicit
		s.File, s.Line, s.Col = qx.siteAt(b, sp)
		out = append(out, s)
	}
	isInt := func(o mir.Operand) bool {
		t := b.OperandType(o)
		return t != types.NoType && tab.Get(t).IsInteger()
	}
	for _, bl := range b.Blocks {
		for _, st := range bl.Stmts {
			if st.Kind != mir.StAssign {
				continue
			}
			rv := &st.Rvalue
			switch rv.Kind {
			case mir.RvBinary:
				if rv.Bin == mir.Shl || rv.Bin == mir.Shr {
					// Integer lanes of a std.simd vector shift by a scalar
					// and trap as a scalar shift does.
					if t := b.OperandType(rv.A); t != types.NoType && tab.Get(t).Kind != types.KFloat {
						add(types.PanicShiftAmount, st.Span)
					}
					continue
				}
				if !isInt(rv.A) {
					continue
				}
				switch rv.Bin {
				case mir.Add, mir.Sub, mir.Mul:
					add(types.PanicOverflow, st.Span)
				case mir.Div, mir.Rem:
					if !constSafeDivisor(tab, b, rv.B) {
						add(types.PanicDivideByZero, st.Span)
					}
				}
			case mir.RvUnary:
				if rv.Un == mir.Neg && isInt(rv.A) {
					add(types.PanicOverflow, st.Span)
				}
			case mir.RvCast:
				if rv.Cast == mir.CastChecked {
					add(types.PanicCast, st.Span)
				}
			case mir.RvSlice:
				// The bounds are a TermPanic of their own; a str slice also
				// traps off a scalar boundary (codegen's cast.go).
				if t := b.PlaceType(rv.Place); t != types.NoType && tab.Get(t).Kind == types.KStr {
					add(types.PanicStrBoundary, st.Span)
				}
			}
		}
		switch t := &bl.Term; t.Kind {
		case mir.TermPanic:
			add(t.Panic, t.Span)
		case mir.TermCall, mir.TermTailCall:
			// A std intrinsic codegen expands in place traps at the call.
			if t.Callee.Kind == mir.CallDirect {
				for _, code := range qx.intrinsicTraps(t.Callee.Fn.Item) {
					add(code, t.Span)
				}
			}
			// EvalArena() reached at run time traps as Panic does
			// (codegen's call.go); a compile-time requires is lowered as
			// a dev entry check too, so it is reachable there.
			if t.Callee.Kind == mir.CallBuiltin && t.Callee.Builtin == mir.BuiltinEvalArena {
				add(types.PanicExplicit, t.Span)
			}
		}
	}
	return out
}

// intrinsicTraps are the panics codegen emits inline for a call of the std
// intrinsic fn (intrinsic_extra.go, intrinsic_simd.go), none for any other
// callee. A trap inside a std function with a body is a site of std, not
// of the caller, and is not listed. std.simd's unjudged-clause panic, which
// only a generic instance can reach, is not listed.
func (qx *queryIndex) intrinsicTraps(fn types.ItemID) []types.PanicCode {
	it := qx.w.items.Get(fn)
	if fd, ok := it.Decl.(*types.FuncDecl); !ok || !fd.Bodiless {
		return nil
	}
	name := qx.w.in.Str(it.Name)
	if it.Owner != types.NoItem {
		name = qx.w.in.Str(qx.w.items.Get(it.Owner).Name) + ":" + name
	}
	return intrinsicTrapTable[qx.w.items.Module(it.Module).Path+" "+name]
}

// intrinsicTrapTable is intrinsicTraps' table, by module path and
// [Owner:]Name.
var intrinsicTrapTable = map[string][]types.PanicCode{
	"std.math DivWide":          {types.PanicDivideByZero, types.PanicOverflow},
	"std.simd Vec:FromSlice":    {types.PanicBounds},
	"std.simd Vec:FromSliceOr":  {types.PanicBounds},
	"std.simd Vec:CopyTo":       {types.PanicBounds},
	"std.simd Vec:CopyToMasked": {types.PanicBounds},
	"std.simd Vec:Gather":       {types.PanicBounds},
	"std.simd Vec:DivTrunc":     {types.PanicDivideByZero},
	"std.simd Vec:Shl":          {types.PanicShiftAmount},
	"std.simd Vec:Shr":          {types.PanicShiftAmount},
	"std.simd Vec:ShlEach":      {types.PanicShiftAmount},
	"std.simd Vec:ShrEach":      {types.PanicShiftAmount},
	"std.simd Vec:Cast":         {types.PanicCast},
}

// constSafeDivisor reports a constant divisor other than 0 and, for a
// signed type, -1: codegen's divChecks emits no trap for it.
func constSafeDivisor(tab *types.Table, b *mir.Body, o mir.Operand) bool {
	if o.Kind != mir.OpImm {
		return false
	}
	t := b.OperandType(o)
	if t == types.NoType {
		return false
	}
	ty := tab.Get(t)
	bits, all := o.Bits, uint64(1<<64-1)
	if w := ty.Bits; w > 0 && w < 64 {
		all = 1<<uint(w) - 1
		bits &= all
	}
	return bits != 0 && (!ty.Signed || bits != all)
}

// siteAt is span sp of b as file, line and column.
func (qx *queryIndex) siteAt(b *mir.Body, sp mir.SpanID) (string, int, int) {
	if int(sp) >= len(b.Spans) {
		return "?", 0, 0
	}
	s := b.Spans[sp]
	f := qx.w.sm.File(s.File)
	if f == nil {
		return "?", 0, 0
	}
	lc := f.LineCol(s.Start)
	return filepath.ToSlash(qx.w.sitePath(s.File)), int(lc.Line), int(lc.Col)
}

// isGeneric reports whether id, or the function a literal or predicate
// belongs to, has generic parameters.
func (qx *queryIndex) isGeneric(id types.ItemID) bool {
	for ; id != types.NoItem; id = qx.w.items.Get(id).Owner {
		if s := qx.funcSig(id); s != nil && len(s.Generics) > 0 {
			return true
		}
	}
	return false
}

// inTest reports whether id is a test block or lies inside one.
func (qx *queryIndex) inTest(id types.ItemID) bool {
	for ; id != types.NoItem; id = qx.w.items.Get(id).Owner {
		if qx.w.items.Get(id).Kind == types.KindTest {
			return true
		}
	}
	return false
}

// MemberDir is the directory of the workspace member named name, by its
// manifest's name or by its entry in the workspace root's members list,
// and its package name. The workspace is the one cfg.Root belongs to
// (the root itself when its manifest lists members); a package in no
// workspace answers for its own name. An unknown name is a *UsageError
// that lists the members.
func MemberDir(cfg Config, name string) (string, string, error) {
	cfg.CacheDir = "" // only the manifest step runs
	s, err := NewSession(cfg)
	if err != nil {
		return "", "", err
	}
	w := newWorld(s.cfg)
	s.w = w
	ms := w.newManifestStep(w.stdTree())
	if _, ds := ms.load(w.stdTree(), stdLayout.Manifest, ""); len(ds) > 0 {
		return "", "", &diagError{ds: ds}
	}
	man, ds := ms.load(w.diskTree(s.root.dir, s.root.prefix), "pkg.mk", "main")
	if len(ds) > 0 {
		return "", "", &diagError{ds: ds}
	}
	if man == nil {
		return "", "", usagef("--member %s: %s has no pkg.mk", name, cfg.Root)
	}
	dw := &depWalk{s: s, ms: ms, rootDir: s.root.dir, wsRoot: s.root.dir, loaded: map[string]*Manifest{}}
	ws := dw.findWorkspace(man)
	if len(dw.diags) > 0 {
		return "", "", &diagError{ds: dw.diags}
	}
	if ws == nil {
		if man.Name == name {
			return s.root.dir, man.Name, nil
		}
		return "", "", usagef("--member %s: the package here is %s and belongs to no workspace", name, man.Name)
	}
	var names []string
	want := strings.Trim(filepath.ToSlash(filepath.Clean(name)), "/")
	for i, md := range ws.members {
		m := ws.mans[i]
		if m == nil {
			m = man
		}
		entry := strings.Trim(filepath.ToSlash(filepath.Clean(ws.man.Members[i])), "/")
		if m.Name == name || entry == want {
			return md, m.Name, nil
		}
		names = append(names, m.Name)
	}
	return "", "", usagef("--member %s: no such member; the workspace's members are %s", name, strings.Join(names, ", "))
}
