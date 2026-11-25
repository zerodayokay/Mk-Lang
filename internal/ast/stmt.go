// Why: statements and patterns share their structure (binding, matching and
// destructuring all take patterns), so their node types live together.
//
// What: the statement and pattern productions of doc/grammar.md: Block,
// Stmt, Bind, RecordPat, PatField, Assign, If, Cond, For, ForHead,
// ForSource, Match, Arm, Pattern, Alt, PatFields, Return, Fail, Defer,
// Scope, Unsafe, the labeled For, break and continue.
//
// How: plain node structs. IfStmt and MatchStmt are also Exprs in a value
// position; a negative integer literal pattern sets Neg; a record pattern's
// trailing ", .." sets Rest.

package ast

// Block is "{" { Stmt Term } "}".
type Block struct {
	Base
	Stmts []Stmt
}

// BindStmt is [ "var" ] Name ( ":=" Expr | ":" Type [ "=" Expr ] ).
type BindStmt struct {
	Base
	Var   bool
	Name  *Ident
	Type  Type // nil for x := e
	Value Expr // nil for x: T
}

// DestructureStmt is [ "var" ] RecordPat ":=" Expr. The parser reads the
// left side as a call, record literal or array literal and reinterprets it
// when ":=" follows
// (the grammar's cover grammar); Reinterpreted records that it did so.
type DestructureStmt struct {
	Base
	Var           bool
	Pat           *RecordPat
	Value         Expr
	Reinterpreted bool
}

// DiscardStmt is "_" "=" Expr.
type DiscardStmt struct {
	Base
	Value Expr
}

// AssignStmt is Expr AssignOp Expr; Op is TokAssign or a compound
// assignment.
type AssignStmt struct {
	Base
	Target Expr
	Op     TokenKind
	Value  Expr
}

// IfStmt is "if" Cond Block [ "else" ( If | Block ) ]. It is also an Expr,
// standing in an Expr field only in a value position; check
// decides whether a last-line IfStmt of a value arm is a value.
type IfStmt struct {
	Base
	// Cond is an Expr, a *BindCond or an *IsCond.
	Cond Node
	Then *Block
	// Else is nil, an *IfStmt or a *Block.
	Else Node
}

// BindCond is Name ":=" Expr: binds the payload of an optional
// (if x := e, for x := e). The Expr may be a UnaryExpr with Op TokMut
// (if x := mut e).
type BindCond struct {
	Base
	Name  *Ident
	Value Expr
}

// IsCond is Name "is" Type: if T is u8. The resolver rejects it when Name is
// not a type parameter.
type IsCond struct {
	Base
	Name *Ident
	Type Type
}

// ForStmt is [ Name ":" ] "for" [ ForHead ] Block.
type ForStmt struct {
	Base
	Label *Ident // nil when unlabeled
	// Head is nil (forever), an *InHead, or a Cond: an Expr (while), a
	// *BindCond (for x := e) or an *IsCond.
	Head Node
	Body *Block
}

// InHead is ( Name [ "," ( Name | RecordPat ) ] | RecordPat ) "in" ForSource.
// for x in e: Key x. for i, x in e: Key i, Value x. for (a, b) in e: Pat,
// Key nil. for i, (a, b) in e: Key i, Pat (a destructuring loop
// variable). Pat.Path is always nil.
type InHead struct {
	Base
	Key   *Ident     // the first name, nil when the loop variable is only Pat
	Value *Ident     // the second name, nil when absent: for i, x in xs
	Pat   *RecordPat // the destructured element, nil when not destructured
	// Source is a *RangeSource, a *RefSource or a *FieldsSource.
	Source Node
}

// RangeSource is Expr ".." Expr.
type RangeSource struct {
	Base
	Lo Expr
	Hi Expr
}

// RefMode is how a for loop reads its source: by value, & or mut.
type RefMode uint8

const (
	// ByCopy is for x in xs.
	ByCopy RefMode = iota
	// ByRef is for x in &xs.
	ByRef
	// ByMutRef is for x in mut xs.
	ByMutRef
)

// RefSource is [ "mut" | "&" ] Expr.
type RefSource struct {
	Base
	Mode RefMode
	X    Expr
}

// FieldsSource is "fields" "(" Expr ")".
type FieldsSource struct {
	Base
	X Expr
}

// MatchStmt is "match" Expr "{" { Arm Term } "}". Like IfStmt, it is also
// an Expr in a value position.
type MatchStmt struct {
	Base
	X    Expr
	Arms []*Arm
}

// Arm is Pattern Block.
type Arm struct {
	Base
	Pat  Pattern
	Body *Block
}

// ReturnStmt is "return" [ Expr ].
type ReturnStmt struct {
	Base
	Value Expr // nil when absent
}

// FailStmt is "fail" Expr.
type FailStmt struct {
	Base
	Value Expr
}

// BreakStmt is "break" [ Name ].
type BreakStmt struct {
	Base
	Label *Ident
}

// ContinueStmt is "continue" [ Name ].
type ContinueStmt struct {
	Base
	Label *Ident
}

// DeferStmt is ( "defer" | "onerr" ) ( Block | Expr ). Exactly one of Body and
// X is set.
type DeferStmt struct {
	Base
	Onerr bool
	Body  *Block
	X     Expr
}

// ScopeStmt is [ "try" ] "scope" Name ":=" Expr Block.
type ScopeStmt struct {
	Base
	Try   bool
	Name  *Ident
	Value Expr
	Body  *Block
}

// UnsafeStmt is "unsafe" String Block.
type UnsafeStmt struct {
	Base
	Reason *BasicLit
	Body   *Block
}

// ExprStmt is an expression used as a statement.
type ExprStmt struct {
	Base
	X Expr
}

// RecordPat is [ Path ] "(" PatField { "," PatField } [ "," ".." ] ")", or,
// with Elems, the array pattern "[" Name { "," Name } "]": field i then binds
// element i of a fixed array, by position, and has no Pat.
type RecordPat struct {
	Base
	Path   *Path // nil for (a, b) := e and [a, b] := e
	Fields []*PatField
	Rest   bool // true when ", .." ends the fields
	Elems  bool // true for [a, b] := e
}

// PatField is Name [ ":" Pattern ].
type PatField struct {
	Base
	Name *Ident
	Pat  Pattern // nil when absent: the field binds a local of its name
}

// AltPattern is Alt "|" Alt { "|" Alt }. A single Alt is returned as itself.
type AltPattern struct {
	Base
	Alts []Pattern
}

// VariantPattern is "." Name [ "(" PatFields ")" ].
type VariantPattern struct {
	Base
	Name      *Ident
	HasFields bool
	Fields    []*PatField
}

// PathPattern is Path [ "(" PatFields ")" ]: a named variant or error.
type PathPattern struct {
	Base
	Path      *Path
	HasFields bool
	Fields    []*PatField
}

// LiteralPattern is Literal | "-" Int. A float literal parses and is E0308.
type LiteralPattern struct {
	Base
	Neg bool
	Lit *BasicLit
}

// WildcardPattern is "_".
type WildcardPattern struct {
	Base
}

// SomePattern is "some" "(" Pattern ")".
type SomePattern struct {
	Base
	Pat Pattern
}

// NonePattern is "none".
type NonePattern struct {
	Base
}

// OkPattern is "ok" "(" Pattern ")".
type OkPattern struct {
	Base
	Pat Pattern
}

// ErrPattern is "err" "(" Pattern ")".
type ErrPattern struct {
	Base
	Pat Pattern
}

// NamePattern is Name: a binding or a payload-free case; the resolver decides.
type NamePattern struct {
	Base
	Name *Ident
}
