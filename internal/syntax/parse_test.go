// Why: the parser is the grammar's single implementation; each production and
// each recovery path needs a pinned shape.
// What: tests of items, signatures and flows, types, expressions, statements,
// patterns, continuation lines and recovery, plus the table runner runCases.
// How: each pcase parses a source and compares the codes, a message and the
// tree shape with spans stripped; each test is named after the rule it pins.

package syntax

import (
	"regexp"
	"strings"
	"testing"

	"mk/internal/ast"
	"mk/internal/source"
)

var spanRE = regexp.MustCompile(` \[\d+,\d+\)`)

// shape is the tree without spans, so two layouts of one program compare.
func (r parsed) shape() string { return flat(spanRE.ReplaceAllString(r.tree(), "")) }

// deepShape is the tree without spans, indentation kept.
func (r parsed) deepShape() string { return spanRE.ReplaceAllString(r.tree(), "") }

// flat strips each line's indentation, so expectations name consecutive
// lines of the tree (a node and its first child) without counting depth.
func flat(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimLeft(l, " ")
	}
	return strings.Join(lines, "\n")
}

// pcase is one parse expectation: the diagnostic codes in order (space
// separated, "" for none), substrings of the span-free tree, and substrings
// of the first diagnostic's message.
type pcase struct {
	name  string
	src   string
	codes string
	tree  []string
	msg   string
	opts  Options
}

func runCases(t *testing.T, cases []pcase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := parse(t, c.src, c.opts)
			if got := r.codeList(); got != c.codes {
				t.Errorf("codes %q, want %q\n  %s\n%s", got, c.codes, strings.Join(r.codes(), "\n  "), c.src)
			}
			sh := r.shape()
			for _, want := range c.tree {
				if !strings.Contains(sh, flat(want)) {
					t.Errorf("tree lacks %q:\n%s", want, sh)
				}
			}
			if c.msg != "" && (len(r.diags) == 0 || !strings.Contains(r.diags[0].Message, c.msg)) {
				t.Errorf("message: got %v, want %q", r.codes(), c.msg)
			}
		})
	}
}

func TestItems(t *testing.T) {
	runCases(t, []pcase{
		{name: "import", src: "import std.fs\nimport io = std.io\n", tree: []string{"Import\n    Path\n      Ident std\n      Ident fs", "Import\n    Ident io"}},
		{name: "import as", src: "import a.b as c\n", codes: "E0101", msg: "write `import c = a.b`"},
		{name: "import keyword segment", src: "import std.test\n", codes: "E0102"},
		{name: "gen", src: "gen derive.Eq(User) fuel 100\n", tree: []string{"GenItem\n    Path"}},
		{name: "test", src: "test \"adds\" {\n}\n", tree: []string{"TestItem"}},
		{name: "test bytes name", src: "test b\"x\" {\n}\n", codes: "E0101"},
		{name: "export decl", src: "export F() {\n}\n", tree: []string{"FuncDecl export"}},
		{name: "export import", src: "export import a\n", codes: "E0101", msg: "only declarations are exported"},
		{name: "two items on a line", src: "struct A { } struct B { }\n", codes: "E0101", msg: "one item per line"},
		{name: "fn literal item", src: "fn() { }\n", codes: "E0101", msg: "a fn literal is not an item"},
		{name: "expected item", src: "+\n", codes: "E0101", msg: "expected an item"},
		{name: "unsafe block item", src: "unsafe \"x\" { }\n", codes: "E0101", msg: "an unsafe block is a statement"},
	})
}

func TestStructs(t *testing.T) {
	runCases(t, []pcase{
		{name: "modifiers", src: "linear view struct S {\n    a: int\n    export b: str\n    invariant a > 0\n}\n",
			tree: []string{"StructDecl linear view", "FieldMember export", "InvariantMember"}},
		{name: "mut view", src: "mut view struct S {\n    a: int\n}\n",
			tree: []string{"StructDecl mut view"}},
		{name: "linear mut view", src: "export linear mut view struct S {\n    a: int\n}\n",
			tree: []string{"StructDecl export linear mut view"}},
		{name: "packed align mut view", src: "packed(4) align(16) mut view struct S {\n    a: int\n}\n",
			tree: []string{"StructDecl mut view", "Packed", "Align"}},
		{name: "bare mut struct", src: "mut struct S { }\n", codes: "E0101", msg: "`mut` on a struct applies only to `view`: write `mut view struct`"},
		{name: "mut view view", src: "mut view view struct S { }\n", codes: "E0101", msg: "`view` written twice"},
		{name: "repeated modifier", src: "linear linear struct S { }\n", codes: "E0101", msg: "`linear` written twice"},
		{name: "packed", src: "packed(4) struct S { }\npacked struct T { }\n", tree: []string{"Packed\n      BasicLit Int"}},
		{name: "packed bad", src: "packed(3) struct S { }\n", codes: "E0101", msg: "`packed(n)` takes 1, 2, 4 or 8"},
		{name: "comma fields", src: "struct S {\n    a: int,\n    b: int\n}\n", codes: "E0101", msg: "one field per line"},
		{name: "empty", src: "struct S { }\n"},
		{name: "generic", src: "struct Stack2[T: Plain] {\n    items: List[T]\n}\n", tree: []string{"TypeParams"}},
	})
}

func TestPackedLookahead(t *testing.T) {
	runCases(t, []pcase{
		{name: "function named packed", src: "packed(x: int) {\n}\n", tree: []string{"FuncDecl"}},
		{name: "modifier", src: "packed(2) linear struct S { }\n", tree: []string{"StructDecl linear"}},
	})
}

func TestEnums(t *testing.T) {
	runCases(t, []pcase{
		{name: "tagged", src: "enum Color: u8 {\n    Red = 1\n    Green\n}\n", tree: []string{"EnumDecl", "Variant"}},
		{name: "payloads", src: "enum Shape {\n    Circle(r: f64)\n    Rect(w: f64, h: f64,)\n}\n"},
		{name: "empty parens", src: "enum E {\n    A()\n}\n", codes: "E0101", msg: "no fields has no parentheses"},
		{name: "payload and discr", src: "enum E {\n    A(x: int) = 1\n}\n", codes: "E0101"},
		{name: "tag with payload", src: "enum E: u8 {\n    A(x: int)\n}\n", codes: "E0101"},
		{name: "comma variants", src: "enum E {\n    A,\n    B\n}\n", codes: "E0101", msg: "one variant per line"},
	})
}

func TestErrors(t *testing.T) {
	runCases(t, []pcase{
		{name: "plain", src: "error Denied\nerror NotFound(path: str)\n"},
		{name: "empty parens", src: "error Denied()\n", codes: "E0101", msg: "write `error Denied`"},
	})
}

func TestInterfaces(t *testing.T) {
	runCases(t, []pcase{
		{name: "members", src: "interface Writer {\n    Write(mut self, b: []u8): WriteError!int\n    Flush(mut self)\n}\n", tree: []string{"InterfaceDecl", "SelfParam"}},
		{name: "body", src: "interface I {\n    F(self) { }\n}\n", codes: "E0101", msg: "interface members have no body"},
		{name: "contract", src: "interface I {\n    F(self, x: int) requires x > 0\n}\n", codes: "E0101", msg: "contracts belong on the implementing functions"},
		{name: "owner", src: "interface I {\n    T.F(self)\n}\n", codes: "E0101"},
	})
}

func TestImpls(t *testing.T) {
	runCases(t, []pcase{
		{name: "impl", src: "impl[T: Eq] Eq for List[T] {\n    Eq(&self, o: &Self): bool {\n        return true\n    }\n}\n", tree: []string{"ImplDecl"}},
		{name: "marker", src: "unsafe impl Send for P { }\n", tree: []string{"ImplDecl unsafe"}},
		{name: "bodiless member", src: "impl I for T {\n    F(self)\n}\n", codes: "E0101", msg: "an impl method needs a body"},
	})
}

func TestAliases(t *testing.T) {
	runCases(t, []pcase{
		{name: "alias", src: "type Bytes = []u8\ntype Pair[T] = (a: T, b: T)\n", tree: []string{"AliasDecl", "RecordType"}},
		{name: "const", src: "const N: int = 4\nconst T = Table() fuel 1000\n", tree: []string{"ConstDecl"}},
		{name: "global", src: "var count: int = 0\n", tree: []string{"GlobalDecl"}},
		{name: "global no type", src: "var count = 0\n", codes: "E0101", msg: "a top-level var needs a type and a value"},
	})
}

func TestExtern(t *testing.T) {
	runCases(t, []pcase{
		{name: "variadic", src: "extern printf(f: *u8, ...,): i32\n"},
		{name: "export extern", src: "extern Callback(x: i32): i32 {\n    return x\n}\n"},
		{name: "generic", src: "extern F[T](x: T)\n", codes: "E0101", msg: "an extern is not generic"},
		{name: "ellipsis with body", src: "extern F(x: int, ...) {\n}\n", codes: "E0101", msg: "`...` is only for declaring a C variadic"},
		{name: "ellipsis on func", src: "F(x: int, ...) {\n}\n", codes: "E0101"},
		{name: "ellipsis alone", src: "extern F(...)\n", codes: "E0101"},
	})
}

func TestBodiless(t *testing.T) {
	runCases(t, []pcase{
		{name: "user", src: "F(x: int): int\n", codes: "E0101", msg: "function needs a body"},
		{name: "std", src: "F(x: int): int\n", opts: Options{AllowBodiless: true}},
	})
}

func TestAllmanBraceOneDiagnostic(t *testing.T) {
	for _, opts := range []Options{{}, {AllowBodiless: true}} {
		r := parse(t, "F()\n{\n}\n", opts)
		if r.codeList() != "E0101" || r.diags[0].At.Start != 4 {
			t.Errorf("%+v: %v", opts, r.codes())
		}
		if !strings.Contains(r.deepShape(), "FuncDecl\n    Signature\n      FuncName\n        Ident F\n    Block") {
			t.Errorf("body not kept:\n%s", r.shape())
		}
	}
}

func TestContinuation(t *testing.T) {
	runCases(t, []pcase{
		{name: "flows and contracts", src: "Pick(a: &A, b: &A): &A\n    from a, b\n    requires true\n{\n    return a\n}\n",
			tree: []string{"FromFlow", "Contract", "Block"}},
		{name: "store", src: "Push(l: mut L, x: T)\n    *x into l\n{\n}\n", tree: []string{"StoreFlow"}},
		{name: "from local is a statement", src: "F() {\n    from := 0\n    ends := from\n}\n", tree: []string{"BindStmt\n      Ident from"}},
	})
}

func TestLoneBraceAfterContinuation(t *testing.T) {
	r := parse(t, "F(a: &A): &A from a\n{\n    return a\n}\n", Options{})
	if r.codeList() != "E0101" {
		t.Errorf("Allman after a one-line signature: %v", r.codes())
	}
}

func TestFlowCommas(t *testing.T) {
	runCases(t, []pcase{
		{name: "signature commas", src: "F(a: &A, b: &A, c: mut C): &A from a, b, *a into c, ends b {\n}\n",
			tree: []string{"FromFlow\n        Source\n          Ident a\n        Source\n          Ident b\n      StoreFlow", "EndsFlow"}},
		{name: "juxtaposed", src: "Exchange[T](a: mut T, b: mut T) *a into b *b into a {\n}\n"},
		{name: "bad comma", src: "F(a: &A): &A from a, 1 {\n}\n", codes: "E0101"},
	})
}

func TestFnTypeCommas(t *testing.T) {
	runCases(t, []pcase{
		{name: "sources continue", src: "F(f: fn(x: &A, y: &B): &A from x, y, n: int) {\n}\n",
			tree: []string{"FromFlow\n            Source\n              Ident x\n            Source\n              Ident y\n      Param\n        Ident n"}},
		{name: "store ends fn type", src: "F(f: fn(a: mut A): &A from a, p: *u8) {\n}\n", tree: []string{"Param\n        Ident p"}},
		{name: "juxtaposed in list", src: "F(f: fn(a: mut A, b: mut B) *a into b *b into a) {\n}\n"},
		{name: "raw", src: "Raw(g: fn(f: fn(a: mut A, b: mut B) *a into b, *u8): int) {\n}\n"},
		{name: "bracket arg", src: "G(p: Two[fn(a: mut A, b: mut B) *a into b, int]) {\n}\n"},
		{name: "parenthesized", src: "G(f: (fn(a: mut A, b: mut B) *a into b, *b into a), n: int) {\n}\n"},
		{name: "refused", src: "Split(f: fn(a: mut A, b: mut B) *a into b, *b into a) {\n}\n", codes: "E0101"},
		{name: "dotted source", src: "F(f: fn(x: &P): &A from x, x.a, n: int) {\n}\n"},
	})
}

func TestFlowsAfterFnResult(t *testing.T) {
	a := parse(t, "F(a: &A): fn(&A): &A from a\n", Options{AllowBodiless: true})
	b := parse(t, "F(a: &A): fn(&A): &A\n    from a\n", Options{AllowBodiless: true})
	if a.codeList() != "" || b.codeList() != "" {
		t.Fatalf("%v %v", a.codes(), b.codes())
	}
	if a.shape() != b.shape() {
		t.Errorf("layouts differ:\n%s\n%s", a.shape(), b.shape())
	}
	if !strings.Contains(a.deepShape(), "\n        FromFlow") {
		t.Errorf("from not on the FnType:\n%s", a.shape())
	}
	c := parse(t, "F(a: &A): (fn(&A): &A)\n    from a\n", Options{AllowBodiless: true})
	if !strings.Contains(c.shape(), "ParenType") || !strings.Contains(c.deepShape(), "\n      FromFlow") {
		t.Errorf("parenthesized result: flow not on the signature:\n%s", c.shape())
	}
	d := parse(t, "F(a: &A): fn(&A): &A\n    requires true\n", Options{AllowBodiless: true})
	if !strings.Contains(d.deepShape(), "\n      Contract") {
		t.Errorf("requires not on the signature:\n%s", d.shape())
	}
	e := parse(t, "Make(): fn(a: mut A, b: mut B) *a into b, *b into a {\n}\n", Options{})
	if e.codeList() != "" || strings.Count(e.shape(), "StoreFlow") != 2 {
		t.Errorf("Make: %v\n%s", e.codes(), e.shape())
	}
}

func TestRepeatedFlows(t *testing.T) {
	r := parse(t, "F(a: &A, b: &A): &A from a from b {\n}\n", Options{})
	if r.codeList() != "E0101" || len(r.diags[0].Fixes) != 1 {
		t.Fatalf("%v", r.codes())
	}
	runCases(t, []pcase{{name: "two ends", src: "F(a: &A, b: &A) ends a ends b {\n}\n", codes: "E0101"}})
}

func TestFlowPosition(t *testing.T) {
	runCases(t, []pcase{{name: "flow before result", src: "F(a: mut T) *a into b: int {\n}\n", codes: "E0101"}})
}

func TestFuncNameCover(t *testing.T) {
	runCases(t, []pcase{
		{name: "owner args", src: "Stack2[T].Push(mut self, x: T) {\n}\n", tree: []string{"FuncName\n        Ident Stack2\n        TypeArgs\n          Ident T\n        Ident Push"}},
		{name: "own params after owner", src: "Slice[T].SortWith[C](mut self, c: C) {\n}\n", tree: []string{"Ident SortWith\n      TypeParams"}},
		{name: "bounds in owner", src: "Stack2[T: Eq].Push(mut self) {\n}\n", codes: "E0101", msg: "bounds come from the type"},
		{name: "generic function", src: "Max[T: Ord](a: T, b: T): T {\n    return a\n}\n", tree: []string{"TypeParams\n        TypeParam"}},
		{name: "primitive owner", src: "str.Len(&self): int\n", opts: Options{AllowBodiless: true}},
	})
}

func TestReceivers(t *testing.T) {
	runCases(t, []pcase{
		{name: "modes", src: "P.A(self) {\n}\nP.B(&self) {\n}\nP.C(mut self) {\n}\n", tree: []string{"SelfParam mut"}},
		{name: "no owner", src: "F(self) {\n}\n", codes: "E0101", msg: "a receiver needs a `Type.` prefix"},
		{name: "self param", src: "P.F(self: P) {\n}\n", codes: "E0101"},
		{name: "underscore param", src: "F(_: int) {\n}\n", codes: "E0101"},
		{name: "fn literal receiver", src: "F() {\n    g := fn(self) { }\n}\n", codes: "E0101"},
		{name: "into", src: "P.A(self into a, a: &Arena) {\n}\nP.B(&self into a, a: &Arena) {\n}\nP.C(mut self into self) {\n}\n",
			tree: []string{"SelfParam self\n        Ident a", "SelfParam &self\n        Ident a", "SelfParam mut self\n        Ident self"}},
		{name: "into alone", src: "P.A(&self into a) {\n}\n"},
		{name: "into member", src: "interface K {\n    Keys(&self into a, a: &Arena)\n}\n", tree: []string{"SelfParam &self\n        Ident a"}},
		{name: "into no name", src: "P.A(&self into ) {\n}\n", codes: "E0101", msg: "a parameter name"},
	})
}

func TestTestParamModifiers(t *testing.T) {
	runCases(t, []pcase{
		{name: "var", src: "test \"t\" (var sys: &System) { }\n", codes: "E0101"},
		{name: "const", src: "test \"t\" (const sys: &System) { }\n", codes: "E0101"},
		{name: "plain", src: "test \"t\" (sys: &System) { }\n"},
	})
}

func TestTypeLadder(t *testing.T) {
	runCases(t, []pcase{
		{name: "error union", src: "F(): A|B!?[]u8\n", opts: Options{AllowBodiless: true},
			tree: []string{"ErrorType\n        UnionType", "OptionalType\n          SliceType"}},
		{name: "prefixes", src: "F(a: &mut T)\n", codes: "E1212", opts: Options{AllowBodiless: true}},
		{name: "double ref", src: "F(a: &&T)\n", opts: Options{AllowBodiless: true}, tree: []string{"RefType\n          RefType"}},
	})
}

func TestMutSlice(t *testing.T) {
	runCases(t, []pcase{
		{name: "mut slice", src: "F(a: mut []u8, b: mut ([]u8))\n", opts: Options{AllowBodiless: true},
			tree: []string{"MutSliceType", "MutType\n          ParenType"}},
	})
}

func TestRecordTypes(t *testing.T) {
	runCases(t, []pcase{
		{name: "record", src: "F(): (a: int, b: str,)\n", opts: Options{AllowBodiless: true}, tree: []string{"RecordType\n        Field"}},
		{name: "empty", src: "F(): ()\n", opts: Options{AllowBodiless: true}, codes: "E0101", msg: "the empty record type cannot be written"},
		{name: "angle", src: "F(a: List<int>)\n", opts: Options{AllowBodiless: true}, codes: "E0101", msg: "type arguments use brackets"},
	})
}

func TestBracketMode(t *testing.T) {
	runCases(t, []pcase{
		{name: "optional union", src: "x: G[?A | B] = y\n", codes: "E0101"},
		{name: "types", src: "F() {\n    x := G[?A | B, []u8, &T, *u8, mut []u8, fn(int): int, (a: []u8), (n), E!T](1)\n}\n",
			tree: []string{"UnionType\n                OptionalType", "SliceType", "UnaryExpr", "MutSliceType", "FnType", "RecordType", "RecordLit", "BinaryExpr"}},
	})
}

func TestPrefixTypesBindTight(t *testing.T) {
	for _, ty := range []string{"?A | B", "[]A | B", "[2]A!B"} {
		a := parse(t, "F(x: "+ty+")\n", Options{AllowBodiless: true})
		b := parse(t, "F(x: G["+ty+"])\n", Options{AllowBodiless: true})
		inner := a.shape()
		i := strings.Index(inner, "Ident x\n")
		want := inner[i+len("Ident x\n"):]
		got := b.shape()
		for _, line := range strings.Split(strings.TrimSpace(want), "\n") {
			if !strings.Contains(got, strings.TrimSpace(line)) {
				t.Errorf("%s: bracket form lacks %q:\n%s\n%s", ty, line, want, got)
				break
			}
		}
	}
}

func TestArrayCover(t *testing.T) {
	runCases(t, []pcase{
		{name: "index of literal", src: "F(m: []int, k: int): int {\n    return m[[k][0]]\n}\n", tree: []string{"IndexExpr\n          Ident m\n          IndexExpr\n            ArrayLit"}},
		{name: "two indices", src: "F(m: []int, k: int): int {\n    return m[[k][0][1]]\n}\n", tree: []string{"IndexExpr\n            IndexExpr\n              ArrayLit"}},
		{name: "array of array", src: "F(x: G[[4][2]u8])\n", opts: Options{AllowBodiless: true}, tree: []string{"ArrayType\nBasicLit Int 4\nArrayType"}},
		{name: "array types", src: "F(x: G[[4]u8], y: G[[4]*u8], z: G[[2](a: A)])\n", opts: Options{AllowBodiless: true}},
		{name: "array literals", src: "F() {\n    x := m[[k].Len()]\n    y := G[[], x]()\n}\n", tree: []string{"ArrayLit"}},
	})
}

func TestPrecedence(t *testing.T) {
	runCases(t, []pcase{
		{name: "ladder", src: "F() {\n    x := a || b && c == d + e * -f\n}\n",
			tree: []string{"BinaryExpr ||\nIdent a\nBinaryExpr &&\nIdent b\nBinaryExpr =="}},
	})
}

func TestNoChaining(t *testing.T) {
	runCases(t, []pcase{{name: "chain", src: "F() {\n    x := a < b < c\n}\n", codes: "E0101", msg: "comparisons do not chain"}})
}

func TestFallback(t *testing.T) {
	runCases(t, []pcase{
		{name: "else", src: "F() {\n    x := G() else { return }\n    y := G() else e { return }\n}\n", tree: []string{"ElseExpr\n          CallExpr", "ElseExpr\n          CallExpr\n            Ident G\n          Ident e"}},
		{name: "two", src: "F() {\n    x := G() else { return } else { return }\n}\n", codes: "E0101", msg: "one `else` per expression"},
		{name: "in if", src: "F() {\n    if G() else { return } {\n    }\n}\n", tree: []string{"IfStmt\n        ElseExpr"}},
	})
}

func TestExprSuffixes(t *testing.T) {
	runCases(t, []pcase{
		{name: "suffixes", src: "F() {\n    x := a.b[c](d)[1..][..2][e..f]\n}\n", tree: []string{"SliceExpr", "CallExpr", "IndexExpr"}},
		{name: "dot int", src: "F() {\n    x := t.0\n}\n", codes: "E0101", msg: "records have names"},
		{name: "empty index", src: "F() {\n    x := xs[]\n}\n", codes: "E0101"},
		{name: "named args", src: "F() {\n    x := Point(x: 1, y: 2,)\n}\n", tree: []string{"Arg\n            Ident x"}},
	})
}

func TestRecordLiterals(t *testing.T) {
	runCases(t, []pcase{
		{name: "records", src: "F() {\n    a := ()\n    b := (i)\n    c := (x: 1, y)\n    d := (1 + 2)\n}\n", tree: []string{"RecordLit\n          RecField", "ParenExpr"}},
		{name: "positional", src: "F() {\n    a := (1, 2)\n}\n", codes: "E0101", msg: "record fields need names"},
	})
}

func TestArrayLiterals(t *testing.T) {
	runCases(t, []pcase{{name: "arrays", src: "F() {\n    a := [1, 2,]\n    b := [0; 4]\n    c := []\n}\n", tree: []string{"ArrayLit", "RepeatLit"}}})
}

func TestFnLiterals(t *testing.T) {
	runCases(t, []pcase{
		{name: "literal", src: "F() {\n    g := fn(var x: int, y: &T into z): int { return x }\n}\n", tree: []string{"FnLit\n          Param var"}},
		{name: "const param", src: "F() {\n    g := fn(const x: int) { }\n}\n", codes: "E0101"},
	})
}

func TestQuote(t *testing.T) {
	runCases(t, []pcase{
		{name: "quote", src: "F() {\n    q := quote { $x + $(a.b) { if c { } } }\n}\n", tree: []string{"Splice\nIdent x\nSplice\nFieldExpr"}},
		{name: "bad splice", src: "F() {\n    q := quote { $1 }\n}\n", codes: "E0101", msg: "a splice is"},
		{name: "nested", src: "F() {\n    q := quote { quote { } }\n}\n", codes: "E0101", msg: "quotes do not nest"},
		{name: "dollar outside", src: "F() {\n    q := $x\n}\n", codes: "E0101", msg: "`$` splices only inside"},
	})
}

func TestGoForms(t *testing.T) {
	runCases(t, []pcase{
		{name: "statement", src: "F(s: &Scope) {\n    s.go G(1)\n}\n", tree: []string{"ExprStmt\n        GoExpr"}},
		{name: "bind", src: "F(s: &Scope) {\n    x := s.go G(1)\n}\n", codes: "E0101", msg: "`.go` is a statement"},
		{name: "not a call", src: "F(s: &Scope) {\n    s.go G\n}\n", codes: "E0101", msg: "`.go` spawns a call"},
		{name: "bare go", src: "F() {\n    go G()\n}\n", codes: "E0101", msg: "write `s.go F()`"},
	})
}

func TestStatementDispatch(t *testing.T) {
	runCases(t, []pcase{
		{name: "all", src: "F() {\n    var x := 1\n    y: int = 2\n    z: int\n    _ = G()\n    x += 1\n    a.b = 3\n    defer x.Close()\n    onerr { Log() }\n    scope s := io.Scope() {\n    }\n    try scope t := io.Scope() {\n    }\n    unsafe \"raw\" {\n    }\n    fail E\n}\n",
			tree: []string{"BindStmt var", "DiscardStmt", "AssignStmt +=", "DeferStmt", "DeferStmt onerr", "ScopeStmt", "ScopeStmt try", "UnsafeStmt", "FailStmt"}},
		{name: "underscore bind", src: "F() {\n    _ := G()\n}\n", codes: "E0101", msg: "write `_ = e`"},
		{name: "block", src: "F() {\n    {\n    }\n}\n", codes: "E0101", msg: "a block is not a statement"},
		{name: "else line", src: "F() {\n    if a {\n    }\n    else {\n    }\n}\n", codes: "E0101", msg: "put `else` on the line"},
		{name: "two assigns", src: "F() {\n    a = b = c\n}\n", codes: "E0101"},
		{name: "defer assign", src: "F() {\n    defer x = 1\n}\n", codes: "E0101", msg: "an assignment needs `defer { }`"},
		{name: "empty reason", src: "F() {\n    unsafe \"\" {\n    }\n}\n", codes: "E0101", msg: "say why"},
		{name: "blank reason", src: "F() {\n    unsafe \" \\t\" {\n    }\n}\n", codes: "E0101", msg: "say why"},
		{name: "two statements", src: "F() {\n    x := 1 y := 2\n}\n", codes: "E0101", msg: "one statement per line"},
	})
}

func TestRecordPatCover(t *testing.T) {
	runCases(t, []pcase{
		{name: "call", src: "F(p: Point) {\n    Point(x, y: py) := p\n}\n", tree: []string{"DestructureStmt reinterpreted\n        RecordPat\n          Path\n            Ident Point\n          PatField\n            Ident x\n          PatField\n            Ident y\n            NamePattern"}},
		{name: "record", src: "F(e: E) {\n    var (a, b) := e\n}\n", tree: []string{"DestructureStmt var reinterpreted"}},
		{name: "type args", src: "F(e: E) {\n    Pair[int](a, b) := e\n}\n", codes: "E0101", msg: "a destructure takes no type arguments"},
	})
}

func TestDestructureRest(t *testing.T) {
	runCases(t, []pcase{
		{name: "rest", src: "F(p: P) {\n    Point(x, ..) := p\n}\n", tree: []string{"RecordPat rest"}},
		{name: "nothing", src: "F(p: P) {\n    Point(..) := p\n}\n", codes: "E0101", msg: "nothing to bind"},
		{name: "rest without bind", src: "F(p: P) {\n    Point(x, ..)\n}\n", codes: "E0101"},
	})
}

func TestCoverPatterns(t *testing.T) {
	runCases(t, []pcase{
		{name: "variant", src: "F(p: P) {\n    (x: .A) := p\n}\n", tree: []string{"PatField\n            Ident x\n            VariantPattern"}},
		{name: "none", src: "F(p: P) {\n    (x: none) := p\n}\n", tree: []string{"NonePattern"}},
		{name: "alts", src: "F(p: P) {\n    (x: .A | .B) := p\n}\n", tree: []string{"AltPattern"}},
		{name: "nested", src: "F(q: Q) {\n    Pair(a: Point(x: .A)) := q\n}\n", tree: []string{"PathPattern fields\n                Path\n                  Ident Point\n                PatField\n                  Ident x\n                  VariantPattern"}},
		{name: "expression", src: "F(p: P) {\n    (x: .A)\n}\n", codes: "E0101"},
	})
}

func TestForHeads(t *testing.T) {
	runCases(t, []pcase{
		{name: "heads", src: "F() {\n    for {\n    }\n    for c {\n    }\n    for x := it.Next() {\n    }\n    for i, x in xs {\n    }\n    for _ in 0..n + 1 {\n    }\n    outer: for x in &xs {\n    }\n}\n",
			tree: []string{"ForStmt\n        Block", "BindCond", "InHead", "RangeSource", "ForStmt\n        Ident outer"}},
		{name: "is", src: "F() {\n    for T is u8 {\n    }\n}\n", codes: "E0101", msg: "a type test cannot change"},
		{name: "range index", src: "F() {\n    for i, x in 0..n {\n    }\n}\n", codes: "E0101", msg: "a range has no index"},
	})
}

func TestForSourceModes(t *testing.T) {
	runCases(t, []pcase{
		{name: "modes", src: "F() {\n    for x in xs {\n    }\n    for x in &xs[1..] {\n    }\n    for x in mut xs {\n    }\n}\n", tree: []string{"RefSource copy", "RefSource &", "RefSource mut"}},
	})
}

func TestFieldsRewritesToken(t *testing.T) {
	r := parse(t, "F(x: P) {\n    for f, v in fields(x) {\n    }\n}\n", Options{})
	found := false
	for _, tk := range r.file.Tokens {
		if tk.Kind.String() == `"fields"` {
			found = true
		}
	}
	if !found || !strings.Contains(r.shape(), "FieldsSource") {
		t.Errorf("fields: %v\n%s", found, r.shape())
	}
}

func TestPatterns(t *testing.T) {
	runCases(t, []pcase{
		{name: "all", src: "F(x: E) {\n    match x {\n        .A { }\n        .B(w: _, h: 1) | .C { }\n        some(y) { }\n        none { }\n        ok(_) { }\n        err(Denied) { }\n        -1 { }\n        \"s\" { }\n        Shape.Rect(w: w) { }\n        n { }\n    }\n}\n",
			tree: []string{"VariantPattern", "AltPattern", "SomePattern", "NonePattern", "OkPattern", "ErrPattern", "LiteralPattern neg", "PathPattern", "NamePattern", "WildcardPattern"}},
		{name: "positional field", src: "F(x: E) {\n    match x {\n        Shape.Rect(1.0, 2.0) { }\n    }\n}\n", codes: "E0101", msg: "patterns name fields"},
		// A range is a pattern; a string end is not.
		{name: "range", src: "F(x: int) {\n    match x {\n        1..5 { }\n    }\n}\n", tree: []string{"RangePattern"}},
		{name: "string range", src: "F(x: str) {\n    match x {\n        \"a\"..\"b\" { }\n    }\n}\n", codes: "E0101", msg: "a range pattern's ends"},
		{name: "fat arrow", src: "F(x: E) {\n    match x {\n        .A => { }\n    }\n}\n", codes: "E1214"},
	})
}

func TestLiteralPatterns(t *testing.T) {
	runCases(t, []pcase{{name: "neg float", src: "F(x: f64) {\n    match x {\n        -1.5 { }\n    }\n}\n", codes: "E0101", msg: "a float literal is not a pattern"}})
}

func TestAlternativesBindNothing(t *testing.T) {
	runCases(t, []pcase{{name: "binds", src: "F(x: E) {\n    match x {\n        .A(v) | .B(v) { }\n    }\n}\n", codes: "E0101", msg: "alternatives bind nothing"}})
}

func TestLayoutsAccepted(t *testing.T) {
	runCases(t, []pcase{{name: "layouts", src: "F() {\n    if a {\n    } else\n    {\n    }\n    if\n    c {\n    }\n    x :=\n        e\n    y := a +\n        b\n    s.go\n        G(x)\n    z := q.\n        R()\n}\n"}})
}

func TestLayoutsRejected(t *testing.T) {
	runCases(t, []pcase{
		{name: "leading dot", src: "F() {\n    x := q\n        .R()\n}\n", codes: "E0101", msg: "break a method chain after the dot"},
	})
}

func TestBreakOutsideLoop(t *testing.T) {
	runCases(t, []pcase{
		{name: "outside", src: "F() {\n    break\n}\n", codes: "E0101", msg: "`break` outside a loop"},
		{name: "continue", src: "F() {\n    continue\n}\n", codes: "E0101"},
		{name: "inside", src: "F() {\n    for {\n        break\n    }\n}\n"},
		{name: "labeled", src: "F() {\n    outer: for {\n        for {\n            continue outer\n        }\n    }\n}\n"},
	})
}

func TestBreakInFnLiteral(t *testing.T) {
	runCases(t, []pcase{
		{name: "literal", src: "F() { for { g := fn() { break } } }\n", codes: "E0101", msg: "`break` outside a loop"},
		{name: "loop", src: "F() { for { break } }\n"},
	})
}

func TestE0102_Keywords(t *testing.T) {
	runCases(t, []pcase{
		{name: "local", src: "F() {\n    type := 1\n}\n", codes: "E0102", msg: "`type` is a keyword"},
		{name: "param", src: "F(match: int) {\n}\n", codes: "E0102"},
		{name: "field", src: "struct S {\n    for: int\n}\n", codes: "E0102"},
		{name: "after dot", src: "F() {\n    x := a.type\n}\n", codes: "E0102"},
	})
}

func TestE0102_UniverseNames(t *testing.T) {
	runCases(t, []pcase{
		{name: "primitive local", src: "F() {\n    str := 1\n}\n", codes: "E0102", msg: "`str` is a predeclared type name"},
		{name: "universe param", src: "F(none: int) {\n}\n", codes: "E0102", msg: "`none` is a universe name"},
		{name: "item", src: "struct int { }\n", codes: "E0102"},
		{name: "method exempt", src: "Chan[T].Send(&self) {\n}\n"},
		{name: "variant exempt", src: "enum E {\n    Plain\n}\n"},
	})
}

func TestOkErrDeclarable(t *testing.T) {
	runCases(t, []pcase{{name: "ok err", src: "struct Register {\n    ok: bool\n    err: int\n}\n"}})
}

func TestNoCascade(t *testing.T) {
	runCases(t, []pcase{
		{name: "one", src: "F() { x := (1 + }\nG() { }\n", codes: "E0101", msg: "unmatched `}`"},
		{name: "two", src: "F() { x := (1 + }\nG() { y := }\n", codes: "E0101 E0101"},
		{name: "unclosed", src: "F() {\n    x := (1 +\n    y := 2\n", codes: "E0101", msg: "unclosed `(`"},
	})
}

func TestEveryErrorReported(t *testing.T) {
	r := parse(t, "A() { x := }\nB() { y := 1 + }\nstruct C { d: }\nD(: int) {\n}\nE() { return ) }\n", Options{})
	if len(r.diags) != 5 {
		t.Errorf("want 5 diagnostics, one per item:\n  %s", strings.Join(r.codes(), "\n  "))
	}
}

func TestHabitEndsItsLine(t *testing.T) {
	runCases(t, []pcase{
		{name: "question", src: "F() {\n    x := f()?\n    y := 2\n}\n", codes: "E1208", tree: []string{"BindStmt\n        Ident y"}},
		{name: "semi", src: "F() {\n    x := 1;\n    y := 2\n}\n", codes: "E1206", tree: []string{"BindStmt\n        Ident y"}},
		{name: "plusplus", src: "F() {\n    x++\n    y := 2\n}\n", codes: "E0101", tree: []string{"BindStmt\n        Ident y"}},
	})
}

func TestBadTokenOneDiagnostic(t *testing.T) {
	runCases(t, []pcase{
		{name: "attribute", src: "#[derive(Eq)]\nstruct P { }\n", codes: "E0101", tree: []string{"StructDecl"}},
		{name: "at", src: "@inline\nF() { }\n", codes: "E0101", tree: []string{"FuncDecl"}},
		{name: "operand", src: "F() {\n    x := @\n    y := 2\n}\n", codes: "E0101", tree: []string{"BindStmt\n        Ident y"}},
		{name: "ident", src: "F() { café := 1 }\n", codes: "E0101"},
	})
}

func TestDepthLimit(t *testing.T) {
	ok := parse(t, "F() { x := "+strings.Repeat("(", 500)+"1"+strings.Repeat(")", 500)+" }\n", Options{})
	if ok.codeList() != "" {
		t.Errorf("500 levels: %v", ok.codes())
	}
	deep := parse(t, "F() { x := "+strings.Repeat("(", 1001)+"1"+strings.Repeat(")", 1001)+" }\n", Options{})
	if deep.codeList() != "E0101" || deep.diags[0].Message != "nesting too deep: more than 1000 levels" {
		t.Errorf("1001 levels: %v", deep.codes())
	}
	ifs := parse(t, "F() { "+strings.Repeat("if c { ", 1001)+strings.Repeat("} ", 1001)+"}\n", Options{})
	if ifs.codeList() != "E0101" {
		t.Errorf("ifs: %v", ifs.codes())
	}
	huge := parse(t, "F() { x := "+strings.Repeat("(", 2_000_000)+" }\n", Options{})
	if !strings.HasPrefix(huge.codeList(), "E0101") {
		t.Errorf("huge: %v", huge.codes())
	}
	// With MaxDepth 50, a call nest of depth exactly 50 parses and one more fails.
	nest := func(n int) string { return strings.Repeat("f(", n) + "x" + strings.Repeat(")", n) }
	base := parse(t, "const C = "+nest(0)+"\n", Options{MaxDepth: 50})
	d0 := treeDepth(base.file.Items[0])
	// Each f( adds CallExpr, Ident callee, Arg: two levels on the path.
	n := (50 - d0) / 2
	at := parse(t, "const C = "+nest(n)+"\n", Options{MaxDepth: 50})
	if at.codeList() != "" {
		t.Errorf("depth %d: %v", treeDepth(at.file.Items[0]), at.codes())
	}
	over := parse(t, "const C = "+nest(n+1)+"\n", Options{MaxDepth: 50})
	if over.codeList() != "E0101" || !strings.Contains(over.diags[0].Message, "more than 50 levels") {
		t.Errorf("over: %v", over.codes())
	}
}

func TestLongChains(t *testing.T) {
	cases := map[string]string{
		"sum":    "F() { x := 1" + strings.Repeat(" + 1", 2_000_000) + " }\n",
		"calls":  "F() { x := f" + strings.Repeat("()", 100_000) + " }\n",
		"fields": "F() { x := x" + strings.Repeat(".a", 100_000) + " }\n",
	}
	for name, src := range cases {
		r := parse(t, src, Options{})
		if r.codeList() != "E0101" {
			t.Errorf("%s: %v", name, r.codes())
		}
	}
	short := parse(t, "F() { x := 1"+strings.Repeat(" + 1", 900)+" }\n", Options{})
	if short.codeList() != "" {
		t.Errorf("900 terms: %v", short.codes())
	}
}

func TestParseDeterministic(t *testing.T) {
	t.Parallel()
	for _, b := range docBlocks(t) {
		a := parse(t, b.text, Options{AllowBodiless: true})
		c := parse(t, b.text, Options{AllowBodiless: true})
		if a.tree() != c.tree() || a.file.NodeCount != c.file.NodeCount {
			t.Fatalf("%s: two parses differ", b.name)
		}
	}
}

func TestParseParallel(t *testing.T) {
	t.Parallel()
	blocks := docBlocks(t)
	serial := make([]string, len(blocks))
	for i, b := range blocks {
		serial[i] = parse(t, b.text, Options{}).tree()
	}
	done := make(chan struct{})
	errs := make(chan string, len(blocks))
	for i, b := range blocks {
		go func() {
			defer func() { done <- struct{}{} }()
			if got := treeOf(b.text); got != serial[i] {
				errs <- b.name
			}
		}()
	}
	for range blocks {
		<-done
	}
	close(errs)
	for name := range errs {
		t.Errorf("%s: parallel parse differs", name)
	}
}

// treeOf parses text without a testing.T, for goroutines.
func treeOf(text string) string {
	sm := source.NewSourceMap()
	src := sm.Add("t.mk", []byte(text))
	in := source.NewInterner()
	f, _ := ParseFile(src, in, Options{})
	return ast.Sprint(f, in)
}

// TestElseAttached: an else on its own line is one E0101 and joins the
// previous if chain, so the statements after it parse.
func TestElseAttached(t *testing.T) {
	runCases(t, []pcase{{name: "attach", src: "F() {\n    if a {\n    } else if b {\n    }\n    else {\n    }\n    x := 1\n}\n",
		codes: "E0101", tree: []string{"IfStmt\nIdent a\nBlock\nIfStmt\nIdent b\nBlock\nBlock\nBindStmt"}}})
}

// TestE0102_ProvenRename: a keyword-named parameter gets one E0102 whose
// proven rename edits the declaration and every use; the uses are folded in
// and the fixed file parses.
func TestE0102_ProvenRename(t *testing.T) {
	src := "F(type: int): int {\n    type_ := 1\n    return type + type_\n}\n"
	r := parse(t, src, Options{})
	if r.codeList() != "E0102" || len(r.diags[0].Fixes) != 1 || len(r.diags[0].Fixes[0].Edits) != 2 {
		t.Fatalf("%v %v", r.codes(), r.diags[0].Fixes)
	}
	fixed := applyFix(r.src.Text, r.diags[0])
	if !strings.Contains(fixed, "F(type_2: int)") {
		t.Errorf("fresh name: %s", fixed)
	}
	if again := parse(t, fixed, Options{}); len(again.diags) != 0 {
		t.Errorf("fixed text: %v", again.codes())
	}
	field := parse(t, "struct S {\n    type: int\n}\n", Options{})
	if field.codeList() != "E0102" || len(field.diags[0].Fixes) != 0 || len(field.diags[0].Alternatives) != 1 {
		t.Errorf("field: alternatives only: %v", field.codes())
	}
}

// TestPerFieldClause: the per-field clause `from (f: a, g: b)`
// (doc/grammar.md FieldSrc): several sources per field, a path, a
// trailing comma, a continuation line, and a clause inside a parameter list,
// where the parentheses close the clause so the comma rule for fn-type flows
// gains no case.
func TestPerFieldClause(t *testing.T) {
	runCases(t, []pcase{
		{name: "two fields", src: "Parse(a: &Arena, src: str): OutOfMemory!Parsed from (nums: a, name: src) {\n}\n",
			tree: []string{"FromFlow\n        FieldSrc\n          Ident nums\n          Source\n            Ident a\n        FieldSrc\n          Ident name\n          Source\n            Ident src"}},
		{name: "several sources and a trailing comma", src: "Mk(a: &A, src: str): Tree from (tree: a, src, name: src,) {\n}\n",
			tree: []string{"FieldSrc\n          Ident tree\n          Source\n            Ident a\n          Source\n            Ident src\n        FieldSrc\n          Ident name"}},
		{name: "a path", src: "Tok.Get(&self): Pair from (text: self.src) {\n}\n",
			tree: []string{"FieldSrc\n          Ident text\n          Source\n            Ident self\n            Ident src"}},
		{name: "continuation line", src: "F(a: &A, b: &B): P\n    from (a: a, b: b)\n{\n}\n"},
		{name: "a fn type in a parameter list", src: "Both(f: fn(x: &A, y: &B): P from (a: x, b: y), n: int) {\n}\n",
			tree: []string{"FromFlow\n            FieldSrc\n              Ident a\n              Source\n                Ident x\n            FieldSrc\n              Ident b\n              Source\n                Ident y\n      Param\n        Ident n"}},
		{name: "then a store", src: "F(a: &A, b: mut B): P from (x: a), *a into b {\n}\n", tree: []string{"StoreFlow"}},
		{name: "a field with no colon", src: "F(a: &A): P from (x a) {\n}\n", codes: "E0101"},
		{name: "unclosed", src: "F(a: &A): P from (x: a {\n}\n", codes: "E0101"},
		{name: "a second from", src: "F(a: &A, b: &B): P from (x: a) from b {\n}\n", codes: "E0101"},
	})
}
