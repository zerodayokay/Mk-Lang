// Why: std declares some functions without bodies (raw memory access,
// atomics, SIMD, type facts) because only the compiler can implement them;
// codegen is where each becomes IR, and std and codegen must agree on every
// signature.
//
// What: intrinsicKeys, intrinsicSigs and runtimeSigs (the contract with
// std), Intrinsics (the table the driver checks), supportCall and
// intrinsicCall (the inline lowerings).
//
// How: a call of an intrinsic is inlined at the call site by a switch on its
// key; the SIMD, MMIO, target and crypto families dispatch to their own
// files. TestIntrinsicTableMatchesStd compares the signature tables with
// std's declarations, and an unknown intrinsic key is an ICE.

package codegen

import (
	"sort"
	"strings"

	"mk/internal/llvm"
	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

// intrinsicKeys are the keys codegen implements, sorted. raw.Cast is
// std's raw.PtrCast (Cast is a universe name); the panic hook keys are not
// intrinsics. The support-pack bodies (CallMain, ErrorName, ProfileTable,
// FuncTable) are called by symbol.
var intrinsicKeys = []string{
	"std.core.Io:Scope", "std.core.Io:ScopeOf", "std.core.Io:ScopeWith", "std.core.Scope:Cancel",
	"std.core.Scope:CancelAfter", "std.core.Scope:Expired", "std.core.Scope:Remaining", "std.core.Slice:Len",
	"std.core.str:AsBytes", "std.core.str:Len", "std.dyn.erased.Key",
	"std.raw.Addr", "std.raw.Atomic:Add", "std.raw.Atomic:AddOrd", "std.raw.Atomic:AndOrd", "std.raw.Atomic:Cas",
	"std.raw.Atomic:CasOrd", "std.raw.Atomic:Load", "std.raw.Atomic:LoadOrd", "std.raw.Atomic:New",
	"std.raw.Atomic:OrOrd", "std.raw.Atomic:Store", "std.raw.Atomic:StoreOrd", "std.raw.Atomic:SubOrd",
	"std.raw.Atomic:Swap", "std.raw.Atomic:SwapOrd", "std.raw.Atomic:XorOrd", "std.raw.Clz64", "std.raw.Compare", "std.raw.Copy",
	"std.raw.Ctz64", "std.raw.Data", "std.raw.DataMut", "std.raw.F32Bits", "std.raw.F32From", "std.raw.F64Bits",
	"std.raw.F64From", "std.raw.Fail", "std.raw.Fence", "std.raw.FenceOrd", "std.raw.Fill", "std.raw.Forget", "std.raw.FromAddr",
	"std.raw.HasFree", "std.raw.IsLinear", "std.raw.IsNull", "std.raw.Move", "std.raw.Mul128", "std.raw.Null", "std.raw.Of",
	"std.raw.OfMut", "std.raw.Offset", "std.raw.Popcount64", "std.raw.PtrCast", "std.raw.Read",
	"std.raw.ReadUnaligned", "std.raw.Release", "std.raw.RotL64", "std.raw.Same", "std.raw.Select", "std.raw.Slice", "std.raw.SliceMut",
	"std.raw.SqrtF64", "std.raw.Str", "std.raw.StrData", "std.raw.Write",
	"std.rt.CallMain", "std.rt.ErrorName", "std.rt.FramePointer", "std.rt.FramePointers", "std.rt.FuncTable", "std.rt.ProfileTable", "std.rt.StackPointer", "std.rt.UsesTasks",
	"std.sys.Clone", "std.sys.LibcBuild", "std.sys.Restorer", "std.sys.Syscall", "std.sys.Teb", "std.sys.TrapEntry",
	"std.task.InitContext", "std.task.Switch",
}

// intrinsicSigs are the std declarations the lowerings below assume, one
// per key of intrinsicKeys, as normalized text: generic parameter lists
// (the owning type's, then the function's), parameter types without names
// (receivers as self, &self, mut self), then ": result" unless void; module
// qualifiers dropped. TestIntrinsicTableMatchesStd compares each with std's
// declaration in both directions, so a std signature
// change fails there, not in clang or at run time.
var intrinsicSigs = map[string]string{
	"std.core.Io:Scope": "(self): Scope", "std.core.Io:ScopeOf": "[E](self): Scope[E]",
	"std.core.Io:ScopeWith": "(self, Limits): Scope", "std.core.Scope:Cancel": "(&self)",
	"std.core.Scope:CancelAfter": "(&self, Duration)", "std.core.Scope:Expired": "(&self): bool",
	"std.core.Scope:Remaining": "(&self): ?Duration", "std.core.Slice:Len": "[T](&self): int",
	"std.core.str:AsBytes": "(self): []u8", "std.core.str:Len": "(self): int",
	"std.dyn.erased.Key": "[T](): u64",
	"std.raw.Addr":       "[T](*T): uint", "std.raw.Atomic:Add": "[T](&self, T): T", "std.raw.Atomic:Cas": "[T](&self, T, T): bool",
	"std.raw.Atomic:Load": "[T](&self): T", "std.raw.Atomic:New": "[T](T): Atomic[T]", "std.raw.Atomic:Store": "[T](&self, T)",
	"std.raw.Atomic:Swap": "[T](&self, T): T", "std.raw.Clz64": "(u64): int",
	"std.raw.Atomic:LoadOrd": "[T](&self, u8): T", "std.raw.Atomic:StoreOrd": "[T](&self, T, u8)",
	"std.raw.Atomic:SwapOrd": "[T](&self, T, u8): T", "std.raw.Atomic:AddOrd": "[T](&self, T, u8): T",
	"std.raw.Atomic:SubOrd": "[T](&self, T, u8): T", "std.raw.Atomic:AndOrd": "[T](&self, T, u8): T",
	"std.raw.Atomic:OrOrd": "[T](&self, T, u8): T", "std.raw.Atomic:XorOrd": "[T](&self, T, u8): T",
	"std.raw.Atomic:CasOrd": "[T](&self, T, T, u8, u8, bool): (ok: bool, value: T)", "std.raw.FenceOrd": "(u8)", "std.raw.Compare": "(*u8, *u8, int): int",
	"std.raw.Copy": "[T](*T, *T, int)", "std.raw.Ctz64": "(u64): int", "std.raw.Data": "[T]([]T): *T",
	"std.raw.DataMut": "[T](mut []T): *T", "std.raw.F32Bits": "(f32): u32", "std.raw.F32From": "(u32): f32",
	"std.raw.F64Bits": "(f64): u64", "std.raw.F64From": "(u64): f64", "std.raw.Fail": "(u32, str): never",
	"std.raw.Fence": "()", "std.raw.Fill": "(*u8, u8, int)", "std.raw.Forget": "[T](T)", "std.raw.FromAddr": "[T](uint): *T",
	"std.raw.HasFree": "[T](): bool", "std.raw.Release": "[T, A](T, &A)",
	"std.raw.IsLinear": "[T](): bool", "std.raw.IsNull": "[T](*T): bool", "std.raw.Move": "[T](*T, *T, int)",
	"std.raw.Mul128": "(u64, u64): (hi: u64, lo: u64)", "std.raw.Null": "[T](): *T", "std.raw.Of": "[T](&T): *T",
	"std.raw.OfMut": "[T](mut T): *T", "std.raw.Offset": "[T](*T, int): *T", "std.raw.Popcount64": "(u64): int",
	"std.raw.PtrCast": "[T, U](*T): *U", "std.raw.Read": "[T](*T): T", "std.raw.ReadUnaligned": "[T: Plain](*T): T",
	"std.raw.RotL64": "(u64, int): u64", "std.raw.Same": "[T](*T, *T): bool", "std.raw.Select": "[T: Plain](bool, T, T): T", "std.raw.Slice": "[T](*T, int): []T",
	"std.raw.SliceMut": "[T](*T, int): mut []T", "std.raw.SqrtF64": "(f64): f64", "std.raw.Str": "(*u8, int): str",
	"std.raw.StrData": "(str): *u8", "std.raw.Write": "[T](*T, T)",
	"std.rt.CallMain": "(&System): MainResult", "std.rt.ErrorName": "(u32): str", "std.rt.FuncTable": "(): []FuncRow",
	"std.rt.ProfileTable": "(): []ProfileRecord", "std.rt.StackPointer": "(): uint", "std.rt.FramePointer": "(): uint", "std.rt.FramePointers": "(): bool", "std.rt.UsesTasks": "(): bool",
	"std.sys.Clone": "(uint, *u8, *u32, *u32, uint, fn(*u8), *u8): int", "std.sys.LibcBuild": "(): bool", "std.sys.Restorer": "(): *u8",
	"std.sys.Syscall": "(uint, uint, uint, uint, uint, uint, uint): int", "std.sys.Teb": "(): *u8", "std.sys.TrapEntry": "(): *u8",
	"std.task.InitContext": "(*Context, *u8, fn(*u8), *u8)", "std.task.Switch": "(*Context, *Context)",
}

// runtimeSigs are the std functions with bodies that codegen calls by key
// (the hook table), in intrinsicSigs' form;
// std.free's panic hooks are x86_64-freestanding's.
// std.math.Rem32 and Rem64 (float %) are not in std yet;
// TestIntrinsicTableMatchesStd checks them once std declares them.
var runtimeSigs = map[string]string{
	"std.rt.PanicAt": "(u32, &Site): never", "std.rt.PanicMsg": "(u32, &Site, str): never",
	"std.rt.PanicErr": "(u32, &Site, u32): never", "std.rt.CoverCmp": "(u64, u64, u8)", "std.task.Start": "(*u8): never",
	"std.free.PanicAt": "(u32, &Site): never", "std.free.PanicMsg": "(u32, &Site, str): never",
	"std.free.PanicErr":   "(u32, &Site, u32): never",
	"std.task.Enter":      "(*u8, Io, int, StackSize, int, &Site)",
	"std.task.SpawnAlloc": "(&Scope, int, int, &Site): *u8", "std.task.Spawn": "(&Scope, fn(*Io, *u8, *u8): bool, *u8)",
	"std.task.Join": "(&Scope, bool)", "std.task.Cancel": "(&Scope)",
	"std.task.CancelAfter": "(&Scope, Duration)", "std.task.Expired": "(&Scope): bool",
	"std.task.Remaining":   "(&Scope): ?Duration",
	"std.task.SystemStack": "(): *SysStack", "std.task.SystemStackDone": "()",
	"std.math.Rem64": "(f64, f64): f64", "std.math.Rem32": "(f32, f32): f32",
}

// supportBodies are the intrinsics whose bodies the support pack defines;
// a call is a call of mk.<key>.
var supportBodies = map[string]bool{"std.rt.CallMain": true, "std.rt.ErrorName": true,
	"std.rt.FuncTable": true, "std.rt.ProfileTable": true}

// Intrinsics is the table of std intrinsic keys codegen implements;
// the driver checks every bodiless std function against it.
func Intrinsics() []types.Key {
	out := make([]types.Key, len(intrinsicKeys))
	for i, k := range intrinsicKeys {
		out[i] = types.KeyFromString(k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Compare(out[j]) < 0 })
	return out
}

// intrinsicBodies emits the support-pack bodies of the std.rt intrinsics
// for the items the build has (support_rt.go).
func (s *support) intrinsicBodies(u *Unit, env Env) { s.rtBodies(u, env) }

// supportCall calls a support-pack intrinsic body (CallMain, ErrorName,
// FuncTable, ProfileTable) by its symbol.
func (fg *fnGen) supportCall(fi int, t *mir.Terminator, args []value) {
	g := fg.g
	fn := g.funcDecl(fi)
	fg.finishCall(t, fn.Ref(), fn.Type(), g.funcSigOf(fi), args, fg.b.PlaceType(t.Dest), fg.coldCallAttrs(callAttrs(fn)))
}

// intrinsicCall inlines a call of the std intrinsic item, stores its
// result into Dest and continues at Target.
func (fg *fnGen) intrinsicCall(item types.ItemID, t *mir.Terminator, args []value) {
	g, m := fg.g, fg.g.m
	key := g.items.Get(item).Key.String()
	if strings.HasPrefix(key, "std.simd.") {
		fg.simdIntrinsic(key, t, args) // intrinsic_simd.go
		return
	}
	if strings.HasPrefix(key, "std.mmio.") {
		fg.mmioIntrinsic(key, t, args) // volatile MMIO access
		return
	}
	if fg.targetCall(key, t, args) { // the crypto intrinsics
		return
	}
	destT := fg.b.PlaceType(t.Dest)
	i64, ptr := m.I(64), m.Ptr()
	ret := func(v llvm.Value) {
		if g.reprOf(destT).kind != rZero {
			d, _ := fg.placeAddr(t.Dest)
			fg.store(d, value{t: destT, kind: rScalar, v: v})
		}
		fg.cur.Br(fg.blocks[t.Target])
	}
	retAgg := func(ptrV, n llvm.Value) {
		d, _ := fg.placeAddr(t.Dest)
		fg.cur.Store(ptrV, d.v, alignAt(d.align, 0))
		fg.cur.Store(n, fg.gep(d.v, 8), alignAt(d.align, 8))
		fg.cur.Br(fg.blocks[t.Target])
	}
	pointee := func(v value) types.TypeID { return g.tt.Get(v.t).Elem }
	sliceWord := func(v value, off uint64) llvm.Value {
		a := fg.addrOf(v)
		if off == 0 {
			return fg.cur.Load(ptr, a.v, alignAt(a.align, 0), m.Metadata().NonNull(), m.Metadata().NoUndef())
		}
		return fg.cur.Load(i64, fg.gep(a.v, off), alignAt(a.align, off), m.Metadata().NoUndef())
	}
	call := func(name string, retT llvm.Type, params []llvm.Type, vals []llvm.Value) llvm.Value {
		// DeclareAs: a Mk extern may declare the same C name.
		f := m.DeclareAs(name, llvm.FuncSig{Linkage: llvm.External, Ret: retT, Params: params, FnAttrs: llvm.Attrs{}.Flag("nounwind")})
		return fg.cur.CallFunc(f, vals)
	}
	// The positional lowerings pass fixed LLVM types: an
	// argument or a result of another width or kind (a std declaration
	// that changed) is an ICE naming the key, never invalid IR or a
	// wrong-width store.
	wantArgs := func(params ...llvm.Type) {
		if len(args) != len(params) {
			source.ICEf(fg.spanOf(t.Span), "codegen: intrinsic %s takes %d arguments, its lowering %d (intrinsicSigs: %s)", key, len(args), len(params), intrinsicSigs[key])
		}
		for i, p := range params {
			if args[i].kind != rScalar || args[i].v.Ty != p {
				source.ICEf(fg.spanOf(t.Span), "codegen: intrinsic %s argument %d is %s, its lowering passes %s (intrinsicSigs: %s)",
					key, i, g.tt.TypeString(args[i].t, nil), p, intrinsicSigs[key])
			}
		}
	}
	wantRet := func(r llvm.Type) {
		dr := g.reprOf(destT)
		if r.IsVoid() && dr.kind == rZero || !r.IsVoid() && dr.kind == rScalar && dr.ssa == r {
			return
		}
		source.ICEf(fg.spanOf(t.Span), "codegen: intrinsic %s returns %s, its lowering %s (intrinsicSigs: %s)", key, g.tt.TypeString(destT, nil), r, intrinsicSigs[key])
	}
	switch key {
	case "std.raw.Fill":
		wantArgs(ptr, m.I(8), i64)
		wantRet(m.Void())
	case "std.raw.Compare":
		wantArgs(ptr, ptr, i64)
		wantRet(i64)
	case "std.raw.Ctz64", "std.raw.Clz64", "std.raw.Popcount64":
		wantArgs(i64)
		wantRet(i64)
	case "std.raw.RotL64":
		wantArgs(i64, i64)
		wantRet(i64)
	case "std.raw.Mul128":
		wantArgs(i64, i64)
	case "std.raw.F64Bits":
		wantArgs(m.Double())
		wantRet(i64)
	case "std.raw.F64From":
		wantArgs(i64)
		wantRet(m.Double())
	case "std.raw.F32Bits":
		wantArgs(m.Float())
		wantRet(m.I(32))
	case "std.raw.F32From":
		wantArgs(m.I(32))
		wantRet(m.Float())
	case "std.raw.SqrtF64":
		wantArgs(m.Double())
		wantRet(m.Double())
	case "std.sys.Syscall":
		wantArgs(i64, i64, i64, i64, i64, i64, i64)
		wantRet(i64)
	case "std.sys.Clone":
		wantArgs(i64, ptr, ptr, ptr, i64, ptr, ptr)
		wantRet(i64)
	case "std.sys.Restorer", "std.sys.Teb", "std.sys.TrapEntry":
		wantArgs()
		wantRet(ptr)
	case "std.rt.StackPointer", "std.rt.FramePointer":
		wantArgs()
		wantRet(i64)
	case "std.task.Switch":
		wantArgs(ptr, ptr)
		wantRet(m.Void())
	case "std.task.InitContext":
		wantArgs(ptr, ptr, ptr, ptr)
		wantRet(m.Void())
	}
	switch key {
	case "std.raw.Of", "std.raw.OfMut", "std.raw.PtrCast", "std.raw.FromAddr":
		if key == "std.raw.FromAddr" {
			ret(fg.cur.Cast(llvm.IntToPtr, args[0].v, ptr))
			return
		}
		ret(args[0].v)
	case "std.raw.Addr":
		ret(fg.cur.Cast(llvm.PtrToInt, args[0].v, i64))
	case "std.raw.Null":
		ret(m.Null())
	case "std.raw.IsNull":
		ret(fg.cur.ICmp(llvm.IEQ, args[0].v, m.Null()))
	case "std.raw.Same":
		ret(fg.cur.ICmp(llvm.IEQ, args[0].v, args[1].v))
	case "std.raw.Offset":
		es, _ := g.sizeAlign(pointee(args[0]))
		off := fg.cur.Bin(llvm.Mul, args[1].v, m.ConstInt(i64, es), 0)
		ret(fg.cur.GEPBytes(args[0].v, off, true))
	case "std.raw.Read", "std.raw.ReadUnaligned":
		a := addr{v: args[0].v, align: g.reprOf(destT).align}
		if key == "std.raw.ReadUnaligned" {
			a.align = 1
		}
		d, _ := fg.placeAddr(t.Dest)
		fg.store(d, fg.loadAtRaw(a, destT))
		fg.cur.Br(fg.blocks[t.Target])
	case "std.raw.Write":
		_, al := g.sizeAlign(args[1].t)
		fg.store(addr{v: args[0].v, align: al}, args[1])
		fg.cur.Br(fg.blocks[t.Target])
	case "std.raw.Copy", "std.raw.Move":
		es, _ := g.sizeAlign(pointee(args[0]))
		n := fg.cur.Bin(llvm.Mul, args[2].v, m.ConstInt(i64, es), 0)
		name := "memcpy"
		if key == "std.raw.Move" {
			name = "memmove"
		}
		fg.cur.CallFunc(m.IntrinsicFn(name, ptr, ptr, i64), []llvm.Value{args[0].v, args[1].v, n, m.ConstInt(m.I(1), 0)})
		fg.cur.Br(fg.blocks[t.Target])
	case "std.raw.Fill":
		fg.cur.CallFunc(m.IntrinsicFn("memset", ptr, i64), []llvm.Value{args[0].v, args[1].v, args[2].v, m.ConstInt(m.I(1), 0)})
		fg.cur.Br(fg.blocks[t.Target])
	case "std.raw.Compare":
		c := call(symMemcmp, m.I(32), []llvm.Type{ptr, ptr, i64}, []llvm.Value{args[0].v, args[1].v, args[2].v})
		ret(fg.cur.Cast(llvm.SExt, c, i64))
	case "std.raw.Slice", "std.raw.SliceMut", "std.raw.Str":
		retAgg(args[0].v, args[1].v)
	case "std.raw.Data", "std.raw.DataMut", "std.raw.StrData":
		ret(sliceWord(args[0], 0))
	case "std.core.str:Len":
		ret(sliceWord(args[0], 8))
	case "std.core.str:AsBytes":
		retAgg(sliceWord(args[0], 0), sliceWord(args[0], 8))
	case "std.core.Slice:Len":
		// &self over a slice-like or an array (the adapted Slice).
		self := pointee(args[0])
		if ty := g.tt.Get(self); ty.Kind == types.KArray {
			ret(m.ConstInt(i64, g.constLen(ty.Len)))
			return
		}
		ret(fg.cur.Load(i64, fg.cur.GEPBytes(args[0].v, m.ConstInt(i64, 8), true), 8, m.Metadata().NoUndef()))
	case "std.raw.Forget":
		fg.cur.Br(fg.blocks[t.Target])
	case "std.rt.FramePointers":
		// The build's --frame-pointers
		// setting, a constant of the pack (PackFacts holds it).
		v := uint64(0)
		if g.cfg.FramePointers {
			v = 1
		}
		ret(m.ConstInt(m.I(1), v))
	case "std.rt.FramePointer":
		// rbp (x29 on aarch64): the frame chain's head under
		// --frame-pointers.
		asm := "movq %rbp, $0"
		if g.cfg.Target.Arch == types.ArchArm64 {
			asm = "mov $0, x29"
		}
		ret(fg.cur.InlineAsm(m.FuncType(i64, nil, false), asm, "=r", true, nil))
	case "std.rt.UsesTasks":
		// mono folds every call (internal/mono/tasks.go); a call it
		// did not see answers true, which is always correct.
		ret(m.ConstInt(m.I(1), 1))
	case "std.sys.LibcBuild":
		// mono folds every call to the build's libc mode
		// (internal/mono libcfold.go); codegen does not know the mode.
		source.ICEf(fg.spanOf(t.Span), "codegen: %s reached codegen: mono folds every call to the build's system-libc mode", key)
	case "std.raw.Release", "std.raw.HasFree":
		// mono rewrites every call per instance
		// (internal/mono release.go), a non-generic caller included.
		source.ICEf(fg.spanOf(t.Span), "codegen: %s reached codegen: mono resolves std.raw.Release and folds std.raw.HasFree", key)
	case "std.raw.IsLinear":
		ga := fg.b.GenericArgs[t.Callee.Fn.Args.Start]
		ret(m.ConstInt(m.I(1), b2u(g.env.Facts(ga.Type).Kind == types.Linear)))
	case typeKeyIntrinsic: // a type's run-time key (typekey.go)
		ga := fg.b.GenericArgs[t.Callee.Fn.Args.Start]
		ret(m.ConstInt(i64, g.u.typeKeysOf(g.tt).key(g.tt, ga.Type)))
	case "std.raw.Fail":
		fg.panicCallV(args[0].v, &args[1])
	case "std.raw.Fence":
		fg.cur.Fence(llvm.SeqCst)
		fg.cur.Br(fg.blocks[t.Target])
	case "std.raw.FenceOrd":
		fg.cur.Fence(fg.atomicOrder(t, key, 0, ordFence))
		fg.cur.Br(fg.blocks[t.Target])
	case "std.raw.Ctz64":
		ret(fg.cur.CallFunc(m.IntrinsicFn("cttz", i64), []llvm.Value{args[0].v, m.ConstInt(m.I(1), 0)}))
	case "std.raw.Clz64":
		ret(fg.cur.CallFunc(m.IntrinsicFn("ctlz", i64), []llvm.Value{args[0].v, m.ConstInt(m.I(1), 0)}))
	case "std.raw.Popcount64":
		ret(fg.cur.CallFunc(m.IntrinsicFn("ctpop", i64), []llvm.Value{args[0].v}))
	case "std.raw.RotL64":
		ret(fg.cur.CallFunc(m.IntrinsicFn("fshl", i64), []llvm.Value{args[0].v, args[0].v, args[1].v}))
	case "std.raw.Mul128":
		i128 := m.I(128)
		p := fg.cur.Bin(llvm.Mul, fg.cur.Cast(llvm.ZExt, args[0].v, i128), fg.cur.Cast(llvm.ZExt, args[1].v, i128), 0)
		hi := fg.cur.Cast(llvm.Trunc, fg.cur.Bin(llvm.LShr, p, m.ConstInt(i128, 64), 0), i64)
		lo := fg.cur.Cast(llvm.Trunc, p, i64)
		d, _ := fg.placeAddr(t.Dest)
		fs := g.layout(destT).Fields // (hi, lo), records in name order
		fg.cur.Store(hi, fg.gep(d.v, fs[0].Offset), alignAt(d.align, fs[0].Offset))
		fg.cur.Store(lo, fg.gep(d.v, fs[1].Offset), alignAt(d.align, fs[1].Offset))
		fg.cur.Br(fg.blocks[t.Target])
	case "std.raw.F64Bits", "std.raw.F64From", "std.raw.F32Bits", "std.raw.F32From":
		ret(fg.cur.Cast(llvm.BitCast, args[0].v, g.reprOf(destT).ssa))
	case "std.raw.SqrtF64":
		ret(fg.cur.CallFunc(m.IntrinsicFn("sqrt", m.Double()), []llvm.Value{args[0].v}))
	case "std.raw.Select":
		fg.selectUnpredictable(t, args, destT)
	case "std.raw.Atomic:New":
		d, _ := fg.placeAddr(t.Dest)
		fg.store(d, args[0])
		fg.cur.Br(fg.blocks[t.Target])
	case "std.raw.Atomic:Load":
		r := g.reprOf(destT)
		ret(fg.cur.AtomicLoad(r.ssa, args[0].v, llvm.SeqCst, uint32(r.size)))
	case "std.raw.Atomic:Store":
		fg.cur.AtomicStore(args[1].v, args[0].v, llvm.SeqCst, uint32(g.reprOf(args[1].t).size))
		fg.cur.Br(fg.blocks[t.Target])
	case "std.raw.Atomic:Add", "std.raw.Atomic:Swap":
		op := llvm.RMWAdd
		if key == "std.raw.Atomic:Swap" {
			op = llvm.RMWXchg
		}
		ret(fg.cur.AtomicRMW(op, args[0].v, args[1].v, llvm.SeqCst, uint32(g.reprOf(args[1].t).size)))
	case "std.raw.Atomic:Cas":
		r := fg.cur.CmpXchg(args[0].v, args[1].v, args[2].v, llvm.SeqCst, llvm.SeqCst, uint32(g.reprOf(args[1].t).size))
		ret(fg.cur.ExtractValue(r, 1))
	case "std.raw.Atomic:LoadOrd":
		r := g.reprOf(destT)
		ret(fg.cur.AtomicLoad(r.ssa, args[0].v, fg.atomicOrder(t, key, 1, ordLoad), uint32(r.size)))
	case "std.raw.Atomic:StoreOrd":
		fg.cur.AtomicStore(args[1].v, args[0].v, fg.atomicOrder(t, key, 2, ordStore), uint32(g.reprOf(args[1].t).size))
		fg.cur.Br(fg.blocks[t.Target])
	case "std.raw.Atomic:SwapOrd", "std.raw.Atomic:AddOrd", "std.raw.Atomic:SubOrd", "std.raw.Atomic:AndOrd",
		"std.raw.Atomic:OrOrd", "std.raw.Atomic:XorOrd":
		op := rmwOps[key]
		ret(fg.cur.AtomicRMW(op, args[0].v, args[1].v, fg.atomicOrder(t, key, 2, ordRMW), uint32(g.reprOf(args[1].t).size)))
	case "std.raw.Atomic:CasOrd":
		// cmpxchg's weak flag is not in internal/llvm's builder, so the
		// weak form is the strong one: a weak CAS may fail spuriously, and
		// one that never does is a valid implementation (C++20
		// [atomics.types.operations] p23). x86-64 has no weak CAS anyway.
		_ = fg.constBool(t, key, 5)
		r := fg.cur.CmpXchg(args[0].v, args[1].v, args[2].v, fg.atomicOrder(t, key, 3, ordRMW),
			fg.atomicOrder(t, key, 4, ordCasFail), uint32(g.reprOf(args[1].t).size))
		d, _ := fg.placeAddr(t.Dest)
		fs := g.layout(destT).Fields // (ok, value), records in name order
		okT, valT := g.tt.Get(destT).Fields[0].Type, g.tt.Get(destT).Fields[1].Type
		fg.store(addr{v: fg.gep(d.v, fs[0].Offset), align: alignAt(d.align, fs[0].Offset)}, value{t: okT, kind: rScalar, v: fg.cur.ExtractValue(r, 1)})
		fg.store(addr{v: fg.gep(d.v, fs[1].Offset), align: alignAt(d.align, fs[1].Offset)}, value{t: valT, kind: rScalar, v: fg.cur.ExtractValue(r, 0)})
		fg.cur.Br(fg.blocks[t.Target])
	case "std.sys.Syscall":
		vals := make([]llvm.Value, 7)
		for i := range vals {
			vals[i] = args[i].v
		}
		ps := []llvm.Type{i64, i64, i64, i64, i64, i64, i64}
		cons := "={rax},{rax},{rdi},{rsi},{rdx},{r10},{r8},{r9},~{rcx},~{r11},~{memory}"
		asm := "syscall"
		if g.cfg.Target.Arch == types.ArchArm64 {
			cons, asm = "={x0},{x8},{x0},{x1},{x2},{x3},{x4},{x5},~{memory}", "svc #0"
		}
		ret(fg.cur.InlineAsm(m.FuncType(i64, ps, false), asm, cons, true, vals))
	case "std.sys.Clone":
		vals := make([]llvm.Value, 7)
		for i := range vals {
			vals[i] = args[i].v
		}
		ret(call(symClone, i64, []llvm.Type{i64, ptr, ptr, ptr, i64, ptr, ptr}, vals))
	case "std.sys.Restorer":
		f := m.DeclareAs(symSigreturn, llvm.FuncSig{Linkage: llvm.External, Ret: m.Void()})
		ret(m.FuncRef(f))
	case "std.sys.Teb":
		ret(fg.cur.InlineAsm(m.FuncType(ptr, nil, false), "movq %gs:0x30, $0", "=r", false, nil))
	case "std.sys.TrapEntry":
		// trapform.go: mk_trap_call when the unit traps a call (the
		// support pack defines it exactly then), else null.
		if g.unitTraps() {
			ret(m.FuncRef(m.DeclareAs(symTrapCall, llvm.FuncSig{Linkage: llvm.External, Ret: m.Void()})))
		} else {
			ret(m.Null())
		}
	case "std.rt.StackPointer":
		asm := "movq %rsp, $0"
		if g.cfg.Target.Arch == types.ArchArm64 {
			asm = "mov $0, sp"
		}
		ret(fg.cur.InlineAsm(m.FuncType(i64, nil, false), asm, "=r", true, nil))
	case "std.task.Switch":
		call(symSwitch, m.Void(), []llvm.Type{ptr, ptr}, []llvm.Value{args[0].v, args[1].v})
		fg.cur.Br(fg.blocks[t.Target])
	case "std.core.Io:Scope", "std.core.Io:ScopeOf", "std.core.Io:ScopeWith", "std.core.Scope:Cancel":
		fg.scopeIntrinsic(key, t, args)
	case "std.core.Scope:CancelAfter", "std.core.Scope:Expired", "std.core.Scope:Remaining":
		fg.deadlineIntrinsic(key, t, args)
	case "std.task.InitContext":
		call(symInitContext, m.Void(), []llvm.Type{ptr, ptr, ptr, ptr}, []llvm.Value{args[0].v, args[1].v, args[2].v, args[3].v})
		fg.cur.Br(fg.blocks[t.Target])
	default:
		source.ICEf(fg.spanOf(t.Span), "codegen: unknown intrinsic %s", key)
	}
}

func b2u(b bool) uint64 {
	if b {
		return 1
	}
	return 0
}

// loadAtRaw reads a value of type t through a raw pointer: no pointee
// facts beyond the scalar's own.
func (fg *fnGen) loadAtRaw(a addr, t types.TypeID) value {
	return fg.loadAt(a, t)
}
