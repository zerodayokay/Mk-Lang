// Why: packages can build DLLs, shared objects and static archives, each
// with its own link line and export rules.
// What: TestLibraryDynamicArgs, TestLibraryDynamicLinks and
// TestLibraryStaticArchive, over the two-function library libLL.
// How: compares command lines, then links real libraries and checks exports,
// the soname and archive members.

package toolchain

import (
	"bytes"
	"context"
	"debug/elf"
	"debug/pe"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// libLL is a two-function library: Twice is the export, Helper a hidden
// function the version script or .def leaves out.
const libLL = `
define hidden i32 @Helper(i32 %x) {
  %r = mul i32 %x, 2
  ret i32 %r
}
define DLL i32 @Twice(i32 %x) {
  %r = call i32 @Helper(i32 %x)
  ret i32 %r
}
`

func compileLL(t *testing.T, tl *Tools, triple, dir, name, body string) string {
	t.Helper()
	facts, err := tl.Probe(context.Background(), triple)
	if err != nil {
		t.Fatal(err)
	}
	ll := filepath.Join(dir, name+".ll")
	writeFile(t, ll, facts.Header()+body)
	obj := filepath.Join(dir, name+".o")
	if err := tl.Compile(context.Background(), CompileJob{Triple: triple, Input: ll, Output: obj, Opt: O0, CPU: "x86-64-v2"}); err != nil {
		t.Fatal(err)
	}
	return obj
}

// TestLibraryDynamicArgs: the command lines of each library kind and target.
func TestLibraryDynamicArgs(t *testing.T) {
	// The paths are the host's absolute ones (linkArgs refuses a relative
	// output, and `C:\o` is relative on Linux), so the test holds there too.
	r := filepath.Join(t.TempDir(), "o")
	at := func(n string) string { return filepath.Join(r, n) }
	coff, err := linkArgs(LinkJob{Format: COFF, Kind: Dynamic, Output: at("m.dll"), ImpLib: at("m.lib"), Def: at("m.def"), Objects: []string{at("a.o")}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(coff, " "), "/nologo /nodefaultlib /dll /noentry /machine:x64 /Brepro /out:"+at("m.dll")+" /implib:"+at("m.lib")+" /def:"+at("m.def")+" "+at("a.o")+" /opt:noicf"; got != want {
		t.Errorf("COFF: %s", got)
	}
	el, err := linkArgs(LinkJob{Format: ELF, Kind: Dynamic, Output: at("libm.so.1"), Soname: "libm.so.1", VersionScript: at("m.ver"), Objects: []string{at("a.o")}, Release: true})
	if err != nil {
		t.Fatal(err)
	}
	want := "-shared -nostdlib --build-id=none -z noexecstack -z now -z relro -z text --hash-style=gnu -Bsymbolic -soname=libm.so.1 --version-script=" +
		at("m.ver") + " -o " + at("libm.so.1") + " " + at("a.o") + " --gc-sections --strip-all"
	if got := strings.Join(el, " "); got != want {
		t.Errorf("ELF: %s", got)
	}
	for _, bad := range []LinkJob{
		{Format: COFF, Kind: Dynamic, Output: at("m.dll"), Objects: []string{at("a.o")}},                                                   // no implib
		{Format: ELF, Kind: Dynamic, Output: at("l.so"), VersionScript: at("v"), Objects: []string{at("a.o")}},                             // no soname
		{Format: ELF, Kind: Dynamic, Output: at("l.so"), Soname: "l.so", VersionScript: at("v"), Objects: []string{at("a.o")}, Entry: "x"}, // an entry
		{Format: ELF, Output: at("a"), Soname: "l.so", Objects: []string{at("a.o")}},                                                       // soname on an executable
		{Format: ELF, Kind: Static, Output: at("a"), Objects: []string{at("a.o")}},                                                         // static is not a link
	} {
		if _, err := linkArgs(bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

// TestLibraryDynamicLinks: a DLL with its import library on COFF and a
// shared object on ELF link from a real object; the ELF one has its
// soname and exports Twice only.
func TestLibraryDynamicLinks(t *testing.T) {
	tl := realTools(t)
	ctx := context.Background()
	t.Run("coff", func(t *testing.T) {
		const triple = "x86_64-pc-windows-msvc"
		if miss := tl.Need(triple); len(miss) > 0 {
			t.Skipf("SKIP: %s lacks %v", tl.Dir, miss)
		}
		dir := t.TempDir()
		obj := compileLL(t, tl, triple, dir, "lib", strings.ReplaceAll(libLL, "DLL", "dllexport"))
		def := filepath.Join(dir, "m.def")
		writeFile(t, def, "LIBRARY m.dll\nEXPORTS\nTwice\n")
		j := LinkJob{Format: COFF, Kind: Dynamic, Output: filepath.Join(dir, "m.dll"), ImpLib: filepath.Join(dir, "m.lib"), Def: def, Objects: []string{obj}}
		if err := tl.Link(ctx, j); err != nil {
			t.Fatal(err)
		}
		f, err := pe.Open(j.Output)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if f.Characteristics&pe.IMAGE_FILE_DLL == 0 {
			t.Error("not a DLL")
		}
		oh := f.OptionalHeader.(*pe.OptionalHeader64)
		if oh.AddressOfEntryPoint != 0 || oh.DataDirectory[pe.IMAGE_DIRECTORY_ENTRY_EXPORT].Size == 0 {
			t.Errorf("entry %#x, export directory %d bytes", oh.AddressOfEntryPoint, oh.DataDirectory[0].Size)
		}
		if b, err := os.ReadFile(j.ImpLib); err != nil || !bytes.HasPrefix(b, []byte("!<arch>\n")) {
			t.Errorf("import library: %v", err)
		}
	})
	t.Run("elf", func(t *testing.T) {
		const triple = "x86_64-unknown-linux-gnu"
		if miss := tl.Need(triple); len(miss) > 0 {
			t.Skipf("SKIP: %s lacks %v", tl.Dir, miss)
		}
		dir := t.TempDir()
		obj := compileLL(t, tl, triple, dir, "lib", strings.ReplaceAll(libLL, "DLL", ""))
		ver := filepath.Join(dir, "m.ver")
		writeFile(t, ver, "M_1 { global: Twice; local: *; };\n")
		j := LinkJob{Format: ELF, Kind: Dynamic, Output: filepath.Join(dir, "libm.so.1"), Soname: "libm.so.1", VersionScript: ver, Objects: []string{obj}}
		if err := tl.Link(ctx, j); err != nil {
			t.Fatal(err)
		}
		f, err := elf.Open(j.Output)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if f.Type != elf.ET_DYN {
			t.Errorf("type %v", f.Type)
		}
		if so, _ := f.DynString(elf.DT_SONAME); !slices.Equal(so, []string{"libm.so.1"}) {
			t.Errorf("soname %q", so)
		}
		syms, _ := f.DynamicSymbols()
		var names []string
		for _, s := range syms {
			if s.Section != elf.SHN_UNDEF {
				names = append(names, s.Name)
			}
		}
		if !slices.Contains(names, "Twice") || slices.Contains(names, "Helper") {
			t.Errorf("dynamic symbols %q", names)
		}
	})
}

// TestLibraryStaticArchive: the Go-written GNU archive indexes its
// objects' globals, and ld.lld pulls the member that defines the entry
// from it; on COFF llvm-lib writes the .lib.
func TestLibraryStaticArchive(t *testing.T) {
	tl := realTools(t)
	ctx := context.Background()
	const triple = "x86_64-unknown-linux-gnu"
	if miss := tl.Need(triple); len(miss) > 0 {
		t.Skipf("SKIP: %s lacks %v", tl.Dir, miss)
	}
	dir := t.TempDir()
	facts, err := tl.Probe(ctx, triple)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join("testdata", "smoke_linux.ll"))
	if err != nil {
		t.Fatal(err)
	}
	smoke := filepath.Join(dir, "a-member-with-a-long-name.o")
	ll := filepath.Join(dir, "smoke.ll")
	writeFile(t, ll, facts.Header()+string(body))
	if err := tl.Compile(ctx, CompileJob{Triple: triple, Input: ll, Output: smoke, Opt: O0, CPU: "x86-64-v2"}); err != nil {
		t.Fatal(err)
	}
	other := compileLL(t, tl, triple, dir, "lib", strings.ReplaceAll(libLL, "DLL", ""))
	ar := filepath.Join(dir, "libsmoke.a")
	if err := tl.StaticLib(ctx, ELF, []string{other, smoke}, ar); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(ar)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(b, []byte("!<arch>\n/ ")) || !bytes.Contains(b, []byte("_start\x00")) || bytes.Contains(b, []byte("a-member-with-a-long-name")) || !bytes.Contains(b, []byte("o0001.o/        0 ")) {
		// Members are named by index, never by file.
		t.Errorf("archive layout: % .200q", b)
	}
	// Writers of one archive at once, as two builds of a package are,
	// all succeed and leave the whole archive and no temporary, not even
	// the one a killed writer left an hour ago.
	shared := filepath.Join(t.TempDir(), "libsmoke.a")
	killed := filepath.Join(filepath.Dir(shared), ".libsmoke.a.tmp-111")
	hour := time.Now().Add(-time.Hour)
	if err := os.WriteFile(killed, []byte("half"), 0o644); err != nil || os.Chtimes(killed, hour, hour) != nil {
		t.Fatal(err)
	}
	for round := 0; round < 10; round++ {
		var wg sync.WaitGroup
		errs := make(chan error, 8)
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs <- WriteArchive(shared, []string{other, smoke})
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("round %d: %v", round, err)
			}
		}
		got, err := os.ReadFile(shared)
		ents, _ := os.ReadDir(filepath.Dir(shared))
		if err != nil || !bytes.Equal(got, b) || len(ents) != 1 {
			t.Fatalf("round %d: the archive (%d bytes, %v) is not the one written alone, or temporaries are left: %d entries", round, len(got), err, len(ents))
		}
	}
	dummy := compileLL(t, tl, triple, dir, "dummy", "@dummy = global i32 0\n")
	exe := filepath.Join(dir, "smoke")
	if err := tl.Link(ctx, LinkJob{Format: ELF, Output: exe, Objects: []string{dummy}, Archives: []string{ar}}); err != nil {
		t.Fatal(err)
	}
	f, err := elf.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	if f.Entry == 0 {
		t.Error("no entry: the archive member was not pulled")
	}
	f.Close()
	if err := tl.StaticLib(ctx, ELF, []string{dir}, ar); err == nil {
		t.Error("a directory archived")
	}
	const wtriple = "x86_64-pc-windows-msvc"
	if miss := tl.Need(wtriple); len(miss) == 0 {
		wobj := compileLL(t, tl, wtriple, t.TempDir(), "lib", strings.ReplaceAll(libLL, "DLL", ""))
		lib := filepath.Join(dir, "m.lib")
		old := responseFileAt
		responseFileAt = 0 // and through a response file
		err := tl.StaticLib(ctx, COFF, []string{wobj}, lib)
		responseFileAt = old
		if err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(lib); !bytes.HasPrefix(b, []byte("!<arch>\n")) || !bytes.Contains(b, []byte("Twice")) {
			t.Error("COFF static library lacks Twice")
		}
	}
}
