// Why: a call is where loans cross function boundaries: the callee's
// signature (its `into` and `ends` clauses) says what each argument stores
// into another and what the result holds, and the caller must apply that
// without reading the callee's body.
//
// What: terminator (the transfer of every terminator), switchOuts and
// narrow (variant narrowing on switch edges), call (the call rule), storeArg
// and spawnStores (the stores a call or a spawn makes), endsArgs and the
// ends exemption, and remnantReach.
//
// How: the call rule runs in six numbered steps: (1) copies and moves, then
// reborrows, into argument slots, (2) the arguments' shallow marks
// (marks.go), (3) `x into p` and (4) `*q into p` repeated to a fixpoint, (5)
// the result, then (6) ends. A fallible call restores by-value arguments on its
// error edge, since it consumes them only on success.

package borrow

import (
	"slices"

	"mk/internal/mir"
	"mk/internal/types"
)

// terminator is the transfer of a block's terminator: the out-state of each
// successor edge, each its own state.
func (c *checker) terminator(st *state, ctx *stmtCtx, t *mir.Terminator) []out {
	switch t.Kind {
	case mir.TermGoto:
		return []out{{t.Target, st}}
	case mir.TermSwitch:
		c.operand(st, ctx, t.On)
		return c.switchOuts(st, t)
	case mir.TermCall:
		return c.call(st, ctx, t)
	case mir.TermTailCall:
		c.tailCall(st, ctx, t)
	case mir.TermTypeTest:
		// Both branches are checked with the same state.
		return []out{{t.Yes, st.clone()}, {t.No, st.clone()}}
	case mir.TermOnerrGuard:
		in := initOf(st.get(rootID(t.Value)))
		if c.report {
			c.guards = append(c.guards, OnerrRun{Block: ctx.b, Run: in&initI != 0})
		}
		switch {
		case in&initI == 0:
			return []out{{t.No, st}}
		case in == initI:
			return []out{{t.Yes, st}}
		}
		return []out{{t.Yes, st.clone()}, {t.No, st.clone()}}
	case mir.TermReturn:
		c.ret(st, ctx, t)
	case mir.TermPanic:
		if t.HasMsg {
			c.operand(st, ctx, t.Msg)
		}
	}
	return nil
}

// switchOuts gives each switch edge its state, narrowing a taken or tested
// temporary on edges whose variants all have plain payloads.
func (c *checker) switchOuts(st *state, t *mir.Terminator) []out {
	cases := c.body.Cases[t.Cases.Start:t.Cases.End()]
	var np pathID = noPath
	if t.On.Kind == mir.OpCopy && t.On.Place.Proj.Len == 0 {
		if pl, ok := c.lastDiscr[t.On.Place.Local]; ok {
			p := c.paths.ofPlace(c.body, c.tab, pl)
			if pi := c.paths.get(p); c.roots[pi.root].kind == rkTemp && pi.onlyFieldSteps() {
				np = p
			}
		}
	}
	var all []uint64
	for _, cs := range cases {
		all = append(all, cs.Value)
	}
	outs := make([]out, 0, len(cases)+1)
	for _, cs := range cases {
		o := st.clone()
		if np != noPath {
			c.narrow(o, np, func(d uint64) bool { return d == cs.Value })
		}
		outs = append(outs, out{cs.Target, o})
	}
	if t.Default != mir.NoBlock {
		o := st.clone()
		if np != noPath {
			c.narrow(o, np, func(d uint64) bool { return !slices.Contains(all, d) })
		}
		outs = append(outs, out{t.Default, o})
	}
	return outs
}

// narrow records that the temporary of path p holds nothing linear at p on
// an edge whose selected variants all have only plain payload fields,
// keeping its held set, when it holds its value on every path
// (consumePart).
func (c *checker) narrow(st *state, p pathID, selected func(uint64) bool) {
	pi := c.paths.get(p)
	vs := c.variantsOf(pi.typ())
	if vs == nil {
		return
	}
	for _, v := range vs {
		if selected(v.discr) && !v.plain {
			return
		}
	}
	if initOf(st.get(pi.root))&(initU|initM) != 0 {
		return
	}
	c.consumePart(st, pi.root, p, false)
}

type variantFact struct {
	discr uint64
	plain bool
}

// variantsOf lists the logical variants of a tested type with whether each
// payload is plain: ?T (none 0, some 1), E!T (ok 0, each error by its code,
// error payloads plain), an enum by its discriminants. Nil for any
// other type.
func (c *checker) variantsOf(t types.TypeID) []variantFact {
	ty := c.tab.Get(t)
	switch ty.Kind {
	case types.KOptional:
		return []variantFact{{0, true}, {1, c.w.plain(ty.Elem)}}
	case types.KErrorUnion:
		out := []variantFact{{0, ty.Elem == types.Void || c.w.plain(ty.Elem)}}
		for _, m := range c.tab.Members(ty.Errors) {
			if m.IsParam {
				continue
			}
			code := uint64(types.ErrorCode(c.tab.Items().Get(m.Item).Key))
			if d, ok := c.tab.Items().Get(m.Item).Decl.(*types.ErrorDecl); ok && d.Code != 0 {
				code = uint64(d.Code)
			}
			out = append(out, variantFact{code, true})
		}
		return out
	case types.KNamed:
		d, ok := c.tab.Items().Get(ty.Item).Decl.(*types.EnumDecl)
		if !ok {
			return nil
		}
		var out []variantFact
		for _, v := range d.Variants {
			plain := true
			for _, f := range v.Fields {
				if !c.w.plain(c.w.subst(f.Type, ty.Args)) {
					plain = false
					break
				}
			}
			out = append(out, variantFact{v.Discr, plain})
		}
		return out
	}
	return nil
}

// sharedMutReceiver is E0507 for a mut self receiver passed as a shared
// value: a Slice[T] method with mut self called on a []T, which check passes with no
// Adjust and lower as the []T itself. Every other receiver of a mut self
// method arrives as a mut T or mut []T (a borrow, a reborrow or a move of
// one), so a []T or &T here can only be that call. Its elements are
// reached through the value's shared step.
func (c *checker) sharedMutReceiver(ctx *stmtCtx, a mir.Arg) {
	o := a.Operand
	if o.Kind != mir.OpCopy && o.Kind != mir.OpMove {
		return
	}
	ty := c.body.PlaceType(o.Place)
	switch c.tab.Kind(ty) {
	case types.KSlice, types.KRef:
		p := c.paths.ofPlace(c.body, c.tab, o.Place)
		after := ty
		if c.tab.Kind(ty) == types.KRef {
			after = c.tab.Get(ty).Elem
		}
		c.e0507Mut(ctx, c.paths.extend(p, step{kind: stDeref, mode: dmShared}, after))
	}
}

// call is the call rule.
func (c *checker) call(st *state, ctx *stmtCtx, t *mir.Terminator) []out {
	args := c.body.Args[t.Args.Start:t.Args.End()]
	fv := c.calleeFlows(t)
	if t.Callee.Kind == mir.CallValue {
		c.operand(st, ctx, t.Callee.Value)
	}
	// The roots whose ends the call clears on success.
	endsRoots := c.endsArgs(ctx, t, fv)
	// A fallible call consumes a by-value argument only on success:
	// snapshot the entries of whole locals passed by ArgMove, for the error
	// edge.
	type snap struct {
		i  int
		r  rootID
		e  entry
		ty types.TypeID // the argument's own type, the parameter's under the call's generic arguments
	}
	var snaps []snap
	for i, a := range args {
		if a.Mode == mir.ArgMove && a.Operand.Place.Proj.Len == 0 && a.Operand.Kind == mir.OpMove {
			r := rootID(a.Operand.Place.Local)
			snaps = append(snaps, snap{i, r, st.get(r), c.body.Locals[a.Operand.Place.Local].Type})
		}
	}
	// Step 1: copies and moves in order, then reborrows left to right.
	ctx.slots = emptySet
	argH := make([]setID, len(args))
	for i, a := range args {
		if a.Mode == mir.ArgReborrow {
			continue
		}
		ctx.exempt = c.exemptFor(st, ctx, endsRoots, a)
		argH[i] = c.operand(st, ctx, a.Operand)
		ctx.exempt = nil
		if sl, ok := c.slots[siteKey{ctx.b, argBase + uint32(i)}]; ok && argH[i] != emptySet {
			c.setHeld(st, sl.root, argH[i])
			ctx.slots = c.sets.add(ctx.slots, uint32(sl.root))
		}
		if i == 0 && fv != nil && fv.recvMut {
			c.sharedMutReceiver(ctx, a)
		}
	}
	for i, a := range args {
		if a.Mode != mir.ArgReborrow {
			continue
		}
		sl, ok := c.slots[siteKey{ctx.b, argBase + uint32(i)}]
		if !ok {
			c.ice("no slot for reborrow argument %d", i)
		}
		x := rootID(a.Operand.Place.Local)
		c.useCheck(st, ctx, x, false)
		c.access(st, ctx, sl.path, colMut, nil)
		S := c.sets.add(c.held(st, x), uint32(sl.loan))
		c.setHeld(st, sl.root, S)
		ctx.slots = c.sets.add(ctx.slots, uint32(sl.root))
		argH[i] = S
	}
	if c.tail {
		c.tailArgLoans(ctx, argH)
	}
	if !c.report && c.anySplit && !c.remnant {
		c.remnant = c.remnantReach(st, fv, argH)
	}
	// entryH is every argument's set before steps 3 and 4: step 5 never
	// reads it, the rounds' walk does (own).
	entryH := slices.Clone(argH)
	// What steps 3 and 4 store into each ends root, and the provenance
	// loans a view or allocator root held as its own value before the
	// call: the call keeps both after it ends the rest.
	endsStored := make([]setID, len(endsRoots))
	endsProv := make([]setID, len(endsRoots))
	for i, r := range endsRoots {
		if c.keepEndsProv(r) {
			e := st.get(r)
			endsProv[i] = c.allocSelf(r, c.sets.minus(e.held, e.stored))
		}
	}
	c.flowCall = ctx.b + 1
	if fv != nil {
		// Steps 3 and 4 repeat until a round grows no argument's set and no
		// root's held set: a clause applied early in a round may make a cell
		// reachable that a later clause of the same call fills (`x into l`
		// before `c into l`, or step 3 before step 4's `*q into l`), so x's
		// loans must reach it in the next round. The first round is the plain
		// single pass. A later round storing S into p walks from p's own loans
		// and the loans the call stored into p, but never through a loan of S
		// that this call stored (callStored): what S's own loans reach there
		// is the stored value's own memory, and judging it would make every
		// forwarded parameter (`l.Push(x)` with `x: T into l`) store x into x.
		// A loan of S a root held before the call is walked, as round 0 walks
		// it (see noteCallStored). Each round only adds loans from a finite
		// universe, so the loop ends; a diagnostic a later round repeats has
		// the same code and site, and finish drops it.
		own := entryH
		reads := make([]setID, len(fv.stores))
		readSh := make([]setID, len(fv.stores))
		drops := make([]setID, len(fv.stores))
		dropAt := make([]rootID, len(fv.stores))
		// Step 2's shallow marks (marks.go): argSh[i] is the shallow part of
		// argH[i], from argument i's type as passed; a loan steps 3 and 4
		// add to argH[i] keeps the mark it was stored with (addMarked).
		argSh := make([]setID, len(args))
		for i, a := range args {
			argSh[i] = c.argShallow(a, argH[i])
		}
		c.callStored = nil
		for round := 0; ; round++ {
			grew := false
			// Step 3: x into p, in parameter order.
			for x, p := range fv.into {
				if p == types.NoParam || x >= len(args) || int(p) > len(args) {
					continue
				}
				if c.storeArg(st, ctx, argH, argSh, own, round, int(p)-1, argH[x], argSh[x], endsRoots, endsStored, emptySet, noRoot) {
					grew = true
				}
			}
			// Step 4: *q into p; within a round, every set is read before
			// any is written. A later round reads what q's referents hold
			// now, B(own[q]), plus what this call stored into them; B of the
			// stored loans would read a level deeper (the arena behind a
			// stored &Arena), which the first round already does.
			for i, s := range fv.stores {
				reads[i], readSh[i] = emptySet, emptySet
				if int(s.From) > len(args) || !c.storesLoans(t, fv, s.From) {
					continue
				}
				q := s.From - 1
				// *q of q referring to an allocator is its handle, not
				// its memory: no allocator keep (behindKeep).
				alloc := !c.allocReferent(args[q])
				if round == 0 {
					reads[i] = c.behindKeep(st, argH[q], alloc)
				} else {
					reads[i] = c.sets.unite(c.behindKeep(st, own[q], alloc), c.sets.minus(argH[q], own[q]))
				}
				drops[i], dropAt[i] = emptySet, noRoot
				if !alloc && int(s.Into) <= len(args) {
					// `*a into a`, a and p both references to the same one
					// allocator: what the handle holds is what the allocator
					// holds by its making (its heap's loan, a sub-arena's
					// parent's), and a store into an allocator drops the
					// loans it holds by being itself. The drop applies to the
					// store into that allocator alone (storeArg).
					if r := c.handleOf(own[q]); r != noRoot && r == c.handleOf(own[s.Into-1]) {
						drops[i], dropAt[i] = c.allocMaking(st, r), r
					}
				}
				if c.referentShallow(args[q], reads[i]) {
					// `*q into p` judges q's referent, for what the
					// argument's referents held at the call. A loan this
					// call chained into q (`c into q`), and what its
					// targets hold, keeps the mark it came with: deep,
					// since the clause chain lets the body store c's value
					// itself, whatever q's referent type.
					added := c.sets.minus(argH[q], own[q])
					readSh[i] = c.sets.minus(c.sets.minus(c.sets.meet(reads[i], c.behind(st, own[q])), c.behind(st, added)), added)
				}
			}
			for i, s := range fv.stores {
				if int(s.Into) <= len(args) && c.storeArg(st, ctx, argH, argSh, own, round, int(s.Into)-1, reads[i], readSh[i], endsRoots, endsStored, drops[i], dropAt[i]) {
					grew = true
				}
			}
			if !grew {
				break
			}
		}
	}
	c.flowCall = 0
	newFlows := c.noteTaskFlows(st, ctx, t, fv, entryH)
	if t.Spawn != mir.NoLocal {
		c.spawnStores(st, ctx, t, argH)
		c.closeFlows(st, ctx, newFlows)
		c.endSlots(st, ctx)
		return []out{{t.Target, st}}
	}
	c.closeFlows(st, ctx, newFlows)
	var outs []out
	if t.HasErr {
		es := st.clone()
		for _, s := range snaps {
			if fv == nil || s.i >= len(fv.paramTypes) {
				continue
			}
			// The declared type decides by-value (by its kind); the
			// argument's type decides plain, since a generic callee's
			// parameter type names the callee's own parameters, which are no
			// type of this body (Env.Facts is only total on those).
			if !c.w.byValueParam(fv.paramTypes[s.i]) || c.w.plain(s.ty) {
				continue
			}
			if sl, ok := c.argSlots[mir.LocalID(s.r)]; ok {
				c.restoreSlot(es, sl, s.e) // the slot's source, not the slot
				continue
			}
			c.put(es, s.r, s.e)
		}
		c.endSlots(es, ctx)
		if t.Err.Error != mir.NoLocal {
			c.put(es, rootID(t.Err.Error), entry{init: initI})
		}
		outs = append(outs, out{t.Err.Block, es})
	}
	// Step 5: the result, on the success edge, from argH after steps 3
	// and 4 have reached their fixpoint; with a per-field clause, one set
	// per field (fields.go resultFV).
	res := emptySet
	var resFV fvID
	if fv != nil && c.w.carrier(c.body.PlaceType(t.Dest)) {
		res = c.sourcesSet(st, fv.sources, argH)
		if t.Dest.Proj.Len == 0 {
			resFV = c.resultFV(st, fv, argH, c.body.PlaceType(t.Dest), rootID(t.Dest.Local))
		}
		// A result that may point into global memory holds the
		// global node's static loan, so a store through it is judged as a
		// store into a global (E0605), as `&G` in this body would be. The
		// result is then whole (an indirect callee's per-field clause may
		// stand for a function that names no source for any field).
		if c.global.root != noRoot && c.resultGlobal(t, fv) {
			res = c.sets.add(res, uint32(c.global.loan))
			resFV = 0
		}
	}
	c.endSlots(st, ctx)
	c.destinationFV(st, ctx, t.Dest, res, resFV)
	// Step 6: ends p ends what r held before the call, on the success
	// edge, keeping the provenance loans a view or allocator root held as
	// its own value (Arena.NewIn, Arena.Sub, Fixed.New, FixedHeap.New,
	// Fallback.New). The loans this call stored into r in steps 3 and 4
	// stay too: the callee may store them after it ends the rest, so
	// dropping them would let a borrowed local end while r still refers to
	// it (step 6 runs before steps 3 and 4 in effect, on the graph of the
	// call's entry). The error edge keeps everything (es above).
	for i, r := range endsRoots {
		c.setHeld(st, r, c.sets.unite(endsProv[i], endsStored[i]))
		if stored := c.sets.minus(endsStored[i], endsProv[i]); stored != emptySet {
			e := st.get(r)
			e.stored = c.sets.meet(stored, e.held)
			c.put(st, r, e)
		}
	}
	return append([]out{{t.Target, st}}, outs...)
}

// remnantReach reports whether this call may reach the self-reach remnant: the
// store the self-reach filter cannot see, a forwarding parameter storing one
// referent's loan into another referent of the same level. The callee sees
// each level of an argument's loans as one node (p#1 for what a by-value
// argument holds, p#2 for what its referents hold, and so on), and the
// self-reach filter drops a store of that node's loan into itself, so a
// forwarding parameter may store one referent's loan into another referent of
// the same level unseen by this body. A judged callee rejects that store in
// itself; an unsafe or extern one is held to the same by its signature, and
// this check is the caller's second line of defence there. The call may reach
// it when a loan of some level of an argument that is no `x into p` source
// (the self-reach rule stores those itself) makes its target writable (a mut
// loan, or a target writable by type) and that target does not hold every
// other loan of the level. One set per root may catch the store's effect where
// field sets would hide it, so the body is checked again with one set per root
// (env.go Check).
func (c *checker) remnantReach(st *state, fv *flowView, argH []setID) bool {
	for i, S := range argH {
		if fv != nil && i < len(fv.into) && fv.into[i] != types.NoParam {
			continue
		}
		seen := emptySet
		for L := S; L != emptySet; L = c.sets.minus(c.behind(st, L), seen) {
			seen = c.sets.unite(seen, L)
			for _, l := range c.sets.get(L) {
				lo := &c.loans[l]
				t := lo.target
				if t == noRoot || !c.roots[t].carrier || !lo.excl && !c.roots[t].writable {
					continue
				}
				if c.sets.minus(c.allocSelf(t, L), c.held(st, t)) != emptySet {
					return true
				}
			}
		}
	}
	return false
}

func (c *checker) directCallArgs(t *mir.Terminator, fv *flowView) []types.GenericArg {
	if fv == nil || t.Callee.Kind != mir.CallDirect || t.Callee.Fn.Args.Len == 0 || int(t.Callee.Fn.Args.Len) != fv.generics {
		return nil
	}
	return c.body.GenericArgs[t.Callee.Fn.Args.Start:t.Callee.Fn.Args.End()]
}

// storesLoans reports whether `*q into p` can carry a loan at this call: the
// contents of q's type, under the call's generic arguments, are a carrier.
// `CopyFrom(mut self, src: []T) *src into self` at T = u8 stores bytes,
// which hold nothing (std.fs ReadBytes); with no arguments, or contents
// that still mention a parameter, it is a carrier as written (a generic
// callee is otherwise not substituted).
func (c *checker) storesLoans(t *mir.Terminator, fv *flowView, q types.ParamIndex) bool {
	args := c.directCallArgs(t, fv)
	if args == nil || int(q) > len(fv.paramTypes) {
		return true
	}
	ty := c.tab.Get(c.w.subst(fv.paramTypes[q-1], args))
	var elem types.TypeID
	switch ty.Kind {
	case types.KRef, types.KMut, types.KSlice, types.KMutSlice:
		elem = ty.Elem
	case types.KStr:
		return false
	default:
		return true
	}
	if c.mentionsParam(elem) {
		return true
	}
	return c.w.carrier(elem)
}

// mentionsParam reports whether t mentions a type parameter or Self.
func (c *checker) mentionsParam(t types.TypeID) bool {
	ty := c.tab.Get(t)
	switch ty.Kind {
	case types.KParam, types.KSelf, types.KInfer:
		return true
	case types.KRef, types.KMut, types.KSlice, types.KMutSlice, types.KOptional, types.KArray, types.KErrorUnion, types.KRawPtr:
		return c.mentionsParam(ty.Elem)
	case types.KNamed:
		for _, a := range ty.Args {
			if !a.IsConst && c.mentionsParam(a.Type) {
				return true
			}
		}
	case types.KRecord:
		for _, f := range ty.Fields {
			if c.mentionsParam(f.Type) {
				return true
			}
		}
	case types.KFn:
		return true
	}
	return false
}

// storeArg applies the store cases to every root of writable(argH[p]), then
// adds S to argH[p] (steps 3 and 4). S is also added to stored[i] for
// each written root that is endsRoots[i]. In a later round of the call's
// fixpoint the walk starts from own[p] (the argument's loans before steps 3
// and 4) and the loans stored into p not in S, and never passes a loan of S
// (see call). It reports whether argH[p] or a written root's held set grew.
//
// An allocator root (reached on a later hop, or as the argument's own target)
// receives S without the loans it holds by being itself (allocSelf). A root
// the walk reached only through an allocator's own memory hop (allocMem: p#2
// of a: &Arena) is judged without the loans of that allocator itself and
// records S in full (memSelf). The self-reach rule: what the stored loans
// themselves make writable (a mut reference among them) is reachable from p
// once they are in it, so it receives S too, with the same self filter.
func (c *checker) storeArg(st *state, ctx *stmtCtx, argH, argSh, own []setID, round, p int, S, sh setID, endsRoots []rootID, stored []setID, drop setID, dropAt rootID) bool {
	if S == emptySet {
		return false
	}
	// The walk does not start from a loan this call stored into p from a
	// shallow value (argSh[p]): nothing can be written through it, and a
	// root that holds it shallow is not hopped through either (shallowHop).
	added := c.sets.minus(c.sets.minus(argH[p], own[p]), argSh[p])
	var roots []rootID
	var fld []int
	if round == 0 {
		roots, fld = c.writableSkip(st, c.sets.unite(own[p], added), emptySet, noRoot)
	} else {
		roots, fld = c.writableSkip(st, c.sets.unite(own[p], c.sets.minus(added, S)), S, c.argRoot(st, own[p]))
	}
	grew := false
	c.taskGrew = false
	later, mem := c.laterAlloc, c.allocMem
	for i, r := range roots {
		before, beforeSh := c.held(st, r), st.get(r).sh
		// The loans of S stored from a shallow value are marked
		// shallow where they land (marks.go); every other store is deep.
		c.shIn = sh
		// A split frame root reached through a loan on one of its fields
		// takes S in that field.
		if later[r] || c.roots[r].allocator {
			Sr := c.allocSelf(r, S)
			if r == dropAt {
				Sr = c.sets.minus(Sr, drop)
			}
			c.storeInto(st, ctx, r, Sr, fld[i])
		} else if a, ok := mem[r]; ok {
			// Judged without the allocator's own loans, recorded in
			// full, so the state is the one the full store leaves. r is an
			// abstract memory node, never a split frame root (fld -1).
			c.storeInto(st, ctx, r, c.memSelf(a, S), fld[i])
			c.addHeld(st, r, S)
		} else {
			c.storeInto(st, ctx, r, S, fld[i])
		}
		c.shIn = emptySet
		if after := c.held(st, r); after != before {
			grew = true
			c.noteCallStored(r, c.sets.minus(after, before))
		} else if st.get(r).sh != beforeSh {
			grew = true // a mark went deep: the walk may pass it now
		}
		for i, e := range endsRoots {
			if e == r {
				stored[i] = c.sets.unite(stored[i], S)
			}
		}
	}
	// Self-reach: a loan is never stored into its own target, nor a
	// parameter's abstract loan into that parameter's own node. At a
	// node that stands for several referents (p#1 of a by-value
	// parameter, p#2) that second filter alone would let one referent's
	// loan reach another, so it does not decide: the callee that writes
	// through the stored value judges the write in its own body, where
	// its abstract nodes hold what it stored. No body may make that
	// write, so this rule is a second line of defence: it rejects the
	// direct caller of an unsafe or extern callee that breaks its
	// contract, not a caller forwarding its own parameter, whose loans
	// the filter above drops. These inferred stores walk and store with
	// abstract nodes as constants (csrConst): recorded, a store a clause
	// names may chain into one no clause clears (`h into conn` chaining
	// into `conn into conn`). The rule's roots take S in every
	// loan-holding field of a split frame root (-1): no field is tracked
	// for them, as a store through a whole loan.
	c.csrConst = true
	// When the allocator dropAt was the only root written, what it took is
	// S without drop, and the rule walks from that; its roots take S.
	full := S
	if drop != emptySet && len(roots) > 0 && !slices.ContainsFunc(roots, func(r rootID) bool { return r != dropAt }) {
		S = c.sets.minus(S, drop)
	}
	csr := c.writable(st, S)
	S = full
	c.csrConst = false
	for _, r := range csr {
		before := c.held(st, r)
		c.csrConst = true
		c.storeInto(st, ctx, r, c.allocSelf(r, S), -1)
		c.csrConst = false
		if after := c.held(st, r); after != before {
			grew = true
			c.noteCallStored(r, c.sets.minus(after, before))
		}
		for i, e := range endsRoots {
			if e == r {
				stored[i] = c.sets.unite(stored[i], S)
			}
		}
	}
	if c.taskGrew {
		grew = true // a store followed through a task's clause grew a root
	}
	oldSh := argSh[p]
	c.addMarked(argH, argSh, p, S, sh)
	if u := c.sets.unite(argH[p], S); u != argH[p] {
		argH[p] = u
		grew = true
	} else if argSh[p] != oldSh {
		grew = true
	}
	return grew
}

// noteCallStored records that this call's steps 3 and 4 added loans to root
// r's held set: a later round of the call's fixpoint skips a loan of S only
// where the call itself stored it, and still walks a loan of S the root
// held before the call. For example, gl holds x's loan before
// K(mut gl, H(none), t) with `h into g` and `t into h`; round 1 stores t's
// loan, x's again, into gl, and the walk through gl's own copy reaches x's
// node: a store of x into x, as round 0 finds when the clause is `t into g`.
func (c *checker) noteCallStored(r rootID, added setID) {
	if added == emptySet {
		return
	}
	if c.callStored == nil {
		c.callStored = map[rootID]setID{}
	}
	c.callStored[r] = c.sets.unite(c.callStored[r], added)
}

// spawnStores is a spawn's "every argument into s" after the callee's own
// steps 3 and 4: a scope local receives each argument's set itself; through
// a &Scope, only the writable roots of H(s) do.
//
// A scope local holds the arguments itself and nothing else does: their
// loans last until the scope's block exits. Storing them also through
// writable(H(s)) would put each argument's loans into the referents of the
// scope's other arguments (a `&Chan`, a `&HeapList` argument), which outlive
// the block: E0503 after it, E0604 and E0505 at the spawn. Each callee's own
// flows still run (steps 3 and 4).
//
// Through a &Scope the walk from H(s) reads abstract nodes as {p.inner}
// (csrConst), and the stores into the roots it reaches are recorded as
// usual. The scope's node records what each spawn stored into it, so
// otherwise a second spawn's walk would go on from that node through the
// earlier arguments' loans into their referents: `s.go F(m, c)` twice, with
// `m: &Mutex[int] into s, c: &Chan[int] into s`, would store m's and c's
// loans into c's nodes (E0604 c into c). The runtime puts a task into
// the scope's own memory, and the Scope API (Cancel, CancelAfter, Expired,
// Remaining) hands nothing back, so no spawn writes through what the scope
// holds.
func (c *checker) spawnStores(st *state, ctx *stmtCtx, t *mir.Terminator, argH []setID) {
	s := rootID(t.Spawn)
	for _, S := range argH {
		if S == emptySet {
			continue
		}
		if c.roots[s].kind == rkScope {
			c.addHeld(st, s, S)
			continue
		}
		c.csrConst = true
		rs, fld := c.writableSkip(st, c.held(st, s), emptySet, noRoot)
		c.csrConst = false
		for i, r := range rs {
			if r == s {
				continue
			}
			if c.laterAlloc[r] {
				c.storeInto(st, ctx, r, c.allocSelf(r, S), fld[i]) // a later-hop allocator
				continue
			}
			c.storeInto(st, ctx, r, S, fld[i])
		}
	}
}

// endSlots drops the argument slots of this call: their loans end with the
// call.
func (c *checker) endSlots(st *state, ctx *stmtCtx) {
	for _, r := range c.sets.get(ctx.slots) {
		c.put(st, rootID(r), entry{})
	}
}

// endsArgs finds, for each `ends p` of the callee, the root whose loans the
// call ends: a whole local moved as the argument, or the whole local
// or parameter an RvRef in the same block borrowed into the temporary
// moved as the argument. Any other argument shape ends nothing.
// A spawned call ends nothing either.
func (c *checker) endsArgs(ctx *stmtCtx, t *mir.Terminator, fv *flowView) []rootID {
	if fv == nil || len(fv.ends) == 0 || t.Spawn != mir.NoLocal {
		return nil
	}
	args := c.body.Args[t.Args.Start:t.Args.End()]
	var out []rootID
	for _, p := range fv.ends {
		i := int(p) - 1
		if i < 0 || i >= len(args) {
			continue
		}
		if r, ok := c.endsRoot(ctx.b, args[i]); ok {
			out = append(out, r)
		}
	}
	return out
}

// endsRoot is the root an ends argument ends, if its shape is one an ends
// clause accepts at a call.
func (c *checker) endsRoot(b mir.BlockID, a mir.Arg) (rootID, bool) {
	if a.Mode != mir.ArgMove || a.Operand.Kind != mir.OpMove || a.Operand.Place.Proj.Len != 0 {
		return noRoot, false
	}
	l := a.Operand.Place.Local
	if c.body.Locals[l].Kind != mir.LocalTemp {
		return rootID(l), true
	}
	if r, ok := c.refSource(b, l); ok {
		return r, true
	}
	return noRoot, false
}

// refSource finds the RvRef in block b that made temporary t from a whole
// local or parameter, and returns that root.
func (c *checker) refSource(b mir.BlockID, t mir.LocalID) (rootID, bool) {
	stmts := c.body.Blocks[b].Stmts
	for i := len(stmts) - 1; i >= 0; i-- {
		s := &stmts[i]
		if s.Kind != mir.StAssign || s.Place.Proj.Len != 0 || s.Place.Local != t {
			continue
		}
		if s.Rvalue.Kind == mir.RvRef && s.Rvalue.Place.Proj.Len == 0 {
			return rootID(s.Rvalue.Place.Local), true
		}
		return noRoot, false
	}
	return noRoot, false
}

// exemptFor returns the ends exemption for a whole-local ArgMove of an ends
// root: a conflicting loan on r is skipped when no live root other than r
// reaches it.
func (c *checker) exemptFor(st *state, ctx *stmtCtx, endsRoots []rootID, a mir.Arg) func(loanID) bool {
	if !c.report || len(endsRoots) == 0 || a.Mode != mir.ArgMove || a.Operand.Place.Proj.Len != 0 {
		return nil
	}
	r := rootID(a.Operand.Place.Local)
	if !slices.Contains(endsRoots, r) || c.body.Locals[r].Kind == mir.LocalTemp {
		return nil
	}
	return c.exemption(st, ctx, r)
}

// exemption is the ends clause's skip function: a loan on r is exempt when no
// live root other than r reaches it.
func (c *checker) exemption(st *state, ctx *stmtCtx, r rootID) func(loanID) bool {
	return func(l loanID) bool { return !c.liveLoan(st, ctx, l, false, r) }
}

// keepEndsProv reports whether an ends call on root r keeps r's provenance
// loans (the loans r held as its own value before the call, outside
// e.stored) and ends only what was stored into r's memory: a view, an
// allocator, or a type parameter or Self that an instance may make one.
func (c *checker) keepEndsProv(r rootID) bool {
	ri := &c.roots[r]
	return ri.view || ri.allocator || c.inlineParam(ri.typ, map[types.TypeID]bool{})
}

// allocReferent reports whether argument a is a reference to an allocator
// (`&a`, `mut cur` of an Arena), whose `*q into p` step reads the allocator's
// handle rather than its memory (behindKeep).
func (c *checker) allocReferent(a mir.Arg) bool {
	if a.Operand.Kind != mir.OpMove && a.Operand.Kind != mir.OpCopy {
		return false
	}
	switch ty := c.tab.Get(c.body.PlaceType(a.Operand.Place)); ty.Kind {
	case types.KRef, types.KMut:
		return c.w.factsOf(ty.Elem).Allocator
	}
	return false
}

// handleOf is the allocator root that every loan of S targets, when S is
// not empty and there is one such root, else noRoot: S is the set of a
// reference to that allocator alone.
func (c *checker) handleOf(S setID) rootID {
	r := noRoot
	for _, l := range c.sets.get(S) {
		t := c.loans[l].target
		if t == noRoot || !c.roots[t].allocator || r != noRoot && t != r {
			return noRoot
		}
		r = t
	}
	return r
}

// allocMaking is what allocator root r holds by its making (its heap's
// loan, a sub-arena's parent's): its held set less what was stored into its
// contents (allocStored).
func (c *checker) allocMaking(st *state, r rootID) setID {
	return c.sets.minus(c.held(st, r), c.allocStored[r])
}
