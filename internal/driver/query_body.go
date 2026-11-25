// Why: some questions about a function (who calls it, what can panic or
// allocate in it, how deep its stack goes) are answered only by its body,
// not by its signature.
//
// What: the body queries callers, panics, allocs and stack, with the call
// graph they share (callsOf, walk, path, cycle) and the panic and check
// counting (panicsOf, checks).
//
// How: each is a walk over the lowered MIR bodies the session already holds
// after its Check (a module hit's bodies are decoded on first use,
// lazybody.go), so a warm query costs one process start and no lowering. The
// call graph follows direct calls (mir.CallDirect); a call through a fn value
// or an interface method in a generic body is counted as not followed, never
// guessed. Each answer says what it covers.

package driver

import (
	"slices"
	"strings"

	"mk/internal/mir"
	"mk/internal/types"
)

// maxReach bounds a call-graph walk (std's bodies are decoded as reached).
const maxReach = 20000

// callSite is one call of a body.
type callSite struct {
	callee   types.ItemID // CallDirect's item; NoItem otherwise
	indirect bool         // a fn value or an interface method
	at       string       // path:line
	line     uint32
	args     mir.Range // the call's arguments in the caller's body
	hasArgs  bool      // args is set: a call terminator of the body
}

// bodyOf is id's lowered body, nil when it has none (bodiless, poisoned or
// never lowered).
func (qx *queryIndex) bodyOf(id types.ItemID) *mir.Body {
	if qx.w.sem5 == nil {
		return nil
	}
	e := qx.w.sem5.body(id)
	if e == nil {
		return nil
	}
	return e.mir
}

// at is span sp of b as path:line, and the line.
func (qx *queryIndex) at(b *mir.Body, sp mir.SpanID) (string, uint32) {
	if int(sp) >= len(b.Spans) {
		return "?", 0
	}
	s := b.Spans[sp]
	f := qx.w.sm.File(s.File)
	if f == nil {
		return "?", 0
	}
	lc := f.LineCol(s.Start)
	return qx.w.sitePath(s.File) + ":" + itoaQ(int(lc.Line)), lc.Line
}

// callsOf is id's call sites, memoized.
func (qx *queryIndex) callsOf(id types.ItemID) []callSite {
	if cs, ok := qx.calls[id]; ok {
		return cs
	}
	var out []callSite
	if b := qx.bodyOf(id); b != nil {
		for _, bl := range b.Blocks {
			t := bl.Term
			if t.Kind != mir.TermCall && t.Kind != mir.TermTailCall {
				continue // a tail call is a call site too
			}
			at, line := qx.at(b, t.Span)
			switch t.Callee.Kind {
			case mir.CallDirect:
				if qx.sliceFn(t.Callee.Fn.Item) && t.Args.Len > 0 {
					// mono turns the receiver into a slice first: an
					// implementer's Items or ItemsMut call, made before this one.
					if s, ok := qx.itemsCallOf(t.Callee.Fn.Item, b.OperandType(b.Args[t.Args.Start].Operand)); ok {
						s.at, s.line = at, line
						out = append(out, s)
					}
				}
				out = append(out, callSite{callee: t.Callee.Fn.Item, at: at, line: line, args: t.Args, hasArgs: true})
			case mir.CallInterface:
				if qx.ifaceNamed(t.Callee.SelfType) {
					// Items or ItemsMut on the self of a Slice[T] namespace
					// function: mono makes it the slice itself, no call.
					continue
				}
				out = append(out, callSite{callee: types.NoItem, indirect: true, at: at, line: line})
			case mir.CallValue:
				out = append(out, callSite{callee: types.NoItem, indirect: true, at: at, line: line})
			}
		}
	}
	if qx.calls == nil {
		qx.calls = map[types.ItemID][]callSite{}
	}
	qx.calls[id] = out
	return out
}

// sliceFn reports whether fn is a Slice[T] namespace function: a function
// whose owner is an interface (only Slice[T] declares them).
func (qx *queryIndex) sliceFn(fn types.ItemID) bool {
	it := qx.w.items.Get(fn)
	if it.Owner == types.NoItem {
		return false
	}
	_, ok := qx.w.items.Get(it.Owner).Decl.(*types.InterfaceDecl)
	return ok
}

// ifaceNamed reports whether t is an interface type I[X]: in MIR, only the
// self of a Slice[T] namespace function.
func (qx *queryIndex) ifaceNamed(t types.TypeID) bool {
	if t == types.NoType {
		return false
	}
	ty := qx.w.tab.Get(t)
	if ty.Kind != types.KNamed {
		return false
	}
	_, ok := qx.w.items.Get(ty.Item).Decl.(*types.InterfaceDecl)
	return ok
}

// itemsCallOf is the call mono adds before a call of the Slice[T] namespace
// function fn on a receiver of type recv (mono's convertRecv): none for an
// array or a slice, which is resliced; the implementer's ItemsMut (for a
// mut self) or Items for a named type; an indirect site when the receiver's
// type is a type parameter, or its impl is not found, since the instance
// decides the implementer.
func (qx *queryIndex) itemsCallOf(fn types.ItemID, recv types.TypeID) (callSite, bool) {
	tab := qx.w.tab
	fd, ok := qx.w.items.Get(fn).Decl.(*types.FuncDecl)
	if !ok || len(fd.Sig.Params) == 0 {
		return callSite{}, false
	}
	name := "Items"
	if tab.Get(fd.Sig.Params[0].Type).Kind == types.KMut {
		name = "ItemsMut"
	}
	et := recv
	if k := tab.Get(recv).Kind; k == types.KRef || k == types.KMut {
		et = tab.Get(recv).Elem
	}
	switch tab.Get(et).Kind {
	case types.KArray, types.KSlice, types.KMutSlice:
		return callSite{}, false
	case types.KNamed:
		if qx.ifaceNamed(et) {
			return callSite{}, false // a Slice[T] function's own self, already a slice
		}
		var rs types.ReadSet
		ifc := qx.w.items.Get(fn).Owner
		if impl, ok := qx.w.sem5.chk.ImplOf(ifc, nil, et, &rs); ok && impl != types.NoItem {
			if m, ok := qx.w.items.ByKey(types.ImplMemberKey(qx.w.items.Get(impl).Key, name)); ok {
				return callSite{callee: m}, true
			}
		}
	}
	return callSite{callee: types.NoItem, indirect: true}, true
}

// name is an item's display name for an answer: a literal is named by its
// owner, a test and an impl method by its key, with the root's local names
// (keyText).
func (qx *queryIndex) name(id types.ItemID) string {
	it := qx.w.items.Get(id)
	if it.Kind == types.KindLiteral && it.Owner != types.NoItem {
		return qx.name(it.Owner) + " (fn literal)"
	}
	if n := qx.displayName(it); n != "" && it.Kind != types.KindTest {
		return n
	}
	return qx.keyText(it.Key)
}

// pkgBodies are the package's items that can have a body, in module then
// key order (literals and tests included).
func (qx *queryIndex) pkgBodies() []types.ItemID {
	var mods []*modNode
	if qx.s.pkgB != nil {
		mods = qx.s.pkgB.mods
	} else {
		mods = qx.s.nonStdMods()
	}
	var out []types.ItemID
	for _, m := range mods {
		if m.mod == nil {
			continue
		}
		for _, id := range m.mod.Items {
			switch qx.w.items.Get(id).Kind {
			case types.KindFunc, types.KindLiteral, types.KindTest:
				out = append(out, id)
			}
		}
	}
	return out
}

// isFunc reports whether e names something with a body to walk.
func (qx *queryIndex) isFunc(e qEntry) bool {
	k := qx.w.items.Get(e.id).Kind
	return k == types.KindFunc || k == types.KindLiteral
}

// callers is `callers F`: every direct call of F in the package's bodies.
func (qx *queryIndex) callers(e qEntry) []string {
	if !qx.isFunc(e) {
		return []string{e.name + " is not a function"}
	}
	var out []string
	indirect := 0
	for _, id := range qx.pkgBodies() {
		for _, c := range qx.callsOf(id) {
			switch {
			case c.callee == e.id:
				out = append(out, qx.name(id)+"  "+c.at)
			case c.indirect:
				indirect++
			}
		}
	}
	// A call lowering expands (Print and Arena.Fmt: an unpadded Print
	// into direct Writer.Str and value-function calls, any other into its
	// runner) leaves no CallDirect of F; the checked bodies' use sites
	// still name it.
	out = append(out, qx.useCallers(e.id)...)
	slices.Sort(out)
	out = slices.Compact(out)
	if len(out) == 0 {
		out = []string{"no direct caller of " + e.name + " in the package"}
	}
	if e.std {
		out = append(out, "(callers inside std are not listed)")
	}
	if indirect > 0 {
		out = append(out, itoaQ(indirect)+" call(s) through fn values or interfaces not resolved")
	}
	return out
}

// reach is the walk of the direct call graph from root: the functions in
// visit order (root first), each one's first caller (parent, the site),
// and the indirect sites met.
type reach struct {
	order    []types.ItemID
	parent   map[types.ItemID]types.ItemID
	site     map[types.ItemID]callSite
	indirect []callSite
	cut      bool // maxReach stopped the walk
}

// walk is a breadth-first walk from root; stop, when it returns true for a
// reached callee, ends the walk there.
func (qx *queryIndex) walk(root types.ItemID, stop func(types.ItemID) bool) (*reach, types.ItemID) {
	r := &reach{order: []types.ItemID{root}, parent: map[types.ItemID]types.ItemID{root: types.NoItem}, site: map[types.ItemID]callSite{}}
	for i := 0; i < len(r.order); i++ {
		if len(r.order) >= maxReach {
			r.cut = true
			break
		}
		for _, c := range qx.callsOf(r.order[i]) {
			if c.indirect {
				r.indirect = append(r.indirect, c)
				continue
			}
			if _, seen := r.parent[c.callee]; seen {
				continue
			}
			r.parent[c.callee], r.site[c.callee] = r.order[i], c
			r.order = append(r.order, c.callee)
			if stop != nil && stop(c.callee) {
				return r, c.callee
			}
		}
	}
	return r, types.NoItem
}

// path is root -> ... -> id with each call's site.
func (qx *queryIndex) path(r *reach, id types.ItemID) string {
	var steps []string
	for id != types.NoItem {
		p := r.parent[id]
		if p == types.NoItem {
			steps = append(steps, qx.name(id))
			break
		}
		steps = append(steps, qx.name(id)+" ("+r.site[id].at+")")
		id = p
	}
	slices.Reverse(steps)
	return strings.Join(steps, " -> ")
}

// panicSite is one explicit panic of a body.
type panicSite struct {
	code types.PanicCode
	at   string
}

// checks counts the implicit checks of a body (traps code generation
// inserts): overflow, bounds, divide, shift, cast.
type checks struct{ overflow, bounds, divide, shift, cast int }

func (c *checks) add(o checks) {
	c.overflow += o.overflow
	c.bounds += o.bounds
	c.divide += o.divide
	c.shift += o.shift
	c.cast += o.cast
}

// panicsOf is b's explicit panic sites and implicit checks.
func (qx *queryIndex) panicsOf(b *mir.Body) ([]panicSite, checks) {
	var ps []panicSite
	var ck checks
	tab := qx.w.tab
	isInt := func(o mir.Operand) bool {
		t := b.OperandType(o)
		return t != types.NoType && tab.Get(t).IsInteger()
	}
	for _, bl := range b.Blocks {
		for _, st := range bl.Stmts {
			if st.Kind != mir.StAssign {
				continue
			}
			rv := st.Rvalue
			switch rv.Kind {
			case mir.RvBinary:
				switch rv.Bin {
				case mir.Add, mir.Sub, mir.Mul:
					if isInt(rv.A) {
						ck.overflow++
					}
				case mir.Div, mir.Rem:
					if isInt(rv.A) {
						ck.divide++
					}
				case mir.Shl, mir.Shr:
					ck.shift++
				}
			case mir.RvUnary:
				if rv.Un == mir.Neg && isInt(rv.A) {
					ck.overflow++
				}
			case mir.RvSlice:
				ck.bounds++
			case mir.RvCast:
				if rv.Cast == mir.CastChecked {
					ck.cast++
				}
			}
		}
		if bl.Term.Kind == mir.TermPanic {
			at, _ := qx.at(b, bl.Term.Span)
			ps = append(ps, panicSite{code: bl.Term.Panic, at: at})
		}
	}
	for _, p := range b.Projections {
		if p.Kind == mir.ProjIndex {
			ck.bounds++
		}
	}
	return ps, ck
}

// panics is `panics F`: the explicit panic sites (Assert, Panic, Must,
// contracts, enum tags) of F and of every function F reaches by direct
// calls, F's own first, each with the call path from F (F -> G (site) ->
// H (site)), then the implicit checks counted over the same set.
func (qx *queryIndex) panics(e qEntry) []string {
	if !qx.isFunc(e) {
		return []string{e.name + " is not a function"}
	}
	r, _ := qx.walk(e.id, nil)
	var sites, stdSites []string
	var ck checks
	withBody := 0
	for _, id := range r.order {
		b := qx.bodyOf(id)
		if b == nil {
			continue
		}
		withBody++
		ps, c := qx.panicsOf(b)
		ck.add(c)
		for _, p := range ps {
			// Each site with its code and the call path from F.
			l := p.code.String() + "  " + p.at + "  in " + qx.path(r, id)
			if strings.HasPrefix(p.at, "std/") {
				stdSites = append(stdSites, l)
			} else {
				sites = append(sites, l)
			}
		}
	}
	n := len(sites) + len(stdSites)
	sites = append(sites, stdSites...)
	out := []string{e.name + " reaches " + itoaQ(withBody) + " function(s) with a body by direct calls; " +
		itoaQ(n) + " explicit panic site(s), the package's first"}
	if ck != (checks{}) {
		out = append(out, "checks that trap: overflow "+itoaQ(ck.overflow)+", bounds "+itoaQ(ck.bounds)+
			", divide "+itoaQ(ck.divide)+", shift "+itoaQ(ck.shift)+", cast "+itoaQ(ck.cast))
	}
	if n := len(r.indirect); n > 0 {
		out = append(out, itoaQ(n)+" indirect call(s) not followed, first "+r.indirect[0].at)
	}
	if r.cut {
		out = append(out, "walk stopped at "+itoaQ(maxReach)+" functions")
	}
	return append(out, sites...)
}

// allocRoot reports whether id allocates by itself: a method of std's Arena
// or Heap whose error set holds OutOfMemory (every allocation in Mk can
// fail with it).
func (qx *queryIndex) allocRoot(id types.ItemID) bool {
	it := qx.w.items.Get(id)
	if qx.w.items.Module(it.Module).Path != preludeModule {
		return false
	}
	n := qx.displayName(it)
	if !strings.HasPrefix(n, "Arena.") && !strings.HasPrefix(n, "Heap.") {
		return false
	}
	return qx.failsOOM(id)
}

// failsOOM reports whether id's error set names OutOfMemory.
func (qx *queryIndex) failsOOM(id types.ItemID) bool {
	s := qx.funcSig(id)
	if s == nil {
		return false
	}
	for _, m := range qx.w.tab.Members(s.Errors) {
		if !m.IsParam && qx.str(qx.w.items.Get(m.Item).Name) == "OutOfMemory" {
			return true
		}
	}
	return false
}

// allocs is `allocs F`: the shortest chain of direct calls from F to an
// allocation (allocRoot), or why there is none.
func (qx *queryIndex) allocs(e qEntry) []string {
	if !qx.isFunc(e) {
		return []string{e.name + " is not a function"}
	}
	if qx.allocRoot(e.id) {
		return []string{e.name + " allocates (an Arena or Heap method that can fail with OutOfMemory)"}
	}
	r, hit := qx.walk(e.id, qx.allocRoot)
	if hit != types.NoItem {
		return []string{e.name + " allocates: " + qx.path(r, hit)}
	}
	out := []string{"none: no chain of direct calls from " + e.name + " reaches an Arena or Heap allocation (" +
		itoaQ(len(r.order)) + " functions walked)"}
	if n := len(r.indirect); n > 0 {
		out = append(out, itoaQ(n)+" indirect call(s) not followed, first "+r.indirect[0].at)
	}
	if qx.failsOOM(e.id) {
		out = append(out, "OutOfMemory is in "+e.name+"'s error set")
	}
	return out
}

// stack is `stack F`: a recursion reachable from F that no depth parameter
// bounds (no static bound), an indirect call or a walk cut at maxReach (no
// static bound either), or the deepest chain of direct calls, each
// recursion a depth parameter bounds (query_depth.go) counted at its
// bound. Frame sizes exist only after code generation, so the bound is
// stated in calls, not bytes.
func (qx *queryIndex) stack(e qEntry) []string {
	if !qx.isFunc(e) {
		return []string{e.name + " is not a function"}
	}
	r, _ := qx.walk(e.id, nil)
	if r.cut {
		// The functions past the cut may recurse or go deeper.
		return []string{"no static bound: the walk stopped at " + itoaQ(maxReach) + " functions"}
	}
	comps, comp := qx.sccs(r.order)
	bounds := map[int]*recBound{}
	for i, c := range comps {
		if qx.recursive(c) {
			if b := qx.depthBound(c); b != nil {
				bounds[i] = b
			}
		}
	}
	inBound := func(from, to types.ItemID) bool {
		cf, ok1 := comp[from]
		ct, ok2 := comp[to]
		return ok1 && ok2 && cf == ct && bounds[cf] != nil
	}
	if cyc := qx.cycle(e.id, inBound); cyc != "" {
		return []string{"no static bound: recursion " + cyc}
	}
	// The deepest chain over the components: a bounded recursion counts its
	// bound, any other function one frame.
	depth := map[int]int{}
	next := map[int]types.ItemID{}
	var deep func(int) int
	deep = func(ci int) int {
		if d, ok := depth[ci]; ok {
			return d
		}
		best, via := 0, types.NoItem
		for _, f := range comps[ci] {
			for _, c := range qx.callsOf(f) {
				cc, ok := comp[c.callee] // an indirect call's NoItem is no node
				if c.indirect || !ok || cc == ci {
					continue
				}
				if d := deep(cc); d > best {
					best, via = d, c.callee
				}
			}
		}
		self := 1
		if b := bounds[ci]; b != nil {
			self = b.frames
		}
		depth[ci], next[ci] = best+self, via
		return best + self
	}
	d := deep(comp[e.id])
	var chain []string
	frames := 0
	for id := e.id; id != types.NoItem && len(chain) < 8; id = next[comp[id]] {
		if b := bounds[comp[id]]; b != nil {
			chain = append(chain, qx.name(id)+" (depth-bounded, "+itoaQ(b.frames)+" frames)")
			frames += b.frames
		} else {
			chain = append(chain, qx.name(id))
			frames++
		}
	}
	if d > frames {
		chain = append(chain, "...")
	}
	var out []string
	if n := len(r.indirect); n > 0 {
		out = append(out, "no static bound: "+itoaQ(n)+" indirect call(s), first "+r.indirect[0].at)
	} else if len(bounds) == 0 {
		out = append(out, "bounded: no recursion and no indirect call in the "+itoaQ(len(r.order))+" function(s) "+e.name+" reaches")
	} else {
		out = append(out, "bounded: no indirect call in the "+itoaQ(len(r.order))+" function(s) "+e.name+
			" reaches, and a depth parameter bounds each of its "+itoaQ(len(bounds))+" recursion(s)")
	}
	out = append(out, "deepest direct chain: "+itoaQ(d)+" frame(s): "+strings.Join(chain, " -> "),
		"frame sizes are known after code generation (mk build); the bound here is in calls")
	// The bounded recursions last, in the order they are reached: the
	// answer's cap may cut them, never the chain.
	for i := len(comps) - 1; i >= 0; i-- {
		if b := bounds[i]; b != nil {
			out = append(out, "depth-bounded recursion "+qx.cycleNames(b.funcs)+": "+b.param+" ("+b.ty+
				") falls around every cycle, at most "+itoaQ(b.frames)+" frame(s)")
		}
	}
	return out
}

// cycle is a recursion reachable from root as "A -> B -> A", or "". It
// leaves out the calls skip names.
func (qx *queryIndex) cycle(root types.ItemID, skip func(from, to types.ItemID) bool) string {
	const (
		white = iota
		grey
		black
	)
	color := map[types.ItemID]int{}
	var stack []types.ItemID
	var found []types.ItemID
	var visit func(types.ItemID) bool
	visit = func(id types.ItemID) bool {
		if len(color) >= maxReach {
			return false
		}
		color[id] = grey
		stack = append(stack, id)
		for _, c := range qx.callsOf(id) {
			if c.indirect || skip != nil && skip(id, c.callee) {
				continue
			}
			switch color[c.callee] {
			case grey:
				found = append(slices.Clone(stack), c.callee)
				return true
			case white:
				if visit(c.callee) {
					return true
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[id] = black
		return false
	}
	if !visit(root) {
		return ""
	}
	// found is root ... X ... X: the path to the cycle, then the cycle.
	last := found[len(found)-1]
	i := slices.Index(found, last)
	names := func(ids []types.ItemID) string {
		ns := make([]string, len(ids))
		for k, id := range ids {
			ns[k] = qx.name(id)
		}
		return strings.Join(ns, " -> ")
	}
	if i == 0 {
		return names(found)
	}
	return names(found[i:]) + ", reached by " + names(found[:i+1])
}
