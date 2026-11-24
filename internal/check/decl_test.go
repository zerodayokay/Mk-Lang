// Why: declarations and signatures are where most type errors of a program
// surface first, and their read sets drive incremental rebuilds.
// What: tests of type expressions, structs, enums, aliases, signatures,
// Main, E0412, E0410, E0303 and declaration read sets.
// How: builds modules from source through syntax and resolve and compares
// the diagnostics, layouts and read sets.

package check

import (
	"strings"
	"testing"

	"mk/internal/diag"
	"mk/internal/types"
)

// checkCodes adds a module and compares its diagnostics with want, a sorted
// space-separated list of "E0301@3:5".
func checkCodes(t *testing.T, w *world, path string, o opt, want string, srcs ...string) *built {
	t.Helper()
	b := w.add(path, o, srcs...)
	if got := codes(w, b.diags()); got != want {
		t.Fatalf("%s: diagnostics %q, want %q\n%s", path, got, want, dump(w, b.diags()))
	}
	return b
}

func (b *built) decl(suffix string) types.Decl { return b.w.items.Get(b.item(suffix)).Decl }

func (b *built) sigOf(suffix string) *types.Signature {
	b.w.t.Helper()
	fd, ok := b.decl(suffix).(*types.FuncDecl)
	if !ok {
		b.w.t.Fatalf("%s is not a function: %T", suffix, b.decl(suffix))
	}
	return &fd.Sig
}

func (b *built) ts(t types.TypeID, gens []types.GenericParam) string {
	return b.w.tab.TypeString(t, gens)
}

func TestDeclStructsAndTypes(t *testing.T) {
	w := newWorld(t, true)
	b := checkCodes(t, w, "m", opt{}, "", `struct Pair[T] {
	first: T
	export second: ?&T
}

struct Row {
	cells: [4]u8
	pair: Pair[int]
	rec: (b: str, a: i32)
	f: fn(&i32): &i32
	e: Oops|Gone!void
	err: Oops
	arr: []mut []u8
	ms: mut ([]u8)
}

error Oops
error Gone(code: int)
`)
	p := b.decl("Pair").(*types.StructDecl)
	if len(p.Generics) != 1 || len(p.Fields) != 2 || !p.Fields[1].Exported || p.Fields[0].Exported {
		t.Fatalf("Pair decl %+v", p)
	}
	if got := b.ts(p.Fields[1].Type, p.Generics); got != "?&T" {
		t.Errorf("Pair.second = %s", got)
	}
	r := b.decl("Row").(*types.StructDecl)
	want := []string{"[4]u8", "Pair[int]", "(a: i32, b: str)", "fn(&i32): &i32 from 1", "Gone|Oops!void", "Oops", "[]mut []u8", "mut []u8"}
	for i, f := range r.Fields {
		got := b.ts(f.Type, nil)
		if i == 3 {
			ft := w.tab.Get(f.Type).Fn
			if len(ft.Sources) != 1 || ft.Sources[0].Param != 1 || ft.Sources[0].Behind {
				t.Errorf("fn(&i32): &i32 sources %+v, want H(1)", ft.Sources)
			}
			continue
		}
		if i < len(want) && got != want[i] && !(i == 4 && got == "Oops|Gone!void") && !(i == 7 && got == "mut ([]u8)") {
			t.Errorf("Row field %d = %s, want %s", i, got, want[i])
		}
	}
	if k := w.tab.Kind(r.Fields[7].Type); k != types.KMut {
		t.Errorf("mut ([]u8) is %v, want KMut of a slice", k)
	}
	if k := w.tab.Kind(r.Fields[6].Type); k != types.KSlice || w.tab.Kind(w.tab.Get(r.Fields[6].Type).Elem) != types.KMutSlice {
		t.Errorf("[]mut []u8 is %v", k)
	}
	if e := b.decl("Oops").(*types.ErrorDecl); e.Code != types.ErrorCode(types.ItemKey("m", "Oops")) {
		t.Errorf("Oops.Code = %d", e.Code)
	}
	// The layout of Row is memoized by the shape pass (non-generic).
	if l := w.c.Layout(w.tab.Named(b.item("Row"), nil)); l.Shape.Size == 0 {
		t.Errorf("Row has no layout")
	}
}

func TestDeclTypeErrors(t *testing.T) {
	w := newWorld(t, true)
	checkCodes(t, w, "m", opt{}, "E0301@2:5 E0301@3:5 E0301@4:5 E0304@5:5 E0304@6:5", `struct S {
	a: void
	b: never
	c: Eq
	d: Pair[int, int]
	e: i32[u8]
	f: *void
	g: Oops!void
}

struct Pair[T] {
	x: T
}

error Oops

F(): never {
	return F()
}
`)
}

func TestDeclEnums(t *testing.T) {
	w := newWorld(t, true)
	b := checkCodes(t, w, "m", opt{}, "E0206@5:2 E0301@8:11 E0303@13:6", `enum Color: u8 {
	Red
	Green = 5
	Blue
	Teal = 6
}

enum Bad: bool {
	X
}

enum Big: u8 {
	A = 300
}

enum Shape {
	Circle(r: f64)
	Square(side: f64)
}
`)
	c := b.decl("Color").(*types.EnumDecl)
	var ds []uint64
	for _, v := range c.Variants {
		ds = append(ds, v.Discr)
	}
	if c.Tag != types.U8 || len(ds) != 4 || ds[0] != 0 || ds[1] != 5 || ds[2] != 6 || ds[3] != 6 {
		t.Fatalf("Color tag %d discriminants %v", c.Tag, ds)
	}
	s := b.decl("Shape").(*types.EnumDecl)
	if len(s.Variants) != 2 || len(s.Variants[1].Fields) != 1 || s.Variants[1].Discr != 1 {
		t.Fatalf("Shape %+v", s)
	}
}

func TestE0907_AliasCycle(t *testing.T) {
	w := newWorld(t, true)
	b := checkCodes(t, w, "m", opt{}, "E0907@1:6", `type A = B

type B = ?A

type C = A

type Bytes = []u8

struct S {
	x: C
	y: Bytes
}
`)
	for _, d := range b.m.Diags {
		if d.Code == diag.E0907 && fact(d, "cycle") != "[A B A]" {
			t.Errorf("cycle = %s", fact(d, "cycle"))
		}
	}
	s := b.decl("S").(*types.StructDecl)
	if s.Fields[0].Type != types.Poison || b.ts(s.Fields[1].Type, nil) != "[]u8" {
		t.Errorf("S fields %s %s", b.ts(s.Fields[0].Type, nil), b.ts(s.Fields[1].Type, nil))
	}
}

func TestDeclSignatures(t *testing.T) {
	w := newWorld(t, true)
	b := checkCodes(t, w, "m", opt{}, "", `struct Stack2[T] {
	items: []T
}

Stack2[U].Push(mut self, x: U into self) {
}

Stack2[U].Peek[V: Plain](&self, v: V): ?&U {
	return none
}

interface Shape {
	Area(&self): f64
	Scale[K](self, k: K): Self
}

struct Sq {
	side: f64
}

impl Shape for Sq {
	Area(&self): f64 {
		return self.side
	}

	Scale[K](self, k: K): Sq {
		return self
	}
}

Load(id: int, var n: int, const m: int): Oops|Gone!str {
	return "x"
}

Div(a: int, b: int): int requires b != 0 ensures result >= 0 {
	return a
}

error Oops
error Gone
`)
	push := b.sigOf("Stack2:Push")
	if len(push.Generics) != 1 || push.OwnGenerics != 0 || push.Receiver != types.ByMut || len(push.Params) != 2 {
		t.Fatalf("Push sig %+v", push)
	}
	if got := b.ts(push.Params[0].Type, push.Generics); got != "mut Stack2[U]" {
		t.Errorf("Push self = %s", got)
	}
	if push.Params[1].Into != 1 {
		t.Errorf("Push x into = %d", push.Params[1].Into)
	}
	peek := b.sigOf("Stack2:Peek")
	if len(peek.Generics) != 2 || peek.OwnGenerics != 1 || len(peek.Generics[1].Bounds) != 1 || peek.Generics[1].Bounds[0].Kind != types.BoundPlain {
		t.Fatalf("Peek generics %+v", peek.Generics)
	}
	if got := b.ts(peek.Result, peek.Generics); got != "?&U" {
		t.Errorf("Peek result %s", got)
	}
	sh := b.decl("Shape").(*types.InterfaceDecl)
	if len(sh.Methods) != 2 || w.tab.Kind(sh.Methods[1].Result) != types.KSelf || sh.Methods[1].OwnGenerics != 1 {
		t.Fatalf("Shape methods %+v", sh.Methods)
	}
	impl := b.decl("#impl(m.Shape for m.Sq)")
	id, ok := impl.(*types.ImplDecl)
	if !ok || len(id.Methods) != 2 || b.ts(id.For, nil) != "Sq" || id.Bound.Item != b.item("Shape") {
		t.Fatalf("impl decl %+v", impl)
	}
	area := w.items.Get(id.Methods[0]).Decl.(*types.FuncDecl)
	if got := b.ts(area.Sig.Params[0].Type, nil); got != "&Sq" {
		t.Errorf("impl Area self = %s", got)
	}
	load := b.sigOf("Load")
	if load.Result != types.Str || len(w.tab.Members(load.Errors)) != 2 || !load.Params[1].Var || !load.Params[2].Const {
		t.Errorf("Load sig %+v", load)
	}
	div := b.sigOf("Div")
	if len(div.Contracts) != 2 || div.Contracts[0].Kind != types.Requires || div.Contracts[1].Kind != types.Ensures {
		t.Fatalf("Div contracts %+v", div.Contracts)
	}
	req := w.items.Get(div.Contracts[0].Body).Decl.(*types.FuncDecl)
	if len(req.Sig.Params) != 1 || b.ts(req.Sig.Params[0].Type, nil) != "&int" || req.Sig.Result != types.Bool {
		t.Errorf("requires predicate sig %+v", req.Sig)
	}
	ens := w.items.Get(div.Contracts[1].Body).Decl.(*types.FuncDecl)
	if len(ens.Sig.Params) != 1 || w.in.Str(ens.Sig.Params[0].Name) != "result" {
		t.Errorf("ensures predicate sig %+v", ens.Sig)
	}
}

func TestE0412_PrivateInSignature(t *testing.T) {
	w := newWorld(t, true)
	// Accepted: Main with private errors; an exported struct with a private
	// field of a private type; a private function.
	checkCodes(t, w, "app", opt{bin: true}, "", `error Oops

struct Secret {
	x: int
}

export struct Pub {
	s: Secret
}

Helper(s: Secret): Oops!void {
}

export Main(sys: &System): Oops!void {
}
`)
	b := checkCodes(t, w, "lib", opt{}, "E0412@10:12 E0412@13:13 E0412@13:21 E0412@16:38 E0412@18:21", `error Oops

type Bytes = []u8

struct Secret {
	x: int
}

export struct Pub {
	export s: Secret
}

export F(b: Bytes): Oops!void {
}

export interface Shape { Get(&self): Secret }

export type Alias = Secret
`)
	for _, d := range b.diags() {
		if fact(d, "type") == "" || fact(d, "item") == "" || fact(d, "declared_at") == "" {
			t.Errorf("E0412 facts missing: %s", dump(w, []*diag.Diagnostic{d}))
		}
	}
}

func TestE0410_ExportedKind(t *testing.T) {
	w := newWorld(t, true)
	b := checkCodes(t, w, "m", opt{}, "E0410@1:15 E0410@5:15", `export struct H {
	name: str
}

export struct L {
	r: Res
}

export linear struct Res {
	x: int
}

struct Stack2 {
	name: str
}

export struct Pair[T] {
	first: T
}

export view struct V {
	name: str
}

export linear struct OK {
	export r: Res
}
`)
	got := map[string]string{}
	for _, d := range b.diags() {
		got[w.lc(d.At)] = fact(d, "needs") + " " + fact(d, "fields")
		if len(d.Fixes) != 1 || d.Fixes[0].Class != diag.Proven {
			t.Errorf("E0410 at %s: fixes %+v", w.lc(d.At), d.Fixes)
		}
	}
	if got["1:15"] != "[plain view] [name]" || got["5:15"] != "[linear] [r]" {
		t.Errorf("E0410 facts %v", got)
	}
}

func TestE0410_LinearDoesNotExcuseCarrier(t *testing.T) {
	w := newWorld(t, true)
	b := checkCodes(t, w, "m", opt{}, "E0410@1:22", `export linear struct H {
	name: str
}

export view linear struct H2 {
	name: str
}
`)
	if n := fact(b.diags()[0], "needs"); n != "[view]" {
		t.Errorf("needs = %s", n)
	}
	if fx := b.diags()[0].Fixes[0].Edits[0]; fx.Text != "view " {
		t.Errorf("fix text %q", fx.Text)
	}
}

func TestE0410_MutView(t *testing.T) {
	w := newWorld(t, true)
	b := checkCodes(t, w, "m", opt{}, "E0410@13:20 E0410@1:15 E0410@5:20 E0410@9:15", `export struct PlainCellCarrier {
	c: Cell[&int]
}

export view struct ViewCellCarrier {
	c: Cell[&int]
}

export struct PlainLinMut {
	a: LinMut
}

export view struct ViewLinMut {
	a: LinMut
}

export linear mut view struct LinMut {
	x: int
}

export mut view struct OKMutView {
	c: Cell[&int]
}

export linear mut view struct OKLinearMutView {
	a: LinMut
}

export view struct NonCarrierCellOnlyView {
	c: Cell[int]
}
`)
	got := map[string]string{}
	fixes := map[string]string{}
	for _, d := range b.diags() {
		loc := w.lc(d.At)
		got[loc] = fact(d, "needs")
		if len(d.Fixes) == 1 && len(d.Fixes[0].Edits) == 1 {
			fixes[loc] = d.Fixes[0].Edits[0].Text
		}
	}
	wantNeeds := map[string]string{
		"1:15":  "[mut view]",
		"5:20":  "[mut view]",
		"9:15":  "[linear mut view]",
		"13:20": "[linear mut view]",
	}
	wantFixes := map[string]string{
		"1:15":  "mut view ",
		"5:20":  "mut ",
		"9:15":  "linear mut view ",
		"13:20": "linear mut ",
	}
	for loc, wn := range wantNeeds {
		if got[loc] != wn {
			t.Errorf("at %s: needs %q, want %q", loc, got[loc], wn)
		}
		if fixes[loc] != wantFixes[loc] {
			t.Errorf("at %s: fix %q, want %q", loc, fixes[loc], wantFixes[loc])
		}
	}
}

func TestE0410_ViewNeedsLinearFixOrder(t *testing.T) {
	w := newWorld(t, true)
	src := `export linear struct Res {
	x: int
}

export view struct ViewLin {
	r: Res
}
`
	b := checkCodes(t, w, "m", opt{}, "E0410@5:20", src)
	d := b.diags()[0]
	if got := fact(d, "needs"); got != "[linear]" {
		t.Fatalf("needs %q, want [linear]", got)
	}
	if len(d.Fixes) != 1 || len(d.Fixes[0].Edits) != 1 {
		t.Fatalf("fixes %+v", d.Fixes)
	}
	ed := d.Fixes[0].Edits[0]
	patched := src[:ed.Span.Start] + ed.Text + src[ed.Span.End:]
	if !strings.Contains(patched, "export linear view struct ViewLin") {
		t.Errorf("fix produced invalid syntax:\n%s", patched)
	}
}

func TestE0410_RecursivePlainViewLoanInterior(t *testing.T) {
	w := newWorld(t, true)
	b := checkCodes(t, w, "m", opt{}, "E0410@10:20", `view struct RoList[T] {
	p: *T
}

struct Node {
	next: RoList[Node]
	c: Cell[?&i32]
}

export view struct Bad {
	xs: RoList[Node]
}
`)
	if got := fact(b.diags()[0], "needs"); got != "[mut view]" {
		t.Errorf("needs %q, want [mut view]", got)
	}
}

func TestMain(t *testing.T) {
	w := newWorld(t, true)
	for i, form := range []string{"", ": int", ": Oops!void", ": Oops!int"} {
		b := checkCodes(t, w, "ok"+string(rune('a'+i)), opt{bin: true}, "", "error Oops\n\nexport Main(sys: &System)"+form+" {\n}\n")
		if b.m.Main() == types.NoItem {
			t.Errorf("form %q: Main() is NoItem", form)
		}
	}
	checkCodes(t, w, "gen", opt{bin: true}, "E0301@1:8", "export Main[T](sys: &System) {\n}\n")
	checkCodes(t, w, "arg", opt{bin: true}, "E0301@1:8", "export Main(x: int) {\n}\n")
	checkCodes(t, w, "res", opt{bin: true}, "E0301@1:8", "export Main(sys: &System): str {\n}\n")
	checkCodes(t, w, "priv", opt{bin: true}, "E0301@1:1", "Main(sys: &System) {\n}\n")
}

func TestMain_MissingInBinRoot(t *testing.T) {
	w := newWorld(t, true)
	// The missing-Main E0201 is resolve's (it emits it for a bin root); check
	// adds nothing, so the module has exactly one diagnostic.
	b := w.add("app", opt{bin: true}, "F() {\n}\n")
	if got := codes(w, b.diags()); got != "E0201@1:1" {
		t.Fatalf("bin root without Main: %q", got)
	}
	for _, d := range b.m.Diags {
		t.Errorf("check emitted %v", d.Code)
	}
}

func TestMain_LibraryHasNoEntryRule(t *testing.T) {
	w := newWorld(t, true)
	b := checkCodes(t, w, "lib", opt{}, "", "Main(x: int) {\n}\n")
	if b.m.Main() != types.NoItem {
		t.Errorf("a library has an entry")
	}
}

func TestStdIntrinsicNoE0101(t *testing.T) {
	w := newWorld(t, true)
	b := checkCodes(t, w, "std.text", opt{std: true}, "", "export str.Shout(&self): int\n\nexport Twice(x: int): int\n")
	for _, k := range []string{"str:Shout", "Twice"} {
		fd := b.decl(k).(*types.FuncDecl)
		if !fd.Bodiless || !fd.Intrinsic {
			t.Errorf("%s: Bodiless %v Intrinsic %v", k, fd.Bodiless, fd.Intrinsic)
		}
	}
	if got := b.ts(b.sigOf("str:Shout").Params[0].Type, nil); got != "&str" {
		t.Errorf("str.Shout self = %s", got)
	}
	p := checkCodes(t, w, "tp", opt{testPrelude: "export Helper(x: int): int\n"}, "", "F(): int {\n\treturn Helper(1)\n}\n")
	fd := p.decl("Helper").(*types.FuncDecl)
	if !fd.Bodiless || fd.Intrinsic {
		t.Errorf("test prelude Helper: Bodiless %v Intrinsic %v", fd.Bodiless, fd.Intrinsic)
	}
	for _, u := range p.m.Units() {
		if p.key(u) == "Helper" {
			t.Errorf("a test-prelude declaration is a unit")
		}
	}
}

func TestBodilessAfterParseError(t *testing.T) {
	// The parser reports E0101 "function needs a body" and keeps the
	// FuncDecl; check adds nothing and raises no ICE (reading (j)).
	w := newWorld(t, true)
	b := w.add("m", opt{parseErrors: true}, "F(x: int): int\n\nG() {\n}\n")
	for _, d := range b.m.Diags {
		t.Errorf("check emitted %v: %s", d.Code, d.Message)
	}
	if fd := b.decl("F").(*types.FuncDecl); !fd.Bodiless || fd.Intrinsic {
		t.Errorf("F: Bodiless %v Intrinsic %v", fd.Bodiless, fd.Intrinsic)
	}
}

func TestDeclNeverTripsFinality(t *testing.T) {
	// Declarations that reach facts only through queued checks: a module
	// whose fn types and generic bounds mention its own structs must not
	// raise the facts walk's ICE (E0000).
	w := newWorld(t, true)
	checkCodes(t, w, "m", opt{}, "E0701@6:10", `struct Node {
	next: fn(&Node): &Node
	cell: Cell[Node2]
}

type X = Need[Res]

struct Need[T: Plain] {
	x: T
}

linear struct Res {
	x: int
}

struct Node2 {
	x: int
}
`)
}

func TestDeclReadSets(t *testing.T) {
	w := newWorld(t, true)
	b := checkCodes(t, w, "m", opt{}, "", "struct S {\n\tc: Cell[int]\n}\n")
	var all []string
	for _, r := range b.m.Reads {
		for _, x := range r.Reads.Sorted() {
			all = append(all, x.Key.String())
		}
	}
	if !strings.Contains(strings.Join(all, " "), "std.core.Cell") {
		t.Errorf("S's reads %v do not name std.core.Cell", all)
	}
}

// TestE0303_DefaultDiscriminant: a default discriminant
// (the previous value plus 1) that does not fit the representation is
// E0303 at the variant, once per run of defaults, and never wraps into a
// false E0206.
func TestE0303_DefaultDiscriminant(t *testing.T) {
	cases := []struct{ name, src, want, value, typ string }{
		{"u8", "enum E: u8 {\n\tA = 255\n\tB\n}\n", "E0303@3:2", "256", "u8"},
		{"i8", "enum E: i8 {\n\tA = 127\n\tB\n}\n", "E0303@3:2", "128", "i8"},
		{"u64", "enum E: u64 {\n\tA = 18446744073709551615\n\tB\n}\n", "E0303@3:2", "18446744073709551616", "u64"},
		{"no wrap into a duplicate", "enum E: u8 {\n\tA = 255\n\tB\n\tC\n\tD = 1\n}\n", "E0303@3:2", "256", "u8"},
		{"negative to zero", "enum E: i8 {\n\tA = -1\n\tB\n\tC = 127\n}\n", "", "", ""},
		{"last fits", "enum E: u8 {\n\tA = 254\n\tB\n}\n", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t, true)
			b := checkCodes(t, w, "m", opt{}, tc.want, tc.src)
			if tc.want == "" {
				return
			}
			d := b.diags()[0]
			if fact(d, "value") != tc.value || fact(d, "type") != tc.typ {
				t.Fatalf("facts %s", dump(w, b.diags()))
			}
		})
	}
}

func TestPlainView_InvariantAAndExports(t *testing.T) {
	t.Run("good phantom and slice handles", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t, true)
		checkCodes(t, w, "m", opt{}, "", `export plain view struct Ref[T] {
	idx: u32
}

export plain view struct Span[T] {
	data: []T
	lo: u32
	hi: u32
}

export plain view struct Pair[T] {
	a: Ref[T]
	b: str
}

export linear struct Res {
	fd: int
}

export F(r: Ref[Res], s: Span[Mutex[?&i32]]): (a: Ref[Res], b: Ref[Res]) from r {
	_ = s
	return (a: r, b: r)
}
`)
	})
	t.Run("rejects non-plain or non-freeze fields", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			name string
			src  string
			want string
		}{
			{"raw ptr", "plain view struct S {\n\tp: *u8\n}\n", "E0408@2:2"},
			{"mut ref", "plain view struct S {\n\tp: mut i32\n}\n", "E0408@2:2"},
			{"cell", "plain view struct S {\n\tc: Cell[u32]\n}\n", "E0408@2:2"},
			{"mutex", "plain view struct S {\n\tm: Mutex[u32]\n}\n", "E0408@2:2"},
			{"chan", "plain view struct S {\n\tch: Chan[u32]\n}\n", "E0408@2:2"},
			{"arena", "plain view struct S {\n\ta: Arena\n}\n", "E0408@2:2"},
			{"mut view", "mut view struct MV {\n\tx: int\n}\n\nplain view struct S {\n\tmv: MV\n}\n", "E0408@6:2"},
			{"non-plain view", "view struct V {\n\tx: int\n}\n\nplain view struct S {\n\tv: V\n}\n", "E0408@6:2"},
			{"type param by value", "plain view struct S[T] {\n\tx: T\n}\n", "E0408@2:2"},
			{"plain-bounded type param by value", "plain view struct S[T: Plain] {\n\tx: T\n}\n", "E0408@2:2"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				w := newWorld(t, true)
				checkCodes(t, w, "m", opt{}, tc.want, tc.src)
			})
		}
	})
	t.Run("E0410 recommends plain view vs view", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t, true)
		b := checkCodes(t, w, "m", opt{unsafe: true}, "E0410@12:15 E0410@1:15 E0410@7:15", `export struct SpanStr {
	buf: str
	lo: u32
	hi: u32
}

export struct GenericByVal[T] {
	export v: T
	buf: str
}

export struct RawAndCarrier {
	p: *u8
	buf: str
}

export plain view struct OKPlain {
	buf: str
	lo: u32
	hi: u32
}
`)
		got := map[string]string{}
		for _, d := range b.diags() {
			got[w.lc(d.At)] = fact(d, "needs")
		}
		if got["1:15"] != "[plain view]" || got["7:15"] != "[view]" || got["12:15"] != "[view]" {
			t.Fatalf("E0410 needs = %v", got)
		}
	})
	t.Run("rejects unsafe impl Send and Share on plain view", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t, true)
		checkCodes(t, w, "m", opt{unsafe: true}, "E0704@5:8 E0704@8:8", `export plain view struct Handle {
	idx: u32
}

unsafe impl Send for Handle {
}

unsafe impl Share for Handle {
}
`)
	})
	t.Run("records Slice impl on plain view struct", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t, true)
		b := checkCodes(t, w, "m", opt{}, "", `export plain view struct Span[T] {
	data: []T
}

impl[T] Slice[T] for Span[T] {
}
`)
		spanInt := w.tab.Named(b.item("Span"), []types.GenericArg{types.TypeArg(types.Int)})
		if !w.c.Facts(spanInt).SliceImpl {
			t.Fatalf("expected SliceImpl on plain view struct Span[int]")
		}
	})
	t.Run("skips E0410 when export plain view fails Invariant A", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t, true)
		checkCodes(t, w, "m", opt{}, "E0408@2:2", `export plain view struct Bad {
	c: Cell[u32]
}
`)
	})
}
