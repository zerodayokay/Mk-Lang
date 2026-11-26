// Why: compile-time evaluation runs tight loops (tables, gen bodies), and
// walking MIR statement by statement with a checked access each time is too
// slow for them.
//
// What: the flat form of a body: instr, tinstr and cblock, built by flatOp,
// flatStmt and flatTerm, and run by exec, runBlock, runTerm and
// int64Inline; giveBack undoes a block's pre-charge on a stop.
//
// How: each body is compiled once into per-block instructions with frame
// offsets, widths and type info precomputed. The hot statement kinds run on
// the frame's own allocation with no map lookup or access check: the
// allocation is live and the offsets lie inside it by construction. A block
// whose whole static cost fits the fuel is charged once on entry; a stop
// inside it gives back what did not start, from the block's prefix sums, so
// Steps is exactly what statement-by-statement charging gives. Anything
// else is iSlow or tSlow and takes the general path in exec.go.

package eval

import (
	"mk/internal/mir"
	"mk/internal/types"
)

// ikind is a flat statement's kind. iSlow runs the MIR statement through the
// general path (exec.go); every other kind reads and writes only bare scalar
// locals of the frame and immediates.
type ikind uint8

const (
	iSlow ikind = iota
	iNop
	iMove
	iBinInt
	// iInt64: iBinInt with both operands 64-bit integers, run with the
	// common operators inline (runBlock).
	iInt64
	iBin
	iUn
	iCast
)

// fop is a flat scalar operand: an immediate (a scalar OpImm, or a scalar
// OpConst with no relocation, whose bytes are its value) or a bare local at
// a frame offset.
type fop struct {
	imm  bool
	off  uint32
	bits uint64
	ti   *tinfo
}

// instr is one statement in flat form.
type instr struct {
	kind ikind
	bin  mir.BinOp
	un   mir.UnOp
	cast mir.CastKind
	dst  uint32
	dti  *tinfo
	a, b fop
	// bt is the type of the right operand where it matters (shifts), else
	// a's.
	bt *tinfo
}

// tkind is a flat terminator's kind; tSlow runs it through exec.go.
type tkind uint8

const (
	tSlow tkind = iota
	tGoto
	tSwitch
)

type tinstr struct {
	kind   tkind
	target mir.BlockID
	def    mir.BlockID
	on     fop
	mask   uint64
	cases  []mir.SwitchCase
}

// cblock is one block in flat form with its static costs: cost[i] is
// statement i's, cost[len(ins)] the terminator's, prefix[i] the sum of
// cost[:i], total = prefix[len(ins)+1].
type cblock struct {
	ins    []instr
	term   tinstr
	cost   []uint64
	prefix []uint64
	total  uint64
	// trace: the terminator calls std.rt.TraceFail or TraceTry, which the
	// evaluator runs as a no-op at no cost (traceHook).
	trace bool
	// pre: the block may be charged once on entry. False when a statement
	// reads a const or global through RvConstRef or RvGlobalRef, which may
	// push a nested segment before the statement is charged.
	pre bool
}

// fastClass reports whether a value of ti can be a flat operand: integers
// (not enum tags, whose loads check the variant), floats, bool and char.
func fastClass(ti *tinfo) bool {
	switch ti.class {
	case cInt:
		return !ti.enum
	case cFloat, cBool, cChar:
		return true
	}
	return false
}

// flatOp builds the flat form of a scalar operand, or reports that it has
// none.
func (e *Evaluator) flatOp(cd *compiled, op *mir.Operand) (fop, bool) {
	switch op.Kind {
	case mir.OpCopy, mir.OpMove:
		if op.Place.Proj.Len != 0 {
			return fop{}, false
		}
		ti := cd.localTI[op.Place.Local]
		if !fastClass(ti) {
			return fop{}, false
		}
		return fop{off: cd.localOff[op.Place.Local], ti: ti}, true
	case mir.OpImm:
		ti := e.tinfo(op.Type)
		if !fastClass(ti) {
			return fop{}, false
		}
		return fop{imm: true, bits: op.Bits, ti: ti}, true
	case mir.OpConst:
		ti := e.tinfo(op.Type)
		if !fastClass(ti) {
			return fop{}, false
		}
		v := e.cfg.Consts.Get(op.Const)
		if len(v.Relocs) != 0 || len(v.Bytes) < ti.width {
			return fop{}, false
		}
		x := types.LoadUint(v.Bytes, ti.width)
		if !validScalar(ti, x) {
			return fop{}, false // the general path refuses it at run time
		}
		return fop{imm: true, bits: x, ti: ti}, true
	}
	return fop{}, false
}

// flatStmt builds the flat form of a statement, iSlow when it has none.
func (e *Evaluator) flatStmt(cd *compiled, st *mir.Statement) instr {
	switch st.Kind {
	case mir.StNop, mir.StScopeEnd, mir.StConsume:
		return instr{kind: iNop}
	case mir.StAssign:
	default:
		return instr{}
	}
	if st.Place.Proj.Len != 0 {
		return instr{}
	}
	dti := cd.localTI[st.Place.Local]
	if !fastClass(dti) {
		return instr{}
	}
	in := instr{dst: cd.localOff[st.Place.Local], dti: dti}
	r := &st.Rvalue
	var ok bool
	switch r.Kind {
	case mir.RvUse:
		in.kind = iMove
		in.a, ok = e.flatOp(cd, &r.A)
	case mir.RvBinary:
		var okb bool
		in.a, ok = e.flatOp(cd, &r.A)
		in.b, okb = e.flatOp(cd, &r.B)
		ok = ok && okb
		if !ok {
			break
		}
		in.bin, in.bt = r.Bin, in.a.ti
		if r.Bin == mir.Shl || r.Bin == mir.Shr {
			in.bt = in.b.ti
		}
		in.kind = iBin
		if in.a.ti.class == cInt {
			in.kind = iBinInt
			if in.a.ti.width == 8 && in.b.ti.width == 8 && in.bt.class == cInt && (dti.width == 8 || dti.width == 1) {
				in.kind = iInt64
			}
		}
	case mir.RvUnary:
		in.kind, in.un = iUn, r.Un
		in.a, ok = e.flatOp(cd, &r.A)
	case mir.RvCast:
		if r.Cast == mir.CastWidenErrors || r.Cast == mir.CastNarrow {
			break
		}
		in.kind, in.cast = iCast, r.Cast
		in.a, ok = e.flatOp(cd, &r.A)
	}
	if !ok {
		return instr{}
	}
	return in
}

// flatTerm builds the flat form of a terminator, tSlow when it has none.
func (e *Evaluator) flatTerm(cd *compiled, t *mir.Terminator) tinstr {
	switch t.Kind {
	case mir.TermGoto:
		return tinstr{kind: tGoto, target: t.Target}
	case mir.TermSwitch:
		on, ok := e.flatOp(cd, &t.On)
		if !ok {
			return tinstr{}
		}
		b := cd.body
		return tinstr{kind: tSwitch, on: on, mask: mask(on.ti.width), def: t.Default,
			cases: b.Cases[t.Cases.Start:t.Cases.End()]}
	}
	// Every other terminator, a TermTailCall included, is tSlow:
	// step runs it.
	return tinstr{}
}

// validScalar is the validity check a load of bool or char makes.
func validScalar(ti *tinfo, x uint64) bool {
	switch ti.class {
	case cBool:
		return x <= 1
	case cChar:
		return x <= 0x10ffff && (x < 0xd800 || x > 0xdfff)
	}
	return true
}

// rd reads a flat operand from frame allocation al (checked: no pointer
// word or fragment under the bytes, bool and char validity).
func rd(al *alloc, o *fop) uint64 {
	if o.imm {
		return o.bits
	}
	var x uint64
	if al.ptr == nil && al.frag == nil {
		x = loadW(al.bytes[o.off:], o.ti.width)
	} else {
		x = al.loadScalar(o.off, o.ti.width)
	}
	if o.ti.class == cBool || o.ti.class == cChar {
		if !validScalar(o.ti, x) {
			if o.ti.class == cBool {
				refuse(types.RefuseRawAccess, "bool")
			}
			refuse(types.RefuseRawAccess, "char")
		}
	}
	return x
}

// wr stores a scalar of width w at offset off of frame allocation al.
func wr(al *alloc, off uint32, w int, x uint64) {
	if al.ptr == nil && al.frag == nil {
		storeW(al.bytes[off:], w, x)
		return
	}
	al.storeScalar(off, w, x)
}

func loadW(b []byte, w int) uint64 {
	switch w {
	case 8:
		_ = b[7]
		return uint64(b[0]) | uint64(b[1])<<8 | uint64(b[2])<<16 | uint64(b[3])<<24 |
			uint64(b[4])<<32 | uint64(b[5])<<40 | uint64(b[6])<<48 | uint64(b[7])<<56
	case 1:
		return uint64(b[0])
	}
	return types.LoadUint(b, w)
}

func storeW(b []byte, w int, x uint64) {
	switch w {
	case 8:
		_ = b[7]
		b[0], b[1], b[2], b[3] = byte(x), byte(x>>8), byte(x>>16), byte(x>>24)
		b[4], b[5], b[6], b[7] = byte(x>>32), byte(x>>40), byte(x>>48), byte(x>>56)
	case 1:
		b[0] = byte(x)
	default:
		types.StoreUint(b, w, x)
	}
}

// exec runs one flat statement of frame f; its static cost is charged.
func (m *Machine) exec(f *frame, in *instr) {
	al := &m.mem.data[f.d]
	var v uint64
	switch in.kind {
	case iNop:
		return
	case iMove:
		v = rd(al, &in.a)
	case iBinInt, iInt64:
		x, y := rd(al, &in.a), rd(al, &in.b)
		var pc types.PanicCode
		v, pc = intBinary(in.bin, in.a.ti.width, in.a.ti.signed, in.bt, x, y)
		trap(pc)
	case iBin:
		x, y := rd(al, &in.a), rd(al, &in.b)
		var pc types.PanicCode
		v, pc = binaryOp(in.bin, in.a.ti, in.bt, x, y)
		trap(pc)
	case iUn:
		var pc types.PanicCode
		v, pc = unary(in.un, in.a.ti, rd(al, &in.a))
		trap(pc)
	case iCast:
		var pc types.PanicCode
		v, pc = cast(in.cast, in.a.ti, in.dti, rd(al, &in.a))
		trap(pc)
	}
	wr(al, in.dst, in.dti.width, v)
}

// runBlock runs a pre-charged block of the top frame f from its first
// statement through its terminator. f.stmt names the statement running, so a
// stop gives back the rest (runLoop) and the trace names the right span.
//
// When the terminator is a flat goto or switch, the frame stays on top, and
// the successor can be pre-charged by runLoop's own test (cb.pre and its
// total fits the fuel left), the successor is charged and run here, with
// m.preBlock naming it, without returning to runLoop. Charges,
// the order of statements and every stop are what runLoop's next turn would
// give: runLoop would take the same branch for that block.
func (m *Machine) runBlock(f *frame, cb *cblock) {
	for {
		var stmts []mir.Statement
		for k := range cb.ins {
			f.stmt = k
			in := &cb.ins[k]
			switch in.kind {
			case iSlow:
				if stmts == nil {
					stmts = f.code.body.Blocks[f.block].Stmts
				}
				m.stmt(f, &stmts[k])
				continue
			case iInt64:
				if al := &m.mem.data[f.d]; al.ptr == nil && al.frag == nil && int64Inline(al.bytes, in) {
					continue
				}
			}
			m.exec(f, in)
		}
		f.stmt = len(cb.ins)
		if cb.term.kind == tSlow {
			m.runTerm(f, cb) // may push or pop a frame: f is stale after it
			return
		}
		m.runTerm(f, cb)
		next := &f.code.blocks[f.block]
		if !next.pre {
			return
		}
		s := m.seg()
		if next.total > s.fuel-s.steps {
			return
		}
		s.steps += next.total
		m.preBlock = next
		m.npre++
		cb = next
	}
}

// runTerm runs the terminator of block cb of frame f; its static cost is
// charged.
func (m *Machine) runTerm(f *frame, cb *cblock) {
	t := &cb.term
	switch t.kind {
	case tGoto:
		f.block, f.stmt = t.target, 0
	case tSwitch:
		x := rd(&m.mem.data[f.d], &t.on) & t.mask
		for i := range t.cases {
			if t.cases[i].Value&t.mask == x {
				f.block, f.stmt = t.cases[i].Target, 0
				return
			}
		}
		if t.def == mir.NoBlock {
			panicWith(types.PanicUnreachable, "")
		}
		f.block, f.stmt = t.def, 0
	default:
		m.term(f, &f.code.body.Blocks[f.block].Term)
	}
}

// giveBack returns the static cost of the statements and terminator of the
// pre-charged block that did not start when the evaluation stopped inside it.
func (m *Machine) giveBack() {
	cb := m.preBlock
	m.preBlock = nil
	if cb == nil || m.preFrame >= len(m.frames) {
		return
	}
	k := m.frames[m.preFrame].stmt
	if n := cb.total - cb.prefix[k+1]; n != 0 {
		m.seg().steps -= n
	}
}

// int64Inline runs the common operators of an iInt64 statement on frame
// bytes b that hold no pointer word or fragment; false leaves the statement
// to exec. The traps are intBinary's.
func int64Inline(b []byte, in *instr) bool {
	x, y := in.a.bits, in.b.bits
	if !in.a.imm {
		x = loadW(b[in.a.off:], 8)
	}
	if !in.b.imm {
		y = loadW(b[in.b.off:], 8)
	}
	signed := in.a.ti.signed
	var v uint64
	switch in.bin {
	case mir.AddWrap, mir.AddNoWrap: // proved in range by its producer, so it runs as AddWrap
		v = x + y
	case mir.SubWrap, mir.SubNoWrap: // as AddNoWrap
		v = x - y
	case mir.Add:
		v = x + y
		if signed {
			if (int64(x)^int64(v))&(int64(y)^int64(v)) < 0 {
				return false // exec traps
			}
		} else if v < x {
			return false
		}
	case mir.Sub:
		v = x - y
		if signed {
			if (int64(x)^int64(y))&(int64(x)^int64(v)) < 0 {
				return false
			}
		} else if y > x {
			return false
		}
	case mir.Eq:
		v = b2u(x == y)
	case mir.Ne:
		v = b2u(x != y)
	case mir.Lt:
		if signed {
			v = b2u(int64(x) < int64(y))
		} else {
			v = b2u(x < y)
		}
	case mir.Le:
		if signed {
			v = b2u(int64(x) <= int64(y))
		} else {
			v = b2u(x <= y)
		}
	case mir.Gt:
		if signed {
			v = b2u(int64(x) > int64(y))
		} else {
			v = b2u(x > y)
		}
	case mir.Ge:
		if signed {
			v = b2u(int64(x) >= int64(y))
		} else {
			v = b2u(x >= y)
		}
	default:
		return false
	}
	storeW(b[in.dst:], in.dti.width, v)
	return true
}
