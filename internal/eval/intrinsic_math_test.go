// Why: the std.math intrinsics must give the same answers at compile time as
// at run time, at every width and edge value.
// What: tests of intrinsic_math.go: the overflow-flag forms, WrapI32 and
// WrapU32, CastSat, MulWide and DivWide.
// How: Go oracles: math/big for the overflow forms and the wide forms,
// ECMA-262's ToInt32 and ToUint32 steps written in Go, a big.Float clamp for
// CastSat; edge values and random bit patterns.

package eval

import (
	"math"
	"math/big"
	"math/rand"
	"testing"

	"mk/internal/types"
)

// vecIntWidths are the integer widths and signs the overflow forms take.
var vecIntWidths = []struct {
	w      int
	signed bool
}{{1, true}, {2, true}, {4, true}, {8, true}, {1, false}, {2, false}, {4, false}, {8, false}}

// bigOf is the w-byte pattern x as an exact integer.
func bigOf(x uint64, w int, signed bool) *big.Int {
	x &= mask(w)
	if signed {
		return big.NewInt(sext(x, w))
	}
	return new(big.Int).SetUint64(x)
}

// bigWrap is v reduced to w bytes, and whether v fits there.
func bigWrap(v *big.Int, w int, signed bool) (uint64, bool) {
	mod := new(big.Int).Lsh(big.NewInt(1), uint(8*w))
	r := new(big.Int).Mod(v, mod) // 0 <= r < mod
	lo, hi := big.NewInt(0), new(big.Int).Sub(mod, big.NewInt(1))
	if signed {
		half := new(big.Int).Rsh(mod, 1)
		lo, hi = new(big.Int).Neg(half), new(big.Int).Sub(half, big.NewInt(1))
	}
	return r.Uint64(), v.Cmp(lo) >= 0 && v.Cmp(hi) <= 0
}

func TestEvalOverflowForms(t *testing.T) {
	rng := rand.New(rand.NewSource(922))
	edges := []uint64{0, 1, 2, 0x7f, 0x80, 0xff, 0x7fff, 0x8000, 0xffff, 0x7fffffff, 0x80000000, 0xffffffff, 1<<63 - 1, 1 << 63, ^uint64(0)}
	for _, it := range vecIntWidths {
		check := func(a, b uint64) {
			x, y := bigOf(a, it.w, it.signed), bigOf(b, it.w, it.signed)
			for op, exact := range map[ovfOp]*big.Int{
				ovfAdd: new(big.Int).Add(x, y), ovfSub: new(big.Int).Sub(x, y), ovfMul: new(big.Int).Mul(x, y),
			} {
				wv, fits := bigWrap(exact, it.w, it.signed)
				if v, ov := overflowOp(op, a, b, it.w, it.signed); v != wv || ov == fits {
					t.Fatalf("op %d w %d signed %v (%#x, %#x) = (%#x, %v), want (%#x, %v)", op, it.w, it.signed, a, b, v, ov, wv, !fits)
				}
			}
		}
		for _, a := range edges {
			for _, b := range edges {
				check(a&mask(it.w), b&mask(it.w))
			}
		}
		for i := 0; i < 5000; i++ {
			check(rng.Uint64()&mask(it.w), rng.Uint64()&mask(it.w))
		}
	}
}

// ecmaToUint32 is ECMA-262 (2024) 7.1.7 ToUint32 for a Number, step by
// step: NaN, +0, -0, +inf and -inf give +0; int is the mathematical value
// truncated toward zero; int32bit is int modulo 2^32. ToInt32 (7.1.6) is
// int32bit, less 2^32 when it is 2^31 or more, which is the same 32 bits.
func ecmaToUint32(x float64) uint32 {
	if math.IsNaN(x) || math.IsInf(x, 0) || x == 0 {
		return 0
	}
	n := new(big.Float).SetFloat64(math.Trunc(x))
	i, _ := n.Int(nil)
	return uint32(new(big.Int).Mod(i, new(big.Int).Lsh(big.NewInt(1), 32)).Uint64())
}

func ecmaToInt32(x float64) int32 {
	u := int64(ecmaToUint32(x))
	if u >= 1<<31 {
		u -= 1 << 32
	}
	return int32(u)
}

func TestEvalWrap32(t *testing.T) {
	edges := []float64{0, math.Copysign(0, -1), 0.5, -0.5, 1, -1, 1.9, -1.9, 2147483647, 2147483648, -2147483648, -2147483649,
		4294967295, 4294967296, 4294967297.5, -4294967297.5, 1e10, -1e10, 9007199254740991, 9007199254740993, 1 << 63, 1e300, -1e300,
		math.MaxFloat64, math.SmallestNonzeroFloat64, math.Inf(1), math.Inf(-1), math.NaN()}
	check := func(x float64) {
		b := math.Float64bits(x)
		if got, want := wrap32(b), ecmaToUint32(x); got != want {
			t.Fatalf("WrapU32(%v) = %d, ToUint32 %d", x, got, want)
		}
		if got, want := int32(wrap32(b)), ecmaToInt32(x); got != want {
			t.Fatalf("WrapI32(%v) = %d, ToInt32 %d", x, got, want)
		}
	}
	for _, x := range edges {
		check(x)
	}
	rng := rand.New(rand.NewSource(2))
	for i := 0; i < 50000; i++ {
		x := math.Float64frombits(rng.Uint64())
		if i%2 == 0 {
			x = (rng.Float64() - 0.5) * math.Ldexp(1, rng.Intn(80))
		}
		check(x)
	}
}

func TestEvalCastSat(t *testing.T) {
	f64 := &tinfo{class: cFloat, width: 8}
	f32 := &tinfo{class: cFloat, width: 4}
	rng := rand.New(rand.NewSource(3))
	for _, it := range vecIntWidths {
		to := &tinfo{class: cInt, width: it.w, signed: it.signed}
		check := func(x float64, from *tinfo, bitsX uint64) {
			var want uint64
			if !math.IsNaN(x) {
				var i *big.Int
				if math.IsInf(x, 0) {
					i = new(big.Int).Lsh(big.NewInt(int64(math.Copysign(1, x))), 100) // beyond every range
				} else {
					i, _ = new(big.Float).SetFloat64(math.Trunc(x)).Int(nil)
				}
				lo, hi := big.NewInt(0), new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), uint(8*it.w)), big.NewInt(1))
				if it.signed {
					hi = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), uint(8*it.w-1)), big.NewInt(1))
					lo = new(big.Int).Neg(new(big.Int).Add(hi, big.NewInt(1)))
				}
				if i.Cmp(lo) < 0 {
					i = lo
				}
				if i.Cmp(hi) > 0 {
					i = hi
				}
				want, _ = bigWrap(i, it.w, it.signed)
			}
			if got, pc := castSat(from, to, bitsX); got != want || pc != types.PanicNone {
				t.Fatalf("CastSat[w %d signed %v](%v) = %#x, want %#x", it.w, it.signed, x, got, want)
			}
		}
		for _, x := range []float64{0, -0.9, 0.9, 127.5, 128, -128.5, -129, 255.9, 256, 65535, 1e19, -1e19, 1 << 63, -(1 << 63), math.Inf(1), math.Inf(-1), math.NaN()} {
			check(x, f64, math.Float64bits(x))
			check(float64(float32(x)), f32, uint64(math.Float32bits(float32(x))))
		}
		for i := 0; i < 3000; i++ {
			x := (rng.Float64() - 0.5) * math.Ldexp(1, rng.Intn(70))
			check(x, f64, math.Float64bits(x))
		}
	}
}

func TestEvalKeys(t *testing.T) {
	for _, key := range []string{"std.math.AddOverflow", "std.math.SubOverflow", "std.math.MulOverflow", "std.math.WrapI32",
		"std.math.WrapU32", "std.math.CastSat", "std.math.CastSatF32", "std.math.MulWide", "std.math.DivWide", "std.math.AddCarry",
		"std.math.SubBorrow", "std.mem.OffsetOf", "std.mem.Prefetch", "std.mem.Zeroed", "std.atomic.Pause", "std.raw.AtomicCopy", "std.raw.AtomicFill"} {
		if _, ok := intrinsics[key]; !ok {
			t.Errorf("eval has no intrinsic %s", key)
		}
	}
	m := &Machine{}
	for key, want := range map[string]types.Refusal{"std.runtime.Cycles": types.RefuseExtern, "std.raw.FnFromAddr": types.RefusePointerBytes} {
		fn, ok := intrinsics[key]
		if !ok {
			t.Errorf("eval has no intrinsic %s", key)
			continue
		}
		func() {
			defer func() {
				st, _ := recover().(*stop)
				if st == nil || st.err == nil || st.err.Kind != types.EvalRefused || st.err.Refusal != want {
					t.Errorf("%s: want a refusal %v, got %+v", key, want, st)
				}
			}()
			fn(m, nil, nil, types.FnRef{})
		}()
	}
}
