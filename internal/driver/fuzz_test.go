// Why: fuzz targets and benchmarks run through the driver; the fuzz loop
// must find, save and replay failures, and mk bench must report honest
// statistics.
// What: tests of fuzz.go, bench.go and testing.go: corpus names and
// dictionaries, the fuzz loop's panic and hang handling, a planted bug, and
// mk bench's medians and confidence intervals.
// How: runs std.testing's fuzz targets under plain mk test and mk test
// --fuzz on small packages, and the statistics on fixed samples.

package driver

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestFuzzDirName(t *testing.T) {
	for in, want := range map[string]string{
		"parse bytes":   "parse bytes",
		`a/b\c:d*e?"f"`: `a_b_c_d_e__f_`,
		"tail. ":        "tail__",
		"":              "_",
		"tab\there":     "tab_here",
	} {
		if got := FuzzDirName(in); got != want {
			t.Errorf("FuzzDirName(%q) = %q, want %q", in, got, want)
		}
	}
}

// fuzzSavedRe is the detail of a fuzz run that saved inputs for target two,
// its nouns agreeing with their counts.
var fuzzSavedRe = regexp.MustCompile(`^fuzz: (1 failing run|([02-9]|\d\d+) failing runs), (1 input|([02-9]|\d\d+) inputs) saved to testdata/fuzz/two/$`)

// TestFuzzCounted: the fuzz log's nouns agree with their counts.
func TestFuzzCounted(t *testing.T) {
	for _, c := range []struct {
		got, want string
	}{{counted(1, "call"), "1 call"}, {counted(uint64(0), "call"), "0 calls"}, {counted(uint64(18), "call"), "18 calls"},
		{counted(1, "input"), "1 input"}, {counted(20, "failing run"), "20 failing runs"}} {
		if c.got != c.want {
			t.Errorf("counted: %q, want %q", c.got, c.want)
		}
	}
}

func TestFuzzDictionary(t *testing.T) {
	got := fuzzDictionary(`Parse(b: []u8) {
    if b[0] == 77 && b[1] == b'K' && b.Len() > 0x1234 && x == 0x01020304 {
        Panic("MK\x21\n")
    }
    y := 77
}`)
	want := [][]byte{{0}, {77}, {1}, {'K'}, {0x34, 0x12}, {4, 3, 2, 1}, []byte("MK!\n")}
	for _, w := range want {
		if !slices.ContainsFunc(got, func(g []byte) bool { return bytes.Equal(g, w) }) {
			t.Errorf("dictionary %q lacks %q", got, w)
		}
	}
	seen := map[string]bool{}
	for _, g := range got {
		if seen[string(g)] {
			t.Errorf("dictionary repeats %q", g)
		}
		seen[string(g)] = true
	}
}

func TestBenchMedianCI(t *testing.T) {
	if _, _, _, ok := medianCI([]float64{1, 2, 3, 4, 5}); ok {
		t.Error("an interval from 5 values; the binomial interval needs 6")
	}
	med, lo, hi, ok := medianCI([]float64{6, 1, 5, 2, 4, 3})
	if !ok || med != 3.5 || lo != 1 || hi != 6 {
		t.Errorf("6 values: %v [%v, %v] %v, want 3.5 [1, 6]", med, lo, hi, ok)
	}
	// m = 20: P(X <= 5) = 2.07% and P(X <= 6) = 5.77%, so [x_(6), x_(15)].
	var xs []float64
	for i := 1; i <= 20; i++ {
		xs = append(xs, float64(i))
	}
	if med, lo, hi, ok = medianCI(xs); !ok || med != 10.5 || lo != 6 || hi != 15 {
		t.Errorf("20 values: %v [%v, %v], want 10.5 [6, 15]", med, lo, hi)
	}
	if !stableCI(100, 98, 102, BenchTolerance) || stableCI(100, 97.9, 101, BenchTolerance) {
		t.Error("stableCI's 2% bound")
	}
}

func TestBenchDiffersOnlyOutsideBothIntervals(t *testing.T) {
	base := BenchResult{Median: 100, Lo: 95, Hi: 105}
	for _, c := range []struct {
		now  BenchResult
		want bool
	}{
		{BenchResult{Median: 104, Lo: 99, Hi: 109}, false},   // inside base's
		{BenchResult{Median: 110, Lo: 99, Hi: 120}, false},   // base's median inside now's
		{BenchResult{Median: 110, Lo: 106, Hi: 114}, true},   // apart
		{BenchResult{Median: 80, Lo: 70, Hi: 90}, true},      // apart, faster
		{BenchResult{Median: 105.5, Lo: 90, Hi: 130}, false}, // wide now
	} {
		if got := BenchDiffers(base, c.now); got != c.want {
			t.Errorf("BenchDiffers(%v, %v) = %v", base, c.now, got)
		}
	}
	var b strings.Builder
	rec := BenchRecord{Machine: "m", Command: "mk bench", Time: "t", Results: []BenchEntry{{Module: "main", Name: "x", BenchResult: BenchResult{N: 8, Reps: 6, Median: 110, Lo: 106, Hi: 114, Stable: true}}}}
	baseRec := BenchRecord{Machine: "m0", Command: "mk bench", Time: "t0", Results: []BenchEntry{{Module: "main", Name: "x", BenchResult: base}}}
	if err := WriteBenchText(&b, rec, &baseRec); err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"machine: m\n", "command: mk bench\n", "time: t\n", "MK_TIMING unset", "baseline: m0", `main "x": 110ns/call, 95% CI [106ns, 114ns], n=8, 6 reps`, "DIFFERS"} {
		if !strings.Contains(b.String(), w) {
			t.Errorf("bench text lacks %q:\n%s", w, b.String())
		}
	}
}

// fuzzSource has a fuzz target that panics on input starting "bad", one
// that panics on any input of two bytes or more, one that hangs on any
// input of three bytes or more, one that never fails, a benchmark, and a
// plain test.
const fuzzSource = `import std.testing

Bad(b: []u8) {
    if b.Len() >= 3 && b[0] == 98 && b[1] == 97 && b[2] == 100 {
        Panic("bad input")
    }
}

Two(b: []u8) {
    Assert(b.Len() < 2)
}

Spin(b: []u8) {
    var n := 0
    for b.Len() >= 3 {
        n += 1
        if n > 1_000_000 {
            n = 0
        }
    }
}

Never(b: []u8) {
    _ = b
}

Count(n: int) {
    var s := 0
    for i in 0..n {
        s += i & 3
    }
    Assert(s >= 0)
}

test "bad target" {
    testing.Fuzz(Bad)
}

test "two" {
    testing.Fuzz(Two)
}

test "spin" {
    testing.Fuzz(Spin)
}

test "never" {
    testing.Fuzz(Never)
}

test "count" {
    testing.Bench(Count)
}

test "plain" {
    Assert(true)
}
`

// TestMkTestFuzzCorpus: plain mk test runs a fuzz target over the files of
// testdata/fuzz/<test name>/ in name order, skipping .report files, and a
// panicking file fails the test with its name in the output. Without the
// corpus hook the target is never called, so every test passes.
func TestMkTestFuzzCorpus(t *testing.T) {
	root := testPkg(t, map[string]string{"main.mk": fuzzSource})
	writeFiles(t, root, map[string]string{
		"testdata/fuzz/bad target/a-good":       "good",
		"testdata/fuzz/bad target/b-bad":        "bad!",
		"testdata/fuzz/bad target/b-bad.report": "bad",
		"testdata/fuzz/bad target/c-after":      "after",
		"testdata/fuzz/two/one":                 "x",
	})
	rep := mkTest(t, Config{Root: root}, TestOptions{Run: "t"})
	got := results(rep)
	for _, w := range []string{"main bad target panic", "main two pass", "main count pass"} {
		if !strings.Contains(got, w) {
			t.Errorf("results lack %q:\n%s", w, got)
		}
	}
	for _, tc := range rep.Tests {
		if tc.Name != "bad target" {
			continue
		}
		if !strings.Contains(tc.Output, "fuzz input a-good\nfuzz input b-bad\n") || strings.Contains(tc.Output, "c-after") || strings.Contains(tc.Output, ".report") {
			t.Errorf("bad target's output:\n%s", tc.Output)
		}
	}
	// Without the panicking file the target passes.
	if err := os.Remove(filepath.Join(root, "testdata", "fuzz", "bad target", "b-bad")); err != nil {
		t.Fatal(err)
	}
	rep = mkTest(t, Config{Root: root}, TestOptions{Run: "bad"})
	if got := results(rep); got != "main bad target pass" {
		t.Errorf("after the removal: %s", got)
	}
}

// TestMkTestFuzzLoopSavesPanic: --fuzz runs the loop until it finds a
// panicking input, saves it with its report into the corpus, and fails;
// plain mk test then fails on the saved input.
func TestMkTestFuzzLoopSavesPanic(t *testing.T) {
	root := testPkg(t, map[string]string{"main.mk": fuzzSource})
	rep := mkTest(t, Config{Root: root}, TestOptions{Fuzz: "two", FuzzTime: 30 * time.Second})
	if len(rep.Tests) != 1 || rep.Tests[0].Result != "fail" || !fuzzSavedRe.MatchString(rep.Tests[0].Detail) {
		t.Fatalf("--fuzz two: %+v", rep.Tests)
	}
	dir := filepath.Join(root, "testdata", "fuzz", "two")
	files := corpusFiles(dir)
	if len(files) == 0 {
		t.Fatalf("nothing saved in %s", dir)
	}
	in, err := os.ReadFile(filepath.Join(dir, files[0]))
	if err != nil || len(in) < 2 {
		t.Errorf("saved input %q (%v): want 2 bytes or more", in, err)
	}
	report, err := os.ReadFile(filepath.Join(dir, files[0]+".report"))
	if err != nil || !strings.Contains(string(report), "result: panic\nstatus: 101\n") {
		t.Errorf("report: %s (%v)", report, err)
	}
	rep = mkTest(t, Config{Root: root}, TestOptions{Run: "two"})
	if got := results(rep); got != "main two panic" {
		t.Errorf("plain mk test over the saved input: %s", got)
	}
}

// TestMkTestFuzzLoopHangAndLimits: a target whose call stops returning is
// killed after the hang limit and its input saved as a hang; a target the
// fuzz time ends without a finding passes; a test that never calls
// testing.Fuzz is refused.
func TestMkTestFuzzLoopHangAndLimits(t *testing.T) {
	root := testPkg(t, map[string]string{"main.mk": fuzzSource})
	rep := mkTest(t, Config{Root: root}, TestOptions{Fuzz: "spin", FuzzTime: 20 * time.Second, FuzzHang: time.Second})
	if len(rep.Tests) != 1 || rep.Tests[0].Result != "fail" || !strings.Contains(rep.Tests[0].Output, "hang (no call finished in 1s)") {
		t.Fatalf("--fuzz spin: %+v", rep.Tests)
	}
	files := corpusFiles(filepath.Join(root, "testdata", "fuzz", "spin"))
	if len(files) == 0 {
		t.Fatal("no hang saved")
	}
	rep = mkTest(t, Config{Root: root}, TestOptions{Fuzz: "never", FuzzTime: 20 * time.Second, FuzzIters: 200})
	if len(rep.Tests) != 1 || rep.Tests[0].Result != "pass" || !strings.Contains(rep.Tests[0].Output, "fuzz: 200 calls") {
		t.Errorf("--fuzz with 200 calls: %+v", rep.Tests)
	}
	rep = mkTest(t, Config{Root: root}, TestOptions{Fuzz: "plain", FuzzTime: 5 * time.Second})
	if len(rep.Tests) != 1 || rep.Tests[0].Result != "fail" || !strings.Contains(rep.Tests[0].Detail, "not a fuzz target") {
		t.Errorf("--fuzz plain: %+v", rep.Tests)
	}
}

// plantedMagic panics on the three bytes 0x4D 0x4B 0x21 only. Each byte is
// tested through an xor, so no literal of the file is a byte of the
// magic and the dictionary gives no shortcut: an unguided loop needs about
// 2^24 inputs of three bytes or more, the guided one about 3 x 256, since
// each byte that matches reaches a new block.
const plantedMagic = `import std.testing

Magic(b: []u8) {
    if b.Len() >= 3 {
        if b[0] ^ 0x5A == 0x17 {
            if b[1] ^ 0x5A == 0x11 {
                if b[2] ^ 0x5A == 0x7B {
                    Panic("planted")
                }
            }
        }
    }
}

test "magic" {
    testing.Fuzz(Magic)
}
`

// TestFuzzPlantedMagic is a planted bug: a target that panics only on a
// three-byte magic number is found within the quick tier's fuzz time, 5
// minutes, by the loop the package's live counters guide (Guided). An
// unguided loop finds nothing in that time.
func TestFuzzPlantedMagic(t *testing.T) {
	root := testPkg(t, map[string]string{"main.mk": plantedMagic})
	start := time.Now()
	rep := mkTest(t, Config{Root: root, TestTimeout: 10 * time.Minute}, TestOptions{Fuzz: "magic", FuzzTime: 5 * time.Minute})
	if len(rep.Tests) != 1 || rep.Tests[0].Result != "fail" || !strings.Contains(rep.Tests[0].Output, "panic: planted") && !strings.Contains(rep.Tests[0].Output, "saved") {
		t.Fatalf("the planted magic was not found in 5 minutes: %+v", rep.Tests)
	}
	// The guidance, not luck: the first finding within 500,000 calls. An
	// unguided loop needs about 2^24 calls of three bytes or more (red:
	// with Guided turned off, a 5-minute run's first finding came only
	// after millions of calls, or none).
	m := regexp.MustCompile(`seed \d+: panic after (\d+) calls`).FindStringSubmatch(rep.Tests[0].Output)
	if m == nil {
		t.Fatalf("no finding line:\n%s", rep.Tests[0].Output)
	}
	if n, _ := strconv.Atoi(m[1]); n > 500_000 {
		t.Errorf("the first finding took %d calls: the loop was not guided", n)
	}
	files := corpusFiles(filepath.Join(root, "testdata", "fuzz", "magic"))
	if len(files) == 0 {
		t.Fatal("nothing saved")
	}
	in, _ := os.ReadFile(filepath.Join(root, "testdata", "fuzz", "magic", files[0]))
	if len(in) < 3 || in[0] != 0x4D || in[1] != 0x4B || in[2] != 0x21 {
		t.Errorf("saved input %x does not start with the magic", in)
	}
	t.Logf("found and saved in %s (whole run, PC not idle)", time.Since(start).Round(time.Millisecond))
}

// TestBenchRunsBenchmarksOnly: mk bench selects the tests whose block
// calls testing.Bench, runs their repetitions and fills the statistics.
func TestBenchRunsBenchmarksOnly(t *testing.T) {
	root := testPkg(t, map[string]string{"main.mk": fuzzSource})
	rep := mkTest(t, Config{Root: root}, TestOptions{Bench: true, BenchLimit: 3 * time.Second})
	if len(rep.Tests) != 1 || rep.Tests[0].Name != "count" || rep.Tests[0].Result != "pass" {
		t.Fatalf("mk bench: %+v", rep.Tests)
	}
	b := rep.Tests[0].Bench
	if b == nil || b.N < 1 || b.Reps < BenchBatch || b.Reps != len(b.Times) || !(b.Lo <= b.Median && b.Median <= b.Hi) {
		t.Fatalf("statistics: %+v", b)
	}
	// One call of f(N) took 20 ms or more when N was chosen (time per
	// call times N), unless N started at 1 and the call was already slow.
	if float64(b.N)*b.Median < 5e6 {
		t.Errorf("n = %d at %v ns per call: the calibration stopped early", b.N, b.Median)
	}
}

func TestBlockText(t *testing.T) {
	src := "test \"a { b\" {\n    x := \"}\" // }\n    if y { z }\n}\nAfter() {}"
	if got, want := blockText(src), src[:strings.Index(src, "\nAfter")]; got != want {
		t.Errorf("blockText = %q, want %q", got, want)
	}
}

// TestMkTestFuzzLateHang: a hang that starts near the end of
// the fuzz time, or under a fuzz time shorter than the hang limit, is
// still saved as a hang (the call in progress at the deadline is waited
// on while its count is unmoved), not killed 5 s after the deadline as a
// normal stop that lets the run pass.
func TestMkTestFuzzLateHang(t *testing.T) {
	root := testPkg(t, map[string]string{"main.mk": fuzzSource})
	rep := mkTest(t, Config{Root: root}, TestOptions{Fuzz: "spin", FuzzTime: time.Second, FuzzHang: 7 * time.Second})
	if len(rep.Tests) != 1 || rep.Tests[0].Result != "fail" || !strings.Contains(rep.Tests[0].Output, "hang (no call finished in 7s)") {
		t.Fatalf("--fuzz spin --fuzztime 1s: %+v", rep.Tests)
	}
	if files := corpusFiles(filepath.Join(root, "testdata", "fuzz", "spin")); len(files) == 0 {
		t.Error("no hang saved")
	}
}

// TestFuzzHitCountLoopDepth verifies AFL 8-bit hit-count bucket guidance by
// requiring a loop body (matching XOR-obfuscated bytes so fuzzDictionary cannot
// guess the constant) to iterate 8 times before panicking.
func TestFuzzHitCountLoopDepth(t *testing.T) {
	const src = `import std.testing

LoopDepth(b: []u8) {
    var n := 0
    for x in b {
        if (x ^ 0x5A) == 0x1B {
            n += 1
        } else {
            break
        }
    }
    if n == 8 {
        Panic("depth 8")
    }
}

test "loop_depth" {
    testing.Fuzz(LoopDepth)
}
`
	root := testPkg(t, map[string]string{"main.mk": src})
	rep := mkTest(t, Config{Root: root, TestTimeout: 2 * time.Minute}, TestOptions{Fuzz: "loop_depth", FuzzTime: 45 * time.Second})
	if len(rep.Tests) != 1 || rep.Tests[0].Result != "fail" || !strings.Contains(rep.Tests[0].Output, "saved testdata/fuzz/loop_depth/") {
		t.Fatalf("--fuzz loop_depth failed to reach n == 8: %+v", rep.Tests)
	}
	files := corpusFiles(filepath.Join(root, "testdata", "fuzz", "loop_depth"))
	if len(files) == 0 {
		t.Fatal("nothing saved in loop_depth")
	}
	in, err := os.ReadFile(filepath.Join(root, "testdata", "fuzz", "loop_depth", files[0]))
	if err != nil || len(in) < 8 || string(in[:8]) != "AAAAAAAA" {
		t.Errorf("saved loop_depth input = %x (%v), want 8 leading 'A' bytes", in, err)
	}
}

// TestFuzzCoverCmpMagic4And8 verifies that live 64-entry CoverCmp tracking and
// Redqueen/splice mutation solve runtime-computed 4-byte and 8-byte comparisons
// whose values do not appear literally in the fuzz dictionary.
func TestFuzzCoverCmpMagic4And8(t *testing.T) {
	const src = `import std.testing

ReadU32LE(b: []u8, at: int): u32 {
    return u32(b[at]) | (u32(b[at + 1]) << 8) | (u32(b[at + 2]) << 16) | (u32(b[at + 3]) << 24)
}

ReadU64LE(b: []u8, at: int): u64 {
    lo := u64(ReadU32LE(b, at))
    hi := u64(ReadU32LE(b, at + 4))
    return lo | (hi << 32)
}

Check4(b: []u8) {
    if b.Len() >= 4 {
        want := (u32(0x1357) << 16) | u32(0x9BDF)
        if ReadU32LE(b, 0) == want {
            Panic("magic4")
        }
    }
}

Check8(b: []u8) {
    if b.Len() >= 8 {
        lo := (u64(0x2468) << 16) | u64(0xACE0)
        hi := (u64(0x1357) << 16) | u64(0x9BDF)
        want := (hi << 32) | lo
        if ReadU64LE(b, 0) == want {
            Panic("magic8")
        }
    }
}

test "magic4" {
    testing.Fuzz(Check4)
}

test "magic8" {
    testing.Fuzz(Check8)
}
`
	root := testPkg(t, map[string]string{"main.mk": src})
	rep4 := mkTest(t, Config{Root: root, TestTimeout: 2 * time.Minute}, TestOptions{Fuzz: "magic4", FuzzTime: 45 * time.Second})
	if len(rep4.Tests) != 1 || rep4.Tests[0].Result != "fail" || !strings.Contains(rep4.Tests[0].Output, "saved testdata/fuzz/magic4/") {
		t.Fatalf("--fuzz magic4 failed: %+v", rep4.Tests)
	}
	f4 := corpusFiles(filepath.Join(root, "testdata", "fuzz", "magic4"))
	if len(f4) == 0 {
		t.Fatal("nothing saved in magic4")
	}
	in4, err := os.ReadFile(filepath.Join(root, "testdata", "fuzz", "magic4", f4[0]))
	if err != nil || !bytes.Equal(in4[:min(len(in4), 4)], []byte{0xDF, 0x9B, 0x57, 0x13}) {
		t.Errorf("saved magic4 input = %x (%v), want df9b5713...", in4, err)
	}

	rep8 := mkTest(t, Config{Root: root, TestTimeout: 2 * time.Minute}, TestOptions{Fuzz: "magic8", FuzzTime: 45 * time.Second})
	if len(rep8.Tests) != 1 || rep8.Tests[0].Result != "fail" || !strings.Contains(rep8.Tests[0].Output, "saved testdata/fuzz/magic8/") {
		t.Fatalf("--fuzz magic8 failed: %+v", rep8.Tests)
	}
	f8 := corpusFiles(filepath.Join(root, "testdata", "fuzz", "magic8"))
	if len(f8) == 0 {
		t.Fatal("nothing saved in magic8")
	}
	in8, err := os.ReadFile(filepath.Join(root, "testdata", "fuzz", "magic8", f8[0]))
	if err != nil || !bytes.Equal(in8[:min(len(in8), 8)], []byte{0xE0, 0xAC, 0x68, 0x24, 0xDF, 0x9B, 0x57, 0x13}) {
		t.Errorf("saved magic8 input = %x (%v), want e0ac6824df9b5713...", in8, err)
	}
}

// fuzzSysSource has FuzzSys targets: "echo" copies each input into an
// arena off sys.heap and prints its length through sys.io, and "two sys"
// does the same and fails on an input of two bytes or more; "no target"
// takes a System and never calls FuzzSys.
const fuzzSysSource = `import std.testing

Copy(sys: &System, b: []u8): int {
    var a := Arena.New(sys.heap).Must()
    defer a.Free()
    c := a.Slice[u8](b.Len()).Must()
    for i in 0..b.Len() {
        c[i] = b[i]
    }
    var same := 0
    for i in 0..c.Len() {
        if c[i] == b[i] {
            same += 1
        }
    }
    return same
}

Echo(sys: &System, b: []u8) {
    n := Copy(sys, b)
    sys.Print("echo {n}\n", (n: n)) else {
        Panic("echo: print failed")
    }
}

TwoSys(sys: &System, b: []u8) {
    Assert(Copy(sys, b) < 2)
}

test "echo" (sys: &System) {
    testing.FuzzSys(sys, Echo)
}

test "two sys" (sys: &System) {
    testing.FuzzSys(sys, TwoSys)
}

test "no target" (sys: &System) {
    _ = sys
}
`

// TestMkTestFuzzSys: a FuzzSys target gets the test block's System. Plain
// mk test calls f(sys, data) once per corpus file, in name order, and f
// allocates from sys.heap and prints through sys.io; --fuzz runs the loop
// on it, saves the input that fails, and plain mk test then fails on it;
// a block that takes a System but never calls FuzzSys is not a fuzz
// target. The failure it guards against: testing.Fuzz takes fn([]u8), so
// a target cannot reach the System (MKH-564), and a FuzzSys whose plain
// run or loop does not call f, or calls it on other bytes, still passes.
func TestMkTestFuzzSys(t *testing.T) {
	root := testPkg(t, map[string]string{"main.mk": fuzzSysSource})
	writeFiles(t, root, map[string]string{
		"testdata/fuzz/echo/a": "abc",
		"testdata/fuzz/echo/b": "hello",
	})
	rep := mkTest(t, Config{Root: root}, TestOptions{Run: "echo"})
	if got := results(rep); got != "main echo pass" {
		t.Fatalf("mk test: %s", got)
	}
	out := rep.Tests[0].Output
	ia, ib := strings.Index(out, "fuzz input a\n"), strings.Index(out, "fuzz input b\n")
	e3, e5 := strings.Index(out, "echo 3\n"), strings.Index(out, "echo 5\n")
	if ia < 0 || ib < ia || e3 < 0 || e5 < e3 || strings.Count(out, "echo ") != 2 {
		t.Errorf("echo's output (want the inputs a, b in order, then echo 3 and echo 5):\n%s", out)
	}
	rep = mkTest(t, Config{Root: root}, TestOptions{Fuzz: "two sys", FuzzTime: 30 * time.Second})
	if len(rep.Tests) != 1 || rep.Tests[0].Result != "fail" || !strings.Contains(rep.Tests[0].Detail, "saved to testdata/fuzz/two sys/") {
		t.Fatalf("--fuzz two sys: %+v", rep.Tests)
	}
	dir := filepath.Join(root, "testdata", "fuzz", "two sys")
	files := corpusFiles(dir)
	if len(files) == 0 {
		t.Fatalf("nothing saved in %s", dir)
	}
	if in, err := os.ReadFile(filepath.Join(dir, files[0])); err != nil || len(in) < 2 {
		t.Errorf("saved input %q (%v): want 2 bytes or more", in, err)
	}
	if report, err := os.ReadFile(filepath.Join(dir, files[0]+".report")); err != nil || !strings.Contains(string(report), "result: panic\nstatus: 101\n") {
		t.Errorf("report: %s (%v)", report, err)
	}
	rep = mkTest(t, Config{Root: root}, TestOptions{Run: "two sys"})
	if got := results(rep); got != "main two sys panic" {
		t.Errorf("plain mk test over the saved input: %s", got)
	}
	rep = mkTest(t, Config{Root: root}, TestOptions{Fuzz: "no target", FuzzTime: 5 * time.Second})
	if len(rep.Tests) != 1 || rep.Tests[0].Result != "fail" || !strings.Contains(rep.Tests[0].Detail, "not a fuzz target") {
		t.Errorf("--fuzz no target: %+v", rep.Tests)
	}
}
