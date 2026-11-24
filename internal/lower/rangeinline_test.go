// Why: the small-leaf inliner (rangeinline.go) runs before a second range
// pass so facts reach through small calls; it must never change a result.
// What: TestPI_Agrees, TestPI_Proves, TestPI_MutantsCaught,
// TestPI_InlineAgrees and FuzzInlineAgrees, TestPI_BenchKernels.
// How: each inlined body validates and leaves its input as it was; eval,
// the reference semantics, gives every program the same value or the same
// panic at the same site with the inliner on, off, and with the range pass
// off; the inliner's two faults are caught by the edge programs; a
// generator of leaves called in loops agrees on and off; and the bench
// kernels' inlined calls and checks are pinned (spectralnorm's A).

package lower

import (
	"fmt"
	"strings"
	"testing"

	"mk/internal/mir"
	"mk/internal/types"
)

// piInline runs the inliner on every body of l as the driver does on the
// instance bodies it hands codegen, the leaves being the bodies as lowered
// (non-generic ones only: the driver offers a non-generic callee), then the
// versions when version is set; it checks that every input is unchanged
// and every output validates.
func piInline(t testing.TB, l *lowered, cfg *Config, version *VersionOptions) {
	t.Helper()
	byItem := map[types.ItemID]*mir.Body{}
	for _, k := range l.keys {
		if b := l.bodies[k]; b.GenericCount == 0 {
			byItem[b.Item] = b
		}
	}
	leaf := func(id types.ItemID) *mir.Body { return byItem[id] }
	p := &mir.Printer{Types: l.w.tab, Items: l.w.items}
	env := &mir.Env{Types: cfg.Types, Callee: cfg.callee, Known: cfg.Known}
	out := map[string]*mir.Body{}
	for _, k := range l.keys {
		mb := l.bodies[k]
		before := p.Print(mb)
		nb, n := inlineLeaves(cfg, mb, leaf)
		if after := p.Print(mb); after != before {
			t.Fatalf("InlineLeaves changed its input:\n%s\nwas\n%s", after, before)
		}
		if nb != mb {
			if err := nb.Validate(env); err != nil {
				t.Fatalf("inlined body does not validate: %v\n%s", err, p.Print(nb))
			}
		}
		l.nInline += n
		if version != nil {
			nb = pvVersion(t, l, cfg, nb, *version)
		}
		out[k] = nb
	}
	for k, b := range out {
		l.bodies[k] = b
	}
}

// piEval lowers src with the inliner on (and tweak) and evaluates its H().
func piEval(t *testing.T, src string, tweak func(*Config)) (string, *lowered) {
	t.Helper()
	l := pcLower(t, src, pcOpt{fold: true, inline: true, tweak: tweak})
	return pcRun(l), l
}

// piEdge are the inliner's edge programs (H(): int): each gives the same
// outcome with the inliner on as off; inlined is how many calls it must
// inline.
var piEdge = []struct {
	name, src string
	inlined   int
}{
	// spectralnorm's A as an int, in two loops over a hidden length: the sums
	// are proved only inlined (widening only on a retreating edge keeps i's
	// bound in the j loop), and A is not symmetric, so swapped arguments
	// change the value.
	{"inl-sum", pvV + "A(i: int, j: int): int {\n\treturn (i + j) * (i + j + 1) / 2 + i + 1\n}\nH(): int {\n\tvar a: [6]int = [1; 6]\n\ts := a[0..V(6)]\n\tn := s.Len()\n\tvar r := 0\n\tfor i in 0..n {\n\t\tfor j in 0..n {\n\t\t\tr = r +% A(i, j) *% s[j] *% (j + 7)\n\t\t}\n\t}\n\treturn r\n}\n", 1},
	// The leaf's add is proved for i < 250 and traps at i = 250, where the
	// sum is 256: the trap stays at the leaf's add.
	{"inl-edge", "B(x: int, k: int): u8 {\n\treturn Cast[u8](x) + Cast[u8](k)\n}\nH(): int {\n\tvar r := 0\n\tfor i in 0..251 {\n\t\tr = int(B(i, 6))\n\t}\n\treturn r\n}\n", 1},
	{"safe-edge", "B(x: int, k: int): u8 {\n\treturn Cast[u8](x) + Cast[u8](k)\n}\nH(): int {\n\tvar r := 0\n\tfor i in 0..250 {\n\t\tr = r + int(B(i, 6))\n\t}\n\treturn r\n}\n", 1},
	// Rule 4 after the splice: M's product did not trap and both factors
	// are at least 0, so p / 2 + 2^62 is proved (MAX), and + 2^62 + 1
	// traps.
	{"inl-mul-ok", pvV + "M(x: int, y: int): int {\n\treturn x * y / 2 + 4611686018427387904\n}\nH(): int {\n\tx := V(2) & 3\n\ty := V(4611686018427387903) & 4611686018427387903\n\treturn M(x, y)\n}\n", 1},
	{"inl-mul", pvV + "M(x: int, y: int): int {\n\treturn x * y / 2 + 4611686018427387905\n}\nH(): int {\n\tx := V(2) & 3\n\ty := V(4611686018427387903) & 4611686018427387903\n\treturn M(x, y)\n}\n", 1},
	// The result goes to a destination read after the call, and the leaf's
	// first parameter is not its result.
	{"inl-ret", "D(x: i32, y: i32): i32 {\n\treturn x * 3 - y\n}\nH(): int {\n\tvar r := 0\n\tfor i in 0..40 {\n\t\tv := D(Cast[i32](i), 5)\n\t\tr = r + int(v)\n\t}\n\treturn r\n}\n", 1},
	// Not inlined: a leaf with a branch (two blocks), an identity (no check
	// to prove), a leaf that fails, and a recursive call.
	{"no-branch", "B(x: int): int {\n\tif x > 3 {\n\t\treturn x + 1\n\t}\n\treturn x\n}\nH(): int {\n\tvar r := 0\n\tfor i in 0..9 {\n\t\tr = r + B(i)\n\t}\n\treturn r\n}\n", 0},
	{"no-identity", pvV + "H(): int {\n\tvar r := 0\n\tfor i in 0..9 {\n\t\tr = r + V(i)\n\t}\n\treturn r\n}\n", 0},
	{"no-slice", "S(s: []int, i: int): int {\n\treturn s.Len() + i\n}\nH(): int {\n\tvar a: [4]int = [2; 4]\n\treturn S(a[0..4], 7)\n}\n", 0},
}

// TestPI_Agrees: every edge program gives the same outcome with the
// inliner on as off and as with the range pass off, inlines as many calls
// as it says, and every pcEdge and pvEdge program agrees with it on.
func TestPI_Agrees(t *testing.T) {
	for _, e := range piEdge {
		off := pcEval(t, e.src, nil)
		none := pcEval(t, e.src, func(c *Config) { c.NoRanges = true })
		on, l := piEval(t, e.src, nil)
		if on != off || on != none {
			t.Errorf("%s: inliner on gives %q, off %q, range pass off %q", e.name, on, off, none)
		}
		if l.nInline != e.inlined {
			t.Errorf("%s: inlined %d calls, want %d", e.name, l.nInline, e.inlined)
		}
		t.Logf("%s: %s, %d inlined", e.name, on, l.nInline)
	}
	for _, e := range pcEdge {
		if on, _ := piEval(t, e.src, nil); on != pcEval(t, e.src, nil) {
			t.Errorf("%s: inliner on gives %q", e.name, on)
		}
	}
	for _, e := range pvEdge {
		l := pcLower(t, e.src, pcOpt{fold: true, inline: true, version: &VersionOptions{}})
		if on := pcRun(l); on != pcEval(t, e.src, nil) {
			t.Errorf("%s: inliner and versions on give %q", e.name, on)
		}
	}
}

// TestPI_Proves: inlined, the leaf's checks see the arguments' ranges.
func TestPI_Proves(t *testing.T) {
	for _, c := range []struct{ name, key, want string }{
		// A's four adds are proved in H, its * stays checked.
		{"inl-sum", "H", "mul=1"},
		{"inl-mul-ok", "H", "mul=1"},
		{"safe-edge", "H", "casts=0"},
	} {
		var src string
		for _, e := range piEdge {
			if e.name == c.name {
				src = e.src
			}
		}
		off := pcLower(t, src, pcOpt{fold: true}).counts(c.key)
		_, l := piEval(t, src, nil)
		on := l.counts(c.key)
		if !strings.Contains(on.String(), c.want) || on.nowrap <= off.nowrap {
			t.Errorf("%s: on %s, off %s: want %s and more proved", c.name, on, off, c.want)
		}
		t.Logf("%s: on %s | off %s", c.name, on, off)
	}
}

// TestPI_MutantsCaught: each inliner fault changes some edge program's
// outcome, and the range mutant of rule 4's product (mutMulTrapHalf) is
// caught through a splice too (inl-mul).
func TestPI_MutantsCaught(t *testing.T) {
	right := map[string]string{}
	for _, e := range piEdge {
		right[e.name] = pcEval(t, e.src, nil)
	}
	for _, m := range []struct {
		name string
		m    inlineMut
	}{{"arg-swap", imutArgSwap}, {"ret-param", imutRetParam}} {
		caught := ""
		for _, e := range piEdge {
			mm := m.m
			if got, _ := piEval(t, e.src, func(c *Config) { c.inlineMut = mm }); got != right[e.name] {
				caught = e.name
				break
			}
		}
		if caught == "" {
			t.Errorf("inliner mutant %s is caught by no edge program", m.name)
		} else {
			t.Logf("inliner mutant %s caught by %s", m.name, caught)
		}
	}
	for _, e := range piEdge {
		if e.name != "inl-mul" {
			continue
		}
		if got, _ := piEval(t, e.src, func(c *Config) { c.rangeMut = mutMulTrapHalf }); got == right[e.name] {
			t.Errorf("inl-mul does not catch mutMulTrapHalf: both give %q", got)
		}
	}
}

// genInlineProg is a leaf L of int parameters x and y with checked
// arithmetic of every integer type, and H(): int calling it in one or two
// counted loops with counters, immediates and hidden values as arguments.
func genInlineProg(data []byte) string {
	g := &pcGen{data: data}
	rt := g.ty()
	g.vars = []pcVar{{"x", pcTys[8]}, {"y", pcTys[8]}}
	expr := func(t pcTy) string {
		for {
			e := g.expr(t)
			if !strings.Contains(e, "a[") && !strings.Contains(e, "a.Len") {
				return e
			}
		}
	}
	var sb strings.Builder
	sb.WriteString(pvV)
	fmt.Fprintf(&sb, "L(x: int, y: int): %s {\n", rt.name)
	for q, k := 0, g.next()%3; q < k; q++ {
		t := g.ty()
		v := pcVar{fmt.Sprintf("v%d", q), t}
		fmt.Fprintf(&sb, "\t%s: %s = %s\n", v.name, t.name, expr(t))
		g.vars = append(g.vars, v)
	}
	fmt.Fprintf(&sb, "\treturn %s\n}\n", expr(rt))
	bound := func(span int) string {
		v := g.next()%span - 2
		if g.next()%4 == 0 {
			return fmt.Sprintf("V(%d)", v)
		}
		return fmt.Sprint(v)
	}
	args := []string{"i", "j", "V(i)", fmt.Sprint(g.next() % 300), "i + j", "i * 2"}
	arg := func() string { return args[g.next()%len(args)] }
	sb.WriteString("H(): int {\n\tvar r := 0\n")
	fmt.Fprintf(&sb, "\tfor i in %s..%s {\n", bound(6), bound(300))
	if g.next()%2 == 0 {
		fmt.Fprintf(&sb, "\t\tfor j in %s..%s {\n\t\t\tr = r ^ %s\n\t\t}\n", bound(4), bound(12), asInt(pcVar{"L(" + arg() + ", " + arg() + ")", rt}))
	} else {
		fmt.Fprintf(&sb, "\t\tj := i + 1\n\t\tr = r ^ %s\n", asInt(pcVar{"L(" + arg() + ", " + arg() + ")", rt}))
	}
	sb.WriteString("\t}\n\treturn r\n}\n")
	return sb.String()
}

// piAgrees evaluates src with the inliner on, off and with the range pass
// off; built false when it does not build, and the inlined call count.
func piAgrees(t testing.TB, src string) (built bool, inlined int, diff string) {
	none, _ := pcBuild(t, src, pcOpt{tweak: func(c *Config) { c.NoRanges = true }})
	if none == nil {
		return false, 0, ""
	}
	off, _ := pcBuild(t, src, pcOpt{fold: true})
	on, _ := pcBuild(t, src, pcOpt{fold: true, inline: true, version: &VersionOptions{}})
	a, b, c := pcRun(none), pcRun(off), pcRun(on)
	if a != b || a != c {
		return true, on.nInline, fmt.Sprintf("range pass off %q, inliner off %q, on %q", a, b, c)
	}
	return true, on.nInline, ""
}

// TestPI_InlineAgrees runs 300 generated programs with the inliner on and
// off: they must agree, and most must inline their call.
func TestPI_InlineAgrees(t *testing.T) {
	built, inlined, panics := 0, 0, 0
	for i, data := range pcNestSeeds(300) {
		src := genInlineProg(append(data, byte(i)))
		ok, n, diff := piAgrees(t, src)
		if diff != "" {
			t.Errorf("program %d: %s\n%s", i, diff, src)
			continue
		}
		if !ok {
			continue
		}
		built++
		if n > 0 {
			inlined++
		}
		if strings.Contains(pcEval(t, src, nil), " panic ") {
			panics++
		}
	}
	t.Logf("built %d of 300, %d inlined a call, %d panic", built, inlined, panics)
	if built < 200 || inlined < 150 || panics < 30 {
		t.Errorf("the generator is too weak: built %d, inlined %d, panics %d", built, inlined, panics)
	}
}

// FuzzInlineAgrees is TestPI_InlineAgrees' check under the fuzzer.
func FuzzInlineAgrees(f *testing.F) {
	f.Add([]byte("seed-inline-1"))
	f.Add([]byte{3, 1, 4, 1, 5, 9, 2, 6, 5, 3, 5, 8, 9, 7, 9, 3})
	f.Fuzz(func(t *testing.T, data []byte) {
		if _, _, diff := piAgrees(t, genInlineProg(data)); diff != "" {
			t.Fatalf("%s\n%s", diff, genInlineProg(data))
		}
	})
}

// piKernelWant is, per bench kernel, the calls the inliner inlines and the
// checks left with it: the record of where the inliner applies. A change
// is read, never updated blindly.
var piKernelWant = map[string]string{
	"binarytrees/mk":  "inlined 0 | off add=9 sub=0 mul=0 nowrap=6 casts=0 bounds=4/4 | on add=9 sub=0 mul=0 nowrap=6 casts=0 bounds=4/4",
	"fannkuch/mk":     "inlined 0 | off add=4 sub=3 mul=1 nowrap=5 casts=0 bounds=5/17 | on add=4 sub=3 mul=1 nowrap=5 casts=0 bounds=5/17",
	"fasta/mk":        "inlined 0 | off add=8 sub=0 mul=5 nowrap=10 casts=0 bounds=9/12 | on add=8 sub=0 mul=5 nowrap=10 casts=0 bounds=9/12",
	"fmtints/mk":      "inlined 0 | off add=2 sub=0 mul=1 nowrap=6 casts=1 bounds=7/11 | on add=2 sub=0 mul=1 nowrap=6 casts=1 bounds=7/11",
	"intsum/mk":       "inlined 0 | off add=1 sub=0 mul=0 nowrap=1 casts=1 bounds=4/6 | on add=1 sub=0 mul=0 nowrap=1 casts=1 bounds=4/6",
	"intsum/mkwrap":   "inlined 0 | off add=0 sub=0 mul=0 nowrap=0 casts=1 bounds=4/6 | on add=0 sub=0 mul=0 nowrap=0 casts=1 bounds=4/6",
	"jsonscan/mk":     "inlined 0 | off add=1 sub=1 mul=0 nowrap=11 casts=0 bounds=2/10 | on add=1 sub=1 mul=0 nowrap=11 casts=0 bounds=2/10",
	"knucleotide/mk":  "inlined 0 | off add=9 sub=8 mul=5 nowrap=0 casts=5 bounds=12/19 | on add=9 sub=8 mul=5 nowrap=0 casts=5 bounds=12/19",
	"mandelbrot/mk":   "inlined 0 | off add=4 sub=3 mul=8 nowrap=1 casts=0 bounds=1/1 | on add=4 sub=3 mul=8 nowrap=1 casts=0 bounds=1/1",
	"matmul/mk":       "inlined 0 | off add=6 sub=0 mul=11 nowrap=0 casts=2 bounds=10/10 | on add=6 sub=0 mul=11 nowrap=0 casts=2 bounds=10/10",
	"nbody/mk":        "inlined 0 | off add=13 sub=10 mul=29 nowrap=2 casts=1 bounds=6/49 | on add=13 sub=10 mul=29 nowrap=2 casts=1 bounds=6/49",
	"sieve/mk":        "inlined 0 | off add=4 sub=0 mul=1 nowrap=2 casts=0 bounds=2/5 | on add=4 sub=0 mul=1 nowrap=2 casts=0 bounds=2/5",
	"sieve/mkwrap":    "inlined 0 | off add=3 sub=0 mul=1 nowrap=0 casts=0 bounds=2/5 | on add=3 sub=0 mul=1 nowrap=0 casts=0 bounds=2/5",
	"sortints/mk":     "inlined 0 | off add=31 sub=22 mul=2 nowrap=49 casts=2 bounds=42/102 | on add=31 sub=22 mul=2 nowrap=49 casts=2 bounds=42/102",
	"spectralnorm/mk": "inlined 2 | off add=9 sub=0 mul=6 nowrap=0 casts=1 bounds=3/10 | on add=9 sub=0 mul=8 nowrap=10 casts=1 bounds=3/10",
}

// TestPI_BenchKernels inlines every bench kernel's bodies (validating each,
// piInline) and pins the inlined calls and the checks left.
func TestPI_BenchKernels(t *testing.T) {
	var got []string
	for _, f := range pcKernels(t) {
		src, name := readKernel(t, f), kernelName(f)
		off := pcLower(t, src, pcOpt{bin: true, fold: true})
		on := pcLower(t, src, pcOpt{bin: true, fold: true, inline: true})
		var a, b pcCounts
		for _, k := range on.keys {
			a.plus(off.counts(k))
			b.plus(on.counts(k))
		}
		line := fmt.Sprintf("inlined %d | off %s | on %s", on.nInline, a, b)
		got = append(got, name+": "+line)
		if w, ok := piKernelWant[name]; !ok || w != line {
			t.Errorf("%s: got  %s\nwant %s", name, line, w)
		}
	}
	t.Log("\n" + strings.Join(got, "\n"))
}
