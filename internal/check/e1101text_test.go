// Why: E1101's message for a call outside unsafe must name the call.
// What: TestE1101_CallTextNamesTheCall.
// How: checks calls of unsafe-only functions outside unsafe and reads the
// message built by concurrency.go's unsafeCall: it calls the item, never
// "the vm.PageSize needs".

package check

import (
	"testing"

	"mk/internal/diag"
)

// TestE1101_CallTextNamesTheCall: a call of raw.ReadUnaligned, of a naked
// function and of a bodiless extern outside `unsafe` each give E1101 whose
// message reads "calling raw.ReadUnaligned needs", "calling a naked
// function needs" and "the call of a bodiless extern needs" (conformance's
// E1101.json pins that one), where the first two read "the
// raw.ReadUnaligned needs" and "the naked-call needs".
func TestE1101_CallTextNamesTheCall(t *testing.T) {
	t.Parallel()
	const tail = " needs an `unsafe \"reason\" { }` block"
	for _, c := range []struct{ name, src, want string }{
		{"raw", "import std.raw\n\nF(p: *u32): u32 {\n\treturn raw.ReadUnaligned[u32](p)\n}\n", "calling raw.ReadUnaligned" + tail},
		{"raw store", "import std.raw\n\nF(p: *u32) {\n\traw.WriteUnaligned[u32](p, 1)\n}\n", "calling raw.WriteUnaligned" + tail},
		{"naked", "naked Halt() {\n\tunsafe \"r\" {\n\t\tasm(\"hlt\")\n\t}\n}\n\nF() {\n\tHalt()\n}\n", "calling a naked function" + tail},
		{"extern", "extern Ext(x: i32): i32\n\nF(): i32 {\n\treturn Ext(1)\n}\n", "the call of a bodiless extern" + tail},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t, true)
			w.add("std.raw", opt{std: true}, rawStub).wantClean(t)
			b := w.add("m", opt{}, c.src)
			var got []*diag.Diagnostic
			for _, d := range append(b.diags(), b.bodyDiags(t)...) {
				if d.Code == diag.E1101 {
					got = append(got, d)
				}
			}
			if len(got) != 1 || got[0].Message != c.want {
				t.Fatalf("want one E1101 %q, got:\n%s", c.want, dump(w, got))
			}
		})
	}
}
