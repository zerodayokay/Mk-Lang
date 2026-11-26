// Why: mem.Zeroed of an atomic word is a fresh word holding 0, so a table
// of atomics can be zeroed, while a unique type whose author owns an
// invariant must stay refused whatever its fields.
// What: TestZeroValidIdentity.
// How: a world with std.raw's and std.atomic's Atomic and a std.mem whose
// Zeroed is bodiless; each case is one module's diagnostics.

package check

import (
	"testing"

	"mk/internal/types"
)

func TestZeroValidIdentity(t *testing.T) {
	w := newWorld(t, true)
	if raw := w.add("std.raw", opt{std: true}, rawAtomicDecls); len(raw.diags()) > 0 {
		t.Fatalf("std.raw:\n%s", dump(w, raw.diags()))
	}
	w.c.known = w.rs.Known()
	if w.c.known.Atomic == types.NoItem {
		t.Fatal("Known.Atomic not set")
	}
	if at := w.add("std.atomic", opt{std: true}, stdAtomicSrc); len(at.diags()) > 0 {
		t.Fatalf("std.atomic:\n%s", dump(w, at.diags()))
	}
	if m := w.add("std.mem", opt{std: true}, "export Zeroed[T](): T\n"); len(m.diags()) > 0 {
		t.Fatalf("std.mem:\n%s", dump(w, m.diags()))
	}
	const imp = "import std.atomic\nimport std.mem\nimport std.raw\n\n"
	for i, tc := range []bodyCase{
		{"array of atomics", imp + "F(): [64]atomic.Atomic[u64] {\n\treturn mem.Zeroed[[64]atomic.Atomic[u64]]()\n}\n", ""},
		{"raw atomic pointer", imp + "F(): raw.Atomic[*u8] {\n\treturn mem.Zeroed[raw.Atomic[*u8]]()\n}\n", ""},
		{"struct unique through its atomics", imp + "struct S {\n\ta: atomic.Atomic[u32]\n\tn: int\n}\n\nF(): [4]S {\n\treturn mem.Zeroed[[4]S]()\n}\n", ""},
		{"record of atomics", imp + "F(): (a: atomic.Atomic[u8], b: int) {\n\treturn mem.Zeroed[(a: atomic.Atomic[u8], b: int)]()\n}\n", ""},
		{"an optional atomic", imp + "F(): ?atomic.Atomic[u8] {\n\treturn mem.Zeroed[?atomic.Atomic[u8]]()\n}\n", "E0701@6:9"},
		{"unsafe impl Share", imp + "struct S {\n\tw: u64\n}\n\nunsafe impl Share for S {}\n\nF(): S {\n\treturn mem.Zeroed[S]()\n}\n", "E0701@12:9"},
		{"unsafe impl Share over atomics", imp + "struct S {\n\tw: atomic.Atomic[u64]\n}\n\nunsafe impl Share for S {}\n\nF(): S {\n\treturn mem.Zeroed[S]()\n}\n", "E0701@12:9"},
		{"unsafe impl Send over atomics", imp + "struct S {\n\tw: atomic.Atomic[u64]\n}\n\nunsafe impl Send for S {}\n\nF(): S {\n\treturn mem.Zeroed[S]()\n}\n", "E0701@12:9"},
		{"error union of an atomic", imp + "struct S {\n\tr: OutOfMemory!atomic.Atomic[u8]\n}\n\nF(): [2]S {\n\treturn mem.Zeroed[[2]S]()\n}\n", ""},
		{"view struct of atomics", imp + "view struct S {\n\tw: atomic.Atomic[u64]\n}\n\nF(): S {\n\treturn mem.Zeroed[S]()\n}\n", "E0701@10:9"},
		{"Mutex by identity", imp + "F(): Mutex[int] {\n\treturn mem.Zeroed[Mutex[int]]()\n}\n", "E0701@6:9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := w.add("std.z"+string(rune('a'+i)), opt{std: true}, tc.src)
			var got []string
			for _, d := range append(b.diags(), b.bodyDiags(t)...) {
				if s := d.Code.String(); s != "E0208" && s != "E1101" {
					got = append(got, s+"@"+w.lc(d.At))
				}
			}
			if g := sortStrings(got); g != tc.want {
				t.Fatalf("diagnostics %q, want %q\n%s", g, tc.want, dump(w, append(b.diags(), b.bodyDiags(t)...)))
			}
		})
	}
}
