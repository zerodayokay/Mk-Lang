// Why: `plain view struct` spans parsing, kind facts, Invariants A and B, E0410,
// copyable borrow tracking, E0401 suggestions, and cross-module interface
// hashing, so end-to-end driver checks must verify both cold and warm caching.
//
// What: TestPlainViewCopyAndBorrow, TestPlainViewE0401Note, and
// TestPlainViewCrossModuleCache.
//
// How: builds self-contained single-module and multi-module packages via the
// driver and verifies exact diagnostics, alternatives, and warm-cache verdicts.

package driver

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"mk/internal/diag"
)

func TestPlainViewCopyAndBorrow(t *testing.T) {
	t.Parallel()
	t.Run("copyable handles, phantom type params, and TwoSyms", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"pkg.mk": manifest(map[string]string{"name": `"pv"`, "unsafe_modules": `["."]`}),
			"main.mk": `export plain view struct Ref[T] {
    idx: u32
}

export plain view struct Sym {
    id: u32
}

export linear struct Res {
    fd: int
}

struct Roots {
    n: int
}

struct TwoSyms {
    a: Sym
    b: Sym
}

TwoSyms.Dup(mut self) {
    self.a = self.b
}

MakeRef[T](r: &Roots): Ref[T] from r {
    _ = r
    return Ref[T](idx: 1)
}

LenRef[T](h: Ref[T], r: &Roots): u32 {
    _ = r
    return h.idx
}

HashRef[T](h: Ref[T], r: &Roots): u32 {
    _ = r
    return h.idx +% 17
}

UsePhantom(a: Ref[Res], b: Ref[Mutex[?&i32]]): (x: Ref[Res], y: Ref[Mutex[?&i32]]) from (x: a, y: b) {
    return (x: a, y: b)
}

export UseTwice(): u32 {
    r := Roots(n: 42)
    h := MakeRef[Res](&r)
    h2 := h
    p := UsePhantom(h, MakeRef[Mutex[?&i32]](&r))
    _ = p
    return LenRef(h, &r) +% HashRef(h2, &r)
}

export Main(sys: &System): void {
    _ = sys
    _ = UseTwice()
}
`,
		}
		res := check(t, Config{Root: writeTree(t, files), CacheDir: t.TempDir()})
		if got := baseLines(res); len(got) != 0 {
			t.Fatalf("unexpected diagnostics: %v", got)
		}
	})

	t.Run("stale handle after Arena.Reset is rejected", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"pkg.mk": manifest(map[string]string{"name": `"pv"`}),
			"main.mk": `plain view struct Sym {
    id: u32
}

Intern(a: &Arena, s: str): Sym from a {
    _ = s
    return Sym(id: 1)
}

ReadSym(h: Sym, a: &Arena): u32 {
    _ = a
    return h.id
}

export Bad(h: &Heap): OutOfMemory!u32 {
    var a := try Arena.New(h)
    defer a.Free()
    s1 := Intern(&a, "foo")
    s2 := s1
    a.Reset()
    return ReadSym(s2, &a)
}

export Main(sys: &System): void {
    _ = Bad(sys.heap) else { 0 }
}
`,
		}
		res := check(t, Config{Root: writeTree(t, files), CacheDir: t.TempDir()})
		if got := strings.Join(baseLines(res), " "); got != "main.mk:20:5:E0501" {
			t.Fatalf("got %q, want main.mk:20:5:E0501", got)
		}
	})
}

func TestPlainViewE0401Note(t *testing.T) {
	t.Parallel()
	const wantAlt = "declare it `plain view struct`"
	checkAlt := func(t *testing.T, name, src string, wantNote bool) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			files := map[string]string{
				"pkg.mk": manifest(map[string]string{"name": `"note"`, "unsafe_modules": `["."]`}),
				"main.mk": src + `
export Main(sys: &System): void {
    _ = sys
}
`,
			}
			res := check(t, Config{Root: writeTree(t, files), CacheDir: t.TempDir()})
			var e401 *diag.Diagnostic
			for _, em := range res.Report.Diags {
				if em.Diag.Code == diag.E0401 {
					e401 = em.Diag
					break
				}
			}
			if e401 == nil {
				t.Fatalf("expected E0401, got %v", baseLines(res))
			}
			has := false
			for _, alt := range e401.Alternatives {
				if strings.Contains(alt, wantAlt) {
					has = true
				}
			}
			if has != wantNote {
				t.Fatalf("E0401 alternatives = %v (has=%v, want=%v)", e401.Alternatives, has, wantNote)
			}
		})
	}

	checkAlt(t, "plain+freeze copyable view gets note", `view struct Sym {
    id: u32
}

Sym.Val(&self): u32 {
    return self.id
}

F(): u32 {
    s := Sym(id: 1)
    a := s.Val()
    b := s
    _ = b
    return a +% s.Val()
}
`, true)

	checkAlt(t, "raw ptr view gets no note", `import std.raw

view struct PtrView {
    p: *u8
}

F(x: &u8): void {
    unsafe "test" {
        v := PtrView(p: raw.Of(x))
        w := v
        _ = w
        _ = v
    }
}
`, false)

	checkAlt(t, "cell field view gets no note", `view struct CellView {
    c: Cell[u32]
}

F() {
    v := CellView(c: Cell.New(1))
    w := v
    _ = w
    _ = v
}
`, false)

	checkAlt(t, "ends self method view gets no note", `view struct Resettable {
    id: u32
}

Resettable.Reset(mut self) ends self {
}

F() {
    v := Resettable(id: 1)
    w := v
    _ = w
    _ = v
}
`, false)

	checkAlt(t, "concrete generic struct wrapper gets note", `struct Wrap[T] {
    v: T
}

view struct ConcreteWrap {
    w: Wrap[&i32]
}

ConcreteWrap.Take(self) {
    _ = self
}

F(x: &i32) {
    v := ConcreteWrap(w: Wrap(v: x))
    w := v
    _ = w
    v.Take()
}
`, true)

	checkAlt(t, "enum wrapper with bare type param by value gets no note", `enum Boxed[T] {
    Val(v: T)
    Empty
}

view struct EnumParamView[T] {
    b: Boxed[T]
}

EnumParamView.Take(self) {
    _ = self
}

F(x: &i32) {
    v: EnumParamView[&i32] = EnumParamView(b: Boxed.Val(v: x))
    w := v
    _ = w
    v.Take()
}
`, false)

	checkAlt(t, "unsafe impl Send view gets no note", `view struct SendView {
    id: u32
}

unsafe impl Send for SendView {
}

SendView.Val(&self): u32 {
    return self.id
}

F(): u32 {
    s := SendView(id: 1)
    a := s.Val()
    b := s
    _ = b
    return a +% s.Val()
}
`, false)
}

func TestPlainViewCrossModuleCache(t *testing.T) {
	t.Parallel()
	const tpl = `export %VIEW% struct Sym {
    id: u32
}

export Make(n: &i32): Sym from n {
    _ = n
    return Sym(id: 1)
}

export Id(s: Sym, n: &i32): u32 {
    _ = n
    return s.id
}
`
	files := map[string]string{
		"pkg.mk": manifest(map[string]string{"name": `"pvcache"`}),
		"v/v.mk": strings.ReplaceAll(tpl, "%VIEW%", "view"),
		"main.mk": `import v

export Run(): u32 {
    n: i32 = 5
    s := v.Make(&n)
    return v.Id(s, &n) +% v.Id(s, &n)
}

export Other(): i32 {
    return 1
}

export Main(sys: &System): void {
    _ = sys
    _ = Run()
}
`,
	}
	dir := writeTree(t, files)
	warm := t.TempDir()
	vPath := filepath.Join(dir, "v", "v.mk")
	for i, plain := range []bool{false, true, false, true} {
		mod := "view"
		want := "main.mk:6:27:E0401"
		if plain {
			mod = "plain view"
			want = "ok"
		}
		if err := os.WriteFile(vPath, []byte(strings.ReplaceAll(tpl, "%VIEW%", mod)), 0o644); err != nil {
			t.Fatal(err)
		}
		for j, cache := range []string{warm, t.TempDir()} {
			got := strings.Join(baseLines(check(t, Config{Root: dir, CacheDir: cache})), " ")
			if got == "" {
				got = "ok"
			}
			if got != want {
				t.Fatalf("step %d (plain=%v, cold=%v): %q, want %q", i+1, plain, j == 1, got, want)
			}
		}
	}
	// Editing an unrelated function reuses main.Run from the warm cache.
	mainPath := filepath.Join(dir, "main.mk")
	if err := os.WriteFile(mainPath, []byte(strings.Replace(files["main.mk"], "return 1", "return 2", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	wres := check(t, Config{Root: dir, CacheDir: warm})
	if got := baseLines(wres); len(got) != 0 {
		t.Fatalf("warm after unrelated edit: %v", got)
	}
	if os.Getenv("MK_CACHE_VERIFY") == "" {
		checked := pkgItems(wres.Trace.ItemsChecked)
		if slices.Contains(checked, "main.Run") || !slices.Contains(checked, "main.Other") {
			t.Errorf("warm rechecked %q, want main.Other and not main.Run", checked)
		}
	}
}
