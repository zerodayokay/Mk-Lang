// Why: the benchmark contract (tests/bench/README.md) bans tuning and
// unchecked tricks per table, and each ban needs a check over the tree's
// real sources, or a baseline can drift out of it unseen.
// What: the ban lists (simdLayer, mkDispatch, rowViolations, simdViolations)
// and TestOtherTablesNoTuning to TestSharedMachineCaveat.
// How: regular-expression scans of every row's sources, plus builds of the
// gzip row's columns run on tampered files (skipped under -short).

package main

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// simdLayer names the sameBans entries the SIMD table may use, because they
// are the vector layer the table measures: the intrinsics header,
// std::arch, @Vector, Mk's std.simd, and four that the SIMD table replaces
// with narrower bans (simdViolations): Rust's unsafe (only the blocks
// std::arch needs), target_feature (only cfg reads of the build's target),
// C's __builtin_ (only __builtin_ctz) and Zig's forced inlining (only
// `inline for`: the comptime loop over the row's fixed number of vectors,
// which the C and Rust sides write out as one statement per vector, and
// validutf8's comptime table lookup).
var simdLayer = map[string]bool{
	"an intrinsics header":                        true,
	"std::arch intrinsics":                        true,
	"hand vectorisation (@Vector)":                true,
	"std.simd (hand vectorisation)":               true,
	"System.Numerics.Vector (hand vectorisation)": true,
	"System.Runtime.Intrinsics":                   true,
	"unsafe":                                      true,
	"target_feature":                              true,
	"a __builtin_ hint":                           true,
	"forced inlining or unrolling":                true,
}

var (
	rustUnsafeItem   = regexp.MustCompile(`\bunsafe\s+(fn|impl|trait|extern)\b`)
	rustTargetAttr   = regexp.MustCompile(`#\s*!?\[\s*target_feature`)
	cBuiltin         = regexp.MustCompile(`__builtin_\w+`)
	zigForcedInline  = regexp.MustCompile(`\binline\s+(fn|while)\b|\.always_inline`)
	rustPtrArith     = regexp.MustCompile(`\.(add|sub|offset|byte_add|byte_sub|byte_offset|wrapping_offset)\(`)
	rustLenAssert    = regexp.MustCompile(`assert!\([^;]*<=[^;]*\.len\(\)`)
	rustFnStart      = regexp.MustCompile(`(?m)\bfn\s+\w+`)
	simdAllowBuiltin = map[string]bool{
		// The bit scan of a movemask: the C spelling of what every other
		// column takes from its language (Rust trailing_zeros, Zig @ctz,
		// Go bits.TrailingZeros).
		"__builtin_ctz": true,

		// The same for a 64-lane avx512bw compare mask (indexdispatch.c).
		"__builtin_ctzll": true,
	}
	// cTargetAttr is the C form of a function compiled for CPU features,
	// which only a dispatch row may use (rowViolations).
	cTargetAttr = regexp.MustCompile(`__attribute__\s*\(\(\s*target\s*\(\s*"[a-z0-9.,-]+"\s*\)\s*\)\)`)
	// mkDispatch is an Mk call that chooses its vector form at run time:
	// a std.simd routine that tests Cpu() before it picks a cpu "avx2" or
	// "avx512bw" body (std/simd/bytes.mk, float.mk, utf8.mk, dispatch.mk),
	// or Cpu() itself. It is the Mk form of C's __attribute__((target)) and
	// Rust's #[target_feature], so only a dispatch row may use it.
	mkDispatch = regexp.MustCompile(`\bsimd\.(IndexByte|IndexAny|Count|Equal|Compare|Dot|Sum|ValidUtf8|Dispatch)\s*[\[(]|\bCpu\s*\(\s*\)`)
)

// mkDispatchUse is rowViolations' finding for an Mk source of a
// fixed-target SIMD row that matches mkDispatch. A lane row may be
// exempted by this text (an internal comparison of std's routines as they
// ship, TestReadmeNamesTheExceptions).
const mkDispatchUse = "a std.simd routine that chooses its vector form at run time (Cpu()), which a fixed-target SIMD row forbids"

// rowViolations is what a source of language lang in row r uses that the
// contract forbids: the same-algorithm list in "same", otherTableViolations
// elsewhere, and in a SIMD row with a dispatch field (the labelled
// dispatch row) the SIMD list with the two dispatch forms let through: C's
// __attribute__((target("..."))), and no other attribute, and Rust's
// #[target_feature] on a safe fn (an unsafe fn is still refused). In a SIMD
// row without dispatch, an Mk source may not call a std.simd routine that
// chooses its form at run time (mkDispatch); a variant that skips Mk
// has no Mk source after expand, so a held program is not read.
func rowViolations(r *row, lang, src string) []string {
	if r.Table == "same" {
		return contractViolations(lang, src)
	}
	if len(r.Dispatch) == 0 || r.Table != "simd" {
		out := otherTableViolations(r.Table, lang, src)
		if r.Table == "simd" && isMkLang(lang) && mkDispatch.MatchString(stripComments(src)) {
			out = append(out, mkDispatchUse)
		}
		return out
	}
	if lang == "c" {
		src = cTargetAttr.ReplaceAllString(src, " ")
	}
	var out []string
	for _, v := range otherTableViolations(r.Table, lang, src) {
		if lang == "rust" && strings.HasPrefix(v, "#[target_feature]") {
			continue
		}
		out = append(out, v)
	}
	return out
}

// otherTableViolations lists what a source of language lang in a row of a
// table other than "same" uses that the contract forbids: every
// table forbids unchecked indexing, fast-math and per-language tuning
// unless every side has the same, so the same-algorithm list (sameBans)
// applies everywhere, and only the SIMD table may use its vector layer.
func otherTableViolations(table, lang, src string) []string {
	if lang == "mk-wrap" || lang == "mk-keyed" || lang == "mk-ctx" {
		lang = "mk"
	}
	if table != "simd" {
		var out []string
		for _, v := range contractViolations(lang, src) {
			// stackalloc is forbidden in the same table only.
			if lang == "cs" && v == "stackalloc" {
				continue
			}
			out = append(out, v)
		}
		return out
	}
	var out []string
	for _, v := range contractViolations(lang, src) {
		if lang == "cs" && v == "stackalloc" {
			continue
		}
		// C# unsafe is forbidden in every table, SIMD included.
		if lang == "cs" && v == "unsafe" {
			out = append(out, v)
			continue
		}
		if !simdLayer[v] {
			out = append(out, v)
		}
	}
	return append(out, simdViolations(lang, stripComments(src))...)
}

// simdViolations is the SIMD table's narrower form of the bans it lifts,
// and its rule for Rust: a load through pointer arithmetic is unchecked
// indexing unless the same function asserts the bound first (the form of
// validutf8.rs); a pointer taken from a checked slice (`s[i..i + W]
// .as_ptr()`, indexbyte.rs and dot.rs) needs no assert.
func simdViolations(lang, code string) []string {
	var out []string
	switch lang {
	case "rust":
		if rustUnsafeItem.MatchString(code) {
			out = append(out, "unsafe beyond the blocks std::arch needs (an unsafe fn, impl, trait or extern)")
		}
		if rustTargetAttr.MatchString(code) {
			out = append(out, "#[target_feature] (the table has one CPU target per variant and no dispatch)")
		}
		for _, m := range rustPtrArith.FindAllStringIndex(code, -1) {
			body := code[:m[0]]
			if fns := rustFnStart.FindAllStringIndex(body, -1); len(fns) > 0 {
				body = body[fns[len(fns)-1][0]:]
			}
			if !rustLenAssert.MatchString(body) {
				line := 1 + strings.Count(code[:m[0]], "\n")
				out = append(out, "unchecked indexing (pointer arithmetic with no bound asserted before it in its fn, line "+strconv.Itoa(line)+"; take the pointer from a checked slice, BS-2)")
				break
			}
		}
	case "zig":
		if zigForcedInline.MatchString(code) {
			out = append(out, "forced inlining or unrolling (inline fn, inline while or .always_inline)")
		}
	case "c":
		for _, b := range cBuiltin.FindAllString(code, -1) {
			if !simdAllowBuiltin[b] {
				out = append(out, "a __builtin_ hint ("+b+")")
				break
			}
		}
	}
	return out
}

// TestOtherTablesNoTuning: outside the same-algorithm table the
// same bans hold (unchecked indexing, fast-math, hints, forced inlining,
// //go: directives, Mk unsafe modules), and only the SIMD table may use
// its vector layer.
func TestOtherTablesNoTuning(t *testing.T) {
	has := func(vs []string, part string) bool {
		for _, v := range vs {
			if strings.Contains(v, part) {
				return true
			}
		}
		return false
	}
	type fixture struct {
		table, lang, src, want string // want "" means clean
	}
	for _, f := range []fixture{
		{"idiomatic", "rust", "#[inline(always)]\nfn f() {}\n", "inlining hint"},
		{"idiomatic", "zig", "fn f() void { @branchHint(.likely); }\n", "@branchHint"},
		{"idiomatic", "zig", "fn f(p: [*]u8) void { @prefetch(p, .{}); }\n", "@prefetch"},
		{"idiomatic", "c", "#pragma GCC unroll 4\n", "#pragma"},
		{"idiomatic", "c", "void f(int *restrict p);\n", "restrict"},
		{"idiomatic", "c", "__attribute__((hot)) void f(void);\n", "attribute"},
		{"idiomatic", "go", "package main\n//go:noinline\nfunc f() {}\n", "//go:"},
		{"idiomatic", "go", "package main\nimport \"unsafe\"\n", "unsafe"},
		{"idiomatic", "rust", "unsafe fn f() {}\n", "unsafe"},
		{"idiomatic", "rust", "fn f() { std::hint::black_box(1); }\n", "std::hint"},
		{"idiomatic", "mk", "Main() {}\nconst Package = pkg.Package(unsafe_modules: [\"std.raw\"], links: [])\n", "unsafe module"},
		{"idiomatic", "c", "#include <immintrin.h>\n", "intrinsics"},
		{"idiomatic", "zig", "const V = @Vector(4, f32);\n", "@Vector"},
		{"idiomatic", "mk", "use std.simd\n", "std.simd"},
		{"idiomatic", "rust", "// no #[inline] here\nfn f() {}\n", ""},
		{"simd", "rust", "#[inline(always)]\nfn f() {}\n", "inlining hint"},
		{"simd", "zig", "fn f() void { @branchHint(.likely); }\n", "@branchHint"},
		{"simd", "rust", "use std::arch::x86_64::*;\n#[cfg(target_feature = \"avx2\")]\nfn f(s: &[u8]) { unsafe { _mm256_loadu_si256(s[0..32].as_ptr() as *const __m256i); } }\n", ""},
		{"simd", "rust", "#[target_feature(enable = \"avx2\")]\nfn f() {}\n", "target_feature"},
		{"simd", "rust", "unsafe fn f() {}\n", "unsafe"},
		{"simd", "rust", "fn f(s: &[u8], i: usize) { let p = s.as_ptr(); unsafe { _mm_loadu_si128(p.add(i) as *const __m128i); } }\n", "unchecked indexing"},
		{"simd", "rust", "fn f(s: &[u8], i: usize) { assert!(i + 16 <= s.len()); unsafe { _mm_loadu_si128(s.as_ptr().add(i) as *const __m128i); } }\n", ""},
		{"simd", "rust", "fn g(s: &[u8], i: usize) { assert!(i + 16 <= s.len()); }\nfn f(s: &[u8], i: usize) { unsafe { _mm_loadu_si128(s.as_ptr().add(i) as *const __m128i); } }\n", "unchecked indexing"},
		{"simd", "c", "#include <immintrin.h>\nint f(unsigned m) { return __builtin_ctz(m); }\n", ""},
		{"simd", "c", "int f(int x) { return __builtin_expect(x, 1); }\n", "__builtin_expect"},
		{"simd", "c", "float f(float *restrict p);\n", "restrict"},
		{"simd", "zig", "const V = @Vector(4, f32);\n", ""},
		{"simd", "zig", "fn f() void { inline for (0..4) |k| g(k); }\n", ""},
		{"simd", "zig", "inline fn f() void {}\n", "forced inlining"},
		{"simd", "zig", "fn f() void { @call(.always_inline, g, .{}); }\n", "forced inlining"},
		{"simd", "rust", "fn f(s: &[u8]) -> u8 { unsafe { *s.get_unchecked(3) } }\n", "get_unchecked"},
		{"simd", "zig", "fn f() void { @setRuntimeSafety(false); }\n", "@setRuntimeSafety"},
		{"idiomatic", "cs", "using System.Runtime.Intrinsics;\n", "System.Runtime.Intrinsics"},
		{"idiomatic", "cs", "var v = Vector<byte>.Zero;\n", "System.Numerics.Vector"},
		{"idiomatic", "cs", "[MethodImpl(MethodImplOptions.AggressiveInlining)]\nstatic void F() {}\n", "MethodImpl"},
		{"idiomatic", "cs", "unsafe class C {}\n", "unsafe"},
		{"idiomatic", "cs", "var x = Unsafe.As<int, uint>(ref a);\n", "Unsafe.*"},
		{"idiomatic", "cs", "Span<byte> buf = stackalloc byte[64];\n", ""},
		{"simd", "cs", "using System.Numerics;\nvar v = Vector<byte>.Zero;\n", ""},
		{"simd", "cs", "unsafe void F() {}\n", "unsafe"},
		{"simd", "cs", "var x = Unsafe.Add(ref a, 1);\n", "Unsafe.*"},
		{"simd", "cs", "[MethodImpl(MethodImplOptions.AggressiveOptimization)]\nstatic void F() {}\n", "MethodImpl"},
	} {
		vs := otherTableViolations(f.table, f.lang, f.src)
		switch {
		case f.want == "" && len(vs) > 0:
			t.Errorf("%s %s %q: flagged %v", f.table, f.lang, f.src, vs)
		case f.want != "" && !has(vs, f.want):
			t.Errorf("%s %s %q: not flagged for %q (got %v)", f.table, f.lang, f.src, f.want, vs)
		}
	}
}

// TestRustSimdLoadsChecked: the tree's Rust SIMD sources load
// only through checked slices or after an asserted bound, so their rust
// and rust-ovf columns are bounds-checked as their class says.
func TestRustSimdLoadsChecked(t *testing.T) {
	dir := filepath.Join("..", "..", "tests", "bench", "simd")
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range ents {
		p := filepath.Join(dir, e.Name(), e.Name()+".rs")
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		n++
		for _, v := range simdViolations("rust", stripComments(string(b))) {
			t.Errorf("%s: %s", p, v)
		}
	}
	if n < 4 {
		t.Fatalf("found %d Rust SIMD sources, want the four rows'", n)
	}
}

// TestGzipTrailerChecked: every built column of the gzip row
// checks the trailer, as the row's task states: on a file whose trailer
// CRC-32 or length has one byte flipped, each exits non-zero or prints
// another checksum, and on the intact file each prints the right one. Zig
// 0.17's Decompress stores the trailer without comparing it, so a Zig cell
// that relies on it prints the normal checksum and skips the CRC-32 pass the
// go column pays. Builds the tree's mk (testMkExe) and the mk, go, zig-fast
// and zig-safe columns into the work directory (stamped, so a rerun does
// not rebuild); the mk column is the row's Mk program.
func TestGzipTrailerChecked(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the gzip row's columns")
	}
	wd, _ := os.Getwd()
	root, err := repoRoot(wd)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := discoverTree(t, root)
	if err != nil {
		t.Fatal(err)
	}
	var gz *row
	for _, r := range rows {
		if r.id() == "gzip" {
			gz = r
		}
	}
	if gz == nil {
		t.Fatal("no gzip row")
	}
	tc, err := findToolchain(defaultWork(), "")
	if err != nil {
		t.Fatal(err)
	}
	if tc.Zig == "" || tc.Go == "" || tc.Clang == "" {
		t.Skip("zig, go or clang not found")
	}
	// One binary directory per test process, so two test runs at once (the
	// gate and another run) never share an executable or a stamp; the
	// compilers' caches under the work directory stay shared.
	tag := "test-bs1-" + strconv.Itoa(os.Getpid())
	t.Cleanup(func() { os.RemoveAll(filepath.Join(tc.Work, "bin", tag)) })
	// The mk, Zig and Go columns: the Mk program checks the trailer
	// inside std.compress's Inflater, which only running it shows.
	if gz.Sources["mk"] == "" {
		t.Fatal("the gzip row has no Mk source")
	}
	if tc.Mk, err = testMkExe(t, tc, root, tag); err != nil {
		t.Fatal(err)
	}
	bs := buildAll(tc, tag, []*row{gz}, func(c column) bool { return c.Lang == "mk" || c.Lang == "zig" || c.Lang == "go" }, func(string) {})
	if len(bs) < 4 {
		t.Fatalf("built %d columns of gzip, want mk, go, zig-fast and zig-safe", len(bs))
	}
	// A small input of the row's kind: text, gzip'd by Go's compress/gzip.
	var text bytes.Buffer
	for i := 0; text.Len() < 300000; i++ {
		fmt.Fprintf(&text, "word%d the quick brown fox %d\n", i%977, i*7919%100003)
	}
	var want uint64
	for _, c := range text.Bytes() {
		want += uint64(c)
	}
	want += uint64(text.Len())
	var z bytes.Buffer
	zw := gzip.NewWriter(&z)
	zw.Write(text.Bytes())
	zw.Close()
	dir := t.TempDir()
	write := func(name string, flip int) string {
		b := bytes.Clone(z.Bytes())
		if flip > 0 {
			b[len(b)-flip] ^= 0x01
		}
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	good, badCRC, badLen := write("good.gz", 0), write("crc.gz", 8), write("len.gz", 4)
	for _, b := range bs {
		if b.Err != nil {
			t.Errorf("%s: build failed: %v", b.Col.ID, b.Err)
			continue
		}
		out, _, err := runOnceEnv(b.Exe, []string{good, "1"}, rowEnv(gz))
		if err != nil || out != strconv.FormatUint(want, 10) {
			t.Errorf("%s on the intact file: %q, %v; want %d", b.Col.ID, out, err, want)
		}
		for _, bad := range []string{badCRC, badLen} {
			out, _, err := runOnceEnv(b.Exe, []string{bad, "1"}, rowEnv(gz))
			if err == nil && out == strconv.FormatUint(want, 10) {
				t.Errorf("%s accepted %s with the normal checksum: it does not check the gzip trailer", b.Col.ID, filepath.Base(bad))
			}
		}
	}
}

// TestFillBaseline: every GB/s row of the SIMD table names a
// baseline (its arguments with 0 repetitions); a baseline on a row with no
// bytes, or equal to the arguments, is refused; and RESULTS.md takes the
// row's times, ratios and GB/s from the kernel (each run minus its cell's
// baseline median), so a fast fill does not outrank a fast kernel, while a
// row timed without baselines says its numbers include the fill.
func TestFillBaseline(t *testing.T) {
	wd, _ := os.Getwd()
	root, err := repoRoot(wd)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := discoverTree(t, root)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range rows {
		if r.Lane != "" || r.Table != "simd" || r.Bytes == 0 {
			continue
		}
		n++
		if len(r.Baseline) != len(r.Args) || len(r.Args) != 3 || r.Baseline[1] != "0" || r.Baseline[0] != r.Args[0] || r.Baseline[2] != r.Args[2] {
			t.Errorf("%s: baseline %q is not its arguments %q with reps 0", r.id(), r.Baseline, r.Args)
		}
	}
	if n < 18 {
		t.Fatalf("%d GB/s SIMD rows, want the 18 variants of indexbyte, dot and validutf8", n)
	}

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "nobytes.json"), `{"table":"simd","args":["9","5","1"],"quick":["9"],"sources":{"c":"x.c"},"baseline":["9","0","1"]}`)
	writeFile(t, filepath.Join(dir, "same.json"), `{"table":"simd","args":["9","5","1"],"quick":["9"],"sources":{"c":"x.c"},"bytes":45,"baseline":["9","5","1"]}`)
	writeFile(t, filepath.Join(dir, "x.c"), "// x, C\n")
	for _, f := range []string{"nobytes.json", "same.json"} {
		if _, err := loadRow(filepath.Join(dir, f), ""); err == nil || !strings.Contains(err.Error(), "baseline") {
			t.Errorf("%s: loaded (%v)", f, err)
		}
	}

	// c: whole run 1.10 s with a 0.10 s fill, kernel 1.00 s; go: whole run
	// 1.05 s with a 0.35 s fill, kernel 0.70 s. By whole runs c would rank
	// behind go only slightly; by kernels go is the faster SIMD cell.
	res := &results{
		Runs:    []facts{{Commit: "abc", CPU: "cpu", Versions: map[string]string{}}},
		Columns: columns, Sweeps: 3, Options: hfOptions{Warmup: 1, Runs: 10},
		Rows: []rowInfo{
			{ID: "ib-dram", Table: "simd", Algorithm: "Find.", Args: []string{"1000", "10", "1"}, Baseline: []string{"1000", "0", "1"}, Checksum: "1", Bytes: 7e9},
			{ID: "ib-old", Table: "simd", Algorithm: "Find.", Args: []string{"1000", "10", "1"}, Checksum: "1", Bytes: 7e9},
		},
	}
	mk := func(row, col string, whole, base float64) cell {
		ts := []float64{whole - 0.01, whole, whole + 0.01}
		c := cell{Row: row, Table: "simd", Col: col, Class: "unchecked", Times: ts, Summary: summarize(ts)}
		if base > 0 {
			c.Base = []float64{base - 0.001, base, base + 0.001}
			s := summarize(c.Base)
			c.BaseSummary = &s
		}
		return c
	}
	res.Cells = []cell{mk("ib-dram", "c", 1.10, 0.10), mk("ib-dram", "go", 1.05, 0.35), mk("ib-old", "c", 1.10, 0), mk("ib-old", "go", 1.05, 0)}
	md := renderResults(res, "2026-11-08")
	for _, w := range []string{
		"| ib-dram | - | 1.000 s, x1.43 | 700.0 ms, x1.00 |", // the summary table ranks kernels
		"| c | unchecked | 1.000 s |",                        // the kernel median
		"| 1.100 s | 100.0 ms (9%) | 7.00 |",                 // whole run, baseline and share, GB/s = 7e9 / 1.0 s
		"| 1.050 s | 350.0 ms (33%) | 10.00 |",               // 7e9 / 0.7 s
		"Baseline `1000 0 1`",
		"GB/s (kernel)",
		"GB/s (median, fill included)",
		"Timed before baselines:",
	} {
		if !strings.Contains(md, w) {
			t.Errorf("RESULTS.md lacks %q:\n%s", w, md)
		}
	}
	// The row without a baseline renders as a plain row: whole-program times.
	if !strings.Contains(md, "| ib-old | - | 1.100 s, x1.05 | 1.050 s, x1.00 |") {
		t.Errorf("a row without baselines changed its numbers:\n%s", md)
	}
}

// simdOffsetRe is the measured arena offset a labelled alignment exception
// states; TestArenaOffsetsAsLabelled checks its value against a probe.
var simdOffsetRe = regexp.MustCompile(`at \d+ mod 64`)

// TestSimdBuffersAligned: every program of a GB/s SIMD row puts
// its buffers on a 64-byte boundary and refuses to run otherwise, so no
// cell measures its allocator's alignment. Mk code cannot read an address
// without std.raw, which the contract bans, so a scalar Mk cell (no
// std.simd) is exempt and an Mk vector cell is the labelled exception,
// whose stated offset and cost TestArenaOffsetsAsLabelled checks.
func TestSimdBuffersAligned(t *testing.T) {
	wd, _ := os.Getwd()
	root, err := repoRoot(wd)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := discoverTree(t, root)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, r := range rows {
		if r.Lane != "" || r.Table != "simd" || r.Bytes == 0 {
			continue
		}
		for lang, src := range r.Sources {
			p := filepath.Join(r.Dir, src)
			if strings.HasPrefix(lang, "mk") {
				p = filepath.Join(p, r.base()+".mk")
			}
			if seen[p] {
				continue
			}
			seen[p] = true
			b, err := os.ReadFile(p)
			if err != nil {
				t.Errorf("%s: %v", p, err)
				continue
			}
			code := string(b)
			if strings.HasPrefix(lang, "mk") && !strings.Contains(stripComments(code), "simd") {
				continue
			}
			if strings.HasPrefix(lang, "mk") {
				// Mk code cannot read or set an address without std.raw
				// (banned) until std's aligned allocation reaches Arena.
				// Such a cell is the labelled exception (the marker text
				// below): its source says so and the row's
				// notes state the measured offset, so the table carries it.
				// TestArenaOffsetsAsLabelled checks that offset and the
				// cost the notes give it against a probe.
				label := "labelled alignment exception"
				notes := strings.Join(r.Notes, " ")
				if !strings.Contains(strings.ReplaceAll(code, "\n// ", " "), label) || !strings.Contains(notes, label) || !simdOffsetRe.MatchString(notes) {
					t.Errorf("%s (%s): an Mk vector cell without aligned buffers must name %q in its source and in the row's notes with the measured offset (\"at <k> mod 64\")", p, lang, label)
				}
				continue
			}
			if !strings.Contains(code, "buffer not 64-byte aligned") {
				t.Errorf("%s (%s): no 64-byte alignment of its buffers with a refusal (\"buffer not 64-byte aligned\")", p, lang)
			}
		}
	}
	if len(seen) < 12 {
		t.Fatalf("checked %d sources, want the GB/s rows' every language", len(seen))
	}
}

// TestSharedMachineCaveat: the "first table" caveat follows what
// the run saw, not only the process list at its start: a run whose start
// was quiet but where 42 of 72 cells stayed noisy carries it, as does one
// where a later sweep found compilers running; a quiet run does not.
func TestSharedMachineCaveat(t *testing.T) {
	render := func(f facts, noisy, timed int) string {
		res := &results{Runs: []facts{f}, Columns: columns, Sweeps: 3, Options: hfOptions{Warmup: 1, Runs: 10}}
		for i := range timed {
			id := "r" + strconv.Itoa(i)
			ts := []float64{1, 1.01, 0.99}
			res.Rows = append(res.Rows, rowInfo{ID: id, Table: "simd", Algorithm: "A.", Args: []string{"1"}, Checksum: "1"})
			res.Cells = append(res.Cells, cell{Row: id, Table: "simd", Col: "c", Class: "unchecked", Times: ts, Summary: summarize(ts), Noisy: i < noisy})
		}
		res.Rows = append(res.Rows, rowInfo{ID: "x", Table: "simd", Algorithm: "A.", Args: []string{"1"}, Checksum: "1"})
		res.Cells = append(res.Cells, cell{Row: "x", Table: "simd", Col: "c", Excluded: "build failed"})
		return renderResults(res, "2026-11-08")
	}
	const caveat = "so these numbers are a first table"
	if md := render(facts{}, 42, 72); !strings.Contains(md, caveat) || !strings.Contains(md, "42 of its 72 timed cells stayed noisy") {
		t.Errorf("42 of 72 noisy cells, no caveat:\n%s", md)
	}
	if md := render(facts{}, 18, 72); strings.Contains(md, caveat) {
		t.Errorf("a quarter of the cells noisy (not more) gave the caveat:\n%s", md)
	}
	if md := render(facts{BusyLater: []string{"go.exe x3"}}, 0, 10); !strings.Contains(md, caveat) || !strings.Contains(md, "| The same, at the start of a later sweep | go.exe x3 |") {
		t.Errorf("compilers at a later sweep, no caveat:\n%s", md)
	}
	if md := render(facts{Busy: []string{"go.exe x1"}}, 0, 10); !strings.Contains(md, caveat) {
		t.Errorf("compilers at the start, no caveat:\n%s", md)
	}
	if md := render(facts{}, 0, 10); strings.Contains(md, caveat) {
		t.Errorf("a quiet run gave the caveat:\n%s", md)
	}
	if got := strings.Join(mergeBusy([]string{"go.exe x2", "zig.exe x1"}, []string{"go.exe x1", "mk.exe x4"}), ", "); got != "go.exe x2, zig.exe x1, mk.exe x4" {
		t.Errorf("mergeBusy: %s", got)
	}
}
