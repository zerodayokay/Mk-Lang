// Why: a default run takes one dispatch path per CPU; the word forms, the
// narrower vector forms and Dot and Sum's edge values need runs of their own.
// What: TestSTDSIMD_WordForms, SIMD1GeneralRegsOnly, SIMD2BaselineWidth,
// SIMD5Avx2Width, SIMD8EdgeValues and SIMD8ModelDetectsFusion.
// How: copies of std with chosen CPU and target tests forced (sdForce),
// judged by the Go models of the differential tests.

// The width and edge-value tests of std.simd:
//
//   - On x86_64-freestanding the byte routines take their word
//     forms (std/simd/words.mk), so a general-regs-only bin that calls them
//     checks clean; the word forms run natively here, through a copy of std
//     whose freestanding test is forced true, against the same Go models.
//   - The 16-lane instances (every CPU without avx2, and the
//     paths a dispatched run never takes on this machine) run natively at
//     every length, through a copy of std whose avx2 tests are forced
//     false.
//   - Dot and Sum over zeros of both signs, subnormals, infinities,
//     NaN, overflow, cancellation and an FMA detector, bit for bit against
//     the Go model of the stated order, natively (dispatched and baseline)
//     and in the evaluator through a const.
//
// Each forcing names the exact text it replaces and fails when the text is
// gone, so a renamed test cannot make a variant silently equal the default.

package std

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sdForce is one replacement in one std/simd file and the count of times
// the text must occur there.
type sdForce struct {
	file, old, new string
	count          int
}

// sdBaseline turns every avx512bw and avx2 dispatch off: the public
// routines run their 16-lane instances at every length.
var sdBaseline = []sdForce{
	{"simd/bytes.mk", `Cpu().Has("avx512bw")`, "false", 4},
	{"simd/utf8.mk", `Cpu().Has("avx512bw")`, "false", 1},
	{"simd/bytes.mk", `Cpu().Has("avx2")`, "false", 6},
	{"simd/utf8.mk", `Cpu().Has("avx2")`, "false", 1},
	{"simd/float.mk", `Cpu().Has("avx2")`, "false", 4},
}

// sdAvx2 turns only the avx512bw dispatch off: on a CPU
// with AVX-512 the default run takes the 64-lane
// forms from WideMin up, so this variant keeps the 32-lane ones run.
var sdAvx2 = []sdForce{
	{"simd/bytes.mk", `Cpu().Has("avx512bw")`, "false", 4},
	{"simd/utf8.mk", `Cpu().Has("avx512bw")`, "false", 1},
}

// sdCoreFree forces std.core's freestanding forks to their freestanding
// branch on any target: ValidUtf8,
// IndexByteIn and BytesEqual run std.core's own SWAR forms, FindFrom runs
// Two-Way alone, and none calls
// std.simd, as on x86_64-freestanding. (The fuel guards whose hosted-only
// forms it also skipped left std.core for std/core_guards_test.go's
// harness program, batch 4's add-on review R4B-1, so it forces no test
// file.) sdCoreHosted forces the hosted branch, which calls std.simd.
var (
	sdCoreFree = []sdForce{
		{"core/core_utf8.mk", "Target.os != Os.Freestanding", "false", 1},
		{"core/core_iter.mk", "Target.os != Os.Freestanding", "false", 1},
		{"core/core_iter.mk", "Target.os == Os.Freestanding", "true", 1},
		{"core/core_str.mk", "Target.os != Os.Freestanding", "false", 1},
		{"core/core_str.mk", "Target.os == Os.Freestanding", "true", 1},
		{"core/core_find.mk", "Target.os != Os.Freestanding", "false", 1},
	}
	sdCoreHosted = []sdForce{
		{"core/core_utf8.mk", "Target.os != Os.Freestanding", "true", 1},
		{"core/core_iter.mk", "Target.os != Os.Freestanding", "true", 1},
		{"core/core_iter.mk", "Target.os == Os.Freestanding", "false", 1},
		{"core/core_str.mk", "Target.os != Os.Freestanding", "true", 1},
		{"core/core_str.mk", "Target.os == Os.Freestanding", "false", 1},
		{"core/core_find.mk", "Target.os != Os.Freestanding", "true", 1},
	}
)

// sdWords takes the freestanding branch on this target: the byte routines
// run their word forms, Dot and Sum their 16-lane instances, and std.core
// its own forms (sdCoreFree), as on x86_64-freestanding.
var sdWords = append([]sdForce{
	{"simd/bytes.mk", "Target.os == Os.Freestanding", "true", 5},
	{"simd/utf8.mk", "Target.os == Os.Freestanding", "true", 1},
	{"simd/float.mk", "Target.os != Os.Freestanding", "false", 4},
}, sdCoreFree...)

// sdFixtures reads today's std/simd files, applies forces and returns them
// as buildNative fixtures (paths relative to std/).
func sdFixtures(t *testing.T, forces []sdForce) map[string]string {
	t.Helper()
	fx := map[string]string{}
	for _, f := range forces {
		text, ok := fx[f.file]
		if !ok {
			b, err := os.ReadFile(filepath.FromSlash(f.file))
			if err != nil {
				t.Fatal(err)
			}
			text = string(b)
		}
		if n := strings.Count(text, f.old); n != f.count {
			t.Fatalf("%s holds %q %d times, want %d: update the forcing with the dispatch", f.file, f.old, n, f.count)
		}
		fx[f.file] = strings.ReplaceAll(text, f.old, f.new)
	}
	return fx
}

// sdRunWith is sdRun over a copy of std with fixtures.
func sdRunWith(t *testing.T, fx map[string]string, src string, flags ...string) string {
	t.Helper()
	r := runNative(t, buildNative(t, fx, src, flags...), "")
	if r.code != 0 {
		t.Fatalf("exit %d\nstdout:\n%.4000s\nstderr:\n%s", r.code, r.stdout, r.stderr)
	}
	return strings.ReplaceAll(r.stdout, "\r\n", "\n")
}

// sdProgram is one differential program and its model's output.
type sdProgram struct {
	name, src string
	want      func() string
}

func sdByteProgs() []sdProgram {
	return []sdProgram{
		{"bytes", sdBytesProgram, sdBytesModel},
		{"utf8", sdUtf8Program, sdUtf8Model},
	}
}

func sdFloatProgs() []sdProgram {
	return []sdProgram{
		{"float", sdFloatProgram, sdFloatModel},
		{"edge", sdEdgeProgram, sdEdgeModel},
	}
}

// sdMatrix runs each program at dev and release over std with forces.
func sdMatrix(t *testing.T, forces []sdForce, progs []sdProgram) {
	fx := sdFixtures(t, forces)
	for _, p := range progs {
		want := p.want()
		for _, flags := range c2Flags {
			t.Run(p.name+"/"+c2FlagName(flags), func(t *testing.T) {
				t.Parallel()
				diffLines(t, sdRunWith(t, fx, p.src, flags...), want)
			})
		}
	}
}

// TestSTDSIMD_BaselineWidth runs the 16-lane instances natively over
// every length of the differential programs: their four-vector
// blocks, overlapping tails and Count's 255-step fold at 16,320 bytes.
func TestSTDSIMD_BaselineWidth(t *testing.T) {
	t.Parallel()
	sdMatrix(t, sdBaseline, append(sdByteProgs(), sdFloatProgs()...))
}

// TestSTDSIMD_Avx2Width runs the 32-lane forms natively over the
// byte programs with the avx512bw dispatch forced off; the default
// differential tests run the 64-lane ones on an AVX-512 machine. On a CPU
// without AVX-512 it repeats the default run.
func TestSTDSIMD_Avx2Width(t *testing.T) {
	t.Parallel()
	sdMatrix(t, sdAvx2, sdByteProgs())
}

// TestSTDSIMD_WordForms runs the freestanding word forms natively
// against the same models, and at compile time (the evaluator must run
// them too: a freestanding const may call them).
func TestSTDSIMD_WordForms(t *testing.T) {
	t.Parallel()
	sdMatrix(t, sdWords, sdByteProgs())
	fx := sdFixtures(t, sdWords)
	const line = "true false 22 37 37 16 true 1 2447.5781 180.375 0\n"
	t.Run("const", func(t *testing.T) {
		t.Parallel()
		if got := sdRunWith(t, fx, sdConstProgram); got != line+line {
			t.Errorf("got %q, want the line %q twice", got, line)
		}
	})
}

// sdFreeProgram is the finding's program: a freestanding bin, so
// general-regs-only by default, calls every byte routine.
const sdFreeProgram = `import std.simd

var Buf: [128]u8 = [0; 128]

export extern MkEntry() {
    _ = simd.IndexByte(Buf[0..], 7)
    _ = simd.IndexAny(Buf[0..], Buf[0..2])
    _ = simd.IndexAny(Buf[0..], Buf[0..5])
    _ = simd.Count(Buf[0..], 7)
    _ = simd.Equal(Buf[0..], Buf[0..])
    _ = simd.Compare(Buf[0..64], Buf[1..65])
    _ = simd.ValidUtf8(Buf[0..])
}
`

// sdFreeCoreProgram calls std.core's byte paths that run std.simd
// and never std.simd itself: str.From, one- and two-byte Find, StartsWith,
// EndsWith, Split, Lines, Slice[u8].Index and CopyFrom of u8 and u64, over
// 128 bytes.
const sdFreeCoreProgram = `var Buf: [128]u8 = [0; 128]

export extern MkEntry() {
    match str.From(Buf[0..]) {
        ok(s) {
            _ = s.Find("a")
            _ = s.Find("ab")
            _ = s.StartsWith("abcdefghijklmnopqrstuvwxyz")
            _ = s.EndsWith("xyz")
            _ = s.Split(",").Next()
            _ = s.Lines().Next()
        }
        err(e) {
            _ = e
        }
    }
    b: []u8 = Buf[0..]
    _ = b.Index(&Buf[3])
    var d: [128]u8 = [0; 128]
    d.CopyFrom(Buf[0..])
    var w: [16]u64 = [0; 16]
    v: [16]u64 = [1; 16]
    w.CopyFrom(v[0..])
    _ = d[1] + Trunc[u8](w[2])
}
`

// TestSTDSIMD_GeneralRegsOnly: mk check --target x86_64-freestanding
// of sdFreeProgram, and of sdFreeCoreProgram (std.core's byte paths, which
// on x86_64-freestanding run std.core's own forms), is clean. The
// same check over a std whose freestanding tests are forced false, and,
// for sdFreeCoreProgram, std.core's forks forced to the hosted branch that
// calls std.simd (sdCoreHosted), reports E1108 in std.simd for each, so
// the check does judge these instances, also when only std.core calls
// them (the finding's failure, kept as the control).
func TestSTDSIMD_GeneralRegsOnly(t *testing.T) {
	t.Parallel()
	check := func(t *testing.T, fx map[string]string, src string) (string, int) {
		root := t.TempDir()
		stdDir := copyStd(t, root, fx)
		if err := os.WriteFile(filepath.Join(root, "main.mk"), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		return mkCmd(t, root, stdDir, "check", "--target", "x86_64-freestanding", "main.mk")
	}
	for _, p := range []struct{ name, src string }{{"simd", sdFreeProgram}, {"core", sdFreeCoreProgram}} {
		t.Run(p.name+"/words", func(t *testing.T) {
			t.Parallel()
			if out, status := check(t, nil, p.src); status != 0 || strings.Contains(out, "E1108") {
				t.Errorf("mk check --target x86_64-freestanding: exit %d, want a clean check\n%s", status, out)
			}
		})
		t.Run(p.name+"/control", func(t *testing.T) {
			t.Parallel()
			fx := sdFixtures(t, append([]sdForce{
				{"simd/bytes.mk", "Target.os == Os.Freestanding", "false", 5},
				{"simd/utf8.mk", "Target.os == Os.Freestanding", "false", 1},
			}, sdCoreHosted...))
			out, status := check(t, fx, p.src)
			if status == 0 || !strings.Contains(out, "E1108") || !strings.Contains(out, "std/simd/") {
				t.Errorf("control: exit %d, want E1108 in std/simd (the vector forms in a general-regs-only bin)\n%s", status, out)
			}
		})
	}
}

// ---- Dot and Sum over edge values ----

// sdP32 and sdP64 are the edge values by bit pattern: +0, -0, the least
// subnormal of both signs, the greatest subnormal of both signs, the least
// normal, 1, -1, 1e30 (1e300) of both signs, the greatest finite of both
// signs, +Inf, -Inf and a quiet NaN.
var sdP32 = []uint32{0x00000000, 0x80000000, 0x00000001, 0x80000001, 0x007FFFFF, 0x807FFFFF, 0x00800000,
	0x3F800000, 0xBF800000, 0x7149F2CA, 0xF149F2CA, 0x7F7FFFFF, 0xFF7FFFFF, 0x7F800000, 0xFF800000, 0x7FC00000}

var sdP64 = []uint64{0, 0x8000000000000000, 1, 0x8000000000000001, 0x000FFFFFFFFFFFFF, 0x800FFFFFFFFFFFFF,
	0x0010000000000000, 0x3FF0000000000000, 0xBFF0000000000000, 0x7E37E43C8800759C, 0xFE37E43C8800759C,
	0x7FEFFFFFFFFFFFFF, 0xFFEFFFFFFFFFFFFF, 0x7FF0000000000000, 0xFFF0000000000000, 0x7FF8000000000000}

var sdEdgeLens = []int{0, 1, 2, 7, 15, 16, 17, 31, 32, 33, 48, 63, 64, 65, 66, 79, 80, 95, 96, 100, 127, 128, 129, 200, 255, 256}

// sdEdgeProgram's Edge hashes Dot and Sum of f32 and f64 over five inputs,
// every length of EdgeLens at offsets 0 to 15: mode 0 draws from all 16
// values, mode 1 from the 13 finite ones (overflow to infinity included),
// mode 2 from the 7 zeros, subnormals and least normal, mode 3 is all -0.0
// (the partial sums start at +0.0, so the result is +0.0), and mode 4
// pairs -(1 + 2^-22) * 1 with (1 + 2^-23)^2 in every partial sum, which is
// 0 unfused and 2^-46 (f64: 2^-104) under a fused multiply-add. A NaN
// result hashes as one canonical pattern (its sign and payload are left
// open). Main prints Edge at compile time (a prefix of the
// lengths and offsets, within the evaluator's fuel) and at run time (all,
// and the same prefix).
const sdEdgeProgram = `import std.simd

const P32: [16]u32 = [0x00000000, 0x80000000, 0x00000001, 0x80000001, 0x007FFFFF, 0x807FFFFF, 0x00800000, 0x3F800000, 0xBF800000, 0x7149F2CA, 0xF149F2CA, 0x7F7FFFFF, 0xFF7FFFFF, 0x7F800000, 0xFF800000, 0x7FC00000]
const P64: [16]u64 = [0, 0x8000000000000000, 1, 0x8000000000000001, 0x000FFFFFFFFFFFFF, 0x800FFFFFFFFFFFFF, 0x0010000000000000, 0x3FF0000000000000, 0xBFF0000000000000, 0x7E37E43C8800759C, 0xFE37E43C8800759C, 0x7FEFFFFFFFFFFFFF, 0xFFEFFFFFFFFFFFFF, 0x7FF0000000000000, 0xFFF0000000000000, 0x7FF8000000000000]
const EdgeLens: [26]int = [0, 1, 2, 7, 15, 16, 17, 31, 32, 33, 48, 63, 64, 65, 66, 79, 80, 95, 96, 100, 127, 128, 129, 200, 255, 256]

Mix(x: u64): u64 {
    var z := x
    z = (z ^ (z >> 30)) *% 0xBF58476D1CE4E5B9
    z = (z ^ (z >> 27)) *% 0x94D049BB133111EB
    return z ^ (z >> 31)
}

F32(b: u32): f32 {
    return simd.Vec[u32, 2].Splat(b).Reinterpret[f32, 2]().Lane(0)
}

F64(b: u64): f64 {
    return simd.Vec[u64, 2].Splat(b).Reinterpret[f64, 2]().Lane(0)
}

C32(x: f32): u64 {
    if x != x {
        return 0x7FC00000
    }
    return Cast[u64](simd.Vec[f32, 2].Splat(x).Reinterpret[u32, 2]().Lane(0))
}

C64(x: f64): u64 {
    if x != x {
        return 0x7FF8000000000000
    }
    return simd.Vec[f64, 2].Splat(x).Reinterpret[u64, 2]().Lane(0)
}

HB(h: u64, b: u64): u64 {
    return (h ^ b) *% 0x100000001b3
}

Idx(mode: int, r: u64): int {
    if mode == 0 {
        return Cast[int](r % 16)
    }
    if mode == 1 {
        return Cast[int](r % 13)
    }
    if mode == 2 {
        return Cast[int](r % 7)
    }
    return 1
}

Edge(nl: int, no: int): u64 {
    var x32: [272]f32 = [0.0; 272]
    var y32: [272]f32 = [0.0; 272]
    var x64: [272]f64 = [0.0; 272]
    var y64: [272]f64 = [0.0; 272]
    var h: u64 = 0
    for mode in 0..5 {
        for i in 0..272 {
            if mode == 4 {
                if i % 32 < 16 {
                    x32[i] = F32(0xBF800002)
                    y32[i] = 1.0
                    x64[i] = F64(0xBFF0000000000002)
                    y64[i] = 1.0
                } else {
                    x32[i] = F32(0x3F800001)
                    y32[i] = F32(0x3F800001)
                    x64[i] = F64(0x3FF0000000000001)
                    y64[i] = F64(0x3FF0000000000001)
                }
            } else {
                kx := Idx(mode, Mix(Cast[u64](i)))
                ky := Idx(mode, Mix(Cast[u64](i) +% 1000))
                x32[i] = F32(P32[kx])
                y32[i] = F32(P32[ky])
                x64[i] = F64(P64[kx])
                y64[i] = F64(P64[ky])
            }
        }
        for li in 0..nl {
            n := EdgeLens[li]
            for o in 0..no {
                h = HB(h, C32(simd.Dot(x32[o..o + n], y32[o..o + n])))
                h = HB(h, C32(simd.Sum(x32[o..o + n])))
                h = HB(h, C64(simd.Dot(x64[o..o + n], y64[o..o + n])))
                h = HB(h, C64(simd.Sum(x64[o..o + n])))
            }
        }
    }
    return h
}

// The evaluator's fuel (4,000,000 steps) covers the lengths to 64 at
// offsets 0 and 1; the run covers them all.
const CE: u64 = Edge(13, 2)

export Main(sys: &System): OutOfMemory|WriteError!void {
    var out := sys.Stdout()
    try Print(mut out, "EC {c}\nER {r}\nEX {x}\n", (c: CE, r: Edge(26, 16), x: Edge(13, 2)))
}
`

func sdEdgeModel() string {
	return fmt.Sprintf("EC %d\nER %d\nEX %d\n", sdEdgeHash(13, 2), sdEdgeHash(len(sdEdgeLens), 16), sdEdgeHash(13, 2))
}

// sdEdgeHash is the model of Edge(nl, no).
func sdEdgeHash(nl, no int) uint64 {
	const m = 272
	x32, y32 := make([]float32, m), make([]float32, m)
	x64, y64 := make([]float64, m), make([]float64, m)
	idx := func(mode int, r uint64) int {
		switch mode {
		case 0:
			return int(r % 16)
		case 1:
			return int(r % 13)
		case 2:
			return int(r % 7)
		}
		return 1
	}
	c32 := func(x float32) uint64 {
		if x != x {
			return 0x7FC00000
		}
		return uint64(math.Float32bits(x))
	}
	c64 := func(x float64) uint64 {
		if x != x {
			return 0x7FF8000000000000
		}
		return math.Float64bits(x)
	}
	hb := func(h, b uint64) uint64 { return (h ^ b) * 0x100000001b3 }
	var h uint64
	for mode := 0; mode < 5; mode++ {
		for i := 0; i < m; i++ {
			if mode == 4 {
				if i%32 < 16 {
					x32[i], y32[i] = math.Float32frombits(0xBF800002), 1
					x64[i], y64[i] = math.Float64frombits(0xBFF0000000000002), 1
				} else {
					x32[i], y32[i] = math.Float32frombits(0x3F800001), math.Float32frombits(0x3F800001)
					x64[i], y64[i] = math.Float64frombits(0x3FF0000000000001), math.Float64frombits(0x3FF0000000000001)
				}
				continue
			}
			kx, ky := idx(mode, sdMix(uint64(i))), idx(mode, sdMix(uint64(i)+1000))
			x32[i], y32[i] = math.Float32frombits(sdP32[kx]), math.Float32frombits(sdP32[ky])
			x64[i], y64[i] = math.Float64frombits(sdP64[kx]), math.Float64frombits(sdP64[ky])
		}
		for _, n := range sdEdgeLens[:nl] {
			for o := 0; o < no; o++ {
				h = hb(h, c32(sdDot32(x32[o:o+n], y32[o:o+n])))
				h = hb(h, c32(sdSum32(x32[o:o+n])))
				h = hb(h, c64(sdDot64(x64[o:o+n], y64[o:o+n])))
				h = hb(h, c64(sdSum64(x64[o:o+n])))
			}
		}
	}
	return h
}

// TestSTDSIMD_EdgeValues: Dot and Sum over edge values agree bit for
// bit with the model at compile time and at run time (the dispatched,
// rotated avx2 forms on an avx2 machine); the baseline instances run under
// TestSTDSIMD_BaselineWidth.
func TestSTDSIMD_EdgeValues(t *testing.T) {
	t.Parallel()
	want := sdEdgeModel()
	for _, flags := range c2Flags {
		t.Run(c2FlagName(flags), func(t *testing.T) {
			t.Parallel()
			diffLines(t, sdRun(t, sdEdgeProgram, flags...), want)
		})
	}
}

// TestSTDSIMD_ModelDetectsFusion: the model's mode 4 would differ
// under a fused multiply-add, so a fusing lowering cannot pass the edge
// test: math.FMA gives the 2^-46 the unfused order cancels.
func TestSTDSIMD_ModelDetectsFusion(t *testing.T) {
	a, one, c := math.Float32frombits(0xBF800002), float32(1), math.Float32frombits(0x3F800001)
	unfused := float32(float32(0+float32(a*one)) + float32(c*c))
	fused := float32(math.FMA(float64(c), float64(c), float64(float32(0+float32(a*one)))))
	if unfused != 0 || fused == 0 {
		t.Fatalf("unfused %g, fused %g: want 0 and nonzero", unfused, fused)
	}
	a64, c64 := math.Float64frombits(0xBFF0000000000002), math.Float64frombits(0x3FF0000000000001)
	if u, f := (0+a64*1)+float64(c64*c64), math.FMA(c64, c64, 0+a64*1); u != 0 || f == 0 {
		t.Fatalf("f64: unfused %g, fused %g: want 0 and nonzero", u, f)
	}
}
