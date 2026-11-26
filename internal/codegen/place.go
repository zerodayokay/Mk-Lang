// Why: every Mk local lives in memory, so reading or writing any place
// (a field, a variant, an element, a deref) is an address computation over
// the type's layout, and the loads and stores must carry the facts LLVM
// needs to optimize them.
//
// What: readAddr and placeAddr, downcast and the variant and error helpers,
// indexAddr, the slice length helpers (lenMax, loadLen, storeSlice), scalar
// loads and stores, operand, the memory helpers, entry allocas and
// frameProbe.
//
// How: a place is an address at types.Layout offsets; a downcast selects a
// variant's fields without moving the address. The error side of an E!T is
// an address view: check's layout puts every error's fields at the same
// offsets in an E!T and in its error set, so a read, a write and a borrow of
// it all address the union's own storage. Slice lengths load with !range
// [0, lenMax].

package codegen

import (
	"mk/internal/llvm"
	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

// value is an evaluated operand: a scalar SSA value, an aggregate's address,
// or nothing for a zero-sized type.
type value struct {
	t    types.TypeID
	kind reprKind
	v    llvm.Value // rScalar
	a    addr       // rAgg
}

// readAddr is placeAddr for a place that is only read (an operand, a
// shared borrow, a discriminant, a length).
func (fg *fnGen) readAddr(p mir.Place) (addr, types.TypeID) {
	return fg.placeAddr(p)
}

// placeAddr is the address of a place and its type.
func (fg *fnGen) placeAddr(p mir.Place) (addr, types.TypeID) {
	g := fg.g
	cur := fg.locals[p.Local]
	curT := fg.b.Locals[p.Local].Type
	var variant []types.FieldLayout // the fields after a downcast
	for k, st := range fg.b.Proj(p) {
		switch st.Kind {
		case mir.ProjField:
			if st.FieldLoop {
				source.ICEf(fg.spanOf(fg.span), "codegen: fields-loop projection in an instance body")
			}
			if variant == nil && g.tt.Kind(curT) == types.KArray {
				// A constant element step: element A sits at A times the
				// element's size (its stride, as in indexAddr).
				es, _ := g.sizeAlign(st.Type)
				off := uint64(st.A) * es
				cur = addr{v: fg.gep(cur.v, off), align: alignAt(cur.align, off), scope: cur.scope}
				break
			}
			fields := variant
			if fields == nil {
				fields = g.fieldsOf(curT)
			}
			if int(st.A) >= len(fields) {
				source.ICEf(fg.spanOf(fg.span), "codegen: field %d of %s (%d fields)", st.A,
					g.tt.TypeString(curT, nil), len(fields))
			}
			off := fields[st.A].Offset
			cur = addr{v: fg.gep(cur.v, off), align: alignAt(cur.align, off), scope: cur.scope}
			variant = nil
		case mir.ProjDeref:
			r := g.reprOf(curT)
			if r.kind != rScalar || (r.class != sPtr && r.class != sRaw) {
				source.ICEf(fg.spanOf(fg.span), "codegen: deref of %s", g.tt.TypeString(curT, nil))
			}
			pv := fg.loadScalar(cur, r, false)
			_, a := g.sizeAlign(st.Type)
			cur = addr{v: pv, align: a}
			variant = nil
		case mir.ProjIndex:
			cur = fg.aliasAddr(fg.indexAddr(cur, curT, mir.LocalID(st.A), st.Type), cur, curT, p.Local, k)
			variant = nil
		case mir.ProjDowncast:
			if st.Sel == mir.SelErr && !g.errViewOK(curT, st.Type) {
				// INVARIANT: check's layout places each error's fields
				// by the error alone, so the view always holds; a copy
				// here would leave a borrow of it pointing at a temporary.
				source.ICEf(fg.spanOf(fg.span), "codegen: the error side of %s is not laid out as %s",
					g.tt.TypeString(curT, nil), g.tt.TypeString(st.Type, nil))
			}
			variant = fg.downcast(curT, st)
		}
		curT = st.Type
	}
	return cur, curT
}

// fieldsOf is the field list of a struct, record, or the one error of a set
// of one, which needs no downcast.
func (g *packGen) fieldsOf(t types.TypeID) []types.FieldLayout {
	l := g.layout(t)
	if len(l.Fields) == 0 && g.tt.Kind(t) == types.KErrorSet && len(l.Variants) == 1 {
		return l.Variants[0].Fields
	}
	return l.Fields
}

// downcast is the field list a downcast selects: the variant's fields
// for SelEnum and SelCase, the payload as field 0 for SelSome and SelOk. The
// address does not change. SelErr is the error side of E!T as an error-set
// value, an address view (errViewOK).
func (fg *fnGen) downcast(t types.TypeID, st mir.Projection) []types.FieldLayout {
	g := fg.g
	ty := g.tt.Get(t)
	switch st.Sel {
	case mir.SelSome:
		if ty.Kind == types.KOptional && types.NicheOptional(g.tt.Kind(ty.Elem)) {
			return []types.FieldLayout{{Offset: 0, Type: ty.Elem}}
		}
		return g.variant(t, 1).Fields
	case mir.SelOk:
		return g.variant(t, 0).Fields
	case mir.SelEnum:
		l := g.layout(t)
		if int(st.A) >= len(l.Variants) {
			source.ICEf(fg.spanOf(fg.span), "codegen: variant %d of %s", st.A, g.tt.TypeString(t, nil))
		}
		return l.Variants[st.A].Fields
	case mir.SelCase:
		return g.errorVariant(t, types.ItemID(st.A)).Fields
	case mir.SelErr:
		return nil // the error-set value at the same address
	}
	source.ICEf(fg.spanOf(fg.span), "codegen: downcast selector %d", st.Sel)
	return nil
}

// variant is variant i of a tagged layout.
func (g *packGen) variant(t types.TypeID, i int) types.VariantLayout {
	l := g.layout(t)
	if i >= len(l.Variants) {
		source.ICEf(source.Span{}, "codegen: variant %d of %s (%d)", i, g.tt.TypeString(t, nil), len(l.Variants))
	}
	return l.Variants[i]
}

// errorCode is an error item's code.
func (g *packGen) errorCode(item types.ItemID) uint32 {
	it := g.items.Get(item)
	if ed, ok := it.Decl.(*types.ErrorDecl); ok && ed.Code != 0 {
		return ed.Code
	}
	return types.ErrorCode(it.Key)
}

// errorVariant is the variant of error item e in an E!T or error-set layout.
func (g *packGen) errorVariant(t types.TypeID, e types.ItemID) types.VariantLayout {
	code := uint64(g.errorCode(e))
	for _, v := range g.layout(t).Variants {
		if v.Discr == code {
			return v
		}
	}
	source.ICEf(source.Span{}, "codegen: %s has no variant for error %s", g.tt.TypeString(t, nil), g.items.Get(e).Key)
	return types.VariantLayout{}
}

// errViewOK reports whether the error side of union u is laid out as the
// error-set value set: the code at the same offset and every error's fields
// at the same offsets, so the view is an address.
func (g *packGen) errViewOK(u, set types.TypeID) bool {
	lu, ls := g.layout(u), g.layout(set)
	ok := lu.Tag.Offset == ls.Tag.Offset && lu.Tag.Size == ls.Tag.Size
	for _, v := range ls.Variants {
		if !ok {
			break
		}
		found := false
		for _, w := range lu.Variants {
			if w.Discr == v.Discr {
				found = len(w.Fields) == len(v.Fields)
				for i := range v.Fields {
					found = found && w.Fields[i].Offset == v.Fields[i].Offset
				}
			}
		}
		ok = found
	}
	return ok
}

// indexAddr is element i (the value of local idx) of an array or slice at
// base: unchecked, because lower emits the bounds check as explicit MIR or
// proves it.
func (fg *fnGen) indexAddr(base addr, t types.TypeID, idx mir.LocalID, elem types.TypeID) addr {
	g, m := fg.g, fg.g.m
	ir := g.reprOf(fg.b.Locals[idx].Type)
	iv := fg.loadScalar(fg.locals[idx], ir, false)
	iv = fg.intTo(iv, ir.signed, m.I(64))
	data := base
	if g.tt.Get(t).IsSliceLike() {
		data = addr{v: fg.cur.Load(m.Ptr(), base.v, alignAt(base.align, 0), m.Metadata().NonNull(), m.Metadata().NoUndef())}
	}
	es, ea := g.sizeAlign(elem)
	if es == 0 {
		return addr{v: data.v, align: ea}
	}
	// The index is in bounds (lower checked or proved 0 <= idx < len),
	// and a []T spans at most 2^56 bytes (a language rule), so idx * size
	// neither wraps nor leaves the object: mul nuw nsw, and getelementptr
	// inbounds nuw (LLVM 20 and later).
	off := fg.cur.Bin(llvm.Mul, iv, m.ConstInt(m.I(64), es), llvm.NUW|llvm.NSW)
	align := ea
	if data.align != 0 {
		align = min(ea, max(data.align, 1))
	}
	return addr{v: fg.cur.GEP8(data.v, off, llvm.GEPInbounds|llvm.GEPNUW), align: max(align, 1)}
}

// maxSpan is the most bytes a slice or object may span (building a larger
// one through a raw pointer is undefined behaviour).
const maxSpan = 1 << 56

// lenMax is the largest length a slice or str of type t may have: 2^56 /
// size, or 2^63 - 1 for a zero-size element.
func (g *packGen) lenMax(t types.TypeID) uint64 {
	ty := g.tt.Get(t)
	var size uint64 = 1 // str
	if ty.Kind != types.KStr {
		size, _ = g.sizeAlign(ty.Elem)
	}
	if size == 0 {
		return 1<<63 - 1
	}
	return maxSpan / size
}

// loadLen loads the length word of the slice or str of type t at p
// (RvLen and RvSlice) with noundef and !range [0, lenMax].
//
// Measured with tools/bench: the !range lets
// LLVM compute the exit counts of a bounds-checked loop over slice
// parameters and vectorize it (codegen kernel matmul 3.46 to 1.03 of C).
// Masking the value with the bound instead (an and that survives SROA) also
// vectorized arena slices in locals, but it cost intsum 8 percent and
// fannkuch 5: a masked length is no longer the n it was made from, so a
// check against it that `for i in 0..n` proved stays. storeSlice gives
// locals the fact without changing the value.
func (fg *fnGen) loadLen(p llvm.Value, align uint32, t types.TypeID) llvm.Value {
	m := fg.g.m
	return fg.cur.Load(m.I(64), p, align, fg.g.lenAttach(t)...)
}

// lenAttach is the metadata of a length load: !range [0, lenMax] and
// noundef.
func (g *packGen) lenAttach(t types.TypeID) []llvm.Attach {
	m := g.m
	return []llvm.Attach{llvm.Range(m.Metadata().Range(m.I(64), []int64{0, int64(g.lenMax(t) + 1)})), m.Metadata().NoUndef()}
}

// storeSlice copies the slice or str value at src to dst as its two words,
// both loaded with their facts (the data pointer nonnull, the length in [0,
// lenMax]) before either is stored, never as a
// memcpy. When SROA promotes the local, it turns the facts of these loads
// into assumptions at the copy, which dominates the uses; a memcpy's words
// would carry none. This is what vectorizes the same row's matmul, whose
// slices are locals (4.002 to 1.176 s, measured with tools/bench compare).
// The Win64 Indirect copy of an argument is the exception: callLowered
// copies it with memcpyAligned, so that MemCpyOpt can forward the source.
func (fg *fnGen) storeSlice(dst, src addr, t types.TypeID) {
	m := fg.g.m
	md := m.Metadata()
	p := fg.cur.Load(m.Ptr(), src.v, alignAt(src.align, 0), md.NonNull(), md.NoUndef())
	n := fg.cur.Load(m.I(64), fg.gep(src.v, 8), alignAt(src.align, 8), fg.g.lenAttach(t)...)
	fg.cur.Store(p, dst.v, alignAt(dst.align, 0))
	fg.cur.Store(n, fg.gep(dst.v, 8), alignAt(dst.align, 8))
}

// intTo converts an integer SSA value to type to: sext or zext by the
// source's signedness, trunc, or nothing.
func (fg *fnGen) intTo(v llvm.Value, signed bool, to llvm.Type) llvm.Value {
	from, want := v.Ty.Bits(), to.Bits()
	switch {
	case from == want:
		return v
	case from > want:
		return fg.cur.Cast(llvm.Trunc, v, to)
	case signed && from > 1:
		return fg.cur.Cast(llvm.SExt, v, to)
	}
	return fg.cur.Cast(llvm.ZExt, v, to)
}

// loadScalar loads a scalar of representation r from a with its metadata;
// bool becomes i1.
func (fg *fnGen) loadScalar(a addr, r *repr, discr bool) llvm.Value {
	md := fg.g.loadMD(r, discr)
	if a.scope != 0 {
		md = append(md, fg.aliasMD(a)...) // aliasscope.go
	}
	v := fg.cur.Load(r.mem, a.v, max(a.align, 1), md...)
	if r.class == sBool {
		return fg.cur.Cast(llvm.Trunc, v, r.ssa)
	}
	return v
}

// storeScalar stores an SSA scalar (bool as i8).
func (fg *fnGen) storeScalar(a addr, r *repr, v llvm.Value) {
	if r.class == sBool && v.Ty.Bits() == 1 {
		v = fg.cur.Cast(llvm.ZExt, v, r.mem)
	}
	fg.cur.Store(v, a.v, max(a.align, 1), fg.aliasMD(a)...) // aliasscope.go
}

// operand evaluates an operand: a scalar place loads, an
// aggregate place stays an address, a const is its bits or its global, an
// immediate is a constant.
func (fg *fnGen) operand(o mir.Operand) value {
	g := fg.g
	t := fg.b.OperandType(o)
	r := g.reprOf(t)
	val := value{t: t, kind: r.kind}
	switch o.Kind {
	case mir.OpCopy, mir.OpMove:
		a, _ := fg.readAddr(o.Place)
		switch r.kind {
		case rScalar:
			val.v = fg.loadScalar(a, r, false)
		case rAgg:
			val.a = a
		}
	case mir.OpImm:
		if r.kind != rScalar || r.class == sFloat || r.class == sPtr || r.class == sRaw {
			source.ICEf(fg.spanOf(fg.span), "codegen: immediate of type %s", g.tt.TypeString(t, nil))
		}
		val.v = g.m.ConstInt(r.ssa, o.Bits)
	case mir.OpConst:
		switch r.kind {
		case rScalar:
			val.v = g.scalarConst(o.Const, r)
		case rAgg:
			val.a = addr{v: g.constGlobal(o.Const), align: r.align}
		}
	}
	return val
}

// store writes a value to a of type t: a scalar store, an aggregate copy, or
// nothing for a zero-sized type.
func (fg *fnGen) store(a addr, v value) {
	switch v.kind {
	case rScalar:
		fg.storeScalar(a, fg.g.reprOf(v.t), v.v)
	case rAgg:
		if fg.g.tt.Get(v.t).IsSliceLike() {
			fg.storeSlice(a, v.a, v.t)
			return
		}
		size, _ := fg.g.sizeAlign(v.t)
		fg.memcpy(a, v.a, size)
	}
}

// memcpy copies n bytes with llvm.memcpy (the support pack defines memcpy).
func (fg *fnGen) memcpy(dst, src addr, n uint64) {
	if n == 0 {
		return
	}
	m := fg.g.m
	f := m.IntrinsicFn("memcpy", m.Ptr(), m.Ptr(), m.I(64))
	fg.cur.CallFunc(f, []llvm.Value{dst.v, src.v, m.ConstInt(m.I(64), n), m.ConstInt(m.I(1), 0)})
}

// memcpyAligned is memcpy with both pointers' known alignments as align
// attributes, so that MemCpyOpt, when it forwards the source in place of
// dst (processImmutArgument, call.go), knows the source is aligned as the
// destination alloca without enforcing it.
func (fg *fnGen) memcpyAligned(dst, src addr, n uint64) {
	if n == 0 {
		return
	}
	m := fg.g.m
	f := m.IntrinsicFn("memcpy", m.Ptr(), m.Ptr(), m.I(64))
	al := func(a addr) llvm.Attrs { return llvm.Attrs{}.Int("align", uint64(max(a.align, 1))) }
	fg.cur.Call(f.Type(), f.Ref(), []llvm.Value{dst.v, src.v, m.ConstInt(m.I(64), n), m.ConstInt(m.I(1), 0)},
		[]llvm.Attrs{al(dst), al(src), {}, {}}, llvm.Attrs{}, llvm.Attrs{})
}

// memset fills n bytes with c.
func (fg *fnGen) memset(dst addr, c byte, n uint64) {
	if n == 0 {
		return
	}
	m := fg.g.m
	f := m.IntrinsicFn("memset", m.Ptr(), m.I(64))
	fg.cur.CallFunc(f, []llvm.Value{dst.v, m.ConstInt(m.I(8), uint64(c)), m.ConstInt(m.I(64), n), m.ConstInt(m.I(1), 0)})
}

// temp is a fresh alloca of t's size in the entry block.
func (fg *fnGen) temp(t types.TypeID) addr {
	r := fg.g.reprOf(t)
	return fg.tempBytes(max(r.size, 1), r.align)
}

func (fg *fnGen) tempBytes(size uint64, align uint32) addr {
	m := fg.g.m
	return addr{v: fg.entryAlloca(m.Array(size, m.I(8)), align), align: max(align, 1)}
}

// entryAlloca adds an alloca to the entry block, which stays open until
// the function's last block is emitted (emitFunc closes it). On x86_64
// Windows a slot aligned over a page gives the function the aligned stack
// probe (frameProbe).
func (fg *fnGen) entryAlloca(t llvm.Type, align uint32) llvm.Value {
	align = max(align, 1)
	fg.frameProbe(align)
	return fg.entry.Alloca(t, align)
}

// pageBytes is the page the Win64 stack probes step by; maxAlign is the
// largest align(n) (check's maxAlign); symChkstkAligned is the
// support routine every Win64 frame aligned over a page probes with.
const (
	pageBytes        = 4096
	maxAlign         = 65536
	symChkstkAligned = "mk_chkstk_65536"
)

// frameProbe: on x86_64 Windows, a frame with a slot
// aligned over a page is realigned by LLVM's prologue after its __chkstk
// call (sub rsp, rax; and rsp, -align), by up to align - 16 bytes that no
// probe touched, so the frame's first call could push past the guard page
// of a task's stack: an access violation before the function's first
// statement, which no handler sees. The function instead names
// mk_chkstk_65536 as its probe ("probe-stack"), which also covers a
// rounding to any alignment up to maxAlign. One routine for every such
// alignment, because an inlined callee's realignment can be larger than
// its caller's and the inliner keeps the caller's probe-stack (clang 21.1.8
// -O2 on a hand-written module: an align(8192) caller of an align(65536)
// callee called its own probe, then rounded rsp to 65536). LLVM always
// probes such a frame (its size rounded to the alignment exceeds a page).
// Other targets probe the realignment inline.
func (fg *fnGen) frameProbe(align uint32) {
	t := fg.g.cfg.Target
	if align <= pageBytes || fg.probed || fg.fn == nil || t.OS != types.OSWindows || t.Arch != types.ArchX64 {
		return
	}
	fg.probed = true
	fg.fn.SetDefAttrs(llvm.Attrs{}.Str("probe-stack", symChkstkAligned))
}
