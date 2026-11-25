// Why: every error code has a snapshot test, so a change to a diagnostic's
// JSON form is seen and reviewed.
// What: the snapshot test for resolve's codes, one minimal trigger per code.
// How: compares each JSON diagnostic with testdata/*.golden; -update rewrites
// them, and a rewritten golden is read by hand before it is kept.

package resolve

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"mk/internal/diag"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden")

// snapshots are one minimal trigger per code resolve emits.
var snapshots = []struct {
	name    string
	prelude bool
	src     string
	lib     string // module lib, imported by the trigger
}{
	{name: "e0201", prelude: true, src: "F() { _ = Missing() }\n"},
	{name: "e0204", lib: "struct Hidden {}\n", src: "import lib\n\nF(h: lib.Hidden) { }\n"},
	{name: "e0206", src: "F(x: int) {\n    y := x\n    if x > 0 {\n        y := 2\n        _ = y\n    }\n    _ = y\n}\n"},
	{name: "e0207", prelude: true, src: "F(): ?int { return None }\n"},
	{name: "e0208", src: "F() {\n    x := 1\n}\n"},
	{name: "e1209", src: "F(): ?int { return null }\n"},
	{name: "e1221", prelude: true, src: "F(v: Vec[int]) { }\n"},
	{name: "e0101", src: "error E\nF(x: E!int) {\n    match x {\n        ok { }\n        _ { }\n    }\n}\n"},
}

func TestSnapshots(t *testing.T) {
	t.Parallel()
	for _, s := range snapshots {
		w := newWorld(t, s.prelude)
		if s.lib != "" {
			w.add("lib", opt{}, s.lib)
		}
		b := w.add("main", opt{}, s.src)
		rep := diag.Order(b.diags, w.sm, diag.NoCap)
		got := append(rep.WriteJSON(w.sm), '\n')
		path := filepath.Join("testdata", s.name+".golden")
		if *update {
			if err := os.MkdirAll("testdata", 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, got, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v (run go test -update)", s.name, err)
		}
		if string(want) != string(got) {
			t.Errorf("%s: snapshot differs\n got: %s\nwant: %s", s.name, got, want)
		}
	}
}
