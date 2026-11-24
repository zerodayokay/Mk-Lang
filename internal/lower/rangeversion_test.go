// Why: the codegen-only loop versions (rangeversion.go: a hoisted bounds
// test; rangereduce.go: checked reductions) run a fast copy of a loop under
// a guard, and a wrong guard silently skips a check.
// What: TestPV_Agrees, TestPV_BenchKernels, TestPC_VersionAgrees and
// FuzzVersionAgrees, TestPV_Off, and the pvEdge programs.
// How: each versioned body validates and leaves its input as it was; eval,
// the reference semantics, gives every program the same value or the same
// panic at the same site with the versions on and off; the edge programs
// fail exactly one past what a wrong guard believes, so each mutant changes
// some program's outcome; a generator of affine-index loops and reductions
// agrees on and off; and the bench kernels' loops that version are pinned.

package lower

import (
	"fmt"
	"math/rand"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"mk/internal/mir"
)

// pvVersion versions mb as the driver does, checks that the result
// validates and that mb is unchanged, and counts the versioned loops in l.
func pvVersion(t testing.TB, l *lowered, cfg *Config, mb *mir.Body, opt VersionOptions) *mir.Body {
	t.Helper()
	p := &mir.Printer{Types: l.w.tab, Items: l.w.items}
	before := p.Print(mb)
	nb, nbnd, nred := versionLoops(cfg, mb, opt)
	if after := p.Print(mb); after != before {
		t.Fatalf("VersionLoops changed its input:\n%s\nwas\n%s", after, before)
	}
	if nb != mb {
		env := &mir.Env{Types: cfg.Types, Callee: cfg.callee, Known: cfg.Known}
		if err := nb.Validate(env); err != nil {
			t.Fatalf("versioned body does not validate: %v\n%s", err, p.Print(nb))
		}
	}
	l.vBounds += nbnd
	l.vReduce += nred
	return nb
}

// pvEval is pcEval with the versions (opt) on.
func pvEval(t *testing.T, src string, opt VersionOptions) (string, *lowered) {
	t.Helper()
	l := pcLower(t, src, pcOpt{fold: true, version: &opt})
	return pcRun(l), l
}

const pvMin = "-9223372036854775807 - 1"

// pvV hides a value from the range analysis (it is intraprocedural), so
// the edge programs' checks reach the versions unproved.
const pvV = "V(x: int): int {\n\treturn x\n}\n"

// pvEdge are the bounds and reduction versions' edge programs (H(): int). An
// edge program fails, with the versions right, at the same site as without
// them; with its mutant it reads past a slice or wraps where the original
// traps. bounds and reduce are how many loops each rewrite must version.
var pvEdge = []struct {
	name, src      string
	bounds, reduce int
}{
	// Bounds: the last index is the length (vmutHiLe).
	{"ver-hi", pvV + "H(): int {\n\tvar a: [8]int = [1; 8]\n\ts := a[0..V(5)]\n\tvar r := 0\n\tfor i in 0..V(6) {\n\t\tr = r +% s[i]\n\t}\n\treturn r\n}\n", 1, 0},
	// The first index is -1 (vmutNoLow).
	{"ver-low", pvV + "H(): int {\n\tvar a: [8]int = [1; 8]\n\ts := a[0..V(5)]\n\tvar r := 0\n\tfor i in 0..V(4) {\n\t\tr = r +% s[i - 1]\n\t}\n\treturn r\n}\n", 1, 0},
	// MIN + MIN wraps to 0: the checked i + b traps (vmutNoOvf).
	{"ver-ovf", pvV + "H(): int {\n\tvar a: [8]int = [1; 8]\n\ts := a[0..V(5)]\n\tb := V(" + pvMin + ")\n\tvar r := 0\n\tfor i in b..b + 3 {\n\t\tr = r +% s[i + b]\n\t}\n\treturn r\n}\n", 1, 0},
	// The slice is assigned in the loop (vmutLenAnyDef).
	{"ver-len", pvV + "H(): int {\n\tvar a: [8]int = [1; 8]\n\tvar s := a[0..V(8)]\n\tt := a[0..V(2)]\n\tvar r := 0\n\tfor i in 0..V(4) {\n\t\tr = r +% s[i]\n\t\ts = t\n\t}\n\treturn r\n}\n", 0, 0},
	// The base is assigned in the loop (vmutBaseAnyDef).
	{"ver-base", pvV + "H(): int {\n\tvar a: [8]int = [1; 8]\n\ts := a[0..V(5)]\n\tvar b := V(0)\n\tb2 := V(2)\n\tvar r := 0\n\tfor i in 0..V(4) {\n\t\tr = r +% s[i + b]\n\t\tb = b2\n\t}\n\treturn r\n}\n", 0, 0},
	// A stepped counter read after its step (vmutIVAfter).
	{"ver-step", pvV + "H(): int {\n\tvar a: [8]int = [1; 8]\n\ts := a[0..V(5)]\n\tvar p := V(0)\n\tvar r := 0\n\tfor _ in 0..V(5) {\n\t\tp += 1\n\t\tr = r +% s[p]\n\t}\n\treturn r\n}\n", 0, 0},
	// Reductions: N half the iterations admits 9 terms of 2^60 - 1
	// (vmutTripShort).
	{"red-trip", pvV + "H(): int {\n\tvar a: [8]int = [0; 8]\n\tfor i in 0..8 {\n\t\ta[i] = V(1152921504606846975)\n\t}\n\tvar s := V(1152921504606846975)\n\tfor i in 0..V(8) {\n\t\ts += a[i]\n\t}\n\treturn s\n}\n", 1, 1},
	// The initial value is no term (vmutNoInit).
	{"red-init", pvV + "H(): int {\n\tvar a: [8]int = [0; 8]\n\tfor i in 0..8 {\n\t\ta[i] = V(1)\n\t}\n\tvar s := V(9223372036854775804)\n\tfor i in 0..V(8) {\n\t\ts += a[i]\n\t}\n\treturn s\n}\n", 1, 1},
	// Terms up to 4B (vmutLimitHigh): 3 * 2^61 - 1 twice overflows.
	{"red-lim", pvV + "H(): int {\n\tvar a: [3]int = [0; 3]\n\tfor i in 0..3 {\n\t\ta[i] = V(6917529027641081855)\n\t}\n\tvar s := V(0)\n\tfor i in 0..V(3) {\n\t\ts += a[i]\n\t}\n\treturn s\n}\n", 1, 1},
	// s -= x: MIN - 1 traps.
	{"red-sub", pvV + "H(): int {\n\tvar a: [4]int = [0; 4]\n\tfor i in 0..4 {\n\t\ta[i] = V(1)\n\t}\n\tvar s := V(" + pvMin + " + 2)\n\tfor i in 0..V(4) {\n\t\ts -= a[i]\n\t}\n\treturn s\n}\n", 1, 1},
	// Programs that never fail: the versions on and off agree. One large
	// term sends safe-restore's loop to the fallback, which does not
	// overflow when it starts from s0 (vmutNoRestore).
	{"safe-restore", pvV + "H(): int {\n\tvar a: [3]int = [0; 3]\n\tfor i in 0..3 {\n\t\ta[i] = V(1)\n\t}\n\ta[0] = V(4611686018427387904)\n\tvar s := V(0)\n\tfor i in 0..V(3) {\n\t\ts += a[i]\n\t}\n\treturn s\n}\n", 1, 1},
	{"safe-mat", pvV + "H(): int {\n\tvar a: [16]int = [3; 16]\n\ts := a[0..V(16)]\n\tn := V(4)\n\tvar r := 0\n\tfor i in 0..n {\n\t\tfor j in 0..n {\n\t\t\tr = r +% s[i * n + j] *% j\n\t\t}\n\t}\n\treturn r\n}\n", 1, 0},
	{"safe-step", pvV + "H(): int {\n\tvar a: [8]int = [2; 8]\n\tvar d: [8]int = [0; 8]\n\ts := a[0..V(8)]\n\tvar p := V(1)\n\tfor j in V(2)..V(6) {\n\t\td[p] = s[j]\n\t\tp += 1\n\t}\n\treturn d[1] + d[4] + p\n}\n", 1, 0},
	{"safe-red", pvV + "H(): int {\n\tvar a: [8]int = [0; 8]\n\tfor i in 0..8 {\n\t\ta[i] = V(i * 1000 - 3000)\n\t}\n\tvar s := V(5)\n\tfor i in 0..a.Len() {\n\t\ts += a[i]\n\t}\n\treturn s\n}\n", 0, 1},
	{"safe-red-u", pvV + "H(): int {\n\tvar a: [8]u64 = [0; 8]\n\tfor i in 0..8 {\n\t\ta[i] = Cast[u64](V(18446744073709551))\n\t}\n\tvar s: u64 = Cast[u64](V(7))\n\tfor i in 0..V(8) {\n\t\ts += a[i]\n\t}\n\treturn Cast[int](s & 1023)\n}\n", 1, 1},
	{"safe-red-big", pvV + "H(): int {\n\tvar a: [3]int = [0; 3]\n\tfor i in 0..3 {\n\t\ta[i] = V(1)\n\t}\n\ta[1] = V(4611686018427387904)\n\tvar s := V(-7)\n\tfor i in 0..V(3) {\n\t\ts -= a[i]\n\t}\n\treturn s\n}\n", 1, 1},
	// The other 64-bit types the rewrites admit: a uint and an i64
	// accumulator (uint overflows past 2^64 - 1), and an i64 counter.
	{"red-uint", pvV + "H(): int {\n\tvar a: [4]uint = [0; 4]\n\tfor i in 0..4 {\n\t\ta[i] = Cast[uint](V(9223372036854775807))\n\t}\n\tvar s: uint = Cast[uint](V(5))\n\tfor i in 0..V(4) {\n\t\ts += a[i]\n\t}\n\treturn Cast[int](s & 1023)\n}\n", 1, 1},
	{"safe-uint", pvV + "H(): int {\n\tvar a: [4]uint = [0; 4]\n\tfor i in 0..4 {\n\t\ta[i] = Cast[uint](V(1000 + i))\n\t}\n\tvar s: uint = Cast[uint](V(5))\n\tfor i in 0..V(4) {\n\t\ts += a[i]\n\t}\n\treturn Cast[int](s)\n}\n", 1, 1},
	{"red-i64", pvV + "H(): int {\n\tvar a: [4]i64 = [0; 4]\n\tfor i in 0..4 {\n\t\ta[i] = Cast[i64](V(4611686018427387904))\n\t}\n\tvar s: i64 = 0\n\tfor i in 0..V(4) {\n\t\ts += a[i]\n\t}\n\treturn Cast[int](s)\n}\n", 1, 1},
	{"ver-i64", pvV + "H(): int {\n\tvar a: [8]int = [1; 8]\n\ts := a[0..V(5)]\n\tlo: i64 = Cast[i64](V(0))\n\thi: i64 = Cast[i64](V(6))\n\tvar r := 0\n\tfor i in lo..hi {\n\t\tr = r +% s[Cast[int](i)]\n\t}\n\treturn r\n}\n", 0, 0},
	// Pass 1 runs in chunks, so a trap early in a loop of 2^40
	// iterations comes after one chunk, within pcRun's fuel (vmutNoChunk
	// runs out of fuel instead). The terms i *% 2^53 overflow s near i = 46.
	{"red-late", pvV + "H(): int {\n\tvar s := V(0)\n\tfor i in 0..V(1099511627776) {\n\t\ts += i *% 9007199254740992\n\t}\n\treturn s\n}\n", 0, 1},
	// With chunks of 3, the first two pass and the third fails: the
	// original reruns from s = 6 at i = 6 and traps there
	// (vmutRestoreFirst restarts it from s0 = 0 and returns MAX).
	{"red-chunk", pvV + "H(): int {\n\tvar a: [8]int = [1; 8]\n\ta[6] = V(9223372036854775807)\n\ta[7] = V(0)\n\tvar s := V(0)\n\tfor i in 0..V(8) {\n\t\ts += a[i]\n\t}\n\treturn s\n}\n", 1, 1},
	// s is read besides its add, and a fact about it (s >= 0,
	// so s < 8 proves b[s]) holds only in states the original reaches;
	// pass 1 under vmutReadsAny wraps s to -2 and reads b[-2].
	{"red-reads", pvV + "H(): int {\n\tvar a: [4]int = [0; 4]\n\tfor i in 0..4 {\n\t\ta[i] = V(9223372036854775807)\n\t}\n\tvar b: [8]int = [1; 8]\n\tvar s := 0\n\tfor i in 0..V(4) {\n\t\ts += a[i] & 9223372036854775807\n\t\tif s < 8 {\n\t\t\t_ = b[s]\n\t\t}\n\t}\n\treturn s\n}\n", 1, 0},
	// p is written in the loop and live at its header, so the loop is not
	// restartable; vmutAnyLive versions it, and the fallback (a[0] is past
	// B) counts p twice.
	{"safe-live", pvV + "H(): int {\n\tvar a: [3]int = [1; 3]\n\ta[0] = V(4611686018427387904)\n\tvar p := 0\n\tvar s := V(0)\n\tfor i in 0..V(3) {\n\t\ts += a[i]\n\t\tp = p +% 1\n\t}\n\treturn s +% p\n}\n", 1, 0},
	// No chunk may run past the bound (vmutChunkPastHi sums 0..8).
	{"safe-iota", pvV + "H(): int {\n\tvar s := V(0)\n\tfor i in 0..V(7) {\n\t\ts += i\n\t}\n\treturn s\n}\n", 0, 1},
}

// pvChunks are the pass-1 chunks the tests run the reduction version with:
// the default, and chunks small enough that the edge programs cross their
// boundaries.
var pvChunks = []uint64{0, 1, 3}

// TestPV_Agrees: every edge program gives the same outcome with the
// versions on as off, with every chunk of pvChunks, and versions as many
// loops as it says.
func TestPV_Agrees(t *testing.T) {
	for _, e := range pvEdge {
		off := pcEval(t, e.src, nil)
		on, l := pvEval(t, e.src, VersionOptions{})
		if on != off {
			t.Errorf("%s: versions on give %q, off %q", e.name, on, off)
		}
		for _, ch := range pvChunks[1:] {
			if got, _ := pvEval(t, e.src, VersionOptions{chunk: ch}); got != off {
				t.Errorf("%s: chunk %d gives %q, off %q", e.name, ch, got, off)
			}
		}
		if l.vBounds != e.bounds || l.vReduce != e.reduce {
			t.Errorf("%s: versioned %d bounds and %d reduction loops, want %d and %d", e.name, l.vBounds, l.vReduce, e.bounds, e.reduce)
		}
		if !strings.HasPrefix(e.name, "safe") && !strings.Contains(on, " panic ") {
			t.Errorf("%s: an edge program must panic, got %q", e.name, on)
		}
		// Each rewrite alone agrees too.
		for _, o := range []VersionOptions{{NoReduce: true}, {NoBounds: true}} {
			if got, _ := pvEval(t, e.src, o); got != off {
				t.Errorf("%s: %+v gives %q, off %q", e.name, o, got, off)
			}
		}
	}
}

// pvMuts are the versions' wrong guards and rules (versionMut).
var pvMuts = []struct {
	name string
	m    versionMut
}{
	{"ver-hi-le", vmutHiLe}, {"ver-no-low", vmutNoLow}, {"ver-no-ovf", vmutNoOvf},
	{"ver-len-any-def", vmutLenAnyDef}, {"ver-base-any-def", vmutBaseAnyDef}, {"ver-iv-after", vmutIVAfter},
	{"red-trip-short", vmutTripShort}, {"red-no-init", vmutNoInit}, {"red-limit-high", vmutLimitHigh},
	{"red-no-restore", vmutNoRestore}, {"red-no-chunk", vmutNoChunk}, {"red-chunk-past-hi", vmutChunkPastHi},
	{"red-restore-first", vmutRestoreFirst}, {"red-any-live", vmutAnyLive}, {"red-reads-any", vmutReadsAny},
}

// pvMutantsCaught is TestPC_MutationCaught's part for the versions: each
// mutant changes some edge program's outcome against the right one.
func pvMutantsCaught(t *testing.T) {
	right := map[string]string{}
	for _, e := range pvEdge {
		right[e.name] = pcEval(t, e.src, nil)
	}
	for _, m := range pvMuts {
		caught := ""
	search:
		for _, ch := range []uint64{0, 3} {
			for _, e := range pvEdge {
				if got, _ := pvEval(t, e.src, VersionOptions{mut: m.m, chunk: ch}); got != right[e.name] {
					caught = fmt.Sprintf("%s (chunk %d)", e.name, ch)
					break search
				}
			}
		}
		if caught == "" {
			t.Errorf("mutant %s is caught by no edge program", m.name)
		} else {
			t.Logf("mutant %s caught by %s", m.name, caught)
		}
	}
}

// pvCodegenKernels are the tests/bench codegen kernels' Run functions
// (the harness around them imports modules the corpus prelude lacks).
func pvCodegenKernels(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", "tests", "bench", "codegen", "*", "*.mk"))
	if err != nil {
		t.Fatal(err)
	}
	run := regexp.MustCompile(`(?ms)^Run\(.*?^}\n`)
	out := map[string]string{}
	for _, f := range files {
		if filepath.Base(f) == "pkg.mk" {
			continue
		}
		if m := run.FindString(readKernel(t, f)); m != "" {
			out["codegen/"+filepath.Base(filepath.Dir(f))] = m
		}
	}
	if len(out) == 0 {
		t.Fatal("no codegen kernels")
	}
	return out
}

// pvKernelWant is, per kernel, the loops each rewrite versions: the record
// of where they apply. A change is read, never updated blindly.
var pvKernelWant = map[string]string{
	"codegen/fmtints": "bounds=1 reduce=1",
	"codegen/intsum":  "bounds=0 reduce=1",
	"codegen/matmul":  "bounds=2 reduce=0",
	"codegen/sieve":   "bounds=1 reduce=0",
	"binarytrees/mk":  "bounds=0 reduce=0",
	"fannkuch/mk":     "bounds=3 reduce=0",
	"fasta/mk":        "bounds=2 reduce=0",
	"fmtints/mk":      "bounds=1 reduce=1",
	"intsum/mk":       "bounds=0 reduce=0",
	"intsum/mkwrap":   "bounds=0 reduce=0",
	"jsonscan/mk":     "bounds=0 reduce=0",
	"knucleotide/mk":  "bounds=0 reduce=0",
	"mandelbrot/mk":   "bounds=0 reduce=0",
	"matmul/mk":       "bounds=2 reduce=0",
	"nbody/mk":        "bounds=0 reduce=0",
	"sieve/mk":        "bounds=0 reduce=0",
	"sieve/mkwrap":    "bounds=0 reduce=0",
	"sortints/mk":     "bounds=8 reduce=0",
	"spectralnorm/mk": "bounds=0 reduce=0",
}

// TestPV_BenchKernels versions every bench kernel's bodies (validating
// each, pvVersion) and pins how many loops each rewrite versions.
func TestPV_BenchKernels(t *testing.T) {
	srcs := pvCodegenKernels(t)
	for _, f := range pcKernels(t) {
		srcs[kernelName(f)] = readKernel(t, f)
	}
	var got []string
	for name, src := range srcs {
		bin := !strings.HasPrefix(name, "codegen/")
		l := pcLower(t, src, pcOpt{bin: bin, fold: true, version: &VersionOptions{}})
		line := fmt.Sprintf("bounds=%d reduce=%d", l.vBounds, l.vReduce)
		got = append(got, name+": "+line)
		if w := pvKernelWant[name]; w != line {
			t.Errorf("%s: got %s, want %s", name, line, w)
		}
	}
	t.Log("\n" + strings.Join(got, "\n"))
}

// genVerProg is a generated H(): int over one array: loops whose index is
// the counter, a stepped counter, or either plus or minus a base (an
// immediate, an invariant local or a product of two), with bounds that may
// run past the array or below 0, and reductions whose terms may overflow.
func genVerProg(data []byte) string {
	g := &pcGen{data: data}
	n := 1 + g.next()%10
	var sb strings.Builder
	sb.WriteString(pvV)
	fmt.Fprintf(&sb, "H(): int {\n\tvar a: [%d]int = [0; %d]\n", n, n)
	big := []string{"1", "7", "-3", "1152921504606846975", "4611686018427387904", "-4611686018427387904", "9223372036854775807", pvMin}
	for i := 0; i < n; i++ {
		v := fmt.Sprintf("%d", g.next()%2001-1000)
		if g.next()%4 == 0 {
			v = big[g.next()%len(big)]
		}
		fmt.Fprintf(&sb, "\ta[%d] = %s\n", i, v)
	}
	lo := g.next()%5 - 2
	cut := g.next()%(n+1) + 1
	fmt.Fprintf(&sb, "\ts := a[0..%d]\n", min(cut, n))
	fmt.Fprintf(&sb, "\tk := %d\n\tm := %d\n", g.next()%4-1, g.next()%3)
	sb.WriteString("\tvar r := 0\n\tvar p := " + fmt.Sprint(g.next()%3-1) + "\n")
	loops := 1 + g.next()%3
	for q := 0; q < loops; q++ {
		hi := lo + g.next()%(n+3)
		switch g.next() % 6 {
		case 0:
			fmt.Fprintf(&sb, "\tfor i in V(%d)..V(%d) {\n\t\tr = r +%% s[i]\n\t}\n", lo, hi)
		case 1:
			fmt.Fprintf(&sb, "\tfor i in V(%d)..V(%d) {\n\t\tr = r +%% s[i + k]\n\t}\n", lo, hi)
		case 2:
			fmt.Fprintf(&sb, "\tfor i in V(%d)..V(%d) {\n\t\tr = r +%% s[k * m + i] +%% s[i - m]\n\t}\n", lo, hi)
		case 3:
			fmt.Fprintf(&sb, "\tfor _ in V(%d)..V(%d) {\n\t\tr = r +%% s[p]\n\t\tp += 1\n\t}\n", lo, hi)
		case 4:
			fmt.Fprintf(&sb, "\tfor i in V(%d)..V(%d) {\n\t\tr += s[i]\n\t}\n", lo, hi)
		default:
			op := "+="
			if g.next()%2 == 0 {
				op = "-="
			}
			fmt.Fprintf(&sb, "\tvar t := %s\n\tfor i in 0..s.Len() {\n\t\tt %s s[i]\n\t}\n\tr = r +%% t\n", big[g.next()%len(big)], op)
		}
	}
	sb.WriteString("\treturn r\n}\n")
	return sb.String()
}

// pvAgrees reports a difference between eval of src with the range pass,
// its folds and the versions on, and with the range pass off, every check
// as lowered ("" when they agree or src does not build), and whether a
// loop was versioned.
func pvAgrees(t testing.TB, src string) (built bool, versioned bool, diff string) {
	off, _ := pcBuild(t, src, pcOpt{tweak: func(c *Config) { c.NoRanges = true }})
	if off == nil {
		return false, false, ""
	}
	on, _ := pcBuild(t, src, pcOpt{fold: true, version: &VersionOptions{}})
	a, b := pcRun(off), pcRun(on)
	if a != b {
		return true, true, fmt.Sprintf("off %q, on %q", a, b)
	}
	// The reduction's chunks cross the generated loops' bounds at chunk 2.
	small, _ := pcBuild(t, src, pcOpt{fold: true, version: &VersionOptions{chunk: 2}})
	if c := pcRun(small); c != a {
		return true, true, fmt.Sprintf("off %q, on with chunk 2 %q", a, c)
	}
	return true, on.vBounds+on.vReduce > 0, ""
}

// TestPC_VersionAgrees runs 300 generated programs with the versions on
// and off; they must agree, and enough of them must version a loop.
func TestPC_VersionAgrees(t *testing.T) {
	r := rand.New(rand.NewSource(767))
	built, versioned := 0, 0
	for i := 0; i < 300; i++ {
		data := make([]byte, 64)
		r.Read(data)
		src := genVerProg(data)
		ok, v, diff := pvAgrees(t, src)
		if diff != "" {
			t.Fatalf("seed %d: %s\n%s", i, diff, src)
		}
		if ok {
			built++
		}
		if v {
			versioned++
		}
	}
	t.Logf("built %d of 300, %d versioned a loop", built, versioned)
	if built < 200 || versioned < 100 {
		t.Errorf("the generator is too weak: built %d, versioned %d", built, versioned)
	}
}

// FuzzVersionAgrees is TestPC_VersionAgrees' check under the fuzzer.
func FuzzVersionAgrees(f *testing.F) {
	f.Add([]byte("seed-ver-1"))
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15})
	f.Fuzz(func(t *testing.T, data []byte) {
		if _, _, diff := pvAgrees(t, genVerProg(data)); diff != "" {
			t.Fatalf("%s\n%s", diff, genVerProg(data))
		}
	})
}

// TestPV_Off: with NoRanges, or both switches set, VersionLoops returns
// its argument.
func TestPV_Off(t *testing.T) {
	src := pvEdge[len(pvEdge)-3].src
	for _, o := range []pcOpt{
		{fold: true, version: &VersionOptions{NoBounds: true, NoReduce: true}},
		{fold: true, version: &VersionOptions{}, tweak: func(c *Config) { c.NoRanges = true }},
	} {
		l := pcLower(t, src, o)
		if l.vBounds+l.vReduce != 0 {
			t.Errorf("%+v: versioned %d and %d loops", o, l.vBounds, l.vReduce)
		}
	}
}
