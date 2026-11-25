// Why: Go has no sum types; the node categories (Item, Expr, Stmt, ...)
// are sealed interfaces, and each node type must say which it belongs to.
//
// What: the unexported marker methods of every node type, and AllNodeTypes.
//
// How: generated from the lists in AllNodeTypes; TestEveryNodeTypeIsListed
// keeps the two in step. IfStmt and MatchStmt are also Exprs, standing in
// any Expr field.

package ast

func (*Import) aNode()               {}
func (*Import) itemNode()            {}
func (*GenItem) aNode()              {}
func (*GenItem) itemNode()           {}
func (*TestItem) aNode()             {}
func (*TestItem) itemNode()          {}
func (*StructDecl) aNode()           {}
func (*StructDecl) itemNode()        {}
func (*StructDecl) declNode()        {}
func (*EnumDecl) aNode()             {}
func (*EnumDecl) itemNode()          {}
func (*EnumDecl) declNode()          {}
func (*ErrorDecl) aNode()            {}
func (*ErrorDecl) itemNode()         {}
func (*ErrorDecl) declNode()         {}
func (*InterfaceDecl) aNode()        {}
func (*InterfaceDecl) itemNode()     {}
func (*InterfaceDecl) declNode()     {}
func (*ImplDecl) aNode()             {}
func (*ImplDecl) itemNode()          {}
func (*ImplDecl) declNode()          {}
func (*AliasDecl) aNode()            {}
func (*AliasDecl) itemNode()         {}
func (*AliasDecl) declNode()         {}
func (*ConstDecl) aNode()            {}
func (*ConstDecl) itemNode()         {}
func (*ConstDecl) declNode()         {}
func (*GlobalDecl) aNode()           {}
func (*GlobalDecl) itemNode()        {}
func (*GlobalDecl) declNode()        {}
func (*ExternDecl) aNode()           {}
func (*ExternDecl) itemNode()        {}
func (*ExternDecl) declNode()        {}
func (*FuncDecl) aNode()             {}
func (*FuncDecl) itemNode()          {}
func (*FuncDecl) declNode()          {}
func (*BadItem) aNode()              {}
func (*BadItem) itemNode()           {}
func (*BadItem) declNode()           {}
func (*ErrorType) aNode()            {}
func (*ErrorType) typeNode()         {}
func (*ErrorType) typeOrExpr()       {}
func (*UnionType) aNode()            {}
func (*UnionType) typeNode()         {}
func (*UnionType) typeOrExpr()       {}
func (*RefType) aNode()              {}
func (*RefType) typeNode()           {}
func (*RefType) typeOrExpr()         {}
func (*MutType) aNode()              {}
func (*MutType) typeNode()           {}
func (*MutType) typeOrExpr()         {}
func (*MutSliceType) aNode()         {}
func (*MutSliceType) typeNode()      {}
func (*MutSliceType) typeOrExpr()    {}
func (*OptionalType) aNode()         {}
func (*OptionalType) typeNode()      {}
func (*OptionalType) typeOrExpr()    {}
func (*RawPtrType) aNode()           {}
func (*RawPtrType) typeNode()        {}
func (*RawPtrType) typeOrExpr()      {}
func (*SliceType) aNode()            {}
func (*SliceType) typeNode()         {}
func (*SliceType) typeOrExpr()       {}
func (*ArrayType) aNode()            {}
func (*ArrayType) typeNode()         {}
func (*ArrayType) typeOrExpr()       {}
func (*FnType) aNode()               {}
func (*FnType) typeNode()            {}
func (*FnType) typeOrExpr()          {}
func (*RecordType) aNode()           {}
func (*RecordType) typeNode()        {}
func (*RecordType) typeOrExpr()      {}
func (*ParenType) aNode()            {}
func (*ParenType) typeNode()         {}
func (*ParenType) typeOrExpr()       {}
func (*PathType) aNode()             {}
func (*PathType) typeNode()          {}
func (*PathType) typeOrExpr()        {}
func (*BadType) aNode()              {}
func (*BadType) typeNode()           {}
func (*BadType) typeOrExpr()         {}
func (*Ident) aNode()                {}
func (*Ident) exprNode()             {}
func (*Ident) typeOrExpr()           {}
func (*BasicLit) aNode()             {}
func (*BasicLit) exprNode()          {}
func (*BasicLit) typeOrExpr()        {}
func (*ElseExpr) aNode()             {}
func (*ElseExpr) exprNode()          {}
func (*ElseExpr) typeOrExpr()        {}
func (*BinaryExpr) aNode()           {}
func (*BinaryExpr) exprNode()        {}
func (*BinaryExpr) typeOrExpr()      {}
func (*UnaryExpr) aNode()            {}
func (*UnaryExpr) exprNode()         {}
func (*UnaryExpr) typeOrExpr()       {}
func (*FieldExpr) aNode()            {}
func (*FieldExpr) exprNode()         {}
func (*FieldExpr) typeOrExpr()       {}
func (*GoExpr) aNode()               {}
func (*GoExpr) exprNode()            {}
func (*GoExpr) typeOrExpr()          {}
func (*IndexExpr) aNode()            {}
func (*IndexExpr) exprNode()         {}
func (*IndexExpr) typeOrExpr()       {}
func (*SliceExpr) aNode()            {}
func (*SliceExpr) exprNode()         {}
func (*SliceExpr) typeOrExpr()       {}
func (*CallExpr) aNode()             {}
func (*CallExpr) exprNode()          {}
func (*CallExpr) typeOrExpr()        {}
func (*ParenExpr) aNode()            {}
func (*ParenExpr) exprNode()         {}
func (*ParenExpr) typeOrExpr()       {}
func (*RecordLit) aNode()            {}
func (*RecordLit) exprNode()         {}
func (*RecordLit) typeOrExpr()       {}
func (*ArrayLit) aNode()             {}
func (*ArrayLit) exprNode()          {}
func (*ArrayLit) typeOrExpr()        {}
func (*RepeatLit) aNode()            {}
func (*RepeatLit) exprNode()         {}
func (*RepeatLit) typeOrExpr()       {}
func (*FnLit) aNode()                {}
func (*FnLit) exprNode()             {}
func (*FnLit) typeOrExpr()           {}
func (*QuoteExpr) aNode()            {}
func (*QuoteExpr) exprNode()         {}
func (*QuoteExpr) typeOrExpr()       {}
func (*BadExpr) aNode()              {}
func (*BadExpr) exprNode()           {}
func (*BadExpr) typeOrExpr()         {}
func (*BindStmt) aNode()             {}
func (*BindStmt) stmtNode()          {}
func (*DestructureStmt) aNode()      {}
func (*DestructureStmt) stmtNode()   {}
func (*DiscardStmt) aNode()          {}
func (*DiscardStmt) stmtNode()       {}
func (*AssignStmt) aNode()           {}
func (*AssignStmt) stmtNode()        {}
func (*IfStmt) aNode()               {}
func (*IfStmt) stmtNode()            {}
func (*IfStmt) exprNode()            {}
func (*IfStmt) typeOrExpr()          {}
func (*ForStmt) aNode()              {}
func (*ForStmt) stmtNode()           {}
func (*MatchStmt) aNode()            {}
func (*MatchStmt) stmtNode()         {}
func (*MatchStmt) exprNode()         {}
func (*MatchStmt) typeOrExpr()       {}
func (*ReturnStmt) aNode()           {}
func (*ReturnStmt) stmtNode()        {}
func (*FailStmt) aNode()             {}
func (*FailStmt) stmtNode()          {}
func (*BreakStmt) aNode()            {}
func (*BreakStmt) stmtNode()         {}
func (*ContinueStmt) aNode()         {}
func (*ContinueStmt) stmtNode()      {}
func (*DeferStmt) aNode()            {}
func (*DeferStmt) stmtNode()         {}
func (*ScopeStmt) aNode()            {}
func (*ScopeStmt) stmtNode()         {}
func (*UnsafeStmt) aNode()           {}
func (*UnsafeStmt) stmtNode()        {}
func (*AsmStmt) aNode()              {}
func (*AsmStmt) stmtNode()           {}
func (*BecomeStmt) aNode()           {}
func (*BecomeStmt) stmtNode()        {}
func (*ExprStmt) aNode()             {}
func (*ExprStmt) stmtNode()          {}
func (*BadStmt) aNode()              {}
func (*BadStmt) stmtNode()           {}
func (*AltPattern) aNode()           {}
func (*AltPattern) patNode()         {}
func (*VariantPattern) aNode()       {}
func (*VariantPattern) patNode()     {}
func (*PathPattern) aNode()          {}
func (*PathPattern) patNode()        {}
func (*LiteralPattern) aNode()       {}
func (*LiteralPattern) patNode()     {}
func (*WildcardPattern) aNode()      {}
func (*WildcardPattern) patNode()    {}
func (*SomePattern) aNode()          {}
func (*SomePattern) patNode()        {}
func (*NonePattern) aNode()          {}
func (*NonePattern) patNode()        {}
func (*OkPattern) aNode()            {}
func (*OkPattern) patNode()          {}
func (*ErrPattern) aNode()           {}
func (*ErrPattern) patNode()         {}
func (*NamePattern) aNode()          {}
func (*NamePattern) patNode()        {}
func (*RangePattern) aNode()         {}
func (*RangePattern) patNode()       {}
func (*BadPattern) aNode()           {}
func (*BadPattern) patNode()         {}
func (*FromFlow) aNode()             {}
func (*FromFlow) flowNode()          {}
func (*EndsFlow) aNode()             {}
func (*EndsFlow) flowNode()          {}
func (*StoreFlow) aNode()            {}
func (*StoreFlow) flowNode()         {}
func (*FieldMember) aNode()          {}
func (*FieldMember) memberNode()     {}
func (*InvariantMember) aNode()      {}
func (*InvariantMember) memberNode() {}
func (*File) aNode()                 {}
func (*Path) aNode()                 {}
func (*Packed) aNode()               {}
func (*Variant) aNode()              {}
func (*Field) aNode()                {}
func (*Signature) aNode()            {}
func (*FuncName) aNode()             {}
func (*SelfParam) aNode()            {}
func (*Param) aNode()                {}
func (*Source) aNode()               {}
func (*FieldSrc) aNode()             {}
func (*Contract) aNode()             {}
func (*TypeParams) aNode()           {}
func (*TypeParam) aNode()            {}
func (*TypeArgs) aNode()             {}
func (*FnParam) aNode()              {}
func (*Block) aNode()                {}
func (*BindCond) aNode()             {}
func (*IsCond) aNode()               {}
func (*InHead) aNode()               {}
func (*RangeSource) aNode()          {}
func (*RefSource) aNode()            {}
func (*FieldsSource) aNode()         {}
func (*Arm) aNode()                  {}
func (*RecordPat) aNode()            {}
func (*PatField) aNode()             {}
func (*Arg) aNode()                  {}
func (*RecField) aNode()             {}
func (*Splice) aNode()               {}
func (*Aligned) aNode()              {}
func (*AsmOperand) aNode()           {}

// AllNodeTypes names every node type, for coverage tests.
var AllNodeTypes = []string{
	"Import", "GenItem", "TestItem", "StructDecl", "EnumDecl", "ErrorDecl",
	"InterfaceDecl", "ImplDecl", "AliasDecl", "ConstDecl", "GlobalDecl", "ExternDecl",
	"FuncDecl", "BadItem", "ErrorType", "UnionType", "RefType", "MutType",
	"MutSliceType", "OptionalType", "RawPtrType", "SliceType", "ArrayType", "FnType",
	"RecordType", "ParenType", "PathType", "BadType", "Ident", "BasicLit",
	"ElseExpr", "BinaryExpr", "UnaryExpr", "FieldExpr", "GoExpr", "IndexExpr",
	"SliceExpr", "CallExpr", "ParenExpr", "RecordLit", "ArrayLit", "RepeatLit",
	"FnLit", "QuoteExpr", "BadExpr", "BindStmt", "DestructureStmt", "DiscardStmt",
	"AssignStmt", "IfStmt", "ForStmt", "MatchStmt", "ReturnStmt", "FailStmt",
	"BreakStmt", "ContinueStmt", "DeferStmt", "ScopeStmt", "UnsafeStmt", "ExprStmt",
	"BadStmt", "AltPattern", "VariantPattern", "PathPattern", "LiteralPattern", "WildcardPattern",
	"SomePattern", "NonePattern", "OkPattern", "ErrPattern", "NamePattern", "BadPattern",
	"FromFlow", "EndsFlow", "StoreFlow", "FieldMember", "InvariantMember", "File",
	"Path", "Packed", "Variant", "Field", "Signature", "FuncName",
	"SelfParam", "Param", "Source", "Contract", "TypeParams", "TypeParam",
	"TypeArgs", "FnParam", "Block", "BindCond", "IsCond", "InHead",
	"RangeSource", "RefSource", "FieldsSource", "Arm", "RecordPat", "PatField",
	"Arg", "RecField", "Splice",
	// The systems forms.
	"Aligned", "AsmStmt", "AsmOperand",
	// Per-field from clauses.
	"FieldSrc",
	// Tail calls and range patterns.
	"BecomeStmt", "RangePattern",
}
