// Why: a packed record's bitfield whose aligned unit would run past the
// record's end must get accessors, under both bitfield layouts, and a
// signed bitfield of an enum whose fixed type is a typedef must read its
// sign.
// What: TestCImport_SpanBitfieldForms and TestCImport_EnumBitfieldSign.
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

// TestCImport_SpanBitfieldForms: on the GNU layout Z's w and S's s are
// bytes 3 to 5 of a 6-byte record, copied alone into a u64 and back (no
// 4-byte unit fits); MSVC's record is 7 bytes with w in a unit at byte 3,
// which the accessors load and store at align 1, as C does. s and N's e
// and f read sign-extended.
func TestCImport_SpanBitfieldForms(t *testing.T) {
	res := translate(t, "x86_64-windows", map[string]string{"z.h": spanHeader}, Import{Name: "z", Headers: []string{"z.h"}})
	dumpIf(t, "x86_64-windows-z.mk", res.Modules[0])
	wantLines(t, res.Modules[0],
		"// C: bitfield w, 20 bits at bit 0 of the u32 at byte 3",
		"export Z.W(&self): u32 {",
		"return (raw.ReadUnaligned(raw.PtrCast[u8, u32](raw.Offset(raw.PtrCast[Z, u8](raw.Of(self)), 3))) >> 0) & 0xfffff",
		"export Z.SetW(mut self, v: u32) {",
		"raw.WriteUnaligned(p, (raw.ReadUnaligned(p) & 0xfff00000) | ((v & 0xfffff) << 0))",
		"export S.S(&self): i32 {",
		"return Trunc[i32](raw.ReadUnaligned(raw.PtrCast[u8, u32](raw.Offset(raw.PtrCast[S, u8](raw.Of(self)), 3))) << 12) >> 12",
		"E(&self): i16 {",
		"F(&self): i16 {")
	for _, target := range []string{"x86_64-linux"} {
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

// TestCImport_EnumBitfieldSign: a bitfield of an enum with no fixed type
// reads as C's type for that enum does: on the GNU layout unsigned when no
// constant is negative (G), signed when one is (H); on the MSVC target the
// enum is int, so both are signed.
func TestCImport_EnumBitfieldSign(t *testing.T) {
	const h = "enum G { GA = 1, GB = 2, GC = 3 };\nenum H { HA = -1, HB = 1 };\ntypedef enum G GT;\n" +
		"struct T { enum G g : 2; enum H h : 2; GT gt : 2; };\n"
	for target, want := range map[string][]string{
		"x86_64-linux":   {"T.G(&self): u32 {", "T.H(&self): i32 {", "T.Gt(&self): u32 {"},
		"x86_64-windows": {"T.G(&self): i32 {", "T.H(&self): i32 {", "T.Gt(&self): i32 {"},
	} {
		res := translate(t, target, map[string]string{"g.h": h}, Import{Name: "g", Headers: []string{"g.h"}})
		dumpIf(t, target+"-g.mk", res.Modules[0])
		wantLines(t, res.Modules[0], want...)
	}
}
