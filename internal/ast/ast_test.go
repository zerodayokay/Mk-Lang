// Why: the AST must match the language documents: the keyword list of
// doc/reference.md and a node type for every production of doc/grammar.md.
// What: TestKeywordTableMatchesReference, TestNodeKindsCoverGrammar,
// TestEveryNodeTypeIsListed, TestPrintGolden, TestTreeValidate and others.
// How: reads both documents and compares them with the token and node
// tables; builds one tree holding every node type (everyNode) for the walk,
// printer and validator tests.

package ast

import (
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"mk/internal/source"
)

var update = flag.Bool("update", false, "rewrite golden files")

func readDoc(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "doc", name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(raw), "\r\n", "\n")
}

// TestKeywordTableMatchesReference: Keyword accepts exactly the reference's
// keywords, rejects the contextual words and fields, and stays within the
// budget of 45.
func TestKeywordTableMatchesReference(t *testing.T) {
	ref := readDoc(t, "reference.md")
	m := regexp.MustCompile("(?m)^Keywords: `([^`]*)`\\. Contextual: `([^`]*)`").FindStringSubmatch(ref)
	if m == nil {
		t.Fatal("keyword line not found in doc/reference.md")
	}
	words := strings.Fields(m[1])
	if len(words) != 39 || len(words) > 45 { // 39 with asm and become
		t.Errorf("%d keywords", len(words))
	}
	for _, w := range words {
		k, ok := Keyword(w)
		if !ok || k.FixedText() != w || !k.IsKeyword() {
			t.Errorf("keyword %q -> %v %v", w, k, ok)
		}
	}
	n := 0
	for k := TokenKind(0); k < numTokenKinds; k++ {
		if k.IsKeyword() {
			n++
		}
	}
	if n != len(words) {
		t.Errorf("%d keyword kinds, reference has %d", n, len(words))
	}
	for _, w := range append(strings.Fields(m[2]), "fields", "let", "fn_", "Fn") {
		if _, ok := Keyword(w); ok {
			t.Errorf("%q must not be a keyword", w)
		}
	}
}

// TestTokenKindsAreComplete walks every kind through String and FixedText.
func TestTokenKindsAreComplete(t *testing.T) {
	seen := map[string]TokenKind{}
	for k := TokenKind(1); k < numTokenKinds; k++ {
		s := k.String()
		if s == "" || strings.HasPrefix(s, "TokenKind(") || s == `""` {
			t.Errorf("kind %d has no name", k)
		}
		if o, dup := seen[s]; dup {
			t.Errorf("kinds %d and %d both print %s", o, k, s)
		}
		seen[s] = k
	}
	for k, want := range map[TokenKind]bool{TokIdent: true, TokInt: true, TokStr: true, TokRParen: true,
		TokRBracket: true, TokRBrace: true, TokReturn: true, TokBreak: true, TokContinue: true,
		TokPlus: false, TokComma: false, TokLBrace: false, TokUnderscore: false, TokMut: false} {
		if k.CanEndStatement() != want {
			t.Errorf("%v CanEndStatement = %v", k, !want)
		}
	}
	if !TokComment.IsTrivia() || TokTerm.IsTrivia() || !TokCaretEq.IsAssignOp() || !TokLifetime.IsHabit() {
		t.Error("class predicates")
	}
}

// grammarNodes maps every production of doc/grammar.md to the node
// type that represents it, or to "" when it makes no node: a pure
// alternative, a list folded into its parent, or a token class.
var grammarNodes = map[string]string{
	"File": "File", "Item": "", "Import": "Import", "Path": "Path", "Gen": "GenItem", "Test": "TestItem",
	"Decl": "", "Struct": "StructDecl", "Packed": "Packed", "Member": "FieldMember InvariantMember",
	"Enum": "EnumDecl", "Variant": "Variant", "Fields": "", "Field": "Field", "ErrorDecl": "ErrorDecl",
	"Interface": "InterfaceDecl", "Impl": "ImplDecl", "Alias": "AliasDecl", "Const": "ConstDecl",
	"Global": "GlobalDecl", "Extern": "ExternDecl", "Func": "FuncDecl", "Signature": "Signature",
	"FuncName": "FuncName", "Params": "", "SelfParam": "SelfParam", "Param": "Param", "ParamFlow": "",
	"Flow": "FromFlow EndsFlow StoreFlow", "Sources": "", "Source": "Source", "Contract": "Contract",
	"TypeParams": "TypeParams", "TypeParam": "TypeParam", "Bounds": "", "TypeArgs": "TypeArgs",
	"Type": "", "ErrorType": "ErrorType", "UnionType": "UnionType",
	"PrefixType": "RefType MutType MutSliceType OptionalType RawPtrType SliceType ArrayType FnType ParenType PathType",
	"FnParams":   "", "FnParam": "FnParam", "Record": "RecordType", "TypeOrExpr": "",
	"Block": "Block", "Stmt": "ExprStmt BreakStmt ContinueStmt ForStmt", "Bind": "BindStmt DestructureStmt DiscardStmt",
	"RecordPat": "RecordPat", "ArrayPat": "RecordPat", "PatField": "PatField", "Assign": "AssignStmt", "AssignOp": "",
	"If": "IfStmt", "Cond": "BindCond IsCond", "For": "ForStmt", "ForHead": "InHead RecordPat",
	"ForSource": "RangeSource RefSource FieldsSource", "Match": "MatchStmt", "Arm": "Arm",
	"Pattern":   "AltPattern",
	"Alt":       "VariantPattern PathPattern LiteralPattern WildcardPattern SomePattern NonePattern OkPattern ErrPattern NamePattern RangePattern",
	"PatFields": "", "Return": "ReturnStmt", "Fail": "FailStmt", "Defer": "DeferStmt", "Scope": "ScopeStmt",
	"Unsafe": "UnsafeStmt", "Expr": "", "Fallback": "ElseExpr", "Or": "BinaryExpr", "And": "BinaryExpr",
	"Compare": "BinaryExpr", "CompareOp": "", "Add": "BinaryExpr", "AddOp": "", "Mul": "BinaryExpr",
	"MulOp": "", "Unary": "UnaryExpr", "UnaryOp": "", "Postfix": "",
	"Suffix": "FieldExpr GoExpr IndexExpr SliceExpr CallExpr", "Args": "", "Arg": "Arg",
	"Primary": "Ident BasicLit ParenExpr", "RecordLit": "RecordLit", "RecField": "RecField",
	"ArrayLit": "ArrayLit RepeatLit", "FnLit": "FnLit", "Quote": "QuoteExpr Splice",
	// Cpu and Section are BasicLit fields of their parents, and
	// AsmDir is AsmOperand.Kind.
	"Aligned": "Aligned", "Cpu": "", "Section": "", "Asm": "AsmStmt", "AsmOperand": "AsmOperand", "AsmDir": "",
	// Value is a position (a value arm); an IfStmt or MatchStmt stands
	// there as an Expr, and a ForHead may hold a RecordPat.
	"Value": "IfStmt MatchStmt",
	// The per-field clause, `from (name: src)`.
	"FieldSrc": "FieldSrc",
	// become; a range is an Alt alternative.
	"Become": "BecomeStmt",
}

// TestNodeKindsCoverGrammar reads every production of doc/grammar.md
// and checks it is mapped, and that every mapped node type exists.
func TestNodeKindsCoverGrammar(t *testing.T) {
	g := readDoc(t, "grammar.md")
	start := strings.Index(g, "```ebnf")
	end := strings.Index(g[start+1:], "```")
	if start < 0 || end < 0 {
		t.Fatal("ebnf block not found")
	}
	prods := regexp.MustCompile(`(?m)^([A-Z][A-Za-z]*)\s+=`).FindAllStringSubmatch(g[start:start+1+end], -1)
	if len(prods) < 80 {
		t.Fatalf("only %d productions found", len(prods))
	}
	types := map[string]bool{}
	for _, n := range AllNodeTypes {
		types[n] = true
	}
	for _, p := range prods {
		nodes, ok := grammarNodes[p[1]]
		if !ok {
			t.Errorf("production %s has no node mapping", p[1])
			continue
		}
		for _, n := range strings.Fields(nodes) {
			if !types[n] {
				t.Errorf("production %s maps to unknown node %s", p[1], n)
			}
		}
	}
}

// builder hands out NodeIDs in creation order.
type builder struct{ next NodeID }

func (b *builder) base() Base { b.next++; return Base{NID: b.next} }

// everyNode builds one tree that holds every node type at least once.
func everyNode() *File {
	var b builder
	id := func(s source.Symbol) *Ident { return &Ident{Base: b.base(), Name: s} }
	lit := func(k TokenKind, raw string) *BasicLit { return &BasicLit{Base: b.base(), Kind: k, Raw: raw} }
	path := func(s source.Symbol) *Path { return &Path{Base: b.base(), Names: []*Ident{id(s)}} }
	pt := func(s source.Symbol) *PathType { return &PathType{Base: b.base(), Path: path(s)} }
	blk := func(ss ...Stmt) *Block { return &Block{Base: b.base(), Stmts: ss} }
	x := func() Expr { return id(source.SymSelf) }
	f := &File{Base: b.base(), Path: "every.mk"}
	sig := &Signature{Base: b.base(), Name: &FuncName{Base: b.base(), Owner: id(source.SymMain),
		OwnerArgs: &TypeArgs{Base: b.base(), Args: []TypeOrExpr{pt(source.SymInt)}}, Name: id(source.SymLen)},
		TypeParams: &TypeParams{Base: b.base(), Params: []*TypeParam{
			{Base: b.base(), Name: id(source.SymSelfType), Bounds: []Type{pt(source.SymPlain)}},
			{Base: b.base(), Const: true, Name: id(source.SymFuel), Type: pt(source.SymInt)}}},
		Self:   &SelfParam{Base: b.base(), Mode: ByMut, Into: id(source.SymFrom)},
		Params: []*Param{{Base: b.base(), Var: true, Name: id(source.SymFrom), Type: pt(source.SymInt), Into: id(source.SymSelf)}},
		Result: &ErrorType{Base: b.base(), Errors: &UnionType{Base: b.base(), Types: []Type{pt(source.SymOk), pt(source.SymErr)}},
			Ok: &RefType{Base: b.base(), Elem: &MutType{Base: b.base(), Elem: &MutSliceType{Base: b.base(),
				Elem: &OptionalType{Base: b.base(), Elem: &RawPtrType{Base: b.base(), Elem: &SliceType{Base: b.base(),
					Elem: &ArrayType{Base: b.base(), Len: lit(TokInt, "4"), Elem: &ParenType{Base: b.base(),
						Type: &FnType{Base: b.base(), Params: []*FnParam{{Base: b.base(), Name: id(source.SymInto), Type: &RecordType{Base: b.base(),
							Fields: []*Field{{Base: b.base(), Name: id(source.SymEnds), Type: &BadType{Base: b.base()}}}}}},
							Flows: []Flow{&EndsFlow{Base: b.base(), Name: id(source.SymSelf)}}}}}}}}}}}},
		Flows: []Flow{&FromFlow{Base: b.base(), Sources: []*Source{{Base: b.base(), Names: []*Ident{id(source.SymSelf)}}}},
			&StoreFlow{Base: b.base(), From: id(source.SymSelf), Into: id(source.SymSelf)}},
		Contracts: []*Contract{{Base: b.base(), Kind: Ensures, X: x()}},
	}
	pats := &AltPattern{Base: b.base(), Alts: []Pattern{
		&VariantPattern{Base: b.base(), Name: id(source.SymSome), HasFields: true, Fields: []*PatField{{Base: b.base(), Name: id(source.SymOk), Pat: &WildcardPattern{Base: b.base()}}}},
		&PathPattern{Base: b.base(), Path: path(source.SymErr)},
		&LiteralPattern{Base: b.base(), Neg: true, Lit: lit(TokInt, "1")},
		&SomePattern{Base: b.base(), Pat: &NonePattern{Base: b.base()}},
		&OkPattern{Base: b.base(), Pat: &ErrPattern{Base: b.base(), Pat: &NamePattern{Base: b.base(), Name: id(source.SymUnderscore)}}},
		&BadPattern{Base: b.base()},
	}}
	body := blk(
		&BindStmt{Base: b.base(), Var: true, Name: id(source.SymTrue), Type: pt(source.SymInt), Value: x()},
		&DestructureStmt{Base: b.base(), Pat: &RecordPat{Base: b.base(), Path: path(source.SymMain), Rest: true,
			Fields: []*PatField{{Base: b.base(), Name: id(source.SymFalse)}}}, Value: x(), Reinterpreted: true},
		&DiscardStmt{Base: b.base(), Value: x()},
		&AssignStmt{Base: b.base(), Target: x(), Op: TokPlusWrapEq, Value: x()},
		&IfStmt{Base: b.base(), Cond: &BindCond{Base: b.base(), Name: id(source.SymSome), Value: x()}, Then: blk(),
			Else: &IfStmt{Base: b.base(), Cond: &IsCond{Base: b.base(), Name: id(source.SymSelfType), Type: pt(source.SymU8)}, Then: blk(), Else: blk()}},
		&ForStmt{Base: b.base(), Label: id(source.SymMain), Head: &InHead{Base: b.base(), Key: id(source.SymItems), Value: id(source.SymNext),
			Source: &RangeSource{Base: b.base(), Lo: x(), Hi: x()}}, Body: blk(
			&ForStmt{Base: b.base(), Head: &InHead{Base: b.base(), Key: id(source.SymLen), Source: &RefSource{Base: b.base(), Mode: ByMutRef, X: x()}}, Body: blk()},
			&ForStmt{Base: b.base(), Head: &InHead{Base: b.base(), Key: id(source.SymLen), Source: &FieldsSource{Base: b.base(), X: x()}}, Body: blk(
				&BreakStmt{Base: b.base(), Label: id(source.SymMain)}, &ContinueStmt{Base: b.base()})},
			// for i, (a, b) in e, and a value if on a binding's right side.
			&ForStmt{Base: b.base(), Head: &InHead{Base: b.base(), Key: id(source.SymLen),
				Pat:    &RecordPat{Base: b.base(), Fields: []*PatField{{Base: b.base(), Name: id(source.SymFalse)}}},
				Source: &RefSource{Base: b.base(), X: x()}}, Body: blk()},
			&BindStmt{Base: b.base(), Name: id(source.SymTrue), Value: &IfStmt{Base: b.base(), Cond: x(), Then: blk(), Else: blk()}},
		)},
		&MatchStmt{Base: b.base(), X: x(), Arms: []*Arm{{Base: b.base(), Pat: pats, Body: blk()}}},
		&DeferStmt{Base: b.base(), Onerr: true, X: x()},
		&ScopeStmt{Base: b.base(), Try: true, Name: id(source.SymSelf), Value: x(), Body: blk(
			&ExprStmt{Base: b.base(), X: &GoExpr{Base: b.base(), Scope: x(), Call: &CallExpr{Base: b.base(), Fun: x(),
				Args: []*Arg{{Base: b.base(), Name: id(source.SymOk), Value: x()}}}}})},
		&UnsafeStmt{Base: b.base(), Reason: lit(TokStr, `"why"`), Body: blk(&BadStmt{Base: b.base()})},
		&ExprStmt{Base: b.base(), X: &ElseExpr{Base: b.base(), Name: id(source.SymErr), Handler: blk(&FailStmt{Base: b.base(), Value: x()}),
			X: &BinaryExpr{Base: b.base(), Op: TokAmpAmp, X: &UnaryExpr{Base: b.base(), Op: TokTry, X: &FieldExpr{Base: b.base(), X: x(), Name: id(source.SymLen)}},
				Y: &IndexExpr{Base: b.base(), X: &SliceExpr{Base: b.base(), X: x(), Lo: x()}, Args: []TypeOrExpr{pt(source.SymInt), x()}}}}},
		&ExprStmt{Base: b.base(), X: &ParenExpr{Base: b.base(), X: &RecordLit{Base: b.base(), Fields: []*RecField{{Base: b.base(), Name: id(source.SymOk)}}}}},
		&ExprStmt{Base: b.base(), X: &ArrayLit{Base: b.base(), Elems: []Expr{&RepeatLit{Base: b.base(), Value: x(), Count: lit(TokInt, "8")}, &BadExpr{Base: b.base()}}}},
		&ExprStmt{Base: b.base(), X: &FnLit{Base: b.base(), Params: []*Param{{Base: b.base(), Name: id(source.SymFrom), Type: pt(source.SymInt)}}, Body: blk(
			&ReturnStmt{Base: b.base(), Value: &QuoteExpr{Base: b.base(), Splices: []*Splice{{Base: b.base(), X: x()}}}})}},
	)
	f.Items = []Item{
		&Import{Base: b.base(), Alias: id(source.SymMain), Path: path(source.SymMain)},
		&GenItem{Base: b.base(), Generator: path(source.SymMain), Args: []TypeOrExpr{pt(source.SymInt)}, Fuel: lit(TokInt, "10")},
		&TestItem{Base: b.base(), Name: lit(TokStr, `"t"`), Param: &Param{Base: b.base(), Name: id(source.SymSelf), Type: pt(source.SymInt)}, Body: blk()},
		&StructDecl{Base: b.base(), ItemInfo: ItemInfo{Export: true}, Linear: true, Packed: &Packed{Base: b.base(), Align: lit(TokInt, "2")},
			Name: id(source.SymMain), Members: []Member{&FieldMember{Base: b.base(), Export: true, Name: id(source.SymLen), Type: pt(source.SymInt)},
				&InvariantMember{Base: b.base(), X: x()}}},
		&EnumDecl{Base: b.base(), Name: id(source.SymMain), Tag: pt(source.SymU8), Variants: []*Variant{{Base: b.base(), Name: id(source.SymOk), Value: lit(TokInt, "1")}}},
		&ErrorDecl{Base: b.base(), Name: id(source.SymMain)},
		&InterfaceDecl{Base: b.base(), Name: id(source.SymMain), Methods: []*Signature{sig}},
		&ImplDecl{Base: b.base(), Unsafe: true, Interface: pt(source.SymShare), For: pt(source.SymMain),
			Methods: []*FuncDecl{{Base: b.base(), Sig: &Signature{Base: b.base(), Name: &FuncName{Base: b.base(), Name: id(source.SymLen)}}, Body: body}}},
		&AliasDecl{Base: b.base(), Name: id(source.SymMain), Type: pt(source.SymInt)},
		&ConstDecl{Base: b.base(), Name: id(source.SymMain), Value: x(), Fuel: lit(TokInt, "1")},
		&GlobalDecl{Base: b.base(), Name: id(source.SymMain), Type: pt(source.SymInt), Value: x()},
		&ExternDecl{Base: b.base(), Sig: &Signature{Base: b.base(), Name: &FuncName{Base: b.base(), Name: id(source.SymMain)}}},
		&BadItem{Base: b.base()},
	}
	// align, cpu, section, naked, extern var and asm.
	asmOp := func(k AsmOperandKind) *AsmOperand { return &AsmOperand{Base: b.base(), Kind: k} }
	in := asmOp(AsmIn)
	in.Label, in.Reg, in.Value = id(source.SymFrom), id(source.SymInt), x()
	out := asmOp(AsmLateOut)
	out.Reg, out.Discard = lit(TokStr, `"rax"`), true
	cst := asmOp(AsmConst)
	cst.Value = lit(TokInt, "3")
	sym := asmOp(AsmSym)
	sym.Sym = path(source.SymMain)
	abi := asmOp(AsmClobberABI)
	abi.ABI = lit(TokStr, `"C"`)
	opts := asmOp(AsmOptions)
	opts.Options = []*Ident{id(source.SymInto), id(source.SymEnds)}
	f.Items = append(f.Items,
		&StructDecl{Base: b.base(), Align: &Aligned{Base: b.base(), Align: lit(TokInt, "16")}, Name: id(source.SymMain)},
		&FuncDecl{Base: b.base(), Naked: true, Sig: &Signature{Base: b.base(), Name: &FuncName{Base: b.base(), Name: id(source.SymMain)},
			Cpu: lit(TokStr, `"avx2"`), Section: lit(TokStr, `".text.boot"`)},
			Body: blk(&UnsafeStmt{Base: b.base(), Reason: lit(TokStr, `"asm"`), Body: blk(
				&AsmStmt{Base: b.base(), Template: lit(TokStr, `"mov {from}, 1"`), Operands: []*AsmOperand{in, out, cst, sym, abi, opts}})})},
		&ExternDecl{Base: b.base(), Naked: true, Sig: &Signature{Base: b.base(), Name: &FuncName{Base: b.base(), Name: id(source.SymMain)}}},
		&ConstDecl{Base: b.base(), Name: id(source.SymMain), Value: x(), Section: lit(TokStr, `".rodata"`)},
		&GlobalDecl{Base: b.base(), Extern: true, Name: id(source.SymMain), Type: pt(source.SymInt)},
	)
	// A per-field clause on a signature and on a fn type.
	src := func(names ...source.Symbol) *Source {
		s := &Source{Base: b.base()}
		for _, n := range names {
			s.Names = append(s.Names, id(n))
		}
		return s
	}
	fsrc := func(field source.Symbol, srcs ...*Source) *FieldSrc {
		return &FieldSrc{Base: b.base(), Name: id(field), Sources: srcs}
	}
	f.Items = append(f.Items,
		&ExternDecl{Base: b.base(), Sig: &Signature{Base: b.base(), Name: &FuncName{Base: b.base(), Name: id(source.SymMain)},
			Params: []*Param{{Base: b.base(), Name: id(source.SymFrom), Type: &FnType{Base: b.base(),
				Params: []*FnParam{{Base: b.base(), Name: id(source.SymInto), Type: pt(source.SymInt)}},
				Result: pt(source.SymMain),
				Flows:  []Flow{&FromFlow{Base: b.base(), Fields: []*FieldSrc{fsrc(source.SymOk, src(source.SymInto))}}}}}},
			Result: pt(source.SymMain),
			Flows: []Flow{&FromFlow{Base: b.base(), Fields: []*FieldSrc{
				fsrc(source.SymOk, src(source.SymFrom), src(source.SymSelf, source.SymLen)),
				fsrc(source.SymErr, src(source.SymSelf))}}}}},
	)
	// Range patterns, closed and open at either end, and become.
	lp := func(neg bool, k TokenKind, raw string) *LiteralPattern {
		return &LiteralPattern{Base: b.base(), Neg: neg, Lit: lit(k, raw)}
	}
	f.Items = append(f.Items,
		&FuncDecl{Base: b.base(), Sig: &Signature{Base: b.base(), Name: &FuncName{Base: b.base(), Name: id(source.SymMain)}},
			Body: blk(
				&MatchStmt{Base: b.base(), X: x(), Arms: []*Arm{
					{Base: b.base(), Pat: &RangePattern{Base: b.base(), Lo: lp(true, TokInt, "8"), Hi: lp(false, TokInt, "8")}, Body: blk()},
					{Base: b.base(), Pat: &RangePattern{Base: b.base(), Lo: lp(false, TokChar, `'a'`)}, Body: blk()},
					{Base: b.base(), Pat: &RangePattern{Base: b.base(), Hi: lp(false, TokInt, "200")}, Body: blk()}}},
				&BecomeStmt{Base: b.base(), Call: &CallExpr{Base: b.base(), Fun: x(),
					Args: []*Arg{{Base: b.base(), Value: x()}}}})},
	)
	f.NodeCount = uint32(b.next)
	return f
}

// TestAlignedAndPackedInSourceOrder: EachChild gives a struct's Packed
// and Aligned in source order whichever comes first, so ast.Validate's
// sibling order holds for both spellings of the grammar's Struct.
func TestAlignedAndPackedInSourceOrder(t *testing.T) {
	sp := func(a, b uint32) Base { return Base{Sp: source.Span{File: 1, Start: a, End: b}} }
	name := &Ident{Base: sp(30, 31)}
	for _, tc := range []struct {
		alignAt, packedAt uint32
		want              []string
	}{{0, 10, []string{"Aligned", "Packed", "Ident"}}, {10, 0, []string{"Packed", "Aligned", "Ident"}}} {
		d := &StructDecl{Base: sp(0, 40), Name: name,
			Align:  &Aligned{Base: sp(tc.alignAt, tc.alignAt+8)},
			Packed: &Packed{Base: sp(tc.packedAt, tc.packedAt+6)}}
		var got []string
		EachChild(d, func(c Node) { n, _ := Describe(c, nil); got = append(got, n) })
		if strings.Join(got, " ") != strings.Join(tc.want, " ") {
			t.Errorf("align at %d, packed at %d: children %v, want %v", tc.alignAt, tc.packedAt, got, tc.want)
		}
	}
	if k, ok := Keyword("asm"); !ok || k != TokAsm || !TokAsm.IsKeyword() || TokAsm.FixedText() != "asm" || TokAsm.IsHabit() {
		t.Errorf("TokAsm: Keyword %v %v, IsKeyword %v", k, ok, TokAsm.IsKeyword())
	}
	if LastKeyword != TokInvariant || TokAsm != TokLifetime+1 {
		t.Error("TokAsm must be appended after TokLifetime, past the 37 older keywords")
	}
}

// TestEveryNodeTypeIsListed: the tree above reaches every type in
// AllNodeTypes through EachChild, and every type Describe names is listed, so
// Walk, the printer and the list cannot fall out of step.
func TestEveryNodeTypeIsListed(t *testing.T) {
	f := everyNode()
	seen := map[string]int{}
	count := 0
	Inspect(f, func(n Node) bool {
		if n != nil {
			name, _ := Describe(n, nil)
			seen[name]++
			count++
		}
		return true
	})
	if uint32(count) != f.NodeCount {
		t.Errorf("walk saw %d nodes, built %d", count, f.NodeCount)
	}
	listed := map[string]bool{}
	for _, n := range AllNodeTypes {
		listed[n] = true
		if seen[n] == 0 {
			t.Errorf("node type %s not reached", n)
		}
	}
	var extra []string
	for n := range seen {
		if !listed[n] {
			extra = append(extra, n)
		}
	}
	sort.Strings(extra)
	if len(extra) > 0 {
		t.Errorf("not in AllNodeTypes: %v", extra)
	}
}

// TestPrintGolden pins the debug printer's output for the tree above.
func TestPrintGolden(t *testing.T) {
	got := Sprint(everyNode(), source.NewInterner())
	golden := filepath.Join("testdata", "every.golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got != strings.ReplaceAll(string(want), "\r\n", "\n") {
		t.Fatalf("printer output changed; rerun with -update and read the diff:\n%s", got)
	}
}

// smallFile is "const X = 1\n" tokenized and parsed by hand.
func smallFile(sm *source.SourceMap, in *source.Interner) (*File, string) {
	sf := sm.Add("small.mk", []byte("const X = 1\n"))
	fid := sf.ID
	toks := []Token{{TokConst, 0, 5}, {TokWhitespace, 5, 1}, {TokIdent, 6, 1}, {TokWhitespace, 7, 1},
		{TokAssign, 8, 1}, {TokWhitespace, 9, 1}, {TokInt, 10, 1}, {TokTerm, 11, 0}, {TokNewline, 11, 1}, {TokEOF, 12, 0}}
	sp := func(a, b uint32) source.Span { return source.Span{File: fid, Start: a, End: b} }
	f := &File{Base: Base{NID: 1, Sp: sp(0, 12)}, Path: "small.mk", Tokens: toks, NodeCount: 4,
		Items: []Item{&ConstDecl{Base: Base{NID: 2, Sp: sp(0, 11)}, ItemInfo: ItemInfo{Toks: TokRange{0, 7}},
			Name:  &Ident{Base: Base{NID: 3, Sp: sp(6, 7)}, Name: in.Intern("X")},
			Value: &BasicLit{Base: Base{NID: 4, Sp: sp(10, 11)}, Kind: TokInt, Raw: "1"}}}}
	return f, sf.Text
}

// TestTreeValidate accepts a well-formed file and rejects each broken form.
func TestTreeValidate(t *testing.T) {
	sm, in := source.NewSourceMap(), source.NewInterner()
	f, text := smallFile(sm, in)
	if err := Validate(f, text); err != nil {
		t.Fatalf("valid file rejected: %v", err)
	}
	if PrintBack(f.Tokens, text) != text {
		t.Fatal("print back")
	}
	breakers := map[string]func(f *File){
		"gap":            func(f *File) { f.Tokens[1].Len = 2 },
		"term has bytes": func(f *File) { f.Tokens[7].Len = 1; f.Tokens[8].Start = 12 },
		"no eof":         func(f *File) { f.Tokens = f.Tokens[:9] },
		"wrong spelling": func(f *File) { f.Tokens[4].Kind = TokColon },
		"duplicate id":   func(f *File) { f.Items[0].(*ConstDecl).Name.NID = 2 },
		"id out of range": func(f *File) {
			f.Items[0].(*ConstDecl).Value.(*BasicLit).NID = 9
		},
		"child outside parent": func(f *File) { f.Items[0].(*ConstDecl).Sp.End = 9 },
		"children out of order": func(f *File) {
			d := f.Items[0].(*ConstDecl)
			d.Name.Sp, d.Value.(*BasicLit).Sp = d.Value.(*BasicLit).Sp, d.Name.Sp
		},
	}
	for name, brk := range breakers {
		t.Run(name, func(t *testing.T) {
			g, text := smallFile(source.NewSourceMap(), in)
			brk(g)
			if err := Validate(g, text); err == nil {
				t.Fatal("not detected")
			}
		})
	}
}

func TestEachChildUnknownIsICE(t *testing.T) {
	if source.Guard("t", source.Span{}, func() { EachChild(nil, func(Node) {}) }) == nil {
		t.Fatal("nil node accepted")
	}
}
