// Why: two repository rules need a test: PENDING is no escape once every
// stage is wired, and no C enters the compiler, the runtime or std.
// What: TestNoPendingOnceEveryStageIsWired, TestEnforcedWithThisBuild and
// TestNoCSource, with the cToolFiles allowlist.
// How: the C guard walks the source directories for C files and Go string
// literals that would run a tool on C, allowing only a package's c_sources
// and c_imports.

package corpus

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"mk/internal/driver"
)

// pendingOnceWired is TestNoPendingOnceEveryStageIsWired's check: once avail
// holds every stage, every pending.txt line and every PENDING unit of r,
// doc block or conformance unit, listed or not, is a problem.
// enforced is false before that.
func pendingOnceWired(a Allowlist, r *Result, avail driver.StageSet) (problems []string, enforced bool) {
	if avail&allStages() != allStages() {
		return nil, false
	}
	if len(a.Entries) != 0 {
		problems = append(problems, fmt.Sprintf("pending.txt has %d lines; it must be empty once every stage is wired", len(a.Entries)))
	}
	for _, u := range r.Units {
		if u.Status == StatusPending {
			problems = append(problems, fmt.Sprintf("%s PENDING %s with every stage wired", u.Unit.Key, u.Reason))
		}
	}
	return problems, true
}

// TestNoPendingOnceEveryStageIsWired runs pendingOnceWired over doc/'s
// blocks and the conformance units.
func TestNoPendingOnceEveryStageIsWired(t *testing.T) {
	r := fullRun(t)
	a, err := LoadPending()
	if err != nil {
		t.Fatal(err)
	}
	problems, enforced := pendingOnceWired(a, r, driver.Available())
	if !enforced {
		// Available() holds every stage, so the no-pending rule is
		// enforced. A build of mk that drops a stage again would turn
		// this check back into nothing.
		t.Fatalf("not every stage is wired (missing %v): %d pending (%d doc blocks, %d conformance units), %d listed in pending.txt; every stage must be wired",
			(allStages() &^ driver.Available()).Stages(), r.Summary.Pending, countPending(r, true), countPending(r, false), len(a.Entries))
	}
	for _, p := range problems {
		t.Error(p)
	}
}

// driver.Available() holds every stage the corpus knows, so
// pendingOnceWired enforces over this build of mk. Without fullRun, so a
// driver change that drops a stage fails here at once.
func TestEnforcedWithThisBuild(t *testing.T) {
	if missing := allStages() &^ driver.Available(); missing != 0 {
		t.Fatalf("driver.Available() lacks %v; F9 (TestNoPendingOnceEveryStageIsWired) would not enforce", missing.Stages())
	}
	u := &Unit{Key: "doc/x.md:1", Verdict: VerdictOK}
	r := &Result{Units: []UnitResult{{Unit: u, Judgement: Judgement{Status: StatusPending, Reason: "needs StageIface"}}}}
	p, enforced := pendingOnceWired(Allowlist{}, r, driver.Available())
	if !enforced || len(p) != 1 || !strings.Contains(p[0], u.Key+" PENDING needs StageIface") {
		t.Errorf("pendingOnceWired over driver.Available(): enforced %v, problems %q", enforced, p)
	}
}

// With every stage wired, a PENDING conformance unit fails the check even
// when pending.txt lists it, and so does the line.
func TestNoPendingOnceWiredCoversConformanceUnits(t *testing.T) {
	u := &Unit{Key: "tests/conformance/codes/E0903", Verdict: VerdictReject}
	r := &Result{Units: []UnitResult{{Unit: u, Judgement: Judgement{Status: StatusPending, Reason: "needs StageGen"}, Explained: "gen"}}}
	a, err := ParseAllowlist(u.Key + " gen\n")
	if err != nil {
		t.Fatal(err)
	}
	if p, enforced := pendingOnceWired(a, r, stagesNative); enforced || len(p) != 0 {
		t.Errorf("before every stage is wired: enforced %v, %v", enforced, p)
	}
	p, enforced := pendingOnceWired(a, r, allStages())
	if !enforced || len(p) != 2 || !strings.Contains(p[0], "pending.txt has 1 lines") || !strings.Contains(p[1], u.Key+" PENDING needs StageGen") {
		t.Errorf("every stage wired: enforced %v, problems %q", enforced, p)
	}
}

func countPending(r *Result, blocks bool) int {
	n := 0
	for _, u := range r.Units {
		if u.Status == StatusPending && (u.Unit.Block != nil) == blocks {
			n++
		}
	}
	return n
}

// cToolArg matches a Go string literal that would make a tool read C: -x c,
// -x c++, or a .c, .h, .cc or .cpp file name.
// sourceRoots are the top-level directories TestNoCSource walks: the
// repository's sources, its documents and its dogfood packages (whose own
// c_sources are allowed). Anything else at the root is scratch.
var sourceRoots = map[string]bool{"cmd": true, "internal": true, "std": true, "tests": true, "tools": true,
	"Dogfoods": true, "editors": true, "design": true, "plan": true, "presentation": true, "review": true, "status": true}

var cToolArg = regexp.MustCompile(`"-x",\s*"c(\+\+)?"|"-xc(\+\+)?"|"-x c(\+\+)?"|"[^"\s]*\.(c|h|cc|cpp)"`)

// lineHasCToolArg reports whether a non-comment Go source line matches cToolArg.
func lineHasCToolArg(line string) bool {
	if strings.HasPrefix(strings.TrimSpace(line), "//") {
		return false
	}
	return cToolArg.MatchString(line)
}

func TestNoCSource(t *testing.T) {
	for s, want := range map[string]bool{`args("-x", "c")`: true, `"-x", "c++"`: true, `"-xc"`: true, `"rt/start.c"`: true,
		`"x.h"`: true, `// "x.h"`: false, `    // __has_include("opt.h")`: false, `"-x", "ir"`: false, `"a.cache"`: false, `"lib.cpp2"`: false} {
		if lineHasCToolArg(s) != want {
			t.Errorf("lineHasCToolArg on %s = %v, want %v", s, !want, want)
		}
	}
	root := repoRoot(t)
	var cFiles, goFiles []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// The rule is about the repository's sources (the compiler, the
			// runtime, std, and the packages and tools beside them), so the
			// walk enters only the top-level directories that hold them, and
			// never an untracked scratch directory left at the root (probe/,
			// _to_delete/, ...).
			if filepath.Dir(p) == root && !sourceRoots[d.Name()] {
				return filepath.SkipDir
			}
			// internal/corpus/_tmp is this package's scratch directory (in
			// .gitignore, never committed): runs put TMP, GOTMPDIR and
			// sometimes GOCACHE there, and Go's own build cache holds .h
			// files (go_asm.h), which are not repository sources.
			if d.Name() == ".git" || d.Name() == ".mk-cache" || p == filepath.Join(root, "internal", "corpus", "_tmp") {
				return filepath.SkipDir
			}
			// The benchmark baselines under tests/bench and the rows under
			// tests/bench-lanes are real C, Rust, Zig and Go programs, built
			// only by tools/bench; the compiler, the runtime and std stay
			// free of C.
			if p == filepath.Join(root, "tests", "bench") || p == filepath.Join(root, "tests", "bench-lanes") {
				return filepath.SkipDir
			}
			return nil
		}
		switch strings.ToLower(filepath.Ext(p)) {
		case ".c", ".h", ".cc", ".cpp":
			cFiles = append(cFiles, p)
		case ".go":
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			if !strings.HasSuffix(rel, "_test.go") && (strings.HasPrefix(rel, "internal/") || strings.HasPrefix(rel, "cmd/")) {
				goFiles = append(goFiles, p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range cFiles {
		rel, _ := filepath.Rel(root, f)
		if ok, why := packageCSource(t, root, filepath.ToSlash(rel)); ok {
			t.Logf("C source %s: %s", rel, why)
			continue
		}
		t.Errorf("C source in the repository: %s", f)
	}
	used := map[string]bool{}
	for _, f := range goFiles {
		rel, _ := filepath.Rel(root, f)
		rel = filepath.ToSlash(rel)
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if !lineHasCToolArg(line) {
				continue
			}
			if _, ok := cToolFiles[rel]; ok {
				used[rel] = true
				continue
			}
			t.Errorf("%s:%d passes C to a tool: %s", f, i+1, strings.TrimSpace(line))
		}
	}
	for f, why := range cToolFiles {
		if !used[f] {
			t.Errorf("cToolFiles lists %s (%s), which no longer passes C to a tool: remove it", f, why)
		}
	}
	t.Logf("scanned %d non-test Go files under internal/ and cmd/", len(goFiles))
}

// cToolFiles are the non-test Go files that may run Clang on C, each for a
// user package's C as the language admits it: its own c_sources, linked
// against std.libc, and its c_imports, whose headers Clang reads to write
// Mk bindings. No C of the compiler, the runtime or std is among them. A listed
// file that stops matching cToolArg is an error, so the list cannot go stale.
var cToolFiles = map[string]string{
	"internal/cimport/cimport.go": "c_imports: Clang reads the translation unit of a package's C imports",
	"internal/cimport/msvch.go":   "synthesizes Microsoft CRT header shims in memory for Windows SDK C imports",
	"internal/cimport/sysroot.go": "stats _mingw.h, windows.h and winnt.h to classify a C import sysroot",
	"internal/driver/cimport.go":  "c_sources and c_imports of a user package",
	"internal/driver/libch.go":    "std.libc's C headers generated from its export extern signatures for c_sources",
	"internal/driver/libout.go":   "a library's generated C header for its C host; no C is compiled",
	// The system-libc mode: the link hint names the C
	// header (math.h) a libc symbol is declared in, to say which glibc
	// library supplies it; no C is compiled.
	"internal/driver/links_libc.go":   "system-libc mode's link hint names a symbol's C header",
	"internal/driver/sdkdir.go":       "stats um\\windows.h to locate the installed Windows SDK",
	"internal/driver/sysgen.go":       "checks for vulkan.h, X11 and Wayland headers when generating shipped system views",
	"internal/driver/sysgen_glibc.go": "reads GLIBC and LINUX_VERSION macros from sysroot features.h and version.h",
	"internal/driver/workdir.go":      "--cpu floor: clang -dM -E of an empty C file reads predefined macros and compiles nothing",
	"internal/sysview/table.go":       "system view catalog listing the SDK headers each shipped view binds",
	"internal/toolchain/cc.go":        "clang -c of a package's c_sources",
}

// cSourcePath matches one pkg.CSource entry of a manifest and captures its
// path.
var cSourcePath = regexp.MustCompile(`pkg\.CSource\(\s*path:\s*"([^"]+)"`)

// cImportHeaders matches the headers list of a pkg.CImport; quoted takes
// each string out of it.
var (
	cImportHeaders = regexp.MustCompile(`pkg\.CImport\([^)]*?headers:\s*\[([^\]]*)\]`)
	quoted         = regexp.MustCompile(`"([^"]+)"`)
)

// packageCSource reports whether rel, a C file outside tests/bench, is a
// package's own c_sources entry: a pkg.mk in one of its ancestor
// directories lists it, and it is not under std/, internal/ or cmd/, where
// the compiler, the runtime and std live.
func packageCSource(t *testing.T, root, rel string) (bool, string) {
	t.Helper()
	for _, p := range []string{"std/", "internal/", "cmd/"} {
		if strings.HasPrefix(rel, p) {
			return false, ""
		}
	}
	for dir := path.Dir(rel); dir != "." && dir != "/"; dir = path.Dir(dir) {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(dir), "pkg.mk"))
		if err != nil {
			continue
		}
		for _, m := range cSourcePath.FindAllStringSubmatch(string(b), -1) {
			if path.Join(dir, m[1]) == rel {
				return true, "c_sources of " + dir + "/pkg.mk"
			}
		}
		// A header the package's own c_imports name is the interface of
		// its own C, as Dogfoods/notes' csrc/normtext.h is.
		for _, list := range cImportHeaders.FindAllStringSubmatch(string(b), -1) {
			for _, h := range quoted.FindAllStringSubmatch(list[1], -1) {
				if path.Join(dir, h[1]) == rel {
					return true, "c_imports headers of " + dir + "/pkg.mk"
				}
			}
		}
	}
	return false, ""
}
