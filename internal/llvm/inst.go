// Why: codegen emits many instructions per function; building them must be
// cheap, and a misuse must fail in the builder with a clear message, not in
// clang.
//
// What: Block and the instruction builders codegen uses: memory (GEP8,
// loads, stores), arithmetic (Bin, without fast-math flags and with no
// frem), compares, casts (Cast, castOK), aggregates, calls (Call, CallFunc,
// MustTailCall), atomics (AtomicRMW, CmpXchg, AtomicLoad, AtomicStore,
// Fence) and control flow (Br, Switch, Ret). No lifetime markers.
//
// How: each instruction is appended as text to its block's one growing
// []byte, with operands written by strconv.Append*, so emitting an
// instruction allocates nothing in steady state. Every check runs before
// the first byte of an instruction is written, so a panic leaves the block
// as it was.

package llvm

import (
	"strconv"
)

// Block is a basic block. Instructions are appended as text; the block is
// closed by exactly one terminator, after which appending panics.
type Block struct {
	f     *Function
	label string
	ref   string // "%" + Quote(label), computed once
	text  []byte // the instructions, each "  ...\n"
	term  bool
	loc   MD
}

// Label is the block's label, unquoted.
func (b *Block) Label() string { return b.label }

// Terminated reports whether the block ends in a terminator.
func (b *Block) Terminated() bool { return b.term }

// SetDebugLoc sets the !dbg location attached to every later instruction of
// this block that has no explicit Dbg attachment; the zero MD clears
// it.
func (b *Block) SetDebugLoc(md MD) { b.f.m.meta.check(md, "SetDebugLoc"); b.loc = md }

// begin checks that an instruction may be appended with attachments md and
// starts its line.
func (b *Block) begin(md []Attach) {
	if b.term {
		panic("llvm: instruction after the terminator of " + b.ref + " in " + b.f.ref)
	}
	for _, a := range md {
		b.f.m.meta.check(a.MD, "attachment")
	}
	b.text = append(b.text, "  "...)
}

// beginValue is begin for an instruction with a result of type t, which it
// returns after writing "%v<n> = ".
func (b *Block) beginValue(t Type, md []Attach) Value {
	b.begin(md)
	v := b.f.newValue(t)
	b.text = append(b.text, "%v"...)
	b.text = strconv.AppendInt(b.text, v.n, 10)
	b.text = append(b.text, " = "...)
	return v
}

// end writes the attachments, the block's location unless md has a !dbg,
// and the newline.
func (b *Block) end(md []Attach) {
	hasDbg := false
	for _, a := range md {
		if a.MD.IsZero() {
			continue
		}
		if a.Kind == "dbg" {
			hasDbg = true
		}
		b.text = append(b.text, ", !"...)
		b.text = append(b.text, a.Kind...)
		b.text = append(b.text, ' ')
		b.text = appendMDRef(b.text, a.MD)
	}
	if !hasDbg && !b.loc.IsZero() {
		b.text = append(b.text, ", !dbg "...)
		b.text = appendMDRef(b.text, b.loc)
	}
	b.text = append(b.text, '\n')
}

// emit appends a void instruction given as text (the rare kinds).
func (b *Block) emit(text string, md []Attach) {
	b.begin(md)
	b.text = append(b.text, text...)
	b.end(md)
}

func (b *Block) typed(v Value)  { b.text = appendTyped(b.text, v) }
func (b *Block) sref(v Value)   { b.text = appendRef(b.text, v) }
func (b *Block) str(s string)   { b.text = append(b.text, s...) }
func (b *Block) align(a uint32) { b.text = appendAlign(b.text, a) }
func (b *Block) typ(t Type)     { b.text = append(b.text, t.String()...) }
func (b *Block) commaTyped(v Value) {
	b.text = append(b.text, ", "...)
	b.text = appendTyped(b.text, v)
}

func (b *Block) val(v Value, what string) { b.f.m.checkValue(v, what) }

func (b *Block) ptr(p Value, what string) {
	b.val(p, what)
	if !p.Ty.IsPtr() {
		panic("llvm: " + what + ": " + p.String() + " is not a pointer")
	}
}

// Alloca reserves a stack slot of type t; its result is a ptr.
func (b *Block) Alloca(t Type, align uint32, md ...Attach) Value {
	b.f.m.checkType(t, "Alloca")
	v := b.beginValue(b.f.m.Ptr(), md)
	b.str("alloca ")
	b.typ(t)
	b.align(align)
	b.end(md)
	return v
}

// Load reads a t from p.
func (b *Block) Load(t Type, p Value, align uint32, md ...Attach) Value {
	b.f.m.checkType(t, "Load")
	b.ptr(p, "Load")
	v := b.beginValue(t, md)
	b.str("load ")
	b.typ(t)
	b.commaTyped(p)
	b.align(align)
	b.end(md)
	return v
}

// Store writes v to p.
func (b *Block) Store(v, p Value, align uint32, md ...Attach) {
	b.val(v, "Store")
	b.ptr(p, "Store")
	b.begin(md)
	b.str("store ")
	b.typed(v)
	b.commaTyped(p)
	b.align(align)
	b.end(md)
}

// GEPBytes is p plus off bytes: getelementptr [inbounds] i8, ptr p, iN off.
func (b *Block) GEPBytes(p Value, off Value, inbounds bool, md ...Attach) Value {
	f := GEPFlags(0)
	if inbounds {
		f = GEPInbounds
	}
	return b.GEP8(p, off, f, md...)
}

// GEPFlags are the getelementptr flags (LLVM 20 and later for nusw and nuw,
// so within the LLVM 21 floor). GEPInbounds implies GEPNUSW, so the
// two are never printed together.
type GEPFlags uint8

// GEPFlags values.
const (
	GEPInbounds GEPFlags = 1 << iota
	GEPNUSW
	GEPNUW
)

// GEP8 is p plus off bytes with flags: getelementptr [inbounds|nusw] [nuw]
// i8, ptr p, iN off. nuw promises that the unsigned addition of p and off
// does not wrap, so off is never negative as a signed number.
func (b *Block) GEP8(p Value, off Value, flags GEPFlags, md ...Attach) Value {
	b.ptr(p, "GEP8")
	b.val(off, "GEP8")
	if !off.Ty.IsInt() {
		panic("llvm: GEP8 offset " + off.String())
	}
	if flags > GEPInbounds|GEPNUSW|GEPNUW {
		panic("llvm: GEP8: unknown flags")
	}
	v := b.beginValue(b.f.m.Ptr(), md)
	b.str("getelementptr")
	switch {
	case flags&GEPInbounds != 0:
		b.str(" inbounds")
	case flags&GEPNUSW != 0:
		b.str(" nusw")
	}
	if flags&GEPNUW != 0 {
		b.str(" nuw")
	}
	b.str(" i8")
	b.commaTyped(p)
	b.commaTyped(off)
	b.end(md)
	return v
}

// BinOp is a binary (or, for FNeg, unary) arithmetic operation.
type BinOp uint8

// BinOp values. There is no frem.
const (
	Add BinOp = iota
	Sub
	Mul
	SDiv
	UDiv
	SRem
	URem
	Shl
	LShr
	AShr
	And
	Or
	Xor
	FAdd
	FSub
	FMul
	FDiv
	FNeg
)

var binNames = [...]string{"add", "sub", "mul", "sdiv", "udiv", "srem", "urem", "shl", "lshr", "ashr",
	"and", "or", "xor", "fadd", "fsub", "fmul", "fdiv", "fneg"}

func (op BinOp) String() string { return binNames[op] }

// Flags is nsw, nuw, exact for Bin; inbounds is a GEPBytes argument.
type Flags uint8

// Flags values.
const (
	NUW Flags = 1 << iota
	NSW
	Exact
)

// Bin is x op y (FNeg ignores y). nuw and nsw are allowed on add, sub, mul
// and shl; exact on sdiv, udiv, lshr and ashr; the float operations take no
// flags (no fast-math).
func (b *Block) Bin(op BinOp, x, y Value, flags Flags, md ...Attach) Value {
	b.val(x, "Bin")
	if op > FNeg {
		panic("llvm: Bin: unknown operation")
	}
	isFloat := op >= FAdd
	elem, _ := lanes(x.Ty)
	if isFloat != elem.IsFloat() || (!isFloat && !elem.IsInt()) {
		panic("llvm: Bin " + op.String() + " on " + x.String())
	}
	switch op {
	case Add, Sub, Mul, Shl:
		if flags&Exact != 0 {
			panic("llvm: exact on " + op.String())
		}
	case SDiv, UDiv, LShr, AShr:
		if flags&(NUW|NSW) != 0 {
			panic("llvm: nuw/nsw on " + op.String())
		}
	default:
		if flags != 0 {
			panic("llvm: flags on " + op.String())
		}
	}
	if op != FNeg {
		b.val(y, "Bin")
		if y.Ty != x.Ty {
			panic("llvm: Bin " + op.String() + " of " + x.String() + " and " + y.String())
		}
	}
	v := b.beginValue(x.Ty, md)
	b.str(binNames[op])
	if flags&NUW != 0 {
		b.str(" nuw")
	}
	if flags&NSW != 0 {
		b.str(" nsw")
	}
	if flags&Exact != 0 {
		b.str(" exact")
	}
	b.str(" ")
	b.typed(x)
	if op != FNeg {
		b.str(", ")
		b.sref(y)
	}
	b.end(md)
	return v
}

// IPred is an icmp predicate.
type IPred uint8

// IPred values.
const (
	IEQ IPred = iota
	INE
	IUGT
	IUGE
	IULT
	IULE
	ISGT
	ISGE
	ISLT
	ISLE
)

var ipredNames = [...]string{"eq", "ne", "ugt", "uge", "ult", "ule", "sgt", "sge", "slt", "sle"}

func (p IPred) String() string { return ipredNames[p] }

// FPred is an fcmp predicate.
type FPred uint8

// FPred values.
const (
	FFalse FPred = iota
	FOEQ
	FOGT
	FOGE
	FOLT
	FOLE
	FONE
	FORD
	FUEQ
	FUGT
	FUGE
	FULT
	FULE
	FUNE
	FUNO
	FTrue
)

var fpredNames = [...]string{"false", "oeq", "ogt", "oge", "olt", "ole", "one", "ord",
	"ueq", "ugt", "uge", "ult", "ule", "une", "uno", "true"}

func (p FPred) String() string { return fpredNames[p] }

func (b *Block) cmpType(x Value) Type {
	if x.Ty.IsVector() {
		return b.f.m.Vector(int(x.Ty.Len()), b.f.m.I(1))
	}
	return b.f.m.I(1)
}

// lanes is the element type and lane count of a vector, or t and 0.
func lanes(t Type) (Type, uint64) {
	if t.IsVector() {
		return t.Elem(0), t.Len()
	}
	return t, 0
}

func (b *Block) cmp(kw, pred string, x, y Value, md []Attach) Value {
	v := b.beginValue(b.cmpType(x), md)
	b.str(kw)
	b.str(pred)
	b.str(" ")
	b.typed(x)
	b.str(", ")
	b.sref(y)
	b.end(md)
	return v
}

// ICmp compares integers or pointers, or vectors of them; the result is i1
// (a vector of i1 for vectors).
func (b *Block) ICmp(p IPred, x, y Value, md ...Attach) Value {
	b.val(x, "ICmp")
	b.val(y, "ICmp")
	if p > ISLE {
		panic("llvm: ICmp: unknown predicate")
	}
	if e, _ := lanes(x.Ty); x.Ty != y.Ty || !(e.IsInt() || e.IsPtr()) {
		panic("llvm: icmp of " + x.String() + " and " + y.String())
	}
	return b.cmp("icmp ", ipredNames[p], x, y, md)
}

// FCmp compares floats; the result is i1.
func (b *Block) FCmp(p FPred, x, y Value, md ...Attach) Value {
	b.val(x, "FCmp")
	b.val(y, "FCmp")
	elem, _ := lanes(x.Ty)
	if p > FTrue || x.Ty != y.Ty || !elem.IsFloat() {
		panic("llvm: fcmp of " + x.String() + " and " + y.String())
	}
	return b.cmp("fcmp ", fpredNames[p], x, y, md)
}

// CastOp is a conversion instruction.
type CastOp uint8

// CastOp values.
const (
	Trunc CastOp = iota
	ZExt
	SExt
	FPTrunc
	FPExt
	FPToUI
	FPToSI
	UIToFP
	SIToFP
	PtrToInt
	IntToPtr
	BitCast
)

var castNames = [...]string{"trunc", "zext", "sext", "fptrunc", "fpext", "fptoui", "fptosi",
	"uitofp", "sitofp", "ptrtoint", "inttoptr", "bitcast"}

func (op CastOp) String() string { return castNames[op] }

// castOK is LLVM's CastInst::castIsValid for the types this package
// builds: the integer and float conversions change the width in the stated
// direction, vectors convert lane by lane with equal lane counts, and a
// bitcast keeps the bit size and never mixes a pointer with a non-pointer.
func castOK(op CastOp, from, to Type) bool {
	fe, fn := lanes(from)
	te, tn := lanes(to)
	if op != BitCast && fn != tn {
		return false
	}
	switch op {
	case Trunc:
		return fe.IsInt() && te.IsInt() && fe.Bits() > te.Bits()
	case ZExt, SExt:
		return fe.IsInt() && te.IsInt() && fe.Bits() < te.Bits()
	case FPTrunc:
		return fe.IsFloat() && te.IsFloat() && fe.Bits() > te.Bits()
	case FPExt:
		return fe.IsFloat() && te.IsFloat() && fe.Bits() < te.Bits()
	case FPToUI, FPToSI:
		return fe.IsFloat() && te.IsInt()
	case UIToFP, SIToFP:
		return fe.IsInt() && te.IsFloat()
	case PtrToInt:
		return fe.IsPtr() && te.IsInt()
	case IntToPtr:
		return fe.IsInt() && te.IsPtr()
	case BitCast:
		if fe.IsPtr() || te.IsPtr() {
			return fe.IsPtr() && te.IsPtr() && fn == tn
		}
		if !(fe.IsInt() || fe.IsFloat()) || !(te.IsInt() || te.IsFloat()) {
			return false
		}
		return uint64(fe.Bits())*max(fn, 1) == uint64(te.Bits())*max(tn, 1)
	}
	return false
}

// Cast converts v to type to (no nneg, no trunc nuw: LLVM 18 syntax). An
// operation LLVM rejects for these types panics.
func (b *Block) Cast(op CastOp, v Value, to Type, md ...Attach) Value {
	b.val(v, "Cast")
	b.f.m.checkType(to, "Cast")
	if op > BitCast {
		panic("llvm: Cast: unknown operation")
	}
	if !castOK(op, v.Ty, to) {
		panic("llvm: Cast: " + op.String() + " " + v.String() + " to " + to.String() + " is not a valid cast")
	}
	r := b.beginValue(to, md)
	b.str(castNames[op])
	b.str(" ")
	b.typed(v)
	b.str(" to ")
	b.typ(to)
	b.end(md)
	return r
}

// Select is c ? x : y.
func (b *Block) Select(c, x, y Value, md ...Attach) Value {
	b.val(c, "Select")
	b.val(x, "Select")
	b.val(y, "Select")
	if x.Ty != y.Ty || c.Ty.Bits() != 1 && !c.Ty.IsVector() {
		panic("llvm: select " + c.String() + ", " + x.String() + ", " + y.String())
	}
	v := b.beginValue(x.Ty, md)
	b.str("select ")
	b.typed(c)
	b.commaTyped(x)
	b.commaTyped(y)
	b.end(md)
	return v
}

// indexType is the type at path idx inside t.
func indexType(t Type, idx []int, what string) Type {
	if len(idx) == 0 {
		panic("llvm: " + what + " without an index")
	}
	for _, i := range idx {
		if !t.IsAggregate() || i < 0 || uint64(i) >= t.Len() {
			panic("llvm: " + what + ": index " + strconv.Itoa(i) + " into " + t.String())
		}
		t = t.Elem(i)
	}
	return t
}

func (b *Block) indices(idx []int) {
	for _, i := range idx {
		b.str(", ")
		b.text = strconv.AppendInt(b.text, int64(i), 10)
	}
}

// ExtractValue reads the element at idx of an aggregate value.
func (b *Block) ExtractValue(agg Value, idx ...int) Value {
	b.val(agg, "ExtractValue")
	t := indexType(agg.Ty, idx, "ExtractValue")
	v := b.beginValue(t, nil)
	b.str("extractvalue ")
	b.typed(agg)
	b.indices(idx)
	b.end(nil)
	return v
}

// InsertValue is agg with the element at idx replaced by v.
func (b *Block) InsertValue(agg, v Value, idx ...int) Value {
	b.val(agg, "InsertValue")
	b.val(v, "InsertValue")
	t := indexType(agg.Ty, idx, "InsertValue")
	if t != v.Ty {
		panic("llvm: InsertValue of " + v.String() + " into a " + t.String() + " slot")
	}
	r := b.beginValue(agg.Ty, nil)
	b.str("insertvalue ")
	b.typed(agg)
	b.commaTyped(v)
	b.indices(idx)
	b.end(nil)
	return r
}

// checkCall checks a call's arguments against fnTy.
func (b *Block) checkCall(fnTy Type, args []Value, argAttrs []Attrs, what string) {
	if !fnTy.IsFunc() {
		panic("llvm: " + what + " with non-function type " + fnTy.String())
	}
	n := int(fnTy.Len())
	if len(args) < n || (!fnTy.Variadic() && len(args) != n) {
		panic("llvm: " + what + ": " + strconv.Itoa(len(args)) + " arguments for " + fnTy.String())
	}
	if len(argAttrs) != 0 && len(argAttrs) != len(args) {
		panic("llvm: " + what + ": argAttrs length differs from args")
	}
	for i, a := range args {
		b.val(a, what)
		if i < n && a.Ty != fnTy.Elem(i) {
			panic("llvm: " + what + ": argument " + a.String() + " for parameter " + fnTy.Elem(i).String())
		}
		if len(argAttrs) != 0 {
			b.f.m.checkAttrs(argAttrs[i], what)
		}
	}
}

// args writes (T [attrs] ref, ...) of checked arguments.
func (b *Block) args(args []Value, argAttrs []Attrs) {
	b.str("(")
	for i, a := range args {
		if i > 0 {
			b.str(", ")
		}
		b.typ(a.Ty)
		if len(argAttrs) != 0 {
			b.text = appendSpAttrs(b.text, argAttrs[i])
		}
		b.str(" ")
		b.sref(a)
	}
	b.str(")")
}

// callType is what follows "call": the result type, or the whole function
// type for a variadic callee.
func callType(fnTy Type) string {
	if fnTy.Variadic() {
		return fnTy.String()
	}
	return fnTy.Ret().String()
}

// Call calls callee (a function address or any ptr value) of type fnTy
// under the platform C ABI. argAttrs is empty or one Attrs per argument.
// The result is the zero Value for a void callee.
func (b *Block) Call(fnTy Type, callee Value, args []Value, argAttrs []Attrs, retAttrs Attrs, fnAttrs Attrs, md ...Attach) Value {
	return b.call("call", CCC, fnTy, callee, args, argAttrs, retAttrs, fnAttrs, md)
}

// CallCC is Call with an explicit calling convention cc.
func (b *Block) CallCC(cc CallingConv, fnTy Type, callee Value, args []Value, argAttrs []Attrs, retAttrs Attrs, fnAttrs Attrs, md ...Attach) Value {
	return b.call("call", cc, fnTy, callee, args, argAttrs, retAttrs, fnAttrs, md)
}

// MustTailCall is Call marked musttail (become) under the platform C ABI:
// LLVM guarantees the tail call, and refuses a module where it cannot (the
// caller's and the callee's prototypes or calling conventions differ, or no
// ret of its value follows). The caller emits that ret next, with Ret.
func (b *Block) MustTailCall(fnTy Type, callee Value, args []Value, argAttrs []Attrs, retAttrs Attrs, fnAttrs Attrs, md ...Attach) Value {
	return b.MustTailCallCC(CCC, fnTy, callee, args, argAttrs, retAttrs, fnAttrs, md...)
}

// MustTailCallCC is MustTailCall with an explicit calling convention cc,
// which must match the enclosing function's calling convention.
func (b *Block) MustTailCallCC(cc CallingConv, fnTy Type, callee Value, args []Value, argAttrs []Attrs, retAttrs Attrs, fnAttrs Attrs, md ...Attach) Value {
	if fnTy.Variadic() {
		panic("llvm: musttail of a variadic callee in " + b.f.ref)
	}
	if cc != b.f.sig.CC {
		panic("llvm: musttail calling convention differs from " + b.f.ref)
	}
	return b.call("musttail call", cc, fnTy, callee, args, argAttrs, retAttrs, fnAttrs, md)
}

func (b *Block) call(kw string, cc CallingConv, fnTy Type, callee Value, args []Value, argAttrs []Attrs, retAttrs Attrs, fnAttrs Attrs, md []Attach) Value {
	if cc > PreserveNoneCC {
		panic("llvm: Call: unknown calling convention")
	}
	b.ptr(callee, "Call")
	b.f.m.checkAttrs(retAttrs, "Call")
	b.f.m.checkAttrs(fnAttrs, "Call")
	b.checkCall(fnTy, args, argAttrs, "Call")
	var v Value
	if fnTy.Ret().IsVoid() {
		b.begin(md)
	} else {
		v = b.beginValue(fnTy.Ret(), md)
	}
	b.str(kw)
	if s := cc.String(); s != "" {
		b.str(" ")
		b.str(s)
	}
	b.text = appendSpAttrs(b.text, retAttrs)
	b.str(" ")
	b.str(callType(fnTy))
	b.str(" ")
	b.sref(callee)
	b.args(args, argAttrs)
	b.text = appendSpAttrs(b.text, fnAttrs)
	b.end(md)
	return v
}

// CallFunc calls f directly, repeating its calling convention and its
// parameter, return and function attributes at the call site, as every call
// repeats the callee's attributes. Variadic extra arguments get no
// attributes.
func (b *Block) CallFunc(f *Function, args []Value, md ...Attach) Value {
	if f == nil || f.m != b.f.m {
		panic("llvm: CallFunc of a function of another module")
	}
	var argAttrs []Attrs
	if len(f.sig.ParamAttrs) != 0 && !f.intrinsic { // no immarg at call sites
		argAttrs = f.sig.ParamAttrs
		if len(args) > len(argAttrs) { // variadic extras
			argAttrs = make([]Attrs, len(args))
			copy(argAttrs, f.sig.ParamAttrs)
		}
	}
	return b.CallCC(f.sig.CC, f.ty, f.Ref(), args, argAttrs, f.sig.RetAttrs, f.sig.FnAttrs, md...)
}

// Ordering is an atomic memory ordering.
type Ordering uint8

// Ordering values.
const (
	Unordered Ordering = iota
	Monotonic
	Acquire
	Release
	AcqRel
	SeqCst
)

var orderNames = [...]string{"unordered", "monotonic", "acquire", "release", "acq_rel", "seq_cst"}

func (o Ordering) String() string { return orderNames[o] }

// RMWOp is an atomicrmw operation.
type RMWOp uint8

// RMWOp values.
const (
	RMWXchg RMWOp = iota
	RMWAdd
	RMWSub
	RMWAnd
	RMWNand
	RMWOr
	RMWXor
	RMWMax
	RMWMin
	RMWUMax
	RMWUMin
	RMWFAdd
	RMWFSub
)

var rmwNames = [...]string{"xchg", "add", "sub", "and", "nand", "or", "xor", "max", "min",
	"umax", "umin", "fadd", "fsub"}

func (op RMWOp) String() string { return rmwNames[op] }

func checkOrdering(o Ordering, what string) {
	if o > SeqCst {
		panic("llvm: " + what + ": unknown ordering")
	}
}

// atomicType panics unless t is a type an atomic access may have: an
// integer of a power-of-two number of bytes, a float or double (when
// floatOK), or ptr (when ptrOK).
func atomicType(t Type, floatOK, ptrOK bool, what string) {
	switch {
	case t.IsInt():
		if n := t.Bits(); n >= 8 && n&(n-1) == 0 {
			return
		}
	case t.IsFloat():
		if floatOK {
			return
		}
	case t.IsPtr():
		if ptrOK {
			return
		}
	}
	panic("llvm: " + what + " of type " + t.String())
}

// AtomicRMW atomically applies op v to *p and yields the old value.
// The ordering is not unordered; xchg takes an integer, float or ptr, fadd
// and fsub a float, the rest an integer.
func (b *Block) AtomicRMW(op RMWOp, p, v Value, ord Ordering, align uint32, md ...Attach) Value {
	b.ptr(p, "AtomicRMW")
	b.val(v, "AtomicRMW")
	checkOrdering(ord, "AtomicRMW")
	if op > RMWFSub {
		panic("llvm: AtomicRMW: unknown operation")
	}
	if ord == Unordered {
		panic("llvm: atomicrmw cannot be unordered")
	}
	switch op {
	case RMWXchg:
		atomicType(v.Ty, true, true, "atomicrmw xchg")
	case RMWFAdd, RMWFSub:
		if !v.Ty.IsFloat() {
			panic("llvm: atomicrmw " + op.String() + " of " + v.String())
		}
	default:
		atomicType(v.Ty, false, false, "atomicrmw "+op.String())
	}
	r := b.beginValue(v.Ty, md)
	b.str("atomicrmw ")
	b.str(rmwNames[op])
	b.str(" ")
	b.typed(p)
	b.commaTyped(v)
	b.str(" ")
	b.str(orderNames[ord])
	b.align(align)
	b.end(md)
	return r
}

// CmpXchg atomically replaces *p by new if it equals old; the result is
// { T, i1 }: the loaded value and whether it was replaced. T is an integer
// or ptr; both orderings are at least monotonic and the failure ordering is
// neither release nor acq_rel.
func (b *Block) CmpXchg(p, old, new Value, succ, fail Ordering, align uint32, md ...Attach) Value {
	b.ptr(p, "CmpXchg")
	b.val(old, "CmpXchg")
	b.val(new, "CmpXchg")
	checkOrdering(succ, "CmpXchg")
	checkOrdering(fail, "CmpXchg")
	if old.Ty != new.Ty {
		panic("llvm: cmpxchg of " + old.String() + " and " + new.String())
	}
	atomicType(old.Ty, false, true, "cmpxchg")
	if succ == Unordered || fail == Unordered || fail == Release || fail == AcqRel {
		panic("llvm: cmpxchg with orderings " + succ.String() + " " + fail.String())
	}
	t := b.f.m.Struct(false, old.Ty, b.f.m.I(1))
	r := b.beginValue(t, md)
	b.str("cmpxchg ")
	b.typed(p)
	b.commaTyped(old)
	b.commaTyped(new)
	b.str(" ")
	b.str(orderNames[succ])
	b.str(" ")
	b.str(orderNames[fail])
	b.align(align)
	b.end(md)
	return r
}

// AtomicLoad is load atomic t, ptr p ord, align a: the ordering is not
// release or acq_rel and the alignment is explicit.
func (b *Block) AtomicLoad(t Type, p Value, ord Ordering, align uint32, md ...Attach) Value {
	b.f.m.checkType(t, "AtomicLoad")
	b.ptr(p, "AtomicLoad")
	checkOrdering(ord, "AtomicLoad")
	atomicType(t, true, true, "atomic load")
	if ord == Release || ord == AcqRel || align == 0 {
		panic("llvm: atomic load with ordering " + ord.String() + ", align " + strconv.FormatUint(uint64(align), 10))
	}
	r := b.beginValue(t, md)
	b.str("load atomic ")
	b.typ(t)
	b.commaTyped(p)
	b.str(" ")
	b.str(orderNames[ord])
	b.align(align)
	b.end(md)
	return r
}

// AtomicStore is store atomic v, ptr p ord, align a: the ordering is not
// acquire or acq_rel and the alignment is explicit.
func (b *Block) AtomicStore(v, p Value, ord Ordering, align uint32, md ...Attach) {
	b.val(v, "AtomicStore")
	b.ptr(p, "AtomicStore")
	checkOrdering(ord, "AtomicStore")
	atomicType(v.Ty, true, true, "atomic store")
	if ord == Acquire || ord == AcqRel || align == 0 {
		panic("llvm: atomic store with ordering " + ord.String() + ", align " + strconv.FormatUint(uint64(align), 10))
	}
	b.begin(md)
	b.str("store atomic ")
	b.typed(v)
	b.commaTyped(p)
	b.str(" ")
	b.str(orderNames[ord])
	b.align(align)
	b.end(md)
}

// Fence is fence ord; ord is acquire, release, acq_rel or seq_cst.
func (b *Block) Fence(ord Ordering, md ...Attach) {
	if ord < Acquire || ord > SeqCst {
		panic("llvm: fence cannot be unordered or monotonic")
	}
	b.emit("fence "+orderNames[ord], md)
}

// checkTarget panics unless t is a block of this function other than its
// entry block, which may have no predecessors (codegen gives a
// MIR loop header at block 0 its own entry block).
func (b *Block) checkTarget(t *Block, what string) {
	if t == nil || t.f != b.f {
		panic("llvm: " + what + " to a block of another function")
	}
	if t == b.f.blocks[0] {
		panic("llvm: " + what + " to the entry block " + t.ref + " of " + b.f.ref)
	}
}

func (b *Block) labelOp(t *Block) {
	b.str("label ")
	b.str(t.ref)
}

// Br is an unconditional branch.
func (b *Block) Br(target *Block, md ...Attach) {
	b.checkTarget(target, "Br")
	b.begin(md)
	b.str("br ")
	b.labelOp(target)
	b.end(md)
	b.term = true
}

// CondBr branches on the i1 c; !prof weights are true then false.
func (b *Block) CondBr(c Value, t, f *Block, md ...Attach) {
	b.val(c, "CondBr")
	if c.Ty.Bits() != 1 || !c.Ty.IsInt() {
		panic("llvm: CondBr on " + c.String())
	}
	b.checkTarget(t, "CondBr")
	b.checkTarget(f, "CondBr")
	b.begin(md)
	b.str("br ")
	b.typed(c)
	b.str(", ")
	b.labelOp(t)
	b.str(", ")
	b.labelOp(f)
	b.end(md)
	b.term = true
}

// Case is one case of a switch: an integer constant and its target.
type Case struct {
	Val    Value
	Target *Block
}

// Switch branches on the integer v; !prof weights are the default then the
// cases in order. Case values are distinct integer constants.
func (b *Block) Switch(v Value, def *Block, cases []Case, md ...Attach) {
	b.val(v, "Switch")
	if !v.Ty.IsInt() {
		panic("llvm: Switch on " + v.String())
	}
	b.checkTarget(def, "Switch")
	var seen map[Value]bool // only for long switches; short ones compare pairwise
	if len(cases) > 16 {
		seen = make(map[Value]bool, len(cases))
	}
	for i, c := range cases {
		b.val(c.Val, "Switch")
		if !c.Val.isConst() || c.Val.Ty != v.Ty {
			panic("llvm: Switch case " + c.Val.String() + " on " + v.String())
		}
		b.checkTarget(c.Target, "Switch")
		dup := false
		if seen != nil {
			dup = seen[c.Val]
			seen[c.Val] = true
		} else {
			for _, d := range cases[:i] {
				dup = dup || d.Val == c.Val
			}
		}
		if dup { // ConstInt prints canonically, so equal values are equal Values
			panic("llvm: Switch: duplicate case value " + c.Val.String())
		}
	}
	b.begin(md)
	b.str("switch ")
	b.typed(v)
	b.str(", ")
	b.labelOp(def)
	b.str(" [")
	for _, c := range cases {
		b.str("\n    ")
		b.typed(c.Val)
		b.str(", ")
		b.labelOp(c.Target)
	}
	if len(cases) > 0 {
		b.str("\n ")
	}
	b.str(" ]")
	b.end(md)
	b.term = true
}

// Ret returns v, or void when v is nil; v must match the function's
// result type.
func (b *Block) Ret(v *Value, md ...Attach) {
	ret := b.f.sig.Ret
	if v == nil {
		if !ret.IsVoid() {
			panic("llvm: ret void in " + b.f.ref + " returning " + ret.String())
		}
		b.emit("ret void", md)
		b.term = true
		return
	}
	b.val(*v, "Ret")
	if v.Ty != ret {
		panic("llvm: ret " + v.String() + " in " + b.f.ref + " returning " + ret.String())
	}
	b.begin(md)
	b.str("ret ")
	b.typed(*v)
	b.end(md)
	b.term = true
}

// Unreachable is unreachable.
func (b *Block) Unreachable(md ...Attach) {
	b.emit("unreachable", md)
	b.term = true
}
