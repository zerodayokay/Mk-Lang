// Why: mk test --prop generates inputs for every contract and shrinks the
// failures it finds; its generation, shrinking, corpus and seeds must behave
// the same on every run.
// What: tests of mk test --prop over the cookbook's and the reference's
// examples, planted holes, generics, slices, structs, enums and overlays,
// and of mk test --sim SEED end to end over std.sim.
// How: propTest runs the tester over small file sets and the tests read
// the TestReport's cases; the sim test runs one seed twice and compares the
// output bytes.

package driver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// propTest runs mk test --prop over files and returns the report and its
// text.
func propTest(t *testing.T, files map[string]string, cfg Config, opt TestOptions) (*TestReport, string) {
	t.Helper()
	if cfg.Root == "" {
		dir := testPkg(t, files)
		cfg.Root = filepath.Join(dir, "t.mk")
	}
	if cfg.CacheDir == "" {
		cfg.CacheDir = t.TempDir()
	}
	s, err := NewSession(cfg)
	if err != nil {
		t.Fatal(err)
	}
	opt.Prop = true
	res, rep, err := s.Test(opt)
	if err != nil {
		t.Fatal(err)
	}
	if rep == nil {
		var b bytes.Buffer
		_ = res.WriteReport(&b, false)
		t.Fatalf("no report:\n%s", b.String())
	}
	var b bytes.Buffer
	if err := rep.WriteText(&b, false); err != nil {
		t.Fatal(err)
	}
	return rep, b.String()
}

// propCaseOf is the report's TestCase for the contract display name.
func propCaseOf(t *testing.T, rep *TestReport, display string) TestCase {
	t.Helper()
	for _, tc := range rep.Tests {
		if tc.Name == "prop "+display {
			return tc
		}
	}
	t.Fatalf("no report for %s in %+v", display, rep.Tests)
	return TestCase{}
}

// The cookbook's Clamp example.
const propClamp = `Clamp(x: i32, lo: i32, hi: i32): i32
    requires lo <= hi
    ensures result >= lo && result <= hi
{
    if x < lo {
        return lo
    }
    if x > hi {
        return hi
    }
    return x
}

test "clamp" {
    Assert(Clamp(5, 0, 3) == 3)
}
`

// TestPropCookbookClamp: the cookbook's Clamp passes 100 cases, with the
// inputs where lo > hi discarded by requires.
func TestPropCookbookClamp(t *testing.T) {
	rep, text := propTest(t, map[string]string{"t.mk": propClamp}, Config{}, TestOptions{})
	tc := propCaseOf(t, rep, "Clamp")
	if tc.Result != "pass" || rep.ExitStatus() != 0 || len(rep.Tests) != 1 {
		t.Fatalf("Clamp: %s", text)
	}
	var valid, discarded int
	if _, err := fmt.Sscanf(tc.Output, "%d cases, %d discarded", &valid, &discarded); err != nil || valid < 100 || discarded == 0 {
		t.Errorf("Clamp's counts: %q", tc.Output)
	}
	if !strings.HasSuffix(text, fmt.Sprintf("1 passed, 0 failed (prop seed %d)\n", PropDefaultSeed)) {
		t.Errorf("text: %q", text)
	}
}

// A planted hole: Clamp returns x above hi.
const propClampHole = `Clamp(x: i32, lo: i32, hi: i32): i32
    requires lo <= hi
    ensures result >= lo && result <= hi
{
    if x < lo {
        return lo
    }
    if x > hi {
        return x
    }
    return x
}
`

// TestPropPlantedHole: the tester finds the hole, shrinks it to the
// smallest input (x one above hi, everything at 0 or 1), reports ensures
// and the clause, exits 1, and keeps the input in the corpus, which the
// next run evaluates first.
func TestPropPlantedHole(t *testing.T) {
	dir := testPkg(t, map[string]string{"t.mk": propClampHole})
	cache := t.TempDir()
	cfg := Config{Root: filepath.Join(dir, "t.mk"), CacheDir: cache}
	rep, text := propTest(t, nil, cfg, TestOptions{})
	tc := propCaseOf(t, rep, "Clamp")
	if tc.Result != "fail" || tc.Detail != "ensures" || rep.ExitStatus() != 1 {
		t.Fatalf("hole not found: %s", text)
	}
	if !strings.HasPrefix(tc.Output, "Clamp(1, 0, 0)  at ") || !strings.Contains(tc.Output, "t.mk:9:9\n") || !strings.Contains(tc.Output, "clause: ensures result >= lo && result <= hi") {
		t.Errorf("not shrunk to Clamp(1, 0, 0): %s", text)
	}
	p := filepath.Join(cache, "prop", "main", "Clamp.json")
	raw, err := os.ReadFile(p)
	if err != nil || strings.Count(string(raw), "\n") != 1 {
		t.Fatalf("corpus: %q %v", raw, err)
	}
	var e propCorpusEntry
	if err := json.Unmarshal(bytes.TrimSpace(raw), &e); err != nil || len(e.Args) != 3 {
		t.Fatalf("corpus entry: %s %v", raw, err)
	}
	rep, text = propTest(t, nil, cfg, TestOptions{})
	tc = propCaseOf(t, rep, "Clamp")
	if tc.Result != "fail" || !strings.Contains(tc.Output, "from the corpus") || !strings.Contains(tc.Output, "Clamp(1, 0, 0)") {
		t.Errorf("second run did not replay the corpus: %s", text)
	}
	if raw2, _ := os.ReadFile(p); !bytes.Equal(raw, raw2) {
		t.Errorf("corpus grew on a replay: %q then %q", raw, raw2)
	}
}

// The reference's Span example and a planted overflow only the limits reach.
const propSpan = `struct Span {
    lo: int
    hi: int
    invariant lo <= hi
}

Span.Len(&self): int
    ensures result >= 0
{
    return self.hi - self.lo
}

SafeDiv(a: i32, b: i32): i32
    requires b != 0
{
    return a / b
}

Mid(lo: i32, hi: i32): i32
    requires lo <= hi
    ensures result >= lo && result <= hi
{
    return (lo + hi) / 2
}

MidWide(lo: i32, hi: i32): i32
    requires lo <= hi
    ensures result >= lo && result <= hi
{
    return Cast[i32]((Cast[i64](lo) + Cast[i64](hi)) / 2)
}
`

// TestPropReferenceSpan: the tester runs the reference's contracts. Its
// Span.Len overflows when hi - lo exceeds int (found at the limits and
// shrunk to one step past them), SafeDiv panics on MIN / -1, Mid
// overflows (both shrunk to the smallest such pair), and the
// midpoint computed in i64 passes.
func TestPropReferenceSpan(t *testing.T) {
	rep, text := propTest(t, map[string]string{"t.mk": propSpan}, Config{}, TestOptions{})
	t.Log(text)
	if tc := propCaseOf(t, rep, "Span.Len"); tc.Result != "fail" || tc.Detail != "panic: overflow" ||
		!(strings.Contains(tc.Output, "Span(lo: -1, hi: 9223372036854775807).Len()") || strings.Contains(tc.Output, "Span(lo: -9223372036854775808, hi: 0).Len()")) {
		t.Errorf("Span.Len: %s", text)
	}
	if tc := propCaseOf(t, rep, "SafeDiv"); tc.Result != "fail" || !strings.HasPrefix(tc.Detail, "panic: ") ||
		!strings.Contains(tc.Output, "SafeDiv(-2147483648, -1)") {
		t.Errorf("SafeDiv: %s", text)
	}
	if tc := propCaseOf(t, rep, "Mid"); tc.Result != "fail" || tc.Detail != "panic: overflow" ||
		!(strings.Contains(tc.Output, "Mid(1, 2147483647)") || strings.Contains(tc.Output, "Mid(-2147483648, -1)")) {
		t.Errorf("Mid: %s", text)
	}
	if tc := propCaseOf(t, rep, "MidWide"); tc.Result != "pass" {
		t.Errorf("MidWide: %s", text)
	}
	if rep.Failed != 3 || rep.Passed != 1 {
		t.Errorf("counts: %s", text)
	}
}

// TestPropShapes: generics over the small set (a bound drops the instances
// that do not meet it), slices and strings of varying length, struct
// arguments field by field with invariant-breaking ones discarded, a mut
// self method's invariant, an Io parameter skipped with its name, and the
// JSON form.
func TestPropShapes(t *testing.T) {
	const src = `struct Span {
    lo: int
    hi: int
    invariant lo <= hi
}

Span.Widen(mut self, by: u8) {
    if self.hi <= 1000 {
        self.hi += Cast[int](by)
    }
}

Span.Swap(mut self) {
    t := self.lo
    self.lo = self.hi
    self.hi = t
}

Id[T](x: T): T
    ensures true
{
    return x
}

Neg[T: Ord](x: T): T
    ensures true
{
    return x
}

Total(xs: []u8): int
    ensures result >= 0 && result <= xs.Len() * 255
{
    var s := 0
    for x in xs {
        s += Cast[int](x)
    }
    return s
}

Bytes(s: str): int
    ensures result == s.Len()
{
    return s.AsBytes().Len()
}

Short(s: str): int
    ensures result < 4
{
    return s.Len()
}

Both(a: Span, b: ?Span): int
    ensures result >= 0
{
    return 0
}

Uses(io: Io): int
    ensures result == 0
{
    return 0
}
`
	rep, text := propTest(t, map[string]string{"t.mk": src}, Config{}, TestOptions{PropCases: 30})
	t.Log(text)
	want := map[string]string{"Span.Widen": "pass", "Span.Swap": "fail", "Id": "pass", "Neg": "pass", "Total": "pass", "Bytes": "pass", "Short": "fail", "Both": "pass", "Uses": "skip"}
	for name, res := range want {
		if tc := propCaseOf(t, rep, name); tc.Result != res {
			t.Errorf("%s: %s, want %s\n%s", name, tc.Result, res, text)
		}
	}
	if tc := propCaseOf(t, rep, "Id"); !strings.Contains(tc.Output, "instances int, i32, u8, bool, str") {
		t.Errorf("Id's instances: %q", tc.Output)
	}
	// A T: Ord bound drops the instance that does not meet it (bool).
	if tc := propCaseOf(t, rep, "Neg"); !strings.Contains(tc.Output, "; instances int, i32, u8, str") || strings.Contains(tc.Output, "bool") {
		t.Errorf("Neg's instances (bool must be dropped by T: Ord): %q", tc.Output)
	}
	if tc := propCaseOf(t, rep, "Span.Swap"); tc.Detail != "invariant" || !strings.Contains(tc.Output, "Span(lo: 0, hi: 1).Swap()") {
		t.Errorf("Span.Swap: %s", text)
	}
	if tc := propCaseOf(t, rep, "Short"); tc.Detail != "ensures" || !(strings.Contains(tc.Output, `Short("aaaa")`) || strings.Contains(tc.Output, `Short("\u{1f600}")`) ||
		strings.Contains(tc.Output, `Short("\u{4e2d}a")`) || strings.Contains(tc.Output, `Short("a\u{4e2d}")`)) {
		t.Errorf("Short not shrunk to four bytes: %s", text)
	}
	if tc := propCaseOf(t, rep, "Uses"); !strings.Contains(tc.Detail, "`io`") {
		t.Errorf("Uses: %q", tc.Detail)
	}
	if tc := propCaseOf(t, rep, "Both"); !strings.Contains(tc.Output, "discarded") || strings.Contains(tc.Output, " 0 discarded") {
		t.Errorf("Both: no struct was discarded by its invariant: %q", tc.Output)
	}
	var b bytes.Buffer
	if err := rep.WriteJSON(&b); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Tests []struct {
			Name, Result, Detail string
		} `json:"tests"`
		Skipped  int     `json:"skipped"`
		PropSeed *uint64 `json:"prop_seed"`
	}
	if err := json.Unmarshal(b.Bytes(), &doc); err != nil || doc.Skipped != 1 || doc.PropSeed == nil || len(doc.Tests) != len(rep.Tests) {
		t.Errorf("json: %s %v", b.String(), err)
	}
	if !strings.Contains(text, "SKIP main \"prop Uses\": ") || !strings.Contains(text, ", 1 skipped (prop seed") {
		t.Errorf("text: %s", text)
	}
}

// TestPropSeedAndRun: --run selects contracts by name, and --sim SEED
// seeds the generator (the report says which seed ran).
func TestPropSeedAndRun(t *testing.T) {
	rep, text := propTest(t, map[string]string{"t.mk": propSpan}, Config{}, TestOptions{Run: "MidWide", Sim: 7, SimSet: true, PropCases: 20})
	if len(rep.Tests) != 1 || rep.Tests[0].Name != "prop MidWide" || rep.PropSeed == nil || *rep.PropSeed != 7 {
		t.Errorf("--run MidWide --sim 7: %s", text)
	}
}

// TestPropOverlay: Config.Overlay adds a file to the module in package,
// directory and single-file mode, and replaces a file on disk, without
// writing anything.
func TestPropOverlay(t *testing.T) {
	dir := testPkg(t, map[string]string{"t.mk": "export Main(sys: &System) {}\n\nconst A = 1\n"})
	ov := map[string][]byte{"o.mk": []byte("const B = A + Missing\n")}
	for _, root := range []string{dir, filepath.Join(dir, "t.mk")} {
		s, err := NewSession(Config{Root: root, CacheDir: t.TempDir(), Overlay: ov})
		if err != nil {
			t.Fatal(err)
		}
		r := s.Check()
		if len(r.Report.Diags) != 1 || r.Report.Diags[0].Diag.Code.String() != "E0201" {
			var b bytes.Buffer
			_ = r.WriteReport(&b, false)
			t.Errorf("%s: want the overlay's E0201:\n%s", root, b.String())
		}
	}
	s, err := NewSession(Config{Root: dir, CacheDir: t.TempDir(), Overlay: map[string][]byte{"t.mk": []byte("export Main(sys: &System) {}\n")}})
	if err != nil {
		t.Fatal(err)
	}
	if r := s.Check(); len(r.Report.Diags) != 0 {
		t.Errorf("replacing t.mk: %v", r.Report.Diags)
	}
	if _, err := os.Stat(filepath.Join(dir, "o.mk")); err == nil {
		t.Error("the overlay reached the disk")
	}
}

// A test over std.sim that takes its seed from mk test --sim SEED (the
// child's MK_SIM_SEED: sim.Sim.New(sys.arena, seed), then s.Io()) and prints
// what 400 seeded crash runs left behind.
const propSimSource = `import std.fs
import std.sim

Put(io: Io, key: str, value: str): fs.Error!void {
    var f := try fs.Create(io, key)
    onerr f.Abandon()
    try f.WriteAll(io, value.AsBytes())
    try f.Sync(io)
    try f.Close(io)
}

test "crash runs under the seed" (sys: &System) {
    seed := try (sys.Env("MK_SIM_SEED") else { "0" }).ParseInt()
    var out := sys.Stdout()
    var whole := 0
    for k in 0..400 {
        var s := try sim.Sim.New(sys.arena, Cast[u64](seed) * 1000 + Cast[u64](k))
        defer s.Free()
        s.CrashAfterRandomOps()
        Put(s.Io(), "key", "value") else {
        }
        io := s.Restart()
        text := fs.ReadText(sys.arena, io, "key") else {
            try Print(mut out, "{k}: absent\n", (k))
            continue
        }
        Assert(text == "value" || text == "")
        if text == "value" {
            whole += 1
        } else {
            try Print(mut out, "{k}: empty\n", (k))
        }
    }
    try Print(mut out, "{whole} whole\n", (whole))
}
`

// TestSimE2ESeedReplays: mk test --sim SEED end to end over sim.Sim. The
// same seed gives byte-identical output twice, another seed gives another
// run, and the report carries the seed.
func TestSimE2ESeedReplays(t *testing.T) {
	t.Setenv(SimSeedEnv, "")
	dir := testPkg(t, map[string]string{"t.mk": propSimSource})
	cfg := Config{Root: filepath.Join(dir, "t.mk")}
	run := func(seed uint64) string {
		t.Helper()
		rep := mkTest(t, cfg, TestOptions{Sim: seed, SimSet: true})
		if got := results(rep); got != "main crash runs under the seed pass" || rep.SimSeed == nil || *rep.SimSeed != seed {
			t.Fatalf("--sim %d: %s\n%+v", seed, got, rep.Tests)
		}
		return rep.Tests[0].Output
	}
	a, b, c := run(7), run(7), run(8)
	if a != b {
		t.Errorf("--sim 7 twice differs:\n%s\n---\n%s", a, b)
	}
	if !strings.HasSuffix(a, " whole\n") || !strings.Contains(c, ": ") {
		t.Errorf("--sim output: %q, %q", a, c)
	}
	if a == c {
		t.Errorf("--sim 7 and --sim 8 gave the same run:\n%s", a)
	}
	t.Logf("--sim 7:\n%s--sim 8:\n%s", a, c)
}

// TestPropStaleCorpus: a corpus entry that no longer checks (the contract's
// parameters changed) is dropped, and the contract still runs its cases,
// never a skip.
func TestPropStaleCorpus(t *testing.T) {
	dir := testPkg(t, map[string]string{"t.mk": propClamp})
	cache := t.TempDir()
	p := filepath.Join(cache, "prop", "main", "Clamp.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	stale := `{"inst":"","args":[{"k":"S","s":"Nope","n":["x"],"e":[{"k":"i","i":1,"bits":64}]},{"k":"i","bits":32},{"k":"i","bits":32}]}` + "\n"
	if err := os.WriteFile(p, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, text := propTest(t, nil, Config{Root: filepath.Join(dir, "t.mk"), CacheDir: cache}, TestOptions{})
	if tc := propCaseOf(t, rep, "Clamp"); tc.Result != "pass" {
		t.Errorf("Clamp with a stale corpus: %s", text)
	}
	if _, err := os.Stat(p); err == nil {
		raw, _ := os.ReadFile(p)
		t.Errorf("the stale entry was kept: %q", raw)
	}
}

// TestPropDirectoryRoot: a directory of contracts with no Main, as mk test
// takes it.
func TestPropDirectoryRoot(t *testing.T) {
	dir := testPkg(t, map[string]string{"clamp.mk": propClamp})
	rep, text := propTest(t, nil, Config{Root: dir}, TestOptions{PropCases: 20})
	if tc := propCaseOf(t, rep, "Clamp"); tc.Result != "pass" {
		t.Errorf("directory root: %s", text)
	}
}

// TestPropEdgesAfterNonInt: the edge enumeration counts digits over
// the integer parameters only, so a str parameter or a struct receiver
// before them does not push them out of the cross product: MIN / -1 is met
// within the first cases under every seed, not by chance. The seeds share
// one cache, so only the first compiles std; the prop corpus the cache
// keeps (a seed's shrunk finding, replayed first by the next run) is
// removed before each seed, so every seed finds MIN / -1 by itself.
func TestPropEdgesAfterNonInt(t *testing.T) {
	const src = `struct Pt {
    x: int
}

Pt.Div(&self, a: i32, b: i32): i32
    requires b != 0
{
    return a / b
}

Div3(s: str, a: i32, b: i32): i32
    requires b != 0
{
    return a / b
}
`
	dir := testPkg(t, map[string]string{"t.mk": src})
	cacheDir := t.TempDir()
	for seed := uint64(1); seed <= 10; seed++ {
		if err := os.RemoveAll(filepath.Join(cacheDir, "prop")); err != nil {
			t.Fatal(err)
		}
		rep, text := propTest(t, nil, Config{Root: filepath.Join(dir, "t.mk"), CacheDir: cacheDir}, TestOptions{Sim: seed, SimSet: true, PropCases: 20})
		if strings.Contains(text, "from the corpus") {
			t.Errorf("--sim %d: a finding came from the corpus, not the generator: %s", seed, text)
		}
		if tc := propCaseOf(t, rep, "Div3"); tc.Result != "fail" || !strings.HasPrefix(tc.Output, `Div3("", -2147483648, -1)  at `) {
			t.Errorf("--sim %d: Div3 not found and shrunk to (\"\", MIN, -1): %s", seed, text)
		}
		if tc := propCaseOf(t, rep, "Pt.Div"); tc.Result != "fail" || !strings.Contains(tc.Output, ".Div(-2147483648, -1)  at ") {
			t.Errorf("--sim %d: Pt.Div not found at (MIN, -1): %s", seed, text)
		}
	}
}

// TestPropEnumPayload: an enum parameter is generated over all its
// variants, payload ones included, so a hole that sits only in a payload
// variant is a finding, shrunk field by field, never a pass over the
// payload-free variants alone.
func TestPropEnumPayload(t *testing.T) {
	const src = `enum Shape {
    Dot
    Line(len: int)
}

Area(s: Shape): int
    ensures result >= 0
{
    match s {
        .Dot {
            return 0
        }
        .Line(len) {
            return len
        }
    }
}
`
	rep, text := propTest(t, map[string]string{"t.mk": src}, Config{}, TestOptions{PropCases: 30})
	if tc := propCaseOf(t, rep, "Area"); tc.Result != "fail" || tc.Detail != "ensures" || !strings.HasPrefix(tc.Output, "Area(Shape.Line(len: -1))  at ") {
		t.Errorf("Area: want a fail shrunk to Area(Shape.Line(len: -1)): %s", text)
	}
}

// TestPropNotJudgedDetail: a contract whose every case ran out of
// fuel is a skip that names fuel, not requires, which rejected nothing.
func TestPropNotJudgedDetail(t *testing.T) {
	const src = `Spin(b: bool): int
    ensures result >= 0
{
    var i := 0
    for i < 1_000_000_000 {
        i += 1
    }
    return i
}
`
	rep, text := propTest(t, map[string]string{"t.mk": src}, Config{}, TestOptions{PropCases: 5})
	tc := propCaseOf(t, rep, "Spin")
	if tc.Result != "skip" || !strings.Contains(tc.Detail, "fuel") || !strings.Contains(tc.Detail, "E0902") || strings.Contains(tc.Detail, "requires") {
		t.Errorf("Spin: want a skip naming fuel, got %q / %q\n%s", tc.Result, tc.Detail, text)
	}
}

// TestPropShrinkBudget: the shrink budget is 64 candidates per round and 40
// rounds per finding, and a finding that cannot converge (a float halving
// from 1e300) stops at 40 rounds and says so.
func TestPropShrinkBudget(t *testing.T) {
	if propShrinkWidth != 64 || propShrinkMax != 40 {
		t.Fatalf("shrink budget %d candidates, %d rounds; the note says 64, 40", propShrinkWidth, propShrinkMax)
	}
	const src = `Big(x: f64): f64
    requires x > 1000000.0
    ensures result < 1.0
{
    return x
}
`
	rep, text := propTest(t, map[string]string{"t.mk": src}, Config{}, TestOptions{PropCases: 5})
	if tc := propCaseOf(t, rep, "Big"); tc.Result != "fail" || !strings.Contains(tc.Output, "shrunk in 40 rounds (stopped at the limit)") {
		t.Errorf("Big: want a finding stopped at 40 rounds: %s", text)
	}
}

// TestPropCorpusCaseFold: the corpus file is the bare display name, which a
// case-insensitive filesystem would fold, but two contracts whose names
// differ only in case cannot exist: the check rejects the second as a
// duplicate (E0206), so --prop never runs them.
func TestPropCorpusCaseFold(t *testing.T) {
	dir := testPkg(t, map[string]string{"t.mk": propClampHole + `
clamp(s: str, n: int, m: int): int
    requires n <= m
    ensures result == n
{
    return n
}
`})
	s, err := NewSession(Config{Root: filepath.Join(dir, "t.mk"), CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	res, rep, err := s.Test(TestOptions{Prop: true})
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	_ = res.WriteReport(&b, false)
	if rep != nil || !strings.Contains(b.String(), "E0206") {
		t.Errorf("Clamp and clamp in one module: want E0206 and no prop run:\n%s", b.String())
	}
}

// TestPropIEEE754EdgesAndShrink checks that f64 and f32 IEEE-754 edge values
// (canonical NaN, non-canonical NaN payloads, -0.0, +Inf, -Inf, subnormals) are
// discovered deterministically, rendered cleanly, shrunk without looping, and
// persisted/replayed through the JSON corpus.
func TestPropIEEE754EdgesAndShrink(t *testing.T) {
	const src = `FailNaN(x: f64): bool
    ensures result
{
    return x == x
}

FailPayloadNaN(x: f64): bool
    ensures result
{
    return x.Bits() != 0x7FF8000000001234
}

FailNegZero(x: f64): bool
    ensures result
{
    return !(x == 0.0 && x.Bits() != 0)
}

FailPosInf(x: f64): bool
    ensures result
{
    return x.Bits() != 0x7FF0000000000000
}

FailNegInf(x: f64): bool
    ensures result
{
    return x.Bits() != 0xFFF0000000000000
}

FailSubnormal64(x: f64): bool
    ensures result
{
    return !(x > 0.0 && x < f64.FromBits(0x0010000000000000))
}

FailSubnormal32(x: f32): bool
    ensures result
{
    return !(x > 0.0 && x < f32.FromBits(0x00800000))
}

FailNaN32(x: f32): bool
    ensures result
{
    return x == x
}
`
	dir := testPkg(t, map[string]string{"t.mk": src})
	cache := t.TempDir()
	cfg := Config{Root: filepath.Join(dir, "t.mk"), CacheDir: cache}
	rep, text := propTest(t, nil, cfg, TestOptions{PropCases: 30})
	if rep.Failed != 8 || rep.Passed != 0 {
		t.Fatalf("want all 8 IEEE-754 traps to fail, got:\n%s", text)
	}
	for _, tc := range rep.Tests {
		if tc.Result != "fail" {
			t.Errorf("%s: want fail, got %s", tc.Name, tc.Result)
		}
		if strings.Contains(tc.Output, "stopped at the limit") {
			t.Errorf("%s: float shrinking did not terminate:\n%s", tc.Name, tc.Output)
		}
		if strings.Contains(tc.Name, "NaN") && !strings.Contains(tc.Output, "FromBits(") {
			t.Errorf("%s: expected FromBits rendering in output:\n%s", tc.Name, tc.Output)
		}
	}
	if tc := propCaseOf(t, rep, "FailNaN"); !strings.Contains(tc.Output, "f64.FromBits(0x7FF8000000000000)") {
		t.Errorf("FailNaN should shrink to canonical qNaN:\n%s", tc.Output)
	}
	if tc := propCaseOf(t, rep, "FailNegZero"); !strings.Contains(tc.Output, "f64.FromBits(0x8000000000000000)") {
		t.Errorf("FailNegZero should preserve -0.0:\n%s", tc.Output)
	}
	if tc := propCaseOf(t, rep, "FailPosInf"); !strings.Contains(tc.Output, "f64.FromBits(0x7FF0000000000000)") {
		t.Errorf("FailPosInf should render +Inf via FromBits:\n%s", tc.Output)
	}
	if tc := propCaseOf(t, rep, "FailNegInf"); !strings.Contains(tc.Output, "f64.FromBits(0xFFF0000000000000)") {
		t.Errorf("FailNegInf should render -Inf via FromBits:\n%s", tc.Output)
	}
	// Corpus replay must succeed for all 8 JSON entries (including NaN and ±Inf).
	rep2, text2 := propTest(t, nil, cfg, TestOptions{PropCases: 30})
	if rep2.Failed != 8 {
		t.Fatalf("corpus replay failed:\n%s", text2)
	}
	for _, name := range []string{"FailNaN", "FailPayloadNaN", "FailNegZero", "FailPosInf", "FailNegInf", "FailSubnormal64", "FailSubnormal32", "FailNaN32"} {
		if tc := propCaseOf(t, rep2, name); !strings.Contains(tc.Output, "from the corpus") {
			t.Errorf("%s: second run did not replay from corpus:\n%s", name, tc.Output)
		}
	}
}

// TestPropCasesEightMixesRandomAndCompletes verifies that --prop-cases 8 caps
// edge-case enumeration so random inputs still run within small budgets and a
// single-integer property still runs all requested cases without stalling at 7.
func TestPropCasesEightMixesRandomAndCompletes(t *testing.T) {
	passSrc := `SingleInt(x: i32): bool
    ensures result
{
    return x == x
}

TwoInts(x: i32, y: i32): bool
    ensures result
{
    return (x +% y) == (y +% x)
}
`
	rep, text := propTest(t, map[string]string{"t.mk": passSrc}, Config{}, TestOptions{PropCases: 8})
	if rep.Failed != 0 || rep.Passed != 2 {
		t.Fatalf("want both passing properties at PropCases=8:\n%s", text)
	}
	var singleValid int
	if tc := propCaseOf(t, rep, "SingleInt"); func() bool {
		_, err := fmt.Sscanf(tc.Output, "%d cases", &singleValid)
		return err != nil || singleValid < 8
	}() {
		t.Fatalf("SingleInt should complete >= 8 cases (not stall after 7 edges): %q", propCaseOf(t, rep, "SingleInt").Output)
	}

	failSrc := `OnlyEdges(x: i32, y: i32): bool
    ensures result
{
    _ = y
    return x == 0 || x == 1 || x == -1 || x == 2147483647 || x == -2147483648 || x == 2147483646 || x == -2147483647
}
`
	repFail, textFail := propTest(t, map[string]string{"t.mk": failSrc}, Config{}, TestOptions{PropCases: 8})
	if repFail.Failed != 1 {
		t.Fatalf("want OnlyEdges to fail via random draw within --prop-cases 8:\n%s", textFail)
	}
}
