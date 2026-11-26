// Why: the tool runner's environment, quoting and errors decide what every
// LLVM invocation sees and what the driver reports.
// What: tests of scrubbedEnv, command-line quoting, tool errors, and the
// target probe's data layout (TestProbeDataLayout, TestParseProbe).
// How: runs the installed LLVM through realTools, which skips without one.

package toolchain

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// realTools discovers the installed LLVM, or skips with a message. On a
// machine with LLVM installed these tests must run; the gate fails if one
// skipped.
func realTools(t *testing.T) *Tools {
	t.Helper()
	tl, err := Discover(Options{})
	if err != nil {
		t.Skipf("SKIP: no usable LLVM, so this test needs clang and did not run: %v", err)
	}
	return tl
}

func TestScrubbedEnv(t *testing.T) {
	host := map[string]string{
		"SystemRoot": `C:\WINDOWS`, "SystemDrive": "C:", "TEMP": `C:\t`, "TMP": `C:\t`,
		"PATH": `C:\evil`, "LIB": `C:\sdk`, "INCLUDE": `C:\inc`, "MK_LLVM": `C:\llvm`,
		"TMPDIR": "/tmp/x", "HOME": "/home/u", "LANG": "de_DE.UTF-8",
	}
	get := func(k string) string { return host[k] }
	win := scrubbedEnv("windows", get)
	wantWin := []string{`SystemRoot=C:\WINDOWS`, "SystemDrive=C:", `TEMP=C:\t`, `TMP=C:\t`,
		"SOURCE_DATE_EPOCH=0", "LC_ALL=C", "ZERO_AR_DATE=1", "CLANG_NO_DEFAULT_CONFIG=1"}
	if !reflect.DeepEqual(win, wantWin) {
		t.Errorf("windows env = %q", win)
	}
	lin := scrubbedEnv("linux", get)
	wantLin := []string{"PATH=/usr/bin:/bin", "TMPDIR=/tmp/x", "SOURCE_DATE_EPOCH=0", "LC_ALL=C", "ZERO_AR_DATE=1", "CLANG_NO_DEFAULT_CONFIG=1"}
	if !reflect.DeepEqual(lin, wantLin) {
		t.Errorf("linux env = %q", lin)
	}
	// Absent host variables are left out, not set empty.
	if got := scrubbedEnv("windows", func(string) string { return "" }); len(got) != 4 {
		t.Errorf("empty host env = %q", got)
	}
}

func TestCommandLineQuoting(t *testing.T) {
	got := CommandLine(`C:\Program Files\LLVM\bin\clang.exe`,
		[]string{"--target=x86_64-pc-windows-msvc", "-x", "ir", `C:\a b\x.ll`, "", `say "hi"`, `back\slash`})
	want := `"C:\\Program Files\\LLVM\\bin\\clang.exe" --target=x86_64-pc-windows-msvc -x ir "C:\\a b\\x.ll" "" "say \"hi\"" back\slash`
	if got != want {
		t.Errorf("CommandLine =\n%s\nwant\n%s", got, want)
	}
}

func TestToolErrorOnMissingExecutable(t *testing.T) {
	dir := t.TempDir()
	tl := &Tools{Clang: filepath.Join(dir, "no-such-clang.exe")}
	err := tl.Compile(context.Background(), CompileJob{
		Triple: "x86_64-pc-windows-msvc", Input: filepath.Join(dir, "a.ll"), Output: filepath.Join(dir, "a.o"),
	})
	var te *ToolError
	if !errors.As(err, &te) || te.ExitCode != -1 || te.Tool != NameClang || te.Err == nil {
		t.Fatalf("err = %#v, want ToolError with exit -1", err)
	}
	if !strings.Contains(te.Command, "no-such-clang.exe") || !strings.Contains(te.Command, "-x ir") {
		t.Errorf("command = %q", te.Command)
	}
	// A tool Discover did not find is the same error, with no process.
	err = (&Tools{}).Link(context.Background(), LinkJob{Format: ELF, Output: filepath.Join(dir, "a"), Objects: []string{filepath.Join(dir, "a.o")}})
	if !errors.As(err, &te) || te.ExitCode != -1 || te.Tool != NameLDLLD || !errors.Is(err, errNoTool) {
		t.Errorf("absent ld.lld: %#v", err)
	}
}

// TestToolErrorCarriesStderr: a tool that runs and fails gives its exit
// code and stderr verbatim.
func TestToolErrorCarriesStderr(t *testing.T) {
	tl := realTools(t)
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.ll")
	writeFile(t, bad, "this is not IR\n")
	err := tl.Compile(context.Background(), CompileJob{Triple: "x86_64-pc-windows-msvc", Input: bad, Output: filepath.Join(dir, "bad.o")})
	var te *ToolError
	if !errors.As(err, &te) || te.ExitCode <= 0 || !strings.Contains(te.Stderr, "bad.ll") {
		t.Fatalf("err = %#v", err)
	}
}

func TestProbeDataLayout(t *testing.T) {
	tl := realTools(t)
	for _, triple := range []string{"x86_64-pc-windows-msvc", "x86_64-unknown-linux-gnu"} {
		f, err := tl.Probe(context.Background(), triple)
		if err != nil {
			t.Fatalf("%s: %v", triple, err)
		}
		if !strings.HasPrefix(f.DataLayout, "e-") || !strings.HasPrefix(f.Triple, "x86_64-") || OSOf(f.Triple) != OSOf(triple) {
			t.Errorf("%s: %+v", triple, f)
		}
		t.Logf("%s (%s): triple %q datalayout %q", triple, tl.Version.Line, f.Triple, f.DataLayout)
	}
}

func TestParseProbe(t *testing.T) {
	out := "; ModuleID = 'x'\nsource_filename = \"x\"\ntarget datalayout = \"e-m:e-S128\"\ntarget triple = \"x86_64-unknown-linux-gnu\"\n"
	f, err := parseProbe(out)
	if err != nil || f != (TargetFacts{Triple: "x86_64-unknown-linux-gnu", DataLayout: "e-m:e-S128"}) {
		t.Errorf("parseProbe = %+v, %v", f, err)
	}
	if f.Header() != "target datalayout = \"e-m:e-S128\"\ntarget triple = \"x86_64-unknown-linux-gnu\"\n" {
		t.Errorf("Header = %q", f.Header())
	}
	if _, err := parseProbe("; nothing\n"); err == nil {
		t.Error("empty output accepted")
	}
}
