// Why: a bitfield setter of an opaque record stores its whole unit, so the
// unit may hold no byte of another member: C's own store to the field
// writes none, and a store to that member made at the same time would be
// lost.
// What: tests of bitfield unit boundaries in opaque and transparent records.
// How: translates records whose aligned unit would start inside a
// neighbour, under the GNU (x86_64-linux) and MSVC (x86_64-windows)
// layouts, and checks every accessor's bytes against every member's, read
// back from the binding's comments and types.

package cimport

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// unitHeader: P's wide is bytes 5 to 7 after an int at bytes 1 to 4, whose
// aligned u32 is bytes 4 to 7; G's a follows a char in the u32 at byte 0;
// A's w follows a member whose last byte is its own padding; the rest are
// the bitfield records of the other importer tests.
const unitHeader = `#include <stdint.h>
typedef short myshort;
enum E : int16_t { EA = -3, EB = 1 };
enum F : myshort { FA = -2, FB = 1 };
enum G2 { GA = 1, GB = 2, GC = 3 };
struct T1 { int sb : 3; unsigned ub : 5; };
struct T2 { short x; signed char sc : 2; };
struct T3 { unsigned a : 3, b : 5; unsigned c; };
struct G { char c; int a : 12; };
#pragma pack(push, 1)
struct Z { char tag; short v; unsigned w : 20; };
struct S { char tag; short v; int s : 20; };
struct N { char tag; enum E e : 4; enum F f : 4; };
struct P { unsigned char tag; int value; unsigned wide : 20; };
struct B { unsigned char c; signed top : 24; unsigned u : 7; };
struct W { char c; unsigned long long x : 57; unsigned long long y : 7; };
struct V { char c; unsigned long long x : 60; };
struct K { char c; enum G2 g : 2; signed char sc : 3; short s : 9; };
struct M { char c; unsigned a : 4; unsigned long long x : 40; unsigned b : 12; };
struct Pt { unsigned char tag; int value; unsigned wide : 20; char after; };
#pragma pack(pop)
struct __attribute__((packed)) A { char c; struct { short s; char t; } in; unsigned w : 20; };
`

var (
	reRecord = regexp.MustCompile(`^// C: (?:struct|union) (\w+), (\d+) bytes`)
	reMember = regexp.MustCompile(`^// C: member (\w+), at byte (\d+)$`)
	reUnit   = regexp.MustCompile(`^// C: bitfield (\w+), \d+ bits at bit \d+ of the u(\d+) at byte (\d+)$`)
	reSpan   = regexp.MustCompile(`^// C: bitfield (\w+), \d+ bits at bit \d+ of its (\d+) bytes at byte (\d+)$`)
	reGetter = regexp.MustCompile(`^export \w+\.\w+\(&self\): (\w+) \{$`)
	reCopy   = regexp.MustCompile(`raw\.Copy\[u8\]\(.*, (\d+)\)$`)
)

// byteRange is [from, to) of one accessor or member.
type byteRange struct {
	name     string
	from, to int
}

// opaqueRanges reads, per opaque record of a binding, its members' byte
// ranges and its bitfield accessors' (the unit or the bytes copied).
func opaqueRanges(t *testing.T, text string) (members, bits map[string][]byteRange) {
	t.Helper()
	members, bits = map[string][]byteRange{}, map[string][]byteRange{}
	lines := strings.Split(text, "\n")
	rec := ""
	atoi := func(s string) int { n, _ := strconv.Atoi(s); return n }
	scalar := map[string]int{"u8": 1, "i8": 1, "bool": 1, "u16": 2, "i16": 2, "u32": 4, "i32": 4, "f32": 4, "u64": 8, "i64": 8, "f64": 8}
	for k, ln := range lines {
		if m := reRecord.FindStringSubmatch(ln); m != nil {
			rec = m[1]
			continue
		}
		if m := reMember.FindStringSubmatch(ln); m != nil {
			off, size := atoi(m[2]), 0
			if k+1 < len(lines) {
				if g := reGetter.FindStringSubmatch(lines[k+1]); g != nil {
					size = scalar[g[1]]
				}
			}
			for j := k + 1; size == 0 && j < len(lines) && j < k+8; j++ {
				if c := reCopy.FindStringSubmatch(strings.TrimSpace(lines[j])); c != nil {
					size = atoi(c[1])
				}
			}
			if size == 0 {
				t.Fatalf("%s.%s: no size read from the binding:\n%s", rec, m[1], strings.Join(lines[k:min(k+8, len(lines))], "\n"))
			}
			members[rec] = append(members[rec], byteRange{m[1], off, off + size})
			continue
		}
		if m := reUnit.FindStringSubmatch(ln); m != nil {
			off := atoi(m[3])
			bits[rec] = append(bits[rec], byteRange{m[1], off, off + atoi(m[2])/8})
		} else if m := reSpan.FindStringSubmatch(ln); m != nil {
			off := atoi(m[3])
			bits[rec] = append(bits[rec], byteRange{m[1], off, off + atoi(m[2])})
		}
	}
	return members, bits
}

// TestCImport_BitfieldUnitsOwnBytes: no bitfield accessor of an opaque
// record loads or stores a byte of a member that is not a bitfield, on
// either layout. Where the aligned unit would hold a neighbour's byte,
// P's and Pt's wide and A's w take a unit at their first byte on the MSVC
// layout (C's own unit) and their own 3 bytes on the GNU one, whose record
// ends sooner; G's a on the GNU layout takes the u16 at byte 1 (on the
// MSVC one G is a plain struct).
func TestCImport_BitfieldUnitsOwnBytes(t *testing.T) {
	for target, want := range map[string][]string{
		"x86_64-linux": {
			"// C: bitfield a, 12 bits at bit 0 of the u16 at byte 1",
			"// C: bitfield wide, 20 bits at bit 0 of its 3 bytes at byte 5",
			"// C: bitfield wide, 20 bits at bit 0 of its 3 bytes at byte 5",
			"// C: bitfield w, 20 bits at bit 0 of its 3 bytes at byte 5"},
		"x86_64-windows": {
			"// C: bitfield wide, 20 bits at bit 0 of the u32 at byte 5",
			"// C: bitfield wide, 20 bits at bit 0 of the u32 at byte 5",
			"// C: bitfield w, 20 bits at bit 0 of the u32 at byte 5"},
	} {
		res := translate(t, target, map[string]string{"u.h": unitHeader}, Import{Name: "u", Headers: []string{"u.h"}})
		text := res.Modules[0]
		dumpIf(t, target+"-units.mk", text)
		wantLines(t, text, want...)
		members, bits := opaqueRanges(t, text)
		n := 0
		for rec, bs := range bits {
			for _, b := range bs {
				n++
				for _, m := range members[rec] {
					if b.from < m.to && m.from < b.to {
						t.Errorf("%s: %s.%s's accessor holds bytes %d to %d, and member %s is bytes %d to %d",
							target, rec, b.name, b.from, b.to-1, m.name, m.from, m.to-1)
					}
				}
			}
		}
		if n < 15 {
			t.Errorf("%s: read %d bitfield accessors, want every one of the header's opaque records", target, n)
		}
	}
}

// TestCImport_BitfieldRunUnit: a zero-width bitfield ends a run, and the
// next run's unit holds no byte of the one before (C11 makes them two
// memory locations): R's b is bytes 1 and 2, its aligned u16 bytes 0 and 1.
func TestCImport_BitfieldRunUnit(t *testing.T) {
	const h = "#pragma pack(push, 1)\nstruct R { unsigned char a : 4; unsigned char : 0; unsigned short b : 12; char c; };\n#pragma pack(pop)\n"
	for _, target := range []string{"x86_64-linux", "x86_64-windows"} {
		res := translate(t, target, map[string]string{"r.h": h}, Import{Name: "r", Headers: []string{"r.h"}})
		dumpIf(t, target+"-runs.mk", res.Modules[0])
		text := res.Modules[0]
		_, bits := opaqueRanges(t, text)
		seen := false
		for _, b := range bits["R"] {
			if b.name == "b" {
				seen = true
				if b.from < 1 {
					t.Errorf("%s: R.b's accessor holds byte %d, which is a's (another run)\n%s", target, b.from, text)
				}
			}
		}
		if !seen {
			t.Errorf("%s: R.b has no accessor\n%s", target, text)
		}
	}
}

func TestCImport_BitfieldSeparateTransparentRuns(t *testing.T) {
	const h = `struct R { unsigned char a : 4; unsigned char : 0; unsigned char b : 4; };`
	for _, target := range []string{"x86_64-linux", "x86_64-windows"} {
		t.Run(target, func(t *testing.T) {
			res := translate(t, target, map[string]string{"r.h": h}, Import{Name: "r", Headers: []string{"r.h"}})
			wantLines(t, res.Modules[0], "export bits0: u8", "export bits1: u8",
				"// C: bitfield a, 4 bits at bit 0 of bits0", "// C: bitfield b, 4 bits at bit 0 of bits1")
		})
	}
}

func TestCImport_BitfieldLargeMemberRange(t *testing.T) {
	const n = 4095
	ch := &ctype{kind: tBuiltin, name: "char"}
	d := &decl{fields: []field{{t: &ctype{kind: tArray, elem: ch, n: n}, bits: -1}, {bits: 12}}}
	l := &recLayout{size: n + 2, fields: []fieldLayout{{width: -1}, {bit: n * 8, width: 12}}}
	e := &emitter{}
	if got := len(e.otherBytes(d, l, 1)); got != 1 {
		t.Errorf("neighbour occupies %d ranges, want one regardless of its byte size", got)
	}
}
