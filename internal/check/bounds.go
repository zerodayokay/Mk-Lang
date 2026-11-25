// Why: every generic call, type expression, impl lookup and const or global
// declaration asks whether a type meets a bound; the answer must be the same
// everywhere and must record what it read, for incremental builds.
//
// What: satisfies (Plain, Send, Share, Fields[B], interfaces, the computed
// Lane and Integer), ImplOf and ImplArgs (impl lookup), boundFailure
// (E0701), constPlain and globalShare, sliceImplicit (the implicit bounds of
// `Slice[T]` functions, HeapList.Extend and List.Resize) and shapeReads.
//
// How: ImplOf searches only the two modules coherence allows (the
// interface's and the type constructor's, plus std for a builtin
// constructor) and matches each impl's target one way against the closed
// type, binding the impl's parameters; it is not unification.

package check

import (
	"mk/internal/diag"
	"mk/internal/source"
	"mk/internal/types"
)

// maxImplDepth caps the recursion of impl bounds on bound arguments
// (impl[T: I] I for S[T] asked of S[S[..]]); past it the answer is "no".
// A guess, not measured: a written type nests far less.
const maxImplDepth = 64

// satisfies reports whether t meets bound b in the unit's environment.
// Poison satisfies every bound. rec records impl reads.
func (c *Checker) satisfies(env *Env, t types.TypeID, b types.Bound, rec recorder) bool {
	return c.satisfiesDepth(env, t, b, rec, 0)
}

func (c *Checker) satisfiesDepth(env *Env, t types.TypeID, b types.Bound, rec recorder, depth int) bool {
	tab := c.cfg.Types
	if t == types.Poison || t == types.NoType {
		return true
	}
	ty := tab.Get(t)
	if ty.Kind == types.KParam && env != nil && int(ty.Index) < len(env.gen) {
		if n := env.narrow[ty.Index]; n != types.NoType {
			return c.satisfiesDepth(env, n, b, rec, depth)
		}
		if b.Kind == types.BoundInterface || b.Kind == types.BoundFields {
			// A parameter meets an interface or Fields bound only through a
			// bound of its own that is the same one.
			for _, own := range env.gen[ty.Index].Bounds {
				if boundEq(own, b) {
					return true
				}
			}
			return false
		}
	}
	if ty.Kind == types.KSelf {
		// Self inside an interface declaration: nothing is known about it but
		// the interface itself; no diagnostic is drawn from it (Poison-like).
		return true
	}
	switch b.Kind {
	case types.BoundPlain:
		c.shapeReads(env, t, rec)
		return c.FactsIn(env, t).Kind == types.Plain
	case types.BoundSend:
		c.shapeReads(env, t, rec)
		return c.FactsIn(env, t).Send
	case types.BoundShare:
		c.shapeReads(env, t, rec)
		return c.FactsIn(env, t).Share
	case types.BoundFields:
		return c.fieldsSatisfy(env, t, b, rec, depth)
	}
	if b.Item == types.NoItem {
		return true
	}
	if c.isLaneItem(b.Item) {
		return isLane(ty) // computed, never an impl (impls.go)
	}
	if c.isIntegerItem(b.Item) {
		return isInteger(ty) // computed, never an impl (impls.go)
	}
	_, ok := c.implOf(env, b.Item, b.Args, t, rec, depth)
	return ok
}

// isLaneItem reports whether item is std.simd's Lane. It reads the
// item's name and module, not KnownItems.Lane: the driver reads KnownItems
// once, when the prelude's closure is resolved, before a std module the
// package imports (std.simd) has items, so the field is NoItem there.
// A name compare first keeps it off the path of every other bound.
func (c *Checker) isLaneItem(item types.ItemID) bool {
	if item == types.NoItem {
		return false
	}
	if item == c.known.Lane {
		return true
	}
	it := c.cfg.Items.Get(item)
	if c.cfg.Interner.Str(it.Name) != "Lane" || it.Owner != types.NoItem {
		return false
	}
	m := c.cfg.Items.Module(it.Module)
	return m != nil && m.Path == "std.simd"
}

// isIntegerItem reports whether item is std.math's Integer,
// read by name and module as isLaneItem reads Lane: a std module the
// package imports has no KnownItems entry when the prelude's closure is
// resolved.
func (c *Checker) isIntegerItem(item types.ItemID) bool {
	if item == types.NoItem {
		return false
	}
	it := c.cfg.Items.Get(item)
	if it.Kind != types.KindInterface || c.cfg.Interner.Str(it.Name) != "Integer" || it.Owner != types.NoItem {
		return false
	}
	m := c.cfg.Items.Module(it.Module)
	return m != nil && m.Path == "std.math"
}

// isInteger is math.Integer's computed answer: i8 to i64, u8 to
// u64, int and uint; no float, bool or char.
func isInteger(ty *types.Type) bool {
	return ty.Kind == types.KInt || ty.Kind == types.KIntPtr
}

// isLane is simd.Lane's computed answer: the
// integer types, int, uint, f32 and f64. A parameter bounded by Lane meets
// it through its own bound (satisfiesDepth's KParam case); bool and char
// are no lanes.
func isLane(ty *types.Type) bool {
	switch ty.Kind {
	case types.KInt, types.KIntPtr, types.KFloat:
		return true
	}
	return false
}

// fieldsSatisfy is Fields[B]: every exported field of a
// struct, every field of a record, meets B. Fields[Plain] and the other
// built-in inner bounds are read without the inner bound (decl.go bound),
// so they hold here; any other type has no fields to loop over.
func (c *Checker) fieldsSatisfy(env *Env, t types.TypeID, b types.Bound, rec recorder, depth int) bool {
	tab := c.cfg.Types
	inner := types.Bound{Kind: types.BoundInterface, Item: b.Item, Args: b.Args}
	ty := tab.Get(t)
	switch ty.Kind {
	case types.KRecord:
		if b.Item == types.NoItem {
			return true
		}
		for _, f := range ty.Fields {
			if !c.satisfiesDepth(env, f.Type, inner, rec, depth) {
				return false
			}
		}
		return true
	case types.KNamed:
		d, ok := c.declOfItem(c.cfg.Items.Get(ty.Item)).(*types.StructDecl)
		if !ok {
			return false
		}
		rec.fields(c.cfg.Items.Get(ty.Item).Key, false)
		if b.Item == types.NoItem {
			return true
		}
		for _, f := range d.Fields {
			if f.Exported && !c.satisfiesDepth(env, c.subst(f.Type, ty.Args, types.NoType), inner, rec, depth) {
				return false
			}
		}
		return true
	}
	return false
}

// ImplOf returns the impl of interface iface with arguments args for the
// closed type t, searching only the two modules coherence allows (the
// interface's and the type constructor's; for a builtin constructor also
// every std module), and records a positive (ReadImpl) or negative
// (ReadNoImpl) read in rs.
func (c *Checker) ImplOf(iface types.ItemID, args []types.GenericArg, t types.TypeID, rs *types.ReadSet) (types.ItemID, bool) {
	return c.implOf(c.NewEnv(nil), iface, args, t, recorder{rs: rs}, 0)
}

func (c *Checker) implOf(env *Env, iface types.ItemID, args []types.GenericArg, t types.TypeID, rec recorder, depth int) (types.ItemID, bool) {
	ifaceIt := c.cfg.Items.Get(iface)
	target := c.cfg.Types.TypeString(t, envGen(env))
	if id, ok := c.findImpl(env, ifaceIt, args, t, rec, depth); ok {
		rec.impl(ifaceIt.Key, target)
		return id, true
	}
	rec.noImpl(ifaceIt.Key, target)
	return types.NoItem, false
}

// stdViews are the std modules an impl for a builtin constructor may live
// in, as the environment's viewer sees them: the viewer if it is std, and
// the std modules it imports, directly or not. A viewer-less query sees the
// finished std modules. Either way no module still in stage 5 on another
// goroutine is read.
func (c *Checker) stdViews(env *Env) []*Module {
	var out []*Module
	if env == nil || env.view == nil {
		for _, om := range c.modulesInOrder() {
			if om.mod.Std {
				out = append(out, om)
			}
		}
		return out
	}
	v := env.view
	for _, id := range v.depList() {
		if om := c.module(id); om != nil && om.mod.Std {
			out = append(out, om)
		}
	}
	if v.mod.Std {
		out = append(out, v)
	}
	return out
}

func envGen(e *Env) []types.GenericParam {
	if e == nil {
		return nil
	}
	return e.gen
}

func (c *Checker) findImpl(env *Env, ifaceIt *types.Item, args []types.GenericArg, t types.TypeID, rec recorder, depth int) (types.ItemID, bool) {
	if ok, set := c.errorSetFormat(env, ifaceIt, t, rec, depth); set {
		return types.NoItem, ok // a structural yes, no impl item
	}
	ck, ok := c.ctorOf(t)
	if !ok || depth > maxImplDepth {
		return types.NoItem, false
	}
	key := implKey{iface: ifaceIt.ID, ctor: ck}
	var probe []*Module
	if om := c.module(ifaceIt.Module); om != nil {
		probe = append(probe, om)
	}
	if cm := c.ctorModule(ck); cm != types.NoModule && cm != ifaceIt.Module {
		if om := c.module(cm); om != nil {
			probe = append(probe, om)
		}
	} else if cm == types.NoModule {
		for _, om := range c.stdViews(env) {
			if om.ID != ifaceIt.Module {
				probe = append(probe, om)
			}
		}
	}
	for _, om := range probe {
		id, ok := om.implTab[key]
		if !ok {
			continue
		}
		if c.implApplies(env, id, args, t, rec, depth) {
			return id, true
		}
		return types.NoItem, false // one impl per pair: no other can apply
	}
	return types.NoItem, false
}

// implApplies matches the impl's target against t, binding the impl's
// parameters, then requires its interface arguments to equal args and its
// own bounds to hold for the bound arguments.
func (c *Checker) implApplies(env *Env, impl types.ItemID, args []types.GenericArg, t types.TypeID, rec recorder, depth int) bool {
	d := c.cfg.Items.Get(impl).Decl.(*types.ImplDecl)
	binds := make([]types.GenericArg, len(d.Generics))
	if !c.match(d.For, t, binds) {
		return false
	}
	for i, g := range d.Generics {
		if !binds[i].IsConst && binds[i].Type == types.NoType && g.Const {
			binds[i] = types.ConstGenericArg(types.ConstArg{Kind: types.CAParam, Param: uint32(i), Type: g.Type})
		}
	}
	if c.isFreeIface(d.Bound.Item) && !c.releaseElemsOK(env, t, d.Bound.Item, rec, depth) {
		return false // std's container impls of Free are unbounded
	}
	s := substituter{c: c, args: binds, self: types.NoType}
	if len(args) > 0 && len(d.Bound.Args) == len(args) {
		for i, a := range d.Bound.Args {
			if got := s.genericArg(a); got != args[i] && args[i].Type != types.Poison {
				return false
			}
		}
	}
	for i, g := range d.Generics {
		if g.Const || binds[i].Type == types.NoType {
			continue
		}
		for _, b := range c.substBounds(g.Bounds, binds, types.NoType) {
			if !c.satisfiesDepth(env, binds[i].Type, b, rec, depth+1) {
				return false
			}
		}
	}
	return true
}

// ImplArgs binds impl's generic parameters by matching its target against
// the closed type t, the binding ImplOf's search makes (implApplies). ok is
// false when impl is no impl, its target does not match t, or a parameter
// is left unbound. For mono's ImplMethod (the driver's monoEnv keeps a
// copy of match until it calls this).
func (c *Checker) ImplArgs(impl types.ItemID, t types.TypeID) ([]types.GenericArg, bool) {
	d, ok := c.cfg.Items.Get(impl).Decl.(*types.ImplDecl)
	if !ok {
		return nil, false
	}
	binds := make([]types.GenericArg, len(d.Generics))
	if !c.match(d.For, t, binds) {
		return nil, false
	}
	for _, b := range binds {
		if !b.IsConst && b.Type == types.NoType {
			return nil, false
		}
	}
	return binds, true
}

// match is a one-way structural match of pattern p (closed over an impl's
// parameters) against t, binding each KParam of p in binds. It is not
// unification: t's own parameters are opaque.
func (c *Checker) match(p, t types.TypeID, binds []types.GenericArg) bool {
	tab := c.cfg.Types
	pt := tab.Get(p)
	if pt.Kind == types.KParam && int(pt.Index) < len(binds) {
		b := &binds[pt.Index]
		if b.Type == types.NoType && !b.IsConst {
			*b = types.TypeArg(t)
			return true
		}
		return !b.IsConst && b.Type == t
	}
	if p == t {
		return true
	}
	tt := tab.Get(t)
	if pt.Kind != tt.Kind {
		return false
	}
	switch pt.Kind {
	case types.KRef, types.KMut, types.KSlice, types.KMutSlice, types.KRawPtr, types.KOptional:
		return c.match(pt.Elem, tt.Elem, binds)
	case types.KArray:
		return c.matchConst(pt.Len, tt.Len, binds) && c.match(pt.Elem, tt.Elem, binds)
	case types.KErrorUnion:
		return pt.Errors == tt.Errors && c.match(pt.Elem, tt.Elem, binds)
	case types.KNamed:
		if pt.Item != tt.Item || len(pt.Args) != len(tt.Args) {
			return false
		}
		for i, a := range pt.Args {
			if a.IsConst != tt.Args[i].IsConst {
				return false
			}
			if a.IsConst {
				if !c.matchConst(a.Const, tt.Args[i].Const, binds) {
					return false
				}
			} else if !c.match(a.Type, tt.Args[i].Type, binds) {
				return false
			}
		}
		return true
	case types.KRecord:
		if len(pt.Fields) != len(tt.Fields) {
			return false
		}
		for i, f := range pt.Fields {
			if f.Name != tt.Fields[i].Name || !c.match(f.Type, tt.Fields[i].Type, binds) {
				return false
			}
		}
		return true
	}
	return false
}

func (c *Checker) matchConst(p, t types.ConstArg, binds []types.GenericArg) bool {
	if p.Kind == types.CAParam && int(p.Param) < len(binds) {
		b := &binds[p.Param]
		if !b.IsConst && b.Type == types.NoType {
			*b = types.ConstGenericArg(t)
			return true
		}
		return b.IsConst && b.Const == t
	}
	return p == t
}

// boundFailure is the E0701 of a type expression's or a call's argument:
// facts param, bound, type; env is the environment the bound was
// judged in.
func (c *Checker) boundFailure(env *Env, at source.Span, param source.Symbol, b types.Bound, t types.TypeID, gens []types.GenericParam) *diag.Diagnostic {
	ts := c.cfg.Types.TypeString(t, gens)
	ps := c.cfg.Interner.Str(param)
	bs := c.boundString(b, gens)
	d := diag.New(diag.E0701, at, "`"+ts+"` does not satisfy `"+ps+": "+bs+"`").
		Text("param", ps).Text("bound", bs).Text("type", ts)
	if b.Kind == types.BoundInterface && b.Item == c.known.Format && b.Item != types.NoItem {
		c.errFmtFacts(d, t)
	}
	c.releaseBoundFacts(d, env, b, t, gens)
	return d
}

// constPlain is the const rule, queued to step 4: a const's
// type must be Plain; one holding a Cell or a raw pointer is E1002 in phase
// types, any other non-plain type E0408.
func (m *Module) constPlain(it *types.Item, t types.TypeID) {
	c := m.c
	if t == types.Poison || !c.closed(t) {
		return
	}
	if part, ok := c.byValuePart(t, func(p types.TypeID) bool {
		ty := c.cfg.Types.Get(p)
		return ty.Kind == types.KRawPtr || (ty.Kind == types.KNamed && ty.Item != types.NoItem && ty.Item == c.known.Cell)
	}); ok {
		m.Diags = append(m.Diags, diag.New(diag.E1002, it.Span, "a const cannot hold a `Cell` or a raw pointer").In(diag.PhaseTypes).
			Text("type", c.cfg.Types.TypeString(t, nil)).Text("part", c.cfg.Types.TypeString(part, nil)))
		return
	}
	if c.Facts(t).Kind != types.Plain {
		m.Diags = append(m.Diags, diag.New(diag.E0408, it.Span, "a const must be plain data").
			Text("type", c.cfg.Types.TypeString(t, nil)).Text("reason", "const"))
	}
}

// globalShare is the global rule, queued to step 4: a `var`
// global's type must be Share, else E1002 in phase types, with the
// innermost by-value part that is not Share.
func (m *Module) globalShare(it *types.Item, t types.TypeID) {
	c := m.c
	if t == types.Poison || !c.closed(t) || c.Facts(t).Share {
		return
	}
	part := t
	for {
		next, ok := c.childPart(part, func(p types.TypeID) bool { return !c.Facts(p).Share })
		if !ok {
			break
		}
		part = next
	}
	m.Diags = append(m.Diags, diag.New(diag.E1002, it.Span, "a global must be `Share`: every task can read it").In(diag.PhaseTypes).
		Text("type", c.cfg.Types.TypeString(t, nil)).Text("part", c.cfg.Types.TypeString(part, nil)))
}

// byValuePart finds a by-value component of the closed type t (t itself
// included) that meets pred, depth first in field order.
func (c *Checker) byValuePart(t types.TypeID, pred func(types.TypeID) bool) (types.TypeID, bool) {
	return c.partWalk(t, pred, map[types.TypeID]bool{})
}

func (c *Checker) partWalk(t types.TypeID, pred func(types.TypeID) bool, seen map[types.TypeID]bool) (types.TypeID, bool) {
	if seen[t] {
		return types.NoType, false
	}
	seen[t] = true
	if pred(t) {
		return t, true
	}
	for _, ch := range c.byValueChildren(t) {
		if p, ok := c.partWalk(ch, pred, seen); ok {
			return p, true
		}
	}
	return types.NoType, false
}

// childPart is the first direct by-value component of t meeting pred.
func (c *Checker) childPart(t types.TypeID, pred func(types.TypeID) bool) (types.TypeID, bool) {
	for _, ch := range c.byValueChildren(t) {
		if pred(ch) {
			return ch, true
		}
	}
	return types.NoType, false
}

// byValueChildren are the components a value of t holds inline: element of
// an array, optional or error union, record and struct fields, enum
// payloads, and the payload fields of each member of an error union's or
// error set's errors (a raw pointer in an error payload counts). A
// generic error member (a parameter) has no fields until it is bound.
// References, slices, raw pointers and fn values hold none.
func (c *Checker) byValueChildren(t types.TypeID) []types.TypeID {
	tab := c.cfg.Types
	ty := tab.Get(t)
	switch ty.Kind {
	case types.KArray, types.KOptional:
		return []types.TypeID{ty.Elem}
	case types.KErrorUnion, types.KErrorSet:
		var out []types.TypeID
		if ty.Kind == types.KErrorUnion {
			out = append(out, ty.Elem)
		}
		for _, em := range tab.Members(ty.Errors) {
			if em.IsParam || em.Item == types.NoItem {
				continue
			}
			if d, ok := c.cfg.Items.Get(em.Item).Decl.(*types.ErrorDecl); ok {
				for _, f := range d.Fields {
					out = append(out, f.Type)
				}
			}
		}
		return out
	case types.KRecord:
		out := make([]types.TypeID, len(ty.Fields))
		for i, f := range ty.Fields {
			out[i] = f.Type
		}
		return out
	case types.KNamed:
		var out []types.TypeID
		switch d := c.cfg.Items.Get(ty.Item).Decl.(type) {
		case *types.StructDecl:
			for _, f := range d.Fields {
				out = append(out, c.subst(f.Type, ty.Args, types.NoType))
			}
		case *types.EnumDecl:
			for _, v := range d.Variants {
				for _, f := range v.Fields {
					out = append(out, c.subst(f.Type, ty.Args, types.NoType))
				}
			}
		}
		return out
	}
	return nil
}

// sliceImplicit is the implicit bound on the T of a `Slice[T]` namespace
// function: `Has` and `Index` need `T: Eq`, `Fill` and `CopyFrom` need
// `T: Plain`. The library's published signatures cannot carry them, so
// check adds the bound to the function's body (where `s[i].Eq(x)` then
// type-checks) and checks it at every call (E0701). std.core's
// `HeapList[T].Extend` and `List[T].Resize` copy elements and need
// `T: Plain` the same way; their calls are judged by implicitBounds
// (zerovalid.go), since they do not come through a Slice.
func (c *Checker) sliceImplicit(it *types.Item) (types.Bound, bool) {
	if c.listCopies(it) {
		return types.Bound{Kind: types.BoundPlain}, true
	}
	if c.known.Slice == types.NoItem || it.Owner != c.known.Slice || it.Kind != types.KindFunc {
		return types.Bound{}, false
	}
	switch c.cfg.Interner.Str(it.Name) {
	case "Has", "Index":
		if c.known.Eq == types.NoItem {
			return types.Bound{}, false
		}
		return types.Bound{Kind: types.BoundInterface, Item: c.known.Eq}, true
	case "Fill", "CopyFrom":
		return types.Bound{Kind: types.BoundPlain}, true
	}
	return types.Bound{}, false
}

// listCopies reports whether it is std.core's `HeapList[T].Extend` or
// `List[T].Resize`, which copy elements and which KnownItems does not name:
// matched by module, owner and name, as freebound.go's release rows are.
func (c *Checker) listCopies(it *types.Item) bool {
	if it.Kind != types.KindFunc || it.Owner == types.NoItem {
		return false
	}
	var owner string
	switch c.cfg.Interner.Str(it.Name) {
	case "Extend":
		owner = "HeapList"
	case "Resize":
		owner = "List"
	default:
		return false
	}
	o := c.cfg.Items.Get(it.Owner)
	if o == nil || c.cfg.Interner.Str(o.Name) != owner {
		return false
	}
	mod := c.cfg.Items.Module(o.Module)
	return mod != nil && mod.Std && mod.Path == "std.core"
}

// withSliceImplicit is gens with sliceImplicit's bound added to the first
// generic (Slice's T), copied so the signature is never changed.
func (c *Checker) withSliceImplicit(it *types.Item, gens []types.GenericParam) []types.GenericParam {
	b, ok := c.sliceImplicit(it)
	if !ok || len(gens) == 0 || gens[0].Const {
		return gens
	}
	out := append([]types.GenericParam(nil), gens...)
	out[0].Bounds = append(append([]types.Bound(nil), gens[0].Bounds...), b)
	return out
}

// shapeReads records ReadShape of every type item t mentions, at any depth
// (a type's shape, kind, Send, Share or Freeze counts as used), for a
// judgement of t's facts: t's kind, Send and Share follow
// from those items' shape records (a reference's Send from its element's
// Share), so an edit to any of them that changes a fact changes a read hash.
// Recording one an answer did not need only costs precision, never
// soundness. A type parameter narrowed by `if T is X` (env) is walked as X;
// an error set is always plain and an error union follows its ok type
// (kinds.go), so error members need no read.
func (c *Checker) shapeReads(env *Env, t types.TypeID, rec recorder) {
	if rec.rs == nil {
		return
	}
	tab := c.cfg.Types
	var walk func(t types.TypeID, depth int)
	walk = func(t types.TypeID, depth int) {
		if t == types.NoType || t == types.Poison || depth > maxFoldDepth {
			return
		}
		ty := tab.Get(t)
		switch ty.Kind {
		case types.KNamed:
			rec.shape(c.cfg.Items.Get(ty.Item).Key)
			for _, a := range ty.Args {
				if !a.IsConst {
					walk(a.Type, depth+1)
				}
			}
		case types.KRecord:
			for _, f := range ty.Fields {
				walk(f.Type, depth+1)
			}
		case types.KFn:
			// A fn value's facts do not depend on its parameter types.
		case types.KErrorUnion, types.KErrorSet:
			// The layout reads each error's payload, so a size
			// taken from it (sizeNow's `SizeOf[lib.Bad!u8]()`) depends on
			// each error's declaration and on its payload types' shapes
			// (without this read, a warm build kept the old size).
			for _, em := range tab.Members(ty.Errors) {
				if em.IsParam {
					continue
				}
				it := c.cfg.Items.Get(em.Item)
				rec.signature(it.Key)
				if d, ok := c.declOfItem(it).(*types.ErrorDecl); ok {
					for _, f := range d.Fields {
						walk(f.Type, depth+1)
					}
				}
			}
			walk(ty.Elem, depth+1)
		case types.KParam:
			if env != nil && int(ty.Index) < len(env.narrow) {
				if n := env.narrow[ty.Index]; n != types.NoType {
					walk(n, depth+1)
				}
			}
		default:
			if ty.Elem != types.NoType {
				walk(ty.Elem, depth+1)
			}
		}
	}
	walk(t, 0)
}
