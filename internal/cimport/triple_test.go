// Why: a binding must share the linked C objects' record layouts.
// What: tests of record layout and predefined macros under the native target.
// How: translates records whose layouts change with the target triple or
// with a target's predefined macros, and checks their resulting sizes.

package cimport

import (
	"testing"

	"mk/internal/source"
)

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
		"// C: bitfield w, 20 bits at bit 0 of the u32 at byte 5",
		"// C: struct Q, 5 bytes, align 1: opaque, members through accessors",
		"// C: bitfield w, 20 bits at bit 0 of the u32 at byte 1",
		"// C: struct R, 5 bytes, align 1: opaque, members through accessors",
		"// C: bitfield w, 20 bits at bit 0 of the u32 at byte 1")
}

func TestCImport_NativeTargetMacros(t *testing.T) {
	const h = `struct R {
#if defined(__APPLE__) && __ENVIRONMENT_MAC_OS_X_VERSION_MIN_REQUIRED__ < 110000
    int old_macos[2];
#elif defined(__linux__) && defined(__NO_MATH_ERRNO__)
    int musl[2];
#else
    int expected;
#endif
};`
	for _, target := range []string{"x86_64-linux", "aarch64-linux", "x86_64-macos", "aarch64-macos"} {
		t.Run(target, func(t *testing.T) {
			res := translate(t, target, map[string]string{"r.h": h}, Import{Name: "r", Headers: []string{"r.h"}})
			wantLines(t, res.Modules[0], "// C: struct R, 4 bytes, align 4", "export expected: i32")
		})
	}
}

func TestCImport_WindowsClangTarget(t *testing.T) {
	const want = "x86_64-pc-windows-msvc19.33.0"
	req := Request{Target: "x86_64-windows", Triple: ParseTriple("x86_64-windows")}
	if got := baseArgs(req, "")[0]; got != "--target="+want {
		t.Errorf("target arg = %q, want --target=%s", got, want)
	}
	var id source.Hash
	key := Key(id, "", req)
	req.Triple = want
	if key != Key(id, "", req) {
		t.Error("the unversioned MSVC triple must key the effective triple")
	}
	req.Triple = "x86_64-pc-windows-msvc19.40.0"
	if key == Key(id, "", req) {
		t.Error("different MSVC versions share a cache key")
	}
}
