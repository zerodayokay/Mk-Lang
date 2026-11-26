// Why: a requires clause that reads only const parameters, consts and
// literals has one value per instance, yet lower's entry check calls its
// predicate on every entry: every dev and checked build, and every
// compile-time evaluation, pays for a clause whose answer is fixed.
//
// What: the per-instance fold of a requires predicate call
// (isRequires, requiresHolds, foldPredicate): a call proved true becomes the
// assignment of true and its switch a goto, as HasFree folds.
//
// How: the fold judges the clause by its value in this instance, never by
// which route the checker took at the calls: every argument of the call
// must be a constant or the shared reference lower passes to a const value
// parameter, the predicate's instance body is built with them, and
// a small interpreter runs it over integer, bool and char immediates
// (compares, bit and bool operators, + - * and negation within their type,
// SizeOf and AlignOf, lossless casts, goto, switch and return). Anything
// else (a call, a memory read, a const reference, an operation that would
// trap, a loop past the step bound) keeps the call as lower emitted it, so a
// clause that is false, traps or cannot be computed here is still checked
// on entry. The folded instance body holds no call of the predicate, so
// what the fold read is recorded through Env.Body (the driver's eval reads).

package mono

import (
	"math/big"

	"mk/internal/mir"
	"mk/internal/types"
)

// predSteps bounds the blocks the interpreter runs in one predicate: a
// clause of operators over constants runs a few blocks per operator.
const predSteps = 1024

// isRequires reports whether item is the predicate of a requires clause of
// its owner function.
func (bl *builder) isRequires(item types.ItemID) bool {
	it := bl.env.Items().Get(item)
	if it.Owner == types.NoItem {
		return false
	}
	d, ok := bl.env.Items().Get(it.Owner).Decl.(*types.FuncDecl)
	if !ok {
		return false
	}
	for _, c := range d.Sig.Contracts {
		if c.Body == item {
			return c.Kind == types.Requires
		}
	}
	return false
}

// requiresHolds reports whether the requires predicate call t of the
// instance body is true for every run: its arguments are constants and the
// predicate's instance body computes true from them.
func (bl *builder) requiresHolds(t *mir.Terminator) bool {
	b := bl.b
	f := t.Callee.Fn
	fd, ok := bl.env.Items().Get(f.Item).Decl.(*types.FuncDecl)
	if !ok || fd.Bodiless {
		return false
	}
	cargs := b.Args[t.Args.Start:t.Args.End()]
	if len(cargs) != len(fd.Sig.Params) {
		return false
	}
	args := append([]types.GenericArg(nil), b.GenericArgs[f.Args.Start:f.Args.End()]...)
	if !closedArgs(bl.tab, args) {
		return false
	}
	vals := make([]predVal, len(cargs))
	for i, a := range cargs {
		v, ok := bl.scalarOperand(a.Operand)
		if !ok && !fd.Sig.Params[i].Const {
			// lower passes each parameter the clause names by shared
			// reference: one to a const value parameter is its constant.
			v, ok = bl.constParamRef(a.Operand)
			if ok {
				v.ref = true
			}
		}
		if !ok {
			return false
		}
		vals[i] = v
		if fd.Sig.Params[i].Const {
			c := a.Operand.Const
			if a.Operand.Kind == mir.OpImm {
				c = bl.immConst(a.Operand.Type, a.Operand.Bits)
			}
			args = append(args, types.ConstGenericArg(types.ConstArg{Kind: types.CAValue, Value: c}))
		}
	}
	if gb := bl.env.Body(f.Item); gb == nil || int(gb.ParamCount) != len(cargs) {
		return false
	}
	opt := bl.opt
	opt.tasks = taskFold{}
	opt.libc = taskFold{}
	pb, _ := instantiate(bl.env, f.Item, args, opt, nil)
	if pb == nil {
		return false
	}
	var layouts []types.TypeID
	v, ok := bl.foldPredicate(pb, vals, &layouts)
	if !ok || !v {
		return false
	}
	if r, ok := bl.env.(BodyReader); ok {
		r.ReadBody(f.Item)
	}
	if lr, ok := bl.env.(LayoutReader); ok {
		for _, lt := range layouts {
			lr.ReadLayout(lt)
		}
	}
	return true
}

// BodyReader is an Env that records the bodies an instance body depends on
// beyond its own generic body: the predicate of each requires clause the
// instance folded, which the instance no longer calls. The driver's
// instances for the evaluator implement it, so an edit of the clause
// invalidates a cached evaluation that ran the instance.
type BodyReader interface {
	ReadBody(item types.ItemID)
}

// LayoutReader is an Env that records the layouts an instance body depends on
// beyond what eval runs directly: types whose size or alignment folded in a
// requires predicate or a substituted SizeOf const generic argument.
type LayoutReader interface {
	ReadLayout(t types.TypeID)
}

// immConst interns the scalar immediate bits of type t as a const of its
// layout's size, as the collector keys a const argument.
func (bl *builder) immConst(t types.TypeID, bits uint64) types.ConstID {
	l := bl.env.Layout(t)
	n := int(l.Shape.Size)
	bs := make([]byte, n)
	types.StoreUint(bs, n, bits)
	return bl.env.Consts().Intern(types.ConstValue{Type: t, Align: l.Shape.Align, Bytes: bs})
}

// predVal is a known scalar: its type and its value, an integer's in
// two's complement masked to the type's width. ref marks a parameter bound
// to a shared reference to the value, read through a dereference.
type predVal struct {
	t    types.TypeID
	bits uint64
	ref  bool
}

// constParamRef is the value behind o when o is a temporary whose one
// assignment in the instance body is a shared reference to a const value
// parameter of a scalar type (lower's argument of a contract predicate).
func (bl *builder) constParamRef(o mir.Operand) (predVal, bool) {
	if bl.constParam == nil || o.Kind != mir.OpCopy && o.Kind != mir.OpMove || o.Place.Proj.Len != 0 {
		return predVal{}, false
	}
	b := bl.b
	var def *mir.Rvalue
	for bi := range b.Blocks {
		for si := range b.Blocks[bi].Stmts {
			st := &b.Blocks[bi].Stmts[si]
			if st.Kind == mir.StAssign && st.Place.Local == o.Place.Local {
				if def != nil || st.Place.Proj.Len != 0 {
					return predVal{}, false
				}
				def = &st.Rvalue
			}
		}
	}
	if def == nil || def.Kind != mir.RvRef || def.Borrow != mir.Shared || def.Place.Proj.Len != 0 {
		return predVal{}, false
	}
	c, ok := bl.constParam[def.Place.Local]
	if !ok {
		return predVal{}, false
	}
	return bl.scalarOperand(mir.Const(b.Locals[def.Place.Local].Type, c))
}

// scalarKind is the width of t in bits and its signedness when t is an
// integer, bool or char of at most 64 bits; ok is false for any other type.
func (bl *builder) scalarKind(t types.TypeID) (width uint, signed, ok bool) {
	ty := bl.tab.Get(t)
	switch ty.Kind {
	case types.KBool:
		return 1, false, true
	case types.KChar:
		return 32, false, true
	case types.KInt, types.KIntPtr:
		l := bl.env.Layout(t)
		if l == nil || l.Shape.Size == 0 || l.Shape.Size > 8 {
			return 0, false, false
		}
		return uint(l.Shape.Size) * 8, ty.Signed, true
	}
	return 0, false, false
}

// mask is v masked to t's width.
func (bl *builder) mask(t types.TypeID, v uint64) (predVal, bool) {
	w, _, ok := bl.scalarKind(t)
	if !ok {
		return predVal{}, false
	}
	if w < 64 {
		v &= 1<<w - 1
	}
	return predVal{t: t, bits: v}, true
}

// scalarOperand is a constant operand's value: an immediate, or an
// interned const of a scalar type with no relocation.
func (bl *builder) scalarOperand(o mir.Operand) (predVal, bool) {
	switch o.Kind {
	case mir.OpImm:
		return bl.mask(o.Type, o.Bits)
	case mir.OpConst:
		v := bl.env.Consts().Get(o.Const)
		if len(v.Relocs) != 0 || len(v.Bytes) == 0 || len(v.Bytes) > 8 {
			return predVal{}, false
		}
		return bl.mask(o.Type, types.LoadUint(v.Bytes, len(v.Bytes)))
	}
	return predVal{}, false
}

// exact is v as an exact integer, by t's signedness.
func (bl *builder) exact(v predVal) *big.Int {
	w, signed, _ := bl.scalarKind(v.t)
	x := new(big.Int).SetUint64(v.bits)
	if signed && w > 0 && v.bits>>(w-1)&1 == 1 {
		x.Sub(x, new(big.Int).Lsh(big.NewInt(1), w))
	}
	return x
}

// fits is the exact integer x as a value of t; false when x is outside
// t's range (the checked operation would trap).
func (bl *builder) fits(t types.TypeID, x *big.Int) (predVal, bool) {
	w, signed, ok := bl.scalarKind(t)
	if !ok {
		return predVal{}, false
	}
	lo, hi := new(big.Int), new(big.Int).Lsh(big.NewInt(1), w)
	if signed {
		lo.Neg(new(big.Int).Lsh(big.NewInt(1), w-1))
		hi.Lsh(big.NewInt(1), w-1)
	}
	if x.Cmp(lo) < 0 || x.Cmp(hi) >= 0 {
		return predVal{}, false
	}
	if x.Sign() < 0 {
		x = new(big.Int).Add(x, new(big.Int).Lsh(big.NewInt(1), w))
	}
	return predVal{t: t, bits: x.Uint64()}, true
}

// foldPredicate runs the predicate instance body pb with its parameters
// bound to params: the bool it returns, or false when a statement or
// terminator is outside the interpreter's forms.
func (bl *builder) foldPredicate(pb *mir.Body, params []predVal, layouts *[]types.TypeID) (holds, ok bool) {
	vals := make([]predVal, len(pb.Locals))
	known := make([]bool, len(pb.Locals))
	for i, v := range params {
		want := v.t
		if v.ref {
			want = bl.tab.Ref(v.t)
		}
		if i+1 >= len(vals) || pb.Locals[i+1].Type != want {
			return false, false
		}
		vals[i+1], known[i+1] = v, true
	}
	read := func(o mir.Operand) (predVal, bool) {
		if o.Kind == mir.OpCopy || o.Kind == mir.OpMove {
			l := int(o.Place.Local)
			if l >= len(vals) || !known[l] {
				return predVal{}, false
			}
			v := vals[l]
			switch {
			case o.Place.Proj.Len == 0 && !v.ref:
			case o.Place.Proj.Len == 1 && v.ref && pb.Projections[o.Place.Proj.Start].Kind == mir.ProjDeref:
				v.ref = false
			default:
				return predVal{}, false
			}
			return v, true
		}
		return bl.scalarOperand(o)
	}
	blk := mir.BlockID(0)
	for steps := 0; steps < predSteps; steps++ {
		if int(blk) >= len(pb.Blocks) {
			return false, false
		}
		bb := &pb.Blocks[blk]
		for si := range bb.Stmts {
			st := &bb.Stmts[si]
			switch st.Kind {
			case mir.StNop, mir.StScopeEnd:
				continue
			case mir.StAssign:
			default:
				return false, false
			}
			l := int(st.Place.Local)
			if st.Place.Proj.Len != 0 || l >= len(vals) {
				return false, false
			}
			v, ok := bl.foldRvalue(&st.Rvalue, pb.Locals[l].Type, read, layouts)
			if !ok {
				return false, false
			}
			vals[l], known[l] = v, true
		}
		t := &bb.Term
		switch t.Kind {
		case mir.TermGoto:
			blk = t.Target
		case mir.TermSwitch:
			v, ok := read(t.On)
			if !ok {
				return false, false
			}
			w, _, ok := bl.scalarKind(v.t)
			if !ok {
				return false, false
			}
			mk := ^uint64(0)
			if w < 64 {
				mk = 1<<w - 1
			}
			to := t.Default
			for ci := t.Cases.Start; ci < t.Cases.End(); ci++ {
				if pb.Cases[ci].Value&mk == v.bits {
					to = pb.Cases[ci].Target
					break
				}
			}
			if to == mir.NoBlock {
				return false, false
			}
			blk = to
		case mir.TermReturn:
			if t.Exit != mir.ExitNormal || !known[0] || pb.Locals[0].Type != types.Bool {
				return false, false
			}
			return vals[0].bits == 1, true
		default:
			return false, false
		}
	}
	return false, false
}

// foldRvalue is the value of rv assigned to a local of type dst, by read
// for the operands; false outside the interpreter's forms or where the
// operation would trap.
func (bl *builder) foldRvalue(rv *mir.Rvalue, dst types.TypeID, read func(mir.Operand) (predVal, bool), layouts *[]types.TypeID) (predVal, bool) {
	switch rv.Kind {
	case mir.RvUse:
		v, ok := read(rv.A)
		if !ok || v.t != dst {
			return predVal{}, false
		}
		return v, true
	case mir.RvSizeOf, mir.RvAlignOf:
		if !closed(bl.tab, rv.Type) {
			return predVal{}, false
		}
		l := bl.env.Layout(rv.Type)
		if l == nil {
			return predVal{}, false
		}
		if layouts != nil {
			*layouts = append(*layouts, rv.Type)
		}
		n := l.Shape.Size
		if rv.Kind == mir.RvAlignOf {
			n = uint64(l.Shape.Align)
		}
		return bl.fits(dst, new(big.Int).SetUint64(n))
	case mir.RvCast:
		v, ok := read(rv.A)
		if !ok || rv.Cast != mir.CastLossless || bl.tab.Kind(v.t) == types.KBool || bl.tab.Kind(dst) == types.KBool {
			return predVal{}, false
		}
		return bl.fits(dst, bl.exact(v))
	case mir.RvUnary:
		v, ok := read(rv.A)
		if !ok || v.t != dst {
			return predVal{}, false
		}
		switch rv.Un {
		case mir.Not:
			if v.t != types.Bool {
				return predVal{}, false
			}
			return predVal{t: v.t, bits: 1 - v.bits}, true
		case mir.BitNot:
			if bl.tab.Kind(v.t) != types.KInt && bl.tab.Kind(v.t) != types.KIntPtr {
				return predVal{}, false
			}
			return bl.mask(v.t, ^v.bits)
		case mir.Neg:
			if bl.tab.Kind(v.t) != types.KInt && bl.tab.Kind(v.t) != types.KIntPtr {
				return predVal{}, false
			}
			return bl.fits(v.t, new(big.Int).Neg(bl.exact(v)))
		}
	case mir.RvBinary:
		a, ok := read(rv.A)
		if !ok {
			return predVal{}, false
		}
		b, ok := read(rv.B)
		if !ok || a.t != b.t {
			return predVal{}, false
		}
		return bl.foldBinary(rv.Bin, a, b, dst)
	}
	return predVal{}, false
}

// foldBinary is a op b, both of one scalar type, as a value of dst.
func (bl *builder) foldBinary(op mir.BinOp, a, b predVal, dst types.TypeID) (predVal, bool) {
	if op.IsCompare() {
		if dst != types.Bool {
			return predVal{}, false
		}
		c := bl.exact(a).Cmp(bl.exact(b))
		var r bool
		switch op {
		case mir.Eq:
			r = c == 0
		case mir.Ne:
			r = c != 0
		case mir.Lt:
			r = c < 0
		case mir.Le:
			r = c <= 0
		case mir.Gt:
			r = c > 0
		case mir.Ge:
			r = c >= 0
		}
		return predVal{t: types.Bool, bits: b2u(r)}, true
	}
	if a.t != dst {
		return predVal{}, false
	}
	switch op {
	case mir.BitAnd:
		return predVal{t: dst, bits: a.bits & b.bits}, true
	case mir.BitOr:
		return predVal{t: dst, bits: a.bits | b.bits}, true
	case mir.BitXor:
		return predVal{t: dst, bits: a.bits ^ b.bits}, true
	}
	k := bl.tab.Kind(dst)
	if k != types.KInt && k != types.KIntPtr {
		return predVal{}, false
	}
	x, y := bl.exact(a), bl.exact(b)
	r := new(big.Int)
	switch op {
	case mir.Add, mir.AddNoWrap:
		r.Add(x, y)
	case mir.Sub, mir.SubNoWrap:
		r.Sub(x, y)
	case mir.Mul, mir.MulNoWrap:
		r.Mul(x, y)
	case mir.AddWrap:
		return bl.mask(dst, a.bits+b.bits)
	case mir.SubWrap:
		return bl.mask(dst, a.bits-b.bits)
	case mir.MulWrap:
		return bl.mask(dst, a.bits*b.bits)
	default:
		return predVal{}, false
	}
	return bl.fits(dst, r)
}
