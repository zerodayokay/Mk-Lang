// Why: lower computes a const used as a place (C[i], C[..], &C[k], C.f)
// into a temporary, so `Sum(Tab[..])` would copy Tab into the frame on every
// call and slice the copy (30,720 bytes per read of std's crypto table
// BaseComb).
//
// What: constLocals (which temporaries are promoted to their constant),
// rawEscape (the bail-out) and assignedConst.
//
// How: a compiler temporary that only ever holds one constant aggregate has
// no alloca: its address is the constant's private unnamed_addr global, and
// its assignments emit nothing.
//
// LLVM removes such a copy only when every use is a load or a GEP
// (InstCombine's copy-from-constant rule); a call that takes the slice keeps
// it, and a dev build never removes it.
//
// Only a temporary is promoted. A user's local
// (`t := Tab`, `var t := Tab`) is the user's own memory: the language makes a raw
// write to it defined once no loan covers it (raw.Write(raw.Of(&t), x) after
// the loan of &t ended), and the constant's global is read-only, so binding
// the local to it would fault or lose the write. A temporary has no name:
// lower makes one for each use of the const, and that use is all that
// reaches it, so it stands for the const's value, as `&C` (RvGlobalRef, a
// reference to the const's own read-only global) already does: a raw write
// into it is a write into the const, which `&C` already sends to read-only
// memory, and while a reference made from it is used, a loan covers it
// (writing through a raw pointer to a place under a live loan is undefined
// behaviour). A temporary whose reference becomes a raw pointer in the body
// keeps its memory as well (rawEscape), so the pass never changes what such
// a body does.

package codegen

import (
	"mk/internal/mir"
	"mk/internal/types"
)

// constLocals is, per local of fg.b, the constant every assignment to it
// stores, or 0 when the local needs its own memory. A local is promoted when:
//   - it is a compiler temporary (mir.LocalTemp: never a user's named
//     local, a parameter or the return local), and its type is a
//     non-zero-sized aggregate that is Freeze (a shared borrow of it can
//     write no byte of it);
//   - every assignment to a place rooted at it is `L = const c` or
//     `L = constref C` of one constant c, to L itself (no projection);
//   - nothing else writes a place rooted at it: no exclusive borrow or
//     exclusive slice, no call destination, no error edge, no reborrowed
//     argument, and the body has no inline assembly (whose operands are
//     places it does not inspect);
//   - no value made from a shared borrow of a promoted local becomes a raw
//     pointer in the body (rawEscape; if one does, the body promotes
//     nothing).
//
// Reads (copies, moves, shared borrows and slices, lengths, discriminants)
// are what a constant allows, so every read sees c's bytes, as a fresh copy
// would; two promoted locals of one constant share an address, which only an
// address comparison in unsafe code could tell.
func (fg *fnGen) constLocals() []types.ConstID {
	g, b := fg.g, fg.b
	if len(b.Asms) > 0 {
		return nil
	}
	out := make([]types.ConstID, len(b.Locals))
	bad := make([]bool, len(b.Locals))
	any := false
	for li := range b.Locals {
		l := &b.Locals[li]
		r := g.reprOf(l.Type)
		bad[li] = l.Kind != mir.LocalTemp || li == int(mir.ReturnLocal) || li <= int(b.ParamCount) ||
			r.kind != rAgg || !g.env.Facts(l.Type).Freeze
	}
	write := func(p mir.Place) { bad[p.Local] = true }
	borrow := func(p mir.Place, k mir.BorrowKind) {
		if k != mir.Shared {
			bad[p.Local] = true
		}
	}
	for bi := range b.Blocks {
		blk := &b.Blocks[bi]
		for si := range blk.Stmts {
			s := &blk.Stmts[si]
			switch s.Kind {
			case mir.StAssign:
				rv := &s.Rvalue
				if c := fg.assignedConst(rv); c != 0 && s.Place.Proj.Len == 0 {
					l := s.Place.Local
					switch {
					case out[l] == 0:
						out[l] = c
						any = true
					case out[l] != c:
						bad[l] = true
					}
				} else {
					write(s.Place)
				}
				switch rv.Kind {
				case mir.RvRef, mir.RvSlice:
					borrow(rv.Place, rv.Borrow)
				}
			case mir.StScopeEnd, mir.StNop, mir.StJoin, mir.StConsume:
			default:
				return nil
			}
		}
		t := &blk.Term
		if isCall(t) {
			write(t.Dest)
			if t.HasErr {
				bad[t.Err.Error] = true
			}
			for _, a := range b.Args[t.Args.Start:t.Args.End()] {
				if a.Mode == mir.ArgReborrow && a.Operand.Kind != mir.OpConst && a.Operand.Kind != mir.OpImm {
					write(a.Operand.Place)
				}
			}
		}
	}
	if !any {
		return nil
	}
	any = false
	for li := range out {
		if out[li] == 0 {
			continue
		}
		r := g.reprOf(b.Locals[li].Type)
		cv := g.env.Consts().Get(out[li])
		// The global must be laid out as the local: same bytes, alignment at
		// least the type's (loads through the local assume r.align).
		if bad[li] || uint64(len(cv.Bytes)) != r.size || max(cv.Align, 1) < max(r.align, 1) {
			out[li] = 0
			continue
		}
		any = true
	}
	if !any || fg.rawEscape(out) {
		return nil
	}
	return out
}

// rawEscape reports whether a value made from a shared borrow or slice of a
// local with cand[l] != 0 may become a raw pointer in fg.b. Derived
// values are followed through every assignment that reads one (into the
// root of its destination) and through every call that takes one (into its
// destination, error local and spawn scope), to a fixed point, except into
// a local of a scalar type (Sum(t[..]) gives an integer). A derived
// value escapes when it is an argument of an intrinsic call (std.raw.Of,
// Data, StrData, Addr and the rest take or make raw pointers), or when a
// local holding one has a type that is not Freeze (a raw pointer, or a value
// holding one, returned by any call or made by a cast). A callee that is not
// an intrinsic is given only a loan; a raw pointer it makes from that loan
// points into the const's value (the file comment).
func (fg *fnGen) rawEscape(cand []types.ConstID) bool {
	g, b := fg.g, fg.b
	der := make([]bool, len(b.Locals))
	reads := func(op mir.Operand) bool {
		return (op.Kind == mir.OpCopy || op.Kind == mir.OpMove) && der[op.Place.Local]
	}
	makes := func(rv *mir.Rvalue) bool {
		switch rv.Kind {
		case mir.RvRef, mir.RvSlice:
			if cand[rv.Place.Local] != 0 || der[rv.Place.Local] {
				return true
			}
		case mir.RvAggregate:
			for _, op := range b.Operands[rv.Ops.Start:rv.Ops.End()] {
				if reads(op) {
					return true
				}
			}
		}
		return reads(rv.A) || reads(rv.B)
	}
	for changed := true; changed; {
		changed = false
		mark := func(l mir.LocalID) {
			switch g.tt.Get(b.Locals[l].Type).Kind {
			case types.KInt, types.KIntPtr, types.KFloat, types.KBool, types.KChar, types.KVoid, types.KNever,
				types.KErrorSet:
				// A scalar holds no pointer: making one an address needs
				// raw.Addr, an intrinsic, which this walk stops at.
				return
			}
			if !der[l] {
				der[l] = true
				changed = true
			}
		}
		for bi := range b.Blocks {
			blk := &b.Blocks[bi]
			for si := range blk.Stmts {
				if s := &blk.Stmts[si]; s.Kind == mir.StAssign && makes(&s.Rvalue) {
					mark(s.Place.Local)
				}
			}
			t := &blk.Term
			if !isCall(t) {
				continue
			}
			hit := false
			for _, a := range b.Args[t.Args.Start:t.Args.End()] {
				hit = hit || reads(a.Operand)
			}
			if !hit {
				continue
			}
			if t.Callee.Kind == mir.CallDirect {
				if fd, ok := g.items.Get(t.Callee.Fn.Item).Decl.(*types.FuncDecl); ok && fd.Intrinsic {
					return true
				}
			}
			mark(t.Dest.Local)
			if t.HasErr {
				mark(t.Err.Error)
			}
			if t.Spawn != mir.NoLocal {
				mark(t.Spawn)
			}
		}
	}
	for li, d := range der {
		if t := b.Locals[li].Type; d && (g.tt.Get(t).Kind == types.KRawPtr || !g.env.Facts(t).Freeze) {
			return true
		}
	}
	return false
}

// assignedConst is the constant rv stores when it is exactly a constant
// aggregate (`const c` or `constref C`), else 0.
func (fg *fnGen) assignedConst(rv *mir.Rvalue) types.ConstID {
	switch {
	case rv.Kind == mir.RvUse && rv.A.Kind == mir.OpConst:
		return rv.A.Const
	case rv.Kind == mir.RvConstRef:
		if cd, ok := fg.g.items.Get(rv.Item).Decl.(*types.ConstDecl); ok && cd.HasValue {
			return cd.Value
		}
	}
	return 0
}
