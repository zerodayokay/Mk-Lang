// Why: lower must not depend on how check stores its results; it reads them
// through one narrow interface, which tests can also implement by hand.
//
// What: Input, the facts lower reads from check for one body (its signature,
// node types, uses, calls, patterns, conversions, value constructs, captures,
// asm templates, fields-loop copies), and the fact types Use, Call, Pat,
// Conversion, Lit, Binding, Env and FieldCopy.
//
// How: every fact is keyed by an ast.NodeID within the body's own node range
// (Nodes), so lower's per-node side tables are sized by the body, not by the
// file.

package lower

import (
	"mk/internal/ast"
	"mk/internal/check"
	"mk/internal/source"
	"mk/internal/types"
)

// Input is one checked body. Every method is indexed by ast.NodeID of the
// body's file, takes no lock, and returns the zero value for a node the
// method does not apply to. The production implementation is FromCheck.
type Input interface {
	Item() types.ItemID
	Kind() BodyKind
	// Sig is the body's signature: the function's own; for a test, a
	// synthesized one (sys: &System when declared, result void, errors the
	// union of the sets of every try and fail in the body); for a const or
	// global, no parameters and Result the declared type; for a contract or
	// invariant item, check's predicate signature.
	Sig() *types.Signature
	// Root is the *ast.Block of a function, literal or test, or the ast.Expr
	// of a const, global, contract or invariant body.
	Root() ast.Node
	File() *ast.File
	// Nodes is the item's NodeID range [lo, lo+n): every node lower reads
	// a fact of or binds a local to lies in it (the parser gives one item
	// a contiguous range, check's Body.Base), so per-node side tables are
	// sized by the body, not by the file.
	Nodes() (lo ast.NodeID, n int)
	// GenericCount is the body's combined generic list length, hidden fields
	// generics included.
	GenericCount() uint32

	// Type is the type of every Expr, binding Ident and Pattern.
	Type(ast.NodeID) types.TypeID
	// Use says what a name (or a FieldExpr naming a field) refers to.
	Use(ast.NodeID) Use
	// Call says what a CallExpr calls.
	Call(ast.NodeID) Call
	// Adjust is the implicit step check applied to an expression's value
	// (auto-deref layers, receiver borrows, copies through a reference).
	// One form for every expression.
	Adjust(ast.NodeID) Adjust
	// Pattern gives the resolved form of every Pattern node and PatField.
	Pattern(ast.NodeID) Pat
	// Convert is the implicit conversion applied to an Expr's value.
	Convert(ast.NodeID) Conversion
	// Literal is the compile-time value check recorded at a node: a
	// literal, a folded constant condition, a SizeOf, a const argument.
	Literal(ast.NodeID) Lit
	// Binding describes the local a binding Ident declares.
	Binding(ast.NodeID) Binding
	// Diverges reports whether control never continues after the node.
	Diverges(ast.NodeID) bool
	// OnerrConsumes is, for an onerr DeferStmt, the one linear value its
	// body consumes, or the zero Consumed.
	OnerrConsumes(ast.NodeID) Consumed
	// Facts gives the ownership facts of a type of this body; a KParam
	// answers from its bounds. ok is false only for an ICE.
	Facts(types.TypeID) (types.Facts, bool)
	// ImplicitRecord reports a Print or Arena.Fmt CallExpr written without
	// its record: lower supplies the ().
	ImplicitRecord(call ast.NodeID) bool
	// IsValue reports whether an IfStmt, MatchStmt or ElseExpr node is a
	// value construct (check's Body.Values).
	IsValue(ast.NodeID) bool
	// ErrorFormat is the plan of a BodyErrorFormat body, nil otherwise.
	ErrorFormat() *check.ErrorFormat
	// EnvRef is a capturing literal body's hidden environment parameter
	// type, &R, NoType for every other body.
	EnvRef() types.TypeID
	// Env is the environment lower builds at a capturing FnLit written as
	// a const argument; ok is false
	// for every other node.
	Env(lit ast.NodeID) (Env, bool)
	// AsmTemplate is an asm statement's template with every hole resolved to
	// the index of its operand in s.Operands ({i}, {i:mod}; {{ and }} kept),
	// as check's AsmTemplate gives it, and the
	// statement's options as mir.AsmOptions bits. With options(raw) the text
	// is the literal as written.
	AsmTemplate(s *ast.AsmStmt) (text string, options uint16)

	// FieldCopies lists the copies of a concretely unrolled fields loop
	// nested directly in the active copy (or in no
	// copy), in field order. A generic fields loop, or a concrete one over
	// a type with no visible fields, has none.
	FieldCopies(loop ast.NodeID) []FieldCopy
	// EnterCopy makes a copy's facts the ones every method answers for the
	// nodes of its range (its loop head and body), until LeaveCopy. Copies
	// nest: a loop inside a copy is entered while that copy is active.
	EnterCopy(FieldCopy)
	LeaveCopy()
}

// FieldCopy is one field's copy of a concretely unrolled fields loop: Field
// is the struct's field index (a record's in sorted order), the static
// ProjField index of the value place; ID is the adapter's handle.
type FieldCopy struct {
	ID    int
	Field uint32
}

// UseKind is what a name denotes.
type UseKind uint8

const (
	UseNone       UseKind = iota
	UseLocal              // a binding: Binding is the declaring Ident's NodeID
	UseParam              // parameter Param (receiver is 1)
	UseSelfField          // a bare field name inside an invariant: self.<Field>
	UseFunc               // function item Item applied to Args, as a value
	UseConst              // const item Item
	UseGlobal             // var global Item
	UseVariant            // payload-free variant Variant of enum Item, as a value
	UseError              // payload-free error Item, as a value
	UseField              // a field read (FieldExpr): Field is its index
	UseTarget             // the universe name Target
	UseConstParam         // a const type parameter used as a value: Generic is its index
	UseNoneLit            // the universe value none
	// UseCapture is a name a capturing literal reads or mutates from its
	// enclosing body: Capture is its field index in the environment record,
	// the place (*env).x when Direct (reborrowed mut T / mut []T) else *((*env).x).
	UseCapture
)

// Env is a capturing literal's environment at its call: the record type is
// Ref's element, its fields &T_i, mut T_i or mut []T_i in Sources' order.
type Env struct {
	Item types.ItemID // the literal item
	Ref  types.TypeID // &R or mut R, the literal body's hidden parameter type
	// Sources are the captured names in field order, as the enclosing body
	// sees them: UseLocal, UseParam, or UseCapture of its own environment.
	Sources []Use
	// Modes records how each capture is borrowed into the environment record,
	// in field order.
	Modes []check.CaptureMode
}

// Consumed is what an onerr body consumes: exactly one of Binding and Param
// is set; both zero is none.
type Consumed struct {
	Binding ast.NodeID
	Param   types.ParamIndex
}

// Use is the meaning of a name.
type Use struct {
	Kind    UseKind
	Binding ast.NodeID
	Param   types.ParamIndex
	Item    types.ItemID
	Args    []types.GenericArg
	Variant uint32
	Field   uint32
	Generic uint32
	Capture uint32 // UseCapture: the environment field index; UseSelfField: 1 + the field index of self's capture, 0 for the predicate's own self
	Direct  bool   // UseCapture: true when the environment field holds the reborrowed value directly (CapReborrow)
}

// CallKind is what a call expression does.
type CallKind uint8

const (
	CallNone    CallKind = iota
	CallFunc             // direct call of Item with Args
	CallIface            // interface method Method of Iface on SelfType
	CallValue            // call through a fn value: the callee expression's value
	CallStruct           // construction of struct Item applied to Args
	CallVariant          // construction of variant Variant of enum Item
	CallError            // construction of error Item
	CallBuiltin          // Builtin
)

// Recv is how a method call forms its receiver.
type Recv uint8

const (
	RecvNone  Recv = iota // not a method call
	RecvPass              // pass the receiver expression by the argument rules
	RecvRef               // &place: an automatic shared borrow
	RecvMut               // mut place: an automatic exclusive borrow
	RecvValue             // by-value self: copy or move the place
)

// Builtin is a universe call lower expands inline.
type Builtin uint8

const (
	BNone      Builtin = iota
	BAssert            // Assert(c)
	BPanic             // Panic(msg)
	BConvert           // T(x), lossless
	BCast              // Cast[T](x)
	BTrunc             // Trunc[T](x)
	BSizeOf            // SizeOf[T]()
	BAlignOf           // AlignOf[T]()
	BEvalArena         // EvalArena()
	BSome              // some(x)
	BOptMust           // ?T .Must()
	BOptIsSome         // ?T .IsSome()
	BOptIsNone         // ?T .IsNone()
	BResMust           // E!T .Must(): the receiver is a fallible call or an E!T value
	BLen               // Len() on []T, mut []T, [N]T, str
	BOk                // ok(x), when check types it
	BErr               // err(x)
	BTarget            // Target
)

// Call is a resolved call.
type Call struct {
	Kind     CallKind
	Item     types.ItemID
	Args     []types.GenericArg
	Iface    types.ItemID
	Method   uint32
	SelfType types.TypeID
	Variant  uint32
	Builtin  Builtin
	// Recv is set for a method call whose receiver is Fun's FieldExpr.X;
	// Recv's place is reached through the receiver's Adjust.Deref layers.
	Recv Recv
	// Fields maps each written argument, in written order, to its field
	// index in declaration order (construction).
	Fields []uint32
	// Errors is the callee's error set (EmptyErrors when it cannot fail).
	Errors types.ErrorSetID
	// Sig is the callee's signature (CallFunc, CallIface), or nil.
	Sig *types.Signature
	// Lossless is set on a BCast whose type pair T(x) accepts by type.
	Lossless bool
}

// AdjustKind is check's implicit step.
type AdjustKind uint8

const (
	AdjNone AdjustKind = iota
	AdjRefShared
	AdjRefMut
	AdjDeref
	AdjCopyThrough
)

// Adjust is check's implicit step on an expression: Deref reference layers
// removed, then a borrow or a copy through the reference.
type Adjust struct {
	Kind  AdjustKind
	Deref uint8
}

// PatKind is the resolved form of a pattern.
type PatKind uint8

const (
	PInvalid   PatKind = iota
	PBind              // a fresh binding
	PWild              // _
	PLit               // literal: value from Literal
	PVariant           // enum variant Variant of Item
	PErrorCase         // error type Item inside an error set
	PSome              // some(p)
	PNone              // none
	POk                // ok(p)
	PErr               // err(p)
	PAlt               // p | q
	PStruct            // destructure of struct Item
	PRecord            // destructure of a record
	PRange             // lo..hi of integer or char literals: ends from Literal
)

// BindMode is how a binding takes its value.
type BindMode uint8

const (
	ByValue  BindMode = iota // copy if plain, move otherwise
	ByShared                 // &T to the place
	ByMut                    // mut T to the place
)

// Pat is a resolved pattern.
type Pat struct {
	Kind    PatKind
	Item    types.ItemID
	Variant uint32
	Mode    BindMode
	// Field is, for a PatField, the field index the sub-pattern matches.
	Field uint32
}

// ConvKind is an implicit conversion applied after computing an Expr.
type ConvKind uint8

const (
	ConvNone   ConvKind = iota
	ConvWiden           // error set widening to To, CastWidenErrors
	ConvNarrow          // retyping to To inside an if T is X branch
)

// Conversion is an implicit conversion.
type Conversion struct {
	Kind ConvKind
	To   types.TypeID
}

// LitKind is a decoded literal.
type LitKind uint8

const (
	LitNone   LitKind = iota // no value recorded
	LitScalar                // Bits: an integer, bool or char at its type's width
	LitConst                 // Const: a float, string or other value, as an OpConst
)

// Lit is a compile-time value at a node.
type Lit struct {
	Kind  LitKind
	Type  types.TypeID
	Bits  uint64
	Const types.ConstID
}

// Binding is the local a binding Ident declares.
type Binding struct {
	Name source.Symbol
	Type types.TypeID
	Var  bool
	OK   bool
}
