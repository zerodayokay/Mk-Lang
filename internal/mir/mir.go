// Why: every stage after lower reads one typed control-flow graph; its
// node types are the contract between lower, borrow, eval, mono and
// codegen, and they stay frozen per compiler version.
//
// What: the IDs (LocalID, BlockID, ...), Local and LocalKind, Place and
// Projection, Operand, BorrowOrigin, BinOp, UnOp, CastKind, the aggregate
// kinds, Rvalue and its kinds, and Statement and its kinds.
//
// How: every node is a pointer-free tagged struct; variable-length parts
// are Ranges into the Body's side slices. BinOp and other numbered kinds
// are encoded by iface, so new kinds are appended, never inserted.

package mir

import (
	"math"
	"strconv"

	"mk/internal/source"
	"mk/internal/types"
)

// MIR IDs index slices of one Body. Slot 0 of each is meaningful (local 0 is
// the return place, block 0 the entry, span 0 the body's own span), so unlike
// the session-wide IDs the "none" value of each is all ones.
type (
	// LocalID indexes Body.Locals.
	LocalID uint32
	// BlockID indexes Body.Blocks.
	BlockID uint32
	// SpanID indexes Body.Spans.
	SpanID uint32
	// LoopID indexes Body.Loops.
	LoopID uint32
	// DeferID indexes Body.Defers.
	DeferID uint32
)

// The none values.
const (
	NoLocal LocalID = math.MaxUint32
	NoBlock BlockID = math.MaxUint32
	NoSpan  SpanID  = math.MaxUint32
	NoLoop  LoopID  = math.MaxUint32
	NoDefer DeferID = math.MaxUint32
)

// ReturnLocal is local 0, the return place (M1).
const ReturnLocal LocalID = 0

// EntryBlock is block 0 (M2).
const EntryBlock BlockID = 0

// Range is a half-open range [Start, Start+Len) of one of a Body's side
// slices (Projections, GenericArgs, Operands, Args, Cases).
type Range struct {
	Start uint32
	Len   uint32
}

// End is Start + Len.
func (r Range) End() uint32 { return r.Start + r.Len }

// LocalKind is what a local is (M1).
type LocalKind uint8

const (
	// LocalReturn is local 0 only: the signature's return type. A void return
	// local is never assigned and counts as initialized (borrow, eval).
	LocalReturn LocalKind = iota
	// LocalParam is parameter Param, local number Param.
	LocalParam
	// LocalUser is a named local; Var says whether it may be assigned again
	// (E0404) or mut-borrowed (E0508).
	LocalUser
	// LocalTemp is a compiler temporary.
	LocalTemp
	// LocalScope is the s of scope s := e.
	LocalScope
	// LocalLoopHolder holds loop Loop's one loan on its source.
	LocalLoopHolder
)

// Local is one local's declaration.
type Local struct {
	Type  types.TypeID
	Kind  LocalKind
	Param types.ParamIndex
	Name  source.Symbol
	Var   bool
	Loop  LoopID
	Span  SpanID
}

// FieldRef names a field: by index in the declaring struct, record (canonical
// order), variant or error; or, only in generic MIR, the field a fields loop
// is at, unrolled before any instance exists.
type FieldRef struct {
	Loop  bool
	Index uint32
}

// VariantSel picks a variant for a downcast or a discriminant compare.
type VariantSel uint8

const (
	// SelEnum is enum variant number A.
	SelEnum VariantSel = iota
	// SelSome is the some side of ?T.
	SelSome
	// SelOk is the ok side of E!T.
	SelOk
	// SelErr is the error side of E!T, as an ErrorSet value.
	SelErr
	// SelCase is one error of a set, item A.
	SelCase
)

// ProjKind is one step of a place.
type ProjKind uint8

const (
	// ProjField: field FieldRef{Loop: FieldLoop, Index: A}. On a fixed
	// array [N]T it is element A < N, a constant index that is a place of
	// its own for overlap (an array destructure's step).
	ProjField ProjKind = iota
	// ProjDeref: through &T, mut T or *T.
	ProjDeref
	// ProjIndex: element at the value of local A. All indices of one array
	// are one step for overlap.
	ProjIndex
	// ProjDowncast: variant Sel (A is the enum variant or case item).
	ProjDowncast
)

// Projection is one step: its kind, its operand, and the type after the step.
// The type before step 0 is the local's.
type Projection struct {
	Kind      ProjKind
	FieldLoop bool
	Sel       VariantSel
	A         uint32
	Type      types.TypeID
}

// Place is a local and a range of Body.Projections. Comparable.
type Place struct {
	Local LocalID
	Proj  Range
}

// LocalPlace is the place of a whole local.
func LocalPlace(l LocalID) Place { return Place{Local: l} }

// OperandKind tags an Operand.
type OperandKind uint8

const (
	// OpCopy reads a plain place.
	OpCopy OperandKind = iota
	// OpMove moves a non-plain place out.
	OpMove
	// OpConst is the interned const Const of type Type.
	OpConst
	// OpImm is an integer, bool or char immediate of type Type, its bit
	// pattern in Bits (for scalars up to 64 bits).
	OpImm
)

// Operand is a value read by an rvalue, an argument or a switch.
type Operand struct {
	Kind  OperandKind
	Place Place
	Type  types.TypeID
	Const types.ConstID
	Bits  uint64
}

// Copy, Move, Const and Imm build operands.
func Copy(p Place) Operand { return Operand{Kind: OpCopy, Place: p} }
func Move(p Place) Operand { return Operand{Kind: OpMove, Place: p} }
func Const(t types.TypeID, c types.ConstID) Operand {
	return Operand{Kind: OpConst, Type: t, Const: c}
}
func Imm(t types.TypeID, bits uint64) Operand { return Operand{Kind: OpImm, Type: t, Bits: bits} }

// BorrowKind is & or mut.
type BorrowKind uint8

const (
	Shared BorrowKind = iota
	Exclusive
)

// BorrowOrigin tags a borrow.
type BorrowOrigin uint8

const (
	// OriginPlain is an ordinary &p or mut p.
	OriginPlain BorrowOrigin = iota
	// OriginLoopHolder is a loop's one loan on its source.
	OriginLoopHolder
	// OriginLoopElement is one iteration's element loan.
	OriginLoopElement
)

// UnOp is a unary operator: Neg traps on overflow in every build.
type UnOp uint8

const (
	Neg UnOp = iota
	Not
	BitNot
)

// BinOp is a binary operator. + - * trap on overflow; / and %
// trap on zero and MIN / -1; << >> trap when the amount is at least the bit
// width; Shr is arithmetic for signed operands; +% -% *% wrap; && and || are
// control flow, never a BinOp. Eq and Ne apply to numbers, bool, char,
// payload-free enums and str (by content); the orderings to numbers, char and
// str (by bytes).
type BinOp uint8

const (
	Add BinOp = iota
	Sub
	Mul
	Div
	Rem
	AddWrap
	SubWrap
	MulWrap
	Shl
	Shr
	BitAnd
	BitOr
	BitXor
	Eq
	Ne
	Lt
	Le
	Gt
	Ge
	// AddNoWrap (appended: the numbering above is encoded by iface) is
	// an integer add whose producer has proved that the exact sum lies in the
	// operand type's range: lower's `for i in a..b` counter increment, where
	// the header tested c < n in the same type, and lower's range analysis,
	// wherever the exact sum over the operands' intervals lies in the type.
	// It never traps. The validator types it as AddWrap and requires
	// integer operands; eval runs it as AddWrap; codegen may mark it nsw on a
	// signed type or nuw on an unsigned one, never both on every type (a
	// signed -1 + 1 wraps as unsigned, an unsigned 2^63-1 + 1 overflows as
	// signed).
	AddNoWrap
	// SubNoWrap and MulNoWrap (appended after AddNoWrap) are the same
	// for - and *: the producer (lower's range analysis) proved the exact
	// difference or product in the operand type's range. The validator types
	// them as SubWrap and MulWrap and requires integer operands; eval runs
	// them as SubWrap and MulWrap; codegen may mark them nsw on a signed type
	// or nuw on an unsigned one, as for AddNoWrap. lower's range analysis
	// (rangeloop.go) emits them.
	SubNoWrap
	MulNoWrap
)

// IsCompare reports whether op yields bool.
func (op BinOp) IsCompare() bool { return op >= Eq && op <= Ge }

// CastKind is a conversion.
type CastKind uint8

const (
	// CastLossless is T(x): no value can change.
	CastLossless CastKind = iota
	// CastChecked is Cast[T](x): traps when it does not fit; rounds to floats.
	CastChecked
	// CastTrunc is Trunc[T](x): keeps the low bits.
	CastTrunc
	// CastWidenErrors is a subset error set to a superset: the code is kept.
	CastWidenErrors
	// CastNarrow is the no-op retyping inside an if T is X branch.
	CastNarrow
)

// AggKind is what an aggregate builds.
type AggKind uint8

const (
	// AggStruct builds struct Item with every field, in declaration order.
	AggStruct AggKind = iota
	// AggEnum builds variant Variant of enum Item.
	AggEnum
	// AggError builds error Item.
	AggError
	// AggRecord builds record type Type, operands in canonical field order.
	AggRecord
	// AggArray builds an array of Type. With Type a []T it is the static
	// slice: a []T over its operands, held in static data;
	// lower emits it only in a const initializer, which only the evaluator
	// runs.
	AggArray
	// AggSome, AggOk and AggErr wrap one operand; AggOk with no operand is
	// the success of E!void.
	AggSome
	AggOk
	AggErr
	// AggNone is none of the destination ?T, with no operands.
	AggNone
)

// RvalueKind tags an Rvalue.
type RvalueKind uint8

const (
	RvUse RvalueKind = iota
	// RvRef is &Place or mut Place (Borrow), with Origin and Loop.
	RvRef
	// RvSlice is Place[lo..hi] with HasLo, A, HasHi, B, of kind Borrow; the
	// place's type picks the memory-model row. Origin and Loop are as for
	// RvRef: OriginLoopHolder makes it the reslice holder of for x in mut xs
	// over a mut []T or an array, the holder's one assignment.
	RvSlice
	RvUnary
	RvBinary
	// RvCast converts A to Type by Cast.
	RvCast
	// RvAggregate builds Agg from Operands Ops.
	RvAggregate
	// RvRepeat is [A; Len], A plain.
	RvRepeat
	// RvLen is the length of Place (int).
	RvLen
	// RvDiscriminant is the logical discriminant of Place (u64): none 0, some
	// 1; ok 0, an error its code; enum variants their discriminants.
	RvDiscriminant
	// RvFnRef is the function Fn as a value.
	RvFnRef
	// RvGlobalRef is &global Item, or &C of const Item: a static reference
	// to its storage.
	RvGlobalRef
	// RvConstRef is the value of const Item.
	RvConstRef
	// RvSizeOf, RvAlignOf and RvFieldCount are int facts of Type.
	RvSizeOf
	RvAlignOf
	RvFieldCount
	// RvTypeIs is bool: generic Param is Type (resolved per instance).
	RvTypeIs
	// RvFieldName is the str name of Field.
	RvFieldName
	// RvConstArg is the value of the const argument Len (normally a
	// CAParam) at type Type: a const type parameter used as a value. mono
	// substitutes it like every ConstArg.
	RvConstArg
)

// FnRef names a function and a range of Body.GenericArgs.
type FnRef struct {
	Item types.ItemID
	Args Range
}

// Rvalue is the right side of an assignment. Pointer-free; which fields are
// meaningful depends on Kind.
type Rvalue struct {
	Kind    RvalueKind
	Borrow  BorrowKind
	Origin  BorrowOrigin
	Loop    LoopID
	Place   Place
	A, B    Operand
	HasLo   bool
	HasHi   bool
	Un      UnOp
	Bin     BinOp
	Cast    CastKind
	Type    types.TypeID
	Agg     AggKind
	Item    types.ItemID
	Variant uint32
	Ops     Range
	Len     types.ConstArg
	Param   uint32
	Field   FieldRef
	Fn      FnRef
}

// StatementKind tags a Statement.
type StatementKind uint8

const (
	// StAssign is Place = Rvalue.
	StAssign StatementKind = iota
	// StScopeEnd ends Local's scope: E0402 for a linear local still
	// initialized; ends loans on a temporary.
	StScopeEnd
	// StJoin waits for every task of scope Local, including those spawned
	// during the wait, after cancelling them when Cancel is set.
	StJoin
	StNop
	// StAsm is one inline-assembly statement: Asm indexes Body.Asms. A
	// noreturn asm is the last statement of its block, whose terminator is
	// TermUnreachable (M13).
	StAsm
)

// Statement is one MIR statement.
type Statement struct {
	Kind   StatementKind
	Place  Place
	Rvalue Rvalue
	Local  LocalID
	Cancel bool
	Span   SpanID
	// Asm is StAsm's index into Body.Asms.
	Asm AsmID
}

func (k RvalueKind) String() string {
	names := [...]string{"use", "ref", "slice", "unary", "binary", "cast", "aggregate", "repeat", "len",
		"discriminant", "fnref", "globalref", "constref", "sizeof", "alignof", "fieldcount", "typeis", "fieldname",
		"constarg"}
	if int(k) < len(names) {
		return names[k]
	}
	return "rvalue(" + strconv.Itoa(int(k)) + ")"
}
