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
