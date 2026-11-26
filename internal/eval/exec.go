// Why: this is the general path of the interpreter: every statement, rvalue
// and terminator of instance MIR has its meaning here, and the flat fast
// path in fast.go must agree with it.
//
// What: stmt, assign, slice, strCompare, aggregate, repeat, term, call,
// callFn, envCallee, resolve, callBody, ret and staticSlice.
//
// How: each kind is a case on the Machine's current frame, with every access
// checked through memory.go. The stage rule is applied to a callee before its
// body is asked for; a callee that takes Io is refused before it runs; scopes,
// spawns and asm are refused or ICEs. A TermOnerrGuard follows the borrow
// checker's decision. A direct call caches its resolved callee per segment.

package eval

import (
	"bytes"
	"unicode/utf8"

	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

// stmt runs one statement; its static cost is charged.
func (m *Machine) stmt(f *frame, st *mir.Statement) {
	if m.tg != nil {
		m.tg.val = noVal // no pointer value crosses a statement
	}
	switch st.Kind {
	case mir.StAssign:
		m.assign(f, st.Place, &st.Rvalue)
	case mir.StScopeEnd:
		if m.tg != nil {
			m.tgScopeEnd(f, st.Local)
		}
	case mir.StNop:
	case mir.StConsume:
		if m.tg != nil {
			m.tgTake(f, st.Place)
		}
	case mir.StAsm:
		// An asm has no compile-time meaning.
		refuse(types.RefuseAsm, "")
	case mir.StJoin:
		if m.tg == nil {
			source.ICEf(f.curSpan(), "eval: StJoin reached (a scope needs Io, which is refused first)")
		}
		m.tgJoin(f, st.Local, st.Cancel)
	default:
		source.ICEf(f.curSpan(), "eval: statement kind %d", st.Kind)
	}
}

func trap(pc types.PanicCode) {
	if pc != types.PanicNone {
		panicWith(pc, "")
	}
}

// assign evaluates r into place p.
func (m *Machine) assign(f *frame, p mir.Place, r *mir.Rvalue) {
	dst := m.place(f, p)
	switch r.Kind {
	case mir.RvUse:
		m.writeOp(f, &r.A, dst)
		if m.tg != nil && p.Proj.Len == 0 {
			m.tgSlot(f, p.Local, &r.A)
		}
	case mir.RvRef:
		l := m.place(f, r.Place)
		if m.tg != nil {
			m.setVal(m.retagRef(l, r.Borrow))
		}
		m.store(dst, ptrWord(l.a, l.off))
	case mir.RvSlice:
		m.slice(f, dst, r)
	case mir.RvUnary:
		v, pc := unary(r.Un, m.operandTI(f, &r.A), m.scalar(f, &r.A))
		trap(pc)
		m.store(dst, v)
	case mir.RvBinary:
		at := m.operandTI(f, &r.A)
		if at.ty.Kind == types.KStr {
			m.store(dst, m.strCompare(r.Bin, m.operandLoc(f, &r.A), m.operandLoc(f, &r.B)))
			return
		}
		bt := at // the amount's own type matters only for shifts
		if r.Bin == mir.Shl || r.Bin == mir.Shr {
			bt = m.operandTI(f, &r.B)
		}
		v, pc := binaryOp(r.Bin, at, bt, m.scalar(f, &r.A), m.scalar(f, &r.B))
		trap(pc)
		m.store(dst, v)
	case mir.RvCast:
		m.castInto(f, dst, r)
	case mir.RvAggregate:
		m.aggregate(f, dst, r)
	case mir.RvRepeat:
		m.repeat(f, dst, r)
	case mir.RvLen:
		l := m.place(f, r.Place)
		if l.ti.ty.Kind == types.KArray {
			m.store(dst, m.arrayLen(l.ti.ty))
		} else {
			_, n := m.sliceWords(l)
			m.store(dst, n)
		}
	case mir.RvDiscriminant:
		m.store(dst, m.discriminant(m.place(f, r.Place)))
	case mir.RvFnRef:
		w := m.fnValue(m.fnRef(f, r.Fn))
		m.setVal(0)
		m.store(dst, w)
	case mir.RvGlobalRef:
		w := m.globalRef(r.Item)
		m.setVal(0)
		m.store(dst, w)
	case mir.RvConstRef:
		cid := m.constValue(r.Item)
		m.copyValue(dst, loc{a: m.constAlloc(cid), ti: dst.ti})
	case mir.RvSizeOf:
		m.store(dst, m.ti(r.Type).size)
	case mir.RvAlignOf:
		m.store(dst, uint64(m.ti(r.Type).align))
	case mir.RvFieldCount:
		m.store(dst, uint64(len(m.ti(r.Type).layout.Fields)))
	case mir.RvConstArg:
		switch r.Len.Kind {
		case types.CAValue:
			m.copyValue(dst, loc{a: m.constAlloc(r.Len.Value), ti: dst.ti})
		case types.CASizeOf:
			m.store(dst, m.ti(r.Len.Type).size)
		default:
			source.ICEf(f.curSpan(), "eval: const parameter in an instance body")
		}
	case mir.RvTypeIs, mir.RvFieldName:
		source.ICEf(f.curSpan(), "eval: %s in an instance body", r.Kind)
	default:
		source.ICEf(f.curSpan(), "eval: rvalue kind %d", r.Kind)
	}
}

// slice builds place[lo..hi] (PanicBounds; for str, PanicStrBoundary).
func (m *Machine) slice(f *frame, dst loc, r *mir.Rvalue) {
	pl := m.place(f, r.Place)
	var data, n, esize uint64
	var dtag uint32
	isStr := false
	switch pl.ti.ty.Kind {
	case types.KArray:
		data, n, esize = ptrWord(pl.a, pl.off), m.arrayLen(pl.ti.ty), m.ti(pl.ti.ty.Elem).size
		dtag = pl.tag
	case types.KSlice, types.KMutSlice:
		data, n = m.sliceWords(pl)
		dtag = m.takeVal()
		esize = m.ti(pl.ti.ty.Elem).size
	case types.KStr:
		data, n = m.sliceWords(pl)
		dtag = m.takeVal()
		esize, isStr = 1, true
	default:
		source.ICEf(f.curSpan(), "eval: slice of %s", pl.ti.ty.Kind)
	}
	lo, hi := uint64(0), n
	if r.HasLo {
		lo = m.index64(f, &r.A)
	}
	if r.HasHi {
		hi = m.index64(f, &r.B)
	}
	if lo > hi || hi > n {
		panicWith(types.PanicBounds, "")
	}
	if isStr {
		// Only the two boundary bytes are read, so the time of a slice does
		// not grow with the str.
		if (lo < n && !utf8.RuneStart(m.strByte(data, lo, dtag))) || (hi < n && !utf8.RuneStart(m.strByte(data, hi, dtag))) {
			panicWith(types.PanicStrBoundary, "")
		}
	}
	nd := data
	if data != 0 {
		nd = ptrWord(ptrAlloc(data), elemOff(ptrOff(data), lo, esize))
	}
	var t uint32
	if m.tg != nil {
		// RvSlice's retag over the element range [lo, hi), shared or Unique
		// by the slice's type. The holder of a for-x-in-mut-xs loop
		// is such a slice; its elements' borrows go through the holder's tag.
		el := m.ti(types.U8)
		if !isStr {
			el = m.ti(pl.ti.ty.Elem)
		}
		if data != 0 {
			t = m.retag(loc{a: ptrAlloc(nd), off: ptrOff(nd), tag: dtag}, (hi-lo)*esize, el, hi-lo, dst.ti.ty.Kind == types.KMutSlice, 0)
		}
		m.tacc(dst.a, dst.off, 16, dst.tag, accWrite)
	}
	al := m.mem.access(dst.a, dst.off, 16, true)
	al.storePtr(dst.off, nd)
	al.storeScalar(dst.off+8, 8, hi-lo)
	if m.tg != nil && nd != 0 {
		m.setVal(t)
		m.tgStorePtr(dst.a, dst.off, nd)
	}
}

// index64 reads an integer operand as an index: negative is PanicBounds.
func (m *Machine) index64(f *frame, op *mir.Operand) uint64 {
	ti := m.operandTI(f, op)
	x := m.scalar(f, op)
	if ti.signed && int64(signExtend(x, ti.width)) < 0 {
		panicWith(types.PanicBounds, "")
	}
	return x
}

// readBytes returns the n bytes at pointer word w, read through tag (the
// tagged mode; 0 otherwise), which must hold no pointer word or fragment
// (checked). The slice aliases evaluation memory.
func (m *Machine) readBytes(w, n uint64, tag uint32) []byte {
	if n == 0 {
		return nil
	}
	a, off := ptrAlloc(w), ptrOff(w)
	if m.tg != nil {
		m.tacc(a, off, n, tag, accRead)
	}
	al := m.mem.access(a, off, n, false)
	end := off + uint32(n)
	if (al.ptr != nil && anyBit(al.ptr, sub7(off), end)) || (al.frag != nil && anyBit(al.frag, off, end)) {
		refuse(types.RefusePointerBytes, "")
	}
	return al.bytes[off:end]
}

// strByte reads byte i of the str whose data word is w, through tag, with
// every access check.
func (m *Machine) strByte(w, i uint64, tag uint32) byte {
	return m.readBytes(ptrWord(ptrAlloc(w), elemOff(ptrOff(w), i, 1)), 1, tag)[0]
}

// strCompare compares two str values by content, bytewise, in place. It is
// a compare of the bytes the shorter operand has, charged c(n) for the n
// bytes it touches when it happens, so fuel bounds its time; a block
// holding one is never charged once on entry (compile.go).
func (m *Machine) strCompare(op mir.BinOp, x, y loc) uint64 {
	xd, xn := m.sliceWords(x)
	xt := m.takeVal()
	yd, yn := m.sliceWords(y)
	yt := m.takeVal()
	m.charge(c(min(xn, yn)))
	r := bytes.Compare(m.readBytes(xd, xn, xt), m.readBytes(yd, yn, yt))
	switch op {
	case mir.Eq:
		return b2u(r == 0)
	case mir.Ne:
		return b2u(r != 0)
	case mir.Lt:
		return b2u(r < 0)
	case mir.Le:
		return b2u(r <= 0)
	case mir.Gt:
		return b2u(r > 0)
	case mir.Ge:
		return b2u(r >= 0)
	}
	source.ICEf(source.Span{}, "eval: str operator %d", op)
	return 0
}

// castInto evaluates a cast.
func (m *Machine) castInto(f *frame, dst loc, r *mir.Rvalue) {
	switch r.Cast {
	case mir.CastWidenErrors:
		m.copyValue(dst, m.operandLoc(f, &r.A))
		return
	case mir.CastNarrow:
		m.writeOp(f, &r.A, dst)
		return
	}
	from := m.operandTI(f, &r.A)
	v, pc := cast(r.Cast, from, dst.ti, m.scalar(f, &r.A))
	trap(pc)
	m.store(dst, v)
}

// scratchLoc is a zeroed buffer of n bytes for building an aggregate, so an
// operand that reads the destination sees its old value.
func (m *Machine) scratchLoc(n uint64, ti *tinfo) loc {
	if m.scratch == 0 || uint64(len(m.mem.at(m.scratch).bytes)) < n {
		if m.scratch != 0 {
			m.mem.free(m.scratch)
		}
		m.scratch = m.mem.newAlloc(allocOwn, max(n, 64))
	}
	al := m.mem.at(m.scratch)
	al.fill(0, uint32(n), 0)
	return loc{a: m.scratch, ti: ti}
}

func (m *Machine) at(l loc, off uint64, t types.TypeID) loc {
	return loc{a: l.a, off: l.off + uint32(off), ti: m.ti(t)}
}

func (m *Machine) writeTag(l loc, lay *types.Layout, v uint64) {
	o := l.off + uint32(lay.Tag.Offset)
	if m.tg != nil {
		m.tacc(l.a, o, uint64(lay.Tag.Size), l.tag, accWrite)
	}
	m.mem.access(l.a, o, uint64(lay.Tag.Size), true).storeScalar(o, int(lay.Tag.Size), v)
}

// aggregate builds an aggregate: fields at their offsets, the tag or code,
// zero padding.
func (m *Machine) aggregate(f *frame, dst loc, r *mir.Rvalue) {
	b := f.code.body
	ops := b.Operands[r.Ops.Start:r.Ops.End()]
	if r.Agg == mir.AggArray && dst.ti.ty.Kind == types.KSlice {
		m.staticSlice(f, dst, ops)
		return
	}
	lay := dst.ti.layout
	s := m.scratchLoc(dst.ti.size, dst.ti)
	fields := func(fs []types.FieldLayout) {
		if len(fs) != len(ops) {
			source.ICEf(f.curSpan(), "eval: aggregate of %d operands for %d fields", len(ops), len(fs))
		}
		for i := range ops {
			m.writeOp(f, &ops[i], m.at(s, fs[i].Offset, fs[i].Type))
		}
	}
	switch r.Agg {
	case mir.AggStruct, mir.AggRecord:
		fields(lay.Fields)
	case mir.AggArray:
		elem := m.ti(dst.ti.ty.Elem)
		for i := range ops {
			m.writeOp(f, &ops[i], loc{a: s.a, off: s.off + uint32(uint64(i)*elem.size), ti: elem})
		}
	case mir.AggEnum:
		v := &lay.Variants[r.Variant]
		m.writeTag(s, lay, v.Discr)
		fields(v.Fields)
	case mir.AggError:
		code := uint64(m.errorCode(r.Item))
		m.writeTag(s, lay, code)
		fields(m.variantByDiscr(lay, code).Fields)
	case mir.AggSome:
		if dst.ti.class == cPtr {
			m.writeOp(f, &ops[0], s)
			break
		}
		m.writeTag(s, lay, 1)
		fl := lay.Variants[1].Fields[0]
		m.writeOp(f, &ops[0], m.at(s, fl.Offset, fl.Type))
	case mir.AggNone:
	case mir.AggOk:
		if len(ops) == 1 {
			fl := lay.Variants[0].Fields[0]
			m.writeOp(f, &ops[0], m.at(s, fl.Offset, fl.Type))
		}
	case mir.AggErr:
		m.convertErr(s, m.operandLoc(f, &ops[0]))
	default:
		source.ICEf(f.curSpan(), "eval: aggregate kind %d", r.Agg)
	}
	if m.tg != nil {
		m.tgCopyCheck(dst, s, dst.ti.size) // one write over the whole destination
	}
	m.mem.copyBytes(dst.a, dst.off, s.a, s.off, dst.ti.size)
}

// repeat writes [A; Len]: the element, then copies of what is written,
// doubling, so the work is O(log Len) copies over Len·size bytes, and none
// for a zero-size element however large Len is.
func (m *Machine) repeat(f *frame, dst loc, r *mir.Rvalue) {
	n := m.constArg(r.Len)
	if m.tg != nil && dst.ti.size == 0 {
		m.tgWriteZero(dst)
	}
	if n == 0 {
		return
	}
	elem := m.ti(dst.ti.ty.Elem)
	first := loc{a: dst.a, off: dst.off, ti: elem}
	m.writeOp(f, &r.A, first)
	if elem.size == 0 {
		return
	}
	total := dst.ti.size // n·size: the array lies inside its allocation
	for done := elem.size; done < total; {
		k := min(done, total-done)
		if m.tg != nil {
			m.tgCopyCheck(loc{a: dst.a, off: dst.off + uint32(done), tag: dst.tag}, dst, k)
		}
		m.mem.copyBytes(dst.a, dst.off+uint32(done), dst.a, dst.off, k)
		done += k
	}
}

// discriminant is the logical discriminant of the value at l (mir
// RvDiscriminant): none 0 and some 1, ok 0 and an error its code, an enum
// variant its discriminant.
func (m *Machine) discriminant(l loc) uint64 {
	switch l.ti.ty.Kind {
	case types.KOptional:
		if l.ti.class == cPtr {
			return b2u(m.load(l) != 0)
		}
		return m.tagAt(l, l.ti.layout)
	case types.KErrorUnion, types.KErrorSet:
		lay := l.ti.layout
		if l.view != nil {
			lay = l.view.layout
		}
		return m.tagAt(l, lay)
	case types.KNamed:
		x := m.tagAt(l, l.ti.layout)
		m.checkTag(l.ti, x)
		return x
	}
	source.ICEf(source.Span{}, "eval: discriminant of %s", l.ti.ty.Kind)
	return 0
}

// fnRef is the types.FnRef of a MIR FnRef of body b.
func (m *Machine) fnRef(f *frame, r mir.FnRef) types.FnRef {
	b := f.code.body
	return types.FnRef{Item: r.Item, Args: b.GenericArgs[r.Args.Start:r.Args.End()]}
}

// term runs a terminator; its static cost is charged.
func (m *Machine) term(f *frame, t *mir.Terminator) {
	if m.tg != nil {
		m.tg.val = noVal
	}
	switch t.Kind {
	case mir.TermGoto:
		f.block, f.stmt = t.Target, 0
	case mir.TermSwitch:
		x := m.scalar(f, &t.On)
		mk := mask(m.operandTI(f, &t.On).width)
		b := f.code.body
		if m.tg != nil {
			m.tgNarrow(f, t.On, x&mk)
		}
		for _, cs := range b.Cases[t.Cases.Start:t.Cases.End()] {
			if cs.Value&mk == x&mk {
				f.block, f.stmt = cs.Target, 0
				return
			}
		}
		if t.Default == mir.NoBlock {
			panicWith(types.PanicUnreachable, "")
		}
		f.block, f.stmt = t.Default, 0
	case mir.TermCall:
		if f.code.blocks[f.block].trace {
			f.block, f.stmt = t.Target, 0 // traceHook
			return
		}
		m.call(f, t)
	case mir.TermTailCall:
		m.tailCall(f, t)
	case mir.TermReturn:
		m.ret()
	case mir.TermPanic:
		msg := ""
		if t.HasMsg {
			msg = m.panicMessage(f, &t.Msg)
		}
		panicWith(t.Panic, msg)
	case mir.TermUnreachable:
		panicWith(types.PanicUnreachable, "")
	case mir.TermOnerrGuard:
		if f.onerr == nil {
			if m.tg != nil && m.tg.opt.Rejected {
				m.skip("onerr guard") // a rejected body has no decision
			}
			source.ICEf(f.curSpan(), "eval: TermOnerrGuard in %s with no onerr decisions", m.items().Get(f.item).Key)
		}
		if f.onerr[f.block] {
			f.block, f.stmt = t.Yes, 0
		} else {
			f.block, f.stmt = t.No, 0
		}
	case mir.TermTypeTest:
		source.ICEf(f.curSpan(), "eval: TermTypeTest in an instance body")
	default:
		source.ICEf(f.curSpan(), "eval: terminator kind %d", t.Kind)
	}
}

// panicMessage reads a panic's message operand: a str, or for PanicMust an
// error-set value, whose error name is the message.
func (m *Machine) panicMessage(f *frame, op *mir.Operand) string {
	l := m.operandLoc(f, op)
	if l.ti.ty.Kind == types.KStr {
		d, n := m.sliceWords(l)
		return string(m.readBytes(d, n, m.takeVal()))
	}
	if isErrorKind(l.ti) {
		lay := l.ti.layout
		if l.view != nil {
			lay = l.view.layout
		}
		code := m.tagAt(l, lay)
		for _, mem := range m.tab().Members(l.ti.ty.Errors) {
			if !mem.IsParam && uint64(m.errorCode(mem.Item)) == code {
				return m.cfg.Interner.Str(m.items().Get(mem.Item).Name)
			}
		}
	}
	return ""
}

// takesIo reports whether a signature has a parameter of type Io (any
// function with no Io parameter can run).
func (m *Machine) takesIo(sig *types.Signature) bool {
	io := m.cfg.Known.Io
	if io == types.NoItem {
		return false
	}
	for _, p := range sig.Params {
		t := m.tab().Get(p.Type)
		if t.Kind == types.KRef || t.Kind == types.KMut {
			t = m.tab().Get(t.Elem)
		}
		if t.Kind == types.KNamed && t.Item == io {
			return true
		}
	}
	return false
}

// call runs a TermCall: by callee kind, with the stage rule applied before
// the body is asked for.
func (m *Machine) call(f *frame, t *mir.Terminator) {
	if t.Spawn != mir.NoLocal {
		if m.tg == nil {
			source.ICEf(f.curSpan(), "eval: spawned call (a scope needs Io, which is refused first)")
		}
		m.tgSpawn(f, t)
		return
	}
	var fn types.FnRef
	switch t.Callee.Kind {
	case mir.CallDirect:
		if cs := m.site(f); cs.cd != nil {
			m.callBody(f, t, *cs)
			return
		}
		fn = m.fnRef(f, t.Callee.Fn)
	case mir.CallValue:
		fn = m.fnOf(m.scalar(f, &t.Callee.Value))
	case mir.CallBuiltin:
		m.builtin(f, t)
		return
	default:
		source.ICEf(f.curSpan(), "eval: interface call in an instance body")
	}
	m.callFn(f, t, fn)
}

// callFn calls fn from the TermCall t of frame f: an override, an
// intrinsic, or a body with its frame.
func (m *Machine) callFn(f *frame, t *mir.Terminator, fn types.FnRef) {
	if m.override(f, t, fn) {
		return
	}
	it := m.items().Get(fn.Item)
	if it.Key == keyRelease {
		// release.go: T's impl of std.core.Free runs in its place;
		// with none, the intrinsic below forgets x.
		if impl, ok := m.freeImpl(f, fn); ok {
			fn, it = impl, m.items().Get(impl.Item)
		}
	}
	valued := false // the callee was keyed by a value read at run time (constCallee)
	if fd, ok := it.Decl.(*types.FuncDecl); ok {
		if m.tg != nil {
			// The soundness host serves its items and lets std's bodies
			// that take Io run.
			if m.tgHostCall(f, t, fn, it, fd) {
				return
			}
		} else if m.takesIo(&fd.Sig) {
			refuse(types.RefuseIo, "")
		}
		if fd.Naked {
			refuse(types.RefuseAsm, "") // a naked body is one asm
		}
		if fd.Bodiless {
			m.intrinsic(f, t, fn, it, fd)
			return
		}
		if t.Callee.Kind == mir.CallDirect && int(t.Args.Len) > len(fd.Sig.Params) {
			fn, valued = m.envCallee(f, t, fn, &fd.Sig)
		} else if t.Callee.Kind == mir.CallDirect && m.forwardsConst(fn, it, &fd.Sig) {
			// The callee passes a const parameter on as a const argument (a
			// forwarded Print format), so it runs as codegen runs it, in
			// mono's instance specialised with the call's const values,
			// where the forwarded format is expanded. constCallee keys every
			// operand form (a place's value is serialized), so the lowered
			// body, whose forwarded Print cannot run, is never entered.
			fn, _, valued = m.constCallee(f, t, fn, &fd.Sig)
		}
	}
	callSpan := f.curSpan()
	cs := m.resolve(fn, it, callSpan)
	b := f.code.body
	if n := t.Args.Len; n != cs.cd.body.ParamCount {
		if m.tg != nil && m.tg.opt.Rejected && t.Callee.Kind == mir.CallDirect {
			// A call with environment arguments in a unit stage 7 rejected:
			// mono serves no rejected unit, so the body is the generic one
			// without the hidden parameters. The run cannot go on
			// faithfully: it ends in poison, as a rejected generic body's
			// instance does.
			m.entering = types.NoItem
			poison()
		}
		source.ICEf(callSpan, "eval: %d arguments to %s, which has %d parameters", n, it.Key, cs.cd.body.ParamCount)
	}
	// A callee keyed by a value read at run time (valued) is not this
	// site's for good: the next run may pass another value (H(1), then
	// H(2) through one shared body of H), so such a site is resolved on
	// every run, never recorded.
	if t.Callee.Kind == mir.CallDirect && t == &b.Blocks[f.block].Term && !valued {
		*m.site(f) = cs
	}
	m.callBody(f, t, cs)
	m.entering = types.NoItem
}

// envCallee is the callee of a direct call with trailing environment arguments
// (a const parameter bound to a capturing literal): mono's instance, keyed as
// codegen's calleeArgs keys it, by the FnRef's generic arguments and then one
// CAValue per const value parameter, because only that body has the hidden
// environment parameters the call passes; the generic body has none. A callee
// with no const parameter (a capturing literal itself, called directly in an
// instance body) is fn unchanged. The bool is constCallee's valued.
func (m *Machine) envCallee(f *frame, t *mir.Terminator, fn types.FnRef, sig *types.Signature) (types.FnRef, bool) {
	g, ok, valued := m.constCallee(f, t, fn, sig)
	if !ok {
		source.ICEf(f.curSpan(), "eval: a const parameter of %s is not a constant at a call with environment arguments",
			m.items().Get(fn.Item).Key)
	}
	return g, valued
}

// constCallee is fn keyed as codegen's calleeArgs keys it: the FnRef's
// generic arguments, then one CAValue per const value parameter, from the
// call's operands. An operand that is a place (a temporary lower left for a
// named const, an aggregate or a field, which mono folds into a constant) is
// keyed by its value, serialized as a const result is, so the instance is
// the one mono makes; a value serialize cannot make a const is refused as
// for a result. ok is false only for an operand of no other kind, none of
// which a call has; fn is then returned unchanged. valued reports a key read
// from a place: it holds for this run of the call only. A callee with no
// const parameter is fn unchanged.
func (m *Machine) constCallee(f *frame, t *mir.Terminator, fn types.FnRef, sig *types.Signature) (g types.FnRef, ok, valued bool) {
	cargs := f.code.body.Args[t.Args.Start:t.Args.End()]
	var args []types.GenericArg
	for pi, p := range sig.Params {
		if !p.Const {
			continue
		}
		if args == nil {
			args = append(make([]types.GenericArg, 0, len(fn.Args)+1), fn.Args...)
		}
		o := &cargs[pi].Operand
		var c types.ConstID
		switch o.Kind {
		case mir.OpConst:
			c = o.Const
		case mir.OpImm:
			ti := m.ti(o.Type)
			b := make([]byte, ti.size)
			types.StoreUint(b, int(ti.size), o.Bits)
			c = m.cfg.Consts.Intern(types.ConstValue{Type: o.Type, Align: ti.align, Bytes: b})
		case mir.OpCopy, mir.OpMove:
			c, valued = m.serialize(m.place(f, o.Place)), true
		default:
			return fn, false, false
		}
		args = append(args, types.ConstGenericArg(types.ConstArg{Kind: types.CAValue, Value: c}))
	}
	if args == nil {
		return fn, true, false
	}
	return types.FnRef{Item: fn.Item, Args: args}, true, valued
}

// forwardsConst reports whether fn (item it, signature sig) has a const
// parameter and its body passes one of them on to a const parameter of a
// direct callee, as the parameter's own local or a once-assigned temporary
// copy of it (lower's slot, which mono's constOf folds): a forwarded
// const, which mono resolves only in the instance specialised with the
// const values (a forwarded Print or Arena.Fmt format is expanded there).
// Any other callee runs its body by the FnRef alone. False when the stage rule would stop the entry (resolve reports
// it) or the body is not ready (resolve reports that too).
func (m *Machine) forwardsConst(fn types.FnRef, it *types.Item, sig *types.Signature) bool {
	has := false
	for _, p := range sig.Params {
		has = has || p.Const
	}
	if !has || m.req.Stage == types.StageSignatures && it.Module == m.req.Module {
		return false
	}
	body := m.cfg.Program.Body(fn.Item, fn.Args)
	b := body.MIR
	if body.State != BodyReady || b == nil {
		return false
	}
	for bi := range b.Blocks {
		t := &b.Blocks[bi].Term
		if (t.Kind != mir.TermCall && t.Kind != mir.TermTailCall) || t.Callee.Kind != mir.CallDirect {
			continue
		}
		cd, ok := m.items().Get(t.Callee.Fn.Item).Decl.(*types.FuncDecl)
		if !ok {
			continue
		}
		args := b.Args[t.Args.Start:t.Args.End()]
		for pi, p := range cd.Sig.Params {
			if !p.Const || pi >= len(args) {
				continue
			}
			if constParamOf(b, sig, args[pi].Operand) {
				return true
			}
		}
	}
	return false
}

// constParamOf reports whether o reads a const parameter of b (signature
// sig) or a field of one (`o.v`): the parameter's place, or a
// temporary whose one assignment is a copy or move of it.
func constParamOf(b *mir.Body, sig *types.Signature, o mir.Operand) bool {
	param := func(o mir.Operand) bool {
		if o.Kind != mir.OpCopy && o.Kind != mir.OpMove {
			return false
		}
		l := int(o.Place.Local)
		return l >= 1 && l <= len(sig.Params) && uint32(l) <= b.ParamCount && sig.Params[l-1].Const
	}
	if param(o) {
		return true
	}
	if (o.Kind != mir.OpCopy && o.Kind != mir.OpMove) || o.Place.Proj.Len != 0 || int(o.Place.Local) >= len(b.Locals) ||
		b.Locals[o.Place.Local].Kind != mir.LocalTemp {
		return false
	}
	var rv *mir.Rvalue
	for bi := range b.Blocks {
		for si := range b.Blocks[bi].Stmts {
			st := &b.Blocks[bi].Stmts[si]
			if st.Kind == mir.StAssign && st.Place.Local == o.Place.Local {
				if rv != nil || st.Place.Proj.Len != 0 {
					return false
				}
				rv = &st.Rvalue
			}
		}
	}
	return rv != nil && rv.Kind == mir.RvUse && param(rv.A)
}

// resolve applies the stage rule to the body of fn (item it), entered from at,
// asks for it, records ReadBody and enters it; the caller pushes its frame and
// then clears m.entering.
func (m *Machine) resolve(fn types.FnRef, it *types.Item, at source.Span) callSite {
	if m.req.Stage == types.StageSignatures && it.Module == m.req.Module {
		panic(&stop{err: &types.EvalError{Kind: types.EvalStage}, entered: fn.Item, enteredAt: at})
	}
	m.entering, m.enteringAt = fn.Item, at
	body := m.cfg.Program.Body(fn.Item, fn.Args)
	m.bodyState(fn.Item, &body)
	m.bodyReads(it.Key, &body)
	cd, ce := m.enter(body.MIR, at)
	return callSite{cd: cd, ce: ce, item: fn.Item, onerr: body.OnerrRuns}
}

// bodyReads records the entry of body (item key): ReadBody of its hash and
// the reads the Program made building it (Body.Reads).
func (m *Machine) bodyReads(key types.Key, body *Body) {
	s := m.seg()
	s.reads.Add(types.Read{Kind: types.ReadBody, Key: key, Hash: body.Hash})
	for _, r := range body.Reads {
		s.reads.Add(r)
	}
}

// callSite is a direct call's callee as its first run in this segment
// resolved it: the same item, body, reads and stage outcome on every later
// run of the site in the segment, so those only push the frame.
// The record lives in the segment's entry of the calling body.
type callSite struct {
	cd    *compiled
	ce    *centry
	item  types.ItemID
	onerr []bool
}

// site is the call-site record of the terminator of f's current block.
func (m *Machine) site(f *frame) *callSite {
	ss := f.ce.sites
	if ss == nil {
		ss = make([]callSite, len(f.code.blocks))
		f.ce.sites = ss
	}
	return &ss[f.block]
}

// callBody pushes the callee's frame (charged) and copies the
// arguments.
func (m *Machine) callBody(f *frame, t *mir.Terminator, cs callSite) {
	tail := m.tailing
	m.tailing = false
	cb := &f.code.blocks[f.block]
	m.pushFrame(cs.cd, cs.ce, cs.item, cs.onerr, f.curSpan(), cb.cost[len(cb.ins)])
	args := f.code.body.Args[t.Args.Start:t.Args.End()]
	nf := &m.frames[len(m.frames)-1]
	caller := &m.frames[len(m.frames)-2]
	for i := range args {
		if m.tg != nil && t.HasErr {
			m.tgArgUndo(caller, &args[i]) // undone on the error edge
		}
		m.writeOp(caller, &args[i].Operand, nf.local(mir.LocalID(i+1)))
		if m.tg != nil && args[i].Mode == mir.ArgMove && args[i].Operand.Kind == mir.OpCopy {
			// lower passes a non-plain value as a Copy operand in mode
			// ArgMove: the argument moved all the same.
			m.tgMoved(caller, args[i].Operand.Place, m.place(caller, args[i].Operand.Place))
		}
	}
	if m.tg != nil {
		m.tgEnter(&m.frames[len(m.frames)-1])
		if cs.item == m.tg.resetItem && cs.item != types.NoItem {
			// Arena.Reset ends what the arena held (tagarena.go):
			// at the call, so Removed is the caller's Reset.
			caller := &m.frames[len(m.frames)-2]
			m.tgArenaReset(&m.frames[len(m.frames)-1], caller.curSpan(), caller.item)
		}
	}
	if tail {
		m.replaceCaller()
	}
}

// bodyState turns a body that cannot run into poison or an ICE.
func (m *Machine) bodyState(item types.ItemID, b *Body) {
	switch b.State {
	case BodyReady:
		if b.MIR == nil {
			source.ICEf(source.Span{}, "eval: ready body of %s has no MIR", m.items().Get(item).Key)
		}
	case BodyPoisoned:
		poison()
	case BodyNotLowered:
		source.ICEf(m.items().Get(item).Span, "eval: body of %s is not lowered", m.items().Get(item).Key)
	default:
		source.ICEf(m.items().Get(item).Span, "eval: no body for %s", m.items().Get(item).Key)
	}
}

// ret runs a TermReturn: local 0 goes to the caller's destination, or, for
// a fallible call with an error edge, the error to the edge's local; the
// frame is freed. The return of a segment's outermost frame ends it.
func (m *Machine) ret() {
	top := len(m.frames) - 1
	if top == m.seg().base {
		m.endSegment()
		return
	}
	f := &m.frames[top]
	caller := &m.frames[top-1]
	t := &caller.code.body.Blocks[caller.block].Term
	res := f.local(mir.ReturnLocal)
	if f.arena {
		m.seg().arena = m.load(res) // the segment's EvalArena, kept
	}
	if f.hostArena {
		// Pushed by a host's Arena(): the caller's terminator is the host
		// call, which goes on when the host returns.
		m.popFrame()
		return
	}
	if f.detached {
		// Pushed by the soundness host (taghost.go): a task's call or a
		// host-run std function; the result goes to its sink, if any.
		if f.hasSink {
			m.copyValue(f.sink, res)
		}
		m.popFrame()
		return
	}
	next := t.Target
	if t.HasErr {
		if code := m.tagAt(res, res.ti.layout); code != 0 {
			m.convertErr(caller.local(t.Err.Error), res)
			next = t.Err.Block
			if m.tg != nil {
				m.tgErrorEdge(f, caller)
			}
		} else if ok := res.ti.layout.Variants[0].Fields; len(ok) == 1 {
			dst := m.place(caller, t.Dest)
			m.copyValue(dst, m.at(res, ok[0].Offset, ok[0].Type))
		}
	} else if dst := m.place(caller, t.Dest); dst.ti.class != cVoid {
		if m.tg != nil && res.ti.ty.Kind == types.KErrorUnion && m.tagAt(res, res.ti.layout) != 0 {
			m.tgErrorExit(f) // an error value returned to a caller that keeps it
		}
		m.copyValue(dst, res)
	}
	caller.block, caller.stmt = next, 0
	m.popFrame()
	if t.Kind == mir.TermTailCall {
		// The caller made a tail call that kept its frame (the tagged
		// mode, tailcall.go): its _0 now holds the result, which it returns.
		m.ret()
	}
}

// traceHook reports whether item is std.rt.TraceFail or TraceTry, the
// error-return trace's hooks, which lower calls before every error return in
// every profile. A trace is a run-time record of the running task, which
// compile time has not: the evaluator skips the call, charging no step for it
// (its block's terminator cost is 0), so step counts are those of a body
// without the calls and the frozen step golden holds.
func traceHook(items *types.Items, item types.ItemID) bool {
	it := items.Get(item)
	if it == nil || it.Kind != types.KindFunc {
		return false
	}
	switch it.Key.String() {
	case "std.rt.TraceFail", "std.rt.TraceTry":
		return true
	}
	return false
}

// staticSlice is an array aggregate whose destination is a []T: the static
// slice of a const initializer. The elements go into memory the evaluation
// owns, never its frame, so the slice outlives the initializer's frame, and
// serializing the result makes them static data, as a string literal's bytes
// are. An empty literal is the empty slice: null data, length 0, which
// serializes to the [0]T const.
func (m *Machine) staticSlice(f *frame, dst loc, ops []mir.Operand) {
	elem := m.ti(dst.ti.ty.Elem)
	n := uint64(len(ops))
	var w uint64
	if n > 0 {
		a := m.mem.newAlloc(allocOwn, max(n*elem.size, 1))
		for i := range ops {
			m.writeOp(f, &ops[i], loc{a: a, off: uint32(uint64(i) * elem.size), ti: elem})
		}
		w = ptrWord(a, 0)
	}
	if m.tg != nil {
		m.tacc(dst.a, dst.off, 16, dst.tag, accWrite) // the elements are owner memory
	}
	al := m.mem.access(dst.a, dst.off, 16, true)
	al.storePtr(dst.off, w)
	al.storeScalar(dst.off+8, 8, n)
}
