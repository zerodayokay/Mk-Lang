// Why: std's bodiless functions (std.raw's pointer and memory operations,
// the page allocator, the runtime's hooks) have no MIR to run; the evaluator
// must give each its meaning, or refuse it with a clear reason.
//
// What: the intrinsics table by stable key, the refusals table of
// runtime-only intrinsics, Machine.intrinsic (the dispatch), override (the
// page allocator's allocate and free, and Evaluating), and the builtins
// such as EvalArena.
//
// How: a bodiless callee is looked up by stable key in eval's own table and
// the SIMD table, then the host's; a runtime-only intrinsic is refused, any
// other bodiless extern is RefuseExtern, and an unknown intrinsic is an ICE.
// Raw access is allowed only inside the evaluation's own allocations, and
// pointer bytes are never read as integers.

package eval

import (
	"bytes"
	"math"
	"math/bits"

	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

// intrinsicFunc runs one bodiless callee inside the interpreter: it reads
// the arguments of t in frame f and writes t.Dest, then the caller goes on
// to t.Target.
type intrinsicFunc func(m *Machine, f *frame, t *mir.Terminator, fn types.FnRef)

// refusals are the runtime-only intrinsics by stable key, with the refusal
// each gets: codegen implements each, and no compile-time evaluation may run
// one. check marks every bodiless non-extern std function Intrinsic, so
// without this table a compile-time path reaching one (rt.PanicErr's
// ErrorName, rt.FaultSite's FuncTable) would be the E0000 of an unknown
// intrinsic, not E0901.
var refusals = map[string]types.Refusal{
	"std.sys.Syscall":      types.RefuseSyscall,
	"std.sys.Clone":        types.RefuseExtern,
	"std.sys.Restorer":     types.RefuseExtern,
	"std.sys.LibcBuild":    types.RefuseExtern, // a fact of the link, never of a constant
	"std.sys.Teb":          types.RefuseExtern,
	"std.task.Switch":      types.RefuseExtern,
	"std.task.InitContext": types.RefuseExtern,
	"std.rt.CallMain":      types.RefuseExtern,
	"std.rt.ErrorName":     types.RefuseExtern,
	"std.rt.ProfileTable":  types.RefuseExtern,
	"std.rt.FuncTable":     types.RefuseExtern,
	"std.rt.StackPointer":  types.RefusePointerBytes,
	"std.rt.UsesTasks":     types.RefuseExtern, // mono folds it after evaluation
	// A fact of the link (which externs bind lazily) and of the machine the
	// program runs on, never of a constant.
	"std.dl.Present":      types.RefuseExtern,
	"std.sys.LazyPresent": types.RefuseExtern,
}

// quoteKey is the key a host implements BuiltinQuote under.
const quoteKey = "#quote"

func (m *Machine) argOp(f *frame, t *mir.Terminator, i int) *mir.Operand {
	if uint32(i) >= t.Args.Len {
		source.ICEf(f.curSpan(), "eval: intrinsic argument %d of %d", i, t.Args.Len)
	}
	return &f.code.body.Args[t.Args.Start+uint32(i)].Operand
}

func (m *Machine) argU(f *frame, t *mir.Terminator, i int) uint64 {
	return m.scalar(f, m.argOp(f, t, i))
}

// argCount reads an int argument; negative is refused as raw access (a count
// or offset that native code would misuse).
func (m *Machine) argCount(f *frame, t *mir.Terminator, i int) uint64 {
	x := m.argU(f, t, i)
	if int64(x) < 0 {
		refuse(types.RefuseRawAccess, "negative count")
	}
	return x
}

func (m *Machine) targ(f *frame, fn types.FnRef, k int) *tinfo {
	if k >= len(fn.Args) || fn.Args[k].IsConst {
		source.ICEf(f.curSpan(), "eval: intrinsic %s without type argument %d", m.items().Get(fn.Item).Key, k)
	}
	return m.ti(fn.Args[k].Type)
}

func (m *Machine) dest(f *frame, t *mir.Terminator) loc { return m.place(f, t.Dest) }

// ptrLoc points p at type ti; null is refused. In the tagged mode the
// location carries the tag in flight, the pointer's own: a raw access is an
// access with the raw pointer's tag.
func (m *Machine) ptrLoc(p uint64, ti *tinfo) loc {
	t := m.takeVal()
	if p == 0 {
		refuse(types.RefuseRawAccess, "null")
	}
	return loc{a: ptrAlloc(p), off: ptrOff(p), tag: t, ti: ti}
}

// storeSlice writes a slice value (p, n) to dst. In the tagged mode a slice
// made from a raw pointer is a retag from the pointer's tag over its n
// elements of elem, Unique when unique.
func storeSlice(m *Machine, dst loc, p, n uint64, elem *tinfo, unique bool) {
	var t uint32
	if m.tg != nil {
		pt := m.takeVal()
		if p != 0 {
			if b, ok := mulBytes(n, elem.size); ok {
				t = m.retag(loc{a: ptrAlloc(p), off: ptrOff(p), tag: pt}, b, elem, n, unique, 0)
			} else {
				t = pt
			}
		}
		m.tacc(dst.a, dst.off, 16, dst.tag, accWrite)
	}
	al := m.mem.access(dst.a, dst.off, 16, true)
	al.storePtr(dst.off, p)
	al.storeScalar(dst.off+8, 8, n)
	if m.tg != nil && p != 0 {
		m.setVal(t)
		m.tgStorePtr(dst.a, dst.off, p)
	}
}

// sliceElem is the element type of the slice or str type of dst.
func (m *Machine) sliceElem(dst loc) *tinfo {
	if dst.ti.ty.Kind == types.KStr {
		return m.ti(types.U8)
	}
	return m.ti(dst.ti.ty.Elem)
}

func samePtr(m *Machine, f *frame, t *mir.Terminator, _ types.FnRef) {
	m.store(m.dest(f, t), m.argU(f, t, 0))
}

func readRaw(m *Machine, f *frame, t *mir.Terminator, fn types.FnRef) {
	src := m.ptrLoc(m.argU(f, t, 0), m.targ(f, fn, 0))
	dst := m.dest(f, t)
	if src.ti.isScalar() {
		m.store(dst, m.load(src)) // the load's tag checks refuse pointer bytes here
		return
	}
	m.copyValue(dst, src)
}

func copyRaw(m *Machine, f *frame, t *mir.Terminator, fn types.FnRef) {
	d := m.argU(f, t, 0)
	dt := m.takeVal()
	s := m.argU(f, t, 1)
	st := m.takeVal()
	hi, n := bits.Mul64(m.argCount(f, t, 2), m.targ(f, fn, 0).size)
	if hi != 0 || n > maxAlloc {
		// More bytes than any allocation holds: never a wrapped short copy.
		refuse(types.RefuseRawAccess, "")
	}
	m.charge(c(n))
	if n == 0 {
		return
	}
	if d == 0 || s == 0 {
		refuse(types.RefuseRawAccess, "null")
	}
	if m.tg != nil {
		dl, sl := loc{a: ptrAlloc(d), off: ptrOff(d), tag: dt}, loc{a: ptrAlloc(s), off: ptrOff(s), tag: st}
		m.tgCopyCheck(dl, sl, n)
		m.mem.copyBytes(dl.a, dl.off, sl.a, sl.off, n)
		m.tgCopyInit(dl, sl, n)
		return
	}
	m.mem.copyBytes(ptrAlloc(d), ptrOff(d), ptrAlloc(s), ptrOff(s), n)
}

// intrinsics is eval's own table by stable key.
var intrinsics = map[string]intrinsicFunc{
	"std.raw.Of":    samePtr,
	"std.raw.OfMut": samePtr,
	"std.raw.Cast":  samePtr,
	// std declares the pointer cast as raw.PtrCast[T, U], because Cast
	// is a universe name (E0102); the Cast key stays for that spelling.
	"std.raw.PtrCast": samePtr,
	"std.raw.Null":    func(m *Machine, f *frame, t *mir.Terminator, _ types.FnRef) { m.store(m.dest(f, t), 0) },
	"std.raw.IsNull": func(m *Machine, f *frame, t *mir.Terminator, _ types.FnRef) {
		m.store(m.dest(f, t), b2u(m.argU(f, t, 0) == 0))
	},
	"std.raw.Same": func(m *Machine, f *frame, t *mir.Terminator, _ types.FnRef) {
		m.store(m.dest(f, t), b2u(m.argU(f, t, 0) == m.argU(f, t, 1)))
	},
	"std.raw.Offset": func(m *Machine, f *frame, t *mir.Terminator, fn types.FnRef) {
		p := m.argU(f, t, 0)
		cnt, size := int64(m.argU(f, t, 1)), m.targ(f, fn, 0).size
		if cnt == 0 || size == 0 {
			m.store(m.dest(f, t), p)
			return
		}
		// |cnt|·size is computed without wrapping: one beyond 32 bits is
		// outside every allocation.
		mag := uint64(cnt)
		if cnt < 0 {
			mag = -mag
		}
		hi, lo := bits.Mul64(mag, size)
		if p == 0 || hi != 0 || lo > math.MaxUint32 {
			refuse(types.RefuseRawAccess, "")
		}
		off := int64(ptrOff(p)) + int64(lo)
		if cnt < 0 {
			off = int64(ptrOff(p)) - int64(lo)
		}
		if off < 0 || off > math.MaxUint32 {
			refuse(types.RefuseRawAccess, "")
		}
		m.store(m.dest(f, t), ptrWord(ptrAlloc(p), uint32(off)))
	},
	"std.raw.Read":          readRaw,
	"std.raw.ReadUnaligned": readRaw,
	"std.raw.Write": func(m *Machine, f *frame, t *mir.Terminator, fn types.FnRef) {
		m.writeOp(f, m.argOp(f, t, 1), m.ptrLoc(m.argU(f, t, 0), m.targ(f, fn, 0)))
	},
	"std.raw.Copy": copyRaw,
	"std.raw.Move": copyRaw,
	"std.raw.Fill": func(m *Machine, f *frame, t *mir.Terminator, _ types.FnRef) {
		d, v, n := m.argU(f, t, 0), m.argU(f, t, 1), m.argCount(f, t, 2)
		m.charge(c(n))
		if n == 0 {
			return
		}
		dl := m.ptrLoc(d, nil)
		if m.tg != nil {
			m.tacc(dl.a, dl.off, n, dl.tag, accWrite)
		}
		m.mem.access(dl.a, dl.off, n, true).fill(dl.off, uint32(n), byte(v))
	},
	"std.raw.Compare": func(m *Machine, f *frame, t *mir.Terminator, _ types.FnRef) {
		a := m.argU(f, t, 0)
		at := m.takeVal()
		b := m.argU(f, t, 1)
		bt := m.takeVal()
		n := m.argCount(f, t, 2)
		m.charge(c(n))
		r := bytes.Compare(m.readBytes(a, n, at), m.readBytes(b, n, bt))
		m.store(m.dest(f, t), uint64(int64(r)))
	},
	"std.raw.Slice": func(m *Machine, f *frame, t *mir.Terminator, _ types.FnRef) {
		dst := m.dest(f, t)
		storeSlice(m, dst, m.argU(f, t, 0), m.argCount(f, t, 1), m.sliceElem(dst), false)
	},
	"std.raw.SliceMut": func(m *Machine, f *frame, t *mir.Terminator, _ types.FnRef) {
		dst := m.dest(f, t)
		storeSlice(m, dst, m.argU(f, t, 0), m.argCount(f, t, 1), m.sliceElem(dst), true)
	},
	"std.raw.Str": func(m *Machine, f *frame, t *mir.Terminator, _ types.FnRef) {
		dst := m.dest(f, t)
		storeSlice(m, dst, m.argU(f, t, 0), m.argCount(f, t, 1), m.sliceElem(dst), false)
	},
	"std.raw.Data":    sliceData,
	"std.raw.DataMut": sliceData,
	"std.raw.StrData": sliceData,
	"std.raw.Addr":    func(*Machine, *frame, *mir.Terminator, types.FnRef) { refuse(types.RefusePointerBytes, "") },
	"std.raw.FromAddr": func(m *Machine, f *frame, t *mir.Terminator, _ types.FnRef) {
		if m.argU(f, t, 0) == 0 {
			m.store(m.dest(f, t), 0)
			return
		}
		// A pointer no evaluation made: it names a dead allocation, so any
		// dereference is refused (raw-access).
		dst := m.dest(f, t)
		a := m.mem.addDead(allocOwn)
		m.setVal(0)
		m.store(dst, ptrWord(a, 0))
	},
	"std.raw.Forget": func(*Machine, *frame, *mir.Terminator, types.FnRef) {},
	"std.raw.Fence":  func(*Machine, *frame, *mir.Terminator, types.FnRef) {},
	"std.raw.IsLinear": func(m *Machine, f *frame, t *mir.Terminator, fn types.FnRef) {
		m.store(m.dest(f, t), b2u(m.targ(f, fn, 0).layout.Shape.Kind == types.Linear))
	},
	// release.go: callFn ran T's Free instead when T has one.
	"std.raw.Release": releaseForget,
	"std.raw.HasFree": hasFree,

	typeKeyIntrinsic: keyOf, // codegen's constant
	"std.raw.Fail": func(m *Machine, f *frame, t *mir.Terminator, _ types.FnRef) {
		code := types.PanicCode(m.argU(f, t, 0))
		d, n := m.sliceWords(m.operandLoc(f, m.argOp(f, t, 1)))
		panicWith(code, string(m.readBytes(d, n, m.takeVal())))
	},
	"std.raw.Ctz64": func(m *Machine, f *frame, t *mir.Terminator, _ types.FnRef) {
		m.store(m.dest(f, t), uint64(bits.TrailingZeros64(m.argU(f, t, 0))))
	},
	"std.raw.Clz64": func(m *Machine, f *frame, t *mir.Terminator, _ types.FnRef) {
		m.store(m.dest(f, t), uint64(bits.LeadingZeros64(m.argU(f, t, 0))))
	},
	"std.raw.Popcount64": func(m *Machine, f *frame, t *mir.Terminator, _ types.FnRef) {
		m.store(m.dest(f, t), uint64(bits.OnesCount64(m.argU(f, t, 0))))
	},
	"std.raw.RotL64": func(m *Machine, f *frame, t *mir.Terminator, _ types.FnRef) {
		m.store(m.dest(f, t), bits.RotateLeft64(m.argU(f, t, 0), int(int64(m.argU(f, t, 1))%64)))
	},
	"std.raw.Mul128": func(m *Machine, f *frame, t *mir.Terminator, _ types.FnRef) {
		hi, lo := bits.Mul64(m.argU(f, t, 0), m.argU(f, t, 1))
		dst := m.dest(f, t)
		for i, fd := range dst.ti.ty.Fields {
			v := lo
			if m.cfg.Interner.Str(fd.Name) == "hi" {
				v = hi
			}
			fl := dst.ti.layout.Fields[i]
			m.store(m.at(dst, fl.Offset, fl.Type), v)
		}
	},
	"std.raw.F64Bits": samePtrBits,
	"std.raw.F64From": samePtrBits,
	"std.raw.F32Bits": samePtrBits,
	"std.raw.F32From": samePtrBits,
	"std.raw.SqrtF64": func(m *Machine, f *frame, t *mir.Terminator, _ types.FnRef) {
		m.store(m.dest(f, t), f64bits(math.Sqrt(f64of(m.argU(f, t, 0)))))
	},
	// Select copies a or b whole, as an assignment would: T is Plain.
	"std.raw.Select": func(m *Machine, f *frame, t *mir.Terminator, _ types.FnRef) {
		op := m.argOp(f, t, 2)
		if m.argU(f, t, 0) != 0 {
			op = m.argOp(f, t, 1)
		}
		dst := m.dest(f, t)
		m.writeOp(f, op, loc{a: dst.a, off: dst.off, ti: m.operandTI(f, op)})
	},
	"std.raw.Atomic:New": func(m *Machine, f *frame, t *mir.Terminator, _ types.FnRef) {
		dst := m.dest(f, t)
		m.writeOp(f, m.argOp(f, t, 0), loc{a: dst.a, off: dst.off, ti: m.operandTI(f, m.argOp(f, t, 0))})
	},
	"std.raw.Atomic:Load": func(m *Machine, f *frame, t *mir.Terminator, fn types.FnRef) {
		m.store(m.dest(f, t), m.load(m.ptrLoc(m.argU(f, t, 0), m.targ(f, fn, 0))))
	},
	"std.raw.Atomic:Store": func(m *Machine, f *frame, t *mir.Terminator, fn types.FnRef) {
		m.store(m.ptrLoc(m.argU(f, t, 0), m.targ(f, fn, 0)), m.argU(f, t, 1))
	},
	"std.raw.Atomic:Add": func(m *Machine, f *frame, t *mir.Terminator, fn types.FnRef) {
		cell := m.ptrLoc(m.argU(f, t, 0), m.targ(f, fn, 0))
		old := m.load(cell)
		m.store(cell, (old+m.argU(f, t, 1))&mask(cell.ti.width)) // wraps
		m.store(m.dest(f, t), old)
	},
	"std.raw.Atomic:Swap": func(m *Machine, f *frame, t *mir.Terminator, fn types.FnRef) {
		cell := m.ptrLoc(m.argU(f, t, 0), m.targ(f, fn, 0))
		old := m.load(cell)
		ot := m.takeVal() // the old word's tag, for the result
		m.store(cell, m.argU(f, t, 1))
		dst := m.dest(f, t)
		m.setVal(ot)
		m.store(dst, old)
	},
	"std.raw.Atomic:Cas": func(m *Machine, f *frame, t *mir.Terminator, fn types.FnRef) {
		cell := m.ptrLoc(m.argU(f, t, 0), m.targ(f, fn, 0))
		ok := m.load(cell) == m.argU(f, t, 1)
		if ok {
			m.store(cell, m.argU(f, t, 2))
		}
		if m.tg != nil {
			m.tg.race.casOK = ok // which order applies (tagrace_order.go)
		}
		m.store(m.dest(f, t), b2u(ok))
	},
	"std.core.str:Len": func(m *Machine, f *frame, t *mir.Terminator, _ types.FnRef) {
		_, n := m.sliceWords(m.operandLoc(f, m.argOp(f, t, 0)))
		m.store(m.dest(f, t), n)
	},
	// Slice[T].Len(&self): int (std/core/core_slice.mk): the length word of
	// the []T (or mut []T) the argument points at, as RvLen reads it.
	"std.core.Slice:Len": func(m *Machine, f *frame, t *mir.Terminator, _ types.FnRef) {
		l := m.ptrLoc(m.argU(f, t, 0), m.ti(types.Int))
		if l.off > math.MaxUint32-8 {
			refuse(types.RefuseRawAccess, "") // past any allocation
		}
		l.off += 8
		m.store(m.dest(f, t), m.load(l))
	},
	"std.core.str:AsBytes": func(m *Machine, f *frame, t *mir.Terminator, _ types.FnRef) {
		src := m.operandLoc(f, m.argOp(f, t, 0))
		dst := m.dest(f, t)
		if m.tg != nil {
			m.tgCopyCheck(dst, src, 16)
		}
		m.mem.copyBytes(dst.a, dst.off, src.a, src.off, 16)
	},
}

func sliceData(m *Machine, f *frame, t *mir.Terminator, _ types.FnRef) {
	d, _ := m.sliceWords(m.operandLoc(f, m.argOp(f, t, 0)))
	m.store(m.dest(f, t), d)
}

// samePtrBits moves a scalar's bits unchanged (a bit cast).
func samePtrBits(m *Machine, f *frame, t *mir.Terminator, _ types.FnRef) {
	m.store(m.dest(f, t), m.argU(f, t, 0))
}

// intrinsic runs a call to a bodiless function.
func (m *Machine) intrinsic(f *frame, t *mir.Terminator, fn types.FnRef, it *types.Item, fd *types.FuncDecl) {
	key := it.Key.String()
	run, ok := intrinsics[key]
	if !ok {
		run, ok = simdIntrinsics[key]
	}
	if ok {
		// The race check: the edges the intrinsic's order makes, on its
		// location (tagrace_order.go), after it ran; its own access
		// synchronizes and is not checked.
		var op atomicOp
		var a, off uint32
		atomic := false
		if m.tg != nil && m.tg.race.on && !m.tg.opt.NoRaces {
			op, atomic = m.atomicOpOf(f, t, key)
			if atomic && op.kind != akFence {
				w := m.argU(f, t, 0)
				m.takeVal()
				a, off = ptrAlloc(w), ptrOff(w)
			}
			m.tg.race.atomic = atomic
		}
		run(m, f, t, fn)
		if atomic {
			m.raceAtomicOp(op, a, off)
		}
		if m.tg != nil {
			m.tg.race.atomic = false
			m.tgMovedArgs(f, t)
		}
		f.block, f.stmt = t.Target, 0
		return
	}
	if m.host != nil {
		if hf, ok := m.host.Intrinsic(it.Key); ok {
			m.hostCall(f, t, fn, hf)
			return
		}
	}
	if r, ok := refusals[key]; ok {
		refuse(r, "")
	}
	if r, ok := sysRefusals[key]; ok {
		refuse(r, "")
	}
	if fd.Sig.Extern || !fd.Intrinsic {
		refuse(types.RefuseExtern, "")
	}
	source.ICEf(f.curSpan(), "eval: intrinsic %s has no evaluator implementation", key)
}

// override runs an evaluator replacement of a std function written over sys:
// the page allocator's allocate and free, named by Config.PageAlloc and
// Config.PageFree, and Config.Evaluating, true here. It reports whether fn
// is one.
func (m *Machine) override(f *frame, t *mir.Terminator, fn types.FnRef) bool {
	switch {
	case fn.Item == types.NoItem:
		return false
	case fn.Item == m.cfg.PageAlloc:
		// PageAlloc(size: int): *u8, a fresh zeroed own allocation.
		n := m.argCount(f, t, 0)
		m.charge(c(n))
		dst := m.dest(f, t)
		if m.tg != nil && m.tg.failAlloc() {
			m.setVal(0)
			m.store(dst, 0) // null: the page source is out of memory
			break
		}
		a := m.mem.newAlloc(allocOwn, n)
		m.setVal(0) // page memory is the owner's
		m.store(dst, ptrWord(a, 0))
	case fn.Item == m.cfg.PageFree:
		// PageFree(p: *u8, size: int): the allocation goes dead.
		p := m.argU(f, t, 0)
		if p == 0 || ptrOff(p) != 0 || m.mem.kindOf(ptrAlloc(p)) != allocOwn || !m.mem.isLive(ptrAlloc(p)) {
			refuse(types.RefuseRawAccess, "")
		}
		if m.tg != nil {
			m.tgDied(ptrAlloc(p))
		}
		m.mem.free(ptrAlloc(p))
	case fn.Item == m.cfg.Evaluating:
		// Evaluating(): bool, true in every evaluation; the call's static
		// cost is its charge.
		m.store(m.dest(f, t), 1)
	default:
		return false
	}
	f.block, f.stmt = t.Target, 0
	return true
}

// builtin runs a CallBuiltin.
func (m *Machine) builtin(f *frame, t *mir.Terminator) {
	switch t.Callee.Builtin {
	case mir.BuiltinQuote:
		if m.host != nil {
			if hf, ok := m.host.Intrinsic(types.KeyFromString(quoteKey)); ok {
				m.hostCall(f, t, types.FnRef{}, hf)
				return
			}
		}
		source.ICEf(f.curSpan(), "eval: quote outside a generator run")
	case mir.BuiltinEvalArena:
		// The segment's one arena: the std function Config.EvalArena builds it
		// over the page overrides on first use.
		if a := m.seg().arena; a != 0 {
			dst := m.dest(f, t)
			m.setVal(0) // the arena's memory is the owner's
			m.store(dst, a)
			f.block, f.stmt = t.Target, 0
			return
		}
		if m.cfg.EvalArena == types.NoItem {
			source.ICEf(f.curSpan(), "eval: EvalArena() with no Config.EvalArena function")
		}
		m.callFn(f, t, types.FnRef{Item: m.cfg.EvalArena})
		m.frames[len(m.frames)-1].arena = true
	default:
		source.ICEf(f.curSpan(), "eval: builtin %d", t.Callee.Builtin)
	}
}
