// Why: regression tests for faults once found in resolve, each pinned so it
// cannot return.
// What: pending names read like handwritten ones, the generated prelude, E0207
// and E0201 fixes, destructuring, later-wins collisions, reads, synonyms,
// methods on generated types, poisoned tasks and habits.
// How: modules with gen lines (genModule, preludeWorld) compared with their
// handwritten equivalents through the helpers_test.go harness.

package resolve

import (
	"strings"
	"testing"

	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/types"
)

// genModule adds std.derive and begins a module with one gen line and src,
// finished but not settled.
func genModule(t *testing.T, w *world, src string) *built {
	t.Helper()
	if _, ok := w.mods["std.derive"]; !ok {
		w.add("std.derive", opt{std: true}, "export Eq(g: int)\n")
	}
	return w.add("main", opt{noSettle: true}, "import std.derive\n\nstruct User {}\ngen derive.Eq(User)\n"+src)
}

// TestPendingPathTypeArgsReading: on a handwritten use of a generated generic
// type the TypeArgs slots get the reading the handwritten type would give
// them.
func TestPendingPathTypeArgsReading(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	b := genModule(t, w, "F(b: Buf[16], c: Buf2[int], d: Buf[1 + 2]) { }\n")
	b.m.AddGenerated([]GeneratedFile{b.generate("struct Buf[const N: int] {}\nstruct Buf2[T] {}\n")})
	b.settle()
	b.want(t)
	got := readings(b)
	if got["16"] != ReadExpr || got["int"] != ReadType || got["1 + 2"] != ReadExpr {
		t.Errorf("readings %v", got)
	}
	noPending(t, b)

	// The handwritten equivalent reads the same.
	w2 := newWorld(t, true)
	h := w2.add("main", opt{}, "struct Buf[const N: int] {}\nstruct Buf2[T] {}\nF(b: Buf[16], c: Buf2[int], d: Buf[1 + 2]) { }\n")
	if hr := readings(h); hr["16"] != got["16"] || hr["int"] != got["int"] || hr["1 + 2"] != got["1 + 2"] {
		t.Errorf("handwritten %v, generated %v", hr, got)
	}
}

// TestPendingTypeArgNotATypeIsE0101: a literal in a type slot is E0101
// "expected a type", also when the head was pending.
func TestPendingTypeArgNotATypeIsE0101(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	b := genModule(t, w, "F(c: Buf2[1]) { }\n")
	b.m.AddGenerated([]GeneratedFile{b.generate("struct Buf2[T] {}\n")})
	b.settle()
	b.want(t, "E0101@5:11")
}

// TestPendingMemberIndexReading: with a pending root, `Buf.Make[int]()` reads
// like the handwritten case, ReadUndecided, and `int` is never a const
// argument of Buf.
func TestPendingMemberIndexReading(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	b := genModule(t, w, "F() { _ = Buf.Make[int]() }\n")
	b.m.AddGenerated([]GeneratedFile{b.generate("struct Buf[const N: int] {}\n")})
	b.settle()
	b.want(t)
	got := readings(b)
	w2 := newWorld(t, true)
	h := readings(w2.add("main", opt{}, "struct Buf[const N: int] {}\nF() { _ = Buf.Make[int]() }\n"))
	if got["Buf.Make[int]"] != ReadUndecided || got["Buf.Make[int]"] != h["Buf.Make[int]"] || got["int"] == ReadExpr || got["int"] != h["int"] {
		t.Errorf("generated %v, handwritten %v", got, h)
	}
}

// preludeWorld is a world whose prelude module std.core has a gen line, so
// its Settle adds generated names. It returns the world and core,
// begun and finished, not settled.
func preludeWorld(t *testing.T) (*world, *built) {
	t.Helper()
	w := newWorld(t, false)
	w.add("std.derive", opt{std: true, noPrelude: true}, "export Eq(g: int)\n")
	w.cfg.PreludePath = "std.core"
	w.cfg.PreludeNames = []string{"List", "Path"}
	core := w.begin("std.core", opt{std: true, noPrelude: true}, "import std.derive\n\nexport struct List[T] {}\nstruct U {}\ngen derive.Eq(U)\n")
	w.core = core.id
	return w, core
}

// TestGeneratedPreludeNameReachesImporters: a prelude name std.core produces
// by generation is found by every importer, with gen lines or without, and is
// no reserved E0201.
func TestGeneratedPreludeNameReachesImporters(t *testing.T) {
	t.Parallel()
	w, core := preludeWorld(t)
	withGen := w.begin("main", opt{hasGen: w.hasGen}, "import std.derive\n\nstruct V {}\ngen derive.Eq(V)\nF(p: Path, l: List[int]) { }\n")
	plain := w.begin("plain", opt{hasGen: w.hasGen}, "F(p: Path) { }\nG(m: Missing) { }\n")
	core.finish(opt{noSettle: true})
	withGen.finish(opt{noSettle: true})
	plain.finish(opt{noSettle: true})
	core.m.AddGenerated([]GeneratedFile{core.generate("export struct Path {}\n")})
	core.settle()
	withGen.m.AddGenerated(nil)
	withGen.settle()
	plain.settle()
	path := w.item("std.core.Path").ID
	for _, b := range []*built{withGen, plain} {
		if r := b.ref("Path", 0); r.Kind != RefItem || types.ItemID(r.A) != path {
			t.Errorf("%s: Path is %+v\n%s", b.path, r, b.dump())
		}
		noPending(t, b)
	}
	withGen.want(t)
	plain.want(t, "E0201@2:6")
}

// TestE0207_FoldedModuleRootContinues: a misspelled module root resolves to
// the binding, its tail is looked up, and the binding is used (no E0208).
func TestE0207_FoldedModuleRootContinues(t *testing.T) {
	t.Parallel()
	for _, gen := range []bool{false, true} {
		w := newWorld(t, true)
		w.add("lib", opt{}, "export Make(): int { return 1 }\n")
		var b *built
		if gen {
			b = genModule(t, w, "import lib\nF() { _ = LIB.Make() }\n")
			b.m.AddGenerated(nil)
			b.settle()
		} else {
			b = w.add("main", opt{}, "import lib\n\nF() { _ = LIB.Make() }\n")
		}
		if len(b.only(diag.E0207)) != 1 || len(b.only(diag.E0208)) != 0 || len(b.diags) != 1 {
			t.Errorf("gen=%v: %s", gen, b.dump())
		}
		mk := w.item("lib.Make").ID
		if r := b.ref("Make", 0); r.Kind != RefItem || types.ItemID(r.A) != mk {
			t.Errorf("gen=%v: Make %+v", gen, r)
		}
		if r := fieldExprRefs(b)["LIB.Make"]; r.Kind != RefItem || r.Segs != 2 || types.ItemID(r.A) != mk {
			t.Errorf("gen=%v: LIB.Make %+v", gen, r)
		}
	}
}

// TestE0201_ImportFixQualifiesUse: the proven fix adds the import and
// qualifies the use; a file that already imports the module gets only the
// qualification; a binding name already taken makes it an alternative.
func TestE0201_ImportFixQualifiesUse(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	w.cat.exports["ReadText"] = []string{"std.fs"}
	w.add("std.fs", opt{std: true}, "export ReadText(): int { return 0 }\n")
	had := w.add("had", opt{}, "import io = std.fs\n\nF() { _ = io.ReadText()\n _ = ReadText() }\n")
	d := had.only(diag.E0201)[0]
	if len(d.Fixes) != 1 || len(d.Fixes[0].Edits) != 1 || d.Fixes[0].Edits[0].Text != "io.ReadText" || fact(d, "import") != "" {
		t.Errorf("already imported: %s", had.dump())
	}
	taken := w.add("taken", opt{}, "F(fs: int) { _ = ReadText()\n _ = fs }\n")
	d = taken.only(diag.E0201)[0]
	if len(d.Fixes) != 0 || len(d.Alternatives) == 0 {
		t.Errorf("binding name taken: %s", taken.dump())
	}
}

// TestE0207_RecaseToTakenNameIsAlternative: a recase whose name is taken is
// an alternative, not a proven fix.
func TestE0207_RecaseToTakenNameIsAlternative(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	b := w.add("main", opt{}, "struct Foo {}\nstruct foo {}\nstruct list {}\nF() {\n x := 1\n X := 2\n True := 3\n _ = x\n _ = X\n _ = True\n}\n")
	ds := b.only(diag.E0207)
	if len(ds) != 4 {
		t.Fatalf("%s", b.dump())
	}
	for _, d := range ds {
		if len(d.Fixes) != 0 || len(d.Alternatives) == 0 {
			t.Errorf("E0207@%s has a proven recase\n%s", b.lc(d.At), b.dump())
		}
	}
}

// TestDestructureVarSubpattern: `var` sets Var on every binding, a labeled
// field's sub-pattern included, and those bindings are LocalDestr.
func TestDestructureVarSubpattern(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("main", opt{}, "struct P {\n x: int\n y: int\n}\nF(p: P) {\n var P(x: a, y) := p\n a = 1\n y = 2\n}\n")
	b.want(t)
	body := itemInfo(b, "main.F").Body
	for _, l := range body.Locals[1:] {
		if n := b.in.Str(l.Name); n == "a" || n == "y" {
			if !l.Var || l.Kind != LocalDestr {
				t.Errorf("%s: %+v", n, l)
			}
		}
	}
}

// TestE0206_FieldAndMethodLaterWins: the collision (a field spelled exactly
// as a method) is reported at whichever declaration is later in (file
// order, offset), first the other.
func TestE0206_FieldAndMethodLaterWins(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("main", opt{}, "P.Len(&self): int { return 0 }\n", "struct P { Len: int }\n")
	ds := b.only(diag.E0206)
	if len(ds) != 1 || ds[0].At.File != b.srcs[1].ID || fact(ds[0], "first") == "" {
		t.Fatalf("%s", b.dump())
	}
	if f, _ := ds[0].FactValue("first"); f.At.File != b.srcs[0].ID {
		t.Errorf("first is not the method: %s", b.dump())
	}
	e := w.add("enum", opt{}, "Color.Red(self): int { return 0 }\nenum Color { Red }\n")
	if ds := e.only(diag.E0206); len(ds) != 1 || e.lc(ds[0].At) != "2:14" {
		t.Errorf("%s", e.dump())
	}
}

// TestReadExportsOnQualifiedFold: a miss of m.X whose diagnostic draws on m's
// names records ReadExports of m.
func TestReadExportsOnQualifiedFold(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	w.add("lib", opt{}, "export struct Foo {}\nexport struct Bar {}\nexport struct BAR {}\n")
	b := w.add("main", opt{}, "import lib\n\nF(x: lib.foo) { }\nG(y: lib.bar) { }\nH(z: list[int]) { }\n")
	b.want(t, "E0207@3:10", "E0201@4:10", "E0207@5:6")
	for _, k := range []string{"main.F", "main.G"} {
		if !hasRead(b, k, types.ReadExports, "lib", "") {
			t.Errorf("%s: no ReadExports lib %v", k, readsOf(b, k))
		}
	}
	if !hasRead(b, "main.H", types.ReadExports, "std.core", "") {
		t.Errorf("main.H: no ReadExports std.core %v", readsOf(b, "main.H"))
	}
}

// TestE1221_SharedUsesBinding: `Rc` becomes `sy.Shared` under `import sy =
// std.sync`, with no import edit.
func TestE1221_SharedUsesBinding(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	w.add("std.sync", opt{std: true}, "export struct Shared[T] {}\n")
	b := w.add("main", opt{}, "import sy = std.sync\n\nF(x: Rc[int], y: sy.Shared[int]) { }\n")
	d := b.only(diag.E1221)[0]
	if len(d.Fixes) != 1 || len(d.Fixes[0].Edits) != 1 || d.Fixes[0].Edits[0].Text != "sy.Shared" || fact(d, "import") != "" {
		t.Errorf("%s", b.dump())
	}
}

// TestProvenFixesConvergeFaultCases extends TestProvenFixesConverge with the
// earlier fault cases: applying every proven fix never leaves the same code at
// the same site.
func TestProvenFixesConvergeFaultCases(t *testing.T) {
	t.Parallel()
	srcs := []string{
		"import lib\nF() { _ = LIB.Make() }\n",
		"F() { _ = ReadText() }\n",
		"import lib\nF() { _ = ReadText()\n _ = lib.Make() }\n",
		"struct Foo {}\nstruct foo {}\nF(a: Foo) { _ = a }\n",
		"F() {\n x := 1\n X := 2\n _ = x\n _ = X\n}\n",
		"struct foo_bar {}\nF(a: foo_bar) { _ = a }\n",
		"import sy = std.sync\nF(x: Rc[int], y: sy.Shared[int]) { }\n",
		"F(x: Rc[int], y: Arc[int]) { }\n",
		"F() { x: Vec[usize] = nil\n _ = x }\n",
	}
	setup := func(w *world) {
		w.cat.exports["ReadText"] = []string{"std.fs"}
		w.add("std.sync", opt{std: true}, "export struct Shared[T] {}\n")
		w.add("std.fs", opt{std: true}, "export ReadText(): int { return 0 }\n")
		w.add("lib", opt{}, "export Make(): int { return 1 }\n")
	}
	for _, src := range srcs {
		w := newWorld(t, true)
		setup(w)
		b := w.add("main", opt{}, src)
		fixed := applyProven(t, w, b, src)
		w2 := newWorld(t, true)
		setup(w2)
		b2 := w2.add("main", opt{}, fixed)
		for _, d2 := range b2.diags {
			for _, d := range b.diags {
				if d2.Code == d.Code && d2.At.Start == d.At.Start {
					t.Errorf("%q -> %q: %v remains\n%s", src, fixed, d.Code, b2.dump())
				}
			}
			if d2.Code == diag.E0201 || d2.Code == diag.E0206 || d2.Code == diag.E0208 {
				t.Errorf("%q -> %q: the fix made %v\n%s", src, fixed, d2.Code, b2.dump())
			}
		}
	}
}

// TestMethodOnGeneratedType, for Type.Name owners and impl heads: a
// handwritten method or impl on a generated type is judged after
// generation, and the impl key is the one the handwritten type would give.
func TestMethodOnGeneratedType(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	w.add("std.derive", opt{std: true}, "export Eq(g: int)\n")
	// The method and the impl come before the gen line.
	m := w.add("main", opt{noSettle: true}, "import std.derive\n\nUserEq[T, n].Extra(&self) { }\nimpl Eq for UserEq { }\nstruct User {}\ngen derive.Eq(User)\n")
	m.m.AddGenerated([]GeneratedFile{m.generate("struct UserEq[T, const n: int] {}\n")})
	m.settle()
	m.want(t)
	ue := w.item("main.UserEq").ID
	got := w.s.MethodsOf(ue)
	if len(got) != 1 || w.items.Get(got[0]).Key.String() != "main.UserEq:Extra" {
		t.Errorf("MethodsOf %v", got)
	}
	w.item("main#impl(std.core.Eq for main.UserEq)")
	if r := m.ref("UserEq", 0); r.Kind != RefItem || types.ItemID(r.A) != ue {
		t.Errorf("owner ref %+v", r)
	}
	noPending(t, m)

	// Still missing after generation: one E0201 at the owner.
	w2 := newWorld(t, true)
	m2 := genModule(t, w2, "Gone.Extra(&self) { }\n")
	m2.m.AddGenerated(nil)
	m2.settle()
	m2.want(t, "E0201@5:1")
	noPending(t, m2)
}

// TestFailedTaskPoisonsItsNodes: an ICE in one task makes every node of the
// task RefPoison, and Settle retries none of its pending names.
func TestFailedTaskPoisonsItsNodes(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	w.add("std.derive", opt{std: true}, "export Eq(g: int)\n")
	b := w.begin("main", opt{}, "import std.derive\n\nstruct User {}\ngen derive.Eq(User)\nF(a: Nope, b: int) {\n    x := a\n    _ = x\n}\nG() { }\n")
	for _, r := range b.m.recs {
		if r.item.Key.String() == "main.F" {
			fd := r.node.(*ast.FuncDecl)
			// The first parameter's type is walked (and pending), then the
			// second's typed nil panics.
			params := []*ast.Param{fd.Sig.Params[0], {Type: (*ast.PathType)(nil)}}
			sig := *fd.Sig
			sig.Params = params
			r.node = &ast.FuncDecl{Base: fd.Base, Sig: &sig, Body: fd.Body}
		}
	}
	b.finish(opt{noSettle: true})
	b.m.AddGenerated(nil)
	b.settle()
	if n := len(b.only(diag.E0000)); n != 1 || len(b.only(diag.E0201)) != 0 {
		t.Errorf("%s", b.dump())
	}
	fi := b.info.Files[0]
	var f *ast.FuncDecl
	for _, it := range fi.File.Items {
		if fd, ok := it.(*ast.FuncDecl); ok && b.in.Str(fd.Sig.Name.Name.Name) == "F" {
			f = fd
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && fi.Refs[id.ID()].Kind != RefPoison {
			t.Errorf("ident %s at %s is %+v", b.in.Str(id.Name), b.lc(id.Span()), fi.Refs[id.ID()])
		}
		return n != nil
	})
	if r := b.ref("G", 0); r.Kind != RefItem {
		t.Errorf("G %+v", r)
	}
	noPending(t, b)
}

// TestHabitsWithoutSources: with no Config.Sources the line is not known,
// so habits keep facts.habits but are neither merged nor given a target.
func TestHabitsWithoutSources(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	w.cfg.Sources = nil
	b := w.add("main", opt{}, "F() { x: Vec[usize] = nil\n _ = x }\n")
	if len(b.diags) != 3 {
		t.Fatalf("%d diagnostics, want 3", len(b.diags))
	}
	for _, d := range b.diags {
		if fact(d, "habits") == "" || fact(d, "target") != "" {
			t.Errorf("%v: habits %q target %q", d.Code, fact(d, "habits"), fact(d, "target"))
		}
	}
}

// TestHabitE0207OnlyForTheRow: `True` folds to `true` but is no name habit,
// so it gets no facts.habits.
func TestHabitE0207OnlyForTheRow(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	b := w.add("main", opt{}, "F(): bool { return True }\n")
	if d := b.only(diag.E0207)[0]; fact(d, "habits") != "" || strings.Contains(b.dump(), "habits") {
		t.Errorf("%s", b.dump())
	}
}
