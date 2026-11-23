// Why: parser tests and goldens need a stable, readable dump of a tree.
//
// What: Fprint and Sprint (the indented tree) and Describe (one node's name
// and attributes).
//
// How: a depth-first walk with one line per node. The output is
// deterministic: names go through the interner and spans print as byte
// offsets, never NodeIDs or pointers, so it serves goldens.

package ast

import (
	"io"
	"strconv"
	"strings"

	"mk/internal/source"
)

// Fprint writes the tree rooted at n, one node per line, children indented
// two spaces: "Kind [start,end) attributes". in spells Idents; a nil in prints
// symbol numbers instead (for tests that have no interner).
func Fprint(w io.Writer, n Node, in *source.Interner) error {
	var b strings.Builder
	fprint(&b, n, in, 0)
	_, err := io.WriteString(w, b.String())
	return err
}

// Sprint is Fprint into a string.
func Sprint(n Node, in *source.Interner) string {
	var b strings.Builder
	fprint(&b, n, in, 0)
	return b.String()
}

func fprint(b *strings.Builder, n Node, in *source.Interner, depth int) {
	for i := 0; i < depth; i++ {
		b.WriteString("  ")
	}
	name, attrs := Describe(n, in)
	b.WriteString(name)
	sp := n.Span()
	b.WriteString(" [")
	b.WriteString(strconv.FormatUint(uint64(sp.Start), 10))
	b.WriteByte(',')
	b.WriteString(strconv.FormatUint(uint64(sp.End), 10))
	b.WriteByte(')')
	if attrs != "" {
		b.WriteByte(' ')
		b.WriteString(attrs)
	}
	b.WriteByte('\n')
	EachChild(n, func(c Node) { fprint(b, c, in, depth+1) })
}

// Describe returns a node's type name and its scalar attributes (flags,
// operators, names, literal spellings), as the printer shows them.
func Describe(n Node, in *source.Interner) (name, attrs string) {
	var a []string
	flag := func(on bool, s string) {
		if on {
			a = append(a, s)
		}
	}
	info := func(i *ItemInfo) {
		flag(i.Export, "export")
	}
	switch n := n.(type) {
	case *File:
		name = "File"
		a = append(a, strconv.Quote(n.Path))
	case *Ident:
		name = "Ident"
		a = append(a, symText(in, n.Name))
	case *BasicLit:
		name = "BasicLit"
		a = append(a, n.Kind.String(), n.Raw)
	case *Path:
		name = "Path"
	case *Import:
		name = "Import"
		info(&n.ItemInfo)
	case *GenItem:
		name = "GenItem"
		info(&n.ItemInfo)
	case *TestItem:
		name = "TestItem"
		info(&n.ItemInfo)
	case *Packed:
		name = "Packed"
	case *Aligned:
		name = "Aligned"
	case *AsmStmt:
		name = "AsmStmt"
	case *AsmOperand:
		name = "AsmOperand"
		a = append(a, n.Kind.String())
		flag(n.Discard, "_")
	case *StructDecl:
		name = "StructDecl"
		info(&n.ItemInfo)
		flag(n.Linear, "linear")
		flag(n.MutView, "mut")
		flag(n.View, "view")
	case *FieldMember:
		name = "FieldMember"
		flag(n.Export, "export")
	case *InvariantMember:
		name = "InvariantMember"
	case *EnumDecl:
		name = "EnumDecl"
		info(&n.ItemInfo)
		flag(n.Open, "open")
	case *Variant:
		name = "Variant"
		flag(n.HasFields, "fields")
	case *Field:
		name = "Field"
	case *ErrorDecl:
		name = "ErrorDecl"
		info(&n.ItemInfo)
		flag(n.HasFields, "fields")
	case *InterfaceDecl:
		name = "InterfaceDecl"
		info(&n.ItemInfo)
	case *ImplDecl:
		name = "ImplDecl"
		info(&n.ItemInfo)
		flag(n.Unsafe, "unsafe")
	case *AliasDecl:
		name = "AliasDecl"
		info(&n.ItemInfo)
		flag(n.Open, "open")
	case *ConstDecl:
		name = "ConstDecl"
		info(&n.ItemInfo)
	case *GlobalDecl:
		name = "GlobalDecl"
		info(&n.ItemInfo)
		flag(n.Extern, "extern")
	case *ExternDecl:
		name = "ExternDecl"
		info(&n.ItemInfo)
		flag(n.Body == nil, "bodiless")
		flag(n.Naked, "naked")
	case *FuncDecl:
		name = "FuncDecl"
		info(&n.ItemInfo)
		flag(n.Body == nil, "bodiless")
		flag(n.Naked, "naked")
	case *BadItem:
		name = "BadItem"
		info(&n.ItemInfo)
	case *Signature:
		name = "Signature"
		flag(n.Ellipsis.File != source.NoFile, "variadic")
	case *FuncName:
		name = "FuncName"
	case *SelfParam:
		name = "SelfParam"
		a = append(a, [...]string{"self", "&self", "mut self"}[n.Mode%3])
	case *Param:
		name = "Param"
		flag(n.Const, "const")
		flag(n.Var, "var")
	case *FromFlow:
		name = "FromFlow"
	case *EndsFlow:
		name = "EndsFlow"
	case *StoreFlow:
		name = "StoreFlow"
	case *Source:
		name = "Source"
	case *FieldSrc:
		name = "FieldSrc"
	case *Contract:
		name = "Contract"
		a = append(a, [...]string{"requires", "ensures"}[n.Kind%2])
	case *TypeParams:
		name = "TypeParams"
	case *TypeParam:
		name = "TypeParam"
		flag(n.Const, "const")
	case *TypeArgs:
		name = "TypeArgs"
	case *ErrorType:
		name = "ErrorType"
	case *UnionType:
		name = "UnionType"
	case *RefType:
		name = "RefType"
	case *MutType:
		name = "MutType"
	case *MutSliceType:
		name = "MutSliceType"
	case *OptionalType:
		name = "OptionalType"
	case *RawPtrType:
		name = "RawPtrType"
	case *SliceType:
		name = "SliceType"
	case *ArrayType:
		name = "ArrayType"
	case *FnType:
		name = "FnType"
	case *FnParam:
		name = "FnParam"
	case *RecordType:
		name = "RecordType"
	case *ParenType:
		name = "ParenType"
	case *PathType:
		name = "PathType"
	case *BadType:
		name = "BadType"
		a = append(a, toks(n.Toks))
	case *Block:
		name = "Block"
	case *BindStmt:
		name = "BindStmt"
		flag(n.Var, "var")
	case *DestructureStmt:
		name = "DestructureStmt"
		flag(n.Var, "var")
		flag(n.Reinterpreted, "reinterpreted")
	case *DiscardStmt:
		name = "DiscardStmt"
	case *AssignStmt:
		name = "AssignStmt"
		a = append(a, n.Op.FixedText())
	case *IfStmt:
		name = "IfStmt"
	case *BindCond:
		name = "BindCond"
	case *IsCond:
		name = "IsCond"
	case *ForStmt:
		name = "ForStmt"
	case *InHead:
		name = "InHead"
	case *RangeSource:
		name = "RangeSource"
	case *RefSource:
		name = "RefSource"
		a = append(a, [...]string{"copy", "&", "mut"}[n.Mode%3])
	case *FieldsSource:
		name = "FieldsSource"
	case *MatchStmt:
		name = "MatchStmt"
	case *Arm:
		name = "Arm"
	case *ReturnStmt:
		name = "ReturnStmt"
	case *FailStmt:
		name = "FailStmt"
	case *BecomeStmt:
		name = "BecomeStmt"
	case *BreakStmt:
		name = "BreakStmt"
	case *ContinueStmt:
		name = "ContinueStmt"
	case *DeferStmt:
		name = "DeferStmt"
		if n.Onerr {
			a = append(a, "onerr")
		} else {
			a = append(a, "defer")
		}
	case *ScopeStmt:
		name = "ScopeStmt"
		flag(n.Try, "try")
	case *UnsafeStmt:
		name = "UnsafeStmt"
	case *ExprStmt:
		name = "ExprStmt"
	case *BadStmt:
		name = "BadStmt"
		a = append(a, toks(n.Toks))
	case *RecordPat:
		name = "RecordPat"
		flag(n.Rest, "rest")
	case *PatField:
		name = "PatField"
	case *AltPattern:
		name = "AltPattern"
	case *VariantPattern:
		name = "VariantPattern"
		flag(n.HasFields, "fields")
	case *PathPattern:
		name = "PathPattern"
		flag(n.HasFields, "fields")
	case *LiteralPattern:
		name = "LiteralPattern"
		flag(n.Neg, "neg")
	case *RangePattern:
		name = "RangePattern"
		flag(n.Lo == nil, "open-lo")
		flag(n.Hi == nil, "open-hi")
	case *WildcardPattern:
		name = "WildcardPattern"
	case *SomePattern:
		name = "SomePattern"
	case *NonePattern:
		name = "NonePattern"
	case *OkPattern:
		name = "OkPattern"
	case *ErrPattern:
		name = "ErrPattern"
	case *NamePattern:
		name = "NamePattern"
	case *BadPattern:
		name = "BadPattern"
		a = append(a, toks(n.Toks))
	case *ElseExpr:
		name = "ElseExpr"
	case *BinaryExpr:
		name = "BinaryExpr"
		a = append(a, n.Op.FixedText())
	case *UnaryExpr:
		name = "UnaryExpr"
		a = append(a, n.Op.FixedText())
	case *FieldExpr:
		name = "FieldExpr"
	case *GoExpr:
		name = "GoExpr"
	case *IndexExpr:
		name = "IndexExpr"
	case *SliceExpr:
		name = "SliceExpr"
	case *CallExpr:
		name = "CallExpr"
	case *Arg:
		name = "Arg"
	case *ParenExpr:
		name = "ParenExpr"
	case *RecordLit:
		name = "RecordLit"
	case *RecField:
		name = "RecField"
	case *ArrayLit:
		name = "ArrayLit"
	case *RepeatLit:
		name = "RepeatLit"
	case *FnLit:
		name = "FnLit"
	case *QuoteExpr:
		name = "QuoteExpr"
		a = append(a, toks(n.Toks))
	case *Splice:
		name = "Splice"
	case *BadExpr:
		name = "BadExpr"
		a = append(a, toks(n.Toks))
	default:
		var sp source.Span
		if n != nil {
			sp = n.Span()
		}
		source.ICEf(sp, "ast.Describe: unknown node %T", n)
	}
	return name, strings.Join(a, " ")
}

func symText(in *source.Interner, s source.Symbol) string {
	if in == nil {
		if p, ok := source.PredeclaredName(s); ok {
			return p
		}
		return "#" + strconv.FormatUint(uint64(s), 10)
	}
	return in.Str(s)
}

func toks(r TokRange) string {
	return "toks[" + strconv.FormatUint(uint64(r.First), 10) + "," + strconv.FormatUint(uint64(r.End), 10) + ")"
}
