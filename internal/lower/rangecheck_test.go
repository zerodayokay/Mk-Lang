// Why: the range analysis removes overflow and bounds checks it proves can
// never fire; a wrong fact removes a check that was needed, so every fact
// source is tested both ways.
// What: TestPC_Facts (one proving and one non-proving program per fact
// source), TestPC_BenchKernels (removed-check counts per tests/bench
// kernel), TestPC_LowerKeepsCFG, TestPC_MutationCaught, and the pcBuild,
// pcLower and pcRun helpers the other range tests share.
// How: programs are lowered with the pass on and off and their checks
// counted; the mutation test plants a wrong fact (a rangeMut of
// rangecheck.go) and requires eval, the reference semantics, to catch it.

package lower

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"mk/internal/check"
	"mk/internal/diag"
	"mk/internal/eval"
	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

// pcFormat adds the Format impls the corpus prelude lacks for the kernels'
// Print of an i64, int, u64 or f64.
const pcFormat = "impl Format for i64 {\n\tFormat(&self, w: mut Writer): WriteError!void {\n\t}\n}\n" +
	"impl Format for int {\n\tFormat(&self, w: mut Writer): WriteError!void {\n\t}\n}\n" +
	"impl Format for u64 {\n\tFormat(&self, w: mut Writer): WriteError!void {\n\t}\n}\n" +
	"impl Format for f64 {\n\tFormat(&self, w: mut Writer): WriteError!void {\n\t}\n}\n"

// pcOpt is how pcLower lowers: a bin module (the kernels), the bounds fold
// a driver runs after borrow (FoldProvedBounds), and a Config tweak.
type pcOpt struct {
	bin, fold bool
	// proved folds as the driver does (LowerProved, ProvedBounds.Fold)
	// instead of Lower and FoldProvedBounds.
	proved bool
	tweak  func(*Config)
	// version runs VersionLoops on every folded body, as
	// the driver does on the instance bodies it hands codegen.
	version *VersionOptions
	// inline runs InlineLeaves on every body after all
	// are lowered, before the versions, as the driver does on the instance
	// bodies it hands codegen; the leaves are the bodies as lowered.
	inline bool
}

// pcStubs are the std modules the bench kernels import beyond the corpus
// harness's stdStubs, declared as far as the kernels use them.
var pcStubs = map[string]string{
	"std.fs": stdStubs["std.fs"] +
		"export ReadBytes(a: &Arena, io: Io, p: str): Error|OutOfMemory![]u8 from a\n",
	"std.math": "export Sqrt(x: f64): f64\n",
	"std.atomic": "export struct AtomicInt {\n\tv: i64\n}\nunsafe impl Share for AtomicInt {\n}\n" +
		"export AtomicInt.New(n: i64): AtomicInt {\n\treturn AtomicInt(v: n)\n}\n" +
		"export AtomicInt.Add(&self, n: i64): i64\nexport AtomicInt.Load(&self): i64\n",
	// Rule 11's bit counts, as std/raw/raw.mk declares them (bodiless in a
	// std module: intrinsics, which eval runs by key).
	"std.raw": "export Ctz64(x: u64): int\nexport Clz64(x: u64): int\nexport Popcount64(x: u64): int\n",
	// std.simd is pcSimdStub's: check's declaration file, read on demand.
	"std.simd": "",
}

// pcSimdStub is std.simd for the programs that import it: the declaration
// file internal/check/testdata/simd/vec.mk (simd_test.go's), whose
// operators lower to calls of the intrinsics eval implements.
func pcSimdStub(t testing.TB) string {
	t.Helper()
	vec, err := os.ReadFile(filepath.Join("..", "check", "testdata", "simd", "vec.mk"))
	if err != nil {
		t.Fatal(err)
	}
	return string(vec)
}

// pcBuild builds module m over the corpus prelude (which has
// Arena.Slice) as the corpus harness does (corpusWorld), with the
// kernels' stubs, and lowers every unit; a source check rejects is the
// reason, a lower ICE fails t.
func pcBuild(t testing.TB, src string, o pcOpt) (*lowered, string) {
	t.Helper()
	w := newWorldCore(t, true, corpusPrelude(t)+pcFormat)
	var paths []string
	for p := range pcStubs {
		paths = append(paths, p)
	}
	for _, p := range stubPaths() {
		if _, ok := pcStubs[p]; !ok {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	for _, p := range paths {
		if strings.Contains(src, "import "+p+"\n") {
			stub, ok := pcStubs[p]
			if !ok {
				stub = stdStubs[p]
			}
			if p == "std.simd" {
				// Naming a lane-count intrinsic judges its requires clause;
				// with no mono here, check gets simd_test.go's
				// laneClauseHolds, as BenchmarkEvalVecOps does.
				stub = pcSimdStub(t)
				w.cfg.Eval = laneClauseHolds{w.consts}
			}
			w.add(p, opt{std: true, unsafe: p == "std.atomic"}, stub)
		}
	}
	b := w.add("m", opt{parseErrors: true, bin: o.bin}, src)
	for _, d := range b.diags() {
		if d.Code != diag.E0208 {
			return nil, "not built: " + whyUnbuilt(w, b)
		}
	}
	cfg := w.lowerCfg()
	if o.tweak != nil {
		o.tweak(cfg)
	}
	out := &lowered{w: w, b: b, bodies: map[string]*mir.Body{}}
	for _, id := range b.m.Units() {
		it := w.items.Get(id)
		var body *check.Body
		if ie := source.Guard(it.Key.String(), it.Span, func() { body = w.c.Check(b.m, id) }); ie != nil || body == nil {
			return nil, fmt.Sprintf("check ICE in %s: %v", it.Key, ie)
		}
		if len(body.Diags) > 0 {
			return nil, fmt.Sprintf("%s: check diagnostics:\n%s", it.Key, dump(w, body.Diags))
		}
		var mb *mir.Body
		if ie := source.Guard(it.Key.String(), it.Span, func() {
			if o.proved {
				var pb ProvedBounds
				mb, pb = LowerProved(cfg, FromCheck(cfg, w.c, body))
				pb.Fold(mb)
				return
			}
			mb = Lower(cfg, FromCheck(cfg, w.c, body))
			if o.fold {
				FoldProvedBounds(cfg, mb) // a goto to an existing block: still valid
			}
			if o.version != nil && !o.inline {
				mb = pvVersion(t, out, cfg, mb, *o.version)
			}
		}); ie != nil {
			t.Fatalf("lower ICE in %s: %v\n%s", it.Key, ie, src)
		}
		k := b.key(id)
		out.bodies[k] = mb
		out.keys = append(out.keys, k)
	}
	if o.inline {
		piInline(t, out, cfg, o.version)
	}
	return out, ""
}

// pcLower is pcBuild for a source that must build.
func pcLower(t *testing.T, src string, o pcOpt) *lowered {
	t.Helper()
	l, why := pcBuild(t, src, o)
	if l == nil {
		t.Fatal(why)
	}
	return l
}

// pcCounts counts a body's checks: checked + - * (add, sub, mul), proved
// + - * (nowrap: AddNoWrap other than counter latches, SubNoWrap and
// MulNoWrap), checked integer casts, and bounds checks emitted
// (bounds) and left reachable (boundsLeft).
type pcCounts struct {
	add, sub, mul, nowrap, casts, bounds, boundsLeft int
}

func countChecks(tab *types.Table, b *mir.Body) pcCounts {
	var c pcCounts
	targeted := make([]bool, len(b.Blocks))
	var buf []mir.BlockID
	for i := range b.Blocks {
		buf = b.Successors(mir.BlockID(i), buf[:0])
		for _, s := range buf {
			targeted[s] = true
		}
	}
	for i := range b.Blocks {
		blk := &b.Blocks[i]
		if blk.Term.Kind == mir.TermPanic && blk.Term.Panic == types.PanicBounds {
			c.bounds++
			if targeted[i] {
				c.boundsLeft++
			}
		}
		for _, s := range blk.Stmts {
			if s.Kind != mir.StAssign {
				continue
			}
			r := s.Rvalue
			switch r.Kind {
			case mir.RvBinary:
				switch r.Bin {
				case mir.Add:
					c.add++
				case mir.Sub:
					c.sub++
				case mir.Mul:
					c.mul++
				case mir.AddNoWrap, mir.SubNoWrap, mir.MulNoWrap:
					if r.Bin != mir.AddNoWrap || !(r.B.Kind == mir.OpImm && r.B.Bits == 1 && wholeOf(r.A, s.Place.Local) && b.Locals[s.Place.Local].Kind == mir.LocalTemp && len(blk.Stmts) == 1) {
						c.nowrap++
					}
				}
			case mir.RvCast:
				if r.Cast == mir.CastChecked && intOf(tab, r.Type).ok {
					c.casts++
				}
			}
		}
	}
	return c
}

func (l *lowered) counts(key string) pcCounts { return countChecks(l.w.tab, l.body(key)) }

func (c pcCounts) String() string {
	return fmt.Sprintf("add=%d sub=%d mul=%d nowrap=%d casts=%d bounds=%d/%d", c.add, c.sub, c.mul, c.nowrap, c.casts, c.boundsLeft, c.bounds)
}

func (c *pcCounts) plus(d pcCounts) {
	c.add, c.sub, c.mul, c.nowrap = c.add+d.add, c.sub+d.sub, c.mul+d.mul, c.nowrap+d.nowrap
	c.casts, c.bounds, c.boundsLeft = c.casts+d.casts, c.bounds+d.bounds, c.boundsLeft+d.boundsLeft
}

// TestPC_CounterNoWrap: lower's half of the counter rule, the counter latch
// is AddNoWrap.
func TestPC_CounterNoWrap(t *testing.T) {
	l := pcLower(t, "F(n: int): int {\n\tvar s := 0\n\tfor i in 0..n {\n\t\ts = i\n\t}\n\treturn s\n}\n", pcOpt{})
	wantIn(t, l.print("F"), "Lt(", "AddNoWrap(copy _")
}

// pcFacts are, per fact source, a program the rule proves and one it must
// not (the check stays). want is F's counts with the pass on and the
// post-borrow bounds fold; with the pass off every check stays. The bounds
// rows test FoldProvedBounds as an API; whether a build folds them is
// pcShipsFold.
var pcFacts = []struct {
	name, src string
	want      string
}{
	{"const/proved", "F(): int {\n\tx := 7\n\treturn x * 6 + 1\n}\n", "add=0 sub=0 mul=0 nowrap=2 casts=0 bounds=0/0"},
	{"const/kept", "F(x: int): int {\n\treturn x * 6 + 1\n}\n", "add=1 sub=0 mul=1 nowrap=0 casts=0 bounds=0/0"},
	{"mask/proved", "F(x: u64): u64 {\n\treturn (x & 1023) + 1\n}\n", "add=0 sub=0 mul=0 nowrap=1 casts=0 bounds=0/0"},
	{"mask/or-kept", "F(x: u64): u64 {\n\treturn (x | 1023) + 1\n}\n", "add=1 sub=0 mul=0 nowrap=0 casts=0 bounds=0/0"},
	{"shr/proved", "F(x: u64): u64 {\n\treturn (x >> 2) + (x >> 2)\n}\n", "add=0 sub=0 mul=0 nowrap=1 casts=0 bounds=0/0"},
	{"shr/kept", "F(x: u64): u64 {\n\treturn (x >> 2) + x\n}\n", "add=1 sub=0 mul=0 nowrap=0 casts=0 bounds=0/0"},
	{"shl/proved", "F(x: u8): u32 {\n\treturn (u32(x) << 8) + 255\n}\n", "add=0 sub=0 mul=0 nowrap=1 casts=0 bounds=0/0"},
	{"shl/kept", "F(x: u32): u32 {\n\treturn (x << 8) + 255\n}\n", "add=1 sub=0 mul=0 nowrap=0 casts=0 bounds=0/0"},
	{"rem/proved", "F(x: i8): i8 {\n\treturn x % 10 + 100\n}\n", "add=0 sub=0 mul=0 nowrap=1 casts=0 bounds=0/0"},
	{"rem/kept", "F(x: i8): i8 {\n\treturn x % 30 + 100\n}\n", "add=1 sub=0 mul=0 nowrap=0 casts=0 bounds=0/0"},
	{"mul/proved", "F(x: u8, y: u8): u32 {\n\treturn u32(x) * u32(y)\n}\n", "add=0 sub=0 mul=0 nowrap=1 casts=0 bounds=0/0"},
	{"mul/kept", "F(x: u32, y: u8): u32 {\n\treturn x * u32(y)\n}\n", "add=0 sub=0 mul=1 nowrap=0 casts=0 bounds=0/0"},
	{"sub/proved", "F(x: u8): i32 {\n\treturn i32(x) - 300\n}\n", "add=0 sub=0 mul=0 nowrap=1 casts=0 bounds=0/0"},
	{"sub/kept", "F(x: i32): i32 {\n\treturn x - 300\n}\n", "add=0 sub=1 mul=0 nowrap=0 casts=0 bounds=0/0"},
	{"checked-op/proved", "F(x: i32): i32 {\n\ty := x / 4\n\treturn y + y\n}\n", "add=0 sub=0 mul=0 nowrap=1 casts=0 bounds=0/0"},
	{"checked-op/kept", "F(x: i32): i32 {\n\ty := x / 2\n\treturn y + y + 2\n}\n", "add=1 sub=0 mul=0 nowrap=1 casts=0 bounds=0/0"},
	{"counter/proved", "F(n: int): int {\n\tvar s := 0\n\tfor i in 0..n {\n\t\ts = i + 1\n\t}\n\treturn s\n}\n", "add=0 sub=0 mul=0 nowrap=1 casts=0 bounds=0/0"},
	{"counter/kept", "F(n: int): int {\n\tvar s := 0\n\tfor i in 0..n {\n\t\ts = i + 2\n\t}\n\treturn s\n}\n", "add=1 sub=0 mul=0 nowrap=0 casts=0 bounds=0/0"},
	{"bounds/index-arith", "F(xs: []u8, i: int): int {\n\tif xs[i] == 0 {\n\t\treturn i + 1\n\t}\n\treturn 0\n}\n", "add=0 sub=0 mul=0 nowrap=1 casts=0 bounds=1/1"},
	{"bounds/no-check-kept", "F(xs: []u8, i: int): int {\n\treturn i + 1\n}\n", "add=1 sub=0 mul=0 nowrap=0 casts=0 bounds=0/0"},
	{"bounds/folded", "F(i: u8): u8 {\n\tvar a: [256]u8 = [0; 256]\n\ta[3] = 7\n\treturn a[i]\n}\n", "add=0 sub=0 mul=0 nowrap=0 casts=0 bounds=0/2"},
	{"bounds/short-kept", "F(i: u8): u8 {\n\tvar a: [255]u8 = [0; 255]\n\ta[3] = 7\n\treturn a[i]\n}\n", "add=0 sub=0 mul=0 nowrap=0 casts=0 bounds=1/2"},
	{"cast/proved", "F(x: u64): i64 {\n\treturn Cast[i64](x & 1023)\n}\n", "add=0 sub=0 mul=0 nowrap=0 casts=0 bounds=0/0"},
	{"cast/kept", "F(x: u64): i64 {\n\treturn Cast[i64](x)\n}\n", "add=0 sub=0 mul=0 nowrap=0 casts=1 bounds=0/0"},
	{"trunc-refine/equal-width", "F(xs: []u8, i: int): int {\n\tif xs[i] == 0 {\n\t\treturn i - 1\n\t}\n\treturn 0\n}\n", "add=0 sub=0 mul=0 nowrap=1 casts=0 bounds=1/1"},
	{"trunc-refine/narrow-kept", "F(x: u16): u16 {\n\tif Trunc[u8](x) < 2 {\n\t\treturn x + 65280\n\t}\n\treturn 0\n}\n", "add=1 sub=0 mul=0 nowrap=0 casts=0 bounds=0/0"},
	{"contents/proved", "F(k: u8): i64 {\n\tvar a: [16]i64 = [0; 16]\n\tfor i in 0..16 {\n\t\ta[i] = Cast[i64](i & 7)\n\t}\n\treturn a[k & 15] + 1\n}\n", "add=0 sub=0 mul=0 nowrap=1 casts=0 bounds=0/2"},
	{"contents/escaped-kept", "Use(a: &[16]i64) {\n}\nF(k: u8): i64 {\n\tvar a: [16]i64 = [0; 16]\n\tfor i in 0..16 {\n\t\ta[i] = Cast[i64](i & 7)\n\t}\n\tUse(&a)\n\treturn a[k & 15] + 1\n}\n", "add=1 sub=0 mul=0 nowrap=0 casts=0 bounds=0/2"},
	{"contents/arena-proved", "F(ar: &Arena, n: int, k: int): OutOfMemory!i64 {\n\txs := try ar.Slice[i64](n)\n\tfor i in 0..n {\n\t\txs[i] = Cast[i64](i & 255)\n\t}\n\treturn xs[k] + 1\n}\n", "add=0 sub=0 mul=0 nowrap=1 casts=0 bounds=1/2"},
	{"contents/arena-param-kept", "F(xs: mut []i64, k: int): i64 {\n\treturn xs[k] + 1\n}\n", "add=1 sub=0 mul=0 nowrap=0 casts=0 bounds=1/1"},
	{"accumulator/proved", "F(a: [100]i32): i64 {\n\tvar s: i64 = 0\n\tfor i in 0..a.Len() {\n\t\ts += i64(a[i])\n\t}\n\treturn s\n}\n", "add=0 sub=0 mul=0 nowrap=1 casts=0 bounds=0/0"},
	{"accumulator/narrow-kept", "F(a: [100]i32): i32 {\n\tvar s: i32 = 0\n\tfor i in 0..a.Len() {\n\t\ts += a[i]\n\t}\n\treturn s\n}\n", "add=1 sub=0 mul=0 nowrap=0 casts=0 bounds=0/0"},
	{"accumulator/two-writes-kept", "F(a: [100]i32): i64 {\n\tvar s: i64 = 0\n\tfor i in 0..a.Len() {\n\t\ts += i64(a[i])\n\t\ts += i64(a[i])\n\t}\n\treturn s\n}\n", "add=2 sub=0 mul=0 nowrap=0 casts=0 bounds=0/0"},
	// The object bound (no object spans more than 2^56 bytes): a slice of
	// i64 has at most 2^53 elements, so a sum of values below 1024 over its
	// in-range indices fits i64; with i32 values (up to 2^31) it does not.
	{"accumulator/object-bound", "F(xs: []i64): i64 {\n\tvar s: i64 = 0\n\tfor i in 0..xs.Len() {\n\t\ts += xs[i] & 1023\n\t}\n\treturn s\n}\n", "add=0 sub=0 mul=0 nowrap=1 casts=0 bounds=0/0"},
	{"accumulator/object-bound-kept", "F(xs: []i64): i64 {\n\tvar s: i64 = 0\n\tfor i in 0..xs.Len() {\n\t\ts += xs[i] & 0xffffffffff\n\t}\n\treturn s\n}\n", "add=1 sub=0 mul=0 nowrap=0 casts=0 bounds=0/0"},
	// Generic bodies: mono runs a fields loop's body once per
	// field, zero times included, so neither the sum, nor the cast after
	// it, nor what follows a loop that may not run is proved; a fact that
	// holds on every iteration still is. A type test keeps both edges, and
	// a const-parameter length is unknown.
	{"fields/kept", "F[A: Fields[Format] + Plain](args: A): u8 {\n\tvar n: u8 = 0\n\tfor _, v in fields(&args) {\n\t\tn += 100\n\t\t_ = v\n\t}\n\treturn n\n}\n", "add=1 sub=0 mul=0 nowrap=0 casts=0 bounds=0/0"},
	{"fields/cast-kept", "F[A: Fields[Format] + Plain](args: A): u8 {\n\tvar k: int = 0\n\tfor _, v in fields(&args) {\n\t\tk += 200\n\t\t_ = v\n\t}\n\treturn Cast[u8](k)\n}\n", "add=1 sub=0 mul=0 nowrap=0 casts=1 bounds=0/0"},
	{"fields/zero-kept", "F[A: Fields[Format] + Plain](args: A): u8 {\n\tvar n: u8 = 0\n\tfor _, v in fields(&args) {\n\t\tn = 5\n\t\t_ = v\n\t}\n\treturn n - 1\n}\n", "add=0 sub=1 mul=0 nowrap=0 casts=0 bounds=0/0"},
	{"fields/proved", "F[A: Fields[Format] + Plain](args: A): u8 {\n\tvar n: u8 = 0\n\tfor _, v in fields(&args) {\n\t\tn = (n & 15) + 1\n\t\t_ = v\n\t}\n\treturn n\n}\n", "add=0 sub=0 mul=0 nowrap=1 casts=0 bounds=0/0"},
	{"typetest/kept", "F[T](x: u8): u8 {\n\tvar y: u8 = 0\n\tif T is int {\n\t\ty = 200\n\t}\n\treturn y + 100\n}\n", "add=1 sub=0 mul=0 nowrap=0 casts=0 bounds=0/0"},
	{"const-len/kept", "F[const N: int](x: u8): u8 {\n\ta: [N]u8 = [0; N]\n\treturn Cast[u8](a.Len())\n}\n", "add=0 sub=0 mul=0 nowrap=0 casts=1 bounds=0/0"},
	// A non-decreasing counter keeps its lower bound (the
	// intervals already do it: a checked step that did not trap is exact,
	// and widening moves only the bound that grows); a wrapping step or a
	// decrement does not.
	{"nondecreasing/proved", "F(n: int): uint {\n\tvar i := 0\n\tvar r: uint = 0\n\tfor i < n {\n\t\tr = Cast[uint](i)\n\t\ti += 3\n\t}\n\treturn r\n}\n", "add=1 sub=0 mul=0 nowrap=0 casts=0 bounds=0/0"},
	{"nondecreasing/wrap-kept", "F(n: int): uint {\n\tvar i := 0\n\tvar r: uint = 0\n\tfor i < n {\n\t\tr = Cast[uint](i)\n\t\ti +%= 3\n\t}\n\treturn r\n}\n", "add=0 sub=0 mul=0 nowrap=0 casts=1 bounds=0/0"},
	{"nondecreasing/dec-kept", "F(n: int): uint {\n\tvar i := 0\n\tvar r: uint = 0\n\tfor i < n {\n\t\tr = Cast[uint](i)\n\t\ti += 3\n\t\tif r > 7 {\n\t\t\ti -= 8\n\t\t}\n\t}\n\treturn r\n}\n", "add=1 sub=1 mul=0 nowrap=0 casts=1 bounds=0/0"},
	// Rule 8 with a counter stepped by 1 or 2, whose
	// unconditional step is on every path (jsonscan's string loop); kept
	// when one path steps by 0 only.
	{"accumulator/steps-proved", "F(b: []u8): int {\n\tn := b.Len()\n\tvar i := 0\n\tvar l := 0\n\tfor i < n {\n\t\tif b[i] == 92 {\n\t\t\ti += 1\n\t\t}\n\t\ti += 1\n\t\tl += 1\n\t}\n\treturn l\n}\n", "add=0 sub=0 mul=0 nowrap=3 casts=0 bounds=0/1"},
	{"accumulator/steps-kept", "F(b: []u8): int {\n\tn := b.Len()\n\tvar i := 0\n\tvar l := 0\n\tfor i < n {\n\t\tif b[i] == 92 {\n\t\t\ti += 1\n\t\t} else {\n\t\t\ti += 0\n\t\t}\n\t\tl += 1\n\t}\n\treturn l\n}\n", "add=1 sub=0 mul=0 nowrap=2 casts=0 bounds=0/1"},
	// Rule 9: n := s.Len() is s's length while s is not
	// assigned; a slice expression's and Arena.Slice's lengths.
	{"len-eq/proved", "F(s: []u8): int {\n\tn := s.Len()\n\tvar r := 0\n\tvar i := 0\n\tfor i < n {\n\t\tr = r +% int(s[i])\n\t\ti += 1\n\t}\n\treturn r\n}\n", "add=0 sub=0 mul=0 nowrap=1 casts=0 bounds=0/1"},
	{"len-eq/le-kept", "F(s: []u8): int {\n\tn := s.Len()\n\tvar r := 0\n\tvar i := 0\n\tfor i <= n {\n\t\tr = r +% int(s[i])\n\t\ti += 1\n\t}\n\treturn r\n}\n", "add=0 sub=0 mul=0 nowrap=1 casts=0 bounds=1/1"},
	{"len-eq/assigned-kept", "F(s: []u8): int {\n\tvar t := s\n\tn := t.Len()\n\tt = s[1..]\n\tvar r := 0\n\tvar i := 0\n\tfor i < n {\n\t\tr = r +% int(t[i])\n\t\ti += 1\n\t}\n\treturn r\n}\n", "add=0 sub=0 mul=0 nowrap=1 casts=0 bounds=2/2"},
	{"slice-len/proved", "F(s: []u8): int {\n\tt := s[2..]\n\tvar r := 0\n\tfor i in 0..s.Len() - 2 {\n\t\tr = r +% int(t[i])\n\t}\n\treturn r\n}\n", "add=0 sub=0 mul=0 nowrap=1 casts=0 bounds=1/2"},
	{"slice-len/kept", "F(s: []u8): int {\n\tt := s[2..]\n\tvar r := 0\n\tfor i in 0..s.Len() - 1 {\n\t\tr = r +% int(t[i])\n\t}\n\treturn r\n}\n", "add=0 sub=0 mul=0 nowrap=1 casts=0 bounds=2/2"},
	{"arena-len/kept", "F(ar: &Arena, n: int): OutOfMemory!i64 {\n\txs := try ar.Slice[i64](n)\n\tfor i in 0..n + 1 {\n\t\txs[i] = 1\n\t}\n\treturn 0\n}\n", "add=1 sub=0 mul=0 nowrap=0 casts=0 bounds=1/1"},
	// Rule 10: i < j with j's check passed proves s[i], and
	// a check that passed proves its index again; i < j + 2 does not.
	{"pair/proved", "F(s: mut []i64) {\n\tif s.Len() == 0 {\n\t\treturn\n\t}\n\tvar i := 0\n\tvar j := Cast[int](s[0] & 255)\n\tfor i < j {\n\t\tt := s[i]\n\t\ts[i] = s[j]\n\t\ts[j] = t\n\t\ti += 1\n\t\tj -= 1\n\t}\n}\n", "add=0 sub=0 mul=0 nowrap=2 casts=0 bounds=2/5"},
	{"pair/kept", "F(s: mut []i64) {\n\tif s.Len() == 0 {\n\t\treturn\n\t}\n\tvar i := 0\n\tvar j := Cast[int](s[0] & 255)\n\tfor i < j + 2 {\n\t\tt := s[i]\n\t\ts[i] = s[j]\n\t\ts[j] = t\n\t\ti += 1\n\t\tj -= 1\n\t}\n}\n", "add=0 sub=0 mul=0 nowrap=3 casts=0 bounds=3/5"},
	// Rule 11: a direct call of std.simd's Mask[T, N].CountSet is in [0, N]
	// (N = 16: MAX - 16 + N fits, MAX - 15 + N does not); in a generic body
	// with N a const parameter, [0, 64] by the restated lane-count clause.
	// An fn value naming the intrinsic and a user method of the same name
	// on a user type Mask are whole-range calls.
	{"countset/proved", "import std.simd\n\nF(m: simd.Mask[u8, 16]): int {\n\treturn m.CountSet() + 9223372036854775791\n}\n", "add=0 sub=0 mul=0 nowrap=1 casts=0 bounds=0/0"},
	{"countset/kept", "import std.simd\n\nF(m: simd.Mask[u8, 16]): int {\n\treturn m.CountSet() + 9223372036854775792\n}\n", "add=1 sub=0 mul=0 nowrap=0 casts=0 bounds=0/0"},
	{"countset/lanes2-kept", "import std.simd\n\nF(m: simd.Mask[i64, 2]): int {\n\treturn m.CountSet() + 9223372036854775806\n}\n", "add=1 sub=0 mul=0 nowrap=0 casts=0 bounds=0/0"},
	{"countset/lanes2-proved", "import std.simd\n\nF(m: simd.Mask[i64, 2]): int {\n\treturn m.CountSet() + 9223372036854775805\n}\n", "add=0 sub=0 mul=0 nowrap=1 casts=0 bounds=0/0"},
	{"countset/generic-proved", "import std.simd\n\nF[const N: int](m: simd.Mask[u8, N]): int requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64 {\n\treturn m.CountSet() + 9223372036854775743\n}\n", "add=0 sub=0 mul=0 nowrap=1 casts=0 bounds=0/0"},
	{"countset/generic-kept", "import std.simd\n\nF[const N: int](m: simd.Mask[u8, N]): int requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64 {\n\treturn m.CountSet() + 9223372036854775744\n}\n", "add=1 sub=0 mul=0 nowrap=0 casts=0 bounds=0/0"},
	{"countset/value-kept", "import std.simd\n\nF(m: simd.Mask[u8, 16]): int {\n\tc := simd.Mask[u8, 16].CountSet\n\treturn c(m) + 9223372036854775791\n}\n", "add=1 sub=0 mul=0 nowrap=0 casts=0 bounds=0/0"},
	{"countset/user-kept", "struct Mask {\n\tx: int\n}\nMask.CountSet(self): int {\n\treturn self.x\n}\nF(m: Mask): int {\n\treturn m.CountSet() + 9223372036854775791\n}\n", "add=1 sub=0 mul=0 nowrap=0 casts=0 bounds=0/0"},
	// Rule 11: std.raw's bit counts are in [0, 64].
	{"popcount/proved", "import std.raw\n\nF(x: u64): int {\n\treturn raw.Popcount64(x) + 9223372036854775743\n}\n", "add=0 sub=0 mul=0 nowrap=1 casts=0 bounds=0/0"},
	{"popcount/kept", "import std.raw\n\nF(x: u64): int {\n\treturn raw.Popcount64(x) + 9223372036854775744\n}\n", "add=1 sub=0 mul=0 nowrap=0 casts=0 bounds=0/0"},
	{"ctz/proved", "import std.raw\n\nF(x: u64): int {\n\treturn raw.Ctz64(x) + 9223372036854775743\n}\n", "add=0 sub=0 mul=0 nowrap=1 casts=0 bounds=0/0"},
	{"ctz/kept", "import std.raw\n\nF(x: u64): int {\n\treturn raw.Ctz64(x) + 9223372036854775744\n}\n", "add=1 sub=0 mul=0 nowrap=0 casts=0 bounds=0/0"},
	{"clz/proved", "import std.raw\n\nF(x: u64): int {\n\treturn 63 - raw.Clz64(x)\n}\n", "add=0 sub=0 mul=0 nowrap=1 casts=0 bounds=0/0"},
	{"clz/kept", "import std.raw\n\nF(x: u64): int {\n\treturn raw.Clz64(x) + 9223372036854775744\n}\n", "add=1 sub=0 mul=0 nowrap=0 casts=0 bounds=0/0"},
	{"bits/value-kept", "import std.raw\n\nF(x: u64): int {\n\tc := raw.Popcount64\n\treturn c(x) + 9223372036854775743\n}\n", "add=1 sub=0 mul=0 nowrap=0 casts=0 bounds=0/0"},
	// Ranges the rules already gave before rule 11 (rule 11's survey of
	// specification-given ranges): Mk has no bool-to-int cast (E0302), and
	// `if b { 1 } else { 0 }` is [0, 1] by rule 1 and the join; Len() of a
	// fixed array is its length (rule 6). x % m, x & m and x >> k are the
	// rem, mask and shr rows above (rule 2).
	{"if-bool/proved", "F(b: bool): int {\n\tx := if b { 1 } else { 0 }\n\treturn x + 9223372036854775806\n}\n", "add=0 sub=0 mul=0 nowrap=1 casts=0 bounds=0/0"},
	{"if-bool/kept", "F(b: bool): int {\n\tx := if b { 1 } else { 0 }\n\treturn x + 9223372036854775807\n}\n", "add=1 sub=0 mul=0 nowrap=0 casts=0 bounds=0/0"},
	{"array-len/proved", "F(a: [8]u8): u8 {\n\treturn Cast[u8](a.Len()) + 247\n}\n", "add=0 sub=0 mul=0 nowrap=1 casts=0 bounds=0/0"},
	{"array-len/kept", "F(a: [8]u8): u8 {\n\treturn Cast[u8](a.Len()) + 248\n}\n", "add=1 sub=0 mul=0 nowrap=0 casts=0 bounds=0/0"},
	// Rule 5 through t = a + c, c + a or a - c (the countbyte benchmark's
	// loop, `for i + 16 <= xs.Len()`): a compare bounding t bounds a by t's
	// bound less c. The header's i + 4 <= n bounds i by n - 4 in the body,
	// so r = i + 4 and the step are proved and r = i + 5 is not; the
	// header's own i + 4 stays checked (i reaches n there).
	{"step/proved", "F(n: u8): u8 {\n\tvar i: u8 = 0\n\tvar r: u8 = 0\n\tfor i + 4 <= n {\n\t\tr = i + 4\n\t\ti += 4\n\t}\n\treturn r\n}\n", "add=1 sub=0 mul=0 nowrap=2 casts=0 bounds=0/0"},
	{"step/kept", "F(n: u8): u8 {\n\tvar i: u8 = 0\n\tvar r: u8 = 0\n\tfor i + 4 <= n {\n\t\tr = i + 5\n\t\ti += 4\n\t}\n\treturn r\n}\n", "add=2 sub=0 mul=0 nowrap=1 casts=0 bounds=0/0"},
	{"step/sub-proved", "F(n: u8): u8 {\n\tif n - 3 <= 200 {\n\t\treturn n + 52\n\t}\n\treturn 0\n}\n", "add=0 sub=1 mul=0 nowrap=1 casts=0 bounds=0/0"},
	{"step/sub-kept", "F(n: u8): u8 {\n\tif n - 3 <= 200 {\n\t\treturn n + 53\n\t}\n\treturn 0\n}\n", "add=1 sub=1 mul=0 nowrap=0 casts=0 bounds=0/0"},
	{"step/rev-proved", "F(n: u8): u8 {\n\tif 3 + n <= 200 {\n\t\treturn n + 58\n\t}\n\treturn 0\n}\n", "add=1 sub=0 mul=0 nowrap=1 casts=0 bounds=0/0"},
	{"step/rev-kept", "F(n: u8): u8 {\n\tif 3 + n <= 200 {\n\t\treturn n + 59\n\t}\n\treturn 0\n}\n", "add=2 sub=0 mul=0 nowrap=0 casts=0 bounds=0/0"},
	// c - a is no step: 10 - a <= 5 leaves a - 6
	// checked; a + 5 >= 10, the same bound as a step, proves a - 5.
	{"step/cminusa-kept", "F(a: u8): u8 {\n\tif 10 - a <= 5 {\n\t\treturn a - 6\n\t}\n\treturn 0\n}\n", "add=0 sub=2 mul=0 nowrap=0 casts=0 bounds=0/0"},
	{"step/cminusa-proved", "F(a: u8): u8 {\n\tif a + 5 >= 10 {\n\t\treturn a - 5\n\t}\n\treturn 0\n}\n", "add=1 sub=0 mul=0 nowrap=1 casts=0 bounds=0/0"},
	// Each compare kind and edge: ==, != (a singleton at t's end), and the
	// false edge of >.
	{"step/eq-proved", "F(i: u8): u8 {\n\tif i + 4 == 200 {\n\t\treturn i + 59\n\t}\n\treturn 0\n}\n", "add=1 sub=0 mul=0 nowrap=1 casts=0 bounds=0/0"},
	{"step/eq-kept", "F(i: u8): u8 {\n\tif i + 4 == 200 {\n\t\treturn i + 60\n\t}\n\treturn 0\n}\n", "add=2 sub=0 mul=0 nowrap=0 casts=0 bounds=0/0"},
	{"step/ne-proved", "F(i: u8): u8 {\n\tif i + 4 != 255 {\n\t\treturn i + 5\n\t}\n\treturn 0\n}\n", "add=1 sub=0 mul=0 nowrap=1 casts=0 bounds=0/0"},
	{"step/ne-kept", "F(i: u8): u8 {\n\tif i + 4 != 255 {\n\t\treturn i + 6\n\t}\n\treturn 0\n}\n", "add=2 sub=0 mul=0 nowrap=0 casts=0 bounds=0/0"},
	{"step/false-proved", "F(i: u8): u8 {\n\tif i + 4 > 200 {\n\t\treturn 0\n\t}\n\treturn i + 59\n}\n", "add=1 sub=0 mul=0 nowrap=1 casts=0 bounds=0/0"},
	{"step/false-kept", "F(i: u8): u8 {\n\tif i + 4 > 200 {\n\t\treturn 0\n\t}\n\treturn i + 60\n}\n", "add=2 sub=0 mul=0 nowrap=0 casts=0 bounds=0/0"},
	// Signed, c negative and c the type's least value: t = a + (-100)
	// <= 0 is a <= 100; t = a - (-128) >= 100 is a >= -28.
	{"step/neg-c-proved", "F(a: i8): i8 {\n\tif a + (-100) <= 0 {\n\t\treturn a + 27\n\t}\n\treturn 0\n}\n", "add=1 sub=0 mul=0 nowrap=1 casts=0 bounds=0/0"},
	{"step/neg-c-kept", "F(a: i8): i8 {\n\tif a + (-100) <= 0 {\n\t\treturn a + 28\n\t}\n\treturn 0\n}\n", "add=2 sub=0 mul=0 nowrap=0 casts=0 bounds=0/0"},
	{"step/min-c-proved", "F(a: i8): i8 {\n\tif a - (-128) >= 100 {\n\t\treturn a - 100\n\t}\n\treturn 0\n}\n", "add=0 sub=1 mul=0 nowrap=1 casts=0 bounds=0/0"},
	{"step/min-c-kept", "F(a: i8): i8 {\n\tif a - (-128) >= 100 {\n\t\treturn a - 101\n\t}\n\treturn 0\n}\n", "add=0 sub=2 mul=0 nowrap=0 casts=0 bounds=0/0"},
	// What refines nothing: i = i + 10 (i's old value is gone), a
	// reassigned between t's definition and the compare, a compare stored
	// before a changes, t defined in an earlier block, a wrapping +%. Each
	// has a proved twin without the obstacle.
	{"step/self-kept", "F(x: u8): u8 {\n\tvar i := x\n\ti = i + 10\n\tif i <= 200 {\n\t\treturn i + 60\n\t}\n\treturn 0\n}\n", "add=2 sub=0 mul=0 nowrap=0 casts=0 bounds=0/0"},
	{"step/self-proved", "F(x: u8): u8 {\n\tt := x + 10\n\tif t <= 200 {\n\t\treturn x + 65\n\t}\n\treturn 0\n}\n", "add=1 sub=0 mul=0 nowrap=1 casts=0 bounds=0/0"},
	{"step/stale-kept", "F(a0: u8, b: u8): u8 {\n\tvar a := a0\n\tt := a + 4\n\ta = b\n\tif t <= 10 {\n\t\treturn a + 249\n\t}\n\treturn 0\n}\n", "add=2 sub=0 mul=0 nowrap=0 casts=0 bounds=0/0"},
	{"step/stale-proved", "F(a0: u8, b: u8): u8 {\n\tvar a := a0\n\tt := a + 4\n\tif t <= 10 {\n\t\treturn a + 249\n\t}\n\treturn b\n}\n", "add=1 sub=0 mul=0 nowrap=1 casts=0 bounds=0/0"},
	{"step/stored-kept", "F(n: u8, i0: u8): u8 {\n\tvar i := i0\n\tc := i + 4 <= n\n\ti += 1\n\tif c {\n\t\treturn i + 4\n\t}\n\treturn 0\n}\n", "add=3 sub=0 mul=0 nowrap=0 casts=0 bounds=0/0"},
	{"step/stored-proved", "F(n: u8, i0: u8): u8 {\n\tvar i := i0\n\tc := i + 4 <= n\n\tif c {\n\t\treturn i + 4\n\t}\n\treturn 0\n}\n", "add=1 sub=0 mul=0 nowrap=1 casts=0 bounds=0/0"},
	{"step/block-kept", "G() {\n}\nF(n: u8, i: u8): u8 {\n\tt := i + 4\n\tG()\n\tif t <= n {\n\t\treturn i + 4\n\t}\n\treturn 0\n}\n", "add=2 sub=0 mul=0 nowrap=0 casts=0 bounds=0/0"},
	{"step/wrap-kept", "F(a: i8): i8 {\n\tt := a +% 100\n\tif t < 0 {\n\t\treturn a + 28\n\t}\n\treturn 0\n}\n", "add=1 sub=0 mul=0 nowrap=0 casts=0 bounds=0/0"},
	{"step/wrap-proved", "F(a: i8): i8 {\n\tt := a + 100\n\tif t < 0 {\n\t\treturn a + 28\n\t}\n\treturn 0\n}\n", "add=1 sub=0 mul=0 nowrap=1 casts=0 bounds=0/0"},
	// The reference's CountByte example (doc/reference.md,
	// tests/bench-lanes/lang-sys/countbyte): rules 11, 5 and 8 together
	// prove the 16-lane loop's n += CountSet(), its i + 16 and its step;
	// the tail's n += 1 stays checked (n arrives widened from the first
	// loop). From an unknown start n0 rule 8 proves no sum.
	{"step/countbyte", "import std.simd\n\n" + pcCountByte("0"), "add=1 sub=0 mul=0 nowrap=4 casts=0 bounds=0/1"},
	{"step/countbyte-kept", "import std.simd\n\n" + pcCountByte("n0"), "add=2 sub=0 mul=0 nowrap=3 casts=0 bounds=0/1"},
	// Rule 12: the switch at a short-circuit join is refined from the
	// predecessors that can take each edge. jsonscan's string loop: the
	// body has i < n, so its s[i] and i += 1 are proved; under || the body
	// is reached with i >= n too, and both stay.
	{"split/and-loop-proved", "F(s: []u8): int {\n\tn := s.Len()\n\tvar i := 0\n\tvar r := 0\n\tfor i < n && s[i] != 34 {\n\t\tr = r +% int(s[i])\n\t\ti += 1\n\t}\n\treturn r\n}\n", "add=0 sub=0 mul=0 nowrap=1 casts=0 bounds=0/2"},
	{"split/or-loop-kept", "F(s: []u8): int {\n\tn := s.Len()\n\tvar i := 0\n\tvar r := 0\n\tfor i < n || s[0] != 34 {\n\t\tr = r +% int(s[i])\n\t\ti += 1\n\t}\n\treturn r\n}\n", "add=0 sub=0 mul=0 nowrap=1 casts=0 bounds=2/2"},
	// PdqPartitionLomuto's guard, a || chain of three: past it a >= 0, b <=
	// s.Len() and so r < b and a < r prove s[r] and s[a]; with b <=
	// s.Len() + 1 s[r] stays.
	{"split/or-guard-proved", "F(s: []i64, a: int, b: int): i64 {\n\tif a < 0 || b > s.Len() || b - a < 2 {\n\t\treturn 0\n\t}\n\tvar t: i64 = 0\n\tfor r in (a + 2)..b {\n\t\tt = t +% s[r] +% s[a]\n\t}\n\treturn t\n}\n", "add=0 sub=1 mul=0 nowrap=1 casts=0 bounds=0/2"},
	{"split/or-guard-kept", "F(s: []i64, a: int, b: int): i64 {\n\tif a < 0 || b > s.Len() + 1 || b - a < 2 {\n\t\treturn 0\n\t}\n\tvar t: i64 = 0\n\tfor r in (a + 2)..b {\n\t\tt = t +% s[r] +% s[a]\n\t}\n\treturn t\n}\n", "add=0 sub=1 mul=0 nowrap=2 casts=0 bounds=1/2"},
	// A chain of three &&, a negation, and a condition stored in a
	// variable: refined when nothing runs between the join and the switch
	// but copies and negations, not when the index changes in between.
	{"split/three-proved", "F(s: []u8, i: int, j: int): u8 {\n\tif i >= 0 && i < s.Len() && s[i] != 0 {\n\t\treturn s[i]\n\t}\n\treturn 0\n}\n", "add=0 sub=0 mul=0 nowrap=0 casts=0 bounds=0/2"},
	{"split/three-kept", "F(s: []u8, i: int, j: int): u8 {\n\tif i >= 0 && i <= s.Len() && j != 0 {\n\t\treturn s[i]\n\t}\n\treturn 0\n}\n", "add=0 sub=0 mul=0 nowrap=0 casts=0 bounds=1/1"},
	{"split/not-proved", "F(s: []u8, i: int): u8 {\n\tif !(i < s.Len() && i >= 0) {\n\t\treturn 0\n\t}\n\treturn s[i]\n}\n", "add=0 sub=0 mul=0 nowrap=0 casts=0 bounds=0/1"},
	{"split/not-kept", "F(s: []u8, i: int): u8 {\n\tif !(i < s.Len() || i >= 0) {\n\t\treturn 0\n\t}\n\treturn s[i]\n}\n", "add=0 sub=0 mul=0 nowrap=0 casts=0 bounds=1/1"},
	{"split/stored-proved", "F(s: []u8, i: int): u8 {\n\tc := i >= 0 && i < s.Len()\n\tif c {\n\t\treturn s[i]\n\t}\n\treturn 0\n}\n", "add=0 sub=0 mul=0 nowrap=0 casts=0 bounds=0/1"},
	{"split/stored-kept", "F(s: []u8, i0: int): u8 {\n\tvar i := i0\n\tc := i >= 0 && i < s.Len()\n\ti += 1\n\tif c {\n\t\treturn s[i]\n\t}\n\treturn 0\n}\n", "add=1 sub=0 mul=0 nowrap=0 casts=0 bounds=1/1"},
}

// pcCountByte is the reference CountByte block as F, its count starting
// at n (an expression over the parameters xs, b and n0).
func pcCountByte(n string) string {
	return "F(xs: []u8, b: u8, n0: int): int {\n\tvar n := " + n + "\n\tvar i := 0\n\tneedle := simd.Vec[u8, 16].Splat(b)\n" +
		"\tfor i + 16 <= xs.Len() {\n\t\tv := simd.Vec[u8, 16].FromSlice(xs, i)\n\t\tn += (v == needle).CountSet()\n\t\ti += 16\n\t}\n" +
		"\tfor i < xs.Len() {\n\t\tif xs[i] == b {\n\t\t\tn += 1\n\t\t}\n\t\ti += 1\n\t}\n\treturn n\n}\n"
}

// TestPC_Facts: each fact source proves its accepting program and leaves
// the check of its rejecting one; with NoRanges nothing is proved.
func TestPC_Facts(t *testing.T) {
	for _, c := range pcFacts {
		t.Run(c.name, func(t *testing.T) {
			l := pcLower(t, c.src, pcOpt{fold: true})
			if got := l.counts("F").String(); got != c.want {
				t.Errorf("got  %s\nwant %s\n%s", got, c.want, l.print("F"))
			}
			off := pcLower(t, c.src, pcOpt{fold: false, tweak: func(cfg *Config) { cfg.NoRanges = true }})
			if got := off.counts("F"); got.nowrap != 0 || got.boundsLeft != got.bounds {
				t.Errorf("with NoRanges something was proved: %s", got)
			}
		})
	}
}

// TestPC_NoRangesFoldsNothing: Config.NoRanges turns the pass off in
// FoldProvedBounds too, so a driver A/B or a test's "pass off" run that
// also folds keeps every check (before, the fold ran the whole analysis,
// and TestPC_MutationCaught's pass-off side was the pass on).
func TestPC_NoRangesFoldsNothing(t *testing.T) {
	src := "F(i: u8, x: u64): i64 {\n\tvar a: [256]u8 = [0; 256]\n\ta[3] = 7\n\treturn i64(a[i]) + Cast[i64](x & 1023)\n}\n"
	off := pcLower(t, src, pcOpt{fold: true, tweak: func(c *Config) { c.NoRanges = true }})
	if got := off.counts("F").String(); got != "add=1 sub=0 mul=0 nowrap=0 casts=1 bounds=2/2" {
		t.Errorf("with NoRanges and the fold: %s", got)
	}
	cfg := off.w.lowerCfg()
	cfg.NoRanges = true
	if n := FoldProvedBounds(cfg, off.body("F")); n != 0 {
		t.Errorf("FoldProvedBounds with NoRanges folded %d checks", n)
	}
	on := pcLower(t, src, pcOpt{fold: true})
	if got := on.counts("F").String(); got != "add=0 sub=0 mul=0 nowrap=1 casts=0 bounds=0/2" {
		t.Errorf("with the pass and the fold: %s", got)
	}
}

// sameShape reports how two lowerings of one body differ beyond what
// Lower's pass may change: an operator Add, Sub, Mul against AddNoWrap,
// SubNoWrap, MulNoWrap, and a cast Checked against Trunc. "" when they agree.
func sameShape(a, b *mir.Body) string {
	if len(a.Blocks) != len(b.Blocks) || len(a.Locals) != len(b.Locals) {
		return fmt.Sprintf("%d blocks and %d locals against %d and %d", len(a.Blocks), len(a.Locals), len(b.Blocks), len(b.Locals))
	}
	norm := func(s mir.Statement) mir.Statement {
		if s.Kind == mir.StAssign {
			switch r := &s.Rvalue; {
			case r.Kind == mir.RvBinary && r.Bin == mir.AddNoWrap:
				r.Bin = mir.Add
			case r.Kind == mir.RvBinary && r.Bin == mir.SubNoWrap:
				r.Bin = mir.Sub
			case r.Kind == mir.RvBinary && r.Bin == mir.MulNoWrap:
				r.Bin = mir.Mul
			case r.Kind == mir.RvCast && r.Cast == mir.CastTrunc:
				r.Cast = mir.CastChecked
			}
		}
		return s
	}
	for i := range a.Blocks {
		x, y := &a.Blocks[i], &b.Blocks[i]
		if x.Term != y.Term {
			return fmt.Sprintf("bb%d: terminator %v against %v", i, x.Term.Kind, y.Term.Kind)
		}
		if len(x.Stmts) != len(y.Stmts) {
			return fmt.Sprintf("bb%d: %d statements against %d", i, len(x.Stmts), len(y.Stmts))
		}
		for j := range x.Stmts {
			if norm(x.Stmts[j]) != norm(y.Stmts[j]) {
				return fmt.Sprintf("bb%d statement %d differs", i, j)
			}
		}
	}
	return ""
}

// TestPC_LowerKeepsCFG: inside Lower the pass changes no block, terminator
// or statement beyond an operator or cast kind, so borrow, which reads a
// bounds check by its shape, sees the body it saw with the pass off, on
// every fact program and every bench kernel.
func TestPC_LowerKeepsCFG(t *testing.T) {
	type prog struct {
		name, src string
		bin       bool
	}
	var progs []prog
	for _, c := range pcFacts {
		progs = append(progs, prog{c.name, c.src, false})
	}
	for _, e := range pcEdge {
		progs = append(progs, prog{"edge/" + e.name, e.src, false})
	}
	for _, f := range pcKernels(t) {
		progs = append(progs, prog{"kernel/" + kernelName(f), readKernel(t, f), true})
	}
	for _, p := range progs {
		on := pcLower(t, p.src, pcOpt{bin: p.bin})
		off := pcLower(t, p.src, pcOpt{bin: p.bin, tweak: func(c *Config) { c.NoRanges = true }})
		for _, k := range on.keys {
			if d := sameShape(on.body(k), off.body(k)); d != "" {
				t.Errorf("%s %s: %s", p.name, k, d)
			}
		}
	}
}

// pcEval lowers src with tweak and evaluates its H(): int; the result is
// its value or its panic's code and span.
func pcEval(t *testing.T, src string, tweak func(*Config)) string {
	t.Helper()
	return pcRun(pcLower(t, src, pcOpt{fold: true, tweak: tweak}))
}

// pcRun evaluates the H(): int of l.
func pcRun(l *lowered) string {
	out, _ := pcRunSteps(l)
	return out
}

// pcRunSteps is pcRun and the steps eval used (one per MIR statement or
// terminator, plus one per 16 bytes moved).
func pcRunSteps(l *lowered) (string, uint64) {
	p := &evalProg{l: l, byItem: map[types.ItemID]string{}}
	var h types.ItemID
	for _, id := range l.b.m.Units() {
		k := l.b.key(id)
		p.byItem[id] = k
		if k == "H" {
			h = id
		}
	}
	ev := eval.New(eval.Config{Types: l.w.tab, Items: l.w.items, Consts: l.w.consts, Interner: l.w.in, Program: p, Known: *l.w.cfg.Known})
	r := ev.Eval(&types.EvalRequest{What: types.EvalCall, Item: h, Fuel: 1 << 22, Stage: types.StageBodies, Module: l.b.id})
	if r.Err != nil {
		return fmt.Sprintf("error %d panic %d at %s", r.Err.Kind, r.Err.Panic, l.w.lc(r.Err.At)), r.Steps
	}
	v := l.w.consts.Get(r.Value)
	return fmt.Sprintf("%d", int64(binary.LittleEndian.Uint64(v.Bytes))), r.Steps
}

// pcEdge are programs whose check fails exactly one past what a wrong fact
// believes: each panics with the pass right (as with the pass off), and a
// mutant that proves the check lets eval wrap, or index, instead.
var pcEdge = []struct{ name, src string }{
	{"lt", "H(): int {\n\tvar r := 0\n\tfor i in 0..128 {\n\t\tx: i8 = Cast[i8](i) + 1\n\t\tr = int(x)\n\t}\n\treturn r\n}\n"},
	{"mask", "H(): int {\n\tvar r := 0\n\tfor i in 0..300 {\n\t\tx: u8 = Cast[u8](i & 255) + 1\n\t\tr = int(x)\n\t}\n\treturn r\n}\n"},
	{"accum", "H(): int {\n\tvar s: i8 = 0\n\tfor _ in 0..128 {\n\t\ts += 1\n\t}\n\treturn int(s)\n}\n"},
	{"store", "H(): int {\n\tvar a: [4]u8 = [0; 4]\n\ta[2] = 255\n\tr: u8 = a[2] + 1\n\treturn int(r)\n}\n"},
	{"trunc", "H(): int {\n\tvar r := 0\n\tfor i in 0..300 {\n\t\tx: u16 = Cast[u16](i)\n\t\tt := Trunc[u8](x)\n\t\tif t < 2 {\n\t\t\tz: u16 = x + 65280\n\t\t\tr = int(z)\n\t\t}\n\t}\n\treturn r\n}\n"},
	{"shr", "H(): int {\n\tvar r := 0\n\tfor i in 0..256 {\n\t\tx: u8 = Cast[u8](i)\n\t\ty: u8 = (x >> 1) + 129\n\t\tr = int(y)\n\t}\n\treturn r\n}\n"},
	{"bounds", "H(): int {\n\tvar a: [8]u8 = [0; 8]\n\tvar r := 0\n\tfor i in 0..9 {\n\t\tr = int(a[i & 15])\n\t}\n\treturn r\n}\n"},
	{"len", "H(): int {\n\ta: [8]u8 = [0; 8]\n\tx: u8 = Cast[u8](a.Len()) + 248\n\treturn int(x)\n}\n"},
	// A - and a * whose least or greatest value is one past the type.
	{"sub", "H(): int {\n\tvar r := 0\n\tfor i in 0..256 {\n\t\tx: u8 = Cast[u8](i)\n\t\ty: u8 = 254 - x\n\t\tr = int(y)\n\t}\n\treturn r\n}\n"},
	{"mul", "H(): int {\n\tvar r := 0\n\tfor i in 0..17 {\n\t\tx: u8 = Cast[u8](i)\n\t\ty: u8 = x * 16\n\t\tr = int(y)\n\t}\n\treturn r\n}\n"},
	// Rules 9 and 10: a bounds check one past what a wrong zone
	// fact believes, so its fold reads past the slice.
	{"rel-le", "H(): int {\n\tvar a: [8]u8 = [0; 8]\n\ts := a[0..5]\n\tn := s.Len()\n\tvar r := 0\n\tvar i := 0\n\tfor i <= n {\n\t\tr = r + int(s[i])\n\t\ti += 1\n\t}\n\treturn r\n}\n"},
	{"rel-neg", "H(): int {\n\tvar a: [8]u8 = [0; 8]\n\ts := a[0..5]\n\tvar r := 0\n\tvar i := 2\n\tfor _ in 0..4 {\n\t\tr = r + int(s[i])\n\t\ti -= 1\n\t}\n\treturn r\n}\n"},
	{"rel-slice", "H(): int {\n\tvar a: [8]u8 = [0; 8]\n\ts := a[0..5]\n\tvar r := 0\n\tvar i := 0\n\tfor i < 6 {\n\t\tr = r + int(s[i])\n\t\ti += 1\n\t}\n\treturn r\n}\n"},
	{"rel-kill", "H(): int {\n\tvar a: [8]u8 = [0; 8]\n\tvar s := a[0..6]\n\tn := s.Len()\n\ts = a[0..3]\n\tvar r := 0\n\tvar i := 0\n\tfor i < n {\n\t\tr = r + int(s[i])\n\t\ti += 1\n\t}\n\treturn r\n}\n"},
	{"rel-var", "H(): int {\n\tvar a: [8]u8 = [0; 8]\n\ts := a[0..4]\n\tvar m := s.Len()\n\tm = m * 2\n\tvar r := 0\n\tvar i := 0\n\tfor i < m {\n\t\tr = r + int(s[i])\n\t\ti += 1\n\t}\n\treturn r\n}\n"},
	// A zone fact proves arithmetic too: i <= n decides the
	// inner i < n only under a wrong zone, so x = 10 is all the intervals
	// see and x + 100 becomes AddNoWrap, wrapping where it traps (i == n).
	{"rel-arith", pvV + "F(s: []u8, i0: int): int {\n\ti := i0 & 1023\n\tn := s.Len()\n\tvar x: u8 = 200\n\tif i <= n {\n\t\tif i < n {\n\t\t\tx = 10\n\t\t}\n\t\ty: u8 = x + 100\n\t\treturn int(y) + int(s[0])\n\t}\n\treturn 0\n}\nH(): int {\n\tvar a: [4]u8 = [7; 4]\n\treturn F(a[0..V(3)], V(3))\n}\n"},
	{"safe-pair", "H(): int {\n\tvar a: [8]u8 = [1; 8]\n\ts := a[0..5]\n\tvar r := 0\n\tvar i := 0\n\tvar j := 5\n\tfor i <= j {\n\t\tr = r + int(s[i])\n\t\ti += 1\n\t\tj -= 1\n\t}\n\treturn r\n}\n"},
	// Rule 8's counter: a step of 0 on one path, a definition that
	// is no step, a wrapping step; each would bound the sum wrongly.
	{"acc-step0", "H(): int {\n\tvar s: i8 = 0\n\tvar c := 0\n\tvar k := 0\n\tfor c < 1 {\n\t\ts += 1\n\t\tk += 1\n\t\tif k == 200 {\n\t\t\tc += 1\n\t\t} else {\n\t\t\tc += 0\n\t\t}\n\t}\n\treturn int(s)\n}\n"},
	{"acc-def", "H(): int {\n\tvar s: i8 = 0\n\tvar c := 0\n\tvar k := 0\n\tfor c < 3 {\n\t\ts += 1\n\t\tc += 1\n\t\tk += 1\n\t\tif k < 200 {\n\t\t\tc = 0\n\t\t}\n\t}\n\treturn int(s)\n}\n"},
	{"acc-wrap", "H(): int {\n\tvar s: i8 = 120\n\tvar c: u8 = 250\n\tfor c != 5 {\n\t\ts += 1\n\t\tc +%= 1\n\t}\n\treturn int(s)\n}\n"},
	// Widening only on a retreating edge: the outer counter reaches the
	// inner loop over a forward edge, where it is joined, not widened: i is
	// [0, 1] there, so the cast is proved and x's + 127 stays checked and
	// traps at i = 1. A fact that kept i at 0 would prove it and wrap.
	{"widen-fwd", "H(): int {\n\tvar r := 0\n\tfor i in 0..2 {\n\t\tfor _ in 0..2 {\n\t\t\tx: i8 = Cast[i8](i) + 127\n\t\t\tr = int(x)\n\t\t}\n\t}\n\treturn r\n}\n"},
	// Rule 4 on a checked *: x * y did not trap with both factors at least
	// 0, so it lies in [0, MAX] and p / 2 in [0, 2^62 - 1]; + 2^62 + 1 can
	// still overflow, and does here (x = 2, y = 2^62 - 1). A product
	// believed at most MAX / 2 proves the add.
	{"mul-trap", pvV + "H(): int {\n\tx := V(2) & 3\n\ty := V(4611686018427387903) & 4611686018427387903\n\tp := x * y\n\treturn p / 2 + 4611686018427387905\n}\n"},
	// Rule 11: a result one past what a short range believes. 16 set lanes
	// plus MAX - 15, and each std.raw bit count's 64 (all ones, or 0 for
	// Ctz64 and Clz64) plus MAX - 63, overflow; a mutant that bounds the
	// result one lower proves the add, which then wraps.
	{"countset", "import std.simd\n\nH(): int {\n\tv := simd.Vec[u8, 16].Splat(7)\n\treturn (v == v).CountSet() + 9223372036854775792\n}\n"},
	{"popcount", "import std.raw\n\nH(): int {\n\treturn raw.Popcount64(18446744073709551615) + 9223372036854775744\n}\n"},
	{"ctz", "import std.raw\n\nH(): int {\n\treturn raw.Ctz64(0) + 9223372036854775744\n}\n"},
	{"clz", "import std.raw\n\nH(): int {\n\treturn raw.Clz64(0) + 9223372036854775744\n}\n"},
	// Rule 5 through t = a + c: a check exactly one past what a wrong
	// refinement believes. From i = 3 the header i + 4 <= 255 admits i =
	// 251, where r = i + 5 overflows before the header's i + 4 can; a
	// refinement one too tight above proves it, which wraps on to the
	// header's trap. (i starts in [0, 3], not at 3 alone: from a single
	// value the too-tight refinement is empty, the body dead to the
	// analysis, and nothing in it rewritten.) a + 4 >= -100 admits a =
	// -104, where a - 25 overflows below.
	{"step-high", pvV + "H(): int {\n\tvar i: u8 = Cast[u8](V(3) & 3)\n\tvar r: u8 = 0\n\tfor i + 4 <= 255 {\n\t\tr = i + 5\n\t\ti += 4\n\t}\n\treturn int(r)\n}\n"},
	{"step-low", pvV + "H(): int {\n\ta: i8 = Cast[i8](V(-104))\n\tif a + 4 >= -100 {\n\t\tz: i8 = a - 25\n\t\treturn int(z)\n\t}\n\treturn 0\n}\n"},
	// i = i + 10 then i <= 200: i is 200, its old value 190; refining the
	// new i through the old proves i + 60, which wraps.
	{"step-self", pvV + "H(): int {\n\tvar i: u8 = Cast[u8](V(190))\n\ti = i + 10\n\tif i <= 200 {\n\t\treturn int(i + 60)\n\t}\n\treturn 0\n}\n"},
	// a +% 100 wraps for a = 100 to -56 < 0: no bound on a; taken as exact
	// it bounds a by -101, and a + 28 is proved and wraps.
	{"step-wrap", pvV + "H(): int {\n\ta: i8 = Cast[i8](V(100))\n\tt := a +% 100\n\tif t < 0 {\n\t\tz: i8 = a + 28\n\t\treturn int(z)\n\t}\n\treturn 0\n}\n"},
	// t = a + 4 from a = 2, then a = 250: t <= 10 bounds the old a only.
	{"step-stale", pvV + "H(): int {\n\tb: u8 = Cast[u8](V(250))\n\tvar a: u8 = Cast[u8](V(2))\n\tt := a + 4\n\ta = b\n\tif t <= 10 {\n\t\tz: u8 = a + 249\n\t\treturn int(z)\n\t}\n\treturn 0\n}\n"},
	// a - 10 <= 100 is a <= 110, where a + 146 overflows; read as a + 10
	// it would be a <= 90.
	{"step-sub", pvV + "H(): int {\n\ta: u8 = Cast[u8](V(110))\n\tif a - 10 <= 100 {\n\t\tz: u8 = a + 146\n\t\treturn int(z)\n\t}\n\treturn 0\n}\n"},
	// t = 10 - a <= 5 is a >= 5: rule 5 takes no
	// c - a, so a - 6 stays checked and traps at a = 5; read as a - 10 the
	// refinement gives a >= 10, proves a - 6, which wraps to 255.
	{"step-cminusa", pvV + "H(): int {\n\ta: u8 = Cast[u8](V(5))\n\tt := 10 - a\n\tif t <= 5 {\n\t\tz: u8 = a - 6\n\t\treturn int(z)\n\t}\n\treturn 0\n}\n"},
	// Rule 12: a read one past a slice of an array, whose fold under a
	// wrong split reads a[5] (1) where the pass right panics. i += 1 after
	// the stored condition: the join assigns i, so it is no split join.
	{"split-stmt", pvV + "H(): int {\n\tvar a: [8]u8 = [1; 8]\n\ts := a[0..5]\n\tn := s.Len()\n\tvar i := V(4)\n\tc := i < n && s[i] != 0\n\ti += 1\n\tif c {\n\t\treturn int(s[i])\n\t}\n\treturn 0\n}\n"},
	// !(i < len && i >= 0) is true at i = 5: the negation's true edge has
	// r false, so i may be 5.
	{"split-not", pvV + "H(): int {\n\tvar a: [8]u8 = [1; 8]\n\ts := a[0..5]\n\ti := V(5)\n\tif !(i < s.Len() && i >= 0) {\n\t\treturn int(s[i])\n\t}\n\treturn 0\n}\n"},
	// The false edge of i < len && i >= 0 is reached with i = 5.
	{"split-flip", pvV + "H(): int {\n\tvar a: [8]u8 = [1; 8]\n\ts := a[0..5]\n\ti := V(5)\n\tif i < s.Len() && i >= 0 {\n\t\treturn 0\n\t}\n\treturn int(s[i])\n}\n"},
	// The && joins inside a loop whose counter grows: a kept state never
	// updated holds i at 0, proves y's add, and wraps at i = 6.
	{"split-first", pvV + "H(): int {\n\tvar i := 0\n\tvar r := 0\n\tfor i < V(10) && i >= 0 {\n\t\ty: u8 = Cast[u8](i) + 250\n\t\tr = int(y)\n\t\ti += 1\n\t}\n\treturn r\n}\n"},
	// Programs that never fail: the pass on, off and mutated agree.
	{"safe-sum", "H(): int {\n\tvar a: [8]i32 = [0; 8]\n\tfor i in 0..8 {\n\t\ta[i] = Cast[i32](i * 1000)\n\t}\n\tvar s: i64 = 0\n\tfor i in 0..a.Len() {\n\t\ts += i64(a[i])\n\t}\n\treturn int(s)\n}\n"},
}

// TestPC_MutationCaught: the pass agrees with no pass on every edge program
// (same value, or the same panic at the same site), and each wrong fact
// changes some edge program's outcome, so the tests above catch it.
func TestPC_MutationCaught(t *testing.T) {
	right := map[string]string{}
	for _, e := range pcEdge {
		on := pcEval(t, e.src, nil)
		off := pcEval(t, e.src, func(c *Config) { c.NoRanges = true })
		if on != off {
			t.Errorf("%s: pass on gives %q, off gives %q", e.name, on, off)
		}
		if !strings.HasPrefix(e.name, "safe") && !strings.Contains(on, " panic ") {
			t.Errorf("%s: an edge program must panic, got %q", e.name, on)
		}
		right[e.name] = on
	}
	muts := []struct {
		name string
		m    rangeMut
	}{
		{"lt-tight", mutLtOffByOne}, {"mask-1", mutMaskMinusOne}, {"accum-iter", mutAccumIter},
		{"content-store", mutContentStore}, {"trunc-nofit", mutTruncNoFit}, {"shr-low", mutShrLow},
		{"len-short", mutLenShort}, {"sub-low", mutSubLow}, {"mul-high", mutMulHigh},
		{"zone-le-as-lt", mutZoneLeAsLt}, {"flag-free", mutFlagFree}, {"len-eq-short", mutLenEqShort},
		{"slice-len-long", mutSliceLenLong}, {"sym-no-kill", mutSymNoKill}, {"var-no-kill", mutVarNoKill},
		{"counter-step0", mutCounterStep0}, {"counter-any-def", mutCounterAnyDef}, {"counter-wrap", mutCounterWrap},
		{"mul-trap-half", mutMulTrapHalf}, {"countset-short", mutCountSetShort}, {"bits-short", mutBitsShort},
		{"step-high", mutStepHigh}, {"step-low", mutStepLow}, {"step-self", mutStepSelf},
		{"step-wrap", mutStepWrap}, {"step-stale", mutStepStale}, {"step-sub-sign", mutStepSubSign},
		{"split-stmt", mutSplitStmt}, {"split-first", mutSplitFirst}, {"split-not", mutSplitNot}, {"split-flip", mutSplitFlip},
		{"step-cminusa", mutStepCMinusA},
	}
	for _, m := range muts {
		caught := ""
		for _, e := range pcEdge {
			got := pcEval(t, e.src, func(c *Config) { c.rangeMut = m.m })
			if got != right[e.name] {
				caught = e.name
				break
			}
		}
		if caught == "" {
			t.Errorf("mutant %s is caught by no edge program", m.name)
		} else {
			t.Logf("mutant %s caught by %s", m.name, caught)
		}
	}
	// Mutants that must also be caught by a named program, through a path
	// the first catch does not show: a wrong zone fact drops an overflow
	// trap (rel-arith), not only a bounds check (rel-le).
	for _, a := range []struct {
		prog string
		m    rangeMut
	}{{"rel-arith", mutZoneLeAsLt}, {"popcount", mutBitsShort}, {"ctz", mutBitsShort}, {"clz", mutBitsShort},
		{"step-high", mutStepHigh}, {"step-low", mutStepLow}, {"step-self", mutStepSelf},
		{"step-wrap", mutStepWrap}, {"step-stale", mutStepStale}, {"step-sub", mutStepSubSign},
		{"split-stmt", mutSplitStmt}, {"split-first", mutSplitFirst}, {"split-not", mutSplitNot}, {"split-flip", mutSplitFlip},
		{"step-cminusa", mutStepCMinusA}} {
		var src string
		for _, e := range pcEdge {
			if e.name == a.prog {
				src = e.src
			}
		}
		if got := pcEval(t, src, func(c *Config) { c.rangeMut = a.m }); got == right[a.prog] {
			t.Errorf("%s does not catch its mutant: both give %q", a.prog, got)
		}
	}
	// Mutants of generic bodies, caught by a named program of pcGeneric run
	// over instance bodies (rangegeneric_test.go): rule 11's lane count N
	// as no value (a const parameter) bounds CountSet by 64, not 32.
	for _, a := range []struct {
		prog, call string
		m          rangeMut
	}{{"countset/generic", pcAllSet64, mutCountSetGen}, {"countset/generic", pcAllSet64, mutCountSetShort}} {
		var src string
		for _, g := range pcGeneric {
			if g.name == a.prog {
				src = fmt.Sprintf(g.src, a.call)
			}
		}
		want := pcEvalInst(t, src, nil)
		if off := pcEvalInst(t, src, func(c *Config) { c.NoRanges = true }); off != want || !strings.Contains(want, " panic ") {
			t.Errorf("%s %s: pass on gives %q, off %q; want the same panic", a.prog, a.call, want, off)
		}
		if got := pcEvalInst(t, src, func(c *Config) { c.rangeMut = a.m }); got == want {
			t.Errorf("%s %s does not catch its mutant: both give %q", a.prog, a.call, got)
		}
	}
	// Loop versioning: the versions agree on every edge program above, and
	// their own mutants are caught by their edge programs
	// (rangeversion_test.go).
	for _, e := range pcEdge {
		if got, _ := pvEval(t, e.src, VersionOptions{}); got != right[e.name] {
			t.Errorf("%s: versions on give %q, want %q", e.name, got, right[e.name])
		}
	}
	pvMutantsCaught(t)
}

// pcKernels are the tests/bench same-table Mk sources.
func pcKernels(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", "tests", "bench", "same", "*", "mk*", "*.mk"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no bench kernels: %v", err)
	}
	var out []string
	for _, f := range files {
		if filepath.Base(f) != "pkg.mk" {
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out
}

func kernelName(f string) string {
	return filepath.Base(filepath.Dir(filepath.Dir(f))) + "/" + filepath.Base(filepath.Dir(f))
}

func readKernel(t *testing.T, f string) string {
	t.Helper()
	data, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(data), "\r\n", "\n")
}

// pcShipsFold reports whether a build folds proved bounds checks. The
// driver does, after borrow accepts a body (internal/driver evaluator.go
// lowerBody: LowerProved, then ProvedBounds.Fold, which
// TestPM_ProvedBoundsMatchFold holds equal to FoldProvedBounds).
// TestPC_BenchKernels' "on" side counts what a build ships, so it folds
// only when this is true, and TestFoldNotInBuild keeps it equal to
// the tree.
const pcShipsFold = true

// pcKernelWant is, per kernel, its checks lowered with the pass off ("off")
// and left in what a build ships with the pass on ("on": Lower, plus the
// bounds fold only when pcShipsFold), summed over its bodies, then the
// bounds checks FoldProvedBounds leaves ("fold": what the driver ships):
// the record of what the analysis removes. A change is read, never updated
// blindly.
var pcKernelWant = map[string]string{
	"binarytrees/mk":  "binarytrees/mk: off add=12 sub=3 mul=0 nowrap=0 casts=0 bounds=4/4 | on add=9 sub=0 mul=0 nowrap=6 casts=0 bounds=4/4 | fold bounds=4/4",
	"fannkuch/mk":     "fannkuch/mk: off add=7 sub=5 mul=1 nowrap=0 casts=0 bounds=17/17 | on add=4 sub=3 mul=1 nowrap=5 casts=0 bounds=5/17 | fold bounds=5/17",
	"fasta/mk":        "fasta/mk: off add=15 sub=3 mul=5 nowrap=0 casts=0 bounds=12/12 | on add=8 sub=0 mul=5 nowrap=10 casts=0 bounds=9/12 | fold bounds=9/12",
	"fmtints/mk":      "fmtints/mk: off add=6 sub=2 mul=1 nowrap=0 casts=5 bounds=11/11 | on add=2 sub=0 mul=1 nowrap=6 casts=1 bounds=7/11 | fold bounds=7/11",
	"intsum/mk":       "intsum/mk: off add=2 sub=0 mul=0 nowrap=0 casts=2 bounds=6/6 | on add=1 sub=0 mul=0 nowrap=1 casts=1 bounds=4/6 | fold bounds=4/6",
	"intsum/mkwrap":   "intsum/mkwrap: off add=0 sub=0 mul=0 nowrap=0 casts=2 bounds=6/6 | on add=0 sub=0 mul=0 nowrap=0 casts=1 bounds=4/6 | fold bounds=4/6",
	"jsonscan/mk":     "jsonscan/mk: off add=12 sub=1 mul=0 nowrap=0 casts=0 bounds=10/10 | on add=1 sub=1 mul=0 nowrap=11 casts=0 bounds=2/10 | fold bounds=2/10",
	"knucleotide/mk":  "knucleotide/mk: off add=9 sub=8 mul=5 nowrap=0 casts=7 bounds=19/19 | on add=9 sub=8 mul=5 nowrap=0 casts=5 bounds=12/19 | fold bounds=12/19",
	"mandelbrot/mk":   "mandelbrot/mk: off add=5 sub=3 mul=8 nowrap=0 casts=0 bounds=1/1 | on add=4 sub=3 mul=8 nowrap=1 casts=0 bounds=1/1 | fold bounds=1/1",
	"matmul/mk":       "matmul/mk: off add=6 sub=0 mul=11 nowrap=0 casts=2 bounds=10/10 | on add=6 sub=0 mul=11 nowrap=0 casts=2 bounds=10/10 | fold bounds=10/10",
	"nbody/mk":        "nbody/mk: off add=15 sub=10 mul=29 nowrap=0 casts=1 bounds=49/49 | on add=13 sub=10 mul=29 nowrap=2 casts=1 bounds=6/49 | fold bounds=6/49",
	"sieve/mk":        "sieve/mk: off add=6 sub=0 mul=1 nowrap=0 casts=0 bounds=5/5 | on add=4 sub=0 mul=1 nowrap=2 casts=0 bounds=2/5 | fold bounds=2/5",
	"sieve/mkwrap":    "sieve/mkwrap: off add=3 sub=0 mul=1 nowrap=0 casts=0 bounds=5/5 | on add=3 sub=0 mul=1 nowrap=0 casts=0 bounds=2/5 | fold bounds=2/5",
	"sortints/mk":     "sortints/mk: off add=47 sub=51 mul=6 nowrap=0 casts=6 bounds=102/102 | on add=31 sub=22 mul=2 nowrap=49 casts=2 bounds=42/102 | fold bounds=42/102",
	"spectralnorm/mk": "spectralnorm/mk: off add=9 sub=0 mul=6 nowrap=0 casts=1 bounds=10/10 | on add=9 sub=0 mul=6 nowrap=0 casts=1 bounds=3/10 | fold bounds=3/10",
}

// TestPC_BenchKernels counts the removed checks on every tests/bench
// kernel, in what a build ships (the "on" side includes FoldProvedBounds
// only when a build calls it). A kernel the harness cannot build fails.
func TestPC_BenchKernels(t *testing.T) {
	var got []string
	for _, f := range pcKernels(t) {
		src, name := readKernel(t, f), kernelName(f)
		on := pcLower(t, src, pcOpt{bin: true, fold: pcShipsFold})
		fold := pcLower(t, src, pcOpt{bin: true, fold: true})
		off := pcLower(t, src, pcOpt{bin: true, tweak: func(c *Config) { c.NoRanges = true }})
		var a, b, c pcCounts
		for _, k := range on.keys {
			a.plus(off.counts(k))
			b.plus(on.counts(k))
			c.plus(fold.counts(k))
		}
		line := fmt.Sprintf("%s: off %s | on %s | fold bounds=%d/%d", name, a, b, c.boundsLeft, c.bounds)
		got = append(got, line)
		if w, ok := pcKernelWant[name]; !ok || w != line {
			t.Errorf("got  %s\nwant %s", line, w)
		}
	}
	t.Log("\n" + strings.Join(got, "\n"))
}

// TestFoldNotInBuild: TestPC_BenchKernels' "on" side counts what a
// build ships, so pcShipsFold must say whether a non-test file of internal/
// or cmd/ calls FoldProvedBounds (or LowerProved). If that changes, this
// test fails: set pcShipsFold to match and replace pcKernelWant's lines
// with TestPC_BenchKernels' output, read.
func TestFoldNotInBuild(t *testing.T) {
	var calls []string
	for _, root := range []string{filepath.Join("..", "..", "internal"), filepath.Join("..", "..", "cmd")} {
		err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return err
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			for i, ln := range strings.Split(string(data), "\n") {
				if c := strings.Index(ln, "//"); c >= 0 {
					ln = ln[:c]
				}
				// LowerProved and ProvedBounds.Fold are the driver's form of
				// the fold.
				if (strings.Contains(ln, "FoldProvedBounds(") || strings.Contains(ln, "LowerProved(")) && !strings.HasPrefix(strings.TrimSpace(ln), "func ") {
					calls = append(calls, fmt.Sprintf("%s:%d", filepath.ToSlash(p), i+1))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if (len(calls) > 0) != pcShipsFold {
		t.Errorf("pcShipsFold is %v, but the calls of FoldProvedBounds outside tests are %v: update pcShipsFold and pcKernelWant (see pcShipsFold)", pcShipsFold, calls)
	}
}

// TestPC_SubMulNoWrap: a proved - or * becomes SubNoWrap or MulNoWrap, as a
// proved + becomes AddNoWrap, on a signed and an unsigned type; a written
// -% or *% stays SubWrap or MulWrap.
func TestPC_SubMulNoWrap(t *testing.T) {
	for _, c := range []struct{ src, want, not string }{
		{"F(x: u8): i32 {\n\treturn i32(x) - 300\n}\n", "SubNoWrap(", "SubWrap("},
		{"F(x: u8): u32 {\n\treturn u32(x | 8) - 8\n}\n", "SubNoWrap(", "SubWrap("},
		{"F(x: u8, y: u8): u32 {\n\treturn u32(x) * u32(y)\n}\n", "MulNoWrap(", "MulWrap("},
		{"F(x: i8, y: i8): i32 {\n\treturn i32(x) * i32(y)\n}\n", "MulNoWrap(", "MulWrap("},
		{"F(x: u32): u32 {\n\treturn x -% 1\n}\n", "SubWrap(", "SubNoWrap("},
		{"F(x: u32): u32 {\n\treturn (x & 7) *% 3\n}\n", "MulWrap(", "MulNoWrap("},
	} {
		got := pcLower(t, c.src, pcOpt{}).print("F")
		if !strings.Contains(got, c.want) || strings.Contains(got, c.not) {
			t.Errorf("want %s and no %s in\n%s", c.want, c.not, got)
		}
	}
}

// TestPC_TwoOpRefineStep verifies interval inversion through two-local exact
// additions and subtractions (t = a + b, t = a - b), including soundness
// against wrapping arithmetic, stale operands, and overflow traps.
func TestPC_TwoOpRefineStep(t *testing.T) {
	// Indexing s[base + b] narrows base via s.Len() <= 2^56/8, proving
	// subsequent base + c and base + a cannot overflow (vm Exec pattern).
	vmLike := `
F(s: mut []i64, base: int, a: u8, b: u8, c: u8) {
	ia := int(a)
	ib := int(b)
	ic := int(c)
	s[base + ia] = s[base + ib] +% s[base + ic]
}
`
	got := pcLower(t, vmLike, pcOpt{})
	if c := got.counts("F"); c.add != 1 || c.nowrap != 2 {
		t.Errorf("vmLike: got add=%d nowrap=%d, want add=1 nowrap=2\n%s", c.add, c.nowrap, got.print("F"))
	}

	// Same for subtraction: s[base - ib] bounds base, proving base - ic no-wrap.
	subLike := `
F(s: []i64, base: int, b: u8, c: u8): i64 {
	ib := int(b)
	ic := int(c)
	return s[base - ib] +% s[base - ic]
}
`
	gotSub := pcLower(t, subLike, pcOpt{})
	if c := gotSub.counts("F"); c.sub != 1 || c.nowrap != 1 {
		t.Errorf("subLike: got sub=%d nowrap=%d, want sub=1 nowrap=1\n%s", c.sub, c.nowrap, gotSub.print("F"))
	}

	// Eval equivalence on edge inputs: wrapping +% must NOT narrow base,
	// stale overwrite of base must NOT narrow the new base, and actual
	// overflow on base + b must still trap identically.
	for _, tc := range []struct {
		name, src string
		wantPanic bool
	}{
		{
			name: "wrap-must-not-refine",
			src: pvV + `
H(): int {
	var a: [4]int = [1, 2, 3, 4]
	s := a[0..V(4)]
	base := V(9223372036854775807)
	off := V(-9223372036854775807)
	x := s[base +% off]
	return x + (base + V(1))
}
`,
			wantPanic: true,
		},
		{
			name: "stale-base-must-not-refine",
			src: pvV + `
H(): int {
	var a: [4]int = [1, 2, 3, 4]
	s := a[0..V(4)]
	var base := V(0)
	off := V(1)
	t := base + off
	base = V(9223372036854775807)
	if t < s.Len() {
		return base + off
	}
	return 0
}
`,
			wantPanic: true,
		},
		{
			name: "exact-add-ok",
			src: pvV + `
H(): int {
	var a: [8]i64 = [10, 20, 30, 40, 50, 60, 70, 80]
	s := a[0..V(8)]
	base := V(2)
	b := Cast[u8](V(1))
	c := Cast[u8](V(3))
	return Cast[int](s[base + int(b)] +% s[base + int(c)])
}
`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			off := pcEval(t, tc.src, func(c *Config) { c.NoRanges = true })
			on := pcEval(t, tc.src, nil)
			if on != off {
				t.Fatalf("eval mismatch: on=%q off=%q", on, off)
			}
			if tc.wantPanic && !strings.Contains(on, "panic") {
				t.Fatalf("expected panic, got %q", on)
			}
		})
	}
}
