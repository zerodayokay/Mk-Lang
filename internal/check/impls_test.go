// Why: the impl table and its coherence decide which method a call reaches,
// and a conflict or a wrong signature must be found at the impl.
// What: tests of impl conflicts (E0704), impl signatures (E0707), bounds
// (E0701), member namespaces, builtin bound impls and sibling collisions.
// How: checks modules with impls and compares codes and the impl table.

package check

import (
	"strings"
	"testing"

	"mk/internal/diag"
	"mk/internal/types"
)

const shapeSrc = `struct Square {
	side: f64
}

interface Shape {
	Area(&self): f64
}
`

func TestE0704_ImplConflict(t *testing.T) {
	t.Run("accepts", func(t *testing.T) {
		w := newWorld(t, true)
		checkCodes(t, w, "m", opt{}, "", shapeSrc+`
impl Shape for Square {
	Area(&self): f64 {
		return self.side * self.side
	}
}
`)
	})
	t.Run("blanket", func(t *testing.T) {
		w := newWorld(t, true)
		b := checkCodes(t, w, "m", opt{}, "E0704@9:1", shapeSrc+`
impl[T] Shape for T {
	Area(&self): f64 {
		return 1.0
	}
}
`)
		if r := fact(b.m.Diags[0], "reason"); r != "blanket" {
			t.Errorf("reason %q", r)
		}
	})
	t.Run("orphan", func(t *testing.T) {
		w := newWorld(t, true)
		w.add("a", opt{}, "export struct Square {\n\tside: f64\n}\n")
		w.add("b", opt{}, "export interface Shape {\n\tArea(&self): f64\n}\n")
		b := checkCodes(t, w, "c", opt{}, "E0704@3:1", "import a\nimport b\nimpl b.Shape for a.Square {\n\tArea(&self): f64 {\n\t\treturn 1.0\n\t}\n}\n")
		if r := fact(b.m.Diags[0], "reason"); r != "orphan" {
			t.Errorf("reason %q", r)
		}
	})
	t.Run("interface module may implement for an imported type", func(t *testing.T) {
		w := newWorld(t, true)
		w.add("a", opt{}, "export struct Square {\n\tside: f64\n}\n")
		checkCodes(t, w, "b", opt{}, "", "import a\nexport interface Shape {\n\tArea(&self): f64\n}\nimpl Shape for a.Square {\n\tArea(&self): f64 {\n\t\treturn 1.0\n\t}\n}\n")
	})
	t.Run("second impl across modules", func(t *testing.T) {
		w := newWorld(t, true)
		w.add("a", opt{}, "export interface Shape {\n\tArea(&self): f64\n}\n")
		w.add("b", opt{}, "import a\nexport struct Square {\n\tside: f64\n}\nimpl a.Shape for Square {\n\tArea(&self): f64 {\n\t\treturn 1.0\n\t}\n}\n")
		// c cannot declare it: c is neither module; a second impl in a's
		// own module is the importing-side duplicate only when a imports b,
		// which a cycle forbids, so the cross-module case is b then a's
		// importer re-declaring in b's position. Here: orphan.
		checkCodes(t, w, "c", opt{}, "E0704@3:1", "import a\nimport b\nimpl a.Shape for b.Square {\n\tArea(&self): f64 {\n\t\treturn 1.0\n\t}\n}\n")
	})
	t.Run("primitive outside std", func(t *testing.T) {
		w := newWorld(t, true)
		w.add("a", opt{}, "export interface Shape {\n\tArea(&self): f64\n}\n")
		b := checkCodes(t, w, "c", opt{}, "E0704@2:1", "import a\nimpl a.Shape for int {\n\tArea(&self): f64 {\n\t\treturn 1.0\n\t}\n}\n")
		if r := fact(b.m.Diags[0], "reason"); r != "orphan" {
			t.Errorf("reason %q", r)
		}
		// The interface's own module may implement it for a primitive.
		checkCodes(t, w, "d", opt{}, "", "interface Size {\n\tBytes(&self): int\n}\nimpl Size for int {\n\tBytes(&self): int {\n\t\treturn 8\n\t}\n}\n")
	})
	t.Run("alias and target are one pair", func(t *testing.T) {
		w := newWorld(t, true)
		b := checkCodes(t, w, "m", opt{}, "E0704@11:1", shapeSrc+`
type Sq = Square

impl Shape for Square {
	Area(&self): f64 {
		return 1.0
	}
}

impl Shape for Sq {
	Area(&self): f64 {
		return 2.0
	}
}
`)
		d := b.m.Diags[0]
		if r := fact(d, "reason"); r != "duplicate" {
			t.Errorf("reason %q", r)
		}
		if _, ok := d.FactValue("first"); !ok {
			t.Errorf("no first fact")
		}
	})
}

func TestE0704_SameModuleDuplicateImpl(t *testing.T) {
	w := newWorld(t, true)
	b := checkCodes(t, w, "m", opt{}, "E0704@15:1", shapeSrc+`
impl Shape for Square {
	Area(&self): f64 {
		return self.side * self.side
	}
}

impl Shape for Square {
	Area(&self): f64 {
		return self.side * self.side
	}
	Extra(&self): int {
		return 1
	}
}
`)
	d := b.m.Diags[0]
	if r := fact(d, "reason"); r != "duplicate" {
		t.Errorf("reason %q", r)
	}
	f, ok := d.FactValue("first")
	if !ok || w.lc(f.At) != "9:1" {
		t.Errorf("first %v %v", ok, w.lc(f.At))
	}
}

func TestE0707_ImplSignature(t *testing.T) {
	cases := []struct{ name, impl, want string }{
		{"missing method", "impl Shape for Square {\n}\n", "E0707@9:1"},
		{"wrong result", "impl Shape for Square {\n\tArea(&self): f32 {\n\t\treturn 1.0\n\t}\n}\n", "E0707@10:2"},
		{"wrong receiver", "impl Shape for Square {\n\tArea(mut self): f64 {\n\t\treturn 1.0\n\t}\n}\n", "E0707@10:2"},
		{"extra parameter", "impl Shape for Square {\n\tArea(&self, n: int): f64 {\n\t\treturn 1.0\n\t}\n}\n", "E0707@10:2"},
		{"extra method", "impl Shape for Square {\n\tArea(&self): f64 {\n\t\treturn 1.0\n\t}\n\tPerimeter(&self): f64 {\n\t\treturn 4.0\n\t}\n}\n", "E0707@13:2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, true)
			b := checkCodes(t, w, "m", opt{}, tc.want, shapeSrc+"\n"+tc.impl)
			d := b.m.Diags[0]
			if fact(d, "expected") == "" || fact(d, "found") == "" {
				t.Errorf("facts: %s", dump(w, b.m.Diags))
			}
		})
	}
	t.Run("Self and interface arguments substitute", func(t *testing.T) {
		w := newWorld(t, true)
		checkCodes(t, w, "m", opt{}, "", `interface Conv[T] {
	To(&self, o: &Self): T
	Pick[U: Plain](&self, u: U): U
}

struct P {
	x: int
}

impl Conv[i32] for P {
	To(&self, o: &P): i32 {
		return 1
	}
	Pick[V: Plain](&self, u: V): V {
		return u
	}
}

struct Box2[T] {
	v: T
}

impl[A] Conv[A] for Box2[A] {
	To(&self, o: &Box2[A]): A {
		return o.v
	}
	Pick[V: Plain](&self, u: V): V {
		return u
	}
}
`)
	})
	t.Run("own generic bound differs", func(t *testing.T) {
		w := newWorld(t, true)
		checkCodes(t, w, "m", opt{}, "E0707@10:2", `interface Conv {
	Pick[U: Plain](&self, u: U): U
}

struct P {
	x: int
}

impl Conv for P {
	Pick[V](&self, u: V): V {
		return u
	}
}
`)
	})
}

func TestE0608_ImplCoveredByHeld(t *testing.T) {
	t.Run("interface H covers impl B", func(t *testing.T) {
		w := newWorld(t, true)
		checkCodes(t, w, "m", opt{}, "", `interface I {
	Get(&self): str from self
}

struct S {
	src: str
}

impl I for S {
	Get(&self): str {
		return self.src
	}
}
`)
	})
	t.Run("interface B does not cover impl H", func(t *testing.T) {
		w := newWorld(t, true)
		b := checkCodes(t, w, "m", opt{}, "E0608@10:2", `interface I {
	Get(&self, t: &Tok): str from t.s
}

struct Tok {
	s: str
}

impl I for Tok {
	Get(&self, t: &Tok): str from t {
		return t.s
	}
}
`)
		if d := b.m.Diags[0]; fact(d, "expected") != "B(t)" || fact(d, "found") != "from t" {
			t.Errorf("facts %s", dump(w, b.m.Diags))
		}
	})
}

func TestBuiltinBoundImpls(t *testing.T) {
	t.Run("accepts the asserted Share with bounds", func(t *testing.T) {
		w := newWorld(t, true)
		b := checkCodes(t, w, "std.sync", opt{std: true, unsafe: true}, "", `export struct Mutex2[T] {
	lock: u32
	v: T
}

unsafe impl[T: Send] Share for Mutex2[T] {
}
`)
		mx := b.item("Mutex2")
		tab := w.tab
		share := w.c.Facts(tab.Named(mx, []types.GenericArg{types.TypeArg(types.Int)})).Share
		cell := w.c.Facts(tab.Named(mx, []types.GenericArg{types.TypeArg(tab.Ref(tab.Named(w.c.known.Cell, []types.GenericArg{types.TypeArg(types.Int)})))})).Share
		if !share || cell {
			t.Errorf("Mutex2[int] Share %v, Mutex2[&Cell[int]] Share %v", share, cell)
		}
	})
	cases := []struct{ name, src, want, reason string }{
		{"Send without unsafe", "struct X {\n\tn: int\n}\n\nimpl Send for X {\n}\n", "E1101@5:1", ""},
		{"Share without unsafe", "struct X {\n\tn: int\n}\n\nimpl Share for X {\n}\n", "E1101@5:1", ""},
		{"Plain", "struct X {\n\tn: int\n}\n\nimpl Plain for X {\n}\n", "E0704@5:1", "built-in bound"},
		{"unsafe Fields", "interface Shape {\n}\n\nstruct X {\n\tn: int\n}\n\nunsafe impl Fields[Shape] for X {\n}\n", "E0704@8:8", "built-in bound"},
		{"unsafe ordinary", "interface Shape {\n}\n\nstruct X {\n\tn: int\n}\n\nunsafe impl Shape for X {\n}\n", "E0704@8:8", "unsafe on an ordinary impl"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, true)
			b := checkCodes(t, w, "m", opt{unsafe: true}, tc.want, tc.src)
			d := b.m.Diags[0]
			if tc.reason != "" && fact(d, "reason") != tc.reason {
				t.Errorf("reason %q, want %q", fact(d, "reason"), tc.reason)
			}
			if d.Code == diag.E1101 && !strings.HasPrefix(fact(d, "operation"), "assert ") {
				t.Errorf("operation %q", fact(d, "operation"))
			}
		})
	}
	t.Run("assertion outside the type's module", func(t *testing.T) {
		w := newWorld(t, true)
		w.add("a", opt{}, "export struct X {\n\tn: int\n}\n")
		b := checkCodes(t, w, "b", opt{unsafe: true}, "E0704@2:8", "import a\nunsafe impl Send for a.X {\n}\n")
		if r := fact(b.m.Diags[0], "reason"); r != "orphan" {
			t.Errorf("reason %q", r)
		}
	})
}

func TestAssertionMakesUnique(t *testing.T) {
	w := newWorld(t, true)
	b := checkCodes(t, w, "m", opt{unsafe: true}, "E0408@8:7", `struct P {
	x: int
}

unsafe impl Send for P {
}

const C: P = P(x: 1)
`)
	if r := fact(b.m.Diags[0], "reason"); r == "" {
		t.Errorf("no reason")
	}
	if k := w.c.Facts(w.tab.Named(b.item("P"), nil)).Kind; k != types.Unique {
		t.Errorf("Facts(P).Kind = %v, want Unique", k)
	}
	t.Run("plain const accepted, Cell const E1002", func(t *testing.T) {
		w := newWorld(t, true)
		b := checkCodes(t, w, "m", opt{}, "E1002@1:7", "const C: ?Cell[int] = none\nconst D: (a: int, b: f64) = (a: 1, b: 2.0)\n")
		d := b.m.Diags[0]
		if d.Phase != diag.PhaseTypes || fact(d, "part") != "Cell[int]" {
			t.Errorf("phase %v part %q", d.Phase, fact(d, "part"))
		}
	})
}

func TestPhase_E1002GlobalIsTypes(t *testing.T) {
	w := newWorld(t, true)
	b := checkCodes(t, w, "m", opt{}, "E1002@1:5", "var Counter: Cell[i32] = Cell.New(0)\nvar Limit: int = 3\n")
	d := b.m.Diags[0]
	if d.Phase != diag.PhaseTypes || fact(d, "part") != "Cell[i32]" || fact(d, "type") != "Cell[i32]" {
		t.Errorf("phase %v facts %s", d.Phase, dump(w, b.m.Diags))
	}
	// The second half: the global's initializer `Cell.New(0)` infers
	// Cell[i32] from the declared type, an unrelated E0301 in a body is also
	// reported, and an s.go argument's E1002 keeps its default phase, flow.
	w2 := newWorld(t, true)
	b2 := checkBody(t, w2, "E0301@4:10 E1002@11:12 E1002@1:5",
		"var Counter: Cell[i32] = Cell.New(0)\n\nF() {\n\tx: u8 = \"s\"\n}\n\nWork(c: &Cell[int]) {\n}\n\nG(s: &Scope, c: &Cell[int]) {\n\ts.go Work(c)\n}\n")
	var phases []diag.Phase
	for _, d := range append(b2.diags(), b2.bodyDiags(t)...) {
		if d.Code == diag.E1002 {
			phases = append(phases, d.Phase)
		}
	}
	if len(phases) != 2 || phases[0] != diag.PhaseTypes || phases[1] != diag.PhaseFlow {
		t.Errorf("E1002 phases %v, want [types flow]", phases)
	}
}

func TestE0701_BoundUnmet(t *testing.T) {
	t.Run("built-in bound at a type expression", func(t *testing.T) {
		w := newWorld(t, true)
		checkCodes(t, w, "m", opt{}, "E0701@6:5", "struct Hold[T: Plain] {\n\tv: T\n}\n\nstruct Use {\n\th: Hold[mut i32]\n\tok: Hold[int]\n}\n")
	})
	t.Run("interface bound at a type expression", func(t *testing.T) {
		w := newWorld(t, true)
		b := checkCodes(t, w, "m", opt{}, "E0701@25:5", shapeSrc+`
impl Shape for Square {
	Area(&self): f64 {
		return 1.0
	}
}

struct Circle {
	r: f64
}

struct Hold[T: Shape] {
	v: T
}

struct Use {
	a: Hold[Square]
	b: Hold[Circle]
}

struct Gen[U: Shape] {
	h: Hold[U]
}
`)
		d := b.m.Diags[0]
		if fact(d, "param") != "T" || fact(d, "bound") != "Shape" || fact(d, "type") != "Circle" {
			t.Errorf("facts %s", dump(w, b.m.Diags))
		}
	})
	t.Run("generic impl with bounds", func(t *testing.T) {
		w := newWorld(t, true)
		checkCodes(t, w, "m", opt{}, "E0701@27:5", `interface Eq2 {
	Same(&self, o: &Self): bool
}

struct L[T] {
	v: T
}

impl Eq2 for int {
	Same(&self, o: &int): bool {
		return true
	}
}

impl[T: Eq2] Eq2 for L[T] {
	Same(&self, o: &L[T]): bool {
		return true
	}
}

struct Need[T: Eq2] {
	v: T
}

struct Use {
	a: Need[L[int]]
	b: Need[L[f64]]
}
`)
	})
	t.Run("Fields bound", func(t *testing.T) {
		w := newWorld(t, true)
		checkCodes(t, w, "m", opt{}, "E0701@26:5", shapeSrc+`
impl Shape for Square {
	Area(&self): f64 {
		return 1.0
	}
}

struct Dump[T: Fields[Shape]] {
	v: T
}

struct Two {
	export a: Square
	b: int
}

struct Use {
	a: Dump[Two]
	b: Dump[(x: int)]
}
`)
	})
}

func TestMemberNamespace_E0206(t *testing.T) {
	t.Run("impl method and a field that fold alike are two members", func(t *testing.T) {
		w := newWorld(t, true)
		b := checkBody(t, w, "", `struct S {
	area: f64
}

interface Shape {
	Area(&self): f64
}

impl Shape for S {
	Area(&self): f64 {
		return self.area
	}
}

Use(s: &S): f64 {
	return s.area + s.Area()
}
`)
		var got []string
		for _, mb := range w.c.Members(w.tab.Named(b.item("S"), nil)) {
			got = append(got, w.in.Str(mb.Name))
		}
		if strings.Join(got, " ") != "Area area" {
			t.Errorf("members %v", got)
		}
	})
	t.Run("Type.Name method and a field that fold alike are two members", func(t *testing.T) {
		w := newWorld(t, true)
		checkBody(t, w, "", `struct M {
	cpu: int
	max_len: int
}

M.Cpu(&self): int {
	return self.cpu
}

M.MaxLen(&self): int {
	return self.max_len
}

Use(m: &M): int {
	return m.cpu + m.Cpu() + m.max_len + m.MaxLen()
}
`)
	})
	t.Run("a folded misspelling picks the field or the method by its use", func(t *testing.T) {
		w := newWorld(t, true)
		b := checkBody(t, w, "E0207@10:11 E0207@10:21", `struct M {
	cpu: int
}

M.Cpu(&self): int {
	return self.cpu
}

Use(m: &M): int {
	return m.CPU() + m.CPU
}
`)
		var want []string
		for _, d := range b.bodyDiags(t) {
			want = append(want, fact(d, "expected"))
		}
		if strings.Join(want, " ") != "Cpu cpu" {
			t.Errorf("expected spellings %v", want)
		}
	})
	t.Run("two impls in one module", func(t *testing.T) {
		w := newWorld(t, true)
		checkCodes(t, w, "m", opt{}, "E0206@20:2", `struct S {
	n: int
}

interface I {
	Name(&self): str
}

interface J {
	Name(&self): str
}

impl I for S {
	Name(&self): str {
		return "i"
	}
}

impl J for S {
	Name(&self): str {
		return "j"
	}
}
`)
	})
	t.Run("members sorted with fields, functions and impl methods", func(t *testing.T) {
		w := newWorld(t, true)
		b := checkCodes(t, w, "m", opt{}, "", `struct S {
	zeta: int
	alpha: int
}

S.Make(): S {
	return S(zeta: 1, alpha: 2)
}

interface I {
	Name(&self): str
}

impl I for S {
	Name(&self): str {
		return "i"
	}
}
`)
		var got []string
		for _, mb := range w.c.Members(w.tab.Named(b.item("S"), nil)) {
			got = append(got, w.in.Str(mb.Name))
		}
		if strings.Join(got, " ") != "Make Name alpha zeta" {
			t.Errorf("members %v", got)
		}
	})
	t.Run("orphan method", func(t *testing.T) {
		w := newWorld(t, true)
		b := checkCodes(t, w, "m", opt{}, "E0704@1:5 E0704@3:6", "str.Shout(self) {\n}\nList.Shout(self) {\n}\n")
		for _, d := range b.m.Diags {
			if fact(d, "reason") != "orphan-method" {
				t.Errorf("reason %q", fact(d, "reason"))
			}
		}
	})
}

func TestSiblingImplCollision(t *testing.T) {
	w := newWorld(t, true)
	w.add("m1", opt{}, "export struct C {\n\tn: int\n}\n")
	checkCodes(t, w, "m2", opt{}, "", "import m1\nexport interface I {\n\tName(&self): str\n}\nimpl I for m1.C {\n\tName(&self): str {\n\t\treturn \"i\"\n\t}\n}\n")
	checkCodes(t, w, "m3", opt{}, "", "import m1\nexport interface J {\n\tName(&self): str\n}\nimpl J for m1.C {\n\tName(&self): str {\n\t\treturn \"j\"\n\t}\n}\n")
	b4 := checkCodes(t, w, "m4", opt{}, "", "import m1\nimport m2\nimport m3\nF[T: m2.I + m3.J](c: &m1.C, t: &T) {\n}\n")
	b1 := w.mods["m1"]
	cands, folded := b4.m.lookup(w.tab.Named(b1.item("C"), nil), w.in.Intern("Name"))
	if folded || len(cands) != 2 || cands[0].Kind != MemberImplMethod || cands[1].Kind != MemberImplMethod {
		t.Errorf("m4 sees %v (folded %v), want both impls' Name", cands, folded)
	}
	// A module that imports only m2 sees one.
	b5 := checkCodes(t, w, "m5", opt{}, "", "import m1\nimport m2\nF[T: m2.I](c: &m1.C, t: &T) {\n}\n")
	if cands, _ := b5.m.lookup(w.tab.Named(b1.item("C"), nil), w.in.Intern("Name")); len(cands) != 1 {
		t.Errorf("m5 sees %d candidates, want 1", len(cands))
	}
}
