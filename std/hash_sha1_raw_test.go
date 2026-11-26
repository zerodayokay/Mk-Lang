// Why: the SHA-1 intrinsics behind Sha1Blocks need checks in the evaluator,
// natively and in the checker, as simd_sha256_test.go gives SHA-256's.
// What: TestHash_Sha1IntrinsicsEvalNative, _Sha1EvaluatorKernel,
// _Sha1KernelCheckEveryTarget, and the round-group and CPU-feature tests.
// How: intrinsics against Go definitions from FIPS 180-4; the kernel in the
// evaluator with the portable loop broken; E0706, E0705, E1101, E1106.

package std

import (
	"fmt"
	"io/fs"
	"math/bits"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// h11S1On forces the SHA-1 kernel's CPU test true.
var h11S1On = []sdForce{{"simd/kernel/sha1.mk", `c.Has("sha")`, "true", 1}}

// h11S1Shim reaches the kernel from std.hash for s8CheckTargets.
const h11S1Shim = `import std.simd.kernel

export Simd8Sha1Blocks(h: mut [5]u32, data: []u8): bool {
    return kernel.Sha1Blocks(h, data)
}
`

// h11S1K are FIPS 180-4 section 4.2.1's constants.
var h11S1K = [4]uint32{0x5a827999, 0x6ed9eba1, 0x8f1bbcdc, 0xca62c1d6}

// h11S1Rounds is four rounds of group g, read from section 6.1.2 step 3
// with the intrinsics' lane order: s = (d, c, b, a), we = (W3, W2, W1,
// W0 + e).
func h11S1Rounds(s, we [4]uint32, g int) [4]uint32 {
	a, b, c, d, e := s[3], s[2], s[1], s[0], uint32(0)
	for i := 0; i < 4; i++ {
		var f uint32
		switch g {
		case 0:
			f = (b & c) ^ (^b & d)
		case 2:
			f = (b & c) ^ (b & d) ^ (c & d)
		default:
			f = b ^ c ^ d
		}
		t := bits.RotateLeft32(a, 5) + f + e + h11S1K[g] + we[3-i]
		e, d, c, b, a = d, c, bits.RotateLeft32(b, 30), a, t
	}
	return [4]uint32{d, c, b, a}
}

// h11S1NextE is w with ROTL30 of s's a added to lane 3.
func h11S1NextE(s, w [4]uint32) [4]uint32 {
	w[3] += bits.RotateLeft32(s[3], 30)
	return w
}

// h11S1Schedule is W[16 .. 19] from W[0 .. 15], each group in lane order.
func h11S1Schedule(w0, w1, w2, w3 [4]uint32) [4]uint32 {
	var w [20]uint32
	for i, g := range [4][4]uint32{w0, w1, w2, w3} {
		for j := 0; j < 4; j++ {
			w[4*i+j] = g[3-j]
		}
	}
	for t := 16; t < 20; t++ {
		w[t] = bits.RotateLeft32(w[t-3]^w[t-8]^w[t-14]^w[t-16], 1)
	}
	return [4]uint32{w[19], w[18], w[17], w[16]}
}

// h11S1IntrinsicProgram runs the three intrinsics, each round group, over
// n random inputs and folds the results into one hash, in the evaluator (a
// const) and natively.
const h11S1IntrinsicProgram = `import std.raw
import std.simd

R0(s: [4]u32, w: [4]u32): [4]u32 cpu "sha" {
    return raw.Sha1Rounds4(s, w, 0)
}

R1(s: [4]u32, w: [4]u32): [4]u32 cpu "sha" {
    return raw.Sha1Rounds4(s, w, 1)
}

R2(s: [4]u32, w: [4]u32): [4]u32 cpu "sha" {
    return raw.Sha1Rounds4(s, w, 2)
}

R3(s: [4]u32, w: [4]u32): [4]u32 cpu "sha" {
    return raw.Sha1Rounds4(s, w, 3)
}

E(s: [4]u32, w: [4]u32): [4]u32 cpu "sha" {
    return raw.Sha1NextE(s, w)
}

S(a: [4]u32, b: [4]u32, c: [4]u32, d: [4]u32): [4]u32 cpu "sha" {
    return raw.Sha1Schedule4(a, b, c, d)
}

// Base calls the intrinsics from a function without sha (inside unsafe),
// through the private helpers.
Base(s: [4]u32, w: [4]u32, x: [4]u32): [4]u32 {
    unsafe "called where Cpu() has sha, or by the evaluator" {
        a := raw.Sha1Rounds4(s, w, 2)
        b := raw.Sha1NextE(a, x)
        return raw.Sha1Schedule4(a, b, s, w)
    }
}

Next(x: u64): u64 {
    return x *% 6364136223846793005 +% 1442695040888963407
}

Mix(h: u64, x: u64): u64 {
    return (h ^ x) *% 0x100000001b3
}

Mix4(h: u64, r: [4]u32): u64 {
    var k := h
    for j in 0..4 {
        k = Mix(k, Cast[u64](r[j]))
    }
    return k
}

Run(n: int, z: u64): u64 {
    var h: u64 = 0xcbf29ce484222325
    var x: u64 = 11 + z
    var w: [16]u32 = [0; 16]
    for _ in 0..n {
        for j in 0..16 {
            x = Next(x)
            w[j] = Cast[u32](x >> 32)
        }
        s: [4]u32 = [w[0], w[1], w[2], w[3]]
        a: [4]u32 = [w[4], w[5], w[6], w[7]]
        b: [4]u32 = [w[8], w[9], w[10], w[11]]
        c: [4]u32 = [w[12], w[13], w[14], w[15]]
        unsafe "Run is called by the evaluator or where Cpu() has sha" {
            h = Mix4(h, R0(s, a))
            h = Mix4(h, R1(s, b))
            h = Mix4(h, R2(s, c))
            h = Mix4(h, R3(a, b))
            h = Mix4(h, E(s, a))
            h = Mix4(h, S(s, a, b, c))
        }
        h = Mix4(h, Base(s, a, b))
    }
    return h
}

const CE: u64 = Run(300, 0)

export Main(sys: &System): WriteError!void {
    z := Cast[u64]((sys.Env("MK_SIMD8_ZERO") else { "" }).Len())
    if !simd.Cpu().Has("sha") {
        try sys.Print("eval {e}\nskip\n", (e: CE))
        return
    }
    try sys.Print("eval {e}\nrun {r}\n", (e: CE, r: Run(300, z)))
}
`

// h11S1IntrinsicModel is h11S1IntrinsicProgram's Run(n, 0) over the Go
// definitions.
func h11S1IntrinsicModel(n int) uint64 {
	next := func(x uint64) uint64 { return x*6364136223846793005 + 1442695040888963407 }
	mix4 := func(h uint64, r [4]uint32) uint64 {
		for _, v := range r {
			h = (h ^ uint64(v)) * 0x100000001b3
		}
		return h
	}
	h, x := uint64(0xcbf29ce484222325), uint64(11)
	for i := 0; i < n; i++ {
		var w [16]uint32
		for j := range w {
			x = next(x)
			w[j] = uint32(x >> 32)
		}
		s, a, b, c := [4]uint32{w[0], w[1], w[2], w[3]}, [4]uint32{w[4], w[5], w[6], w[7]}, [4]uint32{w[8], w[9], w[10], w[11]}, [4]uint32{w[12], w[13], w[14], w[15]}
		h = mix4(h, h11S1Rounds(s, a, 0))
		h = mix4(h, h11S1Rounds(s, b, 1))
		h = mix4(h, h11S1Rounds(s, c, 2))
		h = mix4(h, h11S1Rounds(a, b, 3))
		h = mix4(h, h11S1NextE(s, a))
		h = mix4(h, h11S1Schedule(s, a, b, c))
		r := h11S1Rounds(s, a, 2)
		h = mix4(h, h11S1Schedule(r, h11S1NextE(r, b), s, a))
	}
	return h
}

// TestHash_Sha1IntrinsicsEvalNative: the three intrinsics, each round
// group, in the evaluator and natively (dev and release) against the Go
// definitions, 300 random inputs.
func TestHash_Sha1IntrinsicsEvalNative(t *testing.T) {
	t.Parallel()
	m := h11S1IntrinsicModel(300)
	want := fmt.Sprintf("eval %d\nrun %d\n", m, m)
	for _, flags := range c2Flags {
		t.Run(c2FlagName(flags), func(t *testing.T) {
			t.Parallel()
			got := s8Run(t, nil, h11S1IntrinsicProgram, true, flags...)
			s8ShaNeedsCPU(t, got, fmt.Sprintf("eval %d\n", m))
			diffLines(t, got, want)
		})
	}
}

// TestHash_Sha1EvaluatorKernel: with the kernel's CPU test forced true
// the evaluator runs hash.Sha1 through the intrinsics' evaluator
// semantics, and the compile-time digests equal crypto/sha1 even with the
// portable loop broken; on a CPU with sha the run-time digests too. The
// program stops after the compile-time lines on a CPU without sha (a
// forced kernel would fault there).
func TestHash_Sha1EvaluatorKernel(t *testing.T) {
	t.Parallel()
	want := h11S1Want()
	has, got := h2KernelRunProgram(t, h11S1ProgramStop(true), append(append([]sdForce{}, h11S1On...), h11S1BreakPortable...))
	if !has {
		diffLines(t, strings.Join(got, "\n"), strings.Join(want[:len(h2KernelEval)], "\n"))
		t.Skip("this CPU lacks sha: the compile-time digests are checked; the forced kernel cannot run natively")
	}
	diffLines(t, strings.Join(got, "\n"), strings.Join(want, "\n"))
}

// TestHash_Sha1KernelCheckEveryTarget: the SHA-1 kernel checks clean on
// the hosted targets, on aarch64 (checked only) and on
// x86_64-freestanding, where it reports false and holds no vector.
func TestHash_Sha1KernelCheckEveryTarget(t *testing.T) {
	t.Parallel()
	s8CheckTargets(t, h11S1Shim, `    var h: [5]u32 = [0; 5]
    var b: [64]u8 = [0; 64]
    _ = hash.Simd8Sha1Blocks(mut h, b[..])
`)
}

// TestHash_Sha1RoundGroupConst: Sha1Rounds4's round group is a constant
// from 0 to 3: 4 is E0706 at the call (its requires clause), and a
// run-time value is E0705.
func TestHash_Sha1RoundGroupConst(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, arg, want, fact string }{
		{"four", "4", "E0706", "g < 4"},
		{"runtime", "Trunc[u8](s[0] % 4)", "E0705", "not-const"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			src := `import std.raw

F(s: [4]u32, w: [4]u32): [4]u32 cpu "sha" {
    return raw.Sha1Rounds4(s, w, ` + c.arg + `)
}

export Main(sys: &System): WriteError!void {
    unsafe "test" {
        x := F([1; 4], [2; 4])
        try sys.Print("{a}\n", (a: x[0]))
    }
}
`
			_, out, status := s8Try(t, nil, src, true)
			if status == 0 || !strings.Contains(out, c.want) || !strings.Contains(out, c.fact) || strings.Contains(out, "E0000") {
				t.Fatalf("mk build: exit %d, want %s naming %q\n%s", status, c.want, c.fact, out)
			}
		})
	}
}

// TestHash_Sha1KernelLiteralGroups: std's kernel judges every
// Sha1Rounds4 clause at a concrete call: each call in
// simd/kernel/sha1.mk passes a literal round
// group, the four groups 0 to 3 each appear, and no other std file calls
// Sha1Rounds4. The kernel specializes all four round groups directly.
func TestHash_Sha1KernelLiteralGroups(t *testing.T) {
	t.Parallel()
	call := regexp.MustCompile(`raw\.Sha1Rounds4\(([^()]*)\)`)
	seen := map[string]int{}
	err := filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".mk") {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		ms := call.FindAllStringSubmatch(string(b), -1)
		// A call whose arguments hold parentheses (`Id(g)`) escapes the
		// pattern, so calls are counted apart (std.raw's
		// declaration aside).
		if n := strings.Count(string(b), "Sha1Rounds4(") - strings.Count(string(b), "export Sha1Rounds4("); n != len(ms) {
			t.Errorf("%s: %d calls of Sha1Rounds4, %d of them raw.Sha1Rounds4 with plain arguments; every call must pass a literal group", p, n, len(ms))
		}
		if len(ms) > 0 && filepath.ToSlash(p) != "simd/kernel/sha1.mk" {
			t.Errorf("%s calls raw.Sha1Rounds4 %d times; only std.simd.kernel's sha1.mk may", p, len(ms))
		}
		for _, m := range ms {
			args := strings.Split(m[1], ",")
			g := strings.TrimSpace(args[len(args)-1])
			switch g {
			case "0", "1", "2", "3":
				seen[g]++
			default:
				t.Errorf("%s: raw.Sha1Rounds4(%s): the round group %q is not a literal 0 to 3", p, m[1], g)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range []string{"0", "1", "2", "3"} {
		if seen[g] != 1 {
			t.Errorf("round group %s: %d calls in the kernel, want 1 (one loop per group)", g, seen[g])
		}
	}
}

// TestHash_Sha1RoundGroupGeneric: the round group as a generic's const
// type parameter whose constraint is forwarded to Sha1Rounds4. G[0]
// runs on a CPU with SHA and H[3] evaluates as a const. G[4] and H[9]
// fail their respective instantiations with E0706.
func TestHash_Sha1RoundGroupGeneric(t *testing.T) {
	t.Parallel()
	const fn = `[const g: u8](s: [4]u32, w: [4]u32): [4]u32
    requires g < 4
{
    unsafe "test: the evaluator, or a CPU with sha" {
        return raw.Sha1Rounds4(s, w, g)
    }
}
`
	prog := func(run, cnst string) string {
		return "import std.raw\n\nG" + fn + "\nH" + fn + `
const C: [4]u32 = ` + cnst + `([1; 4], [2; 4])

export Main(sys: &System): WriteError!void {
    x := ` + run + `([1; 4], [2; 4])
    try sys.Print("{a} {b}\n", (a: x[0], b: C[0]))
}
`
	}
	for _, c := range []struct{ name, run, cnst string }{
		{"run time 4", "G[4]", "H[1]"},
		{"const 9", "G[1]", "H[9]"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			src := prog(c.run, c.cnst)
			bad := c.run
			if c.name == "const 9" {
				bad = c.cnst
			}
			if _, out, status := s8Try(t, nil, src, true); status == 0 || !strings.Contains(out, "E0706") ||
				!strings.Contains(out, bad) || strings.Contains(out, "E0000") {
				t.Fatalf("mk build: exit %d, want E0706 at %s\n%s", status, bad, out)
			}
		})
	}
	t.Run("valid", func(t *testing.T) {
		t.Parallel()
		src := "import std.simd\n" + strings.Replace(prog("G[0]", "H[3]"), "    x := G[0]", `    if !simd.Cpu().Has("sha") {
        try sys.Print("const {b}\nskip\n", (b: C[0]))
        return
    }
    x := G[0]`, 1)
		one, two := [4]uint32{1, 1, 1, 1}, [4]uint32{2, 2, 2, 2}
		cnst := fmt.Sprintf("const %d\n", h11S1Rounds(one, two, 3)[0])
		got := s8Run(t, nil, src, true)
		if got == cnst+"skip\n" {
			t.Skip("this CPU lacks sha: the const H[3] matched section 6.1.2; the run-time G[0] was not run")
		}
		if want := fmt.Sprintf("%d %d\n", h11S1Rounds(one, two, 0)[0], h11S1Rounds(one, two, 3)[0]); got != want {
			t.Fatalf("got %q, want %q (section 6.1.2's groups 0 and 3)\n%s", got, want, src)
		}
	})
}

// TestHash_Sha1IntrinsicsNeedTheirFeature: a call outside unsafe from a
// function without sha is E1101 cpu-call.
func TestHash_Sha1IntrinsicsNeedTheirFeature(t *testing.T) {
	t.Parallel()
	s8WantCpuCall(t, `import std.raw

F(w: [4]u32): [4]u32 cpu "avx2" {
    return raw.Sha1Schedule4(w, w, w, w)
}

export Main(sys: &System): WriteError!void {
    unsafe "test" {
        x := F([1; 4])
        try sys.Print("{a}\n", (a: x[0]))
    }
}
`, "sha")
}

// TestHash_Sha1GeneralRegsOnlyNeedsSse2: on x86_64-freestanding, whose
// bins are general-regs-only, a call of a SHA-1 intrinsic from a context
// without sse2 is E1106 needs sse2: from a `cpu "sha"` function and
// inside unsafe from MkEntry with no clause, also when named as a fn
// value there.
func TestHash_Sha1GeneralRegsOnlyNeedsSse2(t *testing.T) {
	t.Parallel()
	s8GroWantNeeds(t, `import std.raw

F(s: [4]u32, w: [4]u32): [4]u32 cpu "sha" {
    return raw.Sha1Rounds4(s, w, 1)
}

export extern MkEntry(): u64 {
    var s: [4]u32 = [1; 4]
    var w: [4]u32 = [2; 4]
    unsafe "probe" {
        s = F(s, w)
        s = raw.Sha1Rounds4(s, w, 0)
        w = raw.Sha1Schedule4(w, w, w, w)
        g := raw.Sha1NextE
        w = g(s, w)
    }
    return Cast[u64](s[0] ^ w[0])
}
`, 4)
}
