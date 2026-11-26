// Why: std.simd's dispatch and error paths are part of its public contract
// and must behave as documented on every CPU and at compile time.
// What: TestSTDSIMD_Dispatch, DotLengthsPanic, IntegerLanesE0701, CompileTime.
// How: small Mk programs, run natively and through a const, checked for
// their output, panic text or diagnostic.

// Tests of Dispatch, Choice and the error paths of Dot and Sum, as the
// reference states them: Dispatch takes the first choice whose needs the
// running CPU has, else base; Dot panics with code 2's text on unequal
// lengths; Dot and Sum over integer lanes are E0701 at the call.

package std

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sdDispatchProgram = `import std.simd

One(): int {
    return 1
}

Two(): int {
    return 2
}

Three(): int {
    return 3
}

export Main(sys: &System): OutOfMemory|WriteError!void {
    var out := sys.Stdout()
    base := simd.CpuFeatures.Of("sse2")
    wide := simd.CpuFeatures.Of("avx512bw")
    cs := [simd.Choice[fn(): int](needs: wide, f: Three), simd.Choice[fn(): int](needs: base, f: Two), simd.Choice[fn(): int](needs: base, f: One)]
    first := simd.Dispatch(cs[0..], One)()
    second := simd.Dispatch(cs[1..], Three)()
    empty := simd.Dispatch(cs[0..0], One)()
    only := simd.Dispatch(cs[0..1], One)()
    has := simd.Cpu().Has("avx512bw")
    var want := 1
    var want_first := 2
    if has {
        want = 3
        want_first = 3
    }
    try Print(mut out, "{a} {b} {c} {d}\n", (a: first == want_first, b: second, c: empty, d: only == want))
}
`

func TestSTDSIMD_Dispatch(t *testing.T) {
	t.Parallel()
	for _, flags := range c2Flags {
		t.Run(c2FlagName(flags), func(t *testing.T) {
			t.Parallel()
			if got := sdRun(t, sdDispatchProgram, flags...); got != "true 2 1 true\n" {
				t.Errorf("got %q, want %q", got, "true 2 1 true\n")
			}
		})
	}
}

const sdDotPanicProgram = `import std.simd

export Main(sys: &System): OutOfMemory|WriteError!void {
    a := try sys.arena.Slice[f32](5)
    b := try sys.arena.Slice[f32](4)
    d := simd.Dot(a[..], b[..])
    var out := sys.Stdout()
    try Print(mut out, "{d}\n", (d))
}
`

func TestSTDSIMD_DotLengthsPanic(t *testing.T) {
	t.Parallel()
	r := runNative(t, buildNative(t, nil, sdDotPanicProgram), "")
	if r.code != 101 || !strings.Contains(r.stderr, "panic: bounds: simd.Dot: the slices' lengths differ") {
		t.Errorf("exit %d, stderr %q: want 101 and the bounds panic naming simd.Dot", r.code, r.stderr)
	}
}

func TestSTDSIMD_IntegerLanesE0701(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	stdDir := copyStd(t, root, nil)
	src := "import std.simd\n\nTotal(xs: []i32): i32 {\n    return simd.Sum(xs)\n}\n\nInner(xs: []u8): u8 {\n    return simd.Dot(xs, xs)\n}\n"
	if err := os.WriteFile(filepath.Join(root, "bad.mk"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	out, status := mkCmd(t, root, stdDir, "check", "bad.mk")
	if status != 1 || strings.Count(out, "E0701 bound-unmet") != 2 || !strings.Contains(out, "bad.mk:4:") || !strings.Contains(out, "bad.mk:8:") {
		t.Errorf("mk check: exit %d, want 1 with E0701 at lines 4 and 8\n%s", status, out)
	}
}

// sdConstProgram computes each routine at compile time (const) and at run
// time over the same data: the evaluator runs the 16-lane path with Cpu()
// at the baseline, the native build the dispatched one (std code that
// uses SIMD may run at compile time, with the same results). Cut counts
// ValidUtf8's and ValidWtf8's wrong answers on a scalar cut short where the
// lookup's unfinished-sequence check decides: at the end of a 64-byte block
// before an ASCII one, in a tail vector, in the overlapping last window and
// at the end of the text, and on the completed scalar.
const sdConstProgram = `import std.simd

const Text: str = "the quick brown fox jumps over the lazy dog, \u{e9}t\u{e9} \u{20ac}100 and more text past sixty-four bytes"
const Bad: [5]u8 = [0x61, 0x62, 0xE2, 0x82, 0x28]
const Xs: [20]f32 = [1.5, 2.25, -3.0, 4.0, 0.5, 6.0, 7.0, 8.0, 9.0, 10.0, 11.0, 12.0, 13.0, 14.0, 15.0, 16.0, 17.0, 18.0, 19.0, 0.125]
const Set: [5]u8 = [0x7A, 0x2C, 0xE2, 0x7E, 0x80]

const V1: bool = simd.ValidUtf8(Text.AsBytes())
const V2: bool = simd.ValidUtf8(Bad[..])
const I1: int = simd.IndexByte(Text.AsBytes(), 0x6D) else { -1 }
const I2: int = simd.IndexAny(Text.AsBytes(), Set[..]) else { -1 }
const I3: int = simd.IndexAny(Text.AsBytes(), Set[0..2]) else { -1 }
const C1: int = simd.Count(Text.AsBytes(), 0x20)
const E1: bool = simd.Equal(Text.AsBytes(), Text.AsBytes())
const P1: int = simd.Compare(Text.AsBytes()[0..70], Text.AsBytes()[1..71])
const D1: f32 = simd.Dot(Xs[..], Xs[..])
const S1: f32 = simd.Sum(Xs[..])

Cut(): int {
    scalars: [3][4]u8 = [[0xC3, 0xA9, 0, 0], [0xE2, 0x82, 0xAC, 0], [0xF0, 0x9F, 0x98, 0x80]]
    var bad := 0
    for n in [20, 33, 64, 70, 80, 128, 200, 256, 300] {
        for j in 0..3 {
            for prefix in 1..j + 2 {
                var buf: [556]u8 = [0x61; 556]
                for k in 0..prefix {
                    buf[n - prefix + k] = scalars[j][k]
                }
                for end in [n, n + 256] {
                    if simd.ValidUtf8(buf[..end]) || simd.ValidWtf8(buf[..end]) {
                        bad += 1
                    }
                }
                for k in prefix..j + 2 {
                    buf[n - prefix + k] = scalars[j][k]
                }
                s := buf[..n - prefix + j + 2]
                if !simd.ValidUtf8(s) || !simd.ValidWtf8(s) {
                    bad += 1
                }
            }
        }
    }
    return bad
}

const U1: int = Cut()

export Main(sys: &System): OutOfMemory|WriteError!void {
    var out := sys.Stdout()
    b := Text.AsBytes()
    try Print(mut out, "{a} {b} {c} {d} {e} {f} {g} {h} {i} {j} {k}\n", (a: V1, b: V2, c: I1, d: I2, e: I3, f: C1, g: E1, h: P1, i: D1, j: S1, k: U1))
    bad := Bad
    xs := Xs
    set := Set
    i1 := simd.IndexByte(b, 0x6D) else { -1 }
    i2 := simd.IndexAny(b, set[..]) else { -1 }
    i3 := simd.IndexAny(b, set[0..2]) else { -1 }
    try Print(mut out, "{a} {b} {c} {d} {e} {f} {g} {h} {i} {j} {k}\n", (a: simd.ValidUtf8(b), b: simd.ValidUtf8(bad[..]), c: i1, d: i2, e: i3, f: simd.Count(b, 0x20), g: simd.Equal(b, b), h: simd.Compare(b[0..70], b[1..71]), i: simd.Dot(xs[..], xs[..]), j: simd.Sum(xs[..]), k: Cut()))
}
`

func TestSTDSIMD_CompileTime(t *testing.T) {
	t.Parallel()
	const line = "true false 22 37 37 16 true 1 2447.5781 180.375 0\n"
	for _, flags := range c2Flags {
		t.Run(c2FlagName(flags), func(t *testing.T) {
			t.Parallel()
			if got := sdRun(t, sdConstProgram, flags...); got != line+line {
				t.Errorf("got %q, want the line %q twice", got, line)
			}
		})
	}
}
