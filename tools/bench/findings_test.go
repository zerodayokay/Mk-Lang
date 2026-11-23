// Why: the same-algorithm rows are fair only while their sources keep the
// rules of tests/bench/README.md: no hints, no hand tuning, matching sizes.
// What: the ban list sameBans with contractViolations; tests of the bans,
// of README sizes against bench.json, of counts agreement and of the budget.
// How: strips comments, removes the one allowed C pragma, then matches each
// ban's regexp on the code; the verdict tests run a copy of the test binary.

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// sameBan is one form the same-algorithm table forbids (the rules: "no
// intrinsics, no hand vectorisation, no unsafe beyond what the algorithm
// needs, no -ffast-math, and no per-language hand tuning"). Lang ""
// applies to every language.
type sameBan struct {
	lang string
	re   *regexp.Regexp
	what string
}

func ban(lang, re, what string) sameBan { return sameBan{lang, regexp.MustCompile(re), what} }

var sameBans = []sameBan{
	// Every language.
	ban("", `get_unchecked`, "unchecked indexing (get_unchecked)"),
	ban("", `-ffast-math`, "-ffast-math"),
	ban("", `intrin\.h`, "an intrinsics header"),
	ban("", `\b(std|core)::arch\b`, "std::arch intrinsics"),
	ban("", `@Vector\(`, "hand vectorisation (@Vector)"),
	ban("", `@setRuntimeSafety\(`, "@setRuntimeSafety"),
	ban("", `@setFloatMode\(`, "@setFloatMode (Zig's fast-math)"),
	// C: aliasing, branch, loop and inlining hints.
	ban("c", `\brestrict\b|__restrict`, "restrict"),
	// Every #pragma, #pragma comment(linker, ...) included (it sets the
	// stack, sections or folding of one column's link). The one let
	// through, #pragma comment(lib, "..."), is removed before the bans run
	// (cPragmaLib): it names an import library (ws2_32, synchronization),
	// which tunes nothing.
	ban("c", `#\s*pragma`, "#pragma"),
	ban("c", `__builtin_`, "a __builtin_ hint"),
	ban("c", `__attribute__|__declspec`, "an attribute"),
	ban("c", `\binline\b|__forceinline`, "an inlining hint"),
	// Rust: unsafe and hints.
	ban("rust", `\bunsafe\b`, "unsafe"),
	ban("rust", `unreachable_unchecked|assert_unchecked|std::hint::`, "a std::hint"),
	ban("rust", `target_feature`, "target_feature"),
	ban("rust", `#\[(inline|cold)\b`, "an inlining hint"),
	// Zig: hints.
	ban("zig", `@branchHint\(`, "@branchHint"),
	ban("zig", `@prefetch\(`, "@prefetch"),
	ban("zig", `\binline\s+(fn|for|while)\b|\.always_inline`, "forced inlining or unrolling"),
	// Go.
	ban("go", `"unsafe"`, `import "unsafe"`),
	// C#: unsafe, stackalloc (same table), hints, intrinsics and hand vectorisation.
	ban("cs", `\bunsafe\b`, "unsafe"),
	ban("cs", `\bstackalloc\b`, "stackalloc"),
	ban("cs", `MethodImpl|AggressiveInlining|AggressiveOptimization`, "a [MethodImpl] hint"),
	ban("cs", `System\.Runtime\.Intrinsics`, "System.Runtime.Intrinsics"),
	ban("cs", `\bUnsafe\s*\.|\bSystem\.Runtime\.CompilerServices\.Unsafe\b`, "Unsafe.*"),
	ban("cs", `\bMemoryMarshal\s*\.\s*Get(?:ArrayData)?Reference\b`, "MemoryMarshal reference bypass"),
	ban("cs", `System\.Numerics\.Vector\b|\bVector\s*<`, "System.Numerics.Vector (hand vectorisation)"),
	// Mk.
	ban("mk", `\bstd\.simd\b`, "std.simd (hand vectorisation)"),
	ban("mk", `unsafe_modules:\s*\[\s*[^\]\s]`, "an unsafe module"),
}

// cPragmaLib is a line holding #pragma comment(lib, "...") and nothing
// else: the only pragma a C source may hold. contractViolations
// removes it from the code before the bans run, so a pragma that differs
// from it in any way (comment(linker, ...), comment(user, ...), pack, once)
// is still "#pragma".
var cPragmaLib = regexp.MustCompile(`(?m)^[ \t]*#[ \t]*pragma[ \t]+comment[ \t]*\([ \t]*lib[ \t]*,[ \t]*"[^"\n]*"[ \t]*\)[ \t\r]*$`)

// goDirective finds a //go: compiler directive (noinline, nosplit, ...),
// which lives in a comment and so is checked on the raw text.
var goDirective = regexp.MustCompile(`(?m)^\s*//go:`)

// stripComments removes /* */ and // comments, so a header that says "no
// @setFloatMode" is not read as using it.
func stripComments(s string) string {
	for {
		i := strings.Index(s, "/*")
		if i < 0 {
			break
		}
		j := strings.Index(s[i+2:], "*/")
		if j < 0 {
			s = s[:i]
			break
		}
		s = s[:i] + " " + s[i+2+j+2:]
	}
	lines := strings.Split(s, "\n")
	for k, l := range lines {
		if i := strings.Index(l, "//"); i >= 0 {
			lines[k] = l[:i]
		}
	}
	return strings.Join(lines, "\n")
}

// contractViolations lists what a same-algorithm source of language lang
// (a bench.json "sources" key) uses that the contract forbids.
func contractViolations(lang, src string) []string {
	if lang == "mk-wrap" || lang == "mk-ctx" {
		lang = "mk"
	}
	code := stripComments(src)
	if lang == "c" {
		code = cPragmaLib.ReplaceAllString(code, "")
	}
	var out []string
	for _, b := range sameBans {
		if (b.lang == "" || b.lang == lang) && b.re.MatchString(code) {
			out = append(out, b.what)
		}
	}
	if lang == "go" && goDirective.MatchString(src) {
		out = append(out, "a //go: directive")
	}
	return out
}

// TestSameRowBans: the ban list covers the hints and unsafe forms the
// codegen kernels' test bans and those the benchmark rules name, on code, not on
// the header comments that promise their absence.
func TestSameRowBans(t *testing.T) {
	bad := map[string][]string{
		"zig":  {"pub fn main() void { @setFloatMode(.optimized); }", "fn f() void { @branchHint(.likely); }", "fn f() void { @prefetch(p, .{}); }", "inline fn f() void {}", "fn f() void { inline for (xs) |x| {} }"},
		"c":    {"void f(int *restrict p) {}", "#pragma clang loop vectorize(enable)\n", "#pragma once\n", "#pragma unroll\n", "#pragma comment(linker, \"/STACK:0x10000000\")\n", "#pragma comment(linker, \"/OPT:ICF\")\n", "if (__builtin_expect(x, 0)) {}", "__builtin_unreachable();", "__attribute__((hot)) void f(void) {}", "static inline int f(void) { return 0; }", "#include <immintrin.h>\n"},
		"rust": {"unsafe fn f() {}", "fn f() { unsafe { g() } }", "std::hint::assert_unchecked(x);", "core::hint::unreachable_unchecked()", "#[target_feature(enable = \"avx2\")]\nfn f() {}", "#[inline(always)]\nfn f() {}", "use std::arch::x86_64::*;"},
		"go":   {"import \"unsafe\"\n", "//go:noinline\nfunc f() {}\n"},
		"cs":   {"unsafe class C {}", "Span<byte> s = stackalloc byte[32];", "[MethodImpl(MethodImplOptions.AggressiveInlining)]\nstatic int F() => 1;", "[MethodImpl(MethodImplOptions.AggressiveOptimization)]\nstatic int F() => 1;", "using System.Runtime.Intrinsics.X86;", "var x = Unsafe.Add(ref a, 1);", "ref var r = ref MemoryMarshal.GetArrayDataReference(arr);", "var v = Vector<byte>.Zero;"},
		"mk":   {"import std.simd\n", "unsafe_modules: [\"x\"],"},
	}
	for lang, srcs := range bad {
		for _, src := range srcs {
			if v := contractViolations(lang, src); len(v) == 0 {
				t.Errorf("%s source %q passed the same-algorithm bans", lang, src)
			}
		}
	}
	good := map[string]string{
		"zig":  "// no @setFloatMode, no @Vector, no inline fn\nconst x = 1;\n",
		"c":    "/* no restrict, no #pragma */\n// no __builtin_expect\n#pragma comment(lib, \"ws2_32\")\nint main(void) { return 0; }\n",
		"rust": "// No unsafe, no std::arch.\nfn main() {}\n",
		"go":   "// no \"unsafe\"\nimport \"fmt\"\n",
		"cs":   "// no unsafe, no stackalloc, no MethodImpl, no Unsafe.*\nusing System;\nConsole.WriteLine(1);\n",
		"mk":   "    unsafe_modules: [],\n		links: [],\n",
	}
	for lang, src := range good {
		if v := contractViolations(lang, src); len(v) != 0 {
			t.Errorf("%s source %q flagged: %v", lang, src, v)
		}
	}
}

// TestReadmeSizesMatchBenchJSON: a same row's README that names a size
// or a checksum names the one bench.json measures and publishes.
func TestReadmeSizesMatchBenchJSON(t *testing.T) {
	wd, _ := os.Getwd()
	root, err := repoRoot(wd)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := discoverTree(t, root)
	if err != nil {
		t.Fatal(err)
	}
	size := regexp.MustCompile("(?i)\\b(publish|quick)(?: size)? `([^`]+)`")
	number := regexp.MustCompile("`([0-9]{5,})`")
	for _, r := range rows {
		if r.Lane != "" || r.Table != "same" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(r.Dir, "README.md"))
		if err != nil {
			continue // TestTreeRowsMeetTheContract reports it
		}
		md := string(b)
		args, quick := strings.Join(r.Args, " "), strings.Join(r.Quick, " ")
		for _, m := range size.FindAllStringSubmatch(md, -1) {
			want := args
			if strings.EqualFold(m[1], "quick") {
				want = quick
			}
			if m[2] != want {
				t.Errorf("%s: README.md names %s size %q, bench.json has %q", r.id(), strings.ToLower(m[1]), m[2], want)
			}
		}
		for _, m := range number.FindAllStringSubmatch(md, -1) {
			if n := m[1]; n != r.Checksum && n != r.QuickSum && n != args && n != quick {
				t.Errorf("%s: README.md names %s, which is neither a size nor a checksum of bench.json (%s at %q, %s at %q)", r.id(), n, r.Checksum, args, r.QuickSum, quick)
			}
		}
	}
}

// TestCountsNeedAMajority: with no counts line printed by a majority of
// the languages, nothing is agreed, no language is timed as verified, and
// every cell of the row is excluded.
func TestCountsNeedAMajority(t *testing.T) {
	for name, tc := range map[string]struct {
		votes map[string]int
		n     int
		want  string
	}{
		"five distinct": {map[string]int{"a": 1, "b": 1, "c": 1, "d": 1, "e": 1}, 5, ""},
		"2-2-1":         {map[string]int{"a": 2, "b": 2, "c": 1}, 5, ""},
		"2 of 5 agree":  {map[string]int{"a": 2}, 5, ""}, // three builds failed
		"3-2":           {map[string]int{"a": 3, "b": 2}, 5, "a"},
		"all":           {map[string]int{"a": 5}, 5, "a"},
		"1-1":           {map[string]int{"a": 1, "b": 1}, 2, ""},
	} {
		if got, ok := majority(tc.votes, tc.n); got != tc.want || ok != (tc.want != "") {
			t.Errorf("%s: majority = %q, %v; want %q", name, got, ok, tc.want)
		}
	}

	// Five counting builds that print five different valid lines: the case
	// publish sees when every language disagrees.
	r := &row{Name: "r", QuickSum: "42", Counts: []string{"n"}}
	var bs []*built
	for i, id := range countColumns {
		c, _ := columnByID(id)
		bs = append(bs, &built{Row: r, Col: c, Exe: helperExe(t, "42\ncounts n="+string(rune('1'+i)))})
	}
	res, agreed, ok := checkCounts(r, bs)
	if ok || agreed != "" {
		t.Fatalf("checkCounts over five distinct lines: agreed %q, ok %v", agreed, ok)
	}
	for _, cr := range res {
		if cr.Err != nil || !cr.Wrong {
			t.Errorf("%s: %+v, want a valid line marked as disagreeing", cr.B.Col.ID, cr)
		}
	}
	ex := countExclusions(res, agreed)
	if !strings.Contains(ex["*"], "majority") || !strings.Contains(ex["*"], "counts n=5") {
		t.Fatalf("no row-wide exclusion naming every line: %v", ex)
	}

	// A 4-2 split still has a majority: the two are excluded, the row is
	// not.
	for i := range bs {
		line := "42\ncounts n=1"
		if i >= len(bs)-2 {
			line = "42\ncounts n=2"
		}
		bs[i].Exe = helperExe(t, line)
	}
	res, agreed, ok = checkCounts(r, bs)
	ex = countExclusions(res, agreed)
	if ok || agreed != "counts n=1" || ex["*"] != "" || len(ex) != 2 {
		t.Fatalf("majority-2 split: agreed %q, ok %v, exclusions %v", agreed, ok, ex)
	}

	// verify's checksum vote before a checksum is recorded: 1-1 is no
	// agreement either.
	two := []*built{{Row: r, Col: bs[0].Col, Exe: helperExe(t, "1")}, {Row: r, Col: bs[1].Col, Exe: helperExe(t, "2")}}
	vres, sum, vok := verify(two, nil, "")
	if vok || sum != "" || !vres[0].Wrong || !vres[1].Wrong {
		t.Fatalf("verify with two different checksums agreed on %q (ok %v)", sum, vok)
	}
}

// helperExe is a copy of the test binary that prints out and exits
// (TestMain reads it from the file <exe>.out beside the copy).
func helperExe(t *testing.T, out string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "helper.exe")
	copyFile(t, os.Args[0], p)
	writeFile(t, p+".out", out)
	return p
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	b, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, b, 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestBudgetVerdictCitesTheInterval: three rows whose prices are 1.02,
// 1.03 and 1.04, each with an interval that straddles 1.03, are not
// "within" the bounds budget: the verdict is inconclusive. Tight rows well
// below or above the budget are within or over.
func TestBudgetVerdictCitesTheInterval(t *testing.T) {
	spread := func(m, w float64) []float64 {
		var xs []float64
		for i := range 30 {
			xs = append(xs, m*(1+w*(float64(i%7)-3)/3))
		}
		return xs
	}
	build := func(prices []float64, w float64) *results {
		res := &results{Kind: "budgets", Runs: []facts{{}}}
		for i, pr := range prices {
			id := string(rune('a' + i))
			res.Rows = append(res.Rows, rowInfo{ID: id, Table: "same", Overflow: "never"})
			for col, m := range map[string]float64{"mk": pr, "mk-nobounds": 1, "mk-noovf": 1} {
				c := cell{Row: id, Table: "same", Col: col, Times: spread(m, w)}
				c.Summary = summarize(c.Times)
				res.Cells = append(res.Cells, c)
			}
		}
		return res
	}
	noisy := build([]float64{1.02, 1.03, 1.04}, 0.03)
	for _, p := range budgetPrices(noisy, "mk", "mk-nobounds") {
		if !(p.Price.CI.Lo < boundsBudget && boundsBudget < p.Price.CI.Hi) {
			t.Fatalf("row %s: interval %+v does not straddle %.2f; the fixture is wrong", p.Row, p.Price.CI, boundsBudget)
		}
	}
	md := renderBudgets(noisy, "D")
	if !strings.Contains(md, "**Bounds checks:** median price +3.0% [") || !strings.Contains(md, "over 3 rows, inconclusive against the budget (at most 3%)") {
		t.Errorf("noisy rows at 1.02, 1.03, 1.04 are not inconclusive:\n%s", md)
	}
	if strings.Contains(md, "within the budget (at most 3%)") {
		t.Errorf("a point median was declared within the budget:\n%s", md)
	}
	if md := renderBudgets(build([]float64{1.00, 1.01, 1.01}, 0.002), "D"); !strings.Contains(md, "over 3 rows, within the budget (at most 3%)") {
		t.Errorf("tight rows at 1.00 to 1.01 are not within:\n%s", md)
	}
	if md := renderBudgets(build([]float64{1.06, 1.07, 1.08}, 0.002), "D"); !strings.Contains(md, "over 3 rows, over the budget (at most 3%)") {
		t.Errorf("tight rows at 1.06 to 1.08 are not over:\n%s", md)
	}
	if v := budgetVerdict(1.02, interval{1.01, 1.04}, boundsBudget); !strings.HasPrefix(v, "inconclusive") {
		t.Errorf("budgetVerdict(1.02, [1.01, 1.04]) = %q, want inconclusive although the point is below 1.03", v)
	}
}
