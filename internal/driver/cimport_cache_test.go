// Why: the C caches (a translation's candidates, a C object's) must hit
// after a header edit, and must miss when a header appears where Clang
// would now find it first: beside the includer, in the include stack's
// directories (msvc), or in a directory the C source's own flags name;
// an object they do not cache must not leave a stale executable.
// What: TestCImport_EditedHeaderHits, TestCImport_BesideIncluder,
// TestCObj_IncludeStackShadow (the msvc rule), TestCObj_FlagIncludeDirs,
// TestCObj_UncachedRelinks and TestCObj_SearchDirsFlags.
// How: packages checked or built twice against one cache, a header added
// or edited between the builds; the flag parser on a job.

package driver

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"mk/internal/toolchain"
)

// checkHit checks the package at dir with cache and reports whether the
// translation was a cache hit and the DepMagic its view binds.
func checkHit(t *testing.T, dir, cache string) (bool, string) {
	t.Helper()
	s, err := NewSession(Config{Root: dir, Tools: needTools(t), CacheDir: cache})
	if err != nil {
		t.Fatal(err)
	}
	r := s.Check()
	if len(r.Report.Diags) > 0 {
		t.Fatalf("check:\n%s", r.Report.Text(r.Sources))
	}
	view, _ := os.ReadFile(filepath.Join(dir, "gen", HostTarget().Name, "c", "k.mk"))
	_, v, _ := strings.Cut(string(view), "DepMagic: i32 = ")
	v, _, _ = strings.Cut(v, "\n")
	return s.w.cimports.hit, v
}

// TestCImport_EditedHeaderHits: after a header edit the first
// check translates and the second hits; restored, the old content hits
// at once (a key keeps one candidate per content).
func TestCImport_EditedHeaderHits(t *testing.T) {
	files := cBin("edh", "#define DEP_MAGIC 1\nint k_fn(void);\n", "", cMain("", "", "{c}", "c: k.DepMagic"))
	dir := writeTree(t, files)
	cache := t.TempDir()
	set := func(v string) {
		if err := os.WriteFile(filepath.Join(dir, "lib", "k.h"), []byte("#define DEP_MAGIC "+v+"\nint k_fn(void);\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for i, st := range []struct {
		set  string
		hit  bool
		want string
	}{{"", false, "1"}, {"", true, "1"}, {"9", false, "9"}, {"", true, "9"}, {"1", true, "1"}, {"9", true, "9"}} {
		if st.set != "" {
			set(st.set)
		}
		if hit, v := checkHit(t, dir, cache); hit != st.hit || v != st.want {
			t.Errorf("check %d: hit %v DepMagic %s, want %v %s", i, hit, v, st.hit, st.want)
		}
	}
}

// TestCImport_BesideIncluder: lib/k.h's "dep.h" is found in
// a/inc; a dep.h added beside lib/k.h comes first for a quoted include, so
// the binding, the shim and the C source all read it on the warm build.
func TestCImport_BesideIncluder(t *testing.T) {
	needTools(t)
	files := cBin("besides", "#include \"dep.h\"\nint k_fn(void);\nstatic inline int k_inl(void) { return DEP_MAGIC; }\n",
		"#include \"lib/k.h\"\nint k_fn(void) { return DEP_MAGIC; }\n",
		cMain("", "        a := k.k_inl()\n        b := k.k_fn()\n", "{a} {b} {c}", "a: a, b: b, c: k.DepMagic"))
	files["pkg.mk"] = strings.Replace(files["pkg.mk"], "vendor: false)", `vendor: false, include_dirs: ["a/inc", "b/inc"])`, 1)
	files["a/inc/dep.h"] = "#define DEP_MAGIC 1\n"
	files["b/inc/dep.h"] = "#define DEP_MAGIC 2\n"
	dir := writeTree(t, files)
	cache := t.TempDir()
	if out := buildRun(t, dir, cache); out != "1 1 1\n" {
		t.Fatalf("first build printed %q, want 1 1 1", out)
	}
	if err := os.WriteFile(filepath.Join(dir, "lib", "dep.h"), []byte("#define DEP_MAGIC 5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := buildRun(t, dir, cache); out != "5 5 5\n" {
		t.Errorf("after adding lib/dep.h printed %q, want 5 5 5", out)
	}
}

// TestCObj_IncludeStackShadow: csrc/k.c includes "sub/a.h",
// which includes "q.h", found in b/inc. On the msvc target (a C object
// for Windows) Clang searches the whole include stack's directories
// first, so a q.h added beside k.c shadows it; on the gnu targets it does
// not. The warm build prints what a fresh one would.
func TestCObj_IncludeStackShadow(t *testing.T) {
	needTools(t)
	files := cBin("stack", "int k_fn(void);\n", "", cMain("", "        b := k.k_fn()\n", "{b}", "b: b"))
	files["pkg.mk"] = strings.Replace(files["pkg.mk"], "vendor: false)", `vendor: false, include_dirs: ["b/inc"])`, 1)
	files["pkg.mk"] = strings.Replace(files["pkg.mk"], "c_sources: [],", `c_sources: [pkg.CSource(path: "csrc/k.c", flags: [])],`, 1)
	files["csrc/k.c"] = "#include \"sub/a.h\"\nint k_fn(void) { return Q; }\n"
	files["csrc/sub/a.h"] = "#include \"q.h\"\n"
	files["b/inc/q.h"] = "#define Q 2\n"
	dir := writeTree(t, files)
	cache := t.TempDir()
	if out := buildRun(t, dir, cache); out != "2\n" {
		t.Fatalf("first build printed %q, want 2", out)
	}
	if err := os.WriteFile(filepath.Join(dir, "csrc", "q.h"), []byte("#define Q 4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	want := "2\n"
	if runtime.GOOS == "windows" {
		want = "4\n"
	}
	if out := buildRun(t, dir, cache); out != want {
		t.Errorf("after adding csrc/q.h printed %q, want %q", out, want)
	}
}

// TestCObj_FlagIncludeDirs: the C source finds cdep.h through
// its own flags' second -I (the separate form); one added to the first
// (the joined form) shadows it, and the warm build recompiles.
func TestCObj_FlagIncludeDirs(t *testing.T) {
	needTools(t)
	files := cBin("flags", "int k_fn(void);\n", "#include \"cdep.h\"\nint k_fn(void) { return CDEP; }\n", cMain("", "        b := k.k_fn()\n", "{b}", "b: b"))
	files["q/inc/cdep.h"] = "#define CDEP 2\n"
	files["p/inc/README"] = "empty\n"
	dir := writeTree(t, files)
	slash := func(p string) string { return filepath.ToSlash(filepath.Join(dir, p)) }
	man := strings.Replace(files["pkg.mk"], `flags: []`, `flags: ["-I`+slash("p/inc")+`", "-I", "`+slash("q/inc")+`"]`, 1)
	if err := os.WriteFile(filepath.Join(dir, "pkg.mk"), []byte(man), 0o644); err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	if out := buildRun(t, dir, cache); out != "2\n" {
		t.Fatalf("first build printed %q, want 2", out)
	}
	if err := os.WriteFile(filepath.Join(dir, "p", "inc", "cdep.h"), []byte("#define CDEP 7\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := buildRun(t, dir, cache); out != "7\n" {
		t.Errorf("after adding p/inc/cdep.h printed %q, want 7", out)
	}
}

// TestCObj_UncachedRelinks: the flags of a response file the C source's
// flags name are in no key, so its object is not cached; after an edit of
// the file the warm build links the new object, not the stored executable.
func TestCObj_UncachedRelinks(t *testing.T) {
	needTools(t)
	files := cBin("rsp", "int k_fn(void);\n", "int k_fn(void) { return V; }\n", cMain("", "        b := k.k_fn()\n", "{b}", "b: b"))
	files["flags.rsp"] = "-DV=1\n"
	dir := writeTree(t, files)
	rsp := filepath.Join(dir, "flags.rsp")
	man := strings.Replace(files["pkg.mk"], `flags: []`, `flags: ["@`+filepath.ToSlash(rsp)+`"]`, 1)
	if err := os.WriteFile(filepath.Join(dir, "pkg.mk"), []byte(man), 0o644); err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	if out := buildRun(t, dir, cache); out != "1\n" {
		t.Fatalf("first build printed %q, want 1", out)
	}
	if err := os.WriteFile(rsp, []byte("-DV=2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := buildRun(t, dir, cache); out != "2\n" {
		t.Errorf("after editing flags.rsp printed %q, want 2", out)
	}
}

// TestCObj_SearchDirsFlags: the flags' directories take Clang's places on
// the path, joined or separate, a relative one under the compile's working
// directory, beside plain flags; any other flag is not modelled.
func TestCObj_SearchDirsFlags(t *testing.T) {
	abs := func(p string) string {
		if runtime.GOOS == "windows" {
			return filepath.Join(`C:\`, filepath.FromSlash(p))
		}
		return filepath.Join("/", p)
	}
	cj := toolchain.CJob{Output: abs("w/c0/x.o"), ResourceDir: abs("rd"), Include: []string{abs("root")}, System: []string{abs("libc")},
		Flags: []string{"-march=x86-64-v2", "-iquote", abs("q"), "-I" + abs("a"), "-I", abs("b"), "-isystem" + abs("s"), "-idirafter", abs("z"), "-Irel", "-DX=1",
			"-D", "Y", "-UZ", "-O2", "-g", "-std=c11", "-Wall", "-Wno-error=unused", "-w", "-fno-builtin", "-fno-pic", "-mno-red-zone"}}
	got, ok := cobjSearchDirs(cj)
	want := []string{abs("q"), abs("root"), abs("a"), abs("b"), abs("w/c0/rel"), abs("rd/include"), abs("libc"), abs("s"), abs("z")}
	if !ok || !slices.Equal(got, want) {
		t.Errorf("dirs %q %v, want %q", got, ok, want)
	}
	unmodelled := [][]string{{"-include", "x.h"}, {"-isystem-after", abs("s")}, {"-I"}, {"-I", ""}, {"-I-"}, {"-I=" + abs("s")}, {"-imultilib", "m"},
		{"-index-header-map"}, {"--include-directory=" + abs("a")}, {"-working-directory", abs("w")}, {"-Xclang", "-I" + abs("a")}, {"@flags.rsp"},
		{"--config=x.cfg"}, {"-MMD"}, {"--write-user-dependencies"}, {"-D"}, {"-mllvm", "-x"}, {"-mllvm=-x"}, {"-fmodules"}, {"-fprofile-use"},
		{"-fprofile-use=p.profdata"}, {"-fvisibility=hidden"}, {"-Wp,-Ia"}, {"-Wa,-Ia"}, {"extra.c"}}
	if runtime.GOOS == "windows" {
		unmodelled = append(unmodelled, []string{`-I\inc`}, []string{"-I/inc"}, []string{"-IC:inc"})
	}
	for _, f := range unmodelled {
		cj.Flags = f
		if _, ok := cobjSearchDirs(cj); ok {
			t.Errorf("flags %q are modelled", f)
		}
	}
}
