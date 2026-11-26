// Why: on Windows the binding's record layouts must be the layouts of the C
// objects it links with, which clang compiles for the MSVC triple.
// What: TestCImport_WindowsLayoutTriple.
// How: translates records whose layout differs between the mingw-w64 and
// the MSVC triples for x86_64-windows and checks their sizes, alignments
// and bitfield units against clang's MSVC layout.

package cimport

import "testing"

// layoutHeader: under mingw-w64's triple A is 12 bytes with w at byte 8, R
// is 8 bytes with w at byte 4, and L is 32 bytes aligned to 16; under the
// MSVC triple A is 9 bytes with w at byte 5, R 5 bytes with w at byte 1,
// and L 16 bytes aligned to 8 (long double is double). Q is the same under
// both.
const layoutHeader = `struct L { char c; long double x; };
struct __attribute__((packed)) A { char c; struct { short s; char t; } in; unsigned w : 20; };
#pragma pack(push, 1)
struct Q { char c; unsigned w : 20; };
#pragma pack(pop)
struct __attribute__((packed)) R { char c; unsigned w : 20; };
`

// TestCImport_WindowsLayoutTriple: the x86_64-windows binding reads the
// headers for the triple the C sources and the shim compile for, and lays
// A, R and L out as that triple does.
func TestCImport_WindowsLayoutTriple(t *testing.T) {
	if got := ParseTriple("x86_64-windows"); got != "x86_64-pc-windows-msvc" {
		t.Errorf("ParseTriple(x86_64-windows) = %q, want the C objects' x86_64-pc-windows-msvc", got)
	}
	res := translate(t, "x86_64-windows", map[string]string{"l.h": layoutHeader}, Import{Name: "l", Headers: []string{"l.h"}})
	text := res.Modules[0]
	dumpIf(t, "x86_64-windows-l.mk", text)
	wantLines(t, text,
		"// C: struct L, 16 bytes, align 8: opaque, members through accessors",
		"// C: struct A, 9 bytes, align 1: opaque, members through accessors",
		"// C: bitfield w, 20 bits at bit 8 of the u32 at byte 4",
		"// C: struct Q, 5 bytes, align 1: opaque, members through accessors",
		"// C: bitfield w, 20 bits at bit 8 of the u32 at byte 0",
		"// C: struct R, 5 bytes, align 1: opaque, members through accessors",
		"// C: bitfield w, 20 bits at bit 8 of the u32 at byte 0")
}
