// Why: mk cimport shows what a package's C imports bind, so a user can see
// which declarations of a header made it into Mk.
// What: TestMkCImportCommand.
// How: writes a package with one C import, runs mk cimport twice and checks
// the view path, the bound count and the cached second run. Skips without
// clang.

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMkCImportCommand: mk cimport prints each C import's view path and
// how much of its headers it binds; a package with none says so.
func TestMkCImportCommand(t *testing.T) {
	if _, err := os.Stat(`C:\Program Files\LLVM\bin\clang.exe`); err != nil && os.Getenv("MK_LLVM") == "" {
		if _, err := exec.LookPath("clang"); err != nil {
			t.Skip("no clang")
		}
	}
	dir := t.TempDir()
	files := map[string]string{
		"pkg.mk": "const Package = pkg.Package(\n    name: \"ci\",\n    bins: [],\n    deps: [],\n    c_sources: [],\n" +
			"    c_imports: [pkg.CImport(name: \"k\", headers: [\"k.h\"], defines: [], macros: [], vendor: false)],\n" +
			"    asm_sources: [],\n    targets: [],\n    unsafe_modules: [],\n    links: [],\n)\n",
		"k.h":      "int k_one(void);\n#define K_TWO 2\n#define K_BAD { int x; }\n#define K_FN(x) ((x)+1)\n",
		"src/a.mk": "import c.k\n\nexport F(): i32 {\n    return k.KTwo\n}\n",
	}
	for name, text := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	st, out, errOut := mk("cimport", "--target", "x86_64-windows", dir)
	if st != 0 || !strings.HasPrefix(out, "c.k for x86_64-windows\n") || !strings.Contains(out, filepath.Join("gen", "x86_64-windows", "c", "k.mk")) ||
		!strings.Contains(out, "bound    2 of 3 declarations (66.7%)") {
		t.Errorf("mk cimport: status %d stdout %q stderr %q", st, out, errOut)
	}
	if st, out, _ := mk("cimport", "--target", "x86_64-windows", dir); st != 0 || !strings.HasPrefix(out, "c.k for x86_64-windows (cached)\n") {
		t.Errorf("second mk cimport: status %d stdout %q", st, out)
	}
	if st, out, errOut := mk("cimport", "--skipped", "--target", "x86_64-windows", dir); st != 0 ||
		!strings.Contains(out, "skipped  macro    K_BAD") || !strings.Contains(out, "macro is no constant") {
		t.Errorf("mk cimport --skipped: status %d stdout %q stderr %q", st, out, errOut)
	}
	for _, tc := range []struct {
		name, want string
	}{
		{"k_one", "is bound as `export extern k_one(): i32`"},
		{"K_BAD", "was skipped: macro is no constant"},
		{"K_FN", "function-like macro with no prototype"},
		{"MISSING_SYM", "not found in the package's C imports"},
	} {
		st, out, errOut := mk("cimport", "--why", tc.name, "--target", "x86_64-windows", dir)
		if st != 0 || !strings.Contains(out, tc.want) {
			t.Errorf("mk cimport --why %s: status %d stdout %q stderr %q; want %q", tc.name, st, out, errOut, tc.want)
		}
	}
}
