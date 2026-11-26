// Why: a release ships LLVM beside mk, and discovery must prefer that bundle
// over stray installs without ever mixing toolchains in one cache.
// What: tests of the bundle rule's place in the discovery order, one lld
// under both flavours with outputs equal to the named tools', import
// libraries without llvm-lib, and the install advice.
// How: builds fake bundle trees (bundleTree) for the order tests, and links
// real smoke programs with the host LLVM to compare bytes.

package toolchain

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
)

// bundleTree makes <root>/bin/mk[.exe], <root>/RELEASE.txt and <root>/llvm/bin holding the
// given tools, and returns the executable's path and the llvm/bin
// directory.
func bundleTree(t *testing.T, goos string, names ...string) (exe, llvmBin string) {
	t.Helper()
	root := t.TempDir()
	llvmBin = filepath.Join(root, "llvm", "bin")
	if err := os.MkdirAll(llvmBin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	suffix := ""
	if goos == "windows" {
		suffix = ".exe"
	}
	if err := os.WriteFile(filepath.Join(root, BundleMarker), []byte("mk test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	exe = filepath.Join(root, "bin", "mk"+suffix)
	if err := os.WriteFile(exe, []byte("fake mk"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(llvmBin, n+suffix), []byte("fake"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return exe, llvmBin
}

// TestDiscoverBundleOrder: --llvm and MK_LLVM beat the bundle, which beats
// the default locations and PATH; a bundle without clang falls through; the
// bundle's one lld makes it complete with no named linker and no llvm-lib.
func TestDiscoverBundleOrder(t *testing.T) {
	for _, goos := range []string{"windows", "linux"} {
		t.Run(goos, func(t *testing.T) {
			exe, bundle := bundleTree(t, goos, NameClang, NameLLD)
			flagD := fakeDir(t, goos, allTools...)
			envD := fakeDir(t, goos, allTools...)
			defD := fakeDir(t, goos, allTools...)
			pathD := fakeDir(t, goos, allTools...)
			bare, _ := bundleTree(t, goos) // llvm/bin holds nothing
			cases := []struct {
				name    string
				flagDir string
				env     map[string]string
				exe     string
				wantDir string
				wantSrc Source
			}{
				{"flag beats bundle", flagD, map[string]string{"MK_LLVM": envD}, exe, flagD, FlagDir},
				{"env beats bundle", "", map[string]string{"MK_LLVM": envD, "PATH": pathD}, exe, envD, EnvDir},
				{"bundle beats default and PATH", "", map[string]string{"PATH": pathD}, exe, bundle, BundleDir},
				{"bundle without clang falls through", "", map[string]string{"PATH": pathD}, bare, defD, DefaultDir},
				{"no executable", "", map[string]string{"PATH": pathD}, "", defD, DefaultDir},
			}
			for _, c := range cases {
				o := fakeOpts(goos, c.env, []string{defD})
				o.LLVMDir = c.flagDir
				e := c.exe
				o.Executable = func() (string, error) {
					if e == "" {
						return "", errors.New("no executable")
					}
					return e, nil
				}
				tl, err := Discover(o)
				if err != nil {
					t.Fatalf("%s: %v", c.name, err)
				}
				if tl.Dir != c.wantDir || tl.Found != c.wantSrc {
					t.Errorf("%s: got %s (%s), want %s (%s)", c.name, tl.Dir, tl.Found, c.wantDir, c.wantSrc)
				}
			}
			o := fakeOpts(goos, map[string]string{}, []string{})
			o.Executable = func() (string, error) { return exe, nil }
			tl, err := Discover(o)
			if err != nil {
				t.Fatal(err)
			}
			if tl.LLD == "" || tl.LLDLink != "" || tl.LDLLD != "" || tl.LLVMLib != "" {
				t.Errorf("bundle tools: %+v", tl)
			}
			for _, triple := range []string{"x86_64-pc-windows-msvc", "x86_64-unknown-linux-gnu", "x86_64-unknown-none-elf"} {
				if miss := tl.Need(triple); len(miss) != 0 {
					t.Errorf("bundle Need(%s) = %v", triple, miss)
				}
			}
			if !strings.Contains(tl.Describe(), "(bundle)") {
				t.Errorf("Describe does not name the bundle rule:\n%s", tl.Describe())
			}
		})
	}
}

// TestIdentityNamesTheRule: the same directory chosen by two rules gives
// two identities, so a cache never mixes them.
func TestIdentityNamesTheRule(t *testing.T) {
	exe, bundle := bundleTree(t, "windows", NameClang, NameLLD)
	o := fakeOpts("windows", map[string]string{}, []string{})
	o.Executable = func() (string, error) { return exe, nil }
	a, err := Discover(o)
	if err != nil {
		t.Fatal(err)
	}
	o = fakeOpts("windows", map[string]string{"MK_LLVM": bundle}, []string{})
	b, err := Discover(o)
	if err != nil {
		t.Fatal(err)
	}
	if a.Dir != b.Dir || a.Found == b.Found {
		t.Fatalf("setup: %s %s / %s %s", a.Dir, a.Found, b.Dir, b.Found)
	}
	if a.Identity() == b.Identity() {
		t.Error("identity ignores the rule that chose the directory")
	}
}

// TestRealPathResolvesLinks: a symbolic link, and on Windows a junction,
// among the executable's components is resolved, so the bundle rule looks
// beside the real file.
func TestRealPathResolvesLinks(t *testing.T) {
	exe, _ := bundleTree(t, runtime.GOOS, NameClang, NameLLD)
	root := filepath.Dir(filepath.Dir(exe))
	want := RealPath(exe)
	t.Run("symlink", func(t *testing.T) {
		const privilegeNotHeld = syscall.Errno(1314) // Windows ERROR_PRIVILEGE_NOT_HELD
		link := filepath.Join(t.TempDir(), "current")
		if err := os.Symlink(root, link); err != nil {
			if runtime.GOOS == "windows" && errors.Is(err, privilegeNotHeld) {
				t.Skipf("symbolic link privilege unavailable: %v", err)
			}
			t.Fatalf("symlink: %v", err)
		}
		if got := RealPath(filepath.Join(link, "bin", filepath.Base(exe))); got != want {
			t.Errorf("through a symbolic link: %s, want %s", got, want)
		}
	})
	if runtime.GOOS != "windows" {
		return
	}
	t.Run("junction", func(t *testing.T) {
		junction := filepath.Join(t.TempDir(), "junction")
		if out, err := exec.Command("cmd", "/c", "mklink", "/J", junction, root).CombinedOutput(); err != nil {
			t.Fatalf("mklink /J: %v %s", err, out)
		}
		je := filepath.Join(junction, "bin", filepath.Base(exe))
		if got := RealPath(je); got != want {
			t.Errorf("through a junction: %s, want %s", got, want)
		}
		// The bundle rule through the junction finds the real llvm/bin.
		o := fakeOpts("windows", map[string]string{}, []string{})
		o.Executable = func() (string, error) { return RealPath(je), nil }
		tl, err := Discover(o)
		if err != nil {
			t.Fatal(err)
		}
		if tl.Found != BundleDir || !strings.HasPrefix(tl.Dir, RealPath(root)) {
			t.Errorf("through a junction: %s (%s)", tl.Dir, tl.Found)
		}
	})
}

// flavourTools is tl with only lld: the named linkers and llvm-lib
// removed, as in a bundle. It skips when tl has no lld.
func flavourTools(t *testing.T, tl *Tools) *Tools {
	t.Helper()
	if tl.LLD == "" {
		t.Skipf("SKIP: %s has no lld, so the flavour forms did not run", tl.Dir)
	}
	f := *tl
	f.LLDLink, f.LDLLD, f.LLVMLib = "", "", ""
	return &f
}

// TestLLDFlavoursMatchNamedTools: an executable linked through
// `lld -flavor link` (COFF) or `lld -flavor gnu` (ELF) is byte for byte the
// one lld-link or ld.lld writes, for each smoke variant (plain, O3, the
// ThinLTO route), and the flavour command lines carry -flavor.
func TestLLDFlavoursMatchNamedTools(t *testing.T) {
	tl := realTools(t)
	flav := flavourTools(t, tl)
	for _, c := range []struct{ triple, file string }{
		{"x86_64-pc-windows-msvc", "smoke_windows.ll"},
		{"x86_64-unknown-linux-gnu", "smoke_linux.ll"},
	} {
		if miss := tl.Need(c.triple); len(miss) > 0 {
			t.Logf("SKIP %s: %s lacks %v", c.triple, tl.Dir, miss)
			continue
		}
		for _, v := range smokeVariants {
			t.Run(c.triple+"/"+v.name, func(t *testing.T) {
				named := smokeBuild(t, tl, c.triple, c.file, v, t.TempDir())
				one := smokeBuild(t, flav, c.triple, c.file, v, t.TempDir())
				if !sameBytes(t, named, one) {
					t.Errorf("lld under its flavour wrote other bytes than the named linker")
				}
			})
		}
	}
	tool, path, pre := flav.linker(COFF)
	if tool != NameLLD || path != flav.LLD || !slices.Equal(pre, []string{"-flavor", "link"}) {
		t.Errorf("COFF linker: %s %s %v", tool, path, pre)
	}
	tool, _, pre = flav.linker(ELF)
	if tool != NameLLD || !slices.Equal(pre, []string{"-flavor", "gnu"}) {
		t.Errorf("ELF linker: %s %v", tool, pre)
	}
	if tool, _, pre = tl.linker(COFF); tool != NameLLDLink || pre != nil {
		t.Errorf("named COFF linker: %s %v", tool, pre)
	}
}

// TestLLDFlavourDebugAndStub: the PDB route links under the flavour (its
// PDB names the linker that ran, so it is not byte-identical), and the ELF
// stub library gives the same bytes under the flavour as under ld.lld.
func TestLLDFlavourDebugAndStub(t *testing.T) {
	tl := realTools(t)
	flav := flavourTools(t, tl)
	ctx := context.Background()
	compile := func(triple, file, dir string) string {
		facts, err := tl.Probe(ctx, triple)
		if err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(filepath.Join("testdata", file))
		if err != nil {
			t.Fatal(err)
		}
		ll := filepath.Join(dir, "x.ll")
		writeFile(t, ll, facts.Header()+string(body))
		obj := filepath.Join(dir, "x.o")
		if err := tl.Compile(ctx, CompileJob{Triple: triple, Input: ll, Output: obj, Opt: O0, CPU: "x86-64-v2"}); err != nil {
			t.Fatal(err)
		}
		return obj
	}
	if len(tl.Need("x86_64-pc-windows-msvc")) == 0 {
		var outs [2]string
		for i, tools := range []*Tools{tl, flav} {
			dir := t.TempDir()
			obj := compile("x86_64-pc-windows-msvc", "smoke_windows.ll", dir)
			def, err := os.ReadFile(filepath.Join("testdata", "kernel32.def"))
			if err != nil {
				t.Fatal(err)
			}
			names, err := ParseDefList(string(def))
			if err != nil {
				t.Fatal(err)
			}
			lib := filepath.Join(dir, "kernel32.lib")
			if err := tools.ImportLib(ctx, "kernel32.dll", names, "x64", lib); err != nil {
				t.Fatal(err)
			}
			outs[i] = filepath.Join(dir, "dbg.exe")
			if err := tools.Link(ctx, LinkJob{Format: COFF, Objects: []string{obj}, Libs: []string{lib}, Debug: true, Output: outs[i]}); err != nil {
				t.Fatal(err)
			}
		}
		// Not byte for byte: lld writes its own path and command line into
		// the PDB's linker record (S_ENVBLOCK), so the PDB, and the image
		// through the PDB's GUID, name lld-link.exe or lld.exe. The same
		// fact already makes a --debug image depend on where LLVM is
		// installed. The two PDBs and the two images differ in a few bytes;
		// the command lines are equal.
		pdb := func(p string) string { return strings.TrimSuffix(p, ".exe") + ".pdb" }
		for i, name := range []string{NameLLDLink, NameLLD} {
			b, err := os.ReadFile(pdb(outs[i]))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(b), string(filepath.Separator)+name+filepath.Ext(tl.Clang)+string(rune(0))) {
				t.Errorf("the PDB of %s does not name its linker", name)
			}
		}
	}
	if len(tl.Need("x86_64-unknown-linux-gnu")) == 0 {
		var outs [2]string
		for i, tools := range []*Tools{tl, flav} {
			dir := t.TempDir()
			obj := compile("x86_64-unknown-linux-gnu", "smoke_linux.ll", dir)
			outs[i] = filepath.Join(dir, "libstub.so")
			if err := tools.LinkStub(ctx, obj, "libstub.so.1", "", outs[i]); err != nil {
				t.Fatal(err)
			}
		}
		if !sameBytes(t, outs[0], outs[1]) {
			t.Error("ELF stub: the flavour wrote other bytes")
		}
	}
}

// TestImportLibByLinker: without llvm-lib, ImportLib runs `lld -flavor link
// /lib` (or `lld-link /lib`), and the import library is byte for byte
// llvm-lib's, for std's kernel32.def and for a package's list.
func TestImportLibByLinker(t *testing.T) {
	tl := realTools(t)
	if tl.LLVMLib == "" {
		t.Skipf("SKIP: %s has no llvm-lib to compare with", tl.Dir)
	}
	flav := flavourTools(t, tl)
	linkOnly := *tl
	linkOnly.LLVMLib = ""
	def, err := os.ReadFile(filepath.Join("..", "..", "std", "sys", "windows", "kernel32.def"))
	if err != nil {
		t.Fatal(err)
	}
	k32, err := ParseDefList(string(def))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, c := range []struct {
		dll   string
		names []string
	}{{"kernel32.dll", k32}, {"mylib.dll", []string{"my_add", "my_mul", "mk_hello"}}} {
		var outs []string
		for i, tools := range []*Tools{tl, flav, &linkOnly} {
			out := filepath.Join(t.TempDir(), fmt.Sprintf("%d.lib", i))
			if err := tools.ImportLib(ctx, c.dll, c.names, "x64", out); err != nil {
				t.Fatalf("%s tools %d: %v", c.dll, i, err)
			}
			outs = append(outs, out)
		}
		if !sameBytes(t, outs[0], outs[1]) {
			t.Errorf("%s: lld -flavor link /lib differs from llvm-lib", c.dll)
		}
		if !sameBytes(t, outs[0], outs[2]) {
			t.Errorf("%s: lld-link /lib differs from llvm-lib", c.dll)
		}
	}
	tool, _, args := flav.importLibTool([]string{"/def:x.def"})
	if tool != NameLLD || !slices.Equal(args, []string{"-flavor", "link", "/lib", "/def:x.def"}) {
		t.Errorf("import library command: %s %v", tool, args)
	}
	if tool, _, args = tl.importLibTool([]string{"/def:x.def"}); tool != NameLLVMLib || !slices.Equal(args, []string{"/def:x.def"}) {
		t.Errorf("llvm-lib command: %s %v", tool, args)
	}
}

// TestInstallAdvice: the advice names MinMajor and MaxTestedMajor (no
// literal range) and the bundle.
func TestInstallAdvice(t *testing.T) {
	want := fmt.Sprintf("install LLVM %d or newer (tested on %d) with clang and lld, and put its bin directory on PATH, "+
		"or install the Mk toolchain bundle, which carries its own LLVM", MinMajor, MaxTestedMajor)
	if InstallAdvice != want {
		t.Errorf("InstallAdvice = %q\nwant %q", InstallAdvice, want)
	}
	if strings.Contains(InstallAdvice, "18") {
		t.Errorf("the advice still names LLVM 18: %q", InstallAdvice)
	}
}

// TestDiscoverStrayBundle: the bundle rule applies only to an mk in a
// bundle's bin directory, with RELEASE.txt one level up and a complete
// llvm/bin (clang and lld or the named linker). Anything else falls through
// to the default locations, and a too-old clang in a directory that is not a
// bundle never stops discovery.
func TestDiscoverStrayBundle(t *testing.T) {
	type layout struct {
		exeDir  string   // the executable's directory under the root
		release bool     // RELEASE.txt in the root
		tools   []string // what llvm/bin holds
	}
	make := func(t *testing.T, l layout) (exe, llvmBin string) {
		root := t.TempDir()
		llvmBin = filepath.Join(root, "llvm", "bin")
		os.MkdirAll(llvmBin, 0o755)
		os.MkdirAll(filepath.Join(root, l.exeDir), 0o755)
		exe = filepath.Join(root, l.exeDir, "mk.exe")
		os.WriteFile(exe, []byte("mk"), 0o755)
		if l.release {
			os.WriteFile(filepath.Join(root, "RELEASE.txt"), []byte("mk 0.1.0\n"), 0o644)
		}
		for _, n := range l.tools {
			os.WriteFile(filepath.Join(llvmBin, n+".exe"), []byte("fake"), 0o755)
		}
		return exe, llvmBin
	}
	defD := fakeDir(t, "windows", allTools...)
	for _, c := range []struct {
		name   string
		l      layout
		bundle bool
	}{
		{"mk not in bin", layout{"tools", true, []string{NameClang, NameLLD}}, false},
		{"no RELEASE.txt", layout{"bin", false, []string{NameClang, NameLLD}}, false},
		{"clang alone", layout{"bin", true, []string{NameClang}}, false},
		{"clang alone, mk not in bin", layout{"tools", false, []string{NameClang}}, false},
		{"a bundle with lld", layout{"bin", true, []string{NameClang, NameLLD}}, true},
		{"a bundle with the named linker", layout{"bin", true, []string{NameClang, NameLLDLink}}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			exe, llvmBin := make(t, c.l)
			o := fakeOpts("windows", map[string]string{}, []string{defD})
			o.Executable = func() (string, error) { return exe, nil }
			// The stray clang is too old: only a real bundle may report it.
			o.VersionLine = func(c string) (string, error) {
				if filepath.Dir(c) == llvmBin {
					return "clang version 18.1.0\n", nil
				}
				return "clang version 21.1.8\n", nil
			}
			tl, err := Discover(o)
			if c.bundle {
				var ve *VersionError
				if !errors.As(err, &ve) || filepath.Dir(ve.Path) != llvmBin {
					t.Errorf("a bundle with an old clang: %v %v", tl, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("not a bundle, yet discovery failed: %v", err)
			}
			if tl.Dir != defD || tl.Found != DefaultDir {
				t.Errorf("chose %s (%s), want the default %s", tl.Dir, tl.Found, defD)
			}
		})
	}
}
