// Why: every compile and link goes through command lines built here; a
// wrong flag changes the binary silently.
// What: tests of the compile, link, import-library and probe command lines,
// the .def format (ParseDefList, DefText) and the LTO cache policy.
// How: compares argument lists as data; TestImportLibFromDef also builds a
// .lib and reads the linked executable's imports with debug/pe.

package toolchain

import (
	"context"
	"debug/pe"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// absPath makes a host-absolute path for command-line tests.
func absPath(t *testing.T, name string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join(string(filepath.Separator)+"w", name))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCompileCommandLine(t *testing.T) {
	in, out := absPath(t, "p.ll"), absPath(t, "p.o")
	tail := []string{"-ffunction-sections", "-fdata-sections", "-fno-addrsig", "-fdebug-compilation-dir=."}
	coff := append(append([]string{}, tail...), "-mno-incremental-linker-compatible", "-Xclang", "-gno-codeview-command-line")
	cases := []struct {
		j    CompileJob
		want []string
	}{
		{CompileJob{Triple: "x86_64-pc-windows-msvc", Input: in, Output: out},
			append([]string{"--target=x86_64-pc-windows-msvc19.33.0", "-x", "ir", "-c", in, "-o", out, "-O0"}, coff...)},
		{CompileJob{Triple: "x86_64-unknown-linux-gnu", Input: in, Output: out, Opt: O3, LTO: true, CPU: "x86-64-v2"},
			append([]string{"--target=x86_64-unknown-linux-gnu", "-x", "ir", "-c", in, "-o", out, "-O3", "-flto=thin", "-march=x86-64-v2"}, tail...)},
		{CompileJob{Triple: "aarch64-unknown-linux-gnu", Input: in, Output: out, Opt: O3, CPU: "neoverse-n1"},
			append(append([]string{"--target=aarch64-unknown-linux-gnu", "-x", "ir", "-c", in, "-o", out, "-O3", "-mcpu=neoverse-n1"}, tail...), "-mno-outline-atomics")},
	}
	for _, c := range cases {
		got, err := compileArgs(c.j)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("compileArgs(%+v) =\n%q, %v\nwant\n%q", c.j, got, err, c.want)
		}
	}
	for _, bad := range []CompileJob{
		{Triple: "x86_64-pc-windows-msvc", Input: "p.ll", Output: out},
		{Triple: "x86_64-pc-windows-msvc", Input: in, Output: out, CPU: "native"},
		{Input: in, Output: out},
	} {
		if _, err := compileArgs(bad); err == nil {
			t.Errorf("compileArgs(%+v) accepted", bad)
		}
	}
}

func TestLinkCommandLine(t *testing.T) {
	out, o1, o2, lib := absPath(t, "a.exe"), absPath(t, "1.o"), absPath(t, "2.o"), absPath(t, "kernel32.lib")
	got, err := linkArgs(LinkJob{Format: COFF, Output: out, Objects: []string{o1, o2}, Libs: []string{lib}, StackReserve: 8 << 20})
	want := []string{"/nologo", "/nodefaultlib", "/entry:mk_start", "/subsystem:console", "/osversion:10.0", "/machine:x64", "/Brepro",
		"/out:" + out, o1, o2, lib, "/opt:noicf", "/stack:8388608"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("dev COFF =\n%q, %v\nwant\n%q", got, err, want)
	}
	// Static data aligned over a page sets the image's section
	// alignment, after the stack reservation.
	got, err = linkArgs(LinkJob{Format: COFF, Output: out, Objects: []string{o1}, StackReserve: 8 << 20, SectionAlign: 65536})
	want = []string{"/nologo", "/nodefaultlib", "/entry:mk_start", "/subsystem:console", "/osversion:10.0", "/machine:x64", "/Brepro",
		"/out:" + out, o1, "/opt:noicf", "/stack:8388608", "/align:65536"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("COFF with SectionAlign =\n%q, %v\nwant\n%q", got, err, want)
	}
	got, err = linkArgs(LinkJob{Format: ELF, Output: out, Objects: []string{o1}})
	want = []string{"-static", "-nostdlib", "-e", "_start", "--build-id=none", "-z", "noexecstack", "-o", out, o1}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("dev ELF =\n%q, %v\nwant\n%q", got, err, want)
	}
	for _, bad := range []LinkJob{
		{Format: COFF, Output: "a.exe", Objects: []string{o1}},
		{Format: COFF, Output: out},
		{Format: COFF, Output: out, Objects: []string{"1.o"}},
		{Format: ELF, Output: out, Objects: []string{o1}, Libs: []string{lib}},
		{Format: NoFormat, Output: out, Objects: []string{o1}},
		{Format: ELF, Output: out, Objects: []string{o1}, LTO: true, LTOCacheDir: absPath(t, "lto")},
		{Format: ELF, Output: out, Objects: []string{o1}, LTOCacheDir: absPath(t, "lto")},
		{Format: COFF, Output: out, Objects: []string{o1}, LTOCacheDir: absPath(t, "lto")},
		{Format: ELF, Output: out, Objects: []string{o1}, LTOCacheSize: -1},
		{Format: ELF, Output: out, Objects: []string{o1}, SectionAlign: 65536},
	} {
		if _, err := linkArgs(bad); err == nil {
			t.Errorf("linkArgs(%+v) accepted", bad)
		}
	}
}

// TestLinkPassesLTOCachePolicy: release links with ThinLTO carry
// the cache directory and the pruning bound of LTOCacheSize.
func TestLinkPassesLTOCachePolicy(t *testing.T) {
	out, o1, dir := absPath(t, "a.exe"), absPath(t, "1.o"), absPath(t, "thinlto")
	const size = 268435456
	coff, err := linkArgs(LinkJob{Format: COFF, Output: out, Objects: []string{o1}, Release: true, LTO: true,
		LTOCacheDir: dir, LTOCacheSize: size, Jobs: 8})
	if err != nil {
		t.Fatal(err)
	}
	wantCOFF := []string{"/opt:ref", "/opt:icf", "/opt:lldlto=3", "/lldltocache:" + dir,
		"/lldltocachepolicy:cache_size_bytes=268435456:prune_after=48h", "/opt:lldltojobs=8"}
	if !reflect.DeepEqual(coff[len(coff)-len(wantCOFF):], wantCOFF) {
		t.Errorf("release COFF = %q", coff)
	}
	elf, err := linkArgs(LinkJob{Format: ELF, Output: out, Objects: []string{o1}, Release: true, LTO: true,
		LTOCacheDir: dir, LTOCacheSize: size, Jobs: 8})
	if err != nil {
		t.Fatal(err)
	}
	wantELF := []string{"--gc-sections", "--strip-all", "--lto-O3", "--thinlto-cache-dir=" + dir,
		"--thinlto-cache-policy=cache_size_bytes=268435456:prune_after=48h", "--thinlto-jobs=8"}
	if !reflect.DeepEqual(elf[len(elf)-len(wantELF):], wantELF) {
		t.Errorf("release ELF = %q", elf)
	}
	// --debug keeps the tables in a release ELF.
	dbg, err := linkArgs(LinkJob{Format: ELF, Output: out, Objects: []string{o1}, Release: true, Debug: true})
	if err != nil || slices.Contains(dbg, "--strip-all") {
		t.Errorf("release --debug ELF = %q, %v", dbg, err)
	}
	// No cache directory: no cache flags, and no size is needed.
	elf, err = linkArgs(LinkJob{Format: ELF, Output: out, Objects: []string{o1}, LTO: true})
	if err != nil || strings.Contains(strings.Join(elf, " "), "cache") {
		t.Errorf("LTO without cache = %q, %v", elf, err)
	}
}

func TestParseDefList(t *testing.T) {
	got, err := ParseDefList("ExitProcess\r\nGetStdHandle\n\n  WriteFile  \n")
	if err != nil || !reflect.DeepEqual(got, []string{"ExitProcess", "GetStdHandle", "WriteFile"}) {
		t.Errorf("ParseDefList = %q, %v", got, err)
	}
	if _, err := ParseDefList("ExitProcess\nGet StdHandle\n"); err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Errorf("inner space: %v", err)
	}
}

func TestDefText(t *testing.T) {
	got, err := DefText("kernel32.dll", []string{"ExitProcess", "WriteFile"})
	if err != nil || got != "LIBRARY kernel32.dll\nEXPORTS\nExitProcess\nWriteFile\n" {
		t.Errorf("DefText = %q, %v", got, err)
	}
	for _, bad := range []struct {
		dll   string
		names []string
	}{{"kernel32.dll", nil}, {"", []string{"A"}}, {"k.dll", []string{"A B"}}, {"k.dll", []string{""}}} {
		if _, err := DefText(bad.dll, bad.names); err == nil {
			t.Errorf("DefText(%q, %q) accepted", bad.dll, bad.names)
		}
	}
}

// TestImportLibFromDef: the runtime's format (three names, one per
// line) plus the DLL name gives a .lib whose import descriptor names
// kernel32.dll; an executable linked against it imports kernel32.dll (the
// smoke test checks the executable side with debug/pe).
func TestImportLibFromDef(t *testing.T) {
	tl := realTools(t)
	dir := t.TempDir()
	def, err := os.ReadFile(filepath.Join("testdata", "kernel32.def"))
	if err != nil {
		t.Fatal(err)
	}
	names, err := ParseDefList(string(def))
	if err != nil || len(names) != 3 {
		t.Fatalf("testdata/kernel32.def: %q, %v", names, err)
	}
	text, _ := DefText("kernel32.dll", names)
	if !strings.HasPrefix(text, "LIBRARY kernel32.dll\nEXPORTS\n") {
		t.Errorf("module-definition text = %q", text)
	}
	out := filepath.Join(dir, "kernel32.lib")
	if err := tl.ImportLib(context.Background(), "kernel32.dll", names, "x64", out); err != nil {
		t.Fatal(err)
	}
	lib, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(lib), "!<arch>\n") || !strings.Contains(string(lib), "kernel32.dll") ||
		!strings.Contains(string(lib), "__IMPORT_DESCRIPTOR_kernel32") {
		t.Errorf("kernel32.lib is not an import library for kernel32.dll (%d bytes)", len(lib))
	}
	for _, n := range names {
		if !strings.Contains(string(lib), "__imp_"+n) {
			t.Errorf("kernel32.lib lacks __imp_%s", n)
		}
	}
	// The temporary .def is removed.
	if left, _ := filepath.Glob(filepath.Join(dir, "*.def")); len(left) != 0 {
		t.Errorf("temporary files left: %q", left)
	}
	// The same input twice gives the same bytes (it is cached by .def hash).
	out2 := filepath.Join(dir, "again.lib")
	if err := tl.ImportLib(context.Background(), "kernel32.dll", names, "x64", out2); err != nil {
		t.Fatal(err)
	}
	if lib2, _ := os.ReadFile(out2); string(lib2) != string(lib) {
		t.Error("import library not byte-identical across runs")
	}
}

// importsOf lists the DLLs a PE file imports, in first-use order, from its
// import table (debug/pe's ImportedLibraries is unimplemented and returns
// nothing, so the DLL names come from ImportedSymbols' "name:dll" form).
func importsOf(t *testing.T, path string) []string {
	t.Helper()
	f, err := pe.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	syms, err := f.ImportedSymbols()
	if err != nil {
		t.Fatal(err)
	}
	var libs []string
	seen := map[string]bool{}
	for _, s := range syms {
		_, dll, ok := strings.Cut(s, ":")
		if !ok {
			t.Fatalf("import %q has no DLL", s)
		}
		if !seen[strings.ToLower(dll)] {
			seen[strings.ToLower(dll)] = true
			libs = append(libs, dll)
		}
	}
	return libs
}

func TestRunner(t *testing.T) {
	wsl := func(string) (string, error) { return `C:\Windows\System32\wsl.exe`, nil }
	none := func(string) (string, error) { return "", os.ErrNotExist }
	cases := []struct {
		triple, goos, goarch string
		look                 func(string) (string, error)
		want                 []string
		ok                   bool
	}{
		{"x86_64-pc-windows-msvc", "windows", "amd64", none, nil, true},
		{"x86_64-unknown-linux-gnu", "linux", "amd64", none, nil, true},
		{"x86_64-unknown-linux-gnu", "windows", "amd64", wsl, []string{`C:\Windows\System32\wsl.exe`, "-e"}, true},
		{"x86_64-unknown-linux-gnu", "windows", "amd64", none, nil, false},
		{"aarch64-unknown-linux-gnu", "windows", "amd64", wsl, nil, false},
		{"x86_64-pc-windows-msvc", "linux", "amd64", wsl, nil, false},
	}
	for _, c := range cases {
		got, ok := runnerFor(c.triple, c.goos, c.goarch, c.look)
		if ok != c.ok || !reflect.DeepEqual(got, c.want) {
			t.Errorf("runnerFor(%s on %s) = %q, %v", c.triple, c.goos, got, ok)
		}
	}
	prefix := []string{`C:\Windows\System32\wsl.exe`, "-e"}
	for in, want := range map[string]string{
		`C:\Users\k\AppData\Local\Temp\x\a.out`: "/mnt/c/Users/k/AppData/Local/Temp/x/a.out",
		`d:/src/mk/tests/a`:                     "/mnt/d/src/mk/tests/a",
		`\\server\share\a`:                      `\\server\share\a`,
		"relative":                              "relative",
	} {
		if got := HostPathFor(prefix, in); got != want {
			t.Errorf("HostPathFor(%q) = %q, want %q", in, got, want)
		}
	}
	if got := HostPathFor(nil, `C:\a`); got != `C:\a` {
		t.Errorf("nil prefix rewrote %q", got)
	}
}

// TestLTOCacheSizeWithoutLTO: LTOCacheSize 0 with an LTOCacheDir is an error
// even when the link does not use LTO; with a size, such a link is accepted
// and carries no cache flags.
func TestLTOCacheSizeWithoutLTO(t *testing.T) {
	out, o1, dir := absPath(t, "a.exe"), absPath(t, "1.o"), absPath(t, "thinlto")
	for _, f := range []Format{COFF, ELF} {
		if _, err := linkArgs(LinkJob{Format: f, Output: out, Objects: []string{o1}, LTOCacheDir: dir}); err == nil {
			t.Errorf("%v: LTOCacheDir with size 0 and no LTO accepted", f)
		}
		args, err := linkArgs(LinkJob{Format: f, Output: out, Objects: []string{o1}, LTOCacheDir: dir, LTOCacheSize: 1 << 20})
		if err != nil || strings.Contains(strings.Join(args, " "), "cache") {
			t.Errorf("%v: no LTO with a sized cache = %q, %v", f, args, err)
		}
	}
}

// TestDefNamesAreIdentifiers: a name holding module-definition syntax would
// silently change the import library (';' a comment, '=' an alias, '@' an
// ordinal, a keyword), so DefText and ParseDefList take plain identifiers
// only.
func TestDefNamesAreIdentifiers(t *testing.T) {
	for _, n := range []string{"A;B", "A=B", "A@1", "DATA", "data", "PRIVATE", "NONAME", "EXPORTS",
		"LIBRARY", "1A", "A.B", "A-B", "\"A\"", "A,B", "Ä"} {
		if _, err := DefText("k.dll", []string{n}); err == nil {
			t.Errorf("DefText name %q accepted", n)
		}
		if _, err := ParseDefList("ExitProcess\n" + n + "\n"); err == nil || !strings.Contains(err.Error(), "line 2") {
			t.Errorf("ParseDefList name %q: %v", n, err)
		}
	}
	for _, n := range []string{"ExitProcess", "_fltused", "WSAStartup", "GetQueuedCompletionStatusEx", "Sleep2"} {
		if _, err := DefText("ws2_32.dll", []string{n}); err != nil {
			t.Errorf("DefText name %q refused: %v", n, err)
		}
	}
	for _, d := range []string{"k;x.dll", "k=x.dll", ".dll", "k x.dll", `C:\k.dll`} {
		if _, err := DefText(d, []string{"A"}); err == nil {
			t.Errorf("DefText DLL %q accepted", d)
		}
	}
	if _, err := DefText("api-ms-win-core-synch-l1-2-0.dll", []string{"WaitOnAddress"}); err != nil {
		t.Errorf("api set DLL refused: %v", err)
	}
}

// TestImportLibCommandLine: llvm-lib's arguments as text.
func TestImportLibCommandLine(t *testing.T) {
	def, out := absPath(t, "implib-1.def"), absPath(t, "kernel32.lib")
	got := importLibArgs(def, "x64", out)
	want := []string{"/def:" + def, "/machine:x64", "/out:" + out}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("importLibArgs = %q, want %q", got, want)
	}
}

// TestProbeCommandLine: the probe reads an empty IR file, never C, and
// prints the module to stdout.
func TestProbeCommandLine(t *testing.T) {
	empty := absPath(t, "empty.ll")
	got := probeArgs("x86_64-unknown-linux-gnu", empty)
	want := []string{"--target=x86_64-unknown-linux-gnu", "-x", "ir", "-S", "-emit-llvm", "-o", "-", empty}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("probeArgs = %q, want %q", got, want)
	}
}
