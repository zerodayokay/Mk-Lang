// Why: lower must never meet a shape that is Poison or untyped without a
// diagnostic; each such gap is an ICE in lower.
// What: tests of SizeOf records, EvalArena, values and types in the wrong
// position, index arity, E0410, E0908, E0909, Slice's implicit bounds and
// stored fallibles, and FuzzNoSilentPoison.
// How: checks small programs and walks every body for Poison expressions
// lower would read (silentPoison).

package check

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/source"
	"mk/internal/types"
)

// TestSizeOfTypeArgumentRecorded: the type argument of SizeOf[T]() and
// AlignOf[T]() carries its type in Body.Types when it is open, so lower emits
// RvSizeOf of it in a generic body; a closed one folds to a constant and
// keeps NoType there (lower takes the constant).
func TestSizeOfTypeArgumentRecorded(t *testing.T) {
	w := newWorld(t, true)
	b := checkBody(t, w, "", "Size[T](): int {\n\treturn SizeOf[T]() + AlignOf[T]()\n}\n\nClosed(): int {\n\treturn SizeOf[i64]()\n}\n")
	for _, tc := range []struct {
		fn     string
		want   types.TypeID
		folded bool
	}{{"Size", w.tab.Param(0), false}, {"Closed", types.NoType, true}} {
		body := w.c.Check(b.m, b.item(tc.fn))
		var calls []*ast.CallExpr
		ast.Inspect(body.Node, func(n ast.Node) bool {
			if ce, ok := n.(*ast.CallExpr); ok {
				calls = append(calls, ce)
			}
			return n != nil
		})
		if len(calls) == 0 {
			t.Fatalf("%s: no call", tc.fn)
		}
		for _, ce := range calls {
			ix := ce.Fun.(*ast.IndexExpr)
			if got := body.TypeOf(ix.Args[0].ID()); got != tc.want {
				t.Errorf("%s: type argument %v, want %v", tc.fn, w.tab.TypeString(got, nil), w.tab.TypeString(tc.want, nil))
			}
			if body.TypeOf(ce.ID()) != types.Int {
				t.Errorf("%s: call type %v", tc.fn, body.TypeOf(ce.ID()))
			}
			u := body.Uses[body.slot(ce.ID())]
			if u.Kind != UseBuiltin || (Builtin(u.Index) != BuiltinSizeOf && Builtin(u.Index) != BuiltinAlignOf) {
				t.Errorf("%s: use %+v", tc.fn, u)
			}
			if folded := body.Consts != nil && body.Consts[body.slot(ce.ID())] != types.NoConst; folded != tc.folded {
				t.Errorf("%s: folded %v, want %v", tc.fn, folded, tc.folded)
			}
		}
	}
}

// TestE0705_EvalArena: EvalArena() is a builtin use of type &Arena in
// compile-time code (a const initializer, a requires over const parameters)
// and E0705 anywhere else.
func TestE0705_EvalArena(t *testing.T) {
	const pre = "Build(a: &Arena): int {\n\treturn 1\n}\n"
	runBodyCases(t, []bodyCase{
		{"const initializer", pre + "const K: int = Build(EvalArena())\n", ""},
		{"global initializer", pre + "var G: int = Build(EvalArena())\n", ""},
		{"compile-time requires", pre + "G[const N: int](): int\n\trequires N > Build(EvalArena())\n{\n\treturn N\n}\n", ""},
		{"run-time body", pre + "F(): int {\n\treturn Build(EvalArena())\n}\n", "E0705@5:15"},
		{"run-time requires", pre + "H(n: int): int\n\trequires n > Build(EvalArena())\n{\n\treturn n\n}\n", "E0705@5:21"},
		{"an argument", pre + "const K: int = Build(EvalArena(1))\n", "E0304@4:22"},
	})
	w := newWorld(t, true)
	b := checkBody(t, w, "", pre+"const K: int = Build(EvalArena())\n")
	body := w.c.Check(b.m, b.item("K"))
	var ea *ast.CallExpr
	ast.Inspect(body.Node, func(n ast.Node) bool {
		if ce, ok := n.(*ast.CallExpr); ok {
			if id, ok := ce.Fun.(*ast.Ident); ok && w.in.Str(id.Name) == "EvalArena" {
				ea = ce
			}
		}
		return n != nil
	})
	if ea == nil {
		t.Fatal("no EvalArena call")
	}
	if u := body.Uses[body.slot(ea.ID())]; u.Kind != UseBuiltin || Builtin(u.Index) != BuiltinEvalArena {
		t.Errorf("use %+v", u)
	}
	if got := body.TypeOf(ea.ID()); got != w.tab.Ref(w.tab.Named(w.c.known.Arena, nil)) {
		t.Errorf("type %s, want &Arena", w.tab.TypeString(got, nil))
	}
}

// TestE0301_ValueInTypePosition: a value, a member of a value or a member
// path read as a type is E0301 where it is written, never a silent Poison
// signature.
func TestE0301_ValueInTypePosition(t *testing.T) {
	runBodyCases(t, []bodyCase{
		{"local as a binding type", "error E\n\nA(h: int): E!void {\n\ta: (h) = 1\n}\n", "E0301@4:6"},
		{"self member as a result", "struct Index {\n\tn: int\n}\n\nIndex.A0(self): self.A {\n}\n", "E0301@5:17"},
		{"member of a primitive as a parameter", "Squares(b: int.A): int {\n\treturn 0\n}\n\nA(): int {\n\treturn Squares(0)\n}\n", "E0301@1:12"},
		{"method path as a type", "struct S {\n\tn: int\n}\n\nS.Make(): S {\n\treturn S(n: 1)\n}\n\nF(x: S.Make) {\n}\n", "E0301@9:6"},
	})
}

// TestE0301_TypeInValuePosition: a type name or a
// universe name written as a value is E0301.
func TestE0301_TypeInValuePosition(t *testing.T) {
	runBodyCases(t, []bodyCase{
		{"primitive", "A() {\n\tint\n}\n", "E0301@2:2"},
		{"primitive bound", "A() {\n\tx := u8\n}\n", "E0301@2:7"},
		{"universe function", "A() {\n\tx := Assert\n}\n", "E0301@2:7"},
		{"struct", "A() {\n\tx := Arena\n}\n", "E0301@2:7"},
		{"conversion still fine", "A(): u8 {\n\treturn u8(1)\n}\n", ""},
	})
}

// TestE0304_IndexArity: an index takes one value.
func TestE0304_IndexArity(t *testing.T) {
	runBodyCases(t, []bodyCase{
		{"two values", "A(xs: []i32): i32 {\n\treturn xs[0, 5]\n}\n", "E0304@2:9"},
		{"one value", "A(xs: []i32): i32 {\n\treturn xs[0]\n}\n", ""},
	})
}

// TestE0410_AtomicPrivateField: a private field of std.raw.Atomic
// counts as exported, so std's Mutex and AtomicInt need neither `view` nor
// a carrier, while a private Mutex field elsewhere still asks for `view`.
func TestE0410_AtomicPrivateField(t *testing.T) {
	w := newWorld(t, true)
	raw := w.add("std.raw", opt{std: true}, "export struct Atomic[T: Plain] {\n\tv: T\n}\n")
	if len(raw.diags()) > 0 {
		t.Fatalf("std.raw:\n%s", dump(w, raw.diags()))
	}
	w.c.known = w.rs.Known()
	if w.c.known.Atomic == types.NoItem {
		t.Fatal("Known.Atomic not set")
	}
	b := w.add("std.sync", opt{std: true}, "import std.raw\n\nexport struct Lock[T] {\n\tstate: raw.Atomic[u32]\n\tvalue: T\n}\n\nexport struct AtomicInt {\n\tv: raw.Atomic[i64]\n}\n\nexport struct Holder {\n\tm: Lock[int]\n}\n")
	if got := codes(w, b.diags()); got != "E0410@12:15" {
		t.Fatalf("diagnostics %q, want only Holder's E0410\n%s", got, dump(w, b.diags()))
	}
	f := w.c.Facts(w.tab.Named(b.item("AtomicInt"), nil))
	if f.Kind != types.Unique || f.View || f.Carrier {
		t.Errorf("AtomicInt facts %+v, want unique, no view, no carrier", f)
	}
}

// TestE0908_ConstInDeclarationType: a same-module const whose initializer
// reaches a same-module function cannot appear in a type, so it is E0908
// at the type, items [const, function]; a const of an imported call is
// evaluated there (EvalCall at StageSignatures).
func TestE0908_ConstInDeclarationType(t *testing.T) {
	w := newWorld(t, true)
	w.add("lib", opt{}, "export Three(): int {\n\treturn 3\n}\n")
	ev := w.withEval()
	ev.answers["lib.Three"] = types.EvalResult{Value: w.c.intConst(3)}
	b := w.add("m", opt{}, "Three(): int {\n\treturn 3\n}\n\nconst N: int = Three()\n\nconst M: int = N + 1\n\nexport struct S {\n\tx: [M]int\n}\n")
	ds := b.diags()
	if got := codes(w, ds); got != "E0908@10:6" {
		t.Fatalf("diagnostics %q\n%s", got, dump(w, ds))
	}
	if got := fact(ds[len(ds)-1], "items"); got != "[m.M m.Three]" {
		t.Errorf("items %s", got)
	}
	b2 := w.add("m2", opt{}, "import lib\n\nconst N: int = lib.Three()\n\nexport struct S {\n\tx: [N]int\n}\n")
	b2.wantClean(t)
	sd := w.items.Get(b2.item("S")).Decl.(*types.StructDecl)
	if got := w.tab.TypeString(sd.Fields[0].Type, nil); got != "[3]int" {
		t.Errorf("field type %s, want [3]int", got)
	}
}

// lowerSeeds are internal/lower's kept FuzzLower inputs that raised a
// tagged check-gap ICE: each must now draw an error from check.
var lowerSeeds = map[string]string{
	"01fb356f9c0dead5": "C00(){c0: i32=some(0) }",
	"19a396ae5e54339a": "struct Node{link:?mut Node}\nNode.A(self):?mut Node{if A:=mut self.link{return some()}\nreturn none}",
	"27e927eae37fc5fe": "struct Index {  a00000: List[str] } \nIndex.A0( self): self.A{} ",
	"84516518b425e994": "A(a00:System){a01:=()\nfor a in 0{}}",
	"87b07979a2bf4226": "A(xs:[]i32){b:=xs[0,5]\nb[0]}",
	"9eaf82743fb92385": "A(h: Heap):OutOfMemory!void{a:(h)}",
	"cfe2fc08fddb779a": "A(){int}",
	"d1f8f93246ace338": "struct Buf{data:[!0]u8\na00:int}\nBuf.A000(mut self){return self.data}",
	"dc3161bd3b2d4c4f": "A(){a:=try Arena}",
	"f64a060f0ab47a8a": "Squares(a:&Alloc,b:int.A):OutOfMemory!HeapList[int]{}\nA(sys:System):OutOfMemory!void{a:=sys.heap.Alloc()\na0:=try Squares(a,0)}",
	"f8d3dc75dac952db": "struct Lexer{a00:str\na01:int}\nLexer.A(self):str{a0000:=0\nreturn some(self)}",
	"gap (i)":          "error A\nerror B\nL1(): A!int {\n\treturn 1\n}\nGet2(r: (A | B)!int): int {\n\treturn 0\n}\nF2(): int {\n\treturn Get2(L1())\n}\n",
	// An enum called as a function and an E!T operand reached lower
	// silently.
	"CHK-A2 enum call": "enum C {\n R\n}\nF(): C { return C() }",
	"CHK-A2 repr enum": "enum Rep: u8 {\n\tA\n}\nF(x: u8): Rep { return Rep(x) }",
	"CHK-A4 operand":   "error E1\nF(): E1!int { return 1 }\nG(): int { return F() + 1 }",
	// An interface applied to arguments as a namespace head checked clean
	// and reached lower as a silent Poison.
	"SEMA-2 Slice[int].Fill": "F(xs: mut []int) {\n\tSlice[int].Fill(xs, 0)\n}\n",
	"SEMA-2 Get[int].Value":  "interface Get[T] {\n\tValue(&self): T\n}\nF(): int {\n\treturn Get[int].Value(3)\n}\n",
}

// reviewSeeds are valid inputs that lowered to a check-gap ICE: a
// conversion through an alias, namespace-form method calls and a closed
// head's fn value. FuzzNoSilentPoison starts from them too.
var reviewSeeds = []string{
	"type Byte = u8\nF(x: u8): int { return int(Byte(x)) }\n",
	"struct Pt {\n\tx: int\n}\nPt.Sum(&self): int { return self.x }\nPt.Bump(mut self) { self.x += 1 }\nPt.Get(self): int { return self.x }\n" +
		"F(): int {\n\tvar p := Pt(x: 1)\n\tPt.Bump(mut p)\n\treturn Pt.Sum(&p) + Pt.Get(p)\n}\n",
	"struct P[T] {\n\tv: T\n}\nP[T].Size(x: T): int { return 1 }\nF(): int {\n\tg := P[int].Size\n\treturn g(3)\n}\n",
}

func TestLowerSeedsDrawAnError(t *testing.T) {
	names := make([]string, 0, len(lowerSeeds))
	for k := range lowerSeeds {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		t.Run(k, func(t *testing.T) {
			t.Parallel()
			w, ds := checkNoICE(t, lowerSeeds[k])
			for _, d := range ds {
				if d.Code != diag.E0208 {
					return
				}
			}
			t.Errorf("only warnings:\n%s", dump(w, ds))
		})
	}
}

// silentPoison lists the expressions of the unit's body that are Poison,
// leaving out what check marks Poison on purpose and lower never reads: a
// callee and everything under it (the call carries the type and Use), the
// head of a member access (a type head `T.x`), and the `mut`/`&` of a
// binding condition (a binding mode).
func silentPoison(w *world, body *Body) []string {
	skip := map[ast.NodeID]bool{}
	var out []string
	mark := func(n ast.Node) {
		ast.Inspect(n, func(m ast.Node) bool {
			if m != nil {
				skip[m.ID()] = true
			}
			return m != nil
		})
	}
	ast.Inspect(body.Node, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		switch x := n.(type) {
		case *ast.CallExpr:
			mark(x.Fun)
		case *ast.FieldExpr:
			skip[x.X.ID()] = true
		case *ast.IndexExpr:
			// The head of a generic function value `Same[i32]` is
			// typed Poison as a FieldExpr's type head is; lower reads only
			// the IndexExpr's Use.
			if id := x.ID(); id >= body.Base && int(id-body.Base) < len(body.Uses) && body.Uses[id-body.Base].Kind == UseItem {
				skip[x.X.ID()] = true
			}
		case *ast.BindCond:
			if ux, ok := x.Value.(*ast.UnaryExpr); ok {
				skip[ux.ID()] = true
			}
		case *ast.MatchStmt:
			// `match &x` and `match mut x` type the & or mut Poison: they
			// are the match's mode, and lower unwraps them to x as check
			// does.
			if ux, ok := x.X.(*ast.UnaryExpr); ok && (ux.Op == ast.TokAmp || ux.Op == ast.TokMut) {
				skip[ux.ID()] = true
			}
		}
		if _, ok := n.(ast.Expr); !ok || skip[n.ID()] {
			return true
		}
		id := n.ID()
		if id < body.Base || int(id-body.Base) >= len(body.Types) {
			return true
		}
		if body.TypeOf(id) == types.Poison {
			out = append(out, w.items.Get(body.Item).Key.String()+" "+strings.TrimPrefix(strings.TrimPrefix(typeName(n), "*"), "ast.")+"@"+w.lc(n.Span()))
		}
		return true
	})
	return out
}

func typeName(n ast.Node) string {
	switch n.(type) {
	case *ast.Ident:
		return "Ident"
	case *ast.CallExpr:
		return "CallExpr"
	case *ast.IndexExpr:
		return "IndexExpr"
	case *ast.FieldExpr:
		return "FieldExpr"
	case *ast.UnaryExpr:
		return "UnaryExpr"
	case *ast.BinaryExpr:
		return "BinaryExpr"
	}
	return "Expr"
}

// TestNoSilentPoison is lower's invariant over the corpus: a doc block
// or conformance unit that draws no diagnostic at all through check (with
// the fake prelude; units that import a module the harness lacks are left
// out) has no Poison expression lower would read.
func TestNoSilentPoison(t *testing.T) {
	keys, texts := languageDocBlocks(t)
	units, _ := filepath.Glob(filepath.Join("..", "..", "tests", "conformance", "*", "*.mk"))
	progs, _ := filepath.Glob(filepath.Join("..", "..", "tests", "programs", "*.mk"))
	units = append(units, progs...)
	sort.Strings(units)
	for _, p := range units {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		k := filepath.ToSlash(p)
		keys = append(keys, k)
		texts[k] = strings.ReplaceAll(string(data), "\r\n", "\n")
	}
	for _, k := range keys {
		src := texts[k]
		if strings.Contains(src, "import ") {
			continue
		}
		t.Run(k, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t, true)
			var b *built
			if ie := source.Guard("m", source.Span{}, func() { b = w.add("m", opt{parseErrors: true}, src) }); ie != nil || b.parsed > 0 || len(b.diags()) > 0 {
				return
			}
			var bodies []*Body
			for _, id := range b.m.Units() {
				body := w.c.Check(b.m, id)
				if len(body.Diags) > 0 {
					return
				}
				bodies = append(bodies, body)
			}
			for _, body := range bodies {
				if bad := silentPoison(w, body); len(bad) > 0 {
					t.Errorf("Poison with no diagnostic: %s", strings.Join(bad, ", "))
				}
			}
		})
	}
}

// TestE0909_UntypedTypingCycle: untyped consts whose types depend on each
// other have no type, so stage 5 reports the cycle once, at the least-key
// const's name, with evalDiag's spelling (found by a silent-Poison fuzz
// run: `const A=A` lowered to lower's check-gap ICE). A typed cycle is left
// to the const's own evaluation, as before.
func TestE0909_UntypedTypingCycle(t *testing.T) {
	runBodyCases(t, []bodyCase{
		{"self", "const A = A\n", "E0909@1:7"},
		{"two", "const B = A + 1\n\nconst A = B + 1\n", "E0909@3:7"},
		{"typed is the evaluator's", "const A: int = B + 1\n\nconst B: int = A + 1\n", ""},
		{"no cycle", "const A = B + 1\n\nconst B = 1\n", ""},
	})
	w := newWorld(t, true)
	b := checkBody(t, w, "E0909@3:7", "const B = A + 1\n\nconst A = B + 1\n")
	d := b.diags()[0]
	if d.Message != "const cycle: A -> B -> A" || fact(d, "cycle") != "[A B]" {
		t.Errorf("%s %s", d.Message, fact(d, "cycle"))
	}
}

// FuzzNoSilentPoison is lower's invariant as a fuzz property: an input
// that draws nothing from resolve and check but warnings (E0208) has no
// Poison signature, field or expression lower would read (silentPoison).
// Sources with an import are skipped (the harness has no such modules).
func FuzzNoSilentPoison(f *testing.F) {
	keys, texts := languageDocBlocks(f)
	for _, k := range keys {
		f.Add(texts[k])
	}
	for _, s := range lowerSeeds {
		f.Add(s)
	}
	for _, s := range reviewSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		if strings.Contains(src, "import") {
			return
		}
		w := newWorld(t, true)
		var b *built
		if ie := source.Guard("m", source.Span{}, func() { b = w.add("m", opt{parseErrors: true}, src) }); ie != nil || b.parsed > 0 {
			return
		}
		for _, d := range b.diags() {
			if d.Code.String() != "E0208" {
				return
			}
		}
		for _, ii := range b.info.Items {
			it := w.items.Get(ii.Item)
			if d, ok := it.Decl.(*types.FuncDecl); ok && sigHasPoison(&d.Sig) {
				t.Errorf("silent Poison signature %s %q", it.Key, src)
			}
			if fl, ok := poisonField(it.Decl); ok {
				t.Errorf("silent Poison field %s.%s %q", it.Key, w.in.Str(fl), src)
			}
		}
		var bodies []*Body
		for _, id := range b.m.Units() {
			body := w.c.Check(b.m, id)
			for _, d := range body.Diags {
				if d.Code.String() != "E0208" {
					return
				}
			}
			bodies = append(bodies, body)
		}
		for _, body := range bodies {
			if bad := silentPoison(w, body); len(bad) > 0 {
				t.Errorf("silent: %s\n%q", strings.Join(bad, ", "), src)
			}
		}
	})
}

// TestSliceImplicitBounds: inside `Slice[T].Has` and `Index` T is
// bounded by Eq, so `x.Eq(x)` checks; at a call Has and Index need `T: Eq`
// and Fill and CopyFrom need `T: Plain`, else E0701 at the method name.
func TestSliceImplicitBounds(t *testing.T) {
	core := fakePrelude() + "export Slice[T].Has(&self, x: &T): bool {\n\treturn x.Eq(x)\n}\n" +
		"export Slice[T].Index(&self, x: &T): ?int {\n\tif x.Eq(x) {\n\t\treturn some(0)\n\t}\n\treturn none\n}\n" +
		"export Slice[T].Fill(mut self, x: T) {\n}\n" +
		"export Slice[T].CopyFrom(mut self, src: []T) {\n}\n"
	for _, tc := range []struct{ name, src, want string }{
		{"Eq element", "F(xs: []int): bool {\n\treturn xs.Has(&1)\n}\n", ""},
		{"no Eq", "struct P {\n\tn: int\n}\n\nF(xs: []P, p: P): bool {\n\treturn xs.Has(&p)\n}\n", "E0701@6:12"},
		{"Index no Eq", "struct P {\n\tn: int\n}\n\nF(xs: []P, p: P): ?int {\n\treturn xs.Index(&p)\n}\n", "E0701@6:12"},
		{"Fill plain", "F(xs: mut []int) {\n\txs.Fill(0)\n}\n", ""},
		{"Fill not plain", "F(xs: mut []Box[int], b: Box[int]) {\n\txs.Fill(b)\n}\n", "E0701@2:5"},
		{"CopyFrom plain", "F(xs: mut []int, ys: []int) {\n\txs.CopyFrom(ys)\n}\n", ""},
		{"CopyFrom not plain", "F(xs: mut []Box[int], ys: []Box[int]) {\n\txs.CopyFrom(ys)\n}\n", "E0701@2:5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorldCore(t, true, core)
			core := w.mods["std.core"]
			if got := codes(w, core.bodyDiags(t)); got != "" {
				t.Fatalf("std.core bodies: %s", got)
			}
			checkBody(t, w, tc.want, tc.src)
		})
	}
}

// TestE0802_StoredFallible: an E!T value is never stored or
// passed, so a fallible call as a construction field, an array or record
// element, a repeat value or some(..)'s argument is E0802 like an argument.
func TestE0802_StoredFallible(t *testing.T) {
	const pre = "error E\n\nF(): E!int {\n\treturn 1\n}\n\n"
	runBodyCases(t, []bodyCase{
		{"construction field", pre + "struct S {\n\tr: E!int\n}\n\nG(): S {\n\treturn S(r: F())\n}\n", "E0802@12:14"},
		{"array element", pre + "G(): int {\n\ta := [F(), F()]\n\treturn 0\n}\n", "E0802@8:13 E0802@8:8"},
		{"record field", pre + "G(): int {\n\ta := (x: F())\n\treturn 0\n}\n", "E0802@8:11"},
		{"repeat value", pre + "G(): int {\n\ta := [F(); 2]\n\treturn 0\n}\n", "E0802@8:8"},
		{"some", pre + "G(): ?(E!int) {\n\treturn some(F())\n}\n", "E0802@8:14"},
		{"try element is fine", pre + "G(): E!int {\n\ta := [try F(), 2]\n\treturn a[0]\n}\n", ""},
	})
}
