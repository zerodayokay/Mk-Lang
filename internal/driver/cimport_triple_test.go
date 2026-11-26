// Why: a C import's binding must lay records out as the C objects it links
// with do; on Windows those are compiled for the MSVC triple, whose packed
// bitfield records and long double differ from mingw-w64's.
// What: TestCImport_LayoutTripleBytes.
// How: a package whose C source reports sizeof and reads and writes the
// bitfields of packed records; the Mk side fills each record with 0xa5,
// sets a bitfield, and C reads it and its neighbours, then the reverse.

package driver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mk/internal/types"
)

// tripleHeader: A and R are the records whose layout differs between the
// mingw-w64 triple (A 12 bytes, w at byte 8; R 8 bytes, w at byte 4) and
// the MSVC triple (A 9 bytes, w at byte 5; R 5 bytes, w at byte 1); L is
// 32 bytes against 16.
const tripleHeader = `struct __attribute__((packed)) A { char c; struct { short s; char t; } in; unsigned w : 20; };
struct __attribute__((packed)) R { char c; unsigned w : 20; };
struct L { char c; long double x; };
long long a_get(struct A *a, int k);
void a_set(struct A *a);
long long r_get(struct R *r, int k);
void r_set(struct R *r);
long long sizes(void);
`

func triplePackage() map[string]string {
	return map[string]string{
		"pkg.mk": manifest(map[string]string{"name": `"lt"`, "bins": `[pkg.Bin(name: "lt", root: "app", link_script: "", cpu: "", subsystem: "console")]`,
			"c_sources":      `[pkg.CSource(path: "u.c", flags: [])]`,
			"c_imports":      `[pkg.CImport(name: "u", headers: ["u.h"], defines: [], macros: [], vendor: false)]`,
			"unsafe_modules": `["app"]`}),
		"u.h": tripleHeader,
		"u.c": `#include "u.h"
long long a_get(struct A *a, int k) {
    switch (k) { case 0: return a->c; case 1: return a->in.t; default: return a->w; }
}
void a_set(struct A *a) { a->w = 0x54321; }
long long r_get(struct R *r, int k) { return k == 0 ? r->c : r->w; }
void r_set(struct R *r) { r->w = 0x12345; }
long long sizes(void) { return (long long)(sizeof(struct A) * 10000 + sizeof(struct R) * 100 + sizeof(struct L)); }
`,
		"app/main.mk": `import c.u
import std.mem
import std.raw

export Main(sys: &System): WriteError!void {
    var out := sys.Stdout()
    var a := mem.Zeroed[u.A]()
    var r := mem.Zeroed[u.R]()
    unsafe "u's functions are plain C; each record is filled through its bytes" {
        raw.Fill(raw.PtrCast[u.A, u8](raw.OfMut(mut a)), 0xa5, SizeOf[u.A]())
        raw.Fill(raw.PtrCast[u.R, u8](raw.OfMut(mut r)), 0xa5, SizeOf[u.R]())
        try Print(mut out, "sizes {c} {m}\n", (c: u.sizes(), m: SizeOf[u.A]() * 10000 + SizeOf[u.R]() * 100 + SizeOf[u.L]()))
        a.SetW(0xfffff)
        r.SetW(0xabcde)
        ap := raw.OfMut(mut a)
        rp := raw.OfMut(mut r)
        try Print(mut out, "A {x} {y} {z}\n", (x: u.a_get(ap, 0), y: u.a_get(ap, 1), z: u.a_get(ap, 2)))
        try Print(mut out, "R {x} {y}\n", (x: u.r_get(rp, 0), y: u.r_get(rp, 1)))
        u.a_set(ap)
        u.r_set(rp)
        try Print(mut out, "A {x} {y}\n", (x: a.C(), y: a.W()))
        try Print(mut out, "R {x} {y}\n", (x: r.C(), y: r.W()))
    }
}
`,
	}
}

// TestCImport_LayoutTripleBytes: what Mk stores in A's and R's w, C reads,
// their neighbours untouched, and what C stores Mk reads; Mk's sizes of A,
// R and L are C's. At dev and release, on the host's layout.
func TestCImport_LayoutTripleBytes(t *testing.T) {
	tools := needTools(t)
	dir := writeTree(t, triplePackage())
	cache := t.TempDir()
	want := "A -91 -91 1048575\nR -91 703710\nA 165 344865\nR 165 74565\n"
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		t.Run(prof.String(), func(t *testing.T) {
			s, err := NewSession(Config{Root: dir, Profile: prof, Tools: tools, CacheDir: cache})
			if err != nil {
				t.Fatal(err)
			}
			r, paths, err := s.Build(filepath.Join(t.TempDir(), "lt"+exeSuffix(HostTarget())))
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Report.Diags) > 0 || len(paths) != 1 {
				view, _ := os.ReadFile(filepath.Join(dir, "gen", HostTarget().Name, "c", "u.mk"))
				t.Fatalf("build:\n%s\n%s", r.Report.Text(r.Sources), view)
			}
			out, errOut, code := runExe(t, HostTarget(), paths[0], "")
			first, rest, _ := strings.Cut(out, "\n")
			f := strings.Fields(first)
			// C's sizes and Mk's agree; on Windows they are MSVC's (A 9, R
			// 5, L 16), not mingw-w64's (12, 8, 32).
			sizesOK := len(f) == 3 && f[1] == f[2] && (HostTarget().OS != types.OSWindows || f[1] == "90516")
			if code != 0 || !sizesOK || rest != want {
				t.Errorf("exit %d, stdout %q, stderr %q; want sizes C = Mk, then %q", code, out, errOut, want)
			}
		})
	}
}
