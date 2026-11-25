// Why: std.simd's vector paths are chosen at run time by CPU features, and a
// wrong vector step gives wrong answers only on some machines and lengths.
// What: TestStd_SimdMatchesGo (scans and text.ToUtf16 against Go models) and
// TestStd_SimdTestBlocks (std/simd/tests_scan.mk, dev and release).
// How: calls the public entry points, so the dispatch this machine takes runs,
// on every short length, random long ones, and adversarial texts.

package std

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"
)

// std10Wtf8 is the WTF-8 model: Go's UTF-8 decoding, with ED A0..BF xx a
// surrogate, and no lead surrogate's bytes just before a trail's.
func std10Wtf8(b []byte) bool {
	lead := false
	for i := 0; i < len(b); {
		if b[i] == 0xED && i+2 < len(b) && b[i+1] >= 0xA0 && b[i+1] <= 0xBF && b[i+2] >= 0x80 && b[i+2] <= 0xBF {
			trail := b[i+1] >= 0xB0
			if lead && trail {
				return false
			}
			lead = !trail
			i += 3
			continue
		}
		r, n := utf8.DecodeRune(b[i:])
		if r == utf8.RuneError && n <= 1 {
			return false
		}
		lead = false
		i += n
	}
	return true
}

func std10Utf16Len(b []byte) int {
	n := 0
	for _, r := range string(b) {
		n += utf16.RuneLen(r)
	}
	return n
}

// std10Utf16LenBytes models simd.Utf16LenBytes: on WTF-8, a decoder's
// count (a surrogate's three bytes are one unit); on other bytes, the
// count the routine defines, one unit per byte that is not a continuation
// byte and one more per byte from 0xF0 on.
func std10Utf16LenBytes(b []byte) int {
	n := 0
	if std10Wtf8(b) {
		for i := 0; i < len(b); {
			if b[i] == 0xED && i+2 < len(b) && b[i+1] >= 0xA0 {
				n++
				i += 3
				continue
			}
			r, w := utf8.DecodeRune(b[i:])
			n += utf16.RuneLen(r)
			i += w
		}
		return n
	}
	for _, x := range b {
		if x&0xC0 != 0x80 {
			n++
		}
		if x >= 0xF0 {
			n++
		}
	}
	return n
}

func std10SimdInputs() [][]byte {
	r := rand.New(rand.NewSource(98))
	pieces := [][]byte{{0x41}, {0xC3, 0xA9}, {0xE2, 0x82, 0xAC}, {0xF0, 0x9F, 0x98, 0x80}, {0xED, 0xA0, 0x80},
		{0xED, 0xB0, 0x80}, {0xED, 0x9F, 0xBF}, {0x80}, {0xF4, 0x90, 0x80, 0x80}, {0xE0, 0x80, 0x80}, {0x0A}}
	gen := func(kind, n int) []byte {
		b := make([]byte, 0, n+4)
		for len(b) < n {
			switch kind {
			case 0:
				b = append(b, byte(r.Intn(256)))
			case 1:
				b = append(b, byte(r.Intn(128)))
			default:
				p := r.Intn(len(pieces))
				if (p == 7 || p == 8 || p == 9) && r.Intn(64) != 0 {
					p = 0
				}
				b = append(b, pieces[p]...)
			}
		}
		b = b[:n]
		if kind == 1 && n > 0 && r.Intn(2) == 0 {
			b[r.Intn(n)] = 0x80 | byte(r.Intn(128))
		}
		return b
	}
	var in [][]byte
	for kind := 0; kind < 3; kind++ {
		for n := 0; n <= 300; n++ {
			in = append(in, gen(kind, n))
		}
		for k := 0; k < 1000; k++ {
			in = append(in, gen(kind, r.Intn(30000)))
		}
	}
	base := bytes.Repeat([]byte{'a'}, 512)
	for off := 0; off+6 <= 512; off += 1 {
		for _, ins := range [][]byte{{0xED, 0xA0, 0x80}, {0xED, 0xA0, 0x80, 0xED, 0xB0, 0x80}, {0xED, 0xB0, 0x80, 0xED, 0xA0, 0x80}, {0xF0, 0x9F, 0x98, 0x80}} {
			b := append([]byte(nil), base...)
			copy(b[off:], ins)
			in = append(in, b)
		}
	}
	for _, n := range []int{63, 64, 65, 255, 256, 257, 4096, 32000} {
		b := bytes.Repeat([]byte{'z'}, n)
		b[0] = 'q'
		in = append(in, b, bytes.Repeat([]byte{0xED}, n), bytes.Repeat([]byte{0xED, 0xA0, 0x80}, n/3+1))
	}
	// Every byte a continuation or a four-byte lead, long enough that a
	// byte counter would wrap if folded too late (an adversarial row of
	// this kind found the first fold bound).
	for _, k := range []int{4096, 8191, 7500} {
		in = append(in, bytes.Repeat([]byte{0xF0, 0x9F, 0x98, 0x80}, k), bytes.Repeat([]byte{0xE2, 0x82, 0xAC}, k))
	}
	return in
}

const std10SimdProgram = `import std.simd
import std.text

export Main(sys: &System): ReadError|WriteError|OutOfMemory!void {
    var w := sys.Stdout()
    var inp := sys.Stdin()
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    for true {
        lo := try inp.Line(&ar)
        line := lo else {
            break
        }
        b := try Unhex(&ar, line)
        var key: u8 = 0x61
        if b.Len() > 0 {
            key = b[b.Len() / 2]
        }
        at := if k := simd.LastIndexByte(b, key) { k } else { -1 }
        var units := -2
        var t16 := -2
        var utf8 := true
        s := str.From(b) else {
            utf8 = false
            ""
        }
        if utf8 {
            units = simd.Utf16Len(s)
            u := try text.ToUtf16(&ar, s)
            t16 = u.Len()
        }
        try Print(mut w, "{a} {l} {w} {v} {u} {t} {b}\n", (a: simd.IsAscii(b), l: at, w: simd.ValidWtf8(b), v: simd.ValidUtf8(b), u: units, t: t16, b: simd.Utf16LenBytes(b)))
        ar.Reset()
    }
    try w.Flush()
}

Unhex(a: &Arena, h: str): OutOfMemory![]u8 from a {
    b := h.AsBytes()
    out := try a.Slice[u8](b.Len() / 2)
    for i in 0..b.Len() / 2 {
        out[i] = Nib(b[2 * i]) * 16 + Nib(b[2 * i + 1])
    }
    return out[..]
}

Nib(c: u8): u8 {
    if c >= 0x61 {
        return c - 0x61 + 10
    }
    return c - 0x30
}
`

func std10SimdWant(b []byte) string {
	key := byte('a')
	if len(b) > 0 {
		key = b[len(b)/2]
	}
	ascii := true
	for _, x := range b {
		if x >= 0x80 {
			ascii = false
		}
	}
	units, t16 := -2, -2
	if utf8.Valid(b) {
		units = std10Utf16Len(b)
		t16 = len(utf16.Encode([]rune(string(b))))
	}
	return fmt.Sprintf("%t %d %t %t %d %d %d", ascii, bytes.LastIndexByte(b, key), std10Wtf8(b), utf8.Valid(b), units, t16, std10Utf16LenBytes(b))
}

// TestStd_SimdMatchesGo: through the public entry points, so the cpu
// dispatch this machine takes (avx2 from 64 bytes, avx512bw from 256 when
// Cpu() reports them) is what runs: simd.IsAscii, Utf16Len, Utf16LenBytes (on
// every input, WTF-8 or not), LastIndexByte and ValidWtf8 against Go models, dev and release, and text.ToUtf16 (whose sizing
// pass is simd.Utf16Len) against unicode/utf16. The bit-for-bit comparison of
// each vector width with its scalar form is std/simd/tests_scan.mk's, run by
// TestStd_SimdTestBlocks.
//
// Inputs: every length 0 to 300 of random bytes, of ASCII with one high byte,
// and of WTF-8 pieces; 3,000 random lengths up to 30,000 bytes of each (a
// line of hex stays under Reader.Line's 64 KiB); and adversarial texts (a lone
// or separated surrogate pair at every offset of a 512-byte ASCII run, the
// searched byte only at the first byte, 0xED everywhere).
func TestStd_SimdMatchesGo(t *testing.T) {
	in := std10SimdInputs()
	var feed bytes.Buffer
	for _, b := range in {
		fmt.Fprintf(&feed, "%x\n", b)
	}
	for _, prof := range []string{"dev", "release"} {
		t.Run(prof, func(t *testing.T) {
			var flags []string
			if prof == "release" {
				flags = append(flags, "--release")
			}
			exe := buildNative(t, nil, std10SimdProgram, flags...)
			inFile := filepath.Join(t.TempDir(), "in.txt")
			if err := os.WriteFile(inFile, feed.Bytes(), 0o644); err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(inFile)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			ctx, cancel := context.WithTimeout(context.Background(), std10RunTimeout)
			defer cancel()
			cmd := exec.CommandContext(ctx, exe)
			cmd.Stdin = f
			var out, errb bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &errb
			if err := cmd.Run(); err != nil {
				t.Fatalf("%v\n%s", err, errb.String())
			}
			got := strings.Split(strings.TrimSpace(out.String()), "\n")
			if len(got) != len(in) {
				t.Fatalf("%d answers for %d inputs", len(got), len(in))
			}
			bad := 0
			for i, b := range in {
				if want := std10SimdWant(b); strings.TrimRight(got[i], "\r") != want {
					bad++
					if bad <= 20 {
						t.Errorf("input %d (%d bytes, %.40x): got %q, want %q", i, len(b), b, got[i], want)
					}
				}
			}
			if bad > 0 {
				t.Fatalf("%d of %d inputs differ", bad, len(in))
			}
			t.Logf("%s: %d inputs equal to the Go models", prof, len(in))
		})
	}
}

// TestStd_SimdTestBlocks runs std/simd/tests_scan.mk's blocks,
// dev and release.
func TestStd_SimdTestBlocks(t *testing.T) {
	for _, prof := range []string{"dev", "release"} {
		t.Run(prof, func(t *testing.T) {
			root := t.TempDir()
			stdDir := copyStd(t, root, nil)
			args := []string{"test", "--std=std.simd", "-run", "string kernels:"}
			if prof == "release" {
				args = append(args, "--release")
			}
			out, status := mkCmd(t, root, stdDir, args...)
			if status != 0 || !strings.Contains(out, "6 passed, 0 failed") {
				t.Fatalf("mk test: exit %d\n%s", status, out)
			}
		})
	}
}
