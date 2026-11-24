// Why: codegen finds every callee by an exact (item, args) match read from
// the instance body, so the body must name exactly what the instance table
// keys; three kinds of spelling would otherwise miss.
//
// What: canonLiterals (every reference to a fn literal names the literal
// kept for its content), the SizeOf fold check mentionsSizeOf, constArgs
// (the argument of a const value parameter becomes a constant operand) and
// foldAggregate (an aggregate of constant parts becomes one const).
//
// How: a fn literal is one instance per content; the first literal met
// while bodies are built, in level and key order, stands for its content.
// Const values are rewritten deepest first on an explicit stack, so a deep
// value costs heap, not Go stack.

package mono

import (
	"cmp"
	"math/big"
	"slices"

	"mk/internal/mir"
	"mk/internal/types"
)

// literals keeps, per collection, the literal item that stands for each fn
// literal content key: the first one met while bodies are built, in level and
// key order, which is deterministic. Keeping the smallest key among equal
// literals instead would need every literal's body hash before collection
// starts.
type literals struct {
	env        Env
	kept       map[string]types.ItemID
	memo       map[types.ConstID]types.ConstID
	sizeOfMemo []uint8 // dense per-TypeID cache for mentionsSizeOf: 0=unseen, 1=no, 2=yes
}

func newLiterals(env Env) *literals {
	return &literals{env: env, kept: map[string]types.ItemID{}, memo: map[types.ConstID]types.ConstID{}}
}

// item is the literal kept for a reference to item at args; any other
// function is itself. args must be closed.
func (l *literals) item(item types.ItemID, args []types.GenericArg) types.ItemID {
	if l == nil || l.env.Items().Get(item).Kind != types.KindLiteral {
		return item
	}
	k := keyOf(l.env, item, args).Text
	if kept, ok := l.kept[k]; ok {
		return kept
	}
	l.kept[k] = item
	return item
}

// constID is c with every fn relocation to a literal, at any depth of data
// relocations and in the generic arguments of every fn relocation, naming the
// kept literal. It is c itself when nothing changes. The values its data
// relocations reach are done first, deepest first, on an explicit stack, so a
// deep value (a 2,000,000-node list) costs heap, not Go stack; each is then
// canonical by constOne with its data relocations already in memo.
func (l *literals) constID(c types.ConstID) types.ConstID {
	if l == nil || c == types.NoConst {
		return c
	}
	ct := l.env.Consts()
	if len(ct.Get(c).Relocs) == 0 {
		return c
	}
	if r, ok := l.memo[c]; ok {
		return r
	}
	type frame struct {
		c    types.ConstID
		next int
	}
	l.memo[c] = c // a cycle of data relocations ends here
	stack := []frame{{c: c}}
	for len(stack) > 0 {
		top := len(stack) - 1
		v := ct.Get(stack[top].c)
		child := types.NoConst
		for stack[top].next < len(v.Relocs) && child == types.NoConst {
			r := v.Relocs[stack[top].next]
			stack[top].next++
			if r.Kind == types.RelocFn {
				// In the recursion's order: the first literal met is kept.
				l.item(r.Fn.Item, l.args(r.Fn.Args))
				continue
			}
			if r.Kind != types.RelocData || r.Data == types.NoConst {
				continue
			}
			if _, ok := l.memo[r.Data]; !ok {
				child = r.Data
			}
		}
		if child != types.NoConst {
			l.memo[child] = child // in progress: a cycle back to it ends here
			stack = append(stack, frame{c: child})
			continue
		}
		l.memo[stack[top].c] = l.constOne(stack[top].c)
		stack = stack[:top]
	}
	return l.memo[c]
}

// constOne is constID's step for one value whose data relocations are in
// memo (done, or in progress on a cycle).
func (l *literals) constOne(c types.ConstID) types.ConstID {
	ct := l.env.Consts()
	v := ct.Get(c)
	var relocs []types.Reloc
	changed := false
	for i, r := range v.Relocs {
		nr := r
		switch r.Kind {
		case types.RelocFn:
			nr.Fn.Args = l.args(r.Fn.Args)
			nr.Fn.Item = l.item(r.Fn.Item, nr.Fn.Args)
		case types.RelocData:
			if d, ok := l.memo[r.Data]; ok {
				nr.Data = d
			} else {
				nr.Data = l.constID(r.Data)
			}
		}
		if !changed && !sameReloc(nr, r) {
			changed = true
			relocs = append(make([]types.Reloc, 0, len(v.Relocs)), v.Relocs[:i]...)
		}
		if changed {
			relocs = append(relocs, nr)
		}
	}
	out := c
	if changed {
		out = ct.Intern(types.ConstValue{Type: v.Type, Align: v.Align, Bytes: append([]byte(nil), v.Bytes...), Relocs: relocs})
	}
	return out
}

func sameReloc(a, b types.Reloc) bool {
	return a.Offset == b.Offset && a.Kind == b.Kind && a.Data == b.Data && a.Fn.Item == b.Fn.Item && sameArgList(a.Fn.Args, b.Fn.Args)
}

func sameArgList(a, b []types.GenericArg) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// args is as with its const arguments' values through constID; the same
// slice when nothing changes.
func (l *literals) args(as []types.GenericArg) []types.GenericArg {
	var out []types.GenericArg
	changed := false
	for i, a := range as {
		na := a
		if a.IsConst && a.Const.Kind == types.CAValue {
			na.Const.Value = l.constID(a.Const.Value)
		}
		if !changed && na != a {
			changed = true
			out = append(make([]types.GenericArg, 0, len(as)), as[:i]...)
		}
		if changed {
			out = append(out, na)
		}
	}
	if !changed {
		return as
	}
	return out
}

// canonLiterals makes every literal reference of body b name the kept
// literal: RvFnRef and CallDirect items, fn relocations in OpConst
// operands and in const generic arguments, and an RvConstRef whose value
// names a literal (it becomes the canonical value, as an RvUse). With apply
// false it only reports whether anything would change. An RvGlobalRef
// stays: it is the address of the const's or global's own storage, whose
// value mono cannot rewrite; Instances.Find resolves its relocations.
func (bl *builder) canonLiterals(b *mir.Body, apply bool) bool {
	l := bl.lits
	if l == nil {
		return false
	}
	changed := false
	op := func(o *mir.Operand) {
		if o.Kind != mir.OpConst {
			return
		}
		if c := l.constID(o.Const); c != o.Const {
			changed = true
			if apply {
				o.Const = c
			}
		}
	}
	fn := func(f *mir.FnRef) {
		if it := l.item(f.Item, b.GenericArgs[f.Args.Start:f.Args.End()]); it != f.Item {
			changed = true
			if apply {
				f.Item = it
			}
		}
	}
	for i := range b.GenericArgs {
		a := &b.GenericArgs[i]
		if !a.IsConst || a.Const.Kind != types.CAValue {
			continue
		}
		if c := l.constID(a.Const.Value); c != a.Const.Value {
			changed = true
			if apply {
				a.Const.Value = c
			}
		}
	}
	for i := range b.Operands {
		op(&b.Operands[i])
	}
	for i := range b.Args {
		op(&b.Args[i].Operand)
	}
	for bi := range b.Blocks {
		blk := &b.Blocks[bi]
		for si := range blk.Stmts {
			st := &blk.Stmts[si]
			if st.Kind != mir.StAssign {
				continue
			}
			rv := &st.Rvalue
			op(&rv.A)
			op(&rv.B)
			switch rv.Kind {
			case mir.RvFnRef:
				fn(&rv.Fn)
			case mir.RvConstRef:
				if d, ok := bl.env.Items().Get(rv.Item).Decl.(*types.ConstDecl); ok && d.HasValue {
					if c := l.constID(d.Value); c != d.Value {
						changed = true
						if apply {
							*rv = mir.Rvalue{Kind: mir.RvUse, A: mir.Const(d.Type, c)}
						}
					}
				}
			}
		}
		t := &blk.Term
		op(&t.On)
		op(&t.Msg)
		op(&t.Callee.Value)
		if isCall(t) && t.Callee.Kind == mir.CallDirect {
			fn(&t.Callee.Fn)
		}
	}
	return changed
}

// constArgs makes every argument of a const value parameter a constant
// operand: a whole-local copy of a temporary assigned once becomes that
// assignment's constant: an RvUse of a constant (what lower makes of a
// forwarded const generic parameter), an RvFnRef (a const fn argument naming
// the caller's generics), an RvConstRef of a named const, or an RvConstArg
// with a value. With apply false it only reports whether a non-constant
// argument is there. Anything else is left for the collector, which raises an
// ICE on it.
func (bl *builder) constArgs(b *mir.Body, apply bool) bool {
	changed := false
	for bi := range b.Blocks {
		t := &b.Blocks[bi].Term
		if !isCall(t) || t.Callee.Kind != mir.CallDirect {
			continue // a tail call's const arguments are made constant as a call's
		}
		fd, ok := bl.env.Items().Get(t.Callee.Fn.Item).Decl.(*types.FuncDecl)
		if !ok {
			continue
		}
		for pi, p := range fd.Sig.Params {
			if !p.Const || pi >= int(t.Args.Len) {
				continue
			}
			o := &b.Args[int(t.Args.Start)+pi].Operand
			if o.Kind == mir.OpConst || o.Kind == mir.OpImm {
				continue
			}
			if !apply {
				return true
			}
			if c, ok := bl.constOf(b, *o); ok {
				*o = c
				changed = true
			}
		}
	}
	return changed
}

// constOf is the constant a whole-local copy of a once-assigned temporary
// holds, if its one assignment is a constant.
func (bl *builder) constOf(b *mir.Body, o mir.Operand) (mir.Operand, bool) {
	if (o.Kind != mir.OpCopy && o.Kind != mir.OpMove) || o.Place.Proj.Len != 0 {
		return mir.Operand{}, false
	}
	l := o.Place.Local
	if b.Locals[l].Kind != mir.LocalTemp {
		return mir.Operand{}, false
	}
	var rv *mir.Rvalue
	for bi := range b.Blocks {
		for si := range b.Blocks[bi].Stmts {
			st := &b.Blocks[bi].Stmts[si]
			if st.Kind == mir.StAssign && st.Place.Local == l {
				if rv != nil || st.Place.Proj.Len != 0 {
					return mir.Operand{}, false
				}
				rv = &st.Rvalue
			}
		}
	}
	if rv == nil {
		return mir.Operand{}, false
	}
	t := b.Locals[l].Type
	switch rv.Kind {
	case mir.RvUse:
		if rv.A.Kind == mir.OpConst || rv.A.Kind == mir.OpImm {
			return rv.A, true
		}
	case mir.RvFnRef:
		f := types.FnRef{Item: rv.Fn.Item, Args: append([]types.GenericArg(nil), b.GenericArgs[rv.Fn.Args.Start:rv.Fn.Args.End()]...)}
		if !closedArgs(bl.tab, f.Args) {
			return mir.Operand{}, false
		}
		return mir.Const(t, fnConst(bl.env.Consts(), t, f)), true
	case mir.RvConstRef:
		if d, ok := bl.env.Items().Get(rv.Item).Decl.(*types.ConstDecl); ok && d.HasValue {
			return bl.constOperand(t, d.Value), true
		}
	case mir.RvConstArg:
		if rv.Len.Kind == types.CAValue {
			return bl.constOperand(t, rv.Len.Value), true
		}
	case mir.RvAggregate:
		// A payload-free enum variant, `Order.Acquire`, or a struct of
		// constants is a constant.
		if v, ok := bl.foldAggregate(b, rv, t); ok {
			return v, true
		}
	case mir.RvBinary:
		// Arithmetic on const parameters, `v.With(N - 1, x)`, is a constant in
		// the instance.
		if v, ok := bl.foldArith(b, rv, t); ok {
			return v, true
		}
	}
	return mir.Operand{}, false
}

// foldAggregate is the const an aggregate of closed type t builds when
// every part is a constant: a payload-free enum variant (`Order.Acquire`:
// zero bytes of the enum's size with the variant's discriminant in the
// tag), or a struct, record or array of constant fields (`Ord(v: 1)`,
// `["ab", "cde"]`), laid out as codegen lays the aggregate out
// (aggregate.go). A part's relocations move to the part's offset, so a
// `str` element keeps its pointer. Padding is zero, as in
// every interned const.
func (bl *builder) foldAggregate(b *mir.Body, rv *mir.Rvalue, t types.TypeID) (mir.Operand, bool) {
	if !closed(bl.tab, t) {
		return mir.Operand{}, false
	}
	l := bl.env.Layout(t)
	if l == nil {
		return mir.Operand{}, false
	}
	out := make([]byte, l.Shape.Size)
	var relocs []types.Reloc
	var fields []types.FieldLayout
	switch rv.Agg {
	case mir.AggEnum:
		if rv.Ops.Len != 0 || int(rv.Variant) >= len(l.Variants) || len(l.Variants[rv.Variant].Fields) != 0 {
			return mir.Operand{}, false
		}
		if !l.HasTag || l.Tag.Niche || l.Tag.Offset+uint64(l.Tag.Size) > uint64(len(out)) {
			return mir.Operand{}, false
		}
		d := l.Variants[rv.Variant].Discr
		for i := range uint64(l.Tag.Size) {
			out[l.Tag.Offset+i] = byte(d >> (8 * i))
		}
	case mir.AggStruct, mir.AggRecord:
		if l.HasTag || int(rv.Ops.Len) != len(l.Fields) {
			return mir.Operand{}, false
		}
		fields = l.Fields
	case mir.AggArray:
		// An array literal passed for a const parameter, `Pick(&xs, [1, 3])`:
		// element i at i times the element size, as codegen's aggregate.go
		// stores it. A []T static slice is not a value of a closed array type
		// and is left alone.
		at := bl.tab.Get(t)
		if at.Kind != types.KArray {
			return mir.Operand{}, false
		}
		el := bl.env.Layout(at.Elem)
		if el == nil || uint64(rv.Ops.Len)*el.Shape.Size != l.Shape.Size {
			return mir.Operand{}, false
		}
		for i := range uint64(rv.Ops.Len) {
			fields = append(fields, types.FieldLayout{Offset: i * el.Shape.Size, Type: at.Elem})
		}
	default:
		return mir.Operand{}, false
	}
	for i, fl := range fields {
		o := b.Operands[int(rv.Ops.Start)+i]
		if o.Kind == mir.OpCopy || o.Kind == mir.OpMove {
			c, ok := bl.constOf(b, o)
			if !ok {
				return mir.Operand{}, false
			}
			o = c
		}
		fs := bl.env.Layout(fl.Type)
		if fs == nil || fl.Offset+fs.Shape.Size > uint64(len(out)) {
			return mir.Operand{}, false
		}
		switch o.Kind {
		case mir.OpImm:
			if fs.Shape.Size > 8 {
				return mir.Operand{}, false
			}
			for j := range fs.Shape.Size {
				out[fl.Offset+j] = byte(o.Bits >> (8 * j))
			}
		case mir.OpConst:
			cv := bl.env.Consts().Get(o.Const)
			if uint64(len(cv.Bytes)) != fs.Shape.Size {
				return mir.Operand{}, false
			}
			copy(out[fl.Offset:], cv.Bytes)
			for _, r := range cv.Relocs {
				r.Offset += uint32(fl.Offset)
				relocs = append(relocs, r)
			}
		default:
			return mir.Operand{}, false
		}
	}
	// Struct fields need not be in offset order; the parts do not overlap,
	// so sorting by offset gives the canonical relocation order.
	slices.SortFunc(relocs, func(a, b types.Reloc) int { return cmp.Compare(a.Offset, b.Offset) })
	c := bl.env.Consts().Intern(types.ConstValue{Type: t, Align: l.Shape.Align, Bytes: out, Relocs: relocs})
	return mir.Const(t, c), true
}

// fnConst interns the const of a fn value of type t naming f.
func fnConst(ct *types.ConstTable, t types.TypeID, f types.FnRef) types.ConstID {
	v := types.ConstValue{Type: t, Align: 8, Bytes: make([]byte, 8),
		Relocs: []types.Reloc{{Offset: 0, Kind: types.RelocFn, Fn: f}}}
	return ct.Intern(v)
}

// mentionsSizeOf reports whether a non-generic body still spells a size as
// SizeOf[T]() anywhere codegen reads a const argument: a generic argument
// range, a local's, projection's, operand's or rvalue's type, or an rvalue's
// Len. Such a body is rewritten so the SizeOf fold reaches it, as it
// reaches the request key.
func (bl *builder) mentionsSizeOf(b *mir.Body) bool {
	var localMemo map[types.TypeID]bool
	var shared *[]uint8
	if bl.lits != nil {
		shared = &bl.lits.sizeOfMemo
	}
	var typ func(t types.TypeID) bool
	var garg func(a types.GenericArg) bool
	carg := func(a types.ConstArg) bool { return a.Kind == types.CASizeOf }
	garg = func(a types.GenericArg) bool {
		if a.IsConst {
			return carg(a.Const)
		}
		return typ(a.Type)
	}
	typ = func(t types.TypeID) bool {
		if t == types.NoType {
			return false
		}
		if shared != nil {
			idx := int(t)
			if idx < len(*shared) {
				if v := (*shared)[idx]; v != 0 {
					return v == 2
				}
			} else {
				n := max(idx+1, len(*shared)*2)
				if n < 256 {
					n = 256
				}
				buf := make([]uint8, n)
				copy(buf, *shared)
				*shared = buf
			}
			(*shared)[idx] = 1 // cycle break
		} else {
			if r, ok := localMemo[t]; ok {
				return r
			}
			if localMemo == nil {
				localMemo = map[types.TypeID]bool{}
			}
			localMemo[t] = false
		}
		ty := bl.tab.Get(t)
		r := false
		switch ty.Kind {
		case types.KRef, types.KMut, types.KSlice, types.KMutSlice, types.KRawPtr, types.KOptional, types.KErrorUnion:
			r = typ(ty.Elem)
		case types.KArray:
			r = carg(ty.Len) || typ(ty.Elem)
		case types.KRecord:
			for _, f := range ty.Fields {
				r = r || typ(f.Type)
			}
		case types.KNamed:
			for _, a := range ty.Args {
				r = r || garg(a)
			}
		case types.KFn:
			for _, p := range ty.Fn.Params {
				r = r || typ(p.Type)
			}
			r = r || typ(ty.Fn.Result)
		}
		if shared != nil {
			if r {
				(*shared)[int(t)] = 2
			}
		} else {
			localMemo[t] = r
		}
		return r
	}
	for _, a := range b.GenericArgs {
		if garg(a) {
			return true
		}
	}
	for i := range b.Locals {
		if typ(b.Locals[i].Type) {
			return true
		}
	}
	for i := range b.Projections {
		if typ(b.Projections[i].Type) {
			return true
		}
	}
	for i := range b.Operands {
		if typ(b.Operands[i].Type) {
			return true
		}
	}
	for i := range b.Args {
		if typ(b.Args[i].Operand.Type) {
			return true
		}
	}
	for bi := range b.Blocks {
		for si := range b.Blocks[bi].Stmts {
			rv := &b.Blocks[bi].Stmts[si].Rvalue
			if carg(rv.Len) || typ(rv.Type) || typ(rv.A.Type) || typ(rv.B.Type) {
				return true
			}
		}
		if typ(b.Blocks[bi].Term.Type) {
			return true
		}
	}
	return false
}

// foldArith evaluates + - * of two constant integer operands of type t, as
// the program would: false when an operand is not constant or the result
// overflows t (that call then stays an ICE).
func (bl *builder) foldArith(b *mir.Body, rv *mir.Rvalue, t types.TypeID) (mir.Operand, bool) {
	ty := bl.tab.Get(t)
	if ty.Kind != types.KInt && ty.Kind != types.KIntPtr {
		return mir.Operand{}, false
	}
	bitsN := uint(ty.Bits)
	if ty.Kind == types.KIntPtr {
		bitsN = 64
	}
	val := func(o mir.Operand) (*big.Int, bool) {
		if o.Kind == mir.OpCopy || o.Kind == mir.OpMove {
			c, ok := bl.constOf(b, o)
			if !ok {
				return nil, false
			}
			o = c
		}
		if o.Kind == mir.OpConst {
			o = bl.constOperand(t, o.Const)
		}
		if o.Kind != mir.OpImm {
			return nil, false
		}
		mask := ^uint64(0)
		if bitsN < 64 {
			mask = 1<<bitsN - 1
		}
		v := new(big.Int).SetUint64(o.Bits & mask)
		if ty.Signed && v.Bit(int(bitsN)-1) == 1 {
			v.Sub(v, new(big.Int).Lsh(big.NewInt(1), bitsN))
		}
		return v, true
	}
	x, ok1 := val(rv.A)
	y, ok2 := val(rv.B)
	if !ok1 || !ok2 {
		return mir.Operand{}, false
	}
	r := new(big.Int)
	switch rv.Bin {
	case mir.Add:
		r.Add(x, y)
	case mir.Sub:
		r.Sub(x, y)
	case mir.Mul:
		r.Mul(x, y)
	default:
		return mir.Operand{}, false
	}
	lo, hi := big.NewInt(0), new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), bitsN), big.NewInt(1))
	if ty.Signed {
		lo = new(big.Int).Neg(new(big.Int).Lsh(big.NewInt(1), bitsN-1))
		hi = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), bitsN-1), big.NewInt(1))
	}
	if r.Cmp(lo) < 0 || r.Cmp(hi) > 0 {
		return mir.Operand{}, false
	}
	if r.Sign() < 0 {
		r.Add(r, new(big.Int).Lsh(big.NewInt(1), bitsN))
	}
	return mir.Imm(t, r.Uint64()), true
}
