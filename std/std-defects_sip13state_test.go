// Why: hash.Sip13State feeds SipHash-1-3 in pieces and keeps a partial
// 8-byte word, where a split bug would hide.
// What: TestStdDefects_Sip13StateMatchesOneShot,
// TestStdDefects_Sip13StateRustVectors and
// TestStdDefects_Sip13StateZeroFillRejected.
// How: after every piece, compares Sum with the one-shot hash.Sip13 and the
// Go model hmSip13Keyed (checked against Rust by
// TestSTDHASHMOD_ZeroKeyMatchesRust), for every split of short inputs into
// two and three pieces and longer split shapes, under several keys; feeds
// Rust's DefaultHasher vectors in pieces; and checks that a zero-filled
// state is refused.

package std

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

// sd9SipKeys are the keys the cases cycle through: (0, 0), the published
// vectors' key, all ones and an unequal pair.
var sd9SipKeys = [][2]uint64{
	{0, 0},
	{0x0706050403020100, 0x0f0e0d0c0b0a0908},
	{^uint64(0), ^uint64(0)},
	{0x9E3779B97F4A7C15, 1},
}

// sd9SipCase is one stream: a key index, then buf[off..off + sum(pieces)]
// fed in pieces, Sum printed after every piece.
type sd9SipCase struct {
	key    int
	off    int
	pieces []int
}

// sd9SipCases are the shapes: every split of every length from 0 to 40
// bytes into two pieces, and of every length from 0 to 12 into three
// (empty pieces among them); one byte at a time up to 33 bytes; runs of
// 1, 3, 7, 8, 9, 15, 16 and 17-byte pieces, so a pending word is
// completed, left partial and completed with whole words after it; random
// splits; one 64 KiB stream in 4,099-byte pieces.
func sd9SipCases() []sd9SipCase {
	var cs []sd9SipCase
	k := 0
	add := func(off int, ps ...int) {
		cs = append(cs, sd9SipCase{key: k % len(sd9SipKeys), off: off, pieces: ps})
		k++
	}
	for n := 0; n <= 40; n++ {
		for a := 0; a <= n; a++ {
			add(n%5, a, n-a)
		}
	}
	for n := 0; n <= 12; n++ {
		for a := 0; a <= n; a++ {
			for b := 0; a+b <= n; b++ {
				add(n%3, a, b, n-a-b)
			}
		}
	}
	for _, n := range []int{7, 8, 9, 16, 33} {
		ones := make([]int, n)
		for i := range ones {
			ones[i] = 1
		}
		add(1, ones...)
	}
	for i, p := range []int{1, 3, 7, 8, 9, 15, 16, 17} {
		ps := make([]int, 7)
		for j := range ps {
			ps[j] = p
		}
		add(i%8, ps...)
	}
	add(2, 5, 3, 0, 8, 7, 1, 16, 0, 9)
	src := rand.New(rand.NewPCG(13, 64))
	for range 40 {
		var ps []int
		for range 1 + src.IntN(10) {
			ps = append(ps, src.IntN(40))
		}
		add(src.IntN(8), ps...)
	}
	big := make([]int, 16)
	for i := range big {
		big[i] = 4096 + 3
	}
	add(3, big...)
	return cs
}

func sd9SipMax(cs []sd9SipCase) int {
	m := 0
	for _, c := range cs {
		n := c.off
		for _, p := range c.pieces {
			n += p
		}
		m = max(m, n)
	}
	return m
}

func sd9Join(xs []int) string {
	s := make([]string, len(xs))
	for i, x := range xs {
		s[i] = fmt.Sprint(x)
	}
	return strings.Join(s, ", ")
}

// sd9SipProgram feeds each case into a fresh Sip13State and prints, after
// every piece, Sum and hash.Sip13 of the bytes so far.
func sd9SipProgram(cs []sd9SipCase) string {
	var keys, offs, counts, pieces []int
	for _, c := range cs {
		keys = append(keys, c.key)
		offs = append(offs, c.off)
		counts = append(counts, len(c.pieces))
		pieces = append(pieces, c.pieces...)
	}
	var k0s, k1s []string
	for _, k := range sd9SipKeys {
		k0s = append(k0s, fmt.Sprintf("0x%x", k[0]))
		k1s = append(k1s, fmt.Sprintf("0x%x", k[1]))
	}
	return h2Prelude + fmt.Sprintf(`
const K0: [%d]u64 = [%s]
const K1: [%d]u64 = [%s]
const Keys: [%d]int = [%s]
const Offs: [%d]int = [%s]
const Counts: [%d]int = [%s]
const Pieces: [%d]int = [%s]

export Main(sys: &System): WriteError|OutOfMemory!void {
    var out := sys.Stdout()
    var r := Rng(s: 29)
    buf := try sys.arena.Slice[u8](%d)
    Fill(mut r, buf)
    var q := 0
    for c in 0..%d {
        key := hash.TestKey(K0[Keys[c]], K1[Keys[c]])
        var s := hash.Sip13State.New(key)
        start := Offs[c]
        var at := start
        for _ in 0..Counts[c] {
            p := Pieces[q]
            q += 1
            s.Bytes(buf[at..at + p])
            at += p
            x := s.Sum()
            y := hash.Sip13(buf[start..at], key)
            try Print(mut out, "{x:x} {y:x}\n", (x: x, y: y))
        }
    }
}
`, len(k0s), strings.Join(k0s, ", "), len(k1s), strings.Join(k1s, ", "),
		len(keys), sd9Join(keys), len(offs), sd9Join(offs), len(counts), sd9Join(counts),
		len(pieces), sd9Join(pieces), sd9SipMax(cs), len(cs))
}

// sd9SipWant is sd9SipProgram's output from the Go model.
func sd9SipWant(cs []sd9SipCase) string {
	buf := make([]byte, sd9SipMax(cs))
	r := hmSplitmix{s: 29}
	r.fill(buf)
	var b strings.Builder
	for _, c := range cs {
		key := sd9SipKeys[c.key]
		at := c.off
		for _, p := range c.pieces {
			at += p
			h := hmSip13Keyed(buf[c.off:at], key[0], key[1])
			fmt.Fprintf(&b, "%x %x\n", h, h)
		}
	}
	return b.String()
}

// sd9SipFixtures give the programs hash.TestKey, a key with chosen words.
var sd9SipFixtures = map[string]string{
	"core/tests_hashmod.mk": hashModCore,
	"hash/tests_sip13.mk":   hashModSip,
}

// TestStdDefects_Sip13StateMatchesOneShot: Sip13State's Sum after every
// piece equals hash.Sip13 of the bytes so far and the Go model, dev and
// release.
func TestStdDefects_Sip13StateMatchesOneShot(t *testing.T) {
	t.Parallel()
	cs := sd9SipCases()
	src, want := sd9SipProgram(cs), sd9SipWant(cs)
	for _, flags := range [][]string{nil, {"--release"}} {
		name := "dev"
		if flags != nil {
			name = "release"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			diffLines(t, h2Run(t, sd9SipFixtures, src, flags...), want)
		})
	}
}

// sd9SipRust feeds hmSip13RustZero's messages, msg[i] = (7*i + 3) mod 256,
// into a Sip13State under a zero-filled HashKey (the key (0, 0), public
// names only) in pieces of 1, 3 and 5 bytes and in two halves.
const sd9SipRust = `import std.hash

const Lens: [13]int = [0, 1, 7, 8, 9, 15, 16, 63, 64, 65, 255, 256, 300]
const Steps: [3]int = [1, 3, 5]

export Main(sys: &System): WriteError|OutOfMemory!void {
    var out := sys.Stdout()
    msg := try sys.arena.Slice[u8](300)
    for i in 0..300 {
        msg[i] = Trunc[u8](i * 7 + 3)
    }
    keys := try sys.arena.Slice[HashKey](1)
    key := keys[0]
    for k in 0..13 {
        n := Lens[k]
        for j in 0..3 {
            var s := hash.Sip13State.New(key)
            var i := 0
            for i < n {
                e := if n - i < Steps[j] { n } else { i + Steps[j] }
                s.Bytes(msg[i..e])
                i = e
            }
            h := s.Sum()
            try Print(mut out, "{n} {h:x}\n", (n: n, h: h))
        }
        var s := hash.Sip13State.New(key)
        s.Bytes(msg[0..n / 2])
        s.Bytes(msg[n / 2..n])
        h := s.Sum()
        try Print(mut out, "{n} {h:x}\n", (n: n, h: h))
    }
}
`

// TestStdDefects_Sip13StateRustVectors: Sip13State, fed in pieces, prints
// the values Rust's DefaultHasher printed (hmSip13RustZero), release.
func TestStdDefects_Sip13StateRustVectors(t *testing.T) {
	t.Parallel()
	var want strings.Builder
	for _, v := range hmSip13RustZero {
		for range 4 {
			fmt.Fprintf(&want, "%d %s\n", v.n, strings.TrimLeft(v.hex, "0"))
		}
	}
	diffLines(t, h2Run(t, nil, sd9SipRust, "--release"), want.String())
}

// TestStdDefects_Sip13StateZeroFillRejected: Sip13State carries an
// invariant, so it is not zero-valid and Arena.Slice of one, which
// zero-fills (a state whose words are not the key's), is E0701.
func TestStdDefects_Sip13StateZeroFillRejected(t *testing.T) {
	t.Parallel()
	src := `import std.hash

export Main(sys: &System): WriteError|OutOfMemory!void {
    s := try sys.arena.Slice[hash.Sip13State](1)
    var t := s[0]
    t.Bytes("abc".AsBytes())
    _ = t.Sum()
}
`
	_, out, status := h2Try(t, nil, src)
	if strings.Contains(out, "no usable LLVM") {
		t.Skipf("needs the LLVM toolchain: %s", out)
	}
	if status == 0 || !strings.Contains(out, "E0701") || !strings.Contains(out, "not zero-valid") {
		t.Fatalf("Arena.Slice[hash.Sip13State] built (exit %d), want E0701 not zero-valid:\n%s", status, out)
	}
}
