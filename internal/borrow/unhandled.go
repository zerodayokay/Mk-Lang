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
// discriminant) clears it, as a borrow does (what reads through the
// reference handles it), a mut borrow then sets it again (a fn literal's
// capture may assign the binding), and a scope end reports a set one. A copy that an `if T is X` branch makes (a CastNarrow between
// two whole locals) is the same binding as its source. Bodies with no such
// local cost one scan of the locals.

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

// unhandledUnions reports E0802 (phase flow) where a named local of an
// error union type is assigned, or its scope ends, while it may still hold
// a value no try, else, match or .Must() has read.
func (c *checker) unhandledUnions() {
	b := c.body
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
	entry := make([]unionBits, len(b.Blocks))
	entry[mir.EntryBlock] = make(unionBits, (n+63)/64)
	run := func(bi mir.BlockID, report bool) unionBits {
		s := append(unionBits(nil), entry[bi]...)
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
			if entry[bi] == nil {
				continue
			}
			out := run(bi, false)
			succ = b.Successors(bi, succ[:0])
			for _, sb := range succ {
				if entry[sb] == nil {
					entry[sb] = append(unionBits(nil), out...)
					changed = true
					continue
				}
				for w := range out {
					if m := entry[sb][w] | out[w]; m != entry[sb][w] {
						entry[sb][w], changed = m, true
					}
				}
			}
		}
	}
	for _, bi := range c.rpo {
		if entry[bi] != nil {
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
func (c *checker) unionStmt(f *unionFlow, st *mir.Statement, s unionBits, report bool) {
	switch st.Kind {
	case mir.StAssign:
		if _, _, ok := narrowCopy(st); ok {
			return // the binding and its narrowed copy are one
		}
		c.unionRvalue(f, &st.Rvalue, s)
		if rv := &st.Rvalue; (rv.Kind == mir.RvRef || rv.Kind == mir.RvSlice) && rv.Borrow == mir.Exclusive && !c.hasDeref(rv.Place) {
			// A mut borrow may write the binding (a fn literal's capture
			// assigns it): it may overwrite the value, and leaves one.
			c.unionDef(f, rv.Place.Local, st.Span, s, report, "borrowed `mut`")
		}
		if st.Place.Proj.Len == 0 {
			c.unionDef(f, st.Place.Local, st.Span, s, report, "assigned")
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
				if op.Out.Local != mir.NoLocal && op.Out.Proj.Len == 0 {
					c.unionDef(f, op.Out.Local, st.Span, s, report, "assigned")
				}
			}
		}
	case mir.StScopeEnd:
		l := st.Local
		i := f.index[l]
		if i < 0 || f.owner[l] != l || !s.has(i) {
			return // a narrowed copy's scope end is not its binding's
		}
		s.clear(i)
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

func (c *checker) unionRvalue(f *unionFlow, rv *mir.Rvalue, s unionBits) {
	switch rv.Kind {
	case mir.RvUse, mir.RvUnary, mir.RvCast, mir.RvRepeat:
		c.unionOperand(f, rv.A, s)
	case mir.RvBinary:
		c.unionOperand(f, rv.A, s)
		c.unionOperand(f, rv.B, s)
	case mir.RvLen, mir.RvDiscriminant:
		c.unionRead(f, rv.Place, s)
	case mir.RvRef, mir.RvSlice:
		if rv.Borrow != mir.Exclusive {
			c.unionRead(f, rv.Place, s) // a mut borrow is unionStmt's
		}
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

func (c *checker) unionOperand(f *unionFlow, o mir.Operand, s unionBits) {
	if o.Kind == mir.OpCopy || o.Kind == mir.OpMove {
		c.unionRead(f, o.Place, s)
	}
}

// unionRead is a read of place p's value: its binding's, unless p goes
// through a deref (the value of another place).
func (c *checker) unionRead(f *unionFlow, p mir.Place, s unionBits) {
	if int(p.Local) < len(f.index) && f.index[p.Local] >= 0 && !c.hasDeref(p) {
		s.clear(f.index[p.Local])
	}
}

func (c *checker) unionTerm(f *unionFlow, t *mir.Terminator, s unionBits, report bool) {
	switch t.Kind {
	case mir.TermCall, mir.TermTailCall:
		for _, a := range c.body.Args[t.Args.Start:t.Args.End()] {
			c.unionOperand(f, a.Operand, s)
		}
		if t.Callee.Kind == mir.CallValue {
			c.unionOperand(f, t.Callee.Value, s)
		}
		if t.Kind == mir.TermCall && t.Dest.Proj.Len == 0 {
			c.unionDef(f, t.Dest.Local, t.Span, s, report, "assigned")
		}
	case mir.TermSwitch:
		c.unionOperand(f, t.On, s)
	case mir.TermPanic:
		if t.HasMsg {
			c.unionOperand(f, t.Msg, s)
		}
	}
}
