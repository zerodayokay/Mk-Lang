// Why: a ratio between languages means something only when every row builds
// each language the same way, for the same CPU target, and the table states
// what each column checks and how it links.
//
// What: columns, the column table with each column's facts (bounds,
// overflow, panic, LTO, linker, CRT); column.class, a column's safety class
// in a row; findToolchain; and toolchain.command, the build of one column of
// one row.
//
// How: the flags are fixed per column: C through lld as a single TU with
// its CRT linkage stated and no FP contraction, Rust and C static, the Go
// toolchain pinned, Zig's caches and target explicit.

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// column is one entry of a row: a language with one fixed set of flags.
type column struct {
	ID   string // "mk", "mk-wrap", "c", "rust", "rust-ovf", "zig-fast", "zig-safe", "go"
	Lang string // the key of bench.json "sources"
	// The facts printed in the table header.
	Bounds   string // "checked" or "none"
	Overflow string // "trap", "wrap" or "UB" (for a row whose overflow class is "never")
	Panic    string
	LTO      string
	Linker   string
	CRT      string
	StdV2    string // whether the language's precompiled std is built for x86-64-v2
	// Budget marks a column of the check-budget table (budgets.go): built
	// only when a run names it, never part of a cross-language table.
	Budget bool `json:"budget,omitempty"`
	// PGO marks the PGO table's columns (pgo.go): never in columns, built
	// only by buildPGO.
	PGO bool `json:"pgo,omitempty"`
}

// columns in table order. The Mk wrapping column exists only for rows that
// give a "mk-wrap" source.
var columns = []column{
	{ID: "mk", Lang: "mk", Bounds: "checked", Overflow: "trap", Panic: "abort (exit 101)", LTO: "ThinLTO", Linker: "lld-link", CRT: "none (Mk runtime, kernel32 only)", StdV2: "yes (built with the program)"},
	{ID: "mk-wrap", Lang: "mk-wrap", Bounds: "checked", Overflow: "wrap (`+%` operators)", Panic: "abort (exit 101)", LTO: "ThinLTO", Linker: "lld-link", CRT: "none (Mk runtime, kernel32 only)", StdV2: "yes (built with the program)"},
	// Mk's keyed map (SipHash-1-3 under a per-map key, Map.NewKeyed), the
	// peer of Rust's RandomState, beside the default fast fixed hash of the
	// mk column. Only rows with a "mk-keyed" source have it.
	{ID: "mk-keyed", Lang: "mk-keyed", Bounds: "checked", Overflow: "trap", Panic: "abort (exit 101)", LTO: "ThinLTO", Linker: "lld-link", CRT: "none (Mk runtime, kernel32 only)", StdV2: "yes (built with the program)"},
	// The closuresort row (the cost of capturing literals): the same sort and
	// comparator as the mk column, with its context passed as an explicit
	// argument instead of captured. Only rows with a "mk-ctx" source have it.
	{ID: "mk-ctx", Lang: "mk-ctx", Bounds: "checked", Overflow: "trap", Panic: "abort (exit 101)", LTO: "ThinLTO", Linker: "lld-link", CRT: "none (Mk runtime, kernel32 only)", StdV2: "yes (built with the program)"},
	{ID: "c", Lang: "c", Bounds: "none", Overflow: "UB (signed), wrap (unsigned)", Panic: "none", LTO: "none (single TU)", Linker: "lld-link", CRT: "static UCRT (-fms-runtime-lib=static)", StdV2: "no (prebuilt UCRT)"},
	{ID: "rust", Lang: "rust", Bounds: "checked", Overflow: "wrap (overflow-checks off at opt-level=3)", Panic: "abort", LTO: "fat", Linker: "link.exe (rustc default)", CRT: "static (+crt-static)", StdV2: "no (prebuilt std at x86-64)"},
	{ID: "rust-ovf", Lang: "rust", Bounds: "checked", Overflow: "trap (-C overflow-checks=on)", Panic: "abort", LTO: "fat", Linker: "link.exe (rustc default)", CRT: "static (+crt-static)", StdV2: "no (prebuilt std at x86-64)"},
	{ID: "zig-fast", Lang: "zig", Bounds: "none", Overflow: "UB (ReleaseFast)", Panic: "none", LTO: "none (one compilation unit)", Linker: "zig (lld)", CRT: "none (x86_64-windows-gnu, no -lc)", StdV2: "yes (std compiled with the program)"},
	{ID: "zig-safe", Lang: "zig", Bounds: "checked", Overflow: "trap (ReleaseSafe)", Panic: "abort with trace", LTO: "none (one compilation unit)", Linker: "zig (lld)", CRT: "none (x86_64-windows-gnu, no -lc)", StdV2: "yes (std compiled with the program)"},
	{ID: "go", Lang: "go", Bounds: "checked", Overflow: "wrap", Panic: "panic (exit 2)", LTO: "n/a (gc whole program)", Linker: "go", CRT: "none (Go runtime)", StdV2: "yes (GOAMD64=v2 rebuilds std)"},
	{ID: "cs", Lang: "cs", Bounds: "checked", Overflow: "wrap", Panic: "exception (exit nonzero)", LTO: "n/a (RyuJIT, TieredCompilation + TieredPGO)", Linker: "dotnet", CRT: "none (.NET 11 runtime, ServerGC=false, InvariantGlobalization=true; JIT start-up included)", StdV2: "yes (RyuJIT pinned via DOTNET_EnableAVX=0/BMI1/BMI2/LZCNT/AVX512=0)"},
	{ID: "cs-aot", Lang: "cs", Bounds: "checked", Overflow: "wrap", Panic: "exception (exit nonzero)", LTO: "whole-program ILC (OptimizationPreference=Speed)", Linker: "link.exe (NativeAOT)", CRT: "static NativeAOT runtime (ServerGC=false, InvariantGlobalization=true; no JIT start-up)", StdV2: "yes (IlcInstructionSet=x86-64-v2)"},
	// The check-budget columns (bounds checks and overflow checks,
	// budgets.go): the row's mk source built with mk build's hidden,
	// benchmark-only flags. Each removes one kind of check from the whole
	// program, std included.
	{ID: "mk-nobounds", Lang: "mk", Bounds: "none (`--no-bounds-checks`)", Overflow: "trap", Panic: "abort (exit 101)", LTO: "ThinLTO", Linker: "lld-link", CRT: "none (Mk runtime, kernel32 only)", StdV2: "yes (built with the program)", Budget: true},
	{ID: "mk-noovf", Lang: "mk", Bounds: "checked", Overflow: "wrap (`--no-overflow-checks`, std included)", Panic: "abort (exit 101)", LTO: "ThinLTO", Linker: "lld-link", CRT: "none (Mk runtime, kernel32 only)", StdV2: "yes (built with the program)", Budget: true},
}

// budgetFlags is the mk build flag of each check-budget column.
var budgetFlags = map[string]string{"mk-nobounds": "--no-bounds-checks", "mk-noovf": "--no-overflow-checks"}

// class is the safety class of a column in a row: "checked" (bounds checked
// and overflow trapped), "bounds" (bounds checked, overflow wraps) or
// "unchecked". In a row whose overflow class is "wraps" every column wraps
// on purpose, so a checking column counts as "bounds" there.
func (c column) class(r *row) string {
	if strings.HasPrefix(c.Bounds, "none") {
		return "unchecked"
	}
	if strings.HasPrefix(c.Overflow, "trap") && r.Overflow != "wraps" {
		return "checked"
	}
	return "bounds"
}

// rowColumns is the columns a row has in the cross-language tables: every
// column whose language has a source, in table order, without the
// check-budget columns.
func rowColumns(r *row) []column {
	var out []column
	for _, c := range allRowColumns(r) {
		if !c.Budget {
			out = append(out, c)
		}
	}
	return out
}

// allRowColumns is every column whose language has a source in the row, the
// check-budget columns included.
func allRowColumns(r *row) []column {
	var out []column
	for _, c := range columns {
		if _, ok := r.Sources[c.Lang]; ok {
			out = append(out, c)
		}
	}
	return out
}

// columnByID finds a column.
func columnByID(id string) (column, bool) {
	for _, c := range columns {
		if c.ID == id {
			return c, true
		}
	}
	return column{}, false
}

// toolchain holds the paths and the environment of every compiler. It is
// filled by findToolchain; a missing compiler leaves its field "" and its
// columns fail to build with that reason.
type toolchain struct {
	Mk        string // the mk executable being measured
	Clang     string
	Rustc     string
	Zig       string
	Go        string
	Dotnet    string
	Hyperfine string
	Cargo     string   // the compile-speed table's Rust builds (compile.go)
	Work      string   // the work directory: builds, caches, raw results
	CEnv      []string // INCLUDE and LIB for clang's MSVC target
	// MkTarget, when set, cross-builds the Mk columns for that target
	// (x86_64-linux for the Linux table, built on Windows).
	MkTarget string
	// MkPrebuilt, when set, is a directory of Mk executables cross-built
	// elsewhere (bin/<tag>/<row>/<column>.exe layout of exeName); the Mk
	// columns are copied from it instead of built (Linux, inside WSL).
	MkPrebuilt string
	// MkStd is the MK_STD every Mk build gets ("" for the std embedded in
	// the mk executable). It is always set in the build's environment, so
	// a value inherited from the caller cannot change a build unseen, and
	// results.json records it with the command.
	MkStd string
	// GOOS is the OS whose command lines command writes ("" is the OS
	// tools/bench runs on). Only tests set it, so the Linux C line of a
	// vendored row is checked on Windows too.
	GOOS string
	// versions memoizes version (facts.go), by language and path.
	versions map[string]string
}

// goos is the OS of t's command lines.
func (t *toolchain) goos() string {
	if t.GOOS != "" {
		return t.GOOS
	}
	return runtime.GOOS
}

// The MSVC toolset and Windows SDK, which clang does not find alone on the
// benchmark machine. msvcPinned is the benchmark machine's toolset, taken
// when vswhere finds none (msvcToolset).
const (
	msvcPinned = `C:\Program Files\Microsoft Visual Studio\18\Community\VC\Tools\MSVC\14.51.36231`
	sdkDir     = `C:\Program Files (x86)\Windows Kits\10`
	sdkVersion = "10.0.26100.0"
	llvmBin    = `C:\Program Files\LLVM\bin`
)

// msvcToolset is the MSVC toolset directory: the default toolset of the
// newest Visual Studio with the x64 C++ tools, as vswhere reports it, or
// msvcPinned when vswhere, that Visual Studio or its toolset is missing.
func msvcToolset() string {
	vswhere := filepath.Join(os.Getenv("ProgramFiles(x86)"), "Microsoft Visual Studio", "Installer", "vswhere.exe")
	out, err := exec.Command(vswhere, "-latest", "-products", "*",
		"-requires", "Microsoft.VisualStudio.Component.VC.Tools.x86.x64", "-property", "installationPath").Output()
	if err != nil {
		return msvcPinned
	}
	inst := strings.TrimSpace(string(out))
	if inst == "" {
		return msvcPinned
	}
	v, err := os.ReadFile(filepath.Join(inst, "VC", "Auxiliary", "Build", "Microsoft.VCToolsVersion.default.txt"))
	if err != nil {
		return msvcPinned
	}
	dir := filepath.Join(inst, "VC", "Tools", "MSVC", strings.TrimSpace(string(v)))
	if _, err := os.Stat(filepath.Join(dir, "include")); err != nil {
		return msvcPinned
	}
	return dir
}

// mkStd is the -mk-std flag: the MK_STD of every Mk build.
var mkStd string

func findToolchain(work, mk string) (*toolchain, error) {
	t := &toolchain{Mk: mk, Work: work, MkStd: mkStd}
	look := func(name string, fixed ...string) string {
		for _, f := range fixed {
			if _, err := os.Stat(f); err == nil {
				return f
			}
		}
		if p, err := lookPath(name); err == nil {
			return p
		}
		return ""
	}
	exe := ""
	if runtime.GOOS == "windows" {
		exe = ".exe"
	}
	if runtime.GOOS == "linux" {
		// clang 21 from apt.llvm.org, installed as clang-21.
		t.Clang = look("clang-21", "/usr/bin/clang-21", "/usr/lib/llvm-21/bin/clang")
	} else {
		t.Clang = look("clang", filepath.Join(llvmBin, "clang"+exe))
	}
	t.Rustc = look("rustc")
	t.Zig = look("zig")
	t.Go = look("go", filepath.Join(runtime.GOROOT(), "bin", "go"+exe))
	t.Dotnet = look("dotnet", `C:\Program Files\dotnet\dotnet`+exe)
	t.Hyperfine = look("hyperfine")
	t.Cargo = look("cargo")
	if runtime.GOOS == "windows" {
		msvcDir := msvcToolset()
		inc := []string{filepath.Join(msvcDir, "include")}
		for _, d := range []string{"ucrt", "um", "shared"} {
			inc = append(inc, filepath.Join(sdkDir, "Include", sdkVersion, d))
		}
		lib := []string{filepath.Join(msvcDir, "lib", "x64")}
		for _, d := range []string{"ucrt", "um"} {
			lib = append(lib, filepath.Join(sdkDir, "Lib", sdkVersion, d, "x64"))
		}
		t.CEnv = []string{"INCLUDE=" + strings.Join(inc, ";"), "LIB=" + strings.Join(lib, ";")}
	}
	return t, nil
}

// buildCmd is one compiler invocation: the program, its arguments, the
// directory it runs in and the variables it adds to the environment.
type buildCmd struct {
	Dir  string   `json:"dir"`
	Env  []string `json:"env"`
	Args []string `json:"args"` // Args[0] is the program
	// Deps is the version text of the toolchain the build runs (version in
	// facts.go), which the build stamp hashes with the command.
	Deps string `json:"-"`
	// Product, when set, is the file the command writes, copied to the
	// column's executable after it succeeds (cargo has no -o; cargo.go).
	Product string `json:"product,omitempty"`
	// Unset is removed from the inherited environment before Env is added
	// (variables a caller could change the build through).
	Unset []string `json:"unset,omitempty"`
	// Inputs are files outside the sources the build reads, hashed into
	// its stamp: the PGO table's profile (pgo.go).
	Inputs []string `json:"inputs,omitempty"`
	// Sidecars are sibling files of Product copied beside the column's
	// executable after a build succeeds (cs.dll, cs.runtimeconfig.json).
	Sidecars []string `json:"-"`
	// Prep runs right before invoking the compiler (e.g. writing the
	// generated .csproj and offline NuGet.Config for C# builds).
	Prep func() error `json:"-"`
}

func (b buildCmd) String() string {
	var s []string
	s = append(s, b.Env...)
	for _, a := range b.Args {
		if strings.ContainsAny(a, " \t") {
			a = `"` + a + `"`
		}
		s = append(s, a)
	}
	return strings.Join(s, " ")
}

// command is the build of column c of row r into out. The flags are the
// benchmark contract's: C through lld, single TU; CRT linkage stated, static
// for C and Rust; no FP contraction in C; the Go toolchain pinned, Zig
// caches and target explicit.
func (t *toolchain) command(r *row, c column, out string) (buildCmd, error) {
	src, ok := r.Sources[c.Lang]
	if !ok {
		return buildCmd{}, fmt.Errorf("row %s has no %s source", r.id(), c.Lang)
	}
	need := func(path, what string) error {
		if path == "" {
			return fmt.Errorf("%s not found", what)
		}
		return nil
	}
	cpu := cpus[r.cpu()]
	switch c.Lang {
	case "mk", "mk-wrap", "mk-keyed", "mk-ctx":
		if t.MkPrebuilt != "" {
			name := strings.ReplaceAll(r.id(), "/", "_")
			return buildCmd{Dir: r.Dir, Args: []string{"cp", filepath.Join(t.MkPrebuilt, name, c.ID+".exe"), out}}, nil
		}
		if err := need(t.Mk, "mk"); err != nil {
			return buildCmd{}, err
		}
		args := []string{t.Mk, "build", "--release", t.pgoFlag(r, c), "--cpu", cpu.Mk, "--no-cache"}
		if r.Package != nil && r.Package.Bin != "" {
			args = append(args, "--bin", r.Package.Bin)
		}
		if f := budgetFlags[c.ID]; f != "" {
			args = append(args, f)
		}
		if f := fpFlags[c.ID]; f != "" {
			args = append(args, f) // the frame-pointer columns (framepointers.go)
		}
		if t.MkTarget != "" {
			args = append(args, "--target", t.MkTarget)
		}
		pkgDir := filepath.Join(r.Dir, src)
		if vendorFor(r, c.Lang) {
			// The vendored C files join a build copy of the package
			// (vendor.go); the tree never holds them.
			d, err := vendorCopy(t.Work, r, c.Lang)
			if err != nil {
				return buildCmd{}, err
			}
			pkgDir = d
		}
		var inputs []string
		if c.ID == pgoColumn.ID {
			// The trained profile is the build's input: a new profile is
			// a changed build (pgo.go).
			inputs = []string{pgoProfile(t.Work, r)}
		}
		return buildCmd{Dir: r.Dir, Env: []string{"MK_CACHE=" + filepath.Join(t.Work, "mkcache"), "MK_STD=" + t.MkStd},
			Args: append(args, "-o", out, pkgDir), Deps: t.version("mk", t.Mk), Inputs: inputs}, nil
	case "c":
		if err := need(t.Clang, "clang"); err != nil {
			return buildCmd{}, err
		}
		if t.goos() == "linux" {
			// The Linux table (README "Linux"): glibc, linked statically by lld;
			// -lm after the sources, since static glibc keeps sqrt in libm. A
			// vendored row's files and flags join the line as on Windows.
			args := append([]string{t.Clang, "-std=c17",
				"-O3", "-march=" + cpu.C, "-ffp-contract=off", "-fuse-ld=lld", "-static",
				"-o", out, filepath.Join(r.Dir, src)}, vendorCArgs(r)...)
			return buildCmd{Dir: r.Dir, Args: append(args, "-lm"), Deps: t.version("c", t.Clang)}, nil
		}
		return buildCmd{Dir: r.Dir, Env: t.CEnv, Args: append([]string{t.Clang, "--target=x86_64-pc-windows-msvc", "-std=c17",
			"-O3", "-march=" + cpu.C, "-ffp-contract=off", "-fms-runtime-lib=static", "-fuse-ld=lld",
			"-o", out, filepath.Join(r.Dir, src)}, vendorCArgs(r)...), Deps: t.version("c", t.Clang)}, nil
	case "rust":
		if err := need(t.Rustc, "rustc"); err != nil {
			return buildCmd{}, err
		}
		if fi, err := os.Stat(filepath.Join(r.Dir, src)); err == nil && fi.IsDir() {
			return t.cargoCommand(r, c, filepath.Join(r.Dir, src)) // an app row's crate (cargo.go)
		}
		args := []string{t.Rustc, "--edition", "2024", "--target", "x86_64-pc-windows-msvc",
			"-C", "opt-level=3", "-C", "target-cpu=" + cpu.Rust, "-C", "codegen-units=1", "-C", "lto=fat",
			"-C", "panic=abort", "-C", "target-feature=+crt-static"}
		if c.ID == "rust-ovf" {
			args = append(args, "-C", "overflow-checks=on")
		}
		args = append(args, "-o", out, filepath.Join(r.Dir, src))
		return buildCmd{Dir: filepath.Dir(out), Args: args, Deps: t.version("rust", t.Rustc)}, nil
	case "zig":
		if err := need(t.Zig, "zig"); err != nil {
			return buildCmd{}, err
		}
		mode := "ReleaseFast"
		if c.ID == "zig-safe" {
			mode = "ReleaseSafe"
		}
		return buildCmd{Dir: filepath.Dir(out), Args: []string{t.Zig, "build-exe", "-O", mode,
			"-target", "x86_64-windows-gnu", "-mcpu=" + cpu.Zig,
			"--cache-dir", filepath.Join(t.Work, "zig-cache"), "--global-cache-dir", filepath.Join(t.Work, "zig-global"),
			"-femit-bin=" + out, filepath.Join(r.Dir, src)}, Deps: t.version("zig", t.Zig)}, nil
	case "go":
		if err := need(t.Go, "go"); err != nil {
			return buildCmd{}, err
		}
		return buildCmd{Dir: r.Dir, Env: []string{"GOFLAGS=", "GOTOOLCHAIN=local", "GOAMD64=" + cpu.GoAmd64, "CGO_ENABLED=0", "GOOS=windows", "GOARCH=amd64"},
			Args: []string{t.Go, "build", "-pgo=off", "-trimpath", "-o", out, filepath.Join(r.Dir, src)}, Deps: t.version("go", t.Go)}, nil
	case "cs":
		return t.csCommand(r, c, out)
	}
	return buildCmd{}, fmt.Errorf("no build for language %q", c.Lang)
}
