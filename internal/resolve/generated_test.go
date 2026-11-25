// Why: generated files join a module after its handwritten ones, and their
// names must resolve the same in any order and under concurrency.
// What: tests of generated files, known items, determinism and races.
// How: modules with gen lines resolved through the helpers_test.go harness,
// with generated files added before Settle; noPending checks the outcome.

package resolve

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/source"
	"mk/internal/syntax"
	"mk/internal/types"
)

// hasGen is the driver's HasGen over the world's modules.
func (w *world) hasGen(id types.ModuleID) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, b := range w.mods {
		if b.id == id {
			return b.m.hasGen && !b.m.settled
		}
	}
	return false
}

// generate parses a generated file for module b.
func (b *built) generate(text string, names ...NameOrigin) GeneratedFile {
	b.w.t.Helper()
	src := b.w.sm.Add(fmt.Sprintf("%s/gen/%d.mk", b.path, len(b.info.Files)), []byte(text))
	f, ds := syntax.ParseFile(src, b.w.in, syntax.Options{})
	for _, d := range ds {
		b.w.t.Fatalf("generated parse: %v %s", d.Code, d.Message)
	}
	var origins []NodeOrigin
	for _, it := range f.Items {
		origins = append(origins, NodeOrigin{Node: it.ID(), Origin: types.GenOrigin{Output: it.Span()}})
	}
	return GeneratedFile{File: f, Origins: origins, Names: names}
}

func TestPendingThenSettle(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	w.add("std.derive", opt{std: true}, "export Eq(g: int)\n")
	b := w.add("main", opt{noSettle: true}, "import std.derive\n\nstruct User {}\ngen derive.Eq(User)\nF(x: UserEq) { }\n")
	if r := b.ref("UserEq", 0); r.Kind != RefPending {
		t.Fatalf("before generation: %+v\n%s", r, b.dump())
	}
	b.m.AddGenerated([]GeneratedFile{b.generate("struct UserEq {}\n")})
	b.settle()
	b.want(t)
	if r := b.ref("UserEq", 0); r.Kind != RefItem || types.ItemID(r.A) != w.item("main.UserEq").ID {
		t.Errorf("after Settle: %+v", r)
	}
	if w.item("main.UserEq").Origin == nil {
		t.Error("generated item has no origin")
	}
	noPending(t, b)
}

func TestSettleReportsStillMissing(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	w.add("std.derive", opt{std: true}, "export Eq(g: int)\n")
	b := w.add("main", opt{noSettle: true}, "import std.derive\n\nstruct User {}\ngen derive.Eq(User)\nF(x: Nope, y: user) { }\n")
	if len(b.info.Diags) != 0 {
		t.Fatalf("Finish reported a pending name: %s", b.dump())
	}
	b.m.AddGenerated(nil)
	b.settle()
	b.want(t, "E0201@5:6", "E0207@5:15")
	noPending(t, b)
}

func TestPendingInImporterWithoutGen(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	w.add("std.derive", opt{std: true}, "export Eq(g: int)\n")
	a := w.begin("a", opt{}, "import std.derive\n\nexport struct User {}\ngen derive.Eq(User)\n")
	bb := w.begin("b", opt{hasGen: w.hasGen}, "import a\n\nF(x: a.Missing, y: a.User, z: a.UserEq) { }\n")
	a.finish(opt{noSettle: true})
	bb.finish(opt{noSettle: true})
	if r := bb.ref("Missing", 0); r.Kind != RefPending {
		t.Fatalf("a.Missing before a settles: %+v", r)
	}
	a.m.AddGenerated([]GeneratedFile{a.generate("export struct UserEq {}\n")})
	a.settle()
	bb.settle()
	a.want(t)
	bb.want(t, "E0201@3:8")
	if d := bb.diags[0]; fact(d, "module") != "a" {
		t.Errorf("%s", bb.dump())
	}
	if r := bb.ref("UserEq", 0); r.Kind != RefItem {
		t.Errorf("a.UserEq: %+v", r)
	}
	noPending(t, bb)
}

func TestGeneratedItemHidesPrelude(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	w.add("std.derive", opt{std: true}, "export Eq(g: int)\n")
	b := w.add("main", opt{noSettle: true}, "import std.derive\n\nstruct User {}\ngen derive.Eq(User)\nF(f: Format) {\n    g := fn() { }\n    g()\n}\n")
	lit := w.item("main.F#fn0").ID
	if r := b.ref("Format", 0); w.items.Get(types.ItemID(r.A)).Module == b.id {
		t.Fatal("hidden before generation")
	}
	b.m.AddGenerated([]GeneratedFile{b.generate("struct Format {}\n")})
	b.settle()
	b.want(t)
	if r := b.ref("Format", 0); r.Kind != RefItem || types.ItemID(r.A) != w.item("main.Format").ID {
		t.Errorf("Format after Settle: %+v", r)
	}
	if w.item("main.F#fn0").ID != lit {
		t.Error("literal item replaced")
	}
	if got := itemInfo(b, "main.F").Literals; len(got) != 1 || got[0] != lit {
		t.Errorf("literals %v", got)
	}
}

func TestNoPendingAfterSettle(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	w.add("std.derive", opt{std: true}, "export Eq(g: int)\n")
	for i, src := range []string{
		"F(x: Nope) { }\n",
		"import std.derive\n\nstruct U {}\ngen derive.Eq(U)\nF(x: Nope, y: ?Gone) { _ = q.r[Z] }\n",
	} {
		b := w.add(fmt.Sprint("m", i), opt{}, src)
		noPending(t, b)
	}
}

func TestPinnedNamesTakenAsGiven(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	w.add("std.derive", opt{std: true}, "export Eq(g: int)\n")
	w.add("lib", opt{}, "export struct Hidden2 {}\n")
	b := w.add("main", opt{noSettle: true}, "import std.derive\n\nstruct User {}\ngen derive.Eq(User)\n")
	text := "F(x: Pinned) { }\n"
	gf := b.generate(text)
	gf.Names = []NameOrigin{{Offset: uint32(strings.Index(text, "Pinned")), Kind: NamePinnedItem, Item: w.item("lib.Hidden2").ID}}
	b.m.AddGenerated([]GeneratedFile{gf})
	b.settle()
	b.want(t)
	if r := b.ref("Pinned", 0); r.Kind != RefItem || types.ItemID(r.A) != w.item("lib.Hidden2").ID {
		t.Errorf("pinned: %+v", r)
	}
}

func TestLiteralNamesSkipModuleScopes(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	w.add("lib", opt{}, "export struct Thing {}\n")
	gen := w.add("std.derive", opt{std: true}, "import lib\n\nexport Eq(g: int)\nexport struct Own {}\nUse(t: lib.Thing) { }\n")
	b := w.add("main", opt{noSettle: true}, "import std.derive\n\nstruct User {}\nstruct Own {}\ngen derive.Eq(User)\n")
	text := "F(a: lib.Thing, b: Own, c: Format) { }\n"
	gf := b.generate(text)
	for _, n := range []string{"lib", "Own", "Format"} {
		gf.Names = append(gf.Names, NameOrigin{Offset: uint32(strings.Index(text, n)), Kind: NameLiteral, Module: gen.id})
	}
	b.m.AddGenerated([]GeneratedFile{gf})
	ds, gs := b.m.Settle()
	if len(ds) != 0 || len(gs) != 1 || gs[0].Code != diag.E0201 {
		t.Fatalf("diags %v, generated %v", ds, gs)
	}
	if r := b.ref("lib", 0); r.Kind != RefModule {
		t.Errorf("lib through the generator's imports: %+v", r)
	}
	if r := b.ref("Format", 0); r.Kind != RefItem {
		t.Errorf("Format through the prelude: %+v", r)
	}
}

func TestLiteralNameAmbiguousBinding(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	w.add("x.m", opt{}, "export struct A {}\n")
	w.add("y.m", opt{}, "export struct A {}\n")
	gen := w.add("g", opt{}, "import x.m\n\nUseA(a: m.A) { }\n", "import y.m\n\nUseB(a: m.A) { }\n")
	if len(gen.m.rec.lit) != 0 {
		t.Errorf("an ambiguous binding is visible: %+v", gen.m.rec.lit)
	}
}

func TestGeneratedDiagsSeparated(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	w.add("std.derive", opt{std: true}, "export Eq(g: int)\n")
	b := w.add("main", opt{noSettle: true}, "import std.derive\n\nstruct User {}\ngen derive.Eq(User)\nH(x: Gone) { }\n")
	b.m.AddGenerated([]GeneratedFile{b.generate("G(x: Missing) { }\nstruct User {}\n")})
	ds, gs := b.m.Settle()
	if len(ds) != 1 || ds[0].Code != diag.E0201 {
		t.Errorf("handwritten: %v", ds)
	}
	codes := map[diag.Code]int{}
	for _, d := range gs {
		codes[d.Code]++
	}
	if codes[diag.E0201] != 1 || codes[diag.E0206] != 1 {
		t.Errorf("generated: %v", gs)
	}
}

func TestKnownItemsKeysFollowPreludePath(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	w.add("std.raw", opt{std: true}, "export struct Atomic[T] {}\n")
	w.add("std.prelude", opt{std: true}, "export struct Hash {}\n")
	k := w.s.Known()
	if k.Eq != w.item("std.core.Eq").ID || k.Arena != w.item("std.core.Arena").ID || k.Atomic != w.item("std.raw.Atomic").ID {
		t.Errorf("known %+v", k)
	}
	if k.Hash != w.item("std.core.Hash").ID {
		t.Error("Hash looked up outside the prelude path")
	}
	if k.Gen != types.NoItem {
		t.Error("absent key found")
	}
}

func TestFinishIndependentOfWorkers(t *testing.T) {
	t.Parallel()
	var sb strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&sb, "F%d(x: int): int {\n    y := x + nope%d\n    g := fn() { _ = x }\n    g()\n    return Lisst()\n}\n", i, i)
	}
	render := func(workers int) string {
		w := newWorld(t, true)
		b := w.add("main", opt{workers: workers}, sb.String())
		var out strings.Builder
		for _, d := range b.diags {
			fmt.Fprintf(&out, "%v@%s %s\n", d.Code, b.lc(d.At), d.Message)
		}
		for _, it := range b.info.Items {
			fmt.Fprintf(&out, "%s %d\n", w.items.Get(it.Item).Key, len(it.Reads.Sorted()))
		}
		return out.String()
	}
	if a, b := render(1), render(8); a != b {
		t.Errorf("workers change the result:\n%s\n---\n%s", a, b)
	}
}

func TestDiagnosticsIndependentOfOrder(t *testing.T) {
	t.Parallel()
	a := "F() { _ = Nope }\n"
	c := "G() { _ = Gone }\n"
	render := func(files ...string) string {
		w := newWorld(t, true)
		b := w.add("main", opt{}, files...)
		return strings.Join(b.codes(), " ")
	}
	if x, y := render(a, c), render(a, c); x != y {
		t.Errorf("%s != %s", x, y)
	}
}

func TestGuardTurnsPanicIntoE0000(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.begin("main", opt{}, "F() { }\nG() { }\n")
	// A tree no parser builds (a typed nil inside an interface field)
	// panics inside the walk; the task's Guard turns it into one E0000 and
	// the other task still resolves.
	for _, r := range b.m.recs {
		if r.item.Key.String() == "main.F" {
			fd := r.node.(*ast.FuncDecl)
			r.node = &ast.FuncDecl{Base: fd.Base, Sig: &ast.Signature{Params: []*ast.Param{{Type: (*ast.PathType)(nil)}}}}
		}
	}
	b.info = b.m.Finish(1)
	n := 0
	for _, d := range b.info.Diags {
		if d.Code == diag.E0000 {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d E0000", n)
	}
}

func TestGenericKindsConcurrentBegin(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	var wg sync.WaitGroup
	mods := make([]*built, 8)
	for i := range mods {
		var sb strings.Builder
		for j := 0; j < 50; j++ {
			fmt.Fprintf(&sb, "export struct G%d[T, const N: int] {}\n", j)
		}
		wg.Add(1)
		go func(i int, text string) {
			defer wg.Done()
			mods[i] = w.begin(fmt.Sprint("m", i), opt{}, text)
		}(i, sb.String())
	}
	wg.Wait()
	for _, m := range mods {
		m.finish(opt{})
	}
	var sb strings.Builder
	for i := range mods {
		fmt.Fprintf(&sb, "import m%d\n", i)
	}
	sb.WriteString("\nF(")
	for i := range mods {
		fmt.Fprintf(&sb, "a%d: m%d.G7[int, 3], ", i, i)
	}
	sb.WriteString(") { }\n")
	b := w.add("main", opt{}, sb.String())
	b.want(t)
	got := readings(b)
	if got["3"] != ReadExpr || got["int"] != ReadType {
		t.Errorf("readings %v", got)
	}
}

func TestFoldedIndexConcurrentMisses(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	var sb strings.Builder
	for j := 0; j < 50; j++ {
		fmt.Fprintf(&sb, "F%d(x: list[int]) { }\n", j)
	}
	a := w.begin("a", opt{}, sb.String())
	c := w.begin("c", opt{}, sb.String())
	var wg sync.WaitGroup
	for _, m := range []*built{a, c} {
		wg.Add(1)
		go func(m *built) {
			defer wg.Done()
			m.info = m.m.Finish(8)
		}(m)
	}
	wg.Wait()
	if len(a.info.Diags) != 50 || len(c.info.Diags) != 50 {
		t.Errorf("%d and %d E0207", len(a.info.Diags), len(c.info.Diags))
	}
}

// noPending checks that no node of a settled module is RefPending.
func noPending(t *testing.T, b *built) {
	t.Helper()
	for _, fi := range b.info.Files {
		ast.Inspect(fi.File, func(n ast.Node) bool {
			if n != nil && fi.Refs[n.ID()].Kind == RefPending {
				t.Errorf("%s: node at %s is RefPending", b.path, b.lc(n.Span()))
			}
			return n != nil
		})
	}
}

func TestLoadRebuildsScope(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	lib := w.add("lib", opt{}, "export struct Buf[const N: int] {}\nstruct Private {}\nBuf[N].Len(&self): int { return N }\n")
	// A second session over the same Items sees lib only through Load, with
	// the generic kinds iface would decode into Item.Decl.
	w.items.Get(w.item("lib.Buf").ID).Decl = &types.StructDecl{Generics: []types.GenericParam{{Const: true}}}
	s2 := NewSession(w.cfg)
	s2.Load(lib.id)
	if got := s2.MethodsOf(w.item("lib.Buf").ID); len(got) != 1 {
		t.Errorf("MethodsOf after Load: %v", got)
	}
	src := w.sm.Add("loaded/00.mk", []byte("import lib\n\nF(b: lib.Buf[4], p: lib.Private) { }\n"))
	f, _ := syntax.ParseFile(src, w.in, syntax.Options{})
	id := w.items.AddModule(&types.Module{Path: "loaded", Files: []source.FileID{src.ID}})
	m := s2.Begin(&ModuleInput{Module: id, Files: []*ast.File{f}, Imports: map[string]types.ModuleID{"lib": lib.id}})
	info := m.Finish(1)
	ds, _ := m.Settle()
	all := append(info.Diags, ds...)
	if len(all) != 1 || all[0].Code != diag.E0204 {
		t.Errorf("diags %v", all)
	}
	reading := ReadingNone
	ast.Inspect(f, func(n ast.Node) bool {
		if l, ok := n.(*ast.BasicLit); ok && l.Raw == "4" {
			reading = info.Files[0].Readings[l.ID()]
		}
		return n != nil
	})
	if reading != ReadExpr {
		t.Errorf("const argument of a loaded generic reads %v", reading)
	}
}

func TestModuleIsNotAValue(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	w.add("lib", opt{}, "export F() {}\n")
	b := w.add("main", opt{}, "import lib\n\nG() { _ = lib }\n")
	b.want(t, "E0201@3:11")
	if fact(b.diags[0], "expected") != "value" {
		t.Errorf("%s", b.dump())
	}
}

// A bare name in a pattern that a gen line may still declare is judged at
// Settle: a const it then names is a value test, else the binding is
// E0207, and Finish reports neither early.
func TestPatternNameWaitsForSettle(t *testing.T) {
	t.Parallel()
	for _, gen := range []string{"", "const Q = 1\n"} {
		w := newWorld(t, true)
		w.add("std.derive", opt{std: true}, "export Eq(g: int)\n")
		b := w.add("main", opt{noSettle: true}, "import std.derive\n\nstruct User {}\ngen derive.Eq(User)\nF(x: int): int {\n    return match x {\n        Q { 1 }\n        _ { 0 }\n    }\n}\n")
		if len(b.info.Diags) != 0 {
			t.Fatalf("Finish reported a pattern name a gen line may declare: %s", b.dump())
		}
		var files []GeneratedFile
		if gen != "" {
			files = append(files, b.generate(gen))
		}
		b.m.AddGenerated(files)
		b.settle()
		if gen == "" {
			b.want(t, "E0207@7:9")
			continue
		}
		b.want(t)
		if r := b.ref("Q", 0); r.Kind != RefItem || types.ItemID(r.A) != w.item("main.Q").ID {
			t.Errorf("Q after Settle: %+v\n%s", r, b.dump())
		}
		noPending(t, b)
	}
}
