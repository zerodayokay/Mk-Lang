// Why: a C import binds a packed record with a member off its natural
// alignment, or a bitfield whose unit is wider than the record's
// alignment, as an opaque struct with accessors, and Mk code must be able
// to write those members as C lays them out (MKH-913).
// What: TestCImport_UnalignedAccessors (Mk sets, C reads, and the
// reverse, at dev and release) and TestCImport_UnalignedAccessorsLinux
// (the GNU layout's binding checks whole).
// How: unalignedPackage writes a package whose C source reads and writes
// each member; the Mk side fills each record with 0xa5 first, so a store
// that clobbers a neighbour shows in C's reads.

package driver

import (
	"os"
	"path/filepath"
	"testing"

	"mk/internal/types"
)

// unalignedHeader: under pack(1), P's value is at byte 1 and the u32 unit
// of wide (byte 4) overlaps value's top byte; B's signed top ends at the
// top bit of its u32 unit; Q holds a float, a double, a pointer and,
// at even offsets of an align 1 record, a struct and an array of align 2.
// Q's bitfield w only makes Q opaque (its unit straddles an 8-byte line,
// so it has no accessors).
const unalignedHeader = `struct In { short a; char b; };
#pragma pack(push, 1)
struct P { unsigned char tag; int value; unsigned wide : 20; };
struct B { unsigned char c; signed top : 24; unsigned u : 7; };
struct Q { unsigned char tag; float f; double d; int *ptr; char pad; struct In in; short arr[2]; long long ll; unsigned w : 20; };
#pragma pack(pop)
long long p_get(struct P *p, int k);
void p_set(struct P *p);
long long b_get(struct B *b, int k);
void b_set(struct B *b);
long long q_get(struct Q *q, int k);
void q_set(struct Q *q);
`

func unalignedPackage() map[string]string {
	return map[string]string{
		"pkg.mk": manifest(map[string]string{"name": `"ua"`, "bins": `[pkg.Bin(name: "ua", root: "app", link_script: "", cpu: "", subsystem: "console")]`,
			"c_sources":      `[pkg.CSource(path: "u.c", flags: [])]`,
			"c_imports":      `[pkg.CImport(name: "u", headers: ["u.h"], defines: [], macros: [], vendor: false)]`,
			"unsafe_modules": `["app"]`}),
		"u.h": unalignedHeader,
		"u.c": `#include "u.h"
static int ninety_nine = 99;
long long p_get(struct P *p, int k) {
    switch (k) { case 0: return p->tag; case 1: return p->value; default: return p->wide; }
}
void p_set(struct P *p) { p->value = 2000000000; p->wide = 0x54321; }
long long b_get(struct B *b, int k) {
    switch (k) { case 0: return b->c; case 1: return b->top; default: return b->u; }
}
void b_set(struct B *b) { b->top = -8388608; b->u = 9; }
long long q_get(struct Q *q, int k) {
    switch (k) {
    case 0: return q->tag;
    case 1: return (long long)(q->f * 100);
    case 2: return (long long)(q->d * 100);
    case 3: return *q->ptr;
    case 4: return q->pad;
    case 5: return q->in.a * 1000 + q->in.b;
    case 6: return q->arr[0] * 1000 + q->arr[1];
    default: return q->ll;
    }
}
void q_set(struct Q *q) {
    q->f = 0.25f; q->d = -8.5; q->ptr = &ninety_nine; q->in.a = -11; q->in.b = 12; q->arr[0] = 13; q->arr[1] = -14;
    q->ll = 1LL << 50;
}
`,
		"app/main.mk": `import c.u
import std.mem
import std.raw

export Main(sys: &System): WriteError!void {
    var out := sys.Stdout()
    var p := mem.Zeroed[u.P]()
    var b := mem.Zeroed[u.B]()
    var q := mem.Zeroed[u.Q]()
    var n: i32 = 7
    unsafe "u's functions are plain C; each record is filled through its bytes" {
        raw.Fill(raw.PtrCast[u.P, u8](raw.OfMut(mut p)), 0xa5, SizeOf[u.P]())
        raw.Fill(raw.PtrCast[u.B, u8](raw.OfMut(mut b)), 0xa5, SizeOf[u.B]())
        raw.Fill(raw.PtrCast[u.Q, u8](raw.OfMut(mut q)), 0xa5, SizeOf[u.Q]())
        p.SetValue(-123456)
        p.SetWide(0x1fffff)
        b.SetTop(-5)
        b.SetU(0xff)
        q.SetF(1.5)
        q.SetD(-2.25)
        q.SetPtr(raw.OfMut(mut n))
        q.SetIn(u.In(a: 3, b: 4))
        q.SetArr([5, -6])
        q.SetLl(-1099511627776)
        pp := raw.OfMut(mut p)
        bp := raw.OfMut(mut b)
        qp := raw.OfMut(mut q)
        try Print(mut out, "P {a} {b} {c}\n", (a: u.p_get(pp, 0), b: u.p_get(pp, 1), c: u.p_get(pp, 2)))
        try Print(mut out, "B {a} {b} {c}\n", (a: u.b_get(bp, 0), b: u.b_get(bp, 1), c: u.b_get(bp, 2)))
        try Print(mut out, "Q {a} {b} {c} {d} {e} {f} {g} {h}\n", (a: u.q_get(qp, 0), b: u.q_get(qp, 1), c: u.q_get(qp, 2),
            d: u.q_get(qp, 3), e: u.q_get(qp, 4), f: u.q_get(qp, 5), g: u.q_get(qp, 6), h: u.q_get(qp, 7)))
        u.p_set(pp)
        u.b_set(bp)
        u.q_set(qp)
        i := q.In()
        r := q.Arr()
        try Print(mut out, "P {a} {b} {c}\n", (a: p.Tag(), b: p.Value(), c: p.Wide()))
        try Print(mut out, "B {a} {b} {c}\n", (a: b.C(), b: b.Top(), c: b.U()))
        try Print(mut out, "Q {a} {b} {c} {d} {e} {f} {g} {h} {k} {l}\n", (a: q.Tag(), b: q.F(), c: q.D(), d: raw.Read(q.Ptr()),
            e: q.Pad(), f: i.a, g: i.b, h: r[0], k: r[1], l: q.Ll()))
    }
}
`,
	}
}

// TestCImport_UnalignedAccessors: every member of P, B and Q has a setter;
// what Mk stores C reads, neighbours untouched (a signed bitfield
// sign-extended, an over-wide value cut to the field), and what C stores
// Mk reads, at dev and release on the host's layout.
func TestCImport_UnalignedAccessors(t *testing.T) {
	tools := needTools(t)
	dir := writeTree(t, unalignedPackage())
	cache := t.TempDir()
	want := "P 165 -123456 1048575\nB 165 -5 127\nQ 165 150 -225 7 -91 3004 4994 -1099511627776\n" +
		"P 165 2000000000 344865\nB 165 -8388608 9\nQ 165 0.25 -8.5 99 165 -11 12 13 -14 1125899906842624\n"
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		t.Run(prof.String(), func(t *testing.T) {
			s, err := NewSession(Config{Root: dir, Profile: prof, Tools: tools, CacheDir: cache})
			if err != nil {
				t.Fatal(err)
			}
			r, paths, err := s.Build(filepath.Join(t.TempDir(), "ua"+exeSuffix(HostTarget())))
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

// TestCImport_UnalignedAccessorsLinux: the same header under the GNU
// layout (x86_64-linux) binds with every accessor checking clean, the
// whole binding checked, not only the items the package uses.
func TestCImport_UnalignedAccessorsLinux(t *testing.T) {
	tools := needTools(t)
	t.Setenv("MK_EVIDENCE", "")
	t.Setenv("MK_SYSROOT_x86_64-linux", t.TempDir())
	files := unalignedPackage()
	files["pkg.mk"] = manifest(map[string]string{"name": `"ua"`, "unsafe_modules": `["src"]`,
		"c_imports": `[pkg.CImport(name: "u", headers: ["u.h"], defines: [], macros: [], vendor: false)]`})
	delete(files, "app/main.mk")
	delete(files, "u.c")
	files["src/a.mk"] = "import c.u\n\nexport F(p: mut u.P, b: mut u.B, q: mut u.Q) {\n    p.SetValue(p.Value() + 1)\n    p.SetWide(p.Wide())\n" +
		"    b.SetTop(b.Top())\n    q.SetF(q.F())\n    q.SetD(q.D())\n    q.SetPtr(q.Ptr())\n    q.SetIn(q.In())\n    q.SetArr(q.Arr())\n}\n"
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
