// Why: a function's loans cross calls only through its signature: which
// parameters a returned reference may come from, which values a call
// stores into another, and which loans it ends. The borrow checker and the
// MIR validator read these facts, so they are settled here, per signature.
//
// What: signatureFlows (the types.Flows of a signature), automaticSources
// (the sources of a result with no `from` clause, E0601), fromSource
// (`from` clauses, E0607), storeClauseCheck (store clauses: E0203, E0507,
// E0301), flowsCovered and fnFlowsCovered (E0608 for fn values), and the
// type predicates they share (preds: contents, ownBytes, refers, hasMut).
//
// How: everything is decided on the signature as written, never from a
// body. A result's automatic suppliers are the parameters whose contents can
// hold a reference of the result's type; past recur's bound on a
// polymorphically recursive type each walk answers on its conservative side.

package check

import (
	"strconv"
	"strings"

	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/source"
	"mk/internal/types"
)

// flowClauses are a signature's written flow clauses, as stage 5 reads them
// from the AST; resolve already reported a clause naming no parameter
// (E0201), and such a clause arrives here absent.
type flowClauses struct {
	hasFrom bool
	from    []fromClause
	// fields is a per-field clause `from (f: a, g: b)`, in written
	// order; from is then empty.
	fields []fieldSrcClause
	stores []storeClause
	ends   []types.ParamIndex
}

// fieldSrcClause is one `f: a, b.c` of a per-field clause: the field's
// name and its sources.
type fieldSrcClause struct {
	name source.Symbol
	at   source.Span
	from []fromClause
}

// fromClause is one source of `from p.f.g`.
type fromClause struct {
	param types.ParamIndex
	steps []stepRef
	at    source.Span
}

// stepRef is one field name of a `from` path.
type stepRef struct {
	name source.Symbol
	at   source.Span
}

// storeClause is `*from into into`.
type storeClause struct {
	from, into types.ParamIndex
	at, fromAt source.Span
}

// flowFn is the function whose flows are computed: its module (for visible
// fields), its generic environment and where E0601 is anchored.
type flowFn struct {
	sig *types.Signature
	mod types.ModuleID
	env *Env
	at  source.Span
	// fix is where E0601's `from` fix goes (fromfix.go); nil for a fn type,
	// a literal, or a signature with other clauses written.
	fix *fromFix
}

// tset is a set of types, or "everything" (ANY).
type tset struct {
	any bool
	m   map[types.TypeID]bool
}

func (s *tset) add(t types.TypeID) {
	if s.m == nil {
		s.m = map[types.TypeID]bool{}
	}
	s.m[t] = true
}

// meets reports whether some type of a is in b, or either is ANY.
func (a tset) meets(b tset) bool {
	if a.any || b.any {
		return true
	}
	for t := range a.m {
		if b.m[t] {
			return true
		}
	}
	return false
}

// fieldRef names one struct field.
type fieldRef struct {
	item  types.ItemID
	index int
}

// preds are the helper predicates over types as written, with the
// fields the function's callers can see.
type preds struct {
	c   *Checker
	env *Env
	mod types.ModuleID
	// exportedOnly counts only exported fields (an exported function);
	// extra is one private field counted anyway (the private_field search).
	exportedOnly bool
	extra        fieldRef
}

// fields returns the visible fields of a non-view struct or an enum
// instance, with their types under the written arguments.
func (p *preds) fields(t types.TypeID) ([]types.TypeID, bool) {
	c := p.c
	ty := c.cfg.Types.Get(t)
	if ty.Kind == types.KRecord {
		out := make([]types.TypeID, len(ty.Fields))
		for i, f := range ty.Fields {
			out[i] = f.Type
		}
		return out, true
	}
	if ty.Kind != types.KNamed {
		return nil, false
	}
	it := c.cfg.Items.Get(ty.Item)
	var out []types.TypeID
	switch d := c.declOfItem(it).(type) {
	case *types.StructDecl:
		for i, f := range d.Fields {
			if p.visible(it, i, f) {
				out = append(out, c.subst(f.Type, ty.Args, types.NoType))
			}
		}
	case *types.EnumDecl:
		for _, v := range d.Variants {
			for _, f := range v.Fields {
				out = append(out, c.subst(f.Type, ty.Args, types.NoType))
			}
		}
	default:
		return nil, false
	}
	return out, true
}

func (p *preds) visible(it *types.Item, i int, f types.FieldDecl) bool {
	if f.Exported || (p.extra.item == it.ID && p.extra.index == i) {
		return true
	}
	return !p.exportedOnly && it.Module == p.mod
}

// isView reports whether t is a view struct.
func (p *preds) isView(t types.TypeID) bool {
	ty := p.c.cfg.Types.Get(t)
	if ty.Kind != types.KNamed {
		return false
	}
	d, ok := p.c.declOfItem(p.c.cfg.Items.Get(ty.Item)).(*types.StructDecl)
	return ok && d.View
}

func (p *preds) carrier(t types.TypeID) bool { return p.c.isCarrier(t) }

// ownRef: t is a reference, slice or str; or ?U, [N]U, E!U of one; or a
// record or non-view struct with such a field; or any view.
func (p *preds) ownRef(t types.TypeID) bool { return p.ownRefSeen(t, map[types.TypeID]bool{}) }

func (p *preds) ownRefSeen(t types.TypeID, seen map[types.TypeID]bool) bool {
	ty := p.c.cfg.Types.Get(t)
	switch ty.Kind {
	case types.KRef, types.KMut, types.KSlice, types.KMutSlice, types.KStr:
		return true
	case types.KOptional, types.KArray, types.KErrorUnion:
		return p.ownRefSeen(ty.Elem, seen)
	}
	if p.isView(t) {
		return true
	}
	if seen[t] {
		return false
	}
	seen[t] = true
	fs, _ := p.fields(t)
	for _, f := range fs {
		if p.ownRefSeen(f, seen) {
			return true
		}
	}
	return false
}

// refers: what t's references point at; a view refers to ANY.
func (p *preds) refers(t types.TypeID) tset {
	var s tset
	p.refersInto(t, &s, map[types.TypeID]bool{})
	return s
}

func (p *preds) refersInto(t types.TypeID, s *tset, seen map[types.TypeID]bool) {
	ty := p.c.cfg.Types.Get(t)
	switch ty.Kind {
	case types.KRef, types.KMut, types.KSlice, types.KMutSlice:
		s.add(ty.Elem)
		return
	case types.KStr:
		s.add(types.U8)
		return
	case types.KOptional, types.KArray, types.KErrorUnion:
		p.refersInto(ty.Elem, s, seen)
		return
	}
	if p.isView(t) {
		s.any = true
		return
	}
	if seen[t] {
		return
	}
	seen[t] = true
	fs, _ := p.fields(t)
	for _, f := range fs {
		p.refersInto(f, s, seen)
	}
}

// contents: the type itself and everything inside it.
func (p *preds) contents(t types.TypeID) tset {
	var s tset
	p.contentsInto(t, &s, newRecur(p.c.cfg.Types))
	return s
}

// contentsInto adds t's contents to s. On a polymorphically recursive type
// (`next: ?&G[?T]`) the contents are infinite: past recur's bound they are
// ANY, which makes every parameter a candidate supplier (automatic), the
// side that records more sources.
func (p *preds) contentsInto(t types.TypeID, s *tset, r *recur) {
	if s.any || s.m[t] {
		return
	}
	if stop, leave := r.enter(t); stop {
		s.any = true
		return
	} else if leave != nil {
		defer leave()
	}
	s.add(t)
	ty := p.c.cfg.Types.Get(t)
	switch ty.Kind {
	case types.KRef, types.KMut, types.KSlice, types.KMutSlice, types.KOptional, types.KArray,
		types.KErrorUnion, types.KRawPtr:
		p.contentsInto(ty.Elem, s, r)
		return
	case types.KStr:
		s.add(types.U8)
		return
	case types.KSelf:
		s.any = true
		return
	case types.KNamed:
		if e, ok := p.sliceElem(ty); ok {
			p.contentsInto(e, s, r) // Slice[T] is seen as []T
			return
		}
	case types.KParam:
		if p.env != nil && int(ty.Index) < len(p.env.gen) {
			for _, b := range p.env.gen[ty.Index].Bounds {
				for _, a := range b.Args {
					if !a.IsConst {
						p.contentsInto(a.Type, s, r)
					}
				}
			}
		}
		return
	}
	if p.isView(t) {
		s.any = true
		return
	}
	fs, _ := p.fields(t)
	for _, f := range fs {
		p.contentsInto(f, s, r)
	}
}

// ownBytes: S's own bytes: its fields, not what its shared
// references point at; a mut field counts with all it reaches, and a view,
// Self or a type parameter counts as ANY.
func (p *preds) ownBytes(t types.TypeID) tset {
	var s tset
	p.ownBytesInto(t, &s)
	return s
}

func (p *preds) ownBytesInto(t types.TypeID, s *tset) {
	if s.any || s.m[t] {
		return
	}
	s.add(t)
	ty := p.c.cfg.Types.Get(t)
	switch ty.Kind {
	case types.KOptional, types.KArray, types.KErrorUnion:
		p.ownBytesInto(ty.Elem, s)
		return
	case types.KMut, types.KMutSlice:
		p.contentsInto(ty.Elem, s, newRecur(p.c.cfg.Types))
		return
	case types.KRef, types.KSlice, types.KStr, types.KRawPtr:
		return // a shared reference field stops
	case types.KSelf, types.KParam:
		s.any = true
		return
	case types.KNamed:
		if e, ok := p.sliceElem(ty); ok {
			p.ownBytesInto(e, s) // what self points at is its elements
			return
		}
	}
	if p.isView(t) {
		s.any = true
		return
	}
	fs, _ := p.fields(t)
	for _, f := range fs {
		p.ownBytesInto(f, s)
	}
}

// sliceElem is T for the compiler-known interface type Slice[T].
// A Slice[T] namespace function's body sees self as []T or mut []T,
// so its signature's flows read `&Slice[T]` as `[]T`:
// the contents are T's, and the own bytes behind self are the elements'.
// Without this the interface has no fields, the automatic-source rule finds
// no supplier for `Get(&self, i: int): ?&T`, and borrow reports E0602 in
// std while callers see a result with no loan.
func (p *preds) sliceElem(ty *types.Type) (types.TypeID, bool) {
	k := p.c.known.Slice
	if k == types.NoItem || ty.Item != k || len(ty.Args) != 1 || ty.Args[0].IsConst {
		return types.NoType, false
	}
	return ty.Args[0].Type, true
}

// hasMut: a mut reference at any depth through optionals, arrays, error
// unions, shared references, slices, records and non-view struct fields.
// Past recur's bound on a polymorphically recursive type it answers true,
// the side that refuses more (behind's H mode, holdsSharedRef's "no
// source", sourceFieldReason's "mut").
func (p *preds) hasMut(t types.TypeID) bool {
	return p.hasMutSeen(t, map[types.TypeID]bool{}, newRecur(p.c.cfg.Types))
}

func (p *preds) hasMutSeen(t types.TypeID, seen map[types.TypeID]bool, r *recur) bool {
	ty := p.c.cfg.Types.Get(t)
	switch ty.Kind {
	case types.KMut, types.KMutSlice:
		return true
	case types.KOptional, types.KArray, types.KErrorUnion, types.KRef, types.KSlice:
		return p.hasMutSeen(ty.Elem, seen, r)
	}
	if p.isView(t) || seen[t] {
		return false
	}
	seen[t] = true
	if stop, leave := r.enter(t); stop {
		return true
	} else if leave != nil {
		defer leave()
	}
	fs, _ := p.fields(t)
	for _, f := range fs {
		if p.hasMutSeen(f, seen, r) {
			return true
		}
	}
	return false
}

// selfMention is the key paramsIn records for Self, which mentions every
// type parameter: two types that both mention Self share it.
const selfMention = ^uint32(0)

// paramsIn lists the type parameters t mentions.
func (c *Checker) paramsIn(t types.TypeID, out map[uint32]bool) {
	ty := c.cfg.Types.Get(t)
	switch ty.Kind {
	case types.KSelf:
		out[selfMention] = true
	case types.KParam:
		out[ty.Index] = true
	case types.KRef, types.KMut, types.KSlice, types.KMutSlice, types.KRawPtr, types.KOptional, types.KArray, types.KErrorUnion:
		if ty.Kind == types.KArray && ty.Len.Kind == types.CAParam {
			out[ty.Len.Param] = true
		}
		c.paramsIn(ty.Elem, out)
	case types.KRecord:
		for _, f := range ty.Fields {
			c.paramsIn(f.Type, out)
		}
	case types.KNamed:
		for _, a := range ty.Args {
			if a.IsConst {
				if a.Const.Kind == types.CAParam {
					out[a.Const.Param] = true
				}
			} else {
				c.paramsIn(a.Type, out)
			}
		}
	case types.KFn:
		for _, fp := range ty.Fn.Params {
			c.paramsIn(fp.Type, out)
		}
		c.paramsIn(ty.Fn.Result, out)
	}
}

// isRefParam: the parameter's type is &, mut, [] or mut [].
func isRefKind(k types.TypeKind) bool {
	return k == types.KRef || k == types.KMut || k == types.KSlice || k == types.KMutSlice
}

// referent is S for a parameter of type &S, mut S, []S, mut []S, and u8 for
// str; ok false for a by-value parameter.
func (c *Checker) referent(t types.TypeID) (types.TypeID, bool) {
	ty := c.cfg.Types.Get(t)
	if isRefKind(ty.Kind) {
		return ty.Elem, true
	}
	if ty.Kind == types.KStr {
		return types.U8, true
	}
	return types.NoType, false
}

// supplier is one automatic source candidate with its mode.
type supplier struct {
	param  types.ParamIndex
	behind bool
}

// automatic is the automatic-source rule and its modes under p's visible
// fields: the candidates that could supply r, each a carrier parameter
// whose contents meet what r refers to.
func (p *preds) automatic(sig *types.Signature, r types.TypeID) []supplier {
	c := p.c
	refs := p.refers(r)
	view := refs.any
	var out []supplier
	for i, prm := range sig.Params {
		pt := prm.Type
		if !c.isCarrier(pt) || c.cfg.Types.Kind(pt) == types.KFn {
			continue
		}
		if !view {
			// &Alloc and &Heap supply only view results; the test is on the
			// parameter's own type.
			if s, ok := c.referent(pt); ok && c.isAllocHandle(s) {
				continue
			}
			if !refs.meets(p.contents(pt)) {
				continue
			}
		}
		out = append(out, supplier{param: types.ParamIndex(i + 1), behind: p.behind(pt, r, refs)})
	}
	return out
}

// isAllocHandle is Alloc or Heap.
func (c *Checker) isAllocHandle(t types.TypeID) bool {
	ty := c.cfg.Types.Get(t)
	return ty.Kind == types.KNamed && ty.Item != types.NoItem && (ty.Item == c.known.Alloc || ty.Item == c.known.Heap)
}

// behind is the mode for a single supplier of type pt.
func (p *preds) behind(pt, r types.TypeID, refs tset) bool {
	s, isRef := p.c.referent(pt)
	if !isRef || p.hasMut(r) || refs.any {
		return false
	}
	return !refs.meets(p.ownBytes(s))
}

func suppliersEqual(a, b []supplier) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// signatureFlows computes sig.Flows from the written clauses and the types
// and returns the diagnostics of the signature.
func (c *Checker) signatureFlows(fn flowFn, cl *flowClauses) (types.Flows, []*diag.Diagnostic) {
	sig := fn.sig
	var ds []*diag.Diagnostic
	var fl types.Flows
	p := &preds{c: c, env: fn.env, mod: fn.mod, exportedOnly: sig.Exported}
	r := sig.Result
	if len(cl.fields) > 0 {
		fl.Declared = true
		ds = c.perFieldFlows(fn, p, cl.fields, &fl, ds)
	} else if cl.hasFrom {
		fl.Declared = true
		for _, fc := range cl.from {
			src, d := c.fromSource(fn, fc)
			if d != nil {
				ds = append(ds, d)
				fl.ClauseError = true
				continue
			}
			if !p.carrier(r) || !p.carrier(sig.Param(fc.param).Type) {
				continue // accepted with no effect
			}
			fl.Sources = append(fl.Sources, src)
		}
	} else if p.carrier(r) {
		fl.Sources, ds = c.automaticSources(fn, p, r, ds)
		fl.Ambiguous = len(ds) > 0
	}
	for _, st := range cl.stores {
		if d := c.storeClauseCheck(fn, st); d != nil {
			ds = append(ds, d)
			continue
		}
		fl.Stores = appendStore(fl.Stores, types.Store{From: st.from, Into: st.into})
	}
	for _, e := range cl.ends {
		fl.Ends = appendParam(fl.Ends, e)
	}
	return fl, ds
}

func appendStore(ss []types.Store, s types.Store) []types.Store {
	for _, x := range ss {
		if x == s {
			return ss
		}
	}
	return append(ss, s)
}

func appendParam(ps []types.ParamIndex, p types.ParamIndex) []types.ParamIndex {
	for _, x := range ps {
		if x == p {
			return ps
		}
	}
	return append(ps, p)
}

// automaticSources is the automatic-source rule with E0601.
func (c *Checker) automaticSources(fn flowFn, p *preds, r types.TypeID, ds []*diag.Diagnostic) ([]types.Source, []*diag.Diagnostic) {
	sig := fn.sig
	if !p.ownRef(r) {
		rp := map[uint32]bool{}
		c.paramsIn(r, rp)
		if len(rp) > 0 {
			var out []types.Source
			for i, prm := range sig.Params {
				pp := map[uint32]bool{}
				c.paramsIn(prm.Type, pp)
				hit := false
				for k := range pp {
					hit = hit || rp[k] || (k == selfMention && len(rp) > 0) || rp[selfMention]
				}
				if hit {
					out = append(out, types.Source{Param: types.ParamIndex(i + 1), Behind: isRefKind(c.cfg.Types.Kind(prm.Type))})
				}
			}
			return out, ds
		}
	}
	cands := p.automatic(sig, r)
	if len(cands) > 1 {
		d := diag.New(diag.E0601, fn.at, "several parameters could supply this result: say which with `from`").
			Fact("candidates", diag.TextList(c.paramNames(sig, cands)...))
		for _, s := range c.paramNames(sig, cands) {
			d.Alt("from " + s)
		}
		c.fromClauseFix(fn, d, cands)
		return allHeld(cands), append(ds, d)
	}
	if sig.Exported {
		all := &preds{c: c, env: p.env, mod: p.mod}
		if full := all.automatic(sig, r); !suppliersEqual(full, cands) {
			d := diag.New(diag.E0601, fn.at, "a private field changes where this exported function's result comes from: say it with `from`").
				Fact("candidates", diag.TextList(c.paramNames(sig, full)...))
			if f, ok := c.privateFieldChanging(sig, p, r, cands); ok {
				d.Text("private_field", f)
			}
			c.fromClauseFix(fn, d, full)
			return allHeld(full), append(ds, d)
		}
	}
	var out []types.Source
	for _, s := range cands {
		out = append(out, types.Source{Param: s.param, Behind: s.behind})
	}
	return out, ds
}

// allHeld is every candidate as H(p): what callers of an E0601 signature
// see.
func allHeld(cs []supplier) []types.Source {
	out := make([]types.Source, len(cs))
	for i, s := range cs {
		out[i] = types.Source{Param: s.param}
	}
	return out
}

func (c *Checker) paramNames(sig *types.Signature, cs []supplier) []string {
	out := make([]string, len(cs))
	for i, s := range cs {
		out[i] = c.cfg.Interner.Str(sig.Param(s.param).Name)
	}
	return out
}

// privateFieldChanging finds the first private field, in a walk of the
// result's and parameters' struct types in field order, whose inclusion
// changes the exported-only answer.
func (c *Checker) privateFieldChanging(sig *types.Signature, p *preds, r types.TypeID, base []supplier) (string, bool) {
	var order []fieldRef
	seen := map[types.TypeID]bool{}
	rc := newRecur(c.cfg.Types)
	var walk func(t types.TypeID)
	walk = func(t types.TypeID) {
		if seen[t] {
			return
		}
		seen[t] = true
		// Past recur's bound (a polymorphically recursive type) the walk
		// stops: it only names a field in E0601's
		// text, and every private field of the type's constructor was
		// listed at its first instance.
		if stop, leave := rc.enter(t); stop {
			return
		} else if leave != nil {
			defer leave()
		}
		ty := c.cfg.Types.Get(t)
		switch ty.Kind {
		case types.KRef, types.KMut, types.KSlice, types.KMutSlice, types.KOptional, types.KArray, types.KErrorUnion:
			walk(ty.Elem)
		case types.KNamed:
			if d, ok := c.cfg.Items.Get(ty.Item).Decl.(*types.StructDecl); ok {
				for i, f := range d.Fields {
					if !f.Exported {
						order = append(order, fieldRef{ty.Item, i})
					}
					walk(c.subst(f.Type, ty.Args, types.NoType))
				}
			}
		}
	}
	walk(r)
	for _, prm := range sig.Params {
		walk(prm.Type)
	}
	for _, f := range order {
		q := *p
		q.extra = f
		if !suppliersEqual(q.automatic(sig, r), base) {
			d := c.cfg.Items.Get(f.item).Decl.(*types.StructDecl)
			return c.cfg.Interner.Str(d.Fields[f.index].Name), true
		}
	}
	return "", false
}

// fromSource checks one `from p.f.g` (E0607, E0203) and returns its
// Source: `from p` is H(p), a path is B(p) through the named fields.
func (c *Checker) fromSource(fn flowFn, fc fromClause) (types.Source, *diag.Diagnostic) {
	tab := c.cfg.Types
	sig := fn.sig
	pt := sig.Param(fc.param).Type
	if len(fc.steps) == 0 {
		return types.Source{Param: fc.param}, nil
	}
	pname := c.cfg.Interner.Str(sig.Param(fc.param).Name)
	e0607 := func(at source.Span, field, reason string) *diag.Diagnostic {
		d := diag.New(diag.E0607, at, "this `from` path cannot be a source: "+reason).
			Text("field", field).Text("reason", reason)
		d.Fix(diag.Fix{Class: diag.Proven, Why: "write `from " + pname + "`",
			Edits: []diag.Edit{{Span: fc.at, Text: pname}}})
		return d
	}
	cur, isRef := c.referent(pt)
	if !isRef || tab.Kind(pt) == types.KStr {
		return types.Source{}, e0607(fc.at, c.cfg.Interner.Str(fc.steps[0].name), "by-value")
	}
	src := types.Source{Param: fc.param, Behind: true}
	for si, st := range fc.steps {
		for tab.Kind(cur) == types.KRef || tab.Kind(cur) == types.KMut {
			cur = tab.Get(cur).Elem
		}
		fname := c.cfg.Interner.Str(st.name)
		idx, ft, owner, ok := c.fieldOf(cur, st.name)
		if !ok {
			return types.Source{}, diag.New(diag.E0203, st.at, "no field `"+fname+"` in "+tab.TypeString(cur, nil)).
				Text("type", tab.TypeString(cur, nil)).Fact("members", diag.TextList(c.fieldNames(cur)...))
		}
		last := si == len(fc.steps)-1
		view := false
		if d, ok := c.cfg.Items.Get(owner).Decl.(*types.StructDecl); ok {
			view = d.View
		}
		if reason := c.sourceFieldReason(ft, view); last && reason != "" {
			return types.Source{}, e0607(st.at, fname, reason)
		} else if !last && c.passesReference(ft) {
			// A reference or view before the last step can never cover
			// anything.
			return types.Source{}, e0607(st.at, fname, "not-a-reference")
		}
		src.Path = append(src.Path, types.FieldStep{Field: uint32(idx), Type: ft})
		cur = ft
	}
	return src, nil
}

// fieldOf finds a field of a struct instance, any visibility.
func (c *Checker) fieldOf(t types.TypeID, name source.Symbol) (int, types.TypeID, types.ItemID, bool) {
	ty := c.cfg.Types.Get(t)
	if ty.Kind != types.KNamed {
		return 0, types.NoType, types.NoItem, false
	}
	d, ok := c.cfg.Items.Get(ty.Item).Decl.(*types.StructDecl)
	if !ok {
		return 0, types.NoType, types.NoItem, false
	}
	for i, f := range d.Fields {
		if f.Name == name {
			return i, c.subst(f.Type, ty.Args, types.NoType), ty.Item, true
		}
	}
	return 0, types.NoType, types.NoItem, false
}

func (c *Checker) fieldNames(t types.TypeID) []string {
	ty := c.cfg.Types.Get(t)
	var out []string
	if ty.Kind == types.KNamed {
		if d, ok := c.cfg.Items.Get(ty.Item).Decl.(*types.StructDecl); ok {
			for _, f := range d.Fields {
				out = append(out, c.cfg.Interner.Str(f.Name))
			}
		}
	}
	return out
}

// sourceFieldReason is why the last field of a `from` path cannot be a
// source, "" when it can: it must hold a shared reference or a view;
// a raw-pointer field of a view is the one exception.
func (c *Checker) sourceFieldReason(ft types.TypeID, ownerView bool) string {
	tab := c.cfg.Types
	switch tab.Kind(ft) {
	case types.KMut, types.KMutSlice:
		return "mut"
	case types.KRawPtr:
		if ownerView {
			return ""
		}
		return "inline"
	}
	p := &preds{c: c}
	if c.holdsSharedRef(ft, map[types.TypeID]bool{}) {
		return ""
	}
	if p.hasMut(ft) {
		if !c.mutInClosure(ft) {
			// hasMut answered true at recur's bound on a polymorphically
			// recursive type (the side that refuses more), and no mut can
			// occur in it: say so, not "mut".
			return "recursive-type"
		}
		return "mut"
	}
	if tab.Kind(ft) == types.KNamed || tab.Kind(ft) == types.KArray || tab.Kind(ft) == types.KRecord {
		return "inline"
	}
	return "not-a-reference"
}

// holdsSharedRef: &, [], str; ?, [N], E! of those; a record with such a
// field; a view struct; a non-view struct with no mut inside and such a
// field.
func (c *Checker) holdsSharedRef(t types.TypeID, seen map[types.TypeID]bool) bool {
	tab := c.cfg.Types
	ty := tab.Get(t)
	switch ty.Kind {
	case types.KRef, types.KSlice, types.KStr:
		return true
	case types.KOptional, types.KArray, types.KErrorUnion:
		return c.holdsSharedRef(ty.Elem, seen)
	case types.KRecord:
		for _, f := range ty.Fields {
			if c.holdsSharedRef(f.Type, seen) {
				return true
			}
		}
		return false
	case types.KNamed:
		d, ok := c.cfg.Items.Get(ty.Item).Decl.(*types.StructDecl)
		if !ok || seen[t] {
			return false
		}
		if d.View {
			return true
		}
		seen[t] = true
		p := &preds{c: c}
		if p.hasMut(t) {
			return false
		}
		for _, f := range d.Fields {
			if c.holdsSharedRef(c.subst(f.Type, ty.Args, types.NoType), seen) {
				return true
			}
		}
	}
	return false
}

// passesReference: a path step whose field is a reference or a view, which
// a later step would go through.
func (c *Checker) passesReference(ft types.TypeID) bool {
	ty := c.cfg.Types.Get(ft)
	if isRefKind(ty.Kind) || ty.Kind == types.KStr {
		return true
	}
	return (&preds{c: c}).isView(ft)
}

// storeClauseCheck checks one `*q into p`: the target needs a
// writable path (E0507, phase types), the source a reference or slice
// (E0301). The first failure of the clause is its only diagnostic.
func (c *Checker) storeClauseCheck(fn flowFn, st storeClause) *diag.Diagnostic {
	sig := fn.sig
	tab := c.cfg.Types
	into := sig.Param(st.into).Type
	if !c.writableTarget(into) {
		name := c.cfg.Interner.Str(sig.Param(st.into).Name)
		return diag.New(diag.E0507, st.at, "`"+name+"` cannot be written through, so nothing can be stored into it").
			In(diag.PhaseTypes).Text("place", name).Text("reason", "store-target").Alt("make `" + name + "` a `mut` parameter")
	}
	from := sig.Param(st.from).Type
	switch tab.Kind(from) {
	case types.KRef, types.KMut, types.KSlice, types.KMutSlice, types.KStr:
		return nil
	}
	return diag.New(diag.E0301, st.fromAt, "the source of `*q into p` must be a reference or a slice").
		Text("expected", "reference or slice").Text("found", tab.TypeString(from, sig.Generics))
}

// writableTarget: p is mut, or reaches loan-interior mutability (a Cell,
// Mutex or Chan holding a carrier, an Arena or Scope, a `mut view struct`, or
// a type reaching one) through any steps, shared reference fields included:
// the restoring step may come after shared ones, not only after mut steps and
// inline fields.
func (c *Checker) writableTarget(t types.TypeID) bool {
	tab := c.cfg.Types
	switch tab.Kind(t) {
	case types.KMut, types.KMutSlice:
		return true
	case types.KRef:
		t = tab.Get(t).Elem
	}
	return c.reachesInterior(t, map[types.TypeID]bool{}, newRecur(c.cfg.Types))
}

// reachesInterior answers false past recur's bound on a polymorphically
// recursive type, the side that refuses more: the store clause gets E0507.
func (c *Checker) reachesInterior(t types.TypeID, seen map[types.TypeID]bool, r *recur) bool {
	tab := c.cfg.Types
	ty := tab.Get(t)
	switch ty.Kind {
	case types.KMut, types.KMutSlice:
		return true
	case types.KOptional, types.KArray, types.KErrorUnion:
		return c.reachesInterior(ty.Elem, seen, r)
	case types.KRef, types.KSlice:
		// Interior mutability reached through a shared reference is still
		// interior mutability (`*q into p` needs `p` to be `mut`, or to
		// have interior mutability): `c: &Ctx` whose `hb: &Hub` holds a
		// channel takes `*c into c`.
		if seen[t] {
			return false
		}
		seen[t] = true
		return c.reachesInterior(ty.Elem, seen, r)
	case types.KRecord:
		for _, f := range ty.Fields {
			if c.reachesInterior(f.Type, seen, r) {
				return true
			}
		}
	case types.KNamed:
		k := &c.known
		if ty.Item != types.NoItem && (ty.Item == k.Cell || ty.Item == k.Mutex || ty.Item == k.Chan || ty.Item == k.SpinCore) {
			return len(ty.Args) > 0 && !ty.Args[0].IsConst && c.isCarrier(ty.Args[0].Type)
		}
		if ty.Item != types.NoItem && (ty.Item == k.Arena || ty.Item == k.Scope) {
			return true
		}
		d, ok := c.cfg.Items.Get(ty.Item).Decl.(*types.StructDecl)
		if !ok || seen[t] {
			return false
		}
		if d.MutView {
			return true
		}
		seen[t] = true
		if stop, leave := r.enter(t); stop {
			return false
		} else if leave != nil {
			defer leave()
		}
		if d.View {
			for _, a := range ty.Args {
				if !a.IsConst && c.reachesInterior(a.Type, seen, r) {
					return true
				}
			}
		}
		for _, f := range d.Fields {
			if c.reachesInterior(c.subst(f.Type, ty.Args, types.NoType), seen, r) {
				return true
			}
		}
	}
	return false
}

// flowsCovered is the E0608 relation, the contract's statement of
// it (types.FlowsCovered), so E0608 and the MIR validator read one
// copy: every flow of the value is covered by a declared flow of the
// target, same kind and parameter position; H(p) covers B(p); `x into p`
// and `*x into p` cover only themselves; a value's `ends p` must
// be declared by the target.
func flowsCovered(value, target types.Flows, valueInto, targetInto []types.ParamIndex) bool {
	return types.FlowsCovered(value, target, valueInto, targetInto)
}

// fnConvert is the E0608 check at a conversion inside a body (an argument, a typed
// binding, a field, a return): a fn value met where a fn type equal to its
// own but for flows is expected is accepted when the target's declared
// flows cover the value's, else E0608 at the value. It reports whether the
// two types were such a pair (false leaves E0301 to the caller).
func (u *unit) fnConvert(e ast.Expr, want, got types.TypeID) bool {
	w, g := u.tab.Get(want), u.tab.Get(got)
	if w.Kind != types.KFn || g.Kind != types.KFn || !types.SameFnShape(w.Fn, g.Fn) {
		return false
	}
	u.fnFlowsCovered(e, want, got)
	return true
}

// fnFlowsCovered is E0608 at e when the fn value of type got has a flow
// the fn type want does not declare.
func (u *unit) fnFlowsCovered(e ast.Expr, want, got types.TypeID) {
	vf, vi := types.FnFlows(u.tab.Get(got).Fn)
	tf, ti := types.FnFlows(u.tab.Get(want).Fn)
	if flowsCovered(vf, tf, vi, ti) {
		return
	}
	u.err(diag.E0608, e.Span(), "this function's flows are not covered by the `fn` type it is given as").
		Text("expected", u.c.fnFlowsString(tf, ti, u.tab.Get(want).Fn.Result)).
		Text("found", u.c.fnFlowsString(vf, vi, u.tab.Get(got).Fn.Result)).
		Alt("declare the flow in the `fn` type, e.g. `fn(x: &i32): T from x`")
}

// fnFlowsString spells a fn type's flows positionally: "B(#1)", "from #2",
// "*#1 into #2", "ends #1"; "none" when it has none. A per-field clause is
// spelled by field in place of its union, "from (r: #1, s: B(#2))", with
// the field names of result, the fn type's result.
func (c *Checker) fnFlowsString(f types.Flows, into []types.ParamIndex, result types.TypeID) string {
	name := func(p types.ParamIndex) string { return "#" + strconv.Itoa(int(p)) }
	var out []string
	if len(f.Fields) > 0 {
		out = append(out, c.fieldClauseText(result, f.Fields, func(src types.Source) string {
			if src.Behind {
				return "B(" + name(src.Param) + ")"
			}
			return name(src.Param)
		}))
	} else {
		for _, src := range f.Sources {
			if src.Behind {
				out = append(out, "B("+name(src.Param)+")")
			} else {
				out = append(out, "from "+name(src.Param))
			}
		}
	}
	for _, st := range f.Stores {
		out = append(out, "*"+name(st.From)+" into "+name(st.Into))
	}
	for i, p := range into {
		if p != types.NoParam {
			out = append(out, name(types.ParamIndex(i+1))+" into "+name(p))
		}
	}
	for _, e := range f.Ends {
		out = append(out, "ends "+name(e))
	}
	if len(out) == 0 {
		return "none"
	}
	return strings.Join(out, ", ")
}

// sigInto is a signature's parameters' into, positionally.
func sigInto(sig *types.Signature) []types.ParamIndex {
	out := make([]types.ParamIndex, len(sig.Params))
	for i, p := range sig.Params {
		out[i] = p.Into
	}
	return out
}
