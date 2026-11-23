// Why: items and declarations are the top level of every file; their node
// types are the parser's output and every later pass's input.
//
// What: one node type per item and declaration production of doc/grammar.md:
// File, Import, Path, Gen, Test, Struct, Packed, Aligned, Member, Enum,
// Variant, Fields, Field, ErrorDecl, Interface, Impl, Alias, Const, Global,
// Extern, Func, Signature, Cpu, Section, FuncName, Params, SelfParam, Param,
// ParamFlow, Flow, Sources, Source, Contract, TypeParams, TypeParam, Bounds
// and TypeArgs, with the systems forms align, cpu, section, naked and
// extern var.
//
// How: plain structs embedding Base and ItemInfo; an optional part is a nil
// pointer. Commas go between flows, trailing commas are allowed, and "..."
// stands only on a bodiless extern.

package ast

import "mk/internal/source"

// Import is "import" [ Name "=" ] Path.
type Import struct {
	Base
	ItemInfo
	Alias *Ident // nil when absent
	Path  *Path
}

// GenItem is "gen" Path "(" [ TypeOrExpr { "," TypeOrExpr } ] ")" [ "fuel" Expr ].
type GenItem struct {
	Base
	ItemInfo
	Generator *Path
	Args      []TypeOrExpr
	Fuel      Expr // nil when absent
}

// TestItem is "test" String [ "(" Param ")" ] Block.
type TestItem struct {
	Base
	ItemInfo
	Name  *BasicLit // a TokStr literal
	Param *Param    // nil when absent: test "x" (sys: &System) { }
	Body  *Block
}

// Packed is "packed" [ "(" Int ")" ].
type Packed struct {
	Base
	Align *BasicLit // nil for bare packed
}

// Aligned is "align" "(" Int ")", a struct modifier beside Packed;
// align is no keyword, read by its position.
type Aligned struct {
	Base
	Align *BasicLit
}

// StructDecl is { "linear" | [ "mut" ] "view" | Packed | Aligned } "struct" Name
// [ TypeParams ] "{" { Member Term } "}". Packed and Align may come in either
// order, and Walk visits them in source order; both at once is check's
// error. MutView implies View.
type StructDecl struct {
	Base
	ItemInfo
	Linear     bool
	View       bool
	MutView    bool
	Packed     *Packed // nil when absent
	Name       *Ident
	TypeParams *TypeParams // nil when absent
	Members    []Member
	Align      *Aligned // nil when absent
}

// FieldMember is [ "export" ] Name ":" Type.
type FieldMember struct {
	Base
	Export bool
	Name   *Ident
	Type   Type
}

// InvariantMember is "invariant" Expr.
type InvariantMember struct {
	Base
	X Expr
}

// EnumDecl is [ "open" ] "enum" Name [ TypeParams ] [ ":" Type ] "{"
// { Variant Term } "}".
type EnumDecl struct {
	Base
	ItemInfo
	// Open is the contextual word open: the enum may gain variants.
	Open       bool
	Name       *Ident
	TypeParams *TypeParams // nil when absent
	Tag        Type        // nil when absent: enum Color: u8
	Variants   []*Variant
}

// Variant is Name [ "(" Fields ")" ] [ "=" Expr ].
type Variant struct {
	Base
	Name      *Ident
	HasFields bool // true when "(" Fields ")" was written
	Fields    []*Field
	Value     Expr // nil when absent
}

// Field is Name ":" Type (the grammar's Field; also the fields of a record
// type).
type Field struct {
	Base
	Name *Ident
	Type Type
}

// ErrorDecl is "error" Name [ "(" Fields ")" ].
type ErrorDecl struct {
	Base
	ItemInfo
	Name      *Ident
	HasFields bool
	Fields    []*Field
}

// InterfaceDecl is "interface" Name [ TypeParams ] "{" { Signature Term } "}".
type InterfaceDecl struct {
	Base
	ItemInfo
	Name       *Ident
	TypeParams *TypeParams
	Methods    []*Signature
}

// ImplDecl is [ "unsafe" ] "impl" [ TypeParams ] Type "for" Type
// "{" { Func Term } "}".
type ImplDecl struct {
	Base
	ItemInfo
	Unsafe     bool
	TypeParams *TypeParams
	Interface  Type
	For        Type
	Methods    []*FuncDecl
}

// AliasDecl is [ "open" ] "type" Name [ TypeParams ] "=" Type.
type AliasDecl struct {
	Base
	ItemInfo
	// Open is the contextual word open: the error set may gain members.
	Open       bool
	Name       *Ident
	TypeParams *TypeParams
	Type       Type
}

// ConstDecl is "const" Name [ ":" Type ] "=" Expr [ "fuel" Expr ]
// [ Section ].
type ConstDecl struct {
	Base
	ItemInfo
	Name  *Ident
	Type  Type // nil when absent
	Value Expr
	Fuel  Expr // nil when absent
	// Section is the string of the Section clause ("section" String), nil
	// when absent.
	Section *BasicLit
}

// GlobalDecl is "var" Name ":" Type "=" Expr [ Section ], or, with Extern
// set, the "extern" "var" Name ":" Type of the grammar's Extern: an extern
// var, whose Value and Section are nil.
type GlobalDecl struct {
	Base
	ItemInfo
	Name  *Ident
	Type  Type
	Value Expr // nil for an extern var
	// Section is the string of the Section clause, nil when absent.
	Section *BasicLit
	// Extern is true for an extern var.
	Extern bool
}

// ExternDecl is "extern" [ "naked" ] Signature [ Block ]. With a body it is
// an "export extern" definition callable from C. An "extern" "var" is a
// GlobalDecl with Extern set.
type ExternDecl struct {
	Base
	ItemInfo
	Sig  *Signature
	Body *Block // nil for a bodiless extern
	// Naked is true for extern naked; naked is no keyword.
	Naked bool
}

// FuncDecl is [ "naked" ] Signature Block | Signature. A bodiless FuncDecl
// is legal only where the design allows one (interface methods are
// Signatures, not FuncDecls; std intrinsics; the test prelude).
type FuncDecl struct {
	Base
	ItemInfo
	Sig  *Signature
	Body *Block // nil when bodiless
	// Naked is true for a naked function; naked is no keyword.
	Naked bool
}

// Signature is FuncName [ TypeParams ] "(" [ Params ] ")" [ ":" Type ]
// [ Flow { [ "," ] Flow } ] { Contract } [ Cpu ] [ Section ].
type Signature struct {
	Base
	Name       *FuncName
	TypeParams *TypeParams
	// Self is the receiver, when Params begins with SelfParam.
	Self   *SelfParam
	Params []*Param
	// Ellipsis is the span of a trailing "...", the zero Span when absent.
	Ellipsis  source.Span
	Result    Type // nil when absent (void)
	Flows     []Flow
	Contracts []*Contract
	// Cpu is the string of the Cpu clause ("cpu" String) and Section that of
	// the Section clause ("section" String), each nil when absent.
	// Both words are read by position, so neither is a keyword.
	Cpu     *BasicLit
	Section *BasicLit
}

// FuncName is Name [ TypeArgs ] "." Name | Name. For a method, Owner and
// OwnerArgs name the type (Stack2[T].Push); for a function Owner is nil.
type FuncName struct {
	Base
	Owner     *Ident
	OwnerArgs *TypeArgs
	Name      *Ident
}

// ReceiverMode is how a SelfParam receives self.
type ReceiverMode uint8

const (
	// ByValue is "self".
	ByValue ReceiverMode = iota
	// ByShared is "&self".
	ByShared
	// ByMut is "mut self".
	ByMut
)

// SelfParam is [ "&" | "mut" ] "self" [ ParamFlow ], where ParamFlow is
// "into" Name, as on a Param.
type SelfParam struct {
	Base
	Mode ReceiverMode
	Into *Ident // nil when absent
}

// Param is [ "const" | "var" ] Name ":" Type [ ParamFlow ], where ParamFlow
// is "into" Name.
type Param struct {
	Base
	Const bool
	Var   bool
	Name  *Ident
	Type  Type
	Into  *Ident // nil when absent
}

// FromFlow is "from" ( Sources | "(" FieldSrc { "," FieldSrc } [ "," ] ")" ).
// Exactly one of Sources and Fields is non-empty in a well-formed tree: the
// flat form fills Sources, the per-field form Fields in written order.
type FromFlow struct {
	Base
	Sources []*Source
	Fields  []*FieldSrc
}

// FieldSrc is Name ":" Sources: one field of a per-field clause and its
// sources. It stands only inside a FromFlow.
type FieldSrc struct {
	Base
	Name    *Ident
	Sources []*Source
}

// EndsFlow is "ends" Name.
type EndsFlow struct {
	Base
	Name *Ident
}

// StoreFlow is "*" Name "into" Name: *From into Into.
type StoreFlow struct {
	Base
	From *Ident
	Into *Ident
}

// Source is Name { "." Name }: a parameter, then field steps.
type Source struct {
	Base
	Names []*Ident
}

// ContractKind tells requires from ensures.
type ContractKind uint8

const (
	// Requires is a precondition.
	Requires ContractKind = iota
	// Ensures is a postcondition; result names the result in it.
	Ensures
)

// Contract is "requires" Expr | "ensures" Expr.
type Contract struct {
	Base
	Kind ContractKind
	X    Expr
}

// TypeParams is "[" TypeParam { "," TypeParam } [ "," ] "]".
type TypeParams struct {
	Base
	Params []*TypeParam
}

// TypeParam is Name [ ":" Bounds ] | "const" Name ":" Type. Bounds is
// Type { "+" Type }.
type TypeParam struct {
	Base
	Const  bool
	Name   *Ident
	Bounds []Type // type parameter bounds, in order
	Type   Type   // the const parameter's type
}

// TypeArgs is "[" TypeOrExpr { "," TypeOrExpr } [ "," ] "]".
type TypeArgs struct {
	Base
	Args []TypeOrExpr
}
