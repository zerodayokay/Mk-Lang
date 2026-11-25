// Why: a diagnostic's JSON form is a public interface for editors and tools,
// so any change to it must show up in a diff.
// What: TestSnapshots and snapshotTriggers: the JSON diagnostic of a minimal
// trigger for every code this package emits, under testdata/diag/.
// How: compares against the golden files, rewritten only by
// go test ./internal/syntax -update.

package syntax

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"mk/internal/diag"
	"mk/internal/source"
)

var update = flag.Bool("update", false, "rewrite the golden files under testdata/")

// snapshotTriggers are one minimal trigger per emitted code; E0101 has one
// per family (lexical, expected-token, foreign form).
var snapshotTriggers = []struct{ name, src string }{
	{"E0101-lexical", "F() {\n    x := 0X1\n}\n"},
	{"E0101-expected", "F() {\n    x :=\n}\n"},
	{"E0101-foreign", "F() {\n    x++\n}\n"},
	{"E0101-statement-if-suffix", "F() {\n    if c { 1 } else { 2 }.F()\n}\n"},
	{"E0101-loop-pattern", "F() {\n    for (a, b) in &xs {\n    }\n}\n"},
	{"E0102", "F() {\n    type := 1\n}\n"},
	{"E0609", "F() {\n    g := |x| x\n}\n"},
	{"E1203", "F(a: &'a T) {\n}\n"},
	{"E1204", "async F() {\n}\n"},
	{"E1205", "fn add(a: int, b: int) -> int {\n    return a + b\n}\n"},
	{"E1206", "F() {\n    x := 1;\n}\n"},
	{"E1207", "F() {\n    let x = 1\n}\n"},
	{"E1208", "F() {\n    x := f()?\n}\n"},
	{"E1210", "F() {\n    throw E\n}\n"},
	{"E1211", "F() -> int {\n    return 1\n}\n"},
	{"E1212", "F(a: &mut T) {\n}\n"},
	{"E1213", "F() {\n    x := a::b()\n}\n"},
	{"E1214", "F(x: E) {\n    match x {\n        .A => 1,\n    }\n}\n"},
	{"E1215", "pub F() {\n}\n"},
	{"E1216", "F() {\n    while c {\n    }\n}\n"},
	{"E1217", "F() {\n    if let Some(x) = e {\n    }\n}\n"},
	{"E1219", "impl P {\n    Len(&self): int { return 0 }\n}\n"},
	{"E1220", "F() {\n    for i in 0..=n {\n    }\n}\n"},
	{"E1221", "F() {\n    println!(\"x\")\n}\n"},
	{"E1222", "F() {\n    y := n as f64\n}\n"},
	{"E1223", "F() {\n    assert x == 1\n}\n"},
}

func TestSnapshots(t *testing.T) {
	for _, c := range snapshotTriggers {
		t.Run(c.name, func(t *testing.T) {
			sm := source.NewSourceMap()
			src := sm.Add("t.mk", []byte(c.src))
			_, ds := ParseFile(src, source.NewInterner(), Options{})
			if len(ds) == 0 {
				t.Fatalf("no diagnostic")
			}
			got := diag.Order(ds, sm, diag.NoCap).WriteJSON(sm)
			path := filepath.Join("testdata", "diag", c.name+".golden")
			if *update {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, append(got, '\n'), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run with -update to create it)", err)
			}
			if string(want) != string(got)+"\n" {
				t.Errorf("snapshot differs:\ngot  %s\nwant %s", got, want)
			}
		})
	}
}
