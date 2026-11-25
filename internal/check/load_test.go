// Why: a module whose declarations come from the cache must give its
// importers the same answers as the module checked from source.
// What: TestLoadEqualsSignatures, TestLoadRequiresClauseText,
// TestLoadReadsModuleImports, with loadLib.
// How: builds the same library once through Signatures and once through
// Checker.Load, then compares the items, facts and diagnostics importers
// see.

package check

import (
	"bytes"
	"reflect"
	"slices"
	"testing"

	"mk/internal/diag"
	"mk/internal/source"
	"mk/internal/types"
)

// loadLib exercises every part of the index Load rebuilds: fields (one
// private, folding like a Type.Name method), variants, an error,
// interfaces, an impl whose method collides with a Type.Name method
// (shadowed), an impl for an enum, Type.Name methods, a generic struct, a
// compile-time requires, an assertion and a Slice impl.
const loadLib = `export struct Point {
	export x: int
	export y: int
	name: int
}

export enum Shape {
	Circle(r: f64)
	Square(s: f64)
}

export error NotFound(code: int)

export interface Named {
	Name(&self): str
}

export interface Area {
	Area(&self): f64
}

impl Named for Point {
	Name(&self): str {
		return "p"
	}
}

impl Area for Shape {
	Area(&self): f64 {
		return 1.0
	}
}

export Point.Sum(&self): int {
	return self.x + self.y
}

Point.Name(&self): int {
	return self.name
}

export struct Box[T] {
	export v: T
}

export F[const N: int](): int
	requires N > 0 {
	return N
}

export struct Raw {
	p: *int
}

unsafe impl Send for Raw {
}

export struct Bag {
	export xs: List[int]
}

impl Slice[int] for Bag {
}
`

// loadApp touches every path of an importer that reads the imported index:
// fields and a Type.Name method, an impl method through lookup, a member
// miss that an imported interface declares (E0702), a variant pattern, a
// generic construction, an interface bound met by an imported impl, a
// shadowed method (the private Type.Name method, E0204), Send through an
// assertion, indexing through a Slice impl, and a restated clause.
const loadApp = `import lib

Use(p: &lib.Point, sh: lib.Shape): int {
	n := p.x + p.Sum()
	a := sh.Area()
	_ = a
	_ = p.Area()
	match sh {
		.Circle(r) {
			_ = r
		}
		.Square(s) {
			_ = s
		}
	}
	b := lib.Box(v: 1)
	_ = b.v
	_ = p.Name()
	return n + lib.F[2]()
}

Big[T: lib.Named](x: &T): str {
	return x.Name()
}

UseBig(p: &lib.Point): str {
	return Big(p)
}

H[const M: int](): int
	requires M > 0 {
	return lib.F[M]()
}

Missing(): lib.NotFound!int {
	fail lib.NotFound(code: 1)
}

Index(b: &lib.Bag): int {
	return b.xs[0]
}
`

// loadWorld builds std.core and lib from source in w and answers every
// compile-time requires by N > 0.
func loadWorld(t *testing.T) (*world, *built) {
	w := newWorld(t, true)
	fe := w.withEval()
	fe.fn = func(req *types.EvalRequest) (types.EvalResult, bool) {
		if req.What != types.EvalContract || len(req.GenericArgs) != 1 {
			return types.EvalResult{}, false
		}
		n, _ := w.c.constInt(req.GenericArgs[0].Const.Value)
		var v uint64
		if n > 0 {
			v = 1
		}
		return types.EvalResult{Value: w.c.scalarConst(types.Bool, 1, v)}, true
	}
	lib := w.add("lib", opt{unsafe: true}, loadLib)
	return w, lib
}

// loadedChecker is a second Checker over w's tables with std.core and lib
// loaded, as the driver would after decoding their interfaces.
func loadedChecker(t *testing.T, w *world, ids ...types.ModuleID) (*Checker, []*Module) {
	t.Helper()
	known := w.rs.Known()
	cfg := *w.cfg
	cfg.Known = &known
	setModuleImports(w)
	c := New(&cfg)
	var ms []*Module
	for _, id := range ids {
		var m *Module
		if ie := source.Guard("load", source.Span{}, func() { m = c.Load(id) }); ie != nil {
			t.Fatalf("Load(%d): %v", id, ie)
		}
		if len(m.Diags) != 0 {
			t.Fatalf("Load(%d) reported %s", id, dump(w, m.Diags))
		}
		ms = append(ms, m)
	}
	return c, ms
}

// checkedApp is everything an importer's checking produced: the stage-5
// and stage-6 diagnostics as JSON, the stage-5 read sets and every body
// with its diagnostics taken out.
type checkedApp struct {
	json   []byte
	reads  []ItemReads
	bodies []*Body
}

func checkApp(t *testing.T, w *world, c *Checker, b *built) checkedApp {
	t.Helper()
	var out checkedApp
	m := c.Signatures(&ModuleInput{Module: b.id, Files: b.files, Info: b.info})
	ds := append([]*diag.Diagnostic(nil), m.Diags...)
	for _, id := range m.Units() {
		it := w.items.Get(id)
		var body *Body
		if ie := source.Guard(it.Key.String(), it.Span, func() { body = c.Check(m, id) }); ie != nil {
			t.Fatalf("ICE in %s: %v", it.Key, ie)
		}
		ds = append(ds, body.Diags...)
		cp := *body
		cp.Diags = nil
		out.bodies = append(out.bodies, &cp)
	}
	out.json = diag.Order(ds, w.sm, diag.NoCap).WriteJSON(w.sm)
	out.reads = m.Reads
	return out
}

func TestLoadEqualsSignatures(t *testing.T) {
	w, lib := loadWorld(t)
	if ds := lib.diags(); codes(w, ds) != "E0206@23:2" {
		t.Fatalf("lib: %s", dump(w, ds)) // the shadowed Name
	}
	app := w.add("app", opt{stopBefore: true}, loadApp)
	want := checkApp(t, w, w.c, app)

	c2, loaded := loadedChecker(t, w, w.core, lib.id)
	for i, id := range []types.ModuleID{w.core, lib.id} {
		a, b := w.c.module(id), loaded[i]
		for _, f := range []struct {
			name string
			x, y any
		}{
			{"deps", a.deps, b.deps},
			{"members", a.members, b.members},
			{"primMembers", a.primMembers, b.primMembers},
			{"implTab", a.implTab, b.implTab},
			{"implList", a.implList, b.implList},
			{"shadowed", a.shadowed, b.shadowed},
			{"asserts", a.asserts, b.asserts},
			{"sliceImpls", a.sliceImpls, b.sliceImpls},
		} {
			if !reflect.DeepEqual(f.x, f.y) {
				t.Errorf("%s: %s differ after Load:\n%v\n%v", w.items.Module(id).Path, f.name, f.x, f.y)
			}
		}
	}
	for _, m := range loaded {
		if len(m.Units()) != 0 || len(m.Early()) != 0 || m.Main() != types.NoItem || len(m.Reads) != 0 {
			t.Errorf("a loaded module has Units %v, Early %v, Main %v, %d read sets; all are the cache's", m.Units(), m.Early(), m.Main(), len(m.Reads))
		}
	}
	lm := loaded[1]
	if len(lm.shadowed) != 1 || len(lm.implList) != 4 || // 3 written, 1 provided Format of its error
		len(lm.asserts) != 1 || len(lm.sliceImpls) != 1 {
		t.Fatalf("lib index: shadowed %d, impls %d, asserts %d, slice impls %d; the fixture should exercise each",
			len(lm.shadowed), len(lm.implList), len(lm.asserts), len(lm.sliceImpls))
	}

	got := checkApp(t, w, c2, app)
	if !bytes.Equal(got.json, want.json) {
		t.Errorf("diagnostics differ:\nSignatures: %s\nLoad:       %s", want.json, got.json)
	}
	if !bytes.Contains(want.json, []byte(`"E0702"`)) {
		t.Errorf("the fixture should draw E0702 through the imported interfaces: %s", want.json)
	}
	if !reflect.DeepEqual(got.reads, want.reads) {
		t.Errorf("stage-5 read sets differ")
	}
	if len(got.bodies) != len(want.bodies) {
		t.Fatalf("%d units, want %d", len(got.bodies), len(want.bodies))
	}
	for i := range want.bodies {
		if !reflect.DeepEqual(got.bodies[i], want.bodies[i]) {
			t.Errorf("unit %s differs after Load", w.items.Get(want.bodies[i].Item).Key)
		}
	}
}

// TestLoadRequiresClauseText: a loaded callee has no AST, so a false
// compile-time requires is still E0706 at the call, and it
// names the clause too, from Contract.Text, as a callee checked from source
// does.
func TestLoadRequiresClauseText(t *testing.T) {
	w, lib := loadWorld(t)
	app := w.add("app", opt{stopBefore: true}, "import lib\nG(): int {\n\treturn lib.F[0]()\n}\n")
	want := checkApp(t, w, w.c, app)
	c2, _ := loadedChecker(t, w, w.core, lib.id)
	got := checkApp(t, w, c2, app)
	for _, x := range []struct {
		name   string
		json   []byte
		clause bool
	}{{"Signatures", want.json, true}, {"Load", got.json, true}} {
		if !bytes.Contains(x.json, []byte(`"E0706"`)) {
			t.Errorf("%s: no E0706: %s", x.name, x.json)
		}
		if has := bytes.Contains(x.json, []byte(`N > 0`)); has != x.clause {
			t.Errorf("%s: clause text present %v, want %v: %s", x.name, has, x.clause, x.json)
		}
	}
}

// setModuleImports sets every module's types.Module.Imports from its
// resolve.Info, as the driver's setImports does from the import graph.
func setModuleImports(w *world) {
	for _, b := range w.mods {
		var out []types.ModuleID
		for _, fi := range b.info.Files {
			for _, bd := range fi.Imports {
				if !slices.Contains(out, bd.Module) {
					out = append(out, bd.Module)
				}
			}
		}
		w.items.Module(b.id).Imports = out
	}
}

// TestLoadReadsModuleImports: Load takes a module's imports from
// types.Module.Imports, not Config.Imports, which is nil here: Load must
// work with Config.Imports nil.
func TestLoadReadsModuleImports(t *testing.T) {
	w, lib := loadWorld(t)
	app := w.add("app", opt{stopBefore: true}, loadApp)
	want := checkApp(t, w, w.c, app)
	c, _ := loadedChecker(t, w, w.core, lib.id)
	if c.cfg.Imports != nil {
		t.Fatal("loadedChecker sets Config.Imports")
	}
	if got := checkApp(t, w, c, app); !bytes.Equal(got.json, want.json) {
		t.Errorf("diagnostics differ:\nSignatures: %s\nLoad:       %s", want.json, got.json)
	}
}
