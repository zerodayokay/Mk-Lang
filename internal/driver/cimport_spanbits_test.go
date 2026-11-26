// Why: a bitfield of a packed record whose aligned storage unit would run
// past the record's end, and a signed bitfield of an enum whose fixed type
// is a typedef, must be read and written from Mk as C lays them out.
// What: TestCImport_SpanBitfields (Mk sets, C reads, and the reverse, at
// dev and release, neighbours untouched) and TestCImport_SpanBitfieldsLinux
// (the GNU layout's binding checks whole).
// How: spanPackage writes a package whose C source reads and writes each
// field; the Mk side fills each record with 0xa5 first, so a store that
// clobbers a neighbour shows in C's reads.

package driver

import (
	"os"
	"path/filepath"
	"testing"

	"mk/internal/types"
)

// spanHeader: under pack(1), Z's 20-bit w starts at byte 3 of a 6-byte
// (GNU) or 7-byte (MSVC) record, so no 4-byte unit at an aligned offset
// holds it; S's s is the same, signed; N's e and f are signed bitfields of
// enums whose fixed types are typedefs.
const spanHeader = `#include <stdint.h>
typedef short myshort;
enum E : int16_t { EA = -3, EB = 1 };
enum F : myshort { FA = -2, FB = 1 };
#pragma pack(push, 1)
struct Z { char tag; short v; unsigned w : 20; };
struct S { char tag; short v; int s : 20; };
struct N { char tag; enum E e : 4; enum F f : 4; };
#pragma pack(pop)
long long z_get(struct Z *z, int k);
void z_set(struct Z *z);
long long s_get(struct S *s, int k);
void s_set(struct S *s);
long long n_get(struct N *n, int k);
void n_set(struct N *n);
`

func spanPackage() map[string]string {
	return map[string]string{
		"pkg.mk": manifest(map[string]string{"name": `"sb"`, "bins": `[pkg.Bin(name: "sb", root: "app", link_script: "", cpu: "", subsystem: "console")]`,
			"c_sources":      `[pkg.CSource(path: "u.c", flags: [])]`,
			"c_imports":      `[pkg.CImport(name: "u", headers: ["u.h"], defines: [], macros: [], vendor: false)]`,
			"unsafe_modules": `["app"]`}),
		"u.h": spanHeader,
		"u.c": `#include "u.h"
long long z_get(struct Z *z, int k) {
    switch (k) { case 0: return z->tag; case 1: return z->v; case 2: return z->w; default: return ((unsigned char *)z)[sizeof *z - 1]; }
}
void z_set(struct Z *z) { z->w = 0x54321; }
long long s_get(struct S *s, int k) {
    switch (k) { case 0: return s->tag; case 1: return s->v; case 2: return s->s; default: return ((unsigned char *)s)[sizeof *s - 1]; }
}
void s_set(struct S *s) { s->s = -524288; }
long long n_get(struct N *n, int k) {
    switch (k) { case 0: return n->tag; case 1: return n->e; default: return n->f; }
}
void n_set(struct N *n) { n->e = (enum E)-8; n->f = (enum F)7; }
`,
		"app/main.mk": `import c.u
import std.mem
import std.raw

export Main(sys: &System): WriteError!void {
    var out := sys.Stdout()
    var z := mem.Zeroed[u.Z]()
    var s := mem.Zeroed[u.S]()
    var n := mem.Zeroed[u.N]()
    unsafe "u's functions are plain C; each record is filled through its bytes" {
        raw.Fill(raw.PtrCast[u.Z, u8](raw.OfMut(mut z)), 0xa5, SizeOf[u.Z]())
        raw.Fill(raw.PtrCast[u.S, u8](raw.OfMut(mut s)), 0xa5, SizeOf[u.S]())
        raw.Fill(raw.PtrCast[u.N, u8](raw.OfMut(mut n)), 0xa5, SizeOf[u.N]())
        z.SetW(0x1fffff)
        s.SetS(-5)
        n.SetE(-3)
        n.SetF(-2)
        zp := raw.OfMut(mut z)
        sp := raw.OfMut(mut s)
        np := raw.OfMut(mut n)
        try Print(mut out, "Z {a} {b} {c}\n", (a: u.z_get(zp, 0), b: u.z_get(zp, 1), c: u.z_get(zp, 2)))
        try Print(mut out, "S {a} {b} {c}\n", (a: u.s_get(sp, 0), b: u.s_get(sp, 1), c: u.s_get(sp, 2)))
        try Print(mut out, "N {a} {b} {c}\n", (a: u.n_get(np, 0), b: u.n_get(np, 1), c: u.n_get(np, 2)))
        u.z_set(zp)
        u.s_set(sp)
        u.n_set(np)
        try Print(mut out, "Z {a} {b} {c}\n", (a: z.Tag(), b: z.V(), c: z.W()))
        try Print(mut out, "S {a} {b} {c}\n", (a: s.Tag(), b: s.V(), c: s.S()))
        try Print(mut out, "N {a} {b} {c}\n", (a: n.Tag(), b: n.E(), c: n.F()))
    }
}
`,
	}
}

// TestCImport_SpanBitfields: Z's w, S's s and N's e and f have a getter
// and a setter; what Mk stores C reads, neighbours untouched (an
// over-wide value cut to the field, a signed one sign-extended), and what
// C stores Mk reads, at dev and release on the host's layout.
func TestCImport_SpanBitfields(t *testing.T) {
	tools := needTools(t)
	dir := writeTree(t, spanPackage())
	cache := t.TempDir()
	want := "Z -91 -23131 1048575\nS -91 -23131 -5\nN -91 -3 -2\n" +
		"Z 165 -23131 344865\nS 165 -23131 -524288\nN 165 -8 7\n"
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		t.Run(prof.String(), func(t *testing.T) {
			s, err := NewSession(Config{Root: dir, Profile: prof, Tools: tools, CacheDir: cache})
			if err != nil {
				t.Fatal(err)
			}
			r, paths, err := s.Build(filepath.Join(t.TempDir(), "sb"+exeSuffix(HostTarget())))
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Report.Diags) > 0 || len(paths) != 1 {
				view, _ := os.ReadFile(filepath.Join(dir, "gen", HostTarget().Name, "c", "u.mk"))
				t.Fatalf("build:\n%s\n%s", r.Report.Text(r.Sources), view)
			}
			out, errOut, code := runExe(t, HostTarget(), paths[0], "")
			if code != 0 || out != want {
				t.Errorf("exit %d, stdout %q, stderr %q; want %q", code, out, errOut, want)
			}
		})
	}
}

// TestCImport_SpanBitfieldsLinux: the same header under the GNU layout
// (x86_64-linux) binds with every accessor checking clean, the whole
// binding checked.
func TestCImport_SpanBitfieldsLinux(t *testing.T) {
	tools := needTools(t)
	t.Setenv("MK_EVIDENCE", "")
	t.Setenv("MK_SYSROOT_x86_64-linux", t.TempDir())
	files := spanPackage()
	files["pkg.mk"] = manifest(map[string]string{"name": `"sb"`, "unsafe_modules": `["src"]`,
		"c_imports": `[pkg.CImport(name: "u", headers: ["u.h"], defines: [], macros: [], vendor: false)]`})
	delete(files, "app/main.mk")
	delete(files, "u.c")
	files["src/a.mk"] = "import c.u\n\nexport F(z: mut u.Z, s: mut u.S, n: mut u.N) {\n    z.SetW(z.W() + 1)\n    s.SetS(s.S() - 1)\n" +
		"    n.SetE(n.E())\n    n.SetF(n.F())\n}\n"
	dir := writeTree(t, files)
	cfg := Config{Root: dir, Target: types.X86_64Linux, Tools: tools, CacheDir: t.TempDir()}
	cfg.TestHooks.CImportWhole = true
	s, err := NewSession(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if r := s.Check(); len(r.Report.Diags) > 0 {
		view, _ := os.ReadFile(filepath.Join(dir, "gen", "x86_64-linux", "c", "u.mk"))
		t.Fatalf("check:\n%s\n%s", r.Report.Text(r.Sources), view)
	}
}
