// Why: a packed record's bitfield whose aligned unit would run past the
// record's end must get accessors, under both bitfield layouts, and a
// signed bitfield of an enum whose fixed type is a typedef must read its
// sign.
// What: TestCImport_SpanBitfieldForms.
// How: translates one header for x86_64-linux (the GNU layout) and
// x86_64-windows (the MSVC layout) and checks the accessors' lines.

package cimport

import "testing"

const spanHeader = `#include <stdint.h>
typedef short myshort;
enum E : int16_t { EA = -3, EB = 1 };
enum F : myshort { FA = -2, FB = 1 };
#pragma pack(push, 1)
struct Z { char tag; short v; unsigned w : 20; };
struct S { char tag; short v; int s : 20; };
struct N { char tag; enum E e : 4; enum F f : 4; };
#pragma pack(pop)
`

// TestCImport_SpanBitfieldForms: Z's w and S's s are bytes 3 to 5, copied
// alone into a u64 and back (no 4-byte unit fits: GNU's record is 6
// bytes, MSVC's 7 with w in a unit at byte 3); s and N's e and f read
// sign-extended.
func TestCImport_SpanBitfieldForms(t *testing.T) {
	for _, target := range []string{"x86_64-linux", "x86_64-windows"} {
		res := translate(t, target, map[string]string{"z.h": spanHeader}, Import{Name: "z", Headers: []string{"z.h"}})
		text := res.Modules[0]
		dumpIf(t, target+"-z.mk", text)
		wantLines(t, text,
			"// C: bitfield w, 20 bits at bit 0 of its 3 bytes at byte 3",
			"export Z.W(&self): u32 {",
			"raw.Copy[u8](raw.PtrCast[u64, u8](raw.OfMut(mut b)), raw.Offset(raw.PtrCast[Z, u8](raw.Of(self)), 3), 3)",
			"return Trunc[u32]((b >> 0) & 0xfffff)",
			"export Z.SetW(mut self, v: u32) {",
			"p := raw.Offset(raw.PtrCast[Z, u8](raw.OfMut(self)), 3)",
			"b = (b & 0xfffffffffff00000) | ((u64(v) & 0xfffff) << 0)",
			"raw.Copy[u8](p, raw.PtrCast[u64, u8](raw.Of(&b)), 3)",
			"export S.S(&self): i32 {",
			"return Trunc[i32](Trunc[i64](b << 44) >> 44)",
			"export S.SetS(mut self, v: i32) {",
			"b = (b & 0xfffffffffff00000) | ((Trunc[u64](i64(v)) & 0xfffff) << 0)",
			"E(&self): i16 {",
			"F(&self): i16 {")
	}
}
