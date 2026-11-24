// Why: the dataflow works over a finite universe of roots and loans; all of
// them, and the facts each needs, are fixed once per body before the first
// transfer runs.
//
// What: roots (rootID, rootKind, rootInfo), loans (loanID, loan), carrier
// parameters' abstract parts (paramInfo), setup, localRoot, static and
// global nodes (staticOf, globalNode, staticTakesStores, resultGlobal),
// argument slots (addSlot), tagged inner loans and entryState.
//
// How: setup computes root facts, the abstract nodes and loans of every
// carrier parameter, the pre-scan of borrow sites, static nodes and argument
// slots, allocating loans in a fixed order (the body's order of borrow
// sites), so loan IDs and diagnostics are deterministic.

package borrow

import (
	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

type rootID uint32

const noRoot rootID = ^rootID(0)

type rootKind uint8

const (
	rkReturn rootKind = iota
	rkParam
	rkUser
	rkTemp
	rkScope
	rkHolder
	rkAbstract
	rkStatic
	rkSlot
)

// rootInfo is computed once per root in setup.
type rootInfo struct {
	kind  rootKind
	local mir.LocalID
	param types.ParamIndex
	// depth is 1 for an abstract node p#1, 2 for p#2 and for p#A (a deep
	// node: a store into it is judged as at p#2).
	depth uint8
	typ   types.TypeID
	item  types.ItemID // a static node's global or const

	carrier, plain, linear bool
	// byValueParam is a by-value parameter; isVar a var user local or
	// parameter.
	byValueParam, isVar bool
	// writable is writableByType; view holds a view; allocator is exactly
	// Arena, Alloc or Heap, and for an abstract node that it stands for
	// allocators alone.
	writable, view, allocator bool
	// walk: the direct write rule walks on from this root that is no view
	// (walker.walkExtra).
	walk bool
	// nfields is how many per-field held sets the root may keep (fields.go):
	// a frame root's split shape; 0 for every other root.
	nfields int
	// fcar marks the loan-holding fields of the split shape, and felem is
	// the root's first field element of liveness (locals.go splitInfo).
	fcar  []bool
	felem uint32
	// rest is true when the split shape is a view's: its last set is the
	// view's rest, and a key on a field that holds no loans reads the whole
	// value (fields.go shapeOf and keyOf).
	rest bool
	// takes is set on an abstract node when a value of a type it stands for
	// may point into global memory that takes stores (storesFirst, as
	// written): a static of such memory stored into it is E0604. On a named
	// static node it is set when the global's memory takes stores at some
	// level (staticOf).
	takes bool
	// cut is, on an abstract node whose takes comes only from a referent
	// its layer-2 walk stopped at (cutTakes), the bound it stopped at
	// (cutReason), which E0604 names (boundAlt); the zero cutReason
	// otherwise.
	cut cutReason
}

// frame reports whether r is storage in this function's frame: a
// local, parameter, temporary, scope or loop holder.
func (ri *rootInfo) frame() bool { return ri.kind <= rkHolder }

func (ri *rootInfo) isLocal() bool { return ri.kind <= rkHolder }

type loanID uint32

const noLoan loanID = ^loanID(0)

// loan is one loan of the body's finite universe.
type loan struct {
	excl bool
	// place is noPath for abstract, tagged and static loans.
	place pathID
	// target is noRoot for a loan made through a deref.
	target rootID
	site   mir.SpanID
	// Abstract loans: the parameter, outer or inner; a tagged inner loan's
	// field path after the first deref, and its base. noAlloc is set on a
	// tagged inner loan whose field type reaches no allocator (reachesAlloc),
	// so hopping through it onto p#2 never jumps to p#A.
	param    types.ParamIndex
	abstract bool
	inner    bool
	noAlloc  bool
	tag      []uint32
	base     loanID
	// Loop loans: a for loop's holder and element loans.
	loop    mir.LoopID
	holder  bool
	element bool
	static  bool
	slot    bool
}

// paramInfo is a carrier parameter's abstract part. nA is p#A, the
// node of the allocators the parameter reaches below its first level, or
// noRoot when it reaches none. No loan of its own targets p#A: a
// tagged inner loan whose field ends at an allocator reference does (tagged).
// n1A is the same split at the first level: the allocators among p#1's
// types when p#1 also stands for something else (a by-value parameter such
// as c: Ctx { a: &Arena, name: str }), else noRoot; p#1 keeps the rest. A
// read of an allocator field of the parameter's own value yields outerA,
// p.outer with n1A as its target (outerAlloc).
type paramInfo struct {
	carrier      bool
	n1, n2, nA   rootID
	n1A          rootID
	outer, inner loanID
	outerA       loanID
	outerKeep    loanID
	// n2F holds optional per-field depth-2 abstract nodes for aliasing-disjoint
	// reference fields of a &S or mut S parameter (noRoot when field stays on n2).
	n2F      []rootID
	n2FLoan  []loanID
	n2FInner []setID
	// innerSet is {inner}: what every abstract node of the parameter holds
	// at entry, before the body stores into it.
	innerSet setID
	// memNode: the parameter is a reference to one Arena (a: &Arena), p#1
	// stands for that arena alone and p#2 for ANY alone, the contents its
	// view reaches: p#2 is the arena's memory (memHop).
	memNode bool
}

// siteKey names a borrow site: the statement's block and index, or for an
// argument slot the terminator's block and the argument's index (stmt is
// then argBase + index).
type siteKey struct {
	b    mir.BlockID
	stmt uint32
}

const argBase = 1 << 30

func (c *checker) setup() {
	b := c.body
	nl := len(b.Locals)
	c.roots = make([]rootInfo, nl, nl+8)
	for i := range b.Locals {
		c.roots[i] = c.localRoot(mir.LocalID(i))
	}
	// Abstract nodes and parameter loans, in parameter order.
	c.params = make([]paramInfo, b.ParamCount+1)
	c.memParams = false
	for p := 1; p <= int(b.ParamCount); p++ {
		ri := &c.roots[p]
		if !ri.carrier || c.tab.Kind(ri.typ) == types.KFn {
			continue
		}
		n1, n2, nA := c.w.abstractNodes(ri.typ)
		n1, n1A := c.w.splitAllocators(n1)
		pi := &c.params[p]
		pi.carrier = true
		pi.n1 = c.addAbstract(types.ParamIndex(p), 1, &n1)
		pi.n1A, pi.outerA, pi.outerKeep = noRoot, noLoan, noLoan
		if len(n1A.types) > 0 {
			// Depth 1: a first-level node, as the merged p#1 was.
			pi.n1A = c.addAbstract(types.ParamIndex(p), 1, &n1A)
		}
		pi.n2 = c.addAbstract(types.ParamIndex(p), 2, &n2)
		pi.nA = noRoot
		if len(nA.types) > 0 {
			pi.nA = c.addAbstract(types.ParamIndex(p), 2, &nA)
		}
		pi.outer = c.addLoan(loan{excl: n1.excl, place: noPath, target: pi.n1, param: types.ParamIndex(p), abstract: true, base: noLoan, loop: mir.NoLoop})
		pi.inner = c.addLoan(loan{excl: n2.excl, place: noPath, target: pi.n2, param: types.ParamIndex(p), abstract: true, inner: true, base: noLoan, loop: mir.NoLoop})
		pi.innerSet = c.sets.of(uint32(pi.inner))
		if pi.n1A == noRoot && c.roots[pi.n1].allocator && n2.any && len(n2.types) == 0 && c.arenaRef(ri.typ) {
			pi.memNode = true
			c.memParams = true
		}
		if pi.n1A != noRoot {
			pi.outerA = c.addLoan(loan{excl: n1.excl, place: noPath, target: pi.n1A, param: types.ParamIndex(p), abstract: true, base: noLoan, loop: mir.NoLoop})
		}
		c.setupParamFieldNodes(types.ParamIndex(p), pi, ri.typ)
	}
	// Static nodes, in first-seen block order; then borrow sites and slots.
	for bi := range b.Blocks {
		for _, st := range b.Blocks[bi].Stmts {
			if st.Kind != mir.StAssign {
				continue
			}
			rv := &st.Rvalue
			if rv.Kind == mir.RvGlobalRef || rv.Kind == mir.RvConstRef {
				c.staticOf(rv.Item, b.PlaceType(st.Place), rv.Kind == mir.RvGlobalRef)
			}
		}
		if t := &b.Blocks[bi].Term; isCall(t) && c.global.root == noRoot && c.resultGlobal(t, c.peekFlows(t)) {
			c.globalNode()
		}
	}
	for bi := range b.Blocks {
		blk := &b.Blocks[bi]
		for si := range blk.Stmts {
			st := &blk.Stmts[si]
			if st.Kind != mir.StAssign {
				continue
			}
			rv := &st.Rvalue
			if rv.Kind != mir.RvRef && rv.Kind != mir.RvSlice {
				continue
			}
			c.siteLoans[siteKey{mir.BlockID(bi), uint32(si)}] = c.borrowSiteLoan(st)
		}
		if isCall(&blk.Term) {
			args := b.Args[blk.Term.Args.Start:blk.Term.Args.End()]
			for ai, a := range args {
				switch {
				case a.Mode == mir.ArgReborrow:
					c.addSlot(mir.BlockID(bi), uint32(ai), a.Operand.Place, blk.Term.Span)
				case a.Operand.Kind == mir.OpMove:
					c.addMoveSlot(mir.BlockID(bi), uint32(ai), a.Operand.Place)
				}
			}
			c.markEndsForms(mir.BlockID(bi))
		}
	}
	c.splitInfo()
}

// markEndsForms records, for a call in block b whose callee ends p, the RvRef
// statement that formed p's argument temporary from a whole local or
// parameter: that borrow is checked with the ends exemption. The callee's
// flows are read for every callee kind, direct, interface or fn value: the
// call clears H(r) through each of them, so each gets the exemption.
func (c *checker) markEndsForms(b mir.BlockID) {
	t := &c.body.Blocks[b].Term
	if t.Spawn != mir.NoLocal {
		return
	}
	fv := c.peekFlows(t)
	if fv == nil || len(fv.ends) == 0 {
		return
	}
	args := c.body.Args[t.Args.Start:t.Args.End()]
	stmts := c.body.Blocks[b].Stmts
	for _, p := range fv.ends {
		i := int(p) - 1
		if i < 0 || i >= len(args) {
			continue
		}
		a := args[i]
		if a.Mode != mir.ArgMove || a.Operand.Place.Proj.Len != 0 || c.body.Locals[a.Operand.Place.Local].Kind != mir.LocalTemp {
			continue
		}
		tl := a.Operand.Place.Local
		for si := len(stmts) - 1; si >= 0; si-- {
			st := &stmts[si]
			if st.Kind != mir.StAssign || st.Place.Proj.Len != 0 || st.Place.Local != tl {
				continue
			}
			if st.Rvalue.Kind == mir.RvRef && st.Rvalue.Place.Proj.Len == 0 {
				c.endsForm[siteKey{b, uint32(si)}] = rootID(st.Rvalue.Place.Local)
			}
			break
		}
	}
}

// localRoot computes the facts of local l.
func (c *checker) localRoot(l mir.LocalID) rootInfo {
	lc := &c.body.Locals[l]
	ri := rootInfo{local: l, typ: lc.Type}
	switch lc.Kind {
	case mir.LocalReturn:
		ri.kind = rkReturn
	case mir.LocalParam:
		ri.kind = rkParam
		ri.param = lc.Param
		ri.byValueParam = c.w.byValueParam(lc.Type)
		if c.env.Sig != nil && int(lc.Param) <= len(c.env.Sig.Params) {
			ri.isVar = c.env.Sig.Params[lc.Param-1].Var
		}
	case mir.LocalUser:
		ri.kind = rkUser
		ri.isVar = lc.Var
	case mir.LocalTemp:
		ri.kind = rkTemp
	case mir.LocalScope:
		ri.kind = rkScope
	case mir.LocalLoopHolder:
		ri.kind = rkHolder
	}
	c.typeFlags(&ri)
	ri.nfields = c.splitFieldCount(&ri)
	return ri
}

func (c *checker) typeFlags(ri *rootInfo) {
	f := c.w.factsOf(ri.typ)
	ri.carrier = f.Carrier
	ri.plain = f.Kind == types.Plain
	ri.linear = f.Kind == types.Linear
	ri.writable = c.w.writableByType(ri.typ)
	ri.view = c.w.holdsView(ri.typ)
	ri.allocator = f.Allocator
	ri.walk = !ri.view && c.w.walkExtra(ri.typ)
}

func (c *checker) addRoot(ri rootInfo) rootID {
	c.roots = append(c.roots, ri)
	return rootID(len(c.roots) - 1)
}

func (c *checker) addAbstract(p types.ParamIndex, depth uint8, n *nodeInfo) rootID {
	carrier, writable, view, alloc := c.w.nodeFlags(n)
	// A referent the walk stopped at counts as its types do (cutTakes).
	takes, walk, cut := n.cutTakes, false, n.cut
	for _, t := range n.types {
		// For every instance (storesFirstGen) when the type mentions a
		// parameter: a generic body's `mut ?T`, `mut ?&T` or `&Cell[?T]` may
		// point into such memory at some instance. A concrete type is judged
		// as written.
		if c.w.takesStores(t) {
			takes, cut = true, cutReason{}
		}
		walk = walk || !view && c.w.walkExtra(t)
	}
	if !n.cutTakes {
		cut = cutReason{}
	}
	return c.addRoot(rootInfo{kind: rkAbstract, local: mir.NoLocal, param: p, depth: depth, typ: types.NoType,
		carrier: carrier, writable: writable, view: view, allocator: alloc, takes: takes, walk: walk, cut: cut})
}

func (c *checker) addLoan(l loan) loanID {
	c.loans = append(c.loans, l)
	return loanID(len(c.loans) - 1)
}

// staticOf returns the static node and loan of global or const item, made at
// its first sight. refType is the type of the reference (or, for a
// const value, of the value).
func (c *checker) staticOf(item types.ItemID, refType types.TypeID, isRef bool) (rootID, loanID) {
	if s, ok := c.statics[item]; ok {
		return s.root, s.loan
	}
	nodeType := refType
	if isRef {
		if ty := c.tab.Get(refType); ty.Kind == types.KRef || ty.Kind == types.KMut {
			nodeType = ty.Elem
		}
	}
	ri := rootInfo{kind: rkStatic, local: mir.NoLocal, typ: nodeType, item: item}
	c.typeFlags(&ri)
	// A named global takes stores when its first level does (writable by type)
	// or when a reference in its bytes reaches, at any level, memory that does
	// (storesFirst behind a shared reference, since a global or const is only
	// ever accessed through `&` or copied when plain): for example a const GB
	// of type `Box { m: &Mutex[?&i32] }`, whose Mutex its initializer allocated
	// in EvalArena. Such a node counts as writable, so the call rule's writable
	// walk lands on it and a store of a frame's loan through `(&GB).m`'s guard
	// is E0605 there, as through a call's result, rather than resting on
	// EvalArena's read-only emission. takes is read by staticTakesStores and by
	// the boundary (boundaryStatic), so an impl that returns &GB under a
	// sourced interface method is refused at the impl.
	ri.takes = ri.carrier && (ri.writable || c.w.storesFirst(c.tab.Ref(nodeType)))
	// A const whose value is settled is judged on that value, not by its type
	// alone (constvalue.go): DefaultClientLimits' `proxy: ?&url.Url` is none,
	// so a copy of it returned under a source is no static that takes stores,
	// while a const like GB above, whose value points at the Mutex its
	// initializer allocated, still is. The judgement is the same whether the
	// const is first seen as a value or behind `&`, since the node is made
	// once per body: a const whose own bytes are writable by type keeps
	// counting (otherwise `p := &DefaultClientLimits` before a copy of it
	// would be E0602 while the copy alone was not).
	if ri.takes {
		if v, ok := c.constValue(item); ok {
			ri.takes = ri.writable || c.w.constTakes(v)
		}
	}
	ri.writable = ri.writable || ri.takes
	r := c.addRoot(ri)
	l := c.addLoan(loan{place: noPath, target: r, static: true, base: noLoan, loop: mir.NoLoop})
	c.statics[item] = staticNode{root: r, loan: l}
	return r, l
}

type staticNode struct {
	root rootID
	loan loanID
}

// globalNode is the node for global memory that a call hands back: one per
// body, made in setup when some call needs it (resultGlobal). It stands for
// every global a result with no source may point into (only statics qualify),
// so it is a carrier that takes stores. Like a named global's node it holds
// nothing (setHeld): a reference copied out of global memory keeps the loan it
// was read through. A store of any loan but a static one into it is E0605, as
// into a named global (storeInto).
func (c *checker) globalNode() staticNode {
	if c.global.root != noRoot {
		return c.global
	}
	r := c.addRoot(rootInfo{kind: rkStatic, local: mir.NoLocal, typ: types.NoType, item: types.NoItem, carrier: true, writable: true})
	l := c.addLoan(loan{place: noPath, target: r, static: true, base: noLoan, loop: mir.NoLoop})
	c.global = staticNode{root: r, loan: l}
	return c.global
}

// staticTakesStores reports whether static node r is global memory that
// takes stores: a named global of a carrier type that takes stores at some
// level (rootInfo.takes, set in staticOf), or the global node.
func (c *checker) staticTakesStores(r rootID) bool {
	if r == noRoot {
		return false
	}
	ri := &c.roots[r]
	return ri.kind == rkStatic && (r == c.global.root || ri.takes)
}

// resultGlobal reports whether call t's result may point into global memory
// its caller cannot see: the result is a carrier with a reference to a type
// that takes stores (storesFirst), and either the callee's signature names no
// source, so only statics qualify, or the callee is not known by name (a fn
// value or an interface method), whose declared sources may stand for a
// function that names none (E0608 lets a function with fewer flows be given as
// a fn type or an impl method). A direct callee that names a source may return
// no such static: its boundary refuses one (boundaryStatic). A callee with a
// raw-pointer parameter is the raw-pointer case: a reference made from the
// pointer holds nothing and its author ties it down (std's SpinRef, ListAt and
// the like), so its result holds nothing here either, and its boundary refuses
// such a static as a sourced one does. The exemption is a direct callee's
// only: a fn value or an interface method stands for a function judged at its
// own parameter types, which may hold no raw pointer.
//
// In a generic body a result is judged for every instance as well: an
// untrusted call's result whose type mentions a type parameter may point
// into global memory at some instance, so it holds the global node, and the
// body's boundary refuses it under a source (boundaryStatic, judged with
// storesFirstGen), as it refuses a static that a narrowed branch
// (`if T is X`) returns. A fn value, with or without a source in its type,
// and a source-less direct callee are judged with storesFirstGen: `&T`,
// `?&T` and `T` count. A sourced fn type one of whose parameters holds the
// result's type is trusted (fnResultSupplied: Twice's `fn(T): T`), and a
// sourced fn value's or a direct callee's concrete result is judged on its
// written type (storesFirstBare), so only a type that mentions a parameter
// meets the generic rules. An interface method is judged with
// storesFirstBare, a bare `T`, and its `&U` and `[]U` are judged at the
// impl instead: an impl method whose interface method names a source has
// its boundary judged as a sourced one (boundaryStatic, implSourced), so
// std's default methods may store through ItemsMut's result
// (Slice[T].Fill), whose impls name self.
func (c *checker) resultGlobal(t *mir.Terminator, fv *flowView) bool {
	if fv == nil || t.Spawn != mir.NoLocal {
		return false
	}
	direct := t.Callee.Kind == mir.CallDirect
	if direct && (len(fv.sources) > 0 || len(fv.fields) > 0 || c.w.rawParam(fv.paramTypes)) {
		return false
	}
	dt := c.body.PlaceType(t.Dest)
	if !c.w.carrier(dt) {
		return false
	}
	if c.w.storesFirst(dt) {
		return true
	}
	if t.Callee.Kind == mir.CallInterface && namesSource(fv.sources, fv.fields) {
		// A sourced interface method's bare T is judged
		// at the impl, as its &U and []U are (implSourced reads the same
		// signature through namesSource).
		return false
	}
	if t.Callee.Kind == mir.CallInterface {
		return c.w.storesFirstBare(dt)
	}
	if t.Callee.Kind == mir.CallValue && (len(fv.sources) > 0 || len(fv.fields) > 0) && c.w.fnResultSupplied(c.body.OperandType(t.Callee.Value)) {
		return false
	}
	if (direct || len(fv.sources) > 0 || len(fv.fields) > 0) && !c.w.paramFrom(dt, 0) {
		// A concrete result is judged on its written type.
		return direct && c.w.storesFirstBare(dt)
	}
	return c.w.storesFirstGen(dt)
}

// borrowSiteLoan allocates the loan of an RvRef or RvSlice statement.
// Its place is the borrowed path (for a slice of a mut []T, the path through
// the exclusive deref of the slice value); its target the root when the path
// has no deref, else none.
func (c *checker) borrowSiteLoan(st *mir.Statement) loanID {
	rv := &st.Rvalue
	p := c.paths.ofPlace(c.body, c.tab, rv.Place)
	if rv.Kind == mir.RvSlice {
		ty := c.paths.get(p).typ()
		switch c.tab.Kind(ty) {
		case types.KMutSlice:
			p = c.paths.extend(p, step{kind: stDeref, mode: dmExcl}, ty)
		case types.KSlice, types.KStr:
			return noLoan // a slice of a slice value makes no loan
		}
	}
	pi := c.paths.get(p)
	target := noRoot
	if pi.derefs == 0 {
		target = pi.root
	}
	return c.addLoan(loan{excl: rv.Borrow == mir.Exclusive, place: p, target: target, site: st.Span, base: noLoan,
		loop: rv.Loop, holder: rv.Origin == mir.OriginLoopHolder, element: rv.Origin == mir.OriginLoopElement})
}

// addSlot makes the argument slot and the reborrow loan of an ArgReborrow
// argument: an exclusive loan on place x.* with no target.
func (c *checker) addSlot(b mir.BlockID, ai uint32, x mir.Place, span mir.SpanID) {
	p := c.paths.ofPlace(c.body, c.tab, x)
	ty := c.paths.get(p).typ()
	mode := modeOf(c.tab, ty)
	if k := c.tab.Kind(ty); k == types.KMutSlice {
		mode = dmExcl
	}
	// The deref step's type is the referent's for a reference (so x.* is the
	// same path, with the same type, as the place *x of the body), and the
	// slice type itself for a mut []T, as ofPlace spells an index's deref.
	after := ty
	if k := c.tab.Kind(ty); k == types.KMut || k == types.KRef {
		after = c.tab.Get(ty).Elem
	}
	dp := c.paths.extend(p, step{kind: stDeref, mode: mode}, after)
	ri := rootInfo{kind: rkSlot, local: mir.NoLocal, typ: ty}
	c.typeFlags(&ri)
	r := c.addRoot(ri)
	l := c.addLoan(loan{excl: true, place: dp, target: noRoot, site: span, base: noLoan, loop: mir.NoLoop, slot: true})
	c.slots[siteKey{b, argBase + ai}] = slotInfo{root: r, loan: l, path: dp}
}

// addMoveSlot makes the argument slot for an OpMove argument of a carrier
// type: moving the operand in step 1 empties the source local, so the slot
// keeps its loans live for every later argument (including reborrows) of the
// same call until endSlots clears it.
func (c *checker) addMoveSlot(b mir.BlockID, ai uint32, x mir.Place) {
	ty := c.body.PlaceType(x)
	if !c.w.carrier(ty) {
		return
	}
	ri := rootInfo{kind: rkSlot, local: x.Local, typ: ty}
	c.typeFlags(&ri)
	r := c.addRoot(ri)
	c.slots[siteKey{b, argBase + ai}] = slotInfo{root: r, loan: noLoan}
}

type slotInfo struct {
	root rootID
	loan loanID
	path pathID
}

// tagged returns the tagged inner loan of parameter p for a field path,
// interned: finite, one per (parameter, path, alloc, shared, noAlloc). With
// alloc (the path ends at a field whose references all refer to allocators,
// allocRefs) the loan targets p#A, not p#2: a loan read from an allocator
// field stays with the allocator's node. With shared (a read-only &T field
// of a mut R hidden environment), the loan is non-exclusive even when another
// capture in the same environment is mutable. With noAlloc (the field type
// reaches no allocator, reachesAlloc), the loan stays off p#A. It is otherwise
// the tagged p.inner it always was (base, tag, flows), so every rule that reads
// a tagged inner loan reads it unchanged.
func (c *checker) tagged(p types.ParamIndex, path []uint32, alloc, shared, noAlloc, allowN2F bool) loanID {
	if len(path) == 0 {
		return c.params[p].inner
	}
	alloc = alloc && c.params[p].nA != noRoot
	if alloc {
		noAlloc = false
	}
	useN2F := !alloc && allowN2F && len(path) > 0 && int(path[0]) < len(c.params[p].n2F) && c.params[p].n2F[path[0]] != noRoot
	key := tagKey{p: p, path: pathKey(path), alloc: alloc, shared: shared, noAlloc: noAlloc, allowN2F: useN2F}
	if l, ok := c.tags[key]; ok {
		return l
	}
	base := c.params[p].inner
	bl := c.loans[base]
	target := bl.target
	excl := bl.excl && !shared
	if alloc {
		target = c.params[p].nA
	} else if useN2F {
		nf := c.params[p].n2F[path[0]]
		nl := c.params[p].n2FLoan[path[0]]
		if len(path) == 1 && excl == c.loans[nl].excl && noAlloc == c.loans[nl].noAlloc {
			c.tags[key] = nl
			return nl
		}
		target = nf
	}
	l := c.addLoan(loan{excl: excl, place: noPath, target: target, param: p, abstract: true, inner: true, noAlloc: noAlloc,
		tag: append([]uint32(nil), path...), base: base, loop: mir.NoLoop})
	c.tags[key] = l
	return l
}

// setupParamFieldNodes allocates aliasing-disjoint depth-2 abstract nodes n2F[i]
// for top-level carrier reference fields of a concrete non-view `&S` or `mut S`
// parameter: `mut` reference/slice fields (exclusive, cannot alias) and
// shared non-writable, non-store-taking struct/slice/str fields.
func (c *checker) setupParamFieldNodes(p types.ParamIndex, pi *paramInfo, paramType types.TypeID) {
	if c.hiddenParam(p) || c.mentionsParam(paramType) || c.tab.Items() == nil {
		return
	}
	ty := c.tab.Get(paramType)
	if ty.Kind != types.KRef && ty.Kind != types.KMut {
		return
	}
	st := c.tab.Get(ty.Elem)
	if st.Kind != types.KNamed || st.Item == types.NoItem {
		return
	}
	d, isStruct := c.tab.Items().Get(st.Item).Decl.(*types.StructDecl)
	if !isStruct || d.View || len(d.Fields) == 0 {
		return
	}
	// Record ReadShape/ReadFields on the struct declaration for incremental caching.
	_ = c.w.factsOf(ty.Elem)
	_ = c.w.inlineFields(ty.Elem)
	for i, fd := range d.Fields {
		ft := c.w.subst(fd.Type, st.Args)
		if !c.w.carrier(ft) || c.w.allocRefs(ft) {
			continue
		}
		fk := c.tab.Get(ft)
		eligible := false
		isMut := false
		switch fk.Kind {
		case types.KMut, types.KMutSlice:
			eligible = true
			isMut = true
		case types.KRef, types.KSlice, types.KStr:
			elem := fk.Elem
			if fk.Kind == types.KStr {
				elem = types.U8
			}
			// Keep scalar non-carrier shared refs (&i32) on p#2 while isolating
			// struct/slice/str referents that cannot alias any writable field.
			if !c.w.writableByType(elem) && !c.w.takesStores(elem) &&
				!(fk.Kind == types.KRef && !c.w.carrier(elem) && c.tab.Kind(elem) != types.KNamed) {
				eligible = true
			}
		}
		if !eligible {
			continue
		}
		fn1, fn2, _ := c.w.abstractNodes(ft)
		if len(fn1.types) == 0 && !fn1.any {
			continue
		}
		// Keep fields whose deeper referents include interior-writable storage
		// (e.g. &Cell/&Mutex) or unbounded recursion on p#2, because distinct
		// fields of the caller's struct could share the same backing Cell.
		if _, fn2Writable, _, _ := c.w.nodeFlags(&fn2); fn2.any || fn2Writable {
			continue
		}
		if pi.n2F == nil {
			n := len(d.Fields)
			pi.n2F = make([]rootID, n)
			pi.n2FLoan = make([]loanID, n)
			pi.n2FInner = make([]setID, n)
			for j := range n {
				pi.n2F[j] = noRoot
				pi.n2FLoan[j] = noLoan
			}
		}
		combined := fn1
		combined.excl = combined.excl || fn2.excl
		combined.cutTakes = combined.cutTakes || fn2.cutTakes
		if !combined.cut.stopped() && fn2.cut.stopped() {
			combined.cut = fn2.cut
		}
		for _, dt := range fn2.types {
			combined.addType(dt)
		}
		nf := c.addAbstract(p, 2, &combined)
		nl := c.addLoan(loan{
			excl:     isMut && ty.Kind == types.KMut,
			place:    noPath,
			target:   nf,
			param:    p,
			abstract: true,
			inner:    true,
			noAlloc:  !c.w.reachesAlloc(ft),
			tag:      []uint32{uint32(i)},
			base:     pi.inner,
			loop:     mir.NoLoop,
		})
		pi.n2F[i] = nf
		pi.n2FLoan[i] = nl
		pi.n2FInner[i] = c.sets.of(uint32(nl))
	}
}

func (c *checker) isParamN2Field(p types.ParamIndex, r rootID) bool {
	if r == noRoot || p == types.NoParam || int(p) >= len(c.params) {
		return false
	}
	for _, nf := range c.params[p].n2F {
		if nf == r {
			return true
		}
	}
	return false
}

func (c *checker) isParamN2FieldLoan(p types.ParamIndex, l loanID) bool {
	if l == noLoan || p == types.NoParam || int(p) >= len(c.params) {
		return false
	}
	for _, nl := range c.params[p].n2FLoan {
		if nl == l {
			return true
		}
	}
	return false
}

func (c *checker) outerKeep(p types.ParamIndex) loanID {
	pi := &c.params[p]
	if pi.outerKeep != noLoan {
		return pi.outerKeep
	}
	pi.outerKeep = c.addLoan(loan{
		excl:     false,
		place:    noPath,
		target:   noRoot,
		param:    p,
		abstract: true,
		inner:    false,
		base:     pi.outer,
		loop:     mir.NoLoop,
	})
	return pi.outerKeep
}

func (c *checker) hasIsolatedFieldTarget(p types.ParamIndex, s setID) bool {
	pi := &c.params[p]
	seen := false
	for _, m := range c.sets.get(s) {
		lo := &c.loans[loanID(m)]
		if !lo.abstract || !lo.inner || lo.param != p {
			continue
		}
		seen = true
		if c.isParamN2Field(p, lo.target) {
			continue
		}
		if pi.nA != noRoot && lo.target == pi.nA {
			continue
		}
		return false
	}
	return seen
}

type tagKey struct {
	p        types.ParamIndex
	path     string
	alloc    bool
	shared   bool
	noAlloc  bool
	allowN2F bool
}

func pathKey(xs []uint32) string {
	b := make([]byte, 0, 4*len(xs))
	for _, x := range xs {
		b = append(b, byte(x), byte(x>>8), byte(x>>16), byte(x>>24))
	}
	return string(b)
}

// entryState builds the body's entry state: every parameter {I} holding
// {p.outer} if a carrier; a void local 0 {I}; every other root absent.
func (c *checker) entryState() *state {
	st := newState(len(c.roots))
	for p := 1; p <= int(c.body.ParamCount); p++ {
		e := entry{init: initI}
		if c.params[p].carrier {
			e.held = c.sets.of(uint32(c.params[p].outer))
			e.fv = c.splitEntry(rootID(p), e.held) // a by-value parameter starts split
		}
		c.put(st, rootID(p), e)
	}
	if c.body.Locals[0].Type == types.Void {
		c.put(st, 0, entry{init: initI})
	}
	// Holders of the parameters' own loans: none (abstract loans have no
	// place).
	return st
}

// ice raises an internal error at the body's span.
func (c *checker) ice(format string, args ...any) {
	source.ICEf(c.body.Spans[0], "borrow: "+format, args...)
}
