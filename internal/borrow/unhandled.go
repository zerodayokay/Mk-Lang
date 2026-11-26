// Why: an E!T is handled where it is made, or kept in a binding that writes
// its type to be handled later; the checker judges the expressions, but
// only a path-sensitive pass sees a binding overwritten, or ending its
// scope, while the error it may hold was never read.
//
// What: unhandledUnions, the E0802 check of every named local of an error
// union type: assigned while it may hold an unread value, or ending its
// scope so.
//
// How: one forward may-dataflow over the reachable blocks, a bit per such
// local: a whole assignment sets it (after reporting a set one), a read of
// the value (a copy or move of a place it roots, its length or
// discriminant) clears it. A shared borrow alone does not read it. A
// dereference or handoff at a call or return can clear it through the
// reference. A mut borrow sets it again, since a fn literal's capture
// may assign the binding. A scope end reports a set bit. A narrowed
// copy made by an `if T is X` branch is the same binding as its source.

package borrow

import (
	"mk/internal/diag"
	"mk/internal/mir"
	"mk/internal/types"
)

// unionBits is the may-hold-an-unread-value set, a bit per tracked local.
type unionBits []uint64

func (b unionBits) has(i int) bool { return b[i/64]&(1<<(i%64)) != 0 }
func (b unionBits) set(i int)      { b[i/64] |= 1 << (i % 64) }
func (b unionBits) clear(i int)    { b[i/64] &^= 1 << (i % 64) }

// unionFlow is the pass's per-body tables: index maps a local to its
// binding's bit (-1 for none), owner to the binding a narrowed copy is.
type unionFlow struct {
	index []int
	owner []mir.LocalID
}

// unionState keeps the unread bindings on any path and the bindings each
// local holds a reference to on every incoming path.
type unionState struct {
	unread unionBits
	refs   []setID
}

// unhandledUnions reports E0802 (phase flow) where a named local of an
// error union type is assigned, or its scope ends, while it may still hold
// a value no try, else, match or .Must() has read.
func (c *checker) unhandledUnions() {
	b := c.body
	hasUnion := false
	for _, l := range b.Locals {
		if l.Kind == mir.LocalUser && c.tab.Kind(l.Type) == types.KErrorUnion {
			hasUnion = true
			break
		}
	}
	if !hasUnion {
		return
	}
	f := &unionFlow{index: make([]int, len(b.Locals)), owner: make([]mir.LocalID, len(b.Locals))}
	for i := range f.owner {
		f.owner[i] = mir.LocalID(i)
	}
	for _, bi := range c.rpo {
		for i := range b.Blocks[bi].Stmts {
			if from, to, ok := narrowCopy(&b.Blocks[bi].Stmts[i]); ok && f.owner[to] == to {
				f.owner[to] = f.owner[from]
			}
		}
	}
	n := 0
	for i, l := range b.Locals {
		f.index[i] = -1
		if f.owner[i] == mir.LocalID(i) && l.Kind == mir.LocalUser && c.tab.Kind(l.Type) == types.KErrorUnion {
			f.index[i] = n
			n++
		}
	}
	if n == 0 {
		return
	}
	for i := range f.index {
		f.index[i] = f.index[f.owner[i]]
	}
	entry := make([]unionState, len(b.Blocks))
	entry[mir.EntryBlock] = unionState{make(unionBits, (n+63)/64), make([]setID, len(b.Locals))}
	run := func(bi mir.BlockID, report bool) unionState {
		s := unionState{
			append(unionBits(nil), entry[bi].unread...),
			append([]setID(nil), entry[bi].refs...),
		}
		blk := &b.Blocks[bi]
		for i := range blk.Stmts {
			c.unionStmt(f, &blk.Stmts[i], s, report)
		}
		c.unionTerm(f, &blk.Term, s, report)
		return s
	}
	var succ []mir.BlockID
	for changed := true; changed; {
		changed = false
		for _, bi := range c.rpo {
			if entry[bi].unread == nil {
				continue
			}
			out := run(bi, false)
			succ = b.Successors(bi, succ[:0])
			for _, sb := range succ {
				e := &entry[sb]
				if e.unread == nil {
					e.unread = append(unionBits(nil), out.unread...)
					e.refs = append([]setID(nil), out.refs...)
					changed = true
					continue
				}
				for w, word := range out.unread {
					if m := e.unread[w] | word; m != e.unread[w] {
						e.unread[w], changed = m, true
					}
				}
				for l, refs := range out.refs {
					if m := c.sets.meet(e.refs[l], refs); m != e.refs[l] {
						e.refs[l], changed = m, true
					}
				}
			}
		}
	}
	for _, bi := range c.rpo {
		if entry[bi].unread != nil {
			run(bi, true)
		}
	}
}

// narrowCopy reports `to = Narrow(copy from)` (or move) between two whole
// locals, with from the older: the retyping an `if T is X` branch makes of
// a local, or its write-back.
func narrowCopy(st *mir.Statement) (from, to mir.LocalID, ok bool) {
	rv := &st.Rvalue
	if st.Kind != mir.StAssign || st.Place.Proj.Len != 0 || rv.Kind != mir.RvCast || rv.Cast != mir.CastNarrow {
		return 0, 0, false
	}
	if (rv.A.Kind != mir.OpCopy && rv.A.Kind != mir.OpMove) || rv.A.Place.Proj.Len != 0 {
		return 0, 0, false
	}
	from, to = rv.A.Place.Local, st.Place.Local
	if from > to {
		from, to = to, from
	}
	return from, to, true
}

// unionStmt is one statement's transfer: its reads, then its def.
func (c *checker) unionStmt(f *unionFlow, st *mir.Statement, s unionState, report bool) {
	switch st.Kind {
	case mir.StAssign:
		if _, _, ok := narrowCopy(st); ok {
			s.refs[st.Place.Local] = s.refs[st.Rvalue.A.Place.Local]
			return // the binding and its narrowed copy are one
		}
		c.unionRvalue(f, &st.Rvalue, s)
		refs := c.unionRefs(f, &st.Rvalue, s)
		if rv := &st.Rvalue; (rv.Kind == mir.RvRef || rv.Kind == mir.RvSlice) && rv.Borrow == mir.Exclusive && !c.hasDeref(rv.Place) {
			// A mut borrow may write the binding (a fn literal's capture
			// assigns it): it may overwrite the value, and leaves one.
			c.unionDef(f, rv.Place.Local, st.Span, s.unread, report, "borrowed `mut`")
			s.refs[rv.Place.Local] = emptySet
		}
		if !c.hasDeref(st.Place) {
			l := st.Place.Local
			s.refs[l] = emptySet
			if st.Place.Proj.Len == 0 {
				c.unionDef(f, l, st.Span, s.unread, report, "assigned")
				s.refs[l] = refs
				if l == 0 {
					c.unionHandOff(refs, s.unread)
				}
			}
		}
	case mir.StAsm:
		ops := c.body.Asms[st.Asm].Operands
		for _, op := range ops {
			switch op.Kind {
			case mir.AsmIn, mir.AsmInOut, mir.AsmInLateOut:
				c.unionOperand(f, op.In, s)
			}
		}
		for _, op := range ops {
			switch op.Kind {
			case mir.AsmOut, mir.AsmLateOut, mir.AsmInOut, mir.AsmInLateOut:
				if op.Out.Local != mir.NoLocal && !c.hasDeref(op.Out) {
					s.refs[op.Out.Local] = emptySet
					if op.Out.Proj.Len == 0 {
						c.unionDef(f, op.Out.Local, st.Span, s.unread, report, "assigned")
					}
				}
			}
		}
	case mir.StScopeEnd:
		l := st.Local
		s.refs[l] = emptySet
		i := f.index[l]
		if i < 0 || f.owner[l] != l || !s.unread.has(i) {
			return // a narrowed copy's scope end is not its binding's
		}
		s.unread.clear(i)
		if report {
			name := c.rootName(rootID(l))
			c.errAt(diag.E0802, c.body.Locals[l].Span, "`%s` may still hold an error that no `try`, `else`, `match` or `.Must()` has read when its scope ends", name).
				Text("place", name).Text("type", c.tab.TypeString(c.body.Locals[l].Type, c.unionGens())).
				Loc("path", c.body.Span(st.Span)).In(diag.PhaseFlow).
				Alt("handle it on every path before its scope ends: `" + name + " else e { ... }`, a `match`, `.Must()` or `try`")
		}
	}
}

func (c *checker) unionGens() []types.GenericParam {
	if c.env != nil && c.env.Sig != nil {
		return c.env.Sig.Generics
	}
	return nil
}

// unionDef is a whole assignment to local l, or a mut borrow of it (how
// says which): E0802 when its binding may still hold an unread value; then
// it holds one.
func (c *checker) unionDef(f *unionFlow, l mir.LocalID, at mir.SpanID, s unionBits, report bool, how string) {
	if int(l) >= len(f.index) || f.index[l] < 0 {
		return
	}
	i := f.index[l]
	if report && s.has(i) {
		o := f.owner[l]
		name := c.rootName(rootID(o))
		c.errAt(diag.E0802, at, "`%s` is %s while it may still hold an error that no `try`, `else`, `match` or `.Must()` has read", name, how).
			Text("place", name).Text("type", c.tab.TypeString(c.body.Locals[o].Type, c.unionGens())).
			Loc("declared_at", c.body.Span(c.body.Locals[o].Span)).In(diag.PhaseFlow).
			Alt("handle the value it holds first: `" + name + " else e { ... }`, a `match`, `.Must()` or `try`")
	}
	s.set(i)
}

func (c *checker) unionRvalue(f *unionFlow, rv *mir.Rvalue, s unionState) {
	switch rv.Kind {
	case mir.RvUse, mir.RvUnary, mir.RvCast, mir.RvRepeat:
		c.unionOperand(f, rv.A, s)
	case mir.RvBinary:
		c.unionOperand(f, rv.A, s)
		c.unionOperand(f, rv.B, s)
	case mir.RvLen, mir.RvDiscriminant:
		c.unionRead(f, rv.Place, s)
	case mir.RvRef, mir.RvSlice:
		if rv.Kind == mir.RvRef {
			return
		}
		if rv.HasLo {
			c.unionOperand(f, rv.A, s)
		}
		if rv.HasHi {
			c.unionOperand(f, rv.B, s)
		}
	case mir.RvAggregate:
		for _, o := range c.body.Operands[rv.Ops.Start:rv.Ops.End()] {
			c.unionOperand(f, o, s)
		}
	}
}

func (c *checker) unionOperand(f *unionFlow, o mir.Operand, s unionState) {
	if o.Kind == mir.OpCopy || o.Kind == mir.OpMove {
		c.unionRead(f, o.Place, s)
	}
}

// unionRead clears a binding's obligation on a read of it, whether the
// place names it directly or dereferences a known reference to it.
func (c *checker) unionRead(f *unionFlow, p mir.Place, s unionState) {
	if c.hasDeref(p) {
		if c.unionRefType(p.Local) && c.body.Projections[p.Proj.Start].Kind == mir.ProjDeref {
			c.unionHandOff(s.refs[p.Local], s.unread)
		}
	} else if i := f.index[p.Local]; i >= 0 {
		s.unread.clear(i)
	}
}

// unionRefType distinguishes a reference to a union from a reference to
// something that merely holds loans of unions. Reading one field of the
// latter does not read all the unions.
func (c *checker) unionRefType(l mir.LocalID) bool {
	t := c.tab.Get(c.body.Locals[l].Type)
	return t.Kind == types.KRef && c.tab.Kind(t.Elem) == types.KErrorUnion
}

func unionOperandRefs(o mir.Operand, s unionState) setID {
	if (o.Kind == mir.OpCopy || o.Kind == mir.OpMove) && o.Place.Proj.Len == 0 {
		return s.refs[o.Place.Local]
	}
	return emptySet
}

// unionRefs follows whole-place copies and aggregates, including a literal's
// captures. A projection of an aggregate cannot stand for all its loans.
func (c *checker) unionRefs(f *unionFlow, rv *mir.Rvalue, s unionState) setID {
	switch rv.Kind {
	case mir.RvUse, mir.RvRepeat:
		return unionOperandRefs(rv.A, s)
	case mir.RvRef:
		if rv.Borrow != mir.Exclusive {
			p := rv.Place
			switch {
			case p.Proj.Len == 0:
				if i := f.index[p.Local]; i >= 0 {
					return c.sets.of(uint32(i))
				}
				return s.refs[p.Local]
			case p.Proj.Len == 1 && c.body.Projections[p.Proj.Start].Kind == mir.ProjDeref:
				return s.refs[p.Local] // reborrow of the entire referent
			}
		}
	case mir.RvAggregate:
		var refs setID
		for _, o := range c.body.Operands[rv.Ops.Start:rv.Ops.End()] {
			refs = c.sets.unite(refs, unionOperandRefs(o, s))
		}
		return refs
	}
	return emptySet
}

func (c *checker) unionHandOff(refs setID, s unionBits) {
	for _, i := range c.sets.get(refs) {
		s.clear(int(i))
	}
}

func (c *checker) unionTerm(f *unionFlow, t *mir.Terminator, s unionState, report bool) {
	switch t.Kind {
	case mir.TermCall, mir.TermTailCall:
		for _, a := range c.body.Args[t.Args.Start:t.Args.End()] {
			c.unionOperand(f, a.Operand, s)
			c.unionHandOff(unionOperandRefs(a.Operand, s), s.unread)
		}
		if t.Callee.Kind == mir.CallValue {
			c.unionOperand(f, t.Callee.Value, s)
		}
		if t.Kind == mir.TermCall && !c.hasDeref(t.Dest) {
			s.refs[t.Dest.Local] = emptySet
			if t.Dest.Proj.Len == 0 {
				c.unionDef(f, t.Dest.Local, t.Span, s.unread, report, "assigned")
			}
		}
	case mir.TermSwitch:
		c.unionOperand(f, t.On, s)
	case mir.TermPanic:
		if t.HasMsg {
			c.unionOperand(f, t.Msg, s)
		}
	}
}
