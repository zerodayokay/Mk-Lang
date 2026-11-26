// Why: a bitfield setter of an opaque record must store only bytes that C's
// own store to the field may write, never a byte of a neighbouring member.
// What: TestCImport_BitfieldUnitNeighbours.
// How: C sets each record's neighbours, Mk sets the bitfield, and C reads
// both back, at dev and release on the host's layout; the bytes each
// setter may store are pinned by internal/cimport's tests.

package driver

import (
	"os"
	"path/filepath"
	"testing"

	"mk/internal/types"
)

// bitunitHeader: P's wide and B's top follow a member whose byte the
// aligned unit of the field holds; R's b follows a zero-width bitfield,
// which ends a's run.
const bitunitHeader = `#pragma pack(push, 1)
struct P { unsigned char tag; int value; unsigned wide : 20; };
struct B { unsigned char c; signed top : 24; unsigned u : 7; };
struct R { unsigned char a : 4; unsigned char : 0; unsigned short b : 12; char c; };
#pragma pack(pop)
struct G { char c; int a : 12; };
void init(struct P *p, struct B *b, struct R *r, struct G *g);
long long get(struct P *p, struct B *b, struct R *r, struct G *g, int k);
`

func bitunitPackage() map[string]string {
	return map[string]string{
		"pkg.mk": manifest(map[string]string{"name": `"bu"`, "bins": `[pkg.Bin(name: "bu", root: "app", link_script: "", cpu: "", subsystem: "console")]`,
			"c_sources":      `[pkg.CSource(path: "u.c", flags: [])]`,
			"c_imports":      `[pkg.CImport(name: "u", headers: ["u.h"], defines: [], macros: [], vendor: false)]`,
			"unsafe_modules": `["app"]`}),
		"u.h": bitunitHeader,
		"u.c": `#include "u.h"
void init(struct P *p, struct B *b, struct R *r, struct G *g) {
    p->tag = 7; p->value = -123456789; b->c = 200; r->a = 5; r->c = -3; g->c = -100;
}
long long get(struct P *p, struct B *b, struct R *r, struct G *g, int k) {
    switch (k) {
    case 0: return p->tag; case 1: return p->value; case 2: return p->wide;
    case 3: return b->c; case 4: return b->top; case 5: return r->a; case 6: return r->b; case 7: return r->c;
    case 8: return g->c; default: return g->a;
    }
}
`,
		"app/main.mk": `import c.u
import std.mem
import std.raw

export Main(sys: &System): WriteError!void {
    var out := sys.Stdout()
    var p := mem.Zeroed[u.P]()
    var b := mem.Zeroed[u.B]()
    var r := mem.Zeroed[u.R]()
    var g := mem.Zeroed[u.G]()
    unsafe "u's functions are plain C" {
        u.init(raw.OfMut(mut p), raw.OfMut(mut b), raw.OfMut(mut r), raw.OfMut(mut g))
        p.SetWide(0xabcde)
        b.SetTop(-77)
        r.SetB(0xfff)
        g.SetA(-2000)
        for k in 0..10 {
            try Print(mut out, "{v} ", (v: u.get(raw.OfMut(mut p), raw.OfMut(mut b), raw.OfMut(mut r), raw.OfMut(mut g), Trunc[i32](k))))
        }
        try Print(mut out, "\n", ())
    }
}
`,
	}
}

// TestCImport_BitfieldUnitNeighbours: after C sets the neighbours and Mk
// sets each bitfield, C reads the neighbours unchanged and the fields as
// Mk set them.
func TestCImport_BitfieldUnitNeighbours(t *testing.T) {
	tools := needTools(t)
	dir := writeTree(t, bitunitPackage())
	cache := t.TempDir()
	const want = "7 -123456789 703710 200 -77 5 4095 -3 -100 -2000 \n"
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		t.Run(prof.String(), func(t *testing.T) {
			s, err := NewSession(Config{Root: dir, Profile: prof, Tools: tools, CacheDir: cache})
			if err != nil {
				t.Fatal(err)
			}
			r, paths, err := s.Build(filepath.Join(t.TempDir(), "bu"+exeSuffix(HostTarget())))
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
