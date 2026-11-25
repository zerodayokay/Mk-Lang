// Why: a loan that is stored somewhere lives as long as the place that
// holds it; every store, through a call or a write through a reference,
// must find each root it may land in and judge the loan against it.
//
// What: writable and writableSkip (the roots a store through a set of loans
// may write), storeInto (the store cases), declaredInto (declared flows,
// through chains), directWrite and ownInline (the direct write rule), and
// the allocator rules allocSelf, memHop and memSelf.
//
// How: the writable walk starts at the loans' targets; an exclusive step
// keeps writing possible, a shared step ends it, a step into a Cell, Mutex,
// Chan, Arena or view restores it. A local, temporary or argument slot whose
// type can write no loan below its own bytes (storesBelow) receives the store
// but passes it on through none of the loans it holds. An allocator reached
// on a later hop is itself the root a store writes, since its contents have
// no node of their own. A store into a frame root, a parameter's node or a
// global is judged against the body's signature (E0604, E0605).

package borrow

import (
	"mk/internal/diag"
	"mk/internal/types"
)

type hop struct {
	l     loanID
	excl  bool
	first bool
	// fromAlloc: the allocator root that holds the loan (a sub-arena holds
	// its parent's loan, an arena its heap's), noRoot when none does.
	fromAlloc rootID
	// capFld: the hidden environment parameter's field index carried along
	// the walk (0-based, or -1 when none/unknown).
	capFld int
	// noAlloc: set on a hop that entered a parameter's inner node through a
	// tagged inner loan whose field type reaches no allocator (reachesAlloc);
	// staying on that parameter's own inner self-loop (p#2 -> p.inner) keeps
	// noAlloc so it never jumps to p#A.
	noAlloc bool
}

// writable is writable(L0): walking from the loans of L0, an exclusive step
// keeps writing possible, a shared step ends it, a step into a Cell, Mutex,
// Chan, Arena or view restores it; the walk does not pass an allocator, nor
// a local, temporary or argument slot whose type can write no loan below its
// own bytes (storesBelow). An allocator reached on a later hop is itself a
// root the store writes: its contents (a Cell put in it, a List's storage)
// have no node of their own, so a loan stored into them is stored into the
// allocator; the call rule stores into it without the loans it holds by
// being itself (allocSelf). The exception is an allocator reached out of an
// allocator through a loan the holder has by provenance (a sub-arena's
// parent, an arena's heap: the loan its own value was made with), not one
// stored into its contents (allocStored): what lives in the holder is not in
// the other allocator's memory. A loan stored into the holder's contents is
// followed, since the holder dies first and the write went into the other's
// memory. Roots come out in walk order, each once; c.laterAlloc marks the
// allocators reached on a later hop, and c.allocMem the roots reached only
// through an allocator's own memory hop (memHop).
//
// An abstract node is an allocator only when it stands for allocators alone:
// a parameter's allocators below its first level are p#A, reached through a
// loan read from an allocator field, so the stop never hides p#2, where a
// mut reference's referent or a view's contents live (merging them would
// let a callee's store through a mut &T beside a List or &Arena field go
// unjudged).
func (c *checker) writable(st *state, l0 setID) []rootID {
	rs, _ := c.writableSkip(st, l0, emptySet, noRoot)
	return rs
}

// writableSkip is writable(L0) with no hop through a loan of skip: the
// later rounds of the call rule's fixpoint walk around the loans
// they store. arg, when not noRoot, is the argument the call stored l0's
// loans into: a root reached through one of them is named as reached
// through arg. fld is, per root of out, the field of a split frame root
// that every loan reaching it names (fieldOfLoan), else -1.
func (c *checker) writableSkip(st *state, l0, skip setID, arg rootID) (out []rootID, fld []int) {
	at := map[rootID]int{}
	seenHop := make(map[hop]bool)
	seenRoot := make(map[rootID]bool)
	c.laterAlloc = nil
	c.allocMem = nil
	// other: the roots some hop reaches other than an allocator's own
	// memory hop (memHop) or a root's loan of itself. Only a body
	// with a memNode parameter tracks them.
	var other map[rootID]bool
	reach := func(src rootID, m loanID) {
		if !c.memParams {
			return
		}
		if other == nil {
			other = map[rootID]bool{}
		}
		tg := c.loans[m].target
		switch {
		case tg == noRoot || tg == src:
		case src != noRoot && c.memHop(src, m):
			if c.allocMem == nil {
				c.allocMem = map[rootID]rootID{}
			}
			c.allocMem[tg] = src
		default:
			other[tg] = true
		}
	}
	var work []hop
	for _, l := range c.sets.get(l0) {
		reach(noRoot, loanID(l))
		work = append(work, hop{l: loanID(l), excl: true, first: true, fromAlloc: noRoot, capFld: -1})
	}
	defer func() {
		for tg := range c.allocMem {
			if other[tg] {
				delete(c.allocMem, tg)
			}
		}
	}()
	if c.hasHiddenParam() {
		c.capField = map[rootID]int{}
	} else {
		c.capField = nil
	}
	// During the replay, the holder of each loan met on a later hop, so a
	// diagnostic at a root reached through it can name the holder.
	var heldBy map[loanID]rootID
	if c.report {
		c.via = map[rootID]viaHop{}
		heldBy = map[loanID]rootID{}
		if arg != noRoot {
			for _, l := range c.sets.get(l0) {
				heldBy[loanID(l)] = arg
			}
		}
	}
	for len(work) > 0 {
		h := work[0]
		work = work[1:]
		if seenHop[h] {
			continue
		}
		seenHop[h] = true
		lo := &c.loans[h.l]
		t := lo.target
		if t == noRoot {
			continue
		}
		ti := &c.roots[t]
		if c.walkRead != nil && (ti.carrier || ti.kind == rkAbstract) {
			c.walkRead[t] = true // the region of a task flow (taskflow.go)
		}
		e := h.excl && lo.excl
		if ti.allocator && !h.first && h.fromAlloc != noRoot && !c.allocContent(h.fromAlloc, h.l) {
			continue // an allocator its holder holds by provenance
		}
		cf := -1
		if c.capField != nil {
			cf = c.loanCapField(t, h.l, h.capFld)
		}
		if ti.allocator && !h.first {
			// A later hop onto an allocator stores into the allocator itself
			// (its contents have no node of their own), and the walk does not
			// pass it, except into what was stored into its contents
			// (allocStored): a holder in its memory leads on to that loan's
			// target.
			c.setCapField(t, cf)
			if !seenRoot[t] {
				seenRoot[t] = true
				at[t] = len(out)
				out = append(out, t)
				fld = append(fld, -1) // an allocator is never a split frame root
				if c.laterAlloc == nil {
					c.laterAlloc = map[rootID]bool{}
				}
				c.laterAlloc[t] = true
				if r, ok := heldBy[h.l]; ok && r != t {
					c.via[t] = viaHop{holder: r, l: h.l}
				}
			}
			for _, m := range c.sets.get(c.held(st, t)) {
				if c.skipHop(skip, t, m) || c.shallowHop(st, t, m) || !c.allocContent(t, loanID(m)) {
					continue
				}
				if _, ok := heldBy[loanID(m)]; !ok && heldBy != nil {
					heldBy[loanID(m)] = t
				}
				reach(t, loanID(m))
				work = append(work, hop{l: loanID(m), excl: e || ti.writable, fromAlloc: t, capFld: cf})
			}
			continue
		}
		if ti.carrier && (e || ti.writable) {
			c.setCapField(t, cf)
			if !seenRoot[t] {
				seenRoot[t] = true
				at[t] = len(out)
				out = append(out, t)
				fld = append(fld, c.fieldOfLoan(h.l, t))
				if r, ok := heldBy[h.l]; ok && r != t {
					c.via[t] = viaHop{holder: r, l: h.l}
				}
			} else if i, ok := at[t]; ok && fld[i] != c.fieldOfLoan(h.l, t) {
				fld[i] = -1
			}
		}
		// A hop onto p#2 through an inner loan that may reach an allocator
		// (an untagged p.inner, or a tagged field whose type reaches an
		// allocator, not read out of an allocator by provenance) also reaches
		// p#A, as does a first hop onto p#1/p#2: a loan not read from a pure
		// allocator field (a local copy of a struct holding one, a result from
		// *p, a shared reference to a struct holding &Arena, or a local struct
		// wrapping *p) may still refer to an allocator, as one merged node would.
		noAlloc := h.noAlloc || lo.noAlloc
		if a := c.allocOf(t); !noAlloc && (h.first || lo.inner && (h.fromAlloc == noRoot || c.allocContent(h.fromAlloc, h.l))) && a != noRoot {
			if ai := &c.roots[a]; ai.carrier && (e || ai.writable) {
				c.setCapField(a, cf)
				if !seenRoot[a] {
					if other != nil {
						other[a] = true
					}
					seenRoot[a] = true
					at[a] = len(out)
					out = append(out, a)
					fld = append(fld, -1)
					if r, ok := heldBy[h.l]; ok && r != a {
						c.via[a] = viaHop{holder: r, l: h.l}
					}
				}
			}
		}
		if !c.csrConst && ti.kind != rkAbstract && ti.kind != rkStatic && ti.typ != types.NoType && !c.w.storesBelow(ti.typ) {
			// Nothing a value of t's type can write below its own bytes
			// holds a loan, so a store through it goes no further than t:
			// what t holds by provenance (a reborrow's path, R12) is no
			// place the store can land (storebelow.go). The self-reach
			// walk and spawnStores' (csrConst) do not cut here, as they
			// read no shallow mark (shallowHop): they defend against a
			// callee that writes past its signature, so they trust no type.
			continue
		}
		for _, m := range c.sets.get(c.held(st, t)) {
			if c.skipHop(skip, t, m) || c.shallowHop(st, t, m) {
				continue // no hop through a shallow loan (marks.go)
			}
			if _, ok := heldBy[loanID(m)]; !ok && heldBy != nil {
				heldBy[loanID(m)] = t
			}
			fa := noRoot
			if ti.allocator {
				fa = t
			}
			na := noAlloc && ti.kind == rkAbstract && ti.depth == 2 && (loanID(m) == c.params[ti.param].inner || c.isParamN2FieldLoan(ti.param, loanID(m)))
			reach(t, loanID(m))
			work = append(work, hop{l: loanID(m), excl: e || ti.writable, fromAlloc: fa, capFld: cf, noAlloc: na})
		}
	}
	return out, fld
}

// skipHop reports whether the walk of a later fixpoint round skips loan m
// held by root t: m is in skip (the set the round stores) and this call
// stored m into t (callStored). A loan of skip that t held before the call
// is walked.
func (c *checker) skipHop(skip setID, t rootID, m uint32) bool {
	return skip != emptySet && c.sets.has(skip, m) && c.sets.has(c.callStored[t], m)
}

// viaHop is how the writable walk reached a root on a later hop: holder
// held loan l, which targets the root.
type viaHop struct {
	holder rootID
	l      loanID
}

// viaText is the clause naming how a store reached root t, for E0604 and
// E0505 (DF-lox-06, DF-kvd-03, DF-mgrep-02): " (`rows` holds a loan of
// `*prog`, so the store reaches it)" when the last writable walk reached t
// through a local's held loan, else "". The store goes where the abstract
// rules say it may; the clause shows the reader the holder that made t a
// target.
func (c *checker) viaText(t rootID) string {
	if s := c.taskFlowText(); s != "" {
		return s // a store followed through a task's clause names the task
	}
	v, ok := c.via[t]
	if !ok {
		return ""
	}
	if k := c.roots[v.holder].kind; k == rkStatic || k == rkAbstract {
		return "" // a parameter's node holds its own loans: no news
	}
	return " (`" + c.rootName(v.holder) + "` holds a loan of `" + c.loanPlace(v.l) + "`, so the store reaches it)"
}

// allocOf is p#A when t is p#2, and p#1A when t is p#1, of a parameter that
// has one, else noRoot. A loan that targets p#2 (p#1) may refer to the
// parameter's allocators too, unless it was read from a field that refers
// to none: wherever p#2 (p#1) is reached directly, the allocator
// node is reached with it.
func (c *checker) allocOf(t rootID) rootID {
	ri := &c.roots[t]
	if ri.kind != rkAbstract {
		return noRoot
	}
	switch pp := &c.params[ri.param]; {
	case t == pp.n2 || c.isParamN2Field(ri.param, t):
		return pp.nA
	case t == pp.n1:
		return pp.n1A
	}
	return noRoot
}

// storeInto is the store cases: storing S into root t at the current
// statement. fld is, for a split frame root, the field the store lands in (the
// field a loan's place names), or -1 for every loan-holding field.
//
// While a spawned task may run, its `*q into p` clauses are applied again
// after the store (followTaskFlows).
func (c *checker) storeInto(st *state, ctx *stmtCtx, t rootID, S setID, fld int) {
	if len(c.taskFlows) != 0 {
		defer c.followTaskFlows(st, ctx, t)
	}
	ti := &c.roots[t]
	switch ti.kind {
	case rkStatic:
		if !c.report {
			return
		}
		for _, l := range c.sets.get(S) {
			if !c.loans[l].static {
				c.errAt(diag.E0605, ctx.span, "a value stored into global memory may hold only static loans").
					Fact("loan", c.loanFact(loanID(l)))
			}
		}
		return
	case rkAbstract:
		// The node holds what is stored into it from here on, so a
		// later deref or walk through it reaches the stored loans' targets.
		// The self-reach rule's inferred stores record nothing (csrConst,
		// call.go).
		if !c.csrConst {
			c.addHeld(st, t, S)
		}
		if !c.report {
			return
		}
		for _, l := range c.sets.get(S) {
			c.storeAbstract(ctx, t, loanID(l))
		}
		return
	}
	if ti.allocator {
		c.noteAllocContent(t, S)
	}
	c.storeLocal(st, t, S, fld)
}

// noteAllocContent records S as stored into allocator root t's contents:
// grow-only for the body, so a loan an arena holds because
// something was stored into its memory is told apart from the loans it holds
// by provenance (its heap's, a sub-arena's parent's), which reach it only
// by assignment. solve runs another round when it grows.
func (c *checker) noteAllocContent(t rootID, S setID) {
	if c.allocStored == nil {
		c.allocStored = map[rootID]setID{}
	}
	if u := c.sets.unite(c.allocStored[t], S); u != c.allocStored[t] {
		c.allocStored[t] = u
		c.allocGrew = true
		c.allocGen++
	}
}

// allocContent reports whether loan l was stored into allocator root t's
// contents. An abstract node holds only p.inner, by provenance.
func (c *checker) allocContent(t rootID, l loanID) bool {
	s, ok := c.allocStored[t]
	return ok && c.sets.has(s, uint32(l))
}

// storeAbstract judges one loan stored into an abstract node of parameter p
// by a table: every loan kind meets exactly one row.
func (c *checker) storeAbstract(ctx *stmtCtx, t rootID, l loanID) {
	node := &c.roots[t]
	lo := &c.loans[l]
	p := node.param
	switch {
	case lo.static:
		// A static outlives every call. But a reference into global memory
		// that takes stores, stored where the caller's argument may point
		// into it, would let a frame's loan into global memory through an
		// out-parameter: no clause names a static, so the caller's argument
		// holds nothing after the call and a store of a frame's loan through
		// it is judged into nothing. The self-reach rule's inferred stores
		// (csrConst) are left out.
		if !c.csrConst && node.takes && c.staticTakesStores(lo.target) {
			if c.hiddenParam(p) {
				name := c.captureTargetName(p, t)
				d := c.captureE0609(ctx.span, name, "store",
					"the literal stores a reference into global memory ("+c.rootName(lo.target)+
						"), which takes stores, into `"+name+"`, which it captured; a capture is no parameter, so no clause can name it").
					Fact("loan", c.loanFact(l))
				if c.staticStore == nil {
					c.staticStore = map[*diag.Diagnostic]bool{}
				}
				c.staticStore[d] = true
				return
			}
			d := c.errAt(diag.E0604, ctx.span, "this stores a reference into global memory (%s), which takes stores, into %s; no clause can name a static, so %s's caller would not see it and would judge a store through it into nothing",
				c.rootName(lo.target), c.paramName(p), c.paramName(p)).Fact("loan", c.loanFact(l))
			if c.staticStore == nil {
				c.staticStore = map[*diag.Diagnostic]bool{}
			}
			c.staticStore[d] = true
			c.boundAlt(d, t) // the walk stopped at a bound (boundmsg.go)
			if c.env.Sig != nil {
				d.Text("signature", c.sigString(c.env.Sig))
			}
			d.Alt("return the reference from a function whose result names no source")
		}
		return
	case lo.abstract:
		// Already held: p#1 holds p.inner, so storing p.inner (or a tagged
		// part of it) back into p#1 adds nothing, when p#1 is one referent:
		// *p of a reference or slice parameter. A by-value parameter's p#1
		// stands for the referents of all its first-level references
		// (S { x: &&i32, b: mut &i32 }), so a loan one of them held may not
		// be stored into another: it is judged as any other; a hidden
		// environment parameter's fields are independent captures too.
		if node.depth == 1 && !c.byValueParamIndex(p) && !c.hiddenParam(p) && (l == c.params[p].inner || lo.tag != nil && lo.base == c.params[p].inner) {
			return
		}
		if c.declaredInto(p, l) {
			return
		}
		if c.hiddenParam(p) {
			// Storing a parameter's or another capture's loan into a captured
			// variable could outlive that loan's scope, and no clause names a
			// capture.
			dst := c.captureTargetName(p, t)
			var msg string
			if src, ok := c.loanCapture(l); ok {
				msg = "the literal stores a loan on `" + src + "` into `" + dst + "`, which it captured; a capture is no parameter, so no clause can name it"
			} else {
				msg = "the literal stores a loan of `" + c.paramName(lo.param) + "` into `" + dst + "`, which it captured; a capture is no parameter, so no clause can name it"
			}
			c.captureE0609(ctx.span, dst, "store", msg).Fact("loan", c.loanFact(l))
			return
		}
		if name, ok := c.loanCapture(l); ok {
			// A stored capture loan would outlive the call's statement, and no
			// clause names it.
			c.captureE0609(ctx.span, name, "store", "the literal stores a loan on `"+name+"`, which it captured, into `"+
				c.paramName(p)+"`; a capture is no parameter, so no clause can name it")
			return
		}
		d := c.errAt(diag.E0604, ctx.span, "this stores a loan of %s into %s, which the signature does not declare%s",
			c.paramName(lo.param), c.paramName(p), c.viaText(t)).Fact("loan", c.loanFact(l))
		c.storeFix(d, l, p)       // the clause to write, and its edit
		c.ctxStateAlt(d, t, l, p) // state reached through a Ctx's field (ctxstore.go)
		if ctx.term {
			c.sigFact(d, ctx)
		} else if c.env.Sig != nil {
			d.Text("signature", c.sigString(c.env.Sig))
		}
	case lo.target != noRoot && c.roots[lo.target].frame():
		tk := c.roots[lo.target].kind
		if tk == rkTemp || tk == rkHolder {
			c.errAt(diag.E0506, lo.site, "this temporary is stored into memory that outlives the call").
				Fact("loan", c.loanFact(l))
			return
		}
		// At the statement that stores, with the loan's creation as a
		// note, when the two are on different lines.
		at, moved := lo.site, false
		if c.env.File != nil {
			a, b := c.body.Span(ctx.span), c.body.Span(lo.site)
			if f := c.env.File(a.File); f != nil && a.File == b.File && a.Start <= f.Len() && b.Start <= f.Len() &&
				f.LineCol(a.Start).Line != f.LineCol(b.Start).Line {
				at, moved = ctx.span, true
			}
		}
		d := c.errAt(diag.E0505, at, "%s is stored into memory that outlives it%s", c.rootName(lo.target), c.viaOutlives(t)).
			Fact("loan", c.loanFact(l))
		if moved {
			d.Loc("created_at", c.body.Span(lo.site))
		}
	}
	// No target (made through a deref): skipped, as at the boundary.
}

// byValueParamIndex reports whether parameter p of this body is passed by
// value (its declared type is not &U, mut U, a slice, str or fn).
func (c *checker) byValueParamIndex(p types.ParamIndex) bool {
	for i := range c.body.Locals {
		if ri := &c.roots[i]; ri.kind == rkParam && ri.param == p {
			return ri.byValueParam
		}
	}
	return false
}

// declaredInto reports whether loan l is a declared flow into parameter p of
// the body's own signature: an abstract loan of a parameter x with `x into p`,
// or an inner (or tagged inner) loan of q for `*q into p`.
//
// Both are read through chains (flowChain): with `name into a` and `a into
// parent`, a loan of name stored into parent is declared, as the caller's
// call rule puts H(name) into a's argument and a's loans into parent's, so
// the caller's reach from parent meets name (the cookbook's AddChild).
// After the first step a chain may take either form: with `*a into b, *b
// into a`, a's contents reach b and b's contents, those included, reach a,
// so a's own inner loan stored into a is declared (the narrower skip of the
// call rule's later rounds walks that path).
func (c *checker) declaredInto(p types.ParamIndex, l loanID) bool {
	sig := c.env.Sig
	lo := &c.loans[l]
	if sig == nil || !lo.abstract {
		return false
	}
	if x := lo.param; x != types.NoParam && int(x) <= len(sig.Params) {
		if q := sig.Params[x-1].Into; q != types.NoParam && (q == p || c.flowChain(q, p)) {
			return true
		}
	}
	if lo.inner {
		for _, s := range sig.Flows.Stores {
			if s.From == lo.param && (s.Into == p || c.flowChain(s.Into, p)) {
				return true
			}
		}
	}
	return false
}

// flowChain reports whether what parameter x holds flows into p through one
// or more clauses of the body's own signature, each `q: T into r` or
// `*q into r`.
func (c *checker) flowChain(x, p types.ParamIndex) bool {
	sig := c.env.Sig
	if sig == nil || p == types.NoParam {
		return false
	}
	seen := make([]bool, len(sig.Params)+1)
	work := []types.ParamIndex{x}
	for len(work) > 0 {
		q := work[len(work)-1]
		work = work[:len(work)-1]
		if q == types.NoParam || int(q) > len(sig.Params) || seen[q] {
			continue
		}
		if q == p {
			return true
		}
		seen[q] = true
		work = append(work, sig.Params[q-1].Into)
		for _, s := range sig.Flows.Stores {
			if s.From == q {
				work = append(work, s.Into)
			}
		}
	}
	return false
}

// directWrite is the direct write rule for a destination through a deref
// with no shared step: each deref steps from the current roots to the
// targets of what they hold; after each deref a view also walks on to the
// targets of what it holds, never to an allocator (a root of allocators
// alone: a local Arena, a parameter's p#A), unless the write stays in the
// parameter's own value beside the view (ownInline); a target reached
// directly stays, allocator or not. The store cases apply at every final
// root.
//
// The walk on extends to every root through which the write reaches
// writable memory, the memory the writable walk reaches at a call: (1) a
// root that is no view walks on as a view does when its type
// holds a mut reference at any depth, or a shared reference to an
// interior-mutable type or a view that is no allocator, or a type
// parameter or Self (rootInfo.walk, walker.walkExtra), so a write through a
// result one level above the reference it reaches lands on
// the referent; (2) an allocator that the walk on from a view meets after
// the last deref, the storage the view lives in, is stored into with the
// allocator self filter, as the call rule stores into it for Push: l[i] = x,
// l.ItemsMut()[i] = x and *l.GetMut(i) = x store x's loans into l's arena
// (whatever x views: a frame, a block, a Box), and a write through a
// Shared clone's guard into the clone's heap.
func (c *checker) directWrite(st *state, ctx *stmtCtx, p pathID, S setID) {
	c.via = nil // no walk: no holder to name
	pi := c.paths.get(p)
	directCap := -1
	if c.hasHiddenParam() {
		c.capField = map[rootID]int{}
		if ri := &c.roots[pi.root]; (ri.kind == rkParam || ri.kind == rkAbstract) && c.hiddenParam(ri.param) {
			for _, stp := range pi.steps {
				if stp.kind == stField {
					directCap = int(stp.a)
					break
				}
			}
		}
	} else {
		c.capField = nil
	}
	roots := []rootID{pi.root}
	// fld is, per final root, the field it is reached at: a split frame
	// root through loans whose place names that field, else -1.
	var fld map[rootID]int
	// allocs are the allocators the last deref's walk on from a view met
	// (case (2) above).
	var allocs []rootID
	derefs := 0
	for si, sp := range pi.steps {
		if sp.kind != stDeref {
			continue
		}
		derefs++
		allocs = allocs[:0]
		var next []rootID
		seen := make(map[rootID]bool)
		fld = make(map[rootID]int)
		add := func(t rootID, f int) {
			if seen[t] {
				if fld[t] != f {
					fld[t] = -1
				}
				return
			}
			seen[t] = true
			fld[t] = f
			next = append(next, t)
		}
		for _, r := range roots {
			h := c.held(st, r)
			if derefs == 2 {
				h = c.tag(h, p)
			}
			for _, l := range c.sets.get(h) {
				if t := c.loans[l].target; t != noRoot {
					add(t, c.fieldOfLoan(loanID(l), t))
					if directCap < 0 {
						c.noteCapField(t, loanID(l))
					}
					// A deref onto p#2 also lands on p#A, reached
					// directly, as on one merged node.
					if a := c.allocOf(t); a != noRoot && !c.loans[l].noAlloc {
						add(a, -1)
						if directCap < 0 {
							c.noteCapField(a, loanID(l))
						}
					}
				}
			}
		}
		for i := 0; i < len(next); i++ {
			src := &c.roots[next[i]]
			if !src.view && !src.walk || c.ownInline(pi, si, next[i]) {
				continue
			}
			for _, l := range c.sets.get(c.held(st, next[i])) {
				t := c.loans[l].target
				if t == noRoot {
					continue
				}
				if c.roots[t].allocator {
					// Case (2): never walked on to, but the memory
					// the view lives in is written.
					if src.view {
						allocs = append(allocs, t)
						if directCap < 0 {
							c.noteCapField(t, loanID(l))
						}
					}
					continue
				}
				add(t, c.fieldOfLoan(loanID(l), t))
				if directCap < 0 {
					c.noteCapField(t, loanID(l))
				}
			}
		}
		roots = next
	}
	if directCap >= 0 {
		for _, t := range roots {
			c.setCapField(t, directCap)
		}
		for _, a := range allocs {
			c.setCapField(a, directCap)
		}
	}
	done := make(map[rootID]bool, len(roots))
	for _, t := range roots {
		done[t] = true
		// A root whose type is no carrier holds nothing, so
		// nothing is stored there: an element loan a view holds (the &i32
		// values of a List[&i32]) reaches the referent's node, an i32
		// (the carrier condition of writable).
		if !c.roots[t].carrier {
			continue
		}
		c.storeInto(st, ctx, t, S, fld[t])
	}
	for _, a := range allocs {
		if done[a] {
			continue
		}
		done[a] = true
		c.storeInto(st, ctx, a, c.allocSelf(a, S), -1) // the allocator self filter
	}
}

// ownInline reports whether the deref at step si of path pi, landing on root
// t, writes inside the parameter's own value and outside every view in it, so
// that the direct write rule's walk on through a view does not apply at t: the
// path starts at a parameter p that is a reference and not var, the deref is
// its first step, t is p#1 (reached by p's outer loan, so the place is *p
// itself), the type after the deref is not a view, and the steps that follow
// up to the next deref select a part that neither is in a view nor holds one.
// The rule walks on when a deref "lands on a view"; *p only holds one, and a
// field beside it is first-level storage, which already holds everything below
// it. A view parameter, a local view, a var parameter, a later deref and a
// write into or of the view field all keep the walk.
//
// The same holds for the walk on from a root that holds a mut reference or
// a type parameter (case (1) of directWrite): a part of *p selected by
// field, variant or array index steps is first-level storage, so `self.b =
// self.a`, `self.xs[i] = x` and, for a `mut []T` parameter, `xs[i] = x`
// need no clause (a store into the first level needs no clause).
// A type parameter in the selected part keeps the walk, since an instance
// may make it a view, unless it is bounded by Plain: by the kinds table
// every view, and anything holding one, is Unique, so a Plain instance is
// no view and holds none. This is a statement about the kind, not that a
// Plain T holds no loan (`&T` is Plain).
func (c *checker) ownInline(pi *pathInfo, si int, t rootID) bool {
	if si != 0 || pi.root >= rootID(len(c.body.Locals)) {
		return false
	}
	ri := &c.roots[pi.root]
	if ri.kind != rkParam || ri.isVar || ri.param == types.NoParam || int(ri.param) >= len(c.params) {
		return false
	}
	if pp := &c.params[ri.param]; !pp.carrier || pp.n1 != t {
		return false
	}
	if k := c.tab.Kind(ri.typ); k != types.KRef && k != types.KMut && k != types.KMutSlice {
		return false
	}
	last := si
	for j := si + 1; j < len(pi.steps) && pi.steps[j].kind != stDeref; j++ {
		switch pi.steps[j].kind {
		case stField, stVariant, stIndex:
		default:
			return false
		}
		last = j
	}
	// The type after the deref and every container up to the selected part:
	// none a view, and none a type parameter or Self, which an instance may
	// make a view.
	for j := si; j < last; j++ {
		if ct := pi.types[j]; (c.w.factsOf(ct).View && !c.w.factsOf(ct).PlainView) || c.typeVar(ct) {
			return false
		}
	}
	return last != si && !c.w.holdsNonPlainView(pi.types[last]) && !c.inlineParam(pi.types[last], map[types.TypeID]bool{})
}

// inlineParam reports whether t, or a type inline in it, is a type
// parameter or Self, which an instance may make a view. A type parameter
// bounded by Plain may not be one (every view is Unique).
func (c *checker) inlineParam(t types.TypeID, seen map[types.TypeID]bool) bool {
	if seen[t] {
		return false
	}
	seen[t] = true
	if c.typeVar(t) {
		return c.tab.Kind(t) != types.KParam || !c.w.plain(t)
	}
	for _, f := range c.w.inlineFields(t) {
		if c.inlineParam(f, seen) {
			return true
		}
	}
	return false
}

// typeVar reports whether t is a type parameter or Self.
func (c *checker) typeVar(t types.TypeID) bool {
	k := c.tab.Kind(t)
	return k == types.KParam || k == types.KSelf
}

// argRoot is the frame root an argument's own loans target (the local a
// `mut l` argument reborrows), or noRoot.
func (c *checker) argRoot(st *state, own setID) rootID {
	for _, l := range c.sets.get(own) {
		if t := c.loans[l].target; t != noRoot && c.roots[t].frame() {
			return t
		}
	}
	return noRoot
}

// viaOutlives is E0505's tail when the store reached t through a local's
// held loan: ": into `*sys` (`files` holds a loan of `*sys`, so the store
// reaches it)".
func (c *checker) viaOutlives(t rootID) string {
	v := c.viaText(t)
	if v == "" {
		return ""
	}
	return ": into `" + c.rootName(t) + "`" + v
}

// allocSelf is S without the loans that allocator root t holds already by
// being itself (the allocator self filter): a loan whose target is t, and for
// a parameter's allocator node, an abstract loan of that parameter. For a
// hidden environment parameter's allocator node, distinct environment fields
// are independent captures with independent scopes, so only a tagged loan
// targeting t from the exact same single capture field that reached t is
// dropped (never another capture's loan or a shorter-lived arena). The
// call rule stores into an allocator through it, so a loan of an arena
// stored into that arena (or a value of it into its own contents) is no
// store of the arena into itself. What it drops comes back on the read
// side: B keeps a loan of an allocator (behind).
func (c *checker) allocSelf(t rootID, S setID) setID {
	ti := &c.roots[t]
	var drop []uint32
	if ti.kind == rkAbstract && c.hiddenParam(ti.param) {
		f, ok := c.capField[t]
		if !ok || f < 0 {
			return S
		}
		for _, l := range c.sets.get(S) {
			lo := &c.loans[l]
			sameField := lo.abstract && lo.param == ti.param && lo.inner && len(lo.tag) > 0 && int(lo.tag[0]) == f
			if sameField && (lo.target == t || c.csrConst) {
				drop = append(drop, l)
			}
		}
	} else {
		for _, l := range c.sets.get(S) {
			lo := &c.loans[l]
			if lo.target == t || ti.kind == rkAbstract && lo.abstract && lo.param == ti.param {
				drop = append(drop, l)
			}
		}
	}
	if len(drop) == 0 {
		return S
	}
	return c.sets.minus(S, c.sets.fromUnsorted(drop))
}

// memHop reports whether the writable walk's hop from root src through loan m
// lands in the memory of the allocator src: src is p#1 of a memNode parameter
// p (a: &Arena: p#1 the arena alone, p#2 ANY alone, the contents the arena's
// view reaches), and m is p.inner, so the hop lands on p#2, the arena's
// memory. A node of several allocators (c: Ctx { a: &Arena, b: &Arena }) or of
// an allocator beside other parts (n1A, p#A) is no memNode, since there p#1 or
// p#2 stands for more than one referent.
func (c *checker) memHop(src rootID, m loanID) bool {
	ri := &c.roots[src]
	if ri.kind != rkAbstract || ri.depth != 1 || ri.param == types.NoParam || int(ri.param) >= len(c.params) {
		return false
	}
	pp := &c.params[ri.param]
	return pp.memNode && pp.n1 == src && m == pp.inner
}

// arenaRef reports whether t, a parameter's declared type, is a reference
// to one Arena: &Arena or mut Arena. Alloc and Heap are left out: the rule
// holds for the Arena, whose view ANY is its memory.
func (c *checker) arenaRef(t types.TypeID) bool {
	ty := c.tab.Get(t)
	if (ty.Kind != types.KRef && ty.Kind != types.KMut) || c.env.Known == nil || c.env.Known.Arena == types.NoItem {
		return false
	}
	e := c.tab.Get(ty.Elem)
	return e.Kind == types.KNamed && e.Item == c.env.Known.Arena
}

// memSelf is S without the loans allocator node a holds by being itself, for
// a store into a's memory node t that the writable walk reached only through
// a's own memory hop (memHop): a loan of the arena, and the parameter's own
// inner loan (what a value read back out of the arena's memory holds), stored
// into the arena's memory die with it. A loan of anything else is stored as
// usual, and so is every loan stored into t when a hop of another kind also
// reaches t.
func (c *checker) memSelf(a rootID, S setID) setID {
	var drop []uint32
	for _, l := range c.sets.get(S) {
		if lo := &c.loans[l]; lo.target == a || lo.abstract && lo.inner && lo.param == c.roots[a].param {
			drop = append(drop, l)
		}
	}
	if len(drop) == 0 {
		return S
	}
	return c.sets.minus(S, c.sets.fromUnsorted(drop))
}
