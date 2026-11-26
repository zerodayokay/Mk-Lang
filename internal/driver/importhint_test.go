// Why: an allocator whose table is a constant where it is used must have
// its entries imported into the user's pack, or a table call LLVM makes
// direct after ThinLTO's import stays a call (codegen's importhint.go).
// What: TestReleaseImportHintFixed, TestImportHintNotInLibrary,
// TestImportHintNotForOtherTables and TestReleaseImportHintInAnotherPack,
// with the helpers hintFiles, hintPackLL, defines, hintCalls and
// hintTables.
// How: release, dev and --profile size builds of small programs, and a
// static library, over std.mem.Fixed, the heap and a table of the
// program's own, reading each pack's IR through
// TestHooks.OnLL; the pack-key test follows prekey_test.go's pattern.

package driver

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"mk/internal/types"
)

// hintApp is a bin whose Main allocates Box[u64]s from a mem.Fixed, as
// the fixed-alloc benchmark does, and exits with 0 when both values come
// back.
const hintApp = "import std.mem\n\n" +
	"export Main(sys: &System): OutOfMemory!int {\n" +
	"    var buf: [256]u8 = [0; 256]\n" +
	"    f := mem.Fixed.New(mut buf[..])\n" +
	"    b1 := try Box[u64].New(f.Alloc(), 7)\n" +
	"    v1 := b1.Take(f.Alloc())\n" +
	"    b2 := try Box[u64].New(f.Alloc(), 5)\n" +
	"    v2 := b2.Take(f.Alloc())\n" +
	"    return Cast[int](v1 + v2) - 12\n" +
	"}\n"

// hintFiles is a one-bin package whose root module app holds app.
func hintFiles(app string) map[string]string {
	return map[string]string{
		"pkg.mk":     manifest(map[string]string{"bins": `[pkg.Bin(name: "app", root: "app", link_script: "", cpu: "", subsystem: "console")]`}),
		"app/app.mk": app,
	}
}

// hintPackLL builds cfg into out and returns every pack's IR and the IR of
// the pack that defines symbol, failing the test when none does.
func hintPackLL(t *testing.T, cfg Config, out, symbol string) (map[string]string, string) {
	t.Helper()
	var mu sync.Mutex
	lls := map[string]string{}
	cfg.TestHooks.OnLL = func(name, ll string) { mu.Lock(); lls[name] = ll; mu.Unlock() }
	incBuild(t, cfg, out)
	for _, ll := range lls {
		if defines(ll, symbol) {
			return lls, ll
		}
	}
	t.Fatalf("no pack defines %s", symbol)
	return nil, ""
}

// defines reports whether ll has a define line of symbol.
func defines(ll, symbol string) bool {
	for _, line := range strings.Split(ll, "\n") {
		if strings.HasPrefix(line, "define ") && strings.Contains(line, "@"+symbol+"(") {
			return true
		}
	}
	return false
}

// hintCalls is the callees of ll's mk.importhint, in order; nil when ll
// defines none.
func hintCalls(ll string) []string {
	_, body, ok := strings.Cut(ll, "define internal void @mk.importhint()")
	if !ok {
		return nil
	}
	body, _, _ = strings.Cut(body, "\n}\n")
	var out []string
	for _, line := range strings.Split(body, "\n") {
		if _, rest, ok := strings.Cut(line, "call "); ok {
			if _, callee, ok := strings.Cut(rest, " @"); ok {
				callee, _, _ = strings.Cut(callee, "(")
				out = append(out, callee)
			}
		}
	}
	return out
}

// TestReleaseImportHintFixed: in release, the pack of a Main that uses a
// mem.Fixed gets mk.importhint, an optnone function in @llvm.compiler.used
// that calls Fixed's three entries, defined in std.mem's packs; a dev
// build gets no hint in any pack. Both executables run and exit 0: the
// hint is never called.
func TestReleaseImportHintFixed(t *testing.T) {
	tools := needTools(t)
	dir := writeTree(t, hintFiles(hintApp))
	out := t.TempDir()
	cfg := Config{Root: dir, Target: pkTarget(), Profile: types.Release, Tools: tools, CacheDir: filepath.Join(t.TempDir(), "cache")}
	_, ll := hintPackLL(t, cfg, filepath.Join(out, "rel.exe"), "mk.app.Main")
	want := []string{"mk.std.mem.FixedAllocEntry", "mk.std.mem.FixedFreeEntry", "mk.std.mem.FixedResizeEntry"}
	got := hintCalls(ll)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("Main's pack's hint calls %v, want %v", got, want)
	}
	if !strings.Contains(ll, "@mk.importhint() nounwind") || !strings.Contains(ll, "noinline optnone") {
		t.Errorf("the hint is not noinline optnone:\n%s", ll)
	}
	used, _, _ := strings.Cut(ll[strings.Index(ll, "@llvm.compiler.used"):], "\n")
	if !strings.Contains(used, "ptr @mk.importhint") {
		t.Errorf("the hint is not in @llvm.compiler.used: %s", used)
	}
	dcfg := cfg
	dcfg.Profile = types.Dev
	dlls, _ := hintPackLL(t, dcfg, filepath.Join(out, "dev.exe"), "mk.app.Main")
	for name, ll := range dlls {
		if strings.Contains(ll, "mk.importhint") {
			t.Errorf("dev pack %s has an import hint", name)
		}
	}
	scfg := cfg
	scfg.Size = true
	slls, _ := hintPackLL(t, scfg, filepath.Join(out, "size.exe"), "mk.app.Main")
	for name, ll := range slls {
		if strings.Contains(ll, "mk.importhint") {
			t.Errorf("--profile size pack %s has an import hint", name)
		}
	}
	if runtime.GOOS == "windows" {
		for _, exe := range []string{"rel.exe", "dev.exe"} {
			if err := exec.Command(filepath.Join(out, exe)).Run(); err != nil {
				t.Errorf("%s: %v", exe, err)
			}
		}
	}
}

// TestImportHintNotInLibrary: a static library whose export uses a
// mem.Fixed gets no hint in any pack in release: its host's linker may
// keep an unreferenced function.
func TestImportHintNotInLibrary(t *testing.T) {
	tools := needTools(t)
	src := "import std.mem\n\n" +
		"export extern Two(n: i32): i32 {\n" +
		"    var buf: [64]u8 = [0; 64]\n" +
		"    f := mem.Fixed.New(mut buf[..])\n" +
		"    b := Box[i32].New(f.Alloc(), n) else {\n        return -1\n    }\n" +
		"    return b.Take(f.Alloc()) * 2\n" +
		"}\n"
	dir := writeTree(t, map[string]string{
		"pkg.mk": manifest(map[string]string{"name": `"hlib"`,
			"bins": `[pkg.Bin(name: "hlib", root: ".", link_script: "", cpu: "", subsystem: "", kind: "static", abi: "")]`}),
		"lib.mk": src,
	})
	var mu sync.Mutex
	lls := map[string]string{}
	cfg := Config{Root: dir, Target: pkTarget(), Profile: types.Release, Tools: tools, CacheDir: t.TempDir()}
	cfg.TestHooks.OnLL = func(name, ll string) { mu.Lock(); lls[name] = ll; mu.Unlock() }
	s, err := NewSession(cfg)
	if err != nil {
		t.Fatal(err)
	}
	r, paths, err := s.Build(t.TempDir() + string(filepath.Separator))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Report.Diags) > 0 || len(paths) != 1 {
		t.Fatalf("build: %d paths\n%s", len(paths), r.Report.Text(r.Sources))
	}
	found := false
	for name, ll := range lls {
		found = found || strings.Contains(ll, "@mk.std.mem.FixedAllocEntry")
		if strings.Contains(ll, "mk.importhint") {
			t.Errorf("library pack %s has an import hint", name)
		}
	}
	if !found {
		t.Errorf("no pack names Fixed's entries: the library does not use a Fixed")
	}
}

// TestImportHintNotForOtherTables: an allocator whose table is not a
// constant in the caller (the heap's, set at run time) gives the caller's
// pack no hint, and a table of the program's own gives one only for its
// entries that another pack defines. Packs are cut by instance name
// (iface.PackCutAfter), so which of NoTable's entries share Main's pack
// follows their names' hashes: the hint must name exactly the others.
func TestImportHintNotForOtherTables(t *testing.T) {
	tools := needTools(t)
	app := "import std.mem\n\n" +
		"NoAlloc(ctx: *u8, n: int, align: int): *u8 {\n    return ctx\n}\n\n" +
		"NoResize(ctx: *u8, p: *u8, old: int, new: int, align: int): bool {\n    return false\n}\n\n" +
		"NoFree(ctx: *u8, p: *u8, n: int, align: int) {\n}\n\n" +
		"const NoTable = mem.AllocTable(alloc: NoAlloc, resize: NoResize, free: NoFree)\n\n" +
		"export Keep(t: &mem.AllocTable): int {\n    return 0\n}\n\n" +
		"export Main(sys: &System): OutOfMemory!int {\n" +
		"    b := try Box[u64].New(sys.heap.Alloc(), 7)\n" +
		"    v := b.Take(sys.heap.Alloc())\n" +
		"    return Cast[int](v) - 7 + Keep(&NoTable)\n" +
		"}\n"
	dir := writeTree(t, hintFiles(app))
	cfg := Config{Root: dir, Target: pkTarget(), Profile: types.Release, Tools: tools, CacheDir: filepath.Join(t.TempDir(), "cache")}
	lls, ll := hintPackLL(t, cfg, filepath.Join(t.TempDir(), "app.exe"), "mk.app.Main")
	if !strings.Contains(ll, "mk.app.NoAlloc") {
		t.Fatalf("Main's pack does not hold NoTable's entries:\n%s", ll)
	}
	var want []string
	for _, e := range []string{"mk.app.NoAlloc", "mk.app.NoFree", "mk.app.NoResize"} {
		if defines(ll, e) {
			continue
		}
		elsewhere := false
		for _, other := range lls {
			elsewhere = elsewhere || defines(other, e)
		}
		if !elsewhere {
			t.Fatalf("no pack defines %s", e)
		}
		want = append(want, e)
	}
	if got := slices.Sorted(slices.Values(hintCalls(ll))); !slices.Equal(got, want) {
		t.Errorf("Main's pack hints %v, want %v: NoTable's entries that another pack defines, never the heap's", got, want)
	}
}

// hintTables is lib's source of entries AllocX, ResizeX and FreeX and
// const TX for each name in xs.
func hintTables(xs ...string) string {
	var b strings.Builder
	for _, x := range xs {
		b.WriteString("Alloc" + x + "(ctx: *u8, n: int, align: int): *u8 {\n    return ctx\n}\n\n" +
			"Resize" + x + "(ctx: *u8, p: *u8, old: int, new: int, align: int): bool {\n    return false\n}\n\n" +
			"Free" + x + "(ctx: *u8, p: *u8, n: int, align: int) {\n}\n\n" +
			"const T" + x + " = mem.AllocTable(alloc: Alloc" + x + ", resize: Resize" + x + ", free: Free" + x + ")\n\n")
	}
	return b.String()
}

// TestReleaseImportHintInAnotherPack: app's Main calls lib.Touch, a small
// function (codegen's smallfn.go) that takes an Alloc from a mem.Fixed,
// and lib.Get, a small function that returns &TA, so app's pack hints
// Fixed's entries and TA's though app's source names no table. lib.Both,
// called twice and looping, so neither small nor cross-marked, keeps both
// tables' entries in the unit, so the edit adds or removes no instance
// (which would miss every pack). The edit makes Get return &TB, with
// app's source unchanged and Get's marks and effect the same: app's hint
// now names TB's entries, so app's pack key
// must move with lib's body. Without the hint's entries in PackFacts app's
// pack hits its stale memo, which the MK_CACHE_VERIFY build reports.
func TestReleaseImportHintInAnotherPack(t *testing.T) {
	tools := needTools(t)
	base := map[string]string{
		"pkg.mk": manifest(map[string]string{"bins": `[pkg.Bin(name: "app", root: "app", link_script: "", cpu: "", subsystem: "console")]`}),
		"lib/lib.mk": "import std.mem\n\n" + hintTables("A", "B") +
			"export Keep(a: &Alloc): int {\n    return 0\n}\n\n" +
			"export Touch(f: &mem.Fixed): int {\n    return Keep(f.Alloc())\n}\n\n" +
			"export Get(): &mem.AllocTable {\n    return &TA\n}\n\n" +
			"export Use(t: &mem.AllocTable): int {\n    return 0\n}\n\n" +
			"export Both(n: int): int {\n    var s := 0\n    for _ in 0..n {\n        s = s + Use(&TA) + Use(&TB)\n    }\n    return s\n}\n",
		"app/app.mk": "import lib\nimport std.mem\n\n" +
			"export Main(sys: &System): int {\n" +
			"    var buf: [64]u8 = [0; 64]\n" +
			"    f := mem.Fixed.New(mut buf[..])\n" +
			"    return lib.Touch(&f) + lib.Use(lib.Get()) + lib.Use(lib.Get()) + lib.Both(0) + lib.Both(1)\n" +
			"}\n",
	}
	dir := writeTree(t, base)
	out := t.TempDir()
	cfg := Config{Root: dir, Target: pkTarget(), Profile: types.Release, Tools: tools, CacheDir: filepath.Join(t.TempDir(), "cache")}
	_, ll := hintPackLL(t, cfg, filepath.Join(out, "cold.exe"), "mk.app.Main")
	got := hintCalls(ll)
	if !slices.Contains(got, "mk.std.mem.FixedAllocEntry") || !slices.Contains(got, "mk.lib.AllocA") || slices.Contains(got, "mk.lib.AllocB") {
		t.Fatalf("app's hint calls %v, want Fixed's entries and TA's", got)
	}
	// OnLL keys no pack, so a build without it fills the memo.
	pkBuild(t, cfg, filepath.Join(out, "keyed.exe"))
	e := pkEdit{name: "hint", file: "lib/lib.mk", old: "    return &TA\n", new: "    return &TB\n"}
	pkWrite(t, dir, e.apply(t, base))
	ed := pkBuild(t, cfg, filepath.Join(out, "edit.exe"))
	if got := ed.emittedMods(); !slices.Contains(got, "lib") || !slices.Contains(got, "app") {
		t.Errorf("emitted %v, want lib (Get's body) and app (its hint names TB's entries)", got)
	}
	pkVerify(t, cfg, filepath.Join(out, "verify.exe"), ed.exe)
}
