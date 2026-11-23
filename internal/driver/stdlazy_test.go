// Why: on-demand std loading must never miss a direct, aliased, transitive or
// implicit std dependency, nor leak unimported std modules into a plain build.
//
// What: TestScanStdImports and TestLazyStdPrunesUnimported.
//
// How: unit-tests scanStdImports on comments, strings, aliases and nested paths,
// and runs Session.Check on a minimal file verifying std.http/std.tls are not
// loaded while explicit std.json (and its transitive deps) and float % (std.math)
// are loaded and clean.

package driver

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestScanStdImports(t *testing.T) {
	src := `// import std.fake_comment
/* nested /* import std.fake_nested */ still comment import std.fake2 */
import std.json
import h = std.hash
import std.time.tzdata
export F(): str {
    return "import std.fake_string"
}
`
	got := scanStdImports([]byte(src))
	want := []string{"std.json", "std.hash", "std.time.tzdata"}
	if !slices.Equal(got, want) {
		t.Fatalf("scanStdImports = %v, want %v", got, want)
	}
}

func TestLazyStdPrunesUnimported(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "main.mk")
	src := "import std.hash\n\nexport Main(sys: &System): int {\n    _ = hash.Sha256(b\"hi\")\n    return 0\n}\n"
	if err := os.WriteFile(mainPath, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := NewSession(Config{Root: mainPath, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	res := s.Check()
	if len(res.Report.Diags) > 0 {
		t.Fatalf("unexpected diagnostics: %s", res.Report.Text(res.Sources))
	}
	has := func(p string) bool {
		for _, m := range s.stdB.mods {
			if m.path == p {
				return true
			}
		}
		return false
	}
	if !has("std.core") || !has("std.hash") {
		t.Fatalf("expected std.core and std.hash loaded")
	}
	for _, unimported := range []string{"std.http", "std.tls", "std.text", "std.regex"} {
		if has(unimported) {
			t.Errorf("unimported module %s should not be loaded in lazy std mode", unimported)
		}
	}
	for _, m := range s.stdB.mods {
		for _, f := range m.files {
			if isStdTestFile(f.rel) {
				t.Errorf("std test file %s should be skipped outside std testing", f.rel)
			}
		}
	}
}

func TestScanStdSourcePercent(t *testing.T) {
	noRem := `// 50% faster with +% -% *%
/* comment % */
export F(x: u64): u64 {
    var a := (x +% 1) *% 3 -% 2
    a +%= 4
    a -%= 1
    a *%= 5
    return a
}
`
	if _, rem := scanStdSource([]byte(noRem)); rem {
		t.Fatalf("wrapping ops and comment %% should not trigger hasRem")
	}
	for _, tc := range []string{
		"export F(x: f64): f64 { return x % 2.0 }\n",
		"export F(x: f64): f64 { var a := x\n a %= 2.0\n return a }\n",
		"export F(): str { return \"x % y\" }\n",
	} {
		if _, rem := scanStdSource([]byte(tc)); !rem {
			t.Fatalf("expected hasRem=true for %q", tc)
		}
	}
}

func TestLazyStdWrappingSkipsMath(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "main.mk")
	src := "export Main(sys: &System): int {\n    var x: u64 = 7\n    x = (x +% 3) *% 5 -% 1\n    return 0\n}\n"
	if err := os.WriteFile(mainPath, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := NewSession(Config{Root: mainPath, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	res := s.Check()
	if len(res.Report.Diags) > 0 {
		t.Fatalf("unexpected diagnostics: %s", res.Report.Text(res.Sources))
	}
	for _, m := range s.stdB.mods {
		if m.path == "std.math" {
			t.Fatalf("std.math should not be loaded for wrapping arithmetic alone")
		}
	}

	// Verify float % still loads std.math and checks cleanly.
	remPath := filepath.Join(dir, "rem.mk")
	remSrc := "export Main(sys: &System): int {\n    _ = 5.5 % 2.0\n    return 0\n}\n"
	if err := os.WriteFile(remPath, []byte(remSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	s2, err := NewSession(Config{Root: remPath, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	res2 := s2.Check()
	if len(res2.Report.Diags) > 0 {
		t.Fatalf("float %% unexpected diagnostics: %s", res2.Report.Text(res2.Sources))
	}
	hasMath := false
	for _, m := range s2.stdB.mods {
		if m.path == "std.math" {
			hasMath = true
		}
	}
	if !hasMath {
		t.Fatalf("expected std.math to be loaded for float %%")
	}
}
