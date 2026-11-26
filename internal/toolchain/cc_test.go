// Why: C sources of a package are compiled by clang with Mk's flags, and must
// reach no C runtime and no host header.
// What: TestCArgs and TestCArgs_ShimStd, the C and assembly job command lines.
// How: builds CJobs and checks the flags and their order.

package toolchain

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestCArgs: a C job's command line reaches no C runtime and no host
// header (-nostdinc, -ffreestanding, no stack probe on Windows), and puts
// the manifest's flags last; an assembly job gets no C flags.
func TestCArgs(t *testing.T) {
	abs := func(p string) string { a, _ := filepath.Abs(p); return a }
	j := CJob{Triple: "x86_64-pc-windows-msvc", Input: abs("a.c"), Output: abs("a.o"), ResourceDir: abs("rd"),
		Include: []string{abs("pkg")}, Flags: []string{"-DX=1"}}
	args, err := CArgs(j, false)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Join(args, " ")
	for _, w := range []string{"-ffreestanding", "-fno-builtin", "-nostdinc", "-mno-stack-arg-probe", "-I " + abs("pkg"), "-c " + abs("a.c")} {
		if !strings.Contains(line, w) {
			t.Errorf("C args lack %q: %s", w, line)
		}
	}
	if args[len(args)-1] != "-DX=1" {
		t.Errorf("flags not last: %s", line)
	}
	j.Triple, j.Input = "x86_64-unknown-linux-gnu", abs("a.S")
	args, _ = CArgs(j, true)
	line = strings.Join(args, " ")
	if !strings.Contains(line, "-x assembler-with-cpp") || strings.Contains(line, "-ffreestanding") || strings.Contains(line, "stack-arg-probe") {
		t.Errorf("asm args: %s", line)
	}
	if _, err := CArgs(CJob{Triple: "x", Input: "rel.c", Output: abs("o")}, false); err == nil {
		t.Error("a relative input was accepted")
	}
}

func TestWindowsClangTarget(t *testing.T) {
	const triple = "x86_64-pc-windows-msvc"
	const want = "--target=x86_64-pc-windows-msvc19.33.0"
	dir := t.TempDir()
	ll := filepath.Join(dir, "input.ll")
	out := filepath.Join(dir, "output.o")
	for _, tc := range []struct {
		name string
		args func() ([]string, error)
	}{
		{"source", func() ([]string, error) {
			return CArgs(CJob{Triple: triple, Input: filepath.Join(dir, "input.c"), Output: out}, false)
		}},
		{"shim", func() ([]string, error) {
			return CArgs(CJob{Triple: triple, Input: filepath.Join(dir, "shim.c"), Output: out, Hosted: true}, false)
		}},
		{"assembly", func() ([]string, error) {
			return CArgs(CJob{Triple: triple, Input: filepath.Join(dir, "input.S"), Output: out}, true)
		}},
		{"IR", func() ([]string, error) {
			return compileArgs(CompileJob{Triple: triple, Input: ll, Output: out})
		}},
		{"probe", func() ([]string, error) { return probeArgs(triple, ll), nil }},
		{"CPU probe", func() ([]string, error) { return nativeArgs(triple, ll, out), nil }},
		{"ThinLTO", func() ([]string, error) { return thinBackendArgs(ll, ll+".thinlto.bc", out, Tuning{}), nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args, err := tc.args()
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(args, want) || slices.Contains(args, "--target="+triple) {
				t.Errorf("args = %v, want %s", args, want)
			}
		})
	}
}

// TestCArgs_ShimStd: every C job reads C as the C import does
// (-std=gnu11, so no __STRICT_ANSI__); the shim (Hosted) also drops
// -ffreestanding, so __STDC_HOSTED__ is the translation's; a DepFile adds
// -MD.
func TestCArgs_ShimStd(t *testing.T) {
	abs := func(p string) string { a, _ := filepath.Abs(p); return a }
	j := CJob{Triple: "x86_64-pc-windows-msvc", Input: abs("a.c"), Output: abs("a.o")}
	for _, hosted := range []bool{false, true} {
		j.Hosted = hosted
		args, err := CArgs(j, false)
		if err != nil {
			t.Fatal(err)
		}
		line := strings.Join(args, " ")
		if !strings.Contains(line, "-std=gnu11") || strings.Contains(line, "-std=c11") {
			t.Errorf("hosted %v: %s", hosted, line)
		}
		if strings.Contains(line, "-ffreestanding") == hosted {
			t.Errorf("hosted %v: -ffreestanding wrong: %s", hosted, line)
		}
		if strings.Contains(line, "-MD") {
			t.Errorf("no DepFile, yet -MD: %s", line)
		}
	}
	j.DepFile = abs("a.d")
	args, _ := CArgs(j, false)
	if line := strings.Join(args, " "); !strings.Contains(line, "-MD -MF "+abs("a.d")) {
		t.Errorf("DepFile: %s", line)
	}
}
