// Why: some intrinsics of std.mem, std.atomic, std.runtime, std.rt and
// std.raw talk about the running machine (cycle counts, code addresses,
// frame pointers, cache hints); each still needs a defined meaning, or a
// clear refusal, at compile time.
//
// What: init registers them in eval's own table:
//
//   - mem.OffsetOf[T](field): the field's offset in T's layout; a field T
//     lacks panics, so a compile-time evaluation reports E0901.
//   - mem.Prefetch and atomic.Pause: nothing (hints).
//   - mem.Zeroed[T](): the destination's bytes cleared (check has refused
//     a T that is not zero-valid).
//   - mem.AsBytes[T](p) and mem.SliceAsBytes[T](xs): read-only []u8 view
//     over p or xs (check has refused padded and reference-holding types).
//   - raw.AtomicCopy: raw.Move (whole elements, overlap as memmove).
//   - raw.AtomicFill: x written n times.
//   - runtime.Cycles: refused (extern), a fact of the run, never of a
//     constant, as sys.LibcBuild is.
//   - raw.FnFromAddr: refused (pointer-bytes): the evaluator has no code
//     address an integer could name.
//   - rt.FramePointers and rt.FramePointer: refused, facts of the build and
//     of the run.
//
// How: the evaluator runs one thread, so each has its single-threaded
// meaning or a refusal, the same model the memory model states for the
// evaluator.

package eval

import (
	"math/bits"

	"mk/internal/mir"
	"mk/internal/types"
)

func init() {
	// Refused from eval's own table, as std.raw.Addr is: refusals holds
	// only the runtime's own list (TestRuntimeIntrinsicsRefused).
	intrinsics["std.runtime.Cycles"] = func(*Machine, *frame, *mir.Terminator, types.FnRef) {
		refuse(types.RefuseExtern, "runtime.Cycles is a fact of the run")
	}
	intrinsics["std.raw.FnFromAddr"] = func(*Machine, *frame, *mir.Terminator, types.FnRef) {
		refuse(types.RefusePointerBytes, "raw.FnFromAddr: no code address")
	}
	// The backtrace's: the build's frame-pointer setting is a fact of the
	// build, rbp one of the run.
	intrinsics["std.rt.FramePointers"] = func(*Machine, *frame, *mir.Terminator, types.FnRef) {
		refuse(types.RefuseExtern, "rt.FramePointers is a fact of the build")
	}
	intrinsics["std.rt.FramePointer"] = func(*Machine, *frame, *mir.Terminator, types.FnRef) {
		refuse(types.RefusePointerBytes, "rt.FramePointer: no frame chain")
	}
	nothing := func(*Machine, *frame, *mir.Terminator, types.FnRef) {}
	passBool := func(m *Machine, f *frame, t *mir.Terminator, _ types.FnRef) {
		m.store(m.dest(f, t), m.argU(f, t, 0)&1)
	}
	intrinsics["std.runtime.Cold"] = nothing
	intrinsics["std.runtime.Likely"] = passBool
	intrinsics["std.runtime.Unlikely"] = passBool
	intrinsics["std.mem.Prefetch"] = nothing
	intrinsics["std.atomic.Pause"] = nothing
	intrinsics["std.raw.AtomicCopy"] = copyRaw
	intrinsics["std.raw.AtomicFill"] = func(m *Machine, f *frame, t *mir.Terminator, fn types.FnRef) {
		ti := m.targ(f, fn, 0)
		d := m.argU(f, t, 0)
		dt := m.takeVal()
		n := m.argCount(f, t, 2)
		hi, size := bits.Mul64(n, ti.size)
		if hi != 0 || size > maxAlloc {
			refuse(types.RefuseRawAccess, "")
		}
		m.charge(c(size))
		if n == 0 {
			return
		}
		if d == 0 {
			refuse(types.RefuseRawAccess, "null")
		}
		// x is any plain T of 1, 2, 4 or 8 bytes: written
		// from its operand, as a store of the value would be.
		for i := uint64(0); i < n; i++ {
			m.writeOp(f, m.argOp(f, t, 1), loc{a: ptrAlloc(d), off: ptrOff(d) + uint32(i*ti.size), tag: dt, ti: ti})
		}
	}
	intrinsics["std.mem.Zeroed"] = func(m *Machine, f *frame, t *mir.Terminator, _ types.FnRef) {
		d := m.dest(f, t)
		n := d.ti.size
		m.charge(c(n))
		if n == 0 {
			return
		}
		if m.tg != nil {
			m.tacc(d.a, d.off, n, d.tag, accWrite)
		}
		m.mem.access(d.a, d.off, n, true).fill(d.off, uint32(n), 0)
	}
	intrinsics["std.mem.AsBytes"] = func(m *Machine, f *frame, t *mir.Terminator, fn types.FnRef) {
		ti := m.targ(f, fn, 0)
		dst := m.dest(f, t)
		p := m.argU(f, t, 0)
		storeSlice(m, dst, p, ti.size, m.ti(types.U8), false)
	}
	intrinsics["std.mem.SliceAsBytes"] = func(m *Machine, f *frame, t *mir.Terminator, fn types.FnRef) {
		ti := m.targ(f, fn, 0)
		dst := m.dest(f, t)
		p, n := m.sliceWords(m.operandLoc(f, m.argOp(f, t, 0)))
		b, ok := mulBytes(n, ti.size)
		if !ok {
			refuse(types.RefuseRawAccess, "")
		}
		storeSlice(m, dst, p, b, m.ti(types.U8), false)
	}
	intrinsics["std.mem.OffsetOf"] = func(m *Machine, f *frame, t *mir.Terminator, fn types.FnRef) {
		ti := m.targ(f, fn, 0)
		p, n := m.sliceWords(m.operandLoc(f, m.argOp(f, t, 0)))
		name := string(m.readBytes(p, n, m.takeVal()))
		// A record's fields are its type's; a struct's are its
		// declaration's, in the layout's order.
		var names []string
		switch ti.ty.Kind {
		case types.KRecord:
			for _, fd := range ti.ty.Fields {
				names = append(names, m.cfg.Interner.Str(fd.Name))
			}
		case types.KNamed:
			if sd, ok := m.items().Get(ti.ty.Item).Decl.(*types.StructDecl); ok {
				for _, fd := range sd.Fields {
					names = append(names, m.cfg.Interner.Str(fd.Name))
				}
			}
		}
		for i, w := range names {
			if w == name && ti.layout != nil && i < len(ti.layout.Fields) {
				m.store(m.dest(f, t), ti.layout.Fields[i].Offset)
				return
			}
		}
		panicWith(types.PanicExplicit, "mem.OffsetOf: "+m.cfg.Types.TypeString(ti.id, nil)+" has no field `"+name+"`")
	}
}
