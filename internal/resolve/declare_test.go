// Why: declaring a module's items fixes their keys and names, which every
// later phase and the cache rely on.
// What: tests of declaration and entry: stable keys, sorted items, E0206
// collisions, the E0207 case rule and its exemptions, owners, missing main,
// test names and the primitive table.
// How: parses small modules with the helpers_test.go harness and checks the
// declared items and diagnostics.

package resolve

import (
	"strings"
	"testing"

	"mk/internal/diag"
	"mk/internal/source"
	"mk/internal/types"
)

func TestFakePreludeIsClean(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	b := w.mods["std.core"]
	b.want(t)
	// The fake prelude has no Path, which is not a prelude name.
	if _, has := w.s.prelude.names[w.in.Intern("Path")]; len(w.s.prelude.names) != 51 || has { // 46 + Chunks, ChunksMut, Free, Replace, Swap
		t.Errorf("prelude scope: %d names, Path %v", len(w.s.prelude.names), has)
	}
}

func TestBeginKeysAreStable(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	src := `
struct Point {
    x: i32
    invariant x >= 0
}
Point.Add(&self, o: &Point): Point { return MakePoint(o) }
MakePoint(p: &Point): Point
    requires p.x > 0
    ensures result.x > 0
{
    f := fn(q: int): int { return q }
    return Point(x: f(1))
}
interface Shape { Area(&self): int }
impl Eq for Point { Same(&self, o: &Self): bool { return true } }
test "a\tb" { }
const Max = 3
`
	b := w.add("main", opt{}, src)
	b.want(t)
	for _, k := range []string{
		"main.Point", "main.Point:Add", "main.MakePoint", "main.Shape", "main.Max",
		"main.Point#invariant0", "main.MakePoint#requires0", "main.MakePoint#ensures0",
		"main.MakePoint#fn0", "main#impl(std.core.Eq for main.Point)",
		"main#impl(std.core.Eq for main.Point):Same", `main#test("a\tb")`,
	} {
		w.item(k)
	}
	if it := w.item("main.Point:Add"); it.Owner != w.item("main.Point").ID {
		t.Errorf("method owner %d", it.Owner)
	}
	if it := w.item("main.MakePoint#fn0"); it.Kind != types.KindLiteral || it.Owner != w.item("main.MakePoint").ID {
		t.Errorf("literal %+v", it)
	}
	if it := w.item("main#impl(std.core.Eq for main.Point):Same"); it.Owner != w.item("main#impl(std.core.Eq for main.Point)").ID {
		t.Errorf("impl method owner")
	}
}

func TestModuleItemsSortedByKey(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("main", opt{}, "Z() { f := fn() { }\n f() }\nA() {}\nstruct M {}\n")
	mod := w.items.Module(b.id)
	for i := 1; i < len(mod.Items); i++ {
		if w.items.Get(mod.Items[i-1]).Key.Compare(w.items.Get(mod.Items[i]).Key) >= 0 {
			t.Fatalf("Module.Items not in key order")
		}
	}
	for i := 1; i < len(b.info.Items); i++ {
		if w.items.Get(b.info.Items[i-1].Item).Key.Compare(w.items.Get(b.info.Items[i].Item).Key) >= 0 {
			t.Fatalf("Info.Items not in key order")
		}
	}
}

func TestModuleItemsIncludeLiterals(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("main", opt{}, "F() { g := fn() { }\n g() }\n")
	lit := w.item("main.F#fn0")
	found := false
	for _, id := range w.items.Module(b.id).Items {
		found = found || id == lit.ID
	}
	if !found {
		t.Error("lifted literal missing from Module.Items")
	}
}

func TestE0206_ModuleLevelFolded(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("main", opt{}, "struct MaxUsers {}\nstruct Max_Users {}\n")
	b.want(t, "E0206@2:8", "E0207@2:8")
	d := b.only(diag.E0206)[0]
	if fact(d, "first") == "" {
		t.Error("no first fact")
	}
}

func TestTypeAndFunctionCollide(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("main", opt{}, "struct Parse {}\nParse() {}\n")
	b.want(t, "E0206@2:1")
}

func TestFieldAndMethodFoldApart(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("main", opt{}, "struct P {\n    len: int\n    max_len: int\n}\nP.Len(&self): int { return 0 }\nP.MaxLen(&self): int { return 0 }\n")
	b.want(t)
	// The method may come first.
	b = w.add("other", opt{}, "Q.Len(&self): int { return 0 }\nstruct Q {\n    len: int\n}\n")
	b.want(t)
}

func TestE0206_FieldAndMethodExact(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("main", opt{}, "struct P {\n    Len: int\n}\nP.Len(&self): int { return 0 }\n")
	b.want(t, "E0206@4:3", "E0207@2:5")
}

func TestE0206_MethodsFolded(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("main", opt{}, "struct P {\n    len: int\n}\nP.Len(&self): int { return 0 }\nP.LEN(&self): int { return 0 }\n")
	b.want(t, "E0206@5:3")
}

func TestE0206_VariantAndMethod(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("main", opt{}, "enum Color { Red\n Green }\nColor.Red(self) {}\n")
	b.want(t, "E0206@3:7")
}

func TestE0206_DuplicateImportBinding(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	w.add("a.fs", opt{}, "export F() {}\n")
	w.add("b.fs", opt{}, "export G() {}\n")
	b := w.add("main", opt{}, "import a.fs\nimport b.fs\n\nH() { fs.F() }\n")
	b.want(t, "E0206@2:10")
}

func TestE0206_DuplicateTestName(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("main", opt{}, "test \"a\" {}\ntest \"a\" {}\n")
	b.want(t, "E0206@2:1")
}

func TestE0206_DuplicateKeysNoICE(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	b := w.add("main", opt{}, `struct Point {}
Point.Add(&self) {}
Point.Add(&self) {}
impl Eq for Point {
    Same(&self) {}
    Same(&self) {}
}
`)
	b.want(t, "E0206@3:7", "E0206@6:5")
	for _, d := range b.diags {
		if d.Code == diag.E0000 {
			t.Fatal("ICE")
		}
	}
}

func TestE0207_DeclarationCase(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("main", opt{}, "struct point {\n    MaxX: int\n}\nenum E { red }\nf(BadName: int) { }\n")
	b.want(t, "E0207@1:8", "E0207@2:5", "E0207@4:10", "E0207@5:1", "E0207@5:3")
	d := b.only(diag.E0207)[0]
	if len(d.Fixes) != 1 || d.Fixes[0].Class != diag.Proven {
		t.Errorf("point: fixes %+v", d.Fixes)
	}
}

func TestE0207_RecaseFixCoversUses(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("main", opt{}, "struct point {}\nF(p: point): point { return p }\n")
	d := b.only(diag.E0207)[0]
	if len(d.Fixes) != 1 || len(d.Fixes[0].Edits) != 3 {
		t.Fatalf("fix edits: %+v\n%s", d.Fixes, b.dump())
	}
	for _, e := range d.Fixes[0].Edits {
		if e.Text != "Point" {
			t.Errorf("edit %+v", e)
		}
	}
}

// Neither a bodiless extern's name nor an export extern's with a body is held
// to the case rule.
func TestExternNameExempt(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("main", opt{}, "extern write(fd: i32, p: *u8, n: uint): int\nexport extern mk_cb(x: i32): i32 { return x }\n")
	b.want(t)
}

// An export extern with a body is named by its C symbol, so std.libc
// can define malloc; a function that is not extern keeps the case rule.
func TestExportExternNameExempt(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("main", opt{}, "export extern malloc(n: uint): uint { return n }\nexport extern sqlite3_os_init(): i32 { return 0 }\n")
	b.want(t)
}

func TestPlainFunctionStillCased(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("main", opt{}, "export extern malloc(n: uint): uint { return n }\nexport my_alloc(n: uint): uint { return n }\nhelper_fn() { }\n")
	b.want(t, "E0207@2:8", "E0207@3:1")
}

func TestE0704_OrphanMethodReportedOnce(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	b := w.add("main", opt{}, "List.Shout(self) {}\nstr.Shout(self) {}\n")
	b.want(t)
	if it := w.item("main.List:Shout"); it.Owner != w.item("std.core.List").ID {
		t.Errorf("owner %d", it.Owner)
	}
	if it := w.item("main.str:Shout"); it.OwnerPrim != types.Str || it.Owner != types.NoItem {
		t.Errorf("prim owner %+v", it)
	}
	if got := w.s.MethodsOf(w.item("std.core.List").ID); len(got) != 0 {
		t.Errorf("orphan indexed: %v", got)
	}
	if got := w.s.PrimMethods(types.Str); len(got) != 0 {
		t.Errorf("user prim method indexed: %v", got)
	}
}

func TestPrimMethodInStd(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	w.add("std.text", opt{std: true}, "export str.Len(self): int\n")
	got := w.s.PrimMethods(types.Str)
	if len(got) != 1 || got[0] != w.item("std.text.str:Len").ID {
		t.Errorf("PrimMethods(str) = %v", got)
	}
}

func TestMethodOnAlias(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("main", opt{}, "struct P {}\ntype Q = P\nQ.Go(&self) {}\n")
	b.want(t)
	if it := w.item("main.Q:Go"); it.Owner != w.item("main.Q").ID {
		t.Errorf("alias owner")
	}
}

func TestTypeNameOnInterfaceInStd(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("std.core", opt{std: true}, "Slice[T].Len(&self): int\ninterface Slice[T] { Items(&self): []T }\n")
	b.want(t)
	got := w.s.MethodsOf(w.item("std.core.Slice").ID)
	if len(got) != 1 || got[0] != w.item("std.core.Slice:Len").ID {
		t.Errorf("MethodsOf(Slice) = %v", got)
	}
}

func TestBeginForwardOwner(t *testing.T) {
	t.Parallel()
	a := "Point.Add(mut self) {}\nimpl Eq for Point { }\n"
	c := "struct Point {}\n"
	for name, files := range map[string][]string{"forward": {a, c}, "swapped": {c, a}, "one file": {c + a}} {
		w := newWorld(t, true)
		b := w.add("main", opt{}, files...)
		b.want(t)
		w.item("main#impl(std.core.Eq for main.Point)")
		if it := w.item("main.Point:Add"); it.Owner != w.item("main.Point").ID {
			t.Errorf("%s: owner", name)
		}
	}
}

func TestDuplicateImplGoesToCheck(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	b := w.add("main", opt{}, "struct P {}\nimpl Eq for P {}\nimpl Eq for P {}\n")
	b.want(t)
	if len(b.info.DuplicateImpls) != 1 || b.info.DuplicateImpls[0].First != w.item("main#impl(std.core.Eq for main.P)").ID {
		t.Errorf("DuplicateImpls %+v", b.info.DuplicateImpls)
	}
}

func TestOwnerArgsDeclareParams(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("main", opt{}, "struct Stack2[T] {\n    x: T\n}\nStack2[T].Push[U](mut self, x: T, y: U) {}\n")
	b.want(t)
	if r := b.ref("T", 3); r.Kind != RefTypeParam || r.A != 0 {
		t.Errorf("T in Push: %+v", r)
	}
	if r := b.ref("U", 1); r.Kind != RefTypeParam || r.A != 1 {
		t.Errorf("U in Push: %+v", r)
	}
	// The parser reads owner arguments as type parameters
	// (internal/syntax/sig.go), so every OwnerArgs entry is a name and
	// resolve's E0101 for a non-name owner argument cannot be reached from
	// parsed source.
}

func TestMissingMainReported(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("app", opt{bin: true}, "F() {}\n")
	b.want(t, "E0201@1:1")
	d := b.diags[0]
	if fact(d, "name") != "Main" || fact(d, "expected") != "function" || !d.At.IsEmpty() {
		t.Errorf("Main E0201: %s", b.dump())
	}
	w.add("lib", opt{}, "F() {}\n").want(t)
	w.add("app2", opt{bin: true}, "Main() {}\n").want(t)
	w.add("app3", opt{bin: true}, "struct T {}\nT.Main(&self) {}\n").want(t, "E0201@1:1")
}

func TestTestNameDecoding(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{`"a"`: "a", `"a\nb"`: "a\nb", `"\x41\u{263A}"`: "A☺", `"q\"q"`: `q"q`} {
		if got := decodeString(in); got != want {
			t.Errorf("decodeString(%s) = %q, want %q", in, got, want)
		}
	}
}

func TestPrimitiveTable(t *testing.T) {
	t.Parallel()
	in := source.NewInterner()
	want := map[string]types.TypeID{"i8": types.I8, "i16": types.I16, "i32": types.I32, "i64": types.I64,
		"u8": types.U8, "u16": types.U16, "u32": types.U32, "u64": types.U64, "int": types.Int, "uint": types.Uint,
		"f32": types.F32, "f64": types.F64, "bool": types.Bool, "char": types.Char, "str": types.Str,
		"void": types.Void, "never": types.Never}
	n := 0
	for s := source.SymI8; s <= source.SymNever; s++ {
		got, ok := primType(s)
		if !ok || got != want[in.Str(s)] {
			t.Errorf("%s: %v", in.Str(s), got)
		}
		n++
	}
	if n != 17 {
		t.Errorf("%d primitives", n)
	}
	if !strings.Contains(fakePrelude(), "Duration") {
		t.Error("fake prelude lost its std-internal export")
	}
}
