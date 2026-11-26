// Why: std.core's byte paths must stay cheap at compile time; a slower form
// would quietly raise every compile-time evaluation's cost.
// What: core11Guards, a program of consts that each run one byte path with
// a stated fuel, checked on core11GuardTargets.
// How: each fuel sits between this std's need and that of the form it rules
// out, so `mk check` is E0902 once a path costs a rejected form's steps.

package std

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// core11GuardTargets are the targets the guard program is checked on: the
// six the guards were evaluated on in std.core.
var core11GuardTargets = []string{"x86_64-windows", "x86_64-freestanding", "x86_64-linux", "aarch64-linux", "x86_64-nutshell", "aarch64-nutshell"}

// core11Guards is the guard program: tests_bytes.mk's former guards, their
// helpers and their test block, as they were in tests_bytes.mk (but for
// two sentences on where they are checked, and needs measured again on this
// std), and an empty Main.
//
// The guards live in a user program, not in std/core/tests_bytes.mk: as
// consts of std.core they read std.simd's layouts at compile time, and the
// front cache looks std.core, in the prelude's closure, up before any
// layout is answered, so std.core missed the front cache on every warm
// check and build. In a user program the same calls take the same steps
// (the needs in the comments were measured in user programs) and std.core
// hits.
const core11Guards = `// The fuel guards over std.core's byte paths (std/core_guards_test.go).

// The guards below hold one path each to its measured compile-time steps.
// Each fuel was set from needs bisected with mk check, as the reference std
// (an earlier std's forms of these paths, kept as the baseline), this std,
// and the mutant the guard is for, on x86_64-windows and x86_64-freestanding.
// The needs written here are exact (the least clean fuel of the same calls in
// a user program). Those of this std and the reference are checked
// (core11GuardNeeds); a mutant's was measured on a copy of std with its one
// change and is not checked. A mutant's need marked "on the 0.5% grid" is a
// bisection to 0.5% on an earlier std, not measured again on this one. Each
// guard is evaluated wherever this program is checked, on six targets
// (TestCore_FuelGuards). On x86_64-freestanding std.core keeps
// its own forms, which need the reference's steps, so the three guards over 4
// KiB and the two that pin ValidUtf8's hand-off from above, set below what
// those forms need, and TbySplit20Hosted, whose fuel leaves those forms 0.6%
// (which TbySplit20 holds there instead), return their value there without
// the work; every other guard runs there too.

// TbySplit sums the lengths of t's pieces split at commas: Split alone,
// so IndexByteIn's word prefix is all a guard over it measures.
TbySplit(t: str): int {
    var n := 0
    var it := t.Split(",")
    for p := it.Next() {
        n += p.Len()
    }
    return n
}

// Forty 9-byte fields. The reference needs 16,229 steps and this std 16,447;
// IndexByteIn calling std.simd whenever 16 bytes are left (an earlier
// form) needs 29,272 on the 0.5% grid. The fuel is 1.3
// times the reference's.
const TbySplitAtCompileTime: int = TbySplit("k001=vx01,k002=vx02,k003=vx03,k004=vx04,k005=vx05,k006=vx06,k007=vx07,k008=vx08,k009=vx09,k010=vx10,k011=vx11,k012=vx12,k013=vx13,k014=vx14,k015=vx15,k016=vx16,k017=vx17,k018=vx18,k019=vx19,k020=vx20,k021=vx21,k022=vx22,k023=vx23,k024=vx24,k025=vx25,k026=vx26,k027=vx27,k028=vx28,k029=vx29,k030=vx30,k031=vx31,k032=vx32,k033=vx33,k034=vx34,k035=vx35,k036=vx36,k037=vx37,k038=vx38,k039=vx39,k040=vx40,") fuel 21_000

// TbySplitHosted is TbySplit(t) on a hosted target, and want on
// x86_64-freestanding, without the work.
TbySplitHosted(t: str, want: int): int {
    if Target.os == Os.Freestanding {
        return want
    }
    return TbySplit(t)
}

// Forty 19-byte fields, each comma 19 bytes past from, in the third word of
// IndexByteIn's prefix. The reference needs 29,805 and this std 22,123, on
// x86_64-freestanding 29,805; a word prefix of 16 bytes (the i16 mutant)
// needs 34,685. One fuel cannot hold both, so there are two guards.
// TbySplit20 runs on every target at 1.3 times the reference's need, which on
// x86_64-freestanding is this std's need. TbySplit20Hosted, the same split
// behind TbySplitHosted (the reference 29,821, this std 22,139, i16 34,701),
// holds a hosted target at the reference's fuel, so the 16-byte prefix still
// fails it.
const TbySplit20AtCompileTime: int = TbySplit("k001=vx01yyyyyyyyyy,k002=vx02yyyyyyyyyy,k003=vx03yyyyyyyyyy,k004=vx04yyyyyyyyyy,k005=vx05yyyyyyyyyy,k006=vx06yyyyyyyyyy,k007=vx07yyyyyyyyyy,k008=vx08yyyyyyyyyy,k009=vx09yyyyyyyyyy,k010=vx10yyyyyyyyyy,k011=vx11yyyyyyyyyy,k012=vx12yyyyyyyyyy,k013=vx13yyyyyyyyyy,k014=vx14yyyyyyyyyy,k015=vx15yyyyyyyyyy,k016=vx16yyyyyyyyyy,k017=vx17yyyyyyyyyy,k018=vx18yyyyyyyyyy,k019=vx19yyyyyyyyyy,k020=vx20yyyyyyyyyy,k021=vx21yyyyyyyyyy,k022=vx22yyyyyyyyyy,k023=vx23yyyyyyyyyy,k024=vx24yyyyyyyyyy,k025=vx25yyyyyyyyyy,k026=vx26yyyyyyyyyy,k027=vx27yyyyyyyyyy,k028=vx28yyyyyyyyyy,k029=vx29yyyyyyyyyy,k030=vx30yyyyyyyyyy,k031=vx31yyyyyyyyyy,k032=vx32yyyyyyyyyy,k033=vx33yyyyyyyyyy,k034=vx34yyyyyyyyyy,k035=vx35yyyyyyyyyy,k036=vx36yyyyyyyyyy,k037=vx37yyyyyyyyyy,k038=vx38yyyyyyyyyy,k039=vx39yyyyyyyyyy,k040=vx40yyyyyyyyyy,") fuel 38_700

const TbySplit20HostedAtCompileTime: int = TbySplitHosted("k001=vx01yyyyyyyyyy,k002=vx02yyyyyyyyyy,k003=vx03yyyyyyyyyy,k004=vx04yyyyyyyyyy,k005=vx05yyyyyyyyyy,k006=vx06yyyyyyyyyy,k007=vx07yyyyyyyyyy,k008=vx08yyyyyyyyyy,k009=vx09yyyyyyyyyy,k010=vx10yyyyyyyyyy,k011=vx11yyyyyyyyyy,k012=vx12yyyyyyyyyy,k013=vx13yyyyyyyyyy,k014=vx14yyyyyyyyyy,k015=vx15yyyyyyyyyy,k016=vx16yyyyyyyyyy,k017=vx17yyyyyyyyyy,k018=vx18yyyyyyyyyy,k019=vx19yyyyyyyyyy,k020=vx20yyyyyyyyyy,k021=vx21yyyyyyyyyy,k022=vx22yyyyyyyyyy,k023=vx23yyyyyyyyyy,k024=vx24yyyyyyyyyy,k025=vx25yyyyyyyyyy,k026=vx26yyyyyyyyyy,k027=vx27yyyyyyyyyy,k028=vx28yyyyyyyyyy,k029=vx29yyyyyyyyyy,k030=vx30yyyyyyyyyy,k031=vx31yyyyyyyyyy,k032=vx32yyyyyyyyyy,k033=vx33yyyyyyyyyy,k034=vx34yyyyyyyyyy,k035=vx35yyyyyyyyyy,k036=vx36yyyyyyyyyy,k037=vx37yyyyyyyyyy,k038=vx38yyyyyyyyyy,k039=vx39yyyyyyyyyy,k040=vx40yyyyyyyyyy,", 40 * 19) fuel 30_000

// TbySparse runs str.From forty times over 100 bytes, ASCII but for one
// two-byte scalar at byte 5: mostly-ASCII text below 256 bytes, on the SWAR
// form. The reference needs 47,649 and this std 47,849; handing the rest to
// std.simd from the first non-ASCII byte needs 153,414 on the 0.5% grid. The
// fuel is 1.3 times the reference's.
TbySparse(): int {
    var buf: [100]u8 = [0x78; 100]
    buf[5] = 0xC3
    buf[6] = 0xA9
    var n := 0
    for _ in 0..40 {
        _ = str.From(buf[0..]) else {
            return -1
        }
        n += 1
    }
    return n
}

const TbySparseAtCompileTime: int = TbySparse() fuel 62_000

// TbyFromShape runs str.From calls times over the first n bytes of a 256-byte
// buffer: shape 0 is ASCII but for one two-byte scalar at byte 5, 1 is
// two-byte scalars (an odd n ends on one ASCII byte), 2 is 9 two-byte
// scalars, then ASCII. The guards over it hold ValidUtf8's 256-byte boundary
// and its SWAR form below it to 1.3 times the reference's steps, on every
// target: on x86_64-freestanding, where std.core keeps its own forms, they
// need the reference's steps.
TbyFromShape(n: int, shape: int, calls: int): int {
    var buf: [256]u8 = [0x78; 256]
    if shape == 0 {
        buf[5] = 0xC3
        buf[6] = 0xA9
    } else if shape == 1 {
        for i in 0..n / 2 {
            buf[2 * i] = 0xD0
            buf[2 * i + 1] = 0xB0
        }
    } else {
        for i in 0..9 {
            buf[2 * i] = 0xC3
            buf[2 * i + 1] = 0xA9
        }
    }
    var k := 0
    for _ in 0..calls {
        _ = str.From(buf[0..n]) else {
            return -1
        }
        k += 1
    }
    return k
}

// Needs, the reference, then this std on x86_64-windows (x86_64-freestanding:
// the reference's), then a mutant each guard kills (six mutants mg1 to mg6,
// an earlier gate re-added with one change each, and g0, that gate itself):
//   255 bytes, one é: 23,745 and 23,795; ValidUtf8's threshold lowered to
//     128 bytes (t128).
//   256 bytes, one é: 24,095 and 30,675 (std.simd's form, 1.27 times); the
//     fork removed, so std.simd's word form runs on x86_64-freestanding
//     (fsu), and std.simd's word forms on x86_64-windows (std-words).
//   255 bytes, two-byte scalars: 98,858 and 98,878; the earlier gate, g0 and
//     mg1 to mg6, on x86_64-freestanding.
//   256 bytes, two-byte scalars: 99,497 and 34,073; fsu, std-words, g0 on
//     x86_64-freestanding.
//   100 bytes, 9 two-byte scalars then ASCII: 99,489 and
//     99,689; the earlier gate needs 213,964 on the 0.5% grid; g0 and mg1 to
//     mg6 on both targets.
// The str.From rows are the reference's steps plus exactly 5 per call below
// 256 bytes.
const TbyEdge255SparseAtCompileTime: int = TbyFromShape(255, 0, 10) fuel 30_900

const TbyEdge256SparseAtCompileTime: int = TbyFromShape(256, 0, 10) fuel 31_400

const TbyEdge255DenseAtCompileTime: int = TbyFromShape(255, 1, 4) fuel 128_700

const TbyEdge256DenseAtCompileTime: int = TbyFromShape(256, 1, 4) fuel 129_700

const TbyDenseHeadAtCompileTime: int = TbyFromShape(100, 2, 40) fuel 129_700

// TbyFromHosted runs str.From calls times over the first n bytes of a
// 300-byte buffer of two-byte scalars on a hosted target, and returns calls
// on x86_64-freestanding without the work. Its two guards pin ValidUtf8's
// hand-off at 256 bytes from above: the fuels are 1.39 and 1.45 times this
// std's need, far below the SWAR form's, so any threshold above 256 bytes
// fails the 256-byte guard. Needs, the reference (the SWAR form) and this std:
//   256 bytes: 99,477 and 34,053; ValidUtf8's threshold at 257, 512 or
//     1,024 bytes (t257, t512, t1024) 99,497.
//   300 bytes: 116,615 and 39,855; t512 and t1024 116,635. A threshold of
//     257 hands 300 bytes to std.simd and needs this std's 39,855, so only
//     the 256-byte guard kills it.
TbyFromHosted(n: int, calls: int): int {
    if Target.os == Os.Freestanding {
        return calls
    }
    var buf: [300]u8 = [0x78; 300]
    for i in 0..n / 2 {
        buf[2 * i] = 0xD0
        buf[2 * i + 1] = 0xB0
    }
    var k := 0
    for _ in 0..calls {
        _ = str.From(buf[0..n]) else {
            return -1
        }
        k += 1
    }
    return k
}

const TbyAbove256AtCompileTime: int = TbyFromHosted(256, 4) fuel 47_200

const TbyAbove300AtCompileTime: int = TbyFromHosted(300, 4) fuel 57_800

// TbyStartsShort runs StartsWith of a 9-byte and of a 10-byte text by itself,
// 20 times each: BytesEqual's two overlapping words, which hold 9 to 16 bytes
// off std.simd. Per call the reference's word loop needs 254 and 298 steps,
// this std's two words 307, and std.simd's Equal from 9 bytes (the be8
// mutant) 428 at every length from 9 to 15 and 416 at 16, while the
// reference's need grows 44 per byte: from 11 bytes be8 is within 1.3 times
// the reference, so the guard holds 9 and 10 bytes. Needs: the reference
// 10,929, this std 12,169 (1.11 times), on x86_64-freestanding 10,929; be8
// 17,009. The fuel is 1.3 times the reference's.
TbyStartsShort(): int {
    var n := 0
    for _ in 0..20 {
        if "k001=vx01".StartsWith("k001=vx01") {
            n += 1
        }
        if "k001=vx01y".StartsWith("k001=vx01y") {
            n += 1
        }
    }
    return n
}

const TbyStartsShortAtCompileTime: int = TbyStartsShort() fuel 14_200

// Forty 32-byte and forty 48-byte fields: Split in the band past
// IndexByteIn's 32-byte word prefix, where this std needs more steps than
// the reference, an accepted cost. 32 bytes: the reference 36,076, this
// std 41,350 (1.15 times); 48 bytes: 41,076 and 43,530 (1.06 times); on
// x86_64-freestanding the reference's. Killed by IndexByteIn calling std.simd
// whenever 16 bytes are left (idx), the fork removed on
// x86_64-freestanding (fsi) and std-words. The fuels are 1.3 times
// the reference's.
const TbySplit32AtCompileTime: int = TbySplit("k001=vx01yyyyyyyyyyyyyyyyyyyyyyy,k002=vx02yyyyyyyyyyyyyyyyyyyyyyy,k003=vx03yyyyyyyyyyyyyyyyyyyyyyy,k004=vx04yyyyyyyyyyyyyyyyyyyyyyy,k005=vx05yyyyyyyyyyyyyyyyyyyyyyy,k006=vx06yyyyyyyyyyyyyyyyyyyyyyy,k007=vx07yyyyyyyyyyyyyyyyyyyyyyy,k008=vx08yyyyyyyyyyyyyyyyyyyyyyy,k009=vx09yyyyyyyyyyyyyyyyyyyyyyy,k010=vx10yyyyyyyyyyyyyyyyyyyyyyy,k011=vx11yyyyyyyyyyyyyyyyyyyyyyy,k012=vx12yyyyyyyyyyyyyyyyyyyyyyy,k013=vx13yyyyyyyyyyyyyyyyyyyyyyy,k014=vx14yyyyyyyyyyyyyyyyyyyyyyy,k015=vx15yyyyyyyyyyyyyyyyyyyyyyy,k016=vx16yyyyyyyyyyyyyyyyyyyyyyy,k017=vx17yyyyyyyyyyyyyyyyyyyyyyy,k018=vx18yyyyyyyyyyyyyyyyyyyyyyy,k019=vx19yyyyyyyyyyyyyyyyyyyyyyy,k020=vx20yyyyyyyyyyyyyyyyyyyyyyy,k021=vx21yyyyyyyyyyyyyyyyyyyyyyy,k022=vx22yyyyyyyyyyyyyyyyyyyyyyy,k023=vx23yyyyyyyyyyyyyyyyyyyyyyy,k024=vx24yyyyyyyyyyyyyyyyyyyyyyy,k025=vx25yyyyyyyyyyyyyyyyyyyyyyy,k026=vx26yyyyyyyyyyyyyyyyyyyyyyy,k027=vx27yyyyyyyyyyyyyyyyyyyyyyy,k028=vx28yyyyyyyyyyyyyyyyyyyyyyy,k029=vx29yyyyyyyyyyyyyyyyyyyyyyy,k030=vx30yyyyyyyyyyyyyyyyyyyyyyy,k031=vx31yyyyyyyyyyyyyyyyyyyyyyy,k032=vx32yyyyyyyyyyyyyyyyyyyyyyy,k033=vx33yyyyyyyyyyyyyyyyyyyyyyy,k034=vx34yyyyyyyyyyyyyyyyyyyyyyy,k035=vx35yyyyyyyyyyyyyyyyyyyyyyy,k036=vx36yyyyyyyyyyyyyyyyyyyyyyy,k037=vx37yyyyyyyyyyyyyyyyyyyyyyy,k038=vx38yyyyyyyyyyyyyyyyyyyyyyy,k039=vx39yyyyyyyyyyyyyyyyyyyyyyy,k040=vx40yyyyyyyyyyyyyyyyyyyyyyy,") fuel 47_000

const TbySplit48AtCompileTime: int = TbySplit("k001=vx01yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy,k002=vx02yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy,k003=vx03yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy,k004=vx04yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy,k005=vx05yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy,k006=vx06yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy,k007=vx07yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy,k008=vx08yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy,k009=vx09yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy,k010=vx10yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy,k011=vx11yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy,k012=vx12yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy,k013=vx13yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy,k014=vx14yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy,k015=vx15yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy,k016=vx16yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy,k017=vx17yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy,k018=vx18yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy,k019=vx19yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy,k020=vx20yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy,k021=vx21yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy,k022=vx22yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy,k023=vx23yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy,k024=vx24yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy,k025=vx25yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy,k026=vx26yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy,k027=vx27yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy,k028=vx28yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy,k029=vx29yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy,k030=vx30yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy,k031=vx31yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy,k032=vx32yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy,k033=vx33yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy,k034=vx34yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy,k035=vx35yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy,k036=vx36yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy,k037=vx37yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy,k038=vx38yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy,k039=vx39yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy,k040=vx40yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy,") fuel 53_500

// Long inputs, where std.simd's forms need fewer steps than the word forms (a
// guard here because thresholds raised out of reach pass every other test).
// str.From of 4 KiB of ASCII: this std 12,433, the SWAR form (the reference)
// 34,931; a ValidUtf8 that never calls std.simd needs the same 34,931.
TbyFromLong(): int {
    if Target.os == Os.Freestanding {
        return 4096
    }
    var buf: [4096]u8 = [0x61; 4096]
    s := str.From(buf[0..]) else {
        return -1
    }
    return s.Len()
}

const TbyFromLongAtCompileTime: int = TbyFromLong() fuel 20_000

// Slice[u8].Index of the last of 4 KiB: this std 12,918, the reference
// 36,200, IndexByteIn never reaching std.simd 102,470.
TbyFindLong(): int {
    if Target.os == Os.Freestanding {
        return 4095
    }
    var buf: [4096]u8 = [0x61; 4096]
    buf[4095] = 0x3D
    var c: u8 = 0x3D
    if k := buf[0..].Index(&c) {
        return k
    }
    return -1
}

const TbyFindLongAtCompileTime: int = TbyFindLong() fuel 20_000

// StartsWith and EndsWith of a 4 KiB text by itself, after its str.From: this
// std 46,739, the reference 150,833, BytesEqual comparing bytes in place of
// std.simd's Equal 373,087.
TbyEqLong(): int {
    if Target.os == Os.Freestanding {
        return 2
    }
    var buf: [4096]u8 = [0x61; 4096]
    s := str.From(buf[0..]) else {
        return -1
    }
    var n := 0
    if s.StartsWith(s) {
        n += 1
    }
    if s.EndsWith(s) {
        n += 1
    }
    return n
}

const TbyEqLongAtCompileTime: int = TbyEqLong() fuel 70_000

test "the compile-time guards hold their values" {
    // Each guard above is checked when this program is (E0902 past its fuel);
    // here their values, against the same calls at run time.
    Assert(TbySplitAtCompileTime == 40 * 9)
    Assert(TbySplit20AtCompileTime == 40 * 19)
    Assert(TbySplit20HostedAtCompileTime == 40 * 19)
    Assert(TbySplitAtCompileTime == TbySplit("k001=vx01,k002=vx02,k003=vx03,k004=vx04,k005=vx05,k006=vx06,k007=vx07,k008=vx08,k009=vx09,k010=vx10,k011=vx11,k012=vx12,k013=vx13,k014=vx14,k015=vx15,k016=vx16,k017=vx17,k018=vx18,k019=vx19,k020=vx20,k021=vx21,k022=vx22,k023=vx23,k024=vx24,k025=vx25,k026=vx26,k027=vx27,k028=vx28,k029=vx29,k030=vx30,k031=vx31,k032=vx32,k033=vx33,k034=vx34,k035=vx35,k036=vx36,k037=vx37,k038=vx38,k039=vx39,k040=vx40,"))
    Assert(TbySparseAtCompileTime == 40)
    Assert(TbySparse() == 40)
    Assert(TbyEdge255SparseAtCompileTime == 10)
    Assert(TbyEdge256SparseAtCompileTime == 10)
    Assert(TbyEdge255DenseAtCompileTime == 4)
    Assert(TbyEdge256DenseAtCompileTime == 4)
    Assert(TbyDenseHeadAtCompileTime == 40)
    Assert(TbyFromShape(255, 1, 4) == 4)
    Assert(TbyFromShape(100, 2, 40) == 40)
    Assert(TbyAbove256AtCompileTime == 4)
    Assert(TbyAbove300AtCompileTime == 4)
    Assert(TbyFromHosted(300, 4) == 4)
    Assert(TbyStartsShortAtCompileTime == 40)
    Assert(TbyStartsShort() == 40)
    Assert(TbySplit32AtCompileTime == 40 * 32)
    Assert(TbySplit48AtCompileTime == 40 * 48)
    Assert(TbyFromLongAtCompileTime == 4096)
    Assert(TbyFindLongAtCompileTime == 4095)
    Assert(TbyFindLong() == 4095)
    Assert(TbyEqLongAtCompileTime == 2)
    Assert(TbyEqLong() == 2)
}

export Main(sys: &System) {
}
`

// core11GuardNeeds is each guard's exact need with this std, the least fuel
// at which mk check of the guard program holds it: on x86_64-windows (each
// other hosted target needs the same steps), and the reference's, on
// x86_64-freestanding with the guards' freestanding returns turned off, so
// that every guard runs std.core's own forms there. The comments in
// core11Guards quote these numbers, and checkGuardNeeds holds both to the
// steps this std takes; they do not depend on the host, since the evaluator
// answers Cpu() with the target's baseline. A change that moves a need fails
// here: find the new need by bisecting that guard's fuel with mk check (clean
// at the need, E0902 one below), then write it here and in the comments.
var core11GuardNeeds = []struct {
	name                 string
	hosted, freestanding int
}{
	{"TbySplitAtCompileTime", 16_447, 16_229},
	{"TbySplit20AtCompileTime", 22_123, 29_805},
	{"TbySplit20HostedAtCompileTime", 22_139, 29_821},
	{"TbySparseAtCompileTime", 47_849, 47_649},
	{"TbyEdge255SparseAtCompileTime", 23_795, 23_745},
	{"TbyEdge256SparseAtCompileTime", 30_675, 24_095},
	{"TbyEdge255DenseAtCompileTime", 98_878, 98_858},
	{"TbyEdge256DenseAtCompileTime", 34_073, 99_497},
	{"TbyDenseHeadAtCompileTime", 99_689, 99_489},
	{"TbyAbove256AtCompileTime", 34_053, 99_477},
	{"TbyAbove300AtCompileTime", 39_855, 116_615},
	{"TbyStartsShortAtCompileTime", 12_169, 10_929},
	{"TbySplit32AtCompileTime", 41_350, 36_076},
	{"TbySplit48AtCompileTime", 43_530, 41_076},
	{"TbyFromLongAtCompileTime", 12_433, 34_931},
	{"TbyFindLongAtCompileTime", 12_918, 36_200},
	{"TbyEqLongAtCompileTime", 46_739, 150_833},
}

// guardConst matches a guard's line in core11Guards: its name, then its fuel.
var guardConst = regexp.MustCompile(`(?m)^const (Tby\w+): int = .*\) fuel ([0-9_]+)$`)

// withNeeds returns prog with each guard's fuel set to its need in
// core11GuardNeeds (the freestanding one if freestanding) plus delta, and
// each guard's line in it.
func withNeeds(t *testing.T, prog string, freestanding bool, delta int) (string, map[string]int) {
	t.Helper()
	need := map[string]int{}
	for _, g := range core11GuardNeeds {
		need[g.name] = g.hosted
		if freestanding {
			need[g.name] = g.freestanding
		}
	}
	lines := map[string]int{}
	out := guardConst.ReplaceAllStringFunc(prog, func(m string) string {
		sm := guardConst.FindStringSubmatch(m)
		n, ok := need[sm[1]]
		if !ok {
			t.Fatalf("guard %s has no need in core11GuardNeeds", sm[1])
		}
		lines[sm[1]] = 0
		return strings.TrimSuffix(m, sm[2]) + fmt.Sprint(n+delta)
	})
	if len(lines) != len(core11GuardNeeds) {
		t.Fatalf("the guard program holds %d guards, core11GuardNeeds %d", len(lines), len(core11GuardNeeds))
	}
	for i, l := range strings.Split(out, "\n") {
		if sm := guardConst.FindStringSubmatch(l); sm != nil {
			lines[sm[1]] = i + 1
		}
	}
	return out, lines
}

// commaInt writes n as the comments do, 30675 as 30,675.
func commaInt(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// checkGuardNeeds holds core11GuardNeeds exact: the guard program checks
// clean with every fuel at its need and is E0902 at every guard one step
// below, hosted on x86_64-windows and, with the freestanding returns off, on
// x86_64-freestanding. The comments must quote every need.
func checkGuardNeeds(t *testing.T, root, stdDir string) {
	t.Helper()
	for _, g := range core11GuardNeeds {
		for _, n := range []int{g.hosted, g.freestanding} {
			if !strings.Contains(core11Guards, commaInt(n)) {
				t.Errorf("%s: the comments do not quote its need %s", g.name, commaInt(n))
			}
		}
	}
	const ret = "if Target.os == Os.Freestanding {"
	if strings.Count(core11Guards, ret) != 5 {
		t.Fatalf("the guard program holds %q %d times, want 5", ret, strings.Count(core11Guards, ret))
	}
	for _, c := range []struct {
		target, prog string
		freestanding bool
	}{
		{"x86_64-windows", core11Guards, false},
		{"x86_64-freestanding", strings.ReplaceAll(core11Guards, ret, "if false {"), true},
	} {
		for _, delta := range []int{0, -1} {
			prog, lines := withNeeds(t, c.prog, c.freestanding, delta)
			if err := os.WriteFile(filepath.Join(root, "needs.mk"), []byte(prog), 0o644); err != nil {
				t.Fatal(err)
			}
			out, st := mkCmd(t, root, stdDir, "check", "--target", c.target, "needs.mk")
			if delta == 0 {
				if st != 0 {
					t.Errorf("mk check --target %s with every fuel at its need: exit %d, so a need is below this std's steps\n%.4000s", c.target, st, out)
				}
				continue
			}
			for _, g := range core11GuardNeeds {
				if !strings.Contains(out, fmt.Sprintf("needs.mk:%d:7: E0902", lines[g.name])) {
					t.Errorf("mk check --target %s with every fuel one below its need: no E0902 at %s, so its need is above this std's steps", c.target, g.name)
				}
			}
		}
	}
}

// TestCore_FuelGuards checks the guard program on each of
// core11GuardTargets (every guard within its fuel), then runs its test block
// in dev and release on this host (each guard's value against the same calls
// at run time). A first check with TbySplitAtCompileTime's fuel cut to 2,000
// must be E0902 at that const, so the harness is shown to evaluate the guards.
// checkGuardNeeds then holds the needs the comments quote to this std's steps.
func TestCore_FuelGuards(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	stdDir := copyStd(t, root, nil)
	main := filepath.Join(root, "main.mk")
	const fuel = ") fuel 21_000\n"
	if strings.Count(core11Guards, fuel) != 1 {
		t.Fatalf("the guard program holds %q %d times, want 1 (TbySplitAtCompileTime's fuel)", fuel, strings.Count(core11Guards, fuel))
	}
	if err := os.WriteFile(main, []byte(strings.Replace(core11Guards, fuel, ") fuel 2_000\n", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	out, st := mkCmd(t, root, stdDir, "check", "main.mk")
	if st == 0 || !strings.Contains(out, "E0902") || !strings.Contains(out, "TbySplitAtCompileTime") {
		t.Errorf("mk check with TbySplitAtCompileTime's fuel at 2,000: exit %d, want E0902 there\n%.2000s", st, out)
	}
	if err := os.WriteFile(main, []byte(core11Guards), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, target := range core11GuardTargets {
		out, st := mkCmd(t, root, stdDir, "check", "--target", target, "main.mk")
		if st != 0 {
			t.Errorf("mk check --target %s of the guard program: exit %d\n%.4000s", target, st, out)
		}
	}
	checkGuardNeeds(t, root, stdDir)
	for _, flags := range [][]string{nil, {"--release"}} {
		args := append(append([]string{"test"}, flags...), "main.mk")
		out, st := mkCmd(t, root, stdDir, args...)
		if strings.Contains(out, "no usable LLVM") {
			t.Skipf("the test block needs the LLVM toolchain (the checks above ran): %s", out)
		}
		if st != 0 || !strings.Contains(out, "1 passed, 0 failed") {
			t.Errorf("mk %s: exit %d, want the guards' block to pass\n%.4000s", strings.Join(args, " "), st, out)
		}
	}
}
